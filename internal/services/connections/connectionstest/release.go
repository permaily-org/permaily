package connectionstest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/permaily-org/permaily/internal/services/connections"
)

// Lease is a connections.Release a test drives (ADR 0030, "Release",
// revised after the second design review, N2): Cut stands for a revocation
// in this enclave, or the authority read again while the answer is sent
// finding it ended; TooLate makes Start refuse. It records what the
// adapter did, so the routes' and MCP's tests (pull requests 6 and 7) put
// their barriers after the service returned and before the first byte, and
// during the transfer, with their real adapters.
type Lease struct {
	// TooLate, when set, is Start's answer: the authority read again
	// refused the answer.
	TooLate bool

	mu       sync.Mutex
	cut      bool
	started  bool
	finished bool
	sent     bool
	tooLate  int
	cancel   context.CancelFunc
}

var _ connections.Release = (*Lease)(nil)

// errStarted is a second Start.
var errStarted = errors.New("connectionstest: the lease was started already")

// Start implements connections.Release.
func (l *Lease) Start(ctx context.Context) (context.Context, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.started || l.finished:
		return nil, errStarted
	case l.cut || l.TooLate:
		l.tooLate++
		return nil, connections.ErrTooLate
	}
	l.started = true
	c, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	return c, nil
}

// Finish implements connections.Release.
func (l *Lease) Finish(sent bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return
	}
	l.finished, l.sent = true, sent && l.started
	if l.cancel != nil {
		l.cancel()
	}
}

// Cut is a revocation: a lease not started refuses to start, and a started
// one's context ends.
func (l *Lease) Cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cut = true
	if l.cancel != nil {
		l.cancel()
	}
}

// State is what the lease saw: started, finished, every byte sent, and how
// many too_late receipts it wrote.
func (l *Lease) State() (started, finished, sent bool, tooLate int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.started, l.finished, l.sent, l.tooLate
}

// ReleaseContract is what every connections.Release keeps; make returns a
// new lease and the revocation that cuts it. The service's lease (pull
// request 3) runs it, with its real revocation, beside V59.
func ReleaseContract(t *testing.T, make func() (connections.Release, func())) {
	t.Helper()
	ctx := context.Background()
	t.Run("cut before the first byte: never starts, too late", func(t *testing.T) {
		r, cut := make()
		cut()
		if _, err := r.Start(ctx); !errors.Is(err, connections.ErrTooLate) {
			t.Fatalf("started after a revocation: %v", err)
		}
		r.Finish(false)
	})
	t.Run("cut during the transfer: its context ends", func(t *testing.T) {
		r, cut := make()
		c, err := r.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c.Err() != nil {
			t.Fatal("ended before the cut")
		}
		cut()
		<-c.Done()
		r.Finish(false)
	})
	t.Run("started once", func(t *testing.T) {
		r, _ := make()
		if _, err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Start(ctx); err == nil {
			t.Fatal("started twice")
		}
		r.Finish(true)
	})
	t.Run("finished, its context ends", func(t *testing.T) {
		r, _ := make()
		c, err := r.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r.Finish(true)
		<-c.Done()
		r.Finish(true)
	})
	t.Run("the request's own end ends it", func(t *testing.T) {
		r, _ := make()
		parent, cancel := context.WithCancel(ctx)
		c, err := r.Start(parent)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		<-c.Done()
		r.Finish(false)
	})
}
