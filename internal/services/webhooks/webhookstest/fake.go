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

// Message is a message of a fake connection's mailboxes: when it was
// received, and whether a read of the connection would give it (Leaves:
// true on every formula, held by no rule).
type Message struct {
	ID       string
	Received time.Time
	Leaves   bool
}

// Fake is a webhooks.Connections over connections a test makes and changes.
// Each connection reads as its record would in the one enclave the fake
// stands for: changes are seen at once.
type Fake struct {
	Now func() time.Time

	mu      sync.Mutex
	conns   map[string]*fakeConn
	model   map[string]bool // accounts whose model cannot be asked
	stopped []string
}

type fakeConn struct {
	sub      webhooks.Subscription
	active   bool
	devices  int
	endsAt   time.Time
	closing  bool
	removed  bool
	messages []Message
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
		f.conns, f.model = map[string]*fakeConn{}, map[string]bool{}
	}
}

// Make adds an active connection with one device, ending in 90 days.
func (f *Fake) Make(s webhooks.Subscription) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if s.State == "" {
		s.State = contracts.WebhookOn
	}
	f.conns[s.Connection] = &fakeConn{sub: s, active: true, devices: 1, endsAt: f.now().Add(90 * 24 * time.Hour)}
}

func (f *Fake) with(connection string, do func(c *fakeConn)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if c, ok := f.conns[connection]; ok {
		do(c)
	}
}

// Revoke ends the connection; RevokeDevices ends every device and keeps it;
// Expire moves its end to now; Close starts its account's deletion; Remove
// removes its record; Off turns the webhook off (the person).
func (f *Fake) Revoke(connection string) { f.with(connection, func(c *fakeConn) { c.active = false }) }
func (f *Fake) RevokeDevices(connection string) {
	f.with(connection, func(c *fakeConn) { c.devices = 0 })
}
func (f *Fake) Expire(connection string) {
	f.with(connection, func(c *fakeConn) { c.endsAt = f.now() })
}
func (f *Fake) Close(connection string)  { f.with(connection, func(c *fakeConn) { c.closing = true }) }
func (f *Fake) Remove(connection string) { f.with(connection, func(c *fakeConn) { c.removed = true }) }
func (f *Fake) Off(connection string) {
	f.with(connection, func(c *fakeConn) { c.sub.State = contracts.WebhookOff })
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

// Deliver puts a message in the connection's mailboxes.
func (f *Fake) Deliver(connection string, m Message) {
	f.with(connection, func(c *fakeConn) { c.messages = append(c.messages, m) })
}

// Stopped are the connections whose vendor asked to stop, in order.
func (f *Fake) Stopped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stopped)
}

// Subscription implements webhooks.Connections.
func (f *Fake) Subscription(_ context.Context, connection string) (*webhooks.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, ok := f.conns[connection]
	if !ok || c.removed {
		return nil, webhooks.ErrNoWebhook
	}
	s := c.sub
	now := f.now()
	s.ReadAt = now
	s.Delivery = contracts.DeliveryOn
	if f.model[s.Account] {
		s.Delivery = contracts.DeliveryModelUnavailable
	}
	s.Calls = c.active && now.Before(c.endsAt) && c.devices > 0 && !c.closing &&
		s.State == contracts.WebhookOn && s.Delivery == contracts.DeliveryOn
	return &s, nil
}

// Pending implements webhooks.Connections: the messages received after the
// watermark, less ArrivalLag, that it has not seen, oldest first, at most
// webhook.ArrivalPage.
func (f *Fake) Pending(_ context.Context, connection string, after webhooks.Watermark) (*webhooks.Evaluation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, ok := f.conns[connection]
	if !ok || c.removed {
		return nil, webhooks.ErrNoWebhook
	}
	if f.model[c.sub.Account] {
		return nil, contracts.ErrModelUnavailable
	}
	from := after.Received.Add(-webhook.ArrivalLag)
	var todo []Message
	for _, m := range c.messages {
		if !m.Received.Before(from) && !slices.Contains(after.Seen, m.ID) {
			todo = append(todo, m)
		}
	}
	slices.SortFunc(todo, func(a, b Message) int { return a.Received.Compare(b.Received) })
	e := &webhooks.Evaluation{Through: webhooks.Watermark{Received: after.Received, Seen: slices.Clone(after.Seen)}}
	if len(todo) > webhook.ArrivalPage {
		todo, e.More = todo[:webhook.ArrivalPage], true
	}
	for _, m := range todo {
		if m.Leaves && (e.Since.IsZero() || m.Received.Before(e.Since)) {
			e.Leaves, e.Since = true, m.Received
		}
		if m.Received.After(e.Through.Received) {
			e.Through.Received = m.Received
		}
		e.Through.Seen = append(e.Through.Seen, m.ID)
	}
	// Keep only what the next evaluation looks at again, newest last.
	lag := e.Through.Received.Add(-webhook.ArrivalLag)
	e.Through.Seen = slices.DeleteFunc(e.Through.Seen, func(id string) bool {
		for _, m := range c.messages {
			if m.ID == id {
				return m.Received.Before(lag)
			}
		}
		return true
	})
	if n := len(e.Through.Seen); n > webhooks.SeenMax {
		e.Through.Seen = e.Through.Seen[n-webhooks.SeenMax:]
	}
	return e, nil
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
	c.sub.State = contracts.WebhookStopped
	f.stopped = append(f.stopped, connection)
	return nil
}

// Fresh says whether a subscription's read is recent enough for a call to
// start now (oauth.ConnectionFresh).
func Fresh(s *webhooks.Subscription, now time.Time) bool {
	return !now.After(s.ReadAt.Add(oauth.ConnectionFresh))
}
