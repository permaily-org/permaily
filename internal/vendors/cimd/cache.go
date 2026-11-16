package cimd

import (
	"container/list"
	"time"

	"github.com/permaily-org/permaily/internal/vendors"
)

// entry is a document or a key set as fetched and checked, with what its
// answer said of caching it, when the answer that last fetched or
// revalidated it was received and how old that answer was then.
type entry struct {
	key       string
	value     any // *oauth.ClientDocument or []vendors.Key
	policy    vendors.CachePolicy
	validated time.Time
	age       time.Duration // the corrected initial age at validated
	etag      string
	modified  string
}

// currentAge is e's age at now (RFC 9111 section 4.2.3): its age when it
// was received plus the time it has been kept since.
func (e *entry) currentAge(now time.Time) time.Duration {
	return capAge(capAge(e.age) + capAge(now.Sub(e.validated)))
}

// lru holds at most max entries, the least recently used dropped first.
type lru struct {
	max   int
	order *list.List // of *entry, the most recently used first
	byKey map[string]*list.Element
}

func newLRU(max int) *lru {
	return &lru{max: max, order: list.New(), byKey: map[string]*list.Element{}}
}

// get is the entry of key, now the most recently used; nil when not held.
func (c *lru) get(key string) *entry {
	el, ok := c.byKey[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry)
}

// put keeps e under its key, in place of what was there.
func (c *lru) put(e *entry) {
	if el, ok := c.byKey[e.key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.byKey[e.key] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.byKey, last.Value.(*entry).key)
	}
}

// remove drops key.
func (c *lru) remove(key string) {
	if el, ok := c.byKey[key]; ok {
		c.order.Remove(el)
		delete(c.byKey, key)
	}
}

// len is how many entries are held.
func (c *lru) len() int { return c.order.Len() }

// refreshes remembers, of at most max key-set URLs, when each was last
// fetched again for a kid it did not have, so that it is at most once every
// KeysRefreshEvery. A URL is remembered only when its fetch is admitted or
// already in flight; one remembered is never forgotten within its minute,
// so past max a new refresh is refused rather than one remembered dropped.
type refreshes struct {
	max   int
	order *list.List // of *refresh, the oldest first
	byURL map[string]*list.Element
}

type refresh struct {
	uri string
	at  time.Time
}

func newRefreshes(max int) *refreshes {
	return &refreshes{max: max, order: list.New(), byURL: map[string]*list.Element{}}
}

// may says uri may be fetched again at now: it was not within
// KeysRefreshEvery, and there is room to remember it.
func (r *refreshes) may(uri string, now time.Time) bool {
	for el := r.order.Front(); el != nil && now.Sub(el.Value.(*refresh).at) >= KeysRefreshEvery; el = r.order.Front() {
		r.order.Remove(el)
		delete(r.byURL, el.Value.(*refresh).uri)
	}
	if _, ok := r.byURL[uri]; ok {
		return false
	}
	return len(r.byURL) < r.max
}

// mark remembers that uri is fetched again at now; may said it could be.
func (r *refreshes) mark(uri string, now time.Time) {
	if el, ok := r.byURL[uri]; ok {
		el.Value.(*refresh).at = now
		r.order.MoveToBack(el)
		return
	}
	r.byURL[uri] = r.order.PushBack(&refresh{uri: uri, at: now})
}
