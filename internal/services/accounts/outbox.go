package accounts

import (
	"context"
	"errors"
	"sync"
)

// Outbox is a Mailer that keeps the messages instead of sending them. For
// dev mode and tests only: the messages hold sign-in codes.
type Outbox struct {
	mu   sync.Mutex
	sent []Mail
	// Fail makes Send fail, as a mail system that is down does.
	Fail bool
}

// maxOutbox bounds what a long dev session keeps.
const maxOutbox = 100

// Send implements Mailer.
func (o *Outbox) Send(_ context.Context, m Mail) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Fail {
		return errors.New("outbox: told to fail, with " + m.To)
	}
	if o.sent = append(o.sent, m); len(o.sent) > maxOutbox {
		o.sent = o.sent[len(o.sent)-maxOutbox:]
	}
	return nil
}

// Sent returns the messages kept, oldest first.
func (o *Outbox) Sent() []Mail {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Mail(nil), o.sent...)
}
