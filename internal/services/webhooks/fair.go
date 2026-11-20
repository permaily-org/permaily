package webhooks

import (
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// item is a connection whose entry is due, held ready to be worked on: its
// host and account (its fair share), and the due entry it was found by.
type item struct {
	connection string
	host       string
	account    string
	due        store.Entry
	// reserved is when its attempt was reserved from the budgets (zero
	// while it waits).
	reserved time.Time
}

// scheduler is the fair share of one enclave's calls (ADR 0039, "Fair
// shares"): bounded queues of ready items, one per host (HostReadyMax each,
// ReadyMax together), taken in turn across hosts and, within a host, across
// accounts, under the enclave's budgets (InFlightMax, AttemptsPerMinute) and
// each host's (HostInFlightMax, HostAttemptsPerMinute). What does not fit
// stays in the due index, never dropped, and is admitted by a later scan:
// when every place is taken, a host with nothing ready takes the newest
// place of the host holding the most, so a host found due is never kept out
// by others' backlogs. It decides nothing and holds no lock of its own: the
// service holds its lock around every call.
type scheduler struct {
	hosts    map[string]*hostQueue
	order    []string // the hosts in turn
	next     int      // the next host's place in order
	total    int      // items ready, every host together
	queued   map[string]bool
	inFlight int
	starts   []time.Time // attempts reserved within the last minute
}

type hostQueue struct {
	accounts map[string][]item
	order    []string // the accounts in turn
	next     int
	n        int // items ready
	inFlight int
	starts   []time.Time
}

func newScheduler() *scheduler {
	return &scheduler{hosts: map[string]*hostQueue{}, queued: map[string]bool{}}
}

// admit holds it ready unless it is already, its host's queue is full, or
// every place is taken and its host already has one: then it stays due.
func (q *scheduler) admit(it item) bool {
	if q.queued[it.connection] {
		return false
	}
	h := q.hosts[it.host]
	if h == nil {
		h = &hostQueue{accounts: map[string][]item{}}
		q.hosts[it.host] = h
		q.order = append(q.order, it.host)
	}
	if h.n >= webhook.HostReadyMax {
		return false
	}
	if q.total >= webhook.ReadyMax && (h.n > 0 || !q.evict()) {
		return false
	}
	if len(h.accounts[it.account]) == 0 {
		h.order = append(h.order, it.account)
	}
	h.accounts[it.account] = append(h.accounts[it.account], it)
	h.n++
	q.total++
	q.queued[it.connection] = true
	return true
}

// evict gives up the newest ready item of the host holding the most, of
// its account holding the most, when that host holds more than one: the
// item stays due and is admitted again by a later scan.
func (q *scheduler) evict() bool {
	var victim *hostQueue
	for _, name := range q.order {
		if h := q.hosts[name]; victim == nil || h.n > victim.n {
			victim = h
		}
	}
	if victim.n < 2 {
		return false
	}
	at := 0
	for i, a := range victim.order {
		if len(victim.accounts[a]) > len(victim.accounts[victim.order[at]]) {
			at = i
		}
	}
	account := victim.order[at]
	items := victim.accounts[account]
	gone := items[len(items)-1]
	if victim.accounts[account] = items[:len(items)-1]; len(items) == 1 {
		delete(victim.accounts, account)
		victim.order = slices.Delete(victim.order, at, at+1)
		if victim.next > at {
			victim.next--
		}
	}
	victim.n--
	q.total--
	delete(q.queued, gone.connection)
	return true
}

// within keeps the times of the last minute.
func within(starts []time.Time, now time.Time) []time.Time {
	return slices.DeleteFunc(starts, func(t time.Time) bool { return !now.Before(t.Add(time.Minute)) })
}

// pick takes the next item in turn whose host has a slot and budget left,
// reserving its attempt from every budget; none while the enclave's own
// are spent. Hosts with nothing ready, running or reserved leave the turn.
func (q *scheduler) pick(now time.Time) (item, bool) {
	q.starts = within(q.starts, now)
	for i := 0; i < len(q.order); {
		h := q.hosts[q.order[i]]
		if h.starts = within(h.starts, now); h.n == 0 && h.inFlight == 0 && len(h.starts) == 0 {
			delete(q.hosts, q.order[i])
			q.order = slices.Delete(q.order, i, i+1)
			if q.next > i {
				q.next--
			}
			continue
		}
		i++
	}
	if q.inFlight >= webhook.InFlightMax || len(q.starts) >= webhook.AttemptsPerMinute {
		return item{}, false
	}
	for i := range q.order {
		at := (q.next + i) % len(q.order)
		h := q.hosts[q.order[at]]
		if h.n == 0 || h.inFlight >= webhook.HostInFlightMax || len(h.starts) >= webhook.HostAttemptsPerMinute {
			continue
		}
		q.next = at + 1
		it := h.take()
		it.reserved = now
		h.inFlight++
		h.starts = append(h.starts, now)
		q.inFlight++
		q.starts = append(q.starts, now)
		q.total--
		return it, true
	}
	return item{}, false
}

// take is the host's next item, its accounts in turn.
func (h *hostQueue) take() item {
	at := h.next % len(h.order)
	account := h.order[at]
	items := h.accounts[account]
	it := items[0]
	h.n--
	if len(items) == 1 {
		delete(h.accounts, account)
		h.order = slices.Delete(h.order, at, at+1)
		h.next = at
	} else {
		h.accounts[account] = items[1:]
		h.next = at + 1
	}
	return it
}

// done ends an item picked: its slot is free, and its reservation given
// back when no call was made.
func (q *scheduler) done(it item, called bool) {
	h := q.hosts[it.host]
	h.inFlight--
	q.inFlight--
	delete(q.queued, it.connection)
	if !called {
		h.starts = unreserve(h.starts, it.reserved)
		q.starts = unreserve(q.starts, it.reserved)
	}
}

func unreserve(starts []time.Time, at time.Time) []time.Time {
	if i := slices.IndexFunc(starts, at.Equal); i >= 0 {
		return slices.Delete(starts, i, i+1)
	}
	return starts
}

// ready is how many items are ready: every host's, and the most of one.
func (q *scheduler) ready() (total, most int) {
	for _, h := range q.hosts {
		most = max(most, h.n)
	}
	return q.total, most
}
