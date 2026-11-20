// Package webhookstest is the stand-in of the connections a webhooks
// service is built against (ADR 0039's pull request 3) while
// services/connections builds the real ones (pull request 2): Fake, and
// Contract with its World, which both pass. The played vendor and the
// scripted sender are internal/webhook/webhooktest's. Never imported outside
// tests (depguard).
package webhookstest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// Message is a message of a fake connection's mailboxes: where it is, when
// it was received, and whether a read of the connection would give it under
// the account's rules as they were made (Leaves: true on every formula,
// held by no rule; a rule HoldAll saves holds it anyway).
type Message struct {
	Mailbox  string
	ID       string
	Received time.Time
	Leaves   bool
}

func (m Message) at() webhooks.Cursor {
	return webhooks.Cursor{Received: m.Received, Mailbox: m.Mailbox, Message: m.ID}
}

// Fake is a webhooks.Connections over connections a test makes and changes.
// Each connection reads as its record would in the one enclave the fake
// stands for: changes are seen at once, and every stop cuts the
// connection's permits before it returns.
type Fake struct {
	Now func() time.Time

	mu        sync.Mutex
	conns     map[string]*fakeConn
	model     map[string]bool // accounts whose model cannot be asked
	rules     map[string]*fakeRules
	stopped   []string
	evaluated map[string]map[webhooks.Cursor]int
}

type fakeRules struct {
	version    uint64
	holdAll    bool
	unreadable bool
}

type fakeConn struct {
	sub      webhooks.Subscription
	version  uint64
	active   bool
	devices  int
	endsAt   time.Time
	closing  bool
	removed  bool
	messages []Message
	permits  map[*permit]struct{}
}

var _ webhooks.Connections = (*Fake)(nil)

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fake) init() {
	if f.conns == nil {
		f.conns, f.model, f.rules, f.evaluated = map[string]*fakeConn{}, map[string]bool{}, map[string]*fakeRules{}, map[string]map[webhooks.Cursor]int{}
	}
}

func (f *Fake) rulesOf(account string) *fakeRules {
	r := f.rules[account]
	if r == nil {
		r = &fakeRules{}
		f.rules[account] = r
	}
	return r
}

// Make adds an active connection with one device, ending in 90 days.
func (f *Fake) Make(s webhooks.Subscription) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if s.State == "" {
		s.State = contracts.WebhookOn
	}
	f.conns[s.Connection] = &fakeConn{sub: s, version: 1, active: true, devices: 1, endsAt: f.now().Add(90 * 24 * time.Hour), permits: map[*permit]struct{}{}}
}

func (f *Fake) with(connection string, do func(c *fakeConn)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if c, ok := f.conns[connection]; ok {
		do(c)
	}
}

// stop is a change that stops the connection's calls: its permits are cut
// first, then its record written.
func (c *fakeConn) stop(change func()) {
	for p := range c.permits {
		p.cut()
	}
	change()
	c.version++
}

// Revoke ends the connection; RevokeDevices ends every device and keeps it;
// Expire moves its end to now; Close starts its account's deletion; Remove
// removes its record; Off turns the webhook off (the person). Each but
// Expire is a write that cuts the connection's permits before it returns;
// the end is checked by every read, and bounds every permit's WriteBy.
func (f *Fake) Revoke(connection string) {
	f.with(connection, func(c *fakeConn) { c.stop(func() { c.active = false }) })
}
func (f *Fake) RevokeDevices(connection string) {
	f.with(connection, func(c *fakeConn) { c.stop(func() { c.devices = 0 }) })
}
func (f *Fake) Expire(connection string) {
	f.with(connection, func(c *fakeConn) { c.endsAt = f.now() })
}
func (f *Fake) Close(connection string) {
	f.with(connection, func(c *fakeConn) { c.stop(func() { c.closing = true }) })
}
func (f *Fake) Remove(connection string) {
	f.with(connection, func(c *fakeConn) { c.stop(func() { c.removed = true }) })
}
func (f *Fake) Off(connection string) {
	f.with(connection, func(c *fakeConn) { c.stop(func() { c.sub.State = contracts.WebhookOff }) })
}

// DropMailbox takes the connection's last mailbox out of it (the person
// changed what it reaches: its record's version moves): its messages are
// no longer the connection's.
func (f *Fake) DropMailbox(connection string) {
	f.with(connection, func(c *fakeConn) {
		n := len(c.sub.Mailboxes)
		if n < 2 {
			return
		}
		gone := c.sub.Mailboxes[n-1]
		c.sub.Mailboxes = slices.Clone(c.sub.Mailboxes[:n-1])
		c.messages = slices.DeleteFunc(c.messages, func(m Message) bool { return m.Mailbox == gone })
		c.version++
	})
}

