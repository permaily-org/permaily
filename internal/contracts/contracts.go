// Package contracts holds the types that cross the service's boundary: what
// the API, MCP, the command-line tools and the web app send and receive. They
// are defined once, here, so every adapter says the same thing.
//
// It imports nothing of the service: every other layer may import it.
package contracts

import (
	"fmt"
	"time"
)

// Error is a failure a caller can act on. Code is stable and part of the
// contract; Message is for a person and may change.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Is makes errors.Is match two errors with the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// The failures of accounts and sign-in. Use errors.Is.
var (
	ErrInvalidAddress    = &Error{"invalid_address", "that is not an email address we can use"}
	ErrInvalidInvitation = &Error{"invalid_invitation", "that invitation is not valid, or was used already"}
	// ErrInvalidCode covers a wrong code, one that ended and one that was
	// used: whoever is guessing learns nothing from which.
	ErrInvalidCode     = &Error{"invalid_code", "that code or link is not valid any more; ask for a new one"}
	ErrTooManyRequests = &Error{"too_many_requests", "too many attempts; wait a while and try again"}
	ErrUnauthorized    = &Error{"unauthorized", "sign in to continue"}
	ErrNotFound        = &Error{"not_found", "not found"}
)

// AccountState says whether an account can be used.
type AccountState string

const (
	// AccountActive is an account in use.
	AccountActive AccountState = "active"
)

// Account is what a person sees of their own account.
type Account struct {
	ID      string       `json:"id"`
	State   AccountState `json:"state"`
	Address string       `json:"address"`
	Name    string       `json:"name"`
	// Language of the app and of the mail it sends (BCP 47).
	Language  string    `json:"language"`
	CreatedAt time.Time `json:"created_at"`
}

// Session is one device signed in to an account.
type Session struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Device is what the browser said of itself when it signed in.
	Device string `json:"device"`
	// Current is true for the session that asked.
	Current bool `json:"current"`
}

// EmailStart asks for a sign-in message. Invitation is needed only by an
// address that has no account yet.
type EmailStart struct {
	Address    string `json:"address"`
	Invitation string `json:"invitation,omitempty"`
}

// EmailStarted answers EmailStart, whether a message was sent or not.
type EmailStarted struct {
	// Attempt names the sign-in that was started; the code is entered against it.
	Attempt string `json:"attempt"`
	// ExpiresIn is how many seconds the code and the link work for.
	ExpiresIn int `json:"expires_in"`
}

// EmailFinish ends a sign-in with what the message carried: the code, typed
// in the browser that asked, or the token of the link.
type EmailFinish struct {
	Attempt string `json:"attempt"`
	Code    string `json:"code,omitempty"`
	Token   string `json:"token,omitempty"`
}

// EmailLook asks whose sign-in the link of a message would end, without
// ending it.
type EmailLook struct {
	Attempt string `json:"attempt"`
	Token   string `json:"token"`
}

// EmailPending is a sign-in that a link would end. The page that opened the
// link shows the address and asks before it signs in: a link may have been
// sent by someone else, to sign the person into an account that is not theirs.
type EmailPending struct {
	Address string `json:"address"`
}

// Me is the account behind a session.
type Me struct {
	Account Account `json:"account"`
	Session Session `json:"session"`
}

// SessionList is the sessions of an account.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}

// InvitationRequest asks for single-use sign-up codes (operator only).
type InvitationRequest struct {
	Count int `json:"count"`
}

// Invitations are sign-up codes, shown once: only their hash is kept.
type Invitations struct {
	Invitations []string  `json:"invitations"`
	ExpiresAt   time.Time `json:"expires_at"`
}
