// Package contracts holds the types that cross the service's boundary: what
// the API, MCP, the command-line tools and the web app send and receive. They
// are defined once, here, so every adapter says the same thing.
//
// It imports nothing of the service: every other layer may import it.
package contracts

import (
	"encoding/base64"
	"encoding/json"
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
	// ErrInvalidPasskey covers every failure of making or using a passkey: a
	// ceremony that ended, an answer that does not check, a passkey or an
	// account that is not there. None says which.
	ErrInvalidPasskey  = &Error{"invalid_passkey", "that passkey could not be used; try again"}
	ErrInvalidName     = &Error{"invalid_name", "a name is one line of 1 to 64 characters"}
	ErrTooManyPasskeys = &Error{"too_many_passkeys", "this account has as many passkeys as one can have; remove one first"}
	ErrLastPasskey     = &Error{"last_passkey", "this is the only passkey of an account that signs in with passkeys only; add another, or turn that off first"}
	ErrPasskeyNeeded   = &Error{"passkey_needed", "add a passkey before turning on passkey only"}
	ErrPasskeyOnlyOff  = &Error{"passkey_only_off", "there are recovery codes only while passkey only is on"}
	// ErrInvalidAccountName has the code of ErrInvalidName: a caller does the
	// same with both, and only the words differ.
	ErrInvalidAccountName = &Error{"invalid_name", "a name is one line of at most 80 characters"}
	// ErrAccountClosing refuses every change to an account that is being
	// deleted but cancelling that.
	ErrAccountClosing = &Error{"account_closing", "this account is being deleted; cancel that to change anything"}
	ErrInvalidLimit   = &Error{"invalid_limit", "limit must be 1-100"}
	ErrInvalidCursor  = &Error{"invalid_cursor", "after is not a place in the list; use the next of the answer before"}
)

// AccountState says whether an account can be used.
type AccountState string

const (
	// AccountActive is an account in use.
	AccountActive AccountState = "active"
	// AccountClosing is an account its person asked to delete: nothing of it
	// changes until it is removed, or until the person cancels.
	AccountClosing AccountState = "closing"
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
	// ClosesAt is when a closing account is removed; absent otherwise.
	ClosesAt time.Time `json:"closes_at,omitzero"`
}

// AccountUpdate changes what a person may change of their account. What is
// absent stays as it is.
type AccountUpdate struct {
	Name *string `json:"name"`
}

// AccountRecord is what the operator's tool shows of an account: what the
// enclave vouches for, read from the account's signed record.
type AccountRecord struct {
	ID       string       `json:"id"`
	State    AccountState `json:"state"`
	Address  string       `json:"address"`
	Name     string       `json:"name"`
	Language string       `json:"language"`
	// CreatedAt is when the account was made, AddressVerifiedAt when its
	// person last proved the address is theirs, and ClosesAt when a closing
	// account is removed.
	CreatedAt         time.Time `json:"created_at"`
	AddressVerifiedAt time.Time `json:"address_verified_at"`
	ClosesAt          time.Time `json:"closes_at,omitzero"`
	Passkeys          int       `json:"passkeys"`
	PasskeyOnly       bool      `json:"passkey_only"`
	// Sessions is how many devices are signed in.
	Sessions int `json:"sessions"`
}

// AccountList is one page of the accounts, the oldest first.
type AccountList struct {
	Accounts []AccountRecord `json:"accounts"`
	// Unreadable counts the accounts of this page whose record could not be
	// read or does not verify: they are there, and nothing is said of them.
	Unreadable int `json:"unreadable"`
	// Next is what to ask the following page with; absent on the last one.
	Next string `json:"next,omitempty"`
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

// Bytes is a binary value, written in JSON the way WebAuthn writes one:
// base64url without padding.
type Bytes []byte

func (b Bytes) String() string { return base64.RawURLEncoding.EncodeToString(b) }

// MarshalText implements encoding.TextMarshaler.
func (b Bytes) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *Bytes) UnmarshalText(text []byte) error {
	out, err := base64.RawURLEncoding.DecodeString(string(text))
	if err != nil {
		return err
	}
	*b = out
	return nil
}

// Passkey is what a person sees of one of their passkeys.
type Passkey struct {
	// ID is the credential's identifier, as the browser names it.
	ID        Bytes     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Synced says the passkey is kept by a provider that copies it to the
	// person's other devices.
	Synced bool `json:"synced"`
}