// ModelOff and ModelOn say whether the model can be asked for an account.
func (f *Fake) ModelOff(account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.model[account] = true
}
func (f *Fake) ModelOn(account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	delete(f.model, account)
}

// HoldAll saves, for the account, a rule that holds every message: its
// rules' version moves (ADR 0040).
func (f *Fake) HoldAll(account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	r := f.rulesOf(account)
	r.version++
	r.holdAll = true
}

// RulesUnreadable says whether the account's rules can be read
// (accountrules.ErrUnavailable).
func (f *Fake) RulesUnreadable(account string, unreadable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.rulesOf(account).unreadable = unreadable
}

// Deliver puts a message in one of the connection's mailboxes (its first
// when m.Mailbox is empty).
func (f *Fake) Deliver(connection string, m Message) {
	f.with(connection, func(c *fakeConn) {
		if m.Mailbox == "" && len(c.sub.Mailboxes) > 0 {
			m.Mailbox = c.sub.Mailboxes[0]
		}
		c.messages = append(c.messages, m)
	})
}

// Delete deletes a message from its mailbox.
func (f *Fake) Delete(connection string, at webhooks.Cursor) {
	f.with(connection, func(c *fakeConn) {
		c.messages = slices.DeleteFunc(c.messages, func(m Message) bool { return m.at() == at })
	})
}

// Stopped are the connections whose vendor asked to stop, in order.
func (f *Fake) Stopped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stopped)
}

// Evaluated says how often each of the connection's messages was
// evaluated: what a bound on reprocessing is checked against.
func (f *Fake) Evaluated(connection string) map[webhooks.Cursor]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	out := map[webhooks.Cursor]int{}
	for k, v := range f.evaluated[connection] {
		out[k] = v
	}
	return out
}

// read is a strict read of the connection: nil when it has no webhook.
func (f *Fake) read(connection string) (*fakeConn, *webhooks.Subscription) {
	c, ok := f.conns[connection]
	if !ok || c.removed {
		return nil, nil
	}
	s := c.sub
	s.Mailboxes = slices.Clone(s.Mailboxes)
	now := f.now()
	r := f.rulesOf(s.Account)
	s.ReadAt = now
	s.Basis = webhooks.Basis{Connection: c.version, RulesVersion: r.version}
	s.Delivery = contracts.DeliveryOn
	if f.model[s.Account] {
		s.Delivery = contracts.DeliveryModelUnavailable
	}
	switch {
	case !c.active || !now.Before(c.endsAt) || c.closing:
		s.Why = webhooks.SkipEnded
	case c.devices == 0:
		s.Why = webhooks.SkipNoDevice
	case s.State != contracts.WebhookOn:
		s.Why = webhooks.SkipOff
	case s.Delivery != contracts.DeliveryOn:
		s.Why = webhooks.SkipModel
	case r.unreadable:
		s.Why = webhooks.SkipRules
	}
	s.Calls = s.Why == ""
	return c, &s
}

// Subscription implements webhooks.Connections.
func (f *Fake) Subscription(_ context.Context, connection string) (*webhooks.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if _, s := f.read(connection); s != nil {
		return s, nil
	}
	return nil, webhooks.ErrNoWebhook
}

// Admit implements webhooks.Connections.
func (f *Fake) Admit(_ context.Context, connection string) (*webhooks.Subscription, webhooks.Permit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, s := f.read(connection)
	if s == nil {
		return nil, nil, webhooks.ErrNoWebhook
	}
	if !s.Calls {
		return s, nil, nil
	}
	writeBy := s.ReadAt.Add(oauth.ConnectionFresh)
	if c.endsAt.Before(writeBy) {
		writeBy = c.endsAt
	}
	p := &permit{f: f, c: c, writeBy: writeBy}
	c.permits[p] = struct{}{}
	return s, p, nil
}

