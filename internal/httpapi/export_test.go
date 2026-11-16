package httpapi

import (
	"context"
	"net"
	"net/http"
	"time"
)

// APIState is what the tests read of the API's own state: the render slots
// running, the bytes of the read budget held and the takes of it waiting.
type APIState struct{ a *mailboxesAPI }

// MailboxesAPIWithState is MailboxesAPI, with its state for the tests.
func MailboxesAPIWithState(cfg MailboxesConfig) (http.Handler, APIState) {
	a, h := newMailboxesAPI(cfg)
	return h, APIState{a}
}

// Running is how many renders hold a slot.
func (s APIState) Running() int {
	s.a.slots.mu.Lock()
	defer s.a.slots.mu.Unlock()
	return s.a.slots.active
}

// Held is the bytes of the read budget held, and Waiting how many takes of
// it wait for room.
func (s APIState) Held() (held int64, waiting int) {
	s.a.budget.mu.Lock()
	defer s.a.budget.mu.Unlock()
	return s.a.budget.total - s.a.budget.free, s.a.budget.waiting
}

// Documents is how many finished documents wait for their readers.
func (s APIState) Documents() int {
	s.a.mu.Lock()
	defer s.a.mu.Unlock()
	n := 0
	for _, w := range s.a.waiting {
		n += w
	}
	return n
}

// Transfer is the API's transfer of an answer under ctx, the lease's (req
// is the request's), for the tests.
func Transfer(ctx, req context.Context, w http.ResponseWriter, conn net.Conn, body []byte) (sent bool, ended string) {
	return transfer(ctx, req, w, conn, body)
}

// Hooked is h whose requests carry a connection, as ConnContext keeps it,
// when the server kept none: for the tests that serve in memory, where a
// route that sends mail is otherwise refused. The abort's deadlines and
// close act on nothing there.
func Hooked(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, kept := r.Context().Value(connKey{}).(net.Conn); !kept {
			r = r.WithContext(ConnContext(r.Context(), memoryConn{}))
		}
		h.ServeHTTP(w, r)
	})
}

type memoryConn struct{ net.Conn }

func (memoryConn) SetDeadline(time.Time) error { return nil }
func (memoryConn) Close() error                { return nil }