// PasskeyList is the passkeys of an account, and how it signs in.
type PasskeyList struct {
	Passkeys []Passkey `json:"passkeys"`
	// PasskeyOnly says email and every provider are turned off for sign-in.
	PasskeyOnly       bool `json:"passkey_only"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

// PasskeyCreation answers the start of making a passkey. Options is what
// PublicKeyCredential.parseCreationOptionsFromJSON takes.
type PasskeyCreation struct {
	Ceremony string          `json:"ceremony"`
	Options  CreationOptions `json:"options"`
}

// CreationOptions are the options of navigator.credentials.create.
type CreationOptions struct {
	RP               RelyingParty          `json:"rp"`
	User             PasskeyUser           `json:"user"`
	Challenge        Bytes                 `json:"challenge"`
	PubKeyCredParams []CredentialParameter `json:"pubKeyCredParams"`
	// Timeout is in milliseconds.
	Timeout                int                    `json:"timeout"`
	ExcludeCredentials     []CredentialDescriptor `json:"excludeCredentials"`
	AuthenticatorSelection AuthenticatorSelection `json:"authenticatorSelection"`
	Attestation            string                 `json:"attestation"`
}

type RelyingParty struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PasskeyUser is the account a passkey is made for. ID is what the passkey
// answers with when it signs in.
type PasskeyUser struct {
	ID          Bytes  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type CredentialParameter struct {
	Type string `json:"type"`
	// Alg is a COSE algorithm.
	Alg int `json:"alg"`
}

type CredentialDescriptor struct {
	Type string `json:"type"`
	ID   Bytes  `json:"id"`
}

type AuthenticatorSelection struct {
	ResidentKey      string `json:"residentKey"`
	UserVerification string `json:"userVerification"`
}

// PasskeyRequest answers the start of signing in with a passkey. Options is
// what PublicKeyCredential.parseRequestOptionsFromJSON takes.
type PasskeyRequest struct {
	Ceremony string         `json:"ceremony"`
	Options  RequestOptions `json:"options"`
}

// RequestOptions are the options of navigator.credentials.get. They name no
// credential: the passkey says which account it is for.
type RequestOptions struct {
	Challenge Bytes `json:"challenge"`
	// Timeout is in milliseconds.
	Timeout          int    `json:"timeout"`
	RPID             string `json:"rpId"`
	UserVerification string `json:"userVerification"`
}

// CreatedCredential is what PublicKeyCredential.toJSON() gives after
// navigator.credentials.create, as far as the service reads it.
type CreatedCredential struct {
	RawID    Bytes `json:"rawId"`
	Response struct {
		ClientDataJSON    Bytes `json:"clientDataJSON"`
		AttestationObject Bytes `json:"attestationObject"`
	} `json:"response"`
}

// UnmarshalJSON takes what is read and leaves the rest: a browser's toJSON()
// carries more, and what it carries grows, whatever the request around it
// accepts.
func (c *CreatedCredential) UnmarshalJSON(b []byte) error {
	type plain CreatedCredential
	return json.Unmarshal(b, (*plain)(c))
}

// AssertedCredential is what PublicKeyCredential.toJSON() gives after
// navigator.credentials.get, as far as the service reads it.
type AssertedCredential struct {
	RawID    Bytes `json:"rawId"`
	Response struct {
		ClientDataJSON    Bytes `json:"clientDataJSON"`
		AuthenticatorData Bytes `json:"authenticatorData"`
		Signature         Bytes `json:"signature"`
		// UserHandle is the PasskeyUser.ID the passkey was made with.
		UserHandle Bytes `json:"userHandle"`
	} `json:"response"`
}

// UnmarshalJSON takes what is read and leaves the rest, as CreatedCredential does.
func (c *AssertedCredential) UnmarshalJSON(b []byte) error {
	type plain AssertedCredential
	return json.Unmarshal(b, (*plain)(c))
}

// PasskeyFinish ends making a passkey, with the browser's answer and the
// name the person gives it.
type PasskeyFinish struct {
	Ceremony   string            `json:"ceremony"`
	Name       string            `json:"name"`
	Credential CreatedCredential `json:"credential"`
}

// PasskeySignIn ends signing in with a passkey, with the browser's answer.
type PasskeySignIn struct {
	Ceremony   string             `json:"ceremony"`
	Credential AssertedCredential `json:"credential"`
}

// PasskeyRename gives a passkey another name.
type PasskeyRename struct {
	Name string `json:"name"`
}

// Recovery signs in with a recovery code, to an account that signs in with
// passkeys only.
type Recovery struct {
	Address string `json:"address"`
	Code    string `json:"code"`
}

// PasskeyOnly turns "passkey only" on or off.
type PasskeyOnly struct {
	On bool `json:"on"`
}

// PasskeyOnlyState answers PasskeyOnly. RecoveryCodes are there when it was
// just turned on, and are shown once: only their hash is kept.
type PasskeyOnlyState struct {
	PasskeyOnly   bool     `json:"passkey_only"`
	RecoveryCodes []string `json:"recovery_codes"`
}

// RecoveryCodes are new recovery codes, shown once; the ones before them
// stop working.
type RecoveryCodes struct {
	RecoveryCodes []string `json:"recovery_codes"`
}