// Pending implements webhooks.Connections: the late sweep first (messages
// within webhook.ArrivalLag before the cursor, at or after after.LateFrom,
// not in after.Recent), then forward from the cursor, in Cursor order, at
// most webhook.ArrivalPage in all.
func (f *Fake) Pending(_ context.Context, connection string, after webhooks.Progress) (*webhooks.Evaluation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, s := f.read(connection)
	if s == nil {
		return nil, webhooks.ErrNoWebhook
	}
	if f.model[s.Account] {
		return nil, contracts.ErrModelUnavailable
	}
	r := f.rulesOf(s.Account)
	if r.unreadable {
		return nil, webhooks.ErrRulesUnavailable
	}
	msgs := slices.Clone(c.messages)
	slices.SortFunc(msgs, func(a, b Message) int { return a.at().Compare(b.at()) })
	lateFrom := after.Cursor.Received.Add(-webhook.ArrivalLag)
	if after.LateFrom.After(lateFrom) {
		lateFrom = after.LateFrom
	}
	var late, forward []Message
	for _, m := range msgs {
		switch at := m.at(); {
		case at.Compare(after.Cursor) > 0:
			forward = append(forward, m)
		case !m.Received.Before(lateFrom) && !slices.Contains(after.Recent, at):
			late = append(late, m)
		}
	}
	todo := append(late, forward...)
	e := &webhooks.Evaluation{Through: webhooks.Progress{Cursor: after.Cursor, Recent: slices.Clone(after.Recent), LateFrom: after.LateFrom}}
	if len(todo) > webhook.ArrivalPage {
		todo, e.More = todo[:webhook.ArrivalPage], true
	}
	if f.evaluated[connection] == nil {
		f.evaluated[connection] = map[webhooks.Cursor]int{}
	}
	var leaves []webhooks.Cursor
	for _, m := range todo {
		at := m.at()
		f.evaluated[connection][at]++
		if m.Leaves && !r.holdAll {
			leaves = append(leaves, at)
		}
		if at.Compare(e.Through.Cursor) > 0 {
			e.Through.Cursor = at
		}
		e.Through.Recent = append(e.Through.Recent, at)
	}
	if len(leaves) > 0 {
		slices.SortFunc(leaves, webhooks.Cursor.Compare)
		if len(leaves) > webhook.MatchMessagesMax {
			leaves = leaves[:webhook.MatchMessagesMax]
		}
		e.Match = &webhooks.Match{Basis: s.Basis, From: after, Messages: leaves}
	}
	e.Through.Recent, e.Through.LateFrom = webhooks.Window(e.Through.Cursor, e.Through.Recent, e.Through.LateFrom)
	return e, nil
}

// Recheck implements webhooks.Connections.
func (f *Fake) Recheck(_ context.Context, connection string, m webhooks.Match) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, s := f.read(connection)
	if s == nil {
		return false, webhooks.ErrNoWebhook
	}
	if f.rulesOf(s.Account).unreadable {
		return false, webhooks.ErrRulesUnavailable
	}
	if s.Basis != m.Basis {
		return false, nil
	}
	for _, msg := range c.messages {
		if slices.Contains(m.Messages, msg.at()) {
			return true, nil
		}
	}
	return false, nil
}

// Webhooks implements webhooks.Connections.
func (f *Fake) Webhooks(_ context.Context, after string, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	var ids []string
	for id, c := range f.conns {
		if !c.removed && id > after {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// Stop implements webhooks.Connections.
func (f *Fake) Stop(_ context.Context, connection string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, ok := f.conns[connection]
	if !ok || c.removed {
		return webhooks.ErrNoWebhook
	}
	c.stop(func() { c.sub.State = contracts.WebhookStopped })
	f.stopped = append(f.stopped, connection)
	return nil
}

// permit implements webhooks.Permit for the fake.
type permit struct {
	f       *Fake
	c       *fakeConn
	writeBy time.Time

	mu       sync.Mutex
	isCut    bool
	started  bool
	finished bool
	cancel   context.CancelCauseFunc
}

func (p *permit) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isCut = true
	if p.cancel != nil {
		p.cancel(webhook.ErrCut)
	}
}

// Start implements webhooks.Permit. The fake's clock is played, so WriteBy
// is checked when it starts; the real one also ends the context at it.
func (p *permit) Start(ctx context.Context) (context.Context, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isCut || p.started || p.finished || p.f.now().After(p.writeBy) {
		return nil, webhook.ErrCut
	}
	c, cancel := context.WithCancelCause(ctx)
	p.started, p.cancel = true, cancel
	return c, nil
}

// WriteBy implements webhooks.Permit.
func (p *permit) WriteBy() time.Time { return p.writeBy }

// Finish implements webhooks.Permit.
func (p *permit) Finish() {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return
	}
	p.finished = true
	if p.cancel != nil {
		p.cancel(context.Canceled)
	}
	p.mu.Unlock()
	p.f.mu.Lock()
	delete(p.c.permits, p)
	p.f.mu.Unlock()
}

// Permits are how many of the connection's permits are registered: none
// once every call finished.
func (f *Fake) Permits(connection string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if c, ok := f.conns[connection]; ok {
		return len(c.permits)
	}
	return 0
}

// Fresh says whether a subscription's read is recent enough for a call to
// start now (oauth.ConnectionFresh).
func Fresh(s *webhooks.Subscription, now time.Time) bool {
	return !now.After(s.ReadAt.Add(oauth.ConnectionFresh))
}
