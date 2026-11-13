package httpapi

import "net/http"

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
