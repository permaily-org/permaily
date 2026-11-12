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
	// ErrAccountBlocked is said to whoever proved who they are and whose
	// account the operator blocked: never before the proof, so it tells nobody
	// else that an address has an account, or that it is blocked.
	ErrAccountBlocked = &Error{"account_blocked", "this account is blocked; write to support@permaily.com"}
	// ErrAddressInUse refuses a contact address that is another account's. It
	// is said only after the code proved the address is the asker's.
	ErrAddressInUse = &Error{"address_in_use", "that address belongs to another account"}
	// ErrNotCancellable refuses the operator, or an admin, cancelling a
	// deletion the account's person asked for: only the person does.
	ErrNotCancellable = &Error{"not_cancellable", "only the person can cancel a deletion they asked for"}
	// ErrConfirmationNeeded refuses a change that decides who gets into an
	// account while the session's proof of who the person is is not fresh.
	ErrConfirmationNeeded = &Error{"confirmation_needed", "confirm it is you to continue"}
	// ErrPasskeyRequired refuses confirming by mail for an account that
	// signs in with passkeys only.
	ErrPasskeyRequired = &Error{"passkey_required", "this account confirms with a passkey"}
	// ErrPasskeyOnlyNeeded refuses an admin whose account is not "passkey
	// only": whoever reads their mail must not be an admin.
	ErrPasskeyOnlyNeeded = &Error{"passkey_only_needed", "turn on passkey only to use the admin panel"}
	// ErrSuperAdminOnly refuses an admin what only the super admin does.
	ErrSuperAdminOnly = &Error{"super_admin_only", "only the super admin can do that"}
	// ErrSuperAdminKept has the code of ErrSuperAdminOnly: nobody, the super
	// admin included, blocks or deletes the super admin's account through
	// the admin panel.
	ErrSuperAdminKept = &Error{"super_admin_only", "the super admin's account is not blocked or deleted here"}
)

// The failures of signing in with a provider (Google, GitHub, Meta,
// Microsoft). The ones the callback can end with are the reason codes the
// app turns into words; none says whether an address or a provider account
// has an account here to someone who did not prove it is theirs.
var (
	// ErrProviderOff refuses a provider the operator gave no secret for, and
	// a name that is not a provider.
	ErrProviderOff = &Error{"provider_off", "signing in with that provider is not available"}
	// ErrProviderFailed covers a trip to a provider that ended, was started in
	// another browser or was refused, an answer that does not check, and an
	// account that does not take providers. None says which.
	ErrProviderFailed = &Error{"provider_failed", "signing in with that provider did not work; try again, or sign in another way"}
	// ErrProviderUnavailable is a provider that could not be reached, or the
	// service's own failure while a trip to one ended: later it may work.
	ErrProviderUnavailable = &Error{"provider_unavailable", "signing in with that provider is not working right now; try again later, or sign in another way"}
	// ErrInvitationNeeded is for whoever the provider vouches for: their
	// address has no account, and no invitation was given to make one.
	ErrInvitationNeeded = &Error{"invitation_needed", "there is no account for your address yet; an invitation is needed to make one"}
	// ErrProviderInUse refuses linking a provider account that leads to
	// another account, or whose verified address is another account's.
	ErrProviderInUse = &Error{"provider_in_use", "that provider account belongs with another Permaily account"}
	// ErrProviderLinked refuses a second account of one provider.
	ErrProviderLinked = &Error{"provider_linked", "this account has an account of that provider linked already; unlink it first"}
	ErrLastWayIn      = &Error{"last_way_in", "this is the only way left to sign in to the account; add another first"}
	// ErrInvalidProviderApp refuses what cannot be an application's
	// credentials. It never repeats them.
	ErrInvalidProviderApp = &Error{"invalid_provider_app", "client_id and client_secret are each 1 to 512 characters of visible ASCII"}
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
	// ClosingBy says who asked to delete a closing account: ClosingByPerson
	// or ClosingByOperator; absent otherwise.
	ClosingBy string `json:"closing_by,omitempty"`
	// Admin is AdminSuper or AdminRole when the account is an admin's,
	// also while "passkey only" is off; absent otherwise.
	Admin string `json:"admin,omitempty"`
	// PasskeyOnly says the account signs in with passkeys only, which the
	// admin panel needs.
	PasskeyOnly bool `json:"passkey_only"`
}

// What an account is in the admin panel.
const (
	// AdminSuper is the account whose contact address is the one compiled
	// into the image: it makes and removes the other admins.
	AdminSuper = "super"
	// AdminRole is an account the super admin made an admin.
	AdminRole = "admin"
)

// Who asked to delete an account.
const (
	ClosingByPerson   = "person"
	ClosingByOperator = "operator"
)

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
	// ClosingBy says who asked to delete a closing account: ClosingByPerson
	// or ClosingByOperator.
	ClosingBy string `json:"closing_by,omitempty"`
	// BlockedAt is when the operator blocked the account; absent when it is
	// not blocked. Nobody signs in to a blocked account.
	BlockedAt   time.Time `json:"blocked_at,omitzero"`
	Passkeys    int       `json:"passkeys"`
	PasskeyOnly bool      `json:"passkey_only"`
	// Providers are the identifiers of the providers linked to the account
	// (google, github, meta, microsoft), the oldest first. Never the subject:
	// which account it is at the provider.
	Providers []string `json:"providers"`
	// Sessions is how many devices are signed in.
	Sessions int `json:"sessions"`
	// Admin is AdminSuper or AdminRole for an admin's account; absent
	// otherwise.
	Admin string `json:"admin,omitempty"`
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
	// ConfirmedUntil is when the proof of who the person is, made with this
	// session, stops being fresh; absent once it is not, and on every
	// session but the one that asked.
	ConfirmedUntil time.Time `json:"confirmed_until,omitzero"`
}

// ConfirmWays says how the person behind a session can confirm it is them
// now, and until when the session's last proof is fresh.
type ConfirmWays struct {
	// Ways is ConfirmPasskey when the account has a passkey, and
	// ConfirmEmail unless it signs in with passkeys only.
	Ways           []string  `json:"ways"`
	ConfirmedUntil time.Time `json:"confirmed_until,omitzero"`
}

// The ways a person confirms it is them.
const (
	ConfirmPasskey = "passkey"
	ConfirmEmail   = "email"
)

// ConfirmFinish confirms with the code of the message sent to the contact
// address.
type ConfirmFinish struct {
	Attempt string `json:"attempt"`
	Code    string `json:"code"`
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
	Invitations []string `json:"invitations"`
	// IDs name the invitations for the operator, in the same order: what one
	// is listed and revoked by. An identifier is not the code, and the code
	// cannot be had from it.
	IDs       []string  `json:"ids"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Invitation is what the operator's tool shows of an invitation that was not
// used yet: never its code.
type Invitation struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InvitationList is the invitations not used yet, the oldest first. One that
// was used is deleted when it is used, so it is not here.
type InvitationList struct {
	Invitations []Invitation `json:"invitations"`
	// Truncated says there are more than the list holds.
	Truncated bool `json:"truncated,omitempty"`
}

// AddressStart asks for the message that proves a new contact address is the
// person's.
type AddressStart struct {
	Address string `json:"address"`
}

// AddressFinish changes the contact address with the code of that message.
type AddressFinish struct {
	Attempt string `json:"attempt"`
	Code    string `json:"code"`
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
	// AllowCredentials names the passkeys that may answer: only when a
	// signed-in person confirms it is them, with one of their account's.
	AllowCredentials []CredentialDescriptor `json:"allowCredentials,omitempty"`
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

// ProvidersOn says which providers a person can sign in with: the ones the
// operator gave a secret for.
type ProvidersOn struct {
	Providers []string `json:"providers"`
}

// ProviderStart starts a trip to a provider. Invitation is needed only by
// whoever has no account yet, and ReturnTo is the path of the app the person
// was going to.
type ProviderStart struct {
	Provider   string `json:"provider"`
	Invitation string `json:"invitation,omitempty"`
	ReturnTo   string `json:"return_to,omitempty"`
}

// ProviderStarted answers ProviderStart: where the browser goes, and for how
// many seconds the provider's answer is taken.
type ProviderStarted struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

// ProviderPending is a sign-in with a provider that waits for an address to
// be confirmed: the provider said who the person is there, and not that an
// address is theirs.
type ProviderPending struct {
	Provider string `json:"provider"`
	// Address is what the provider gave, if anything, to offer in the form.
	// It proves nothing: the message's code does.
	Address string `json:"address"`
	// ReturnTo is where the person was going when the sign-in started.
	ReturnTo string `json:"return_to"`
	// ExpiresIn is how many seconds are left to confirm.
	ExpiresIn int `json:"expires_in"`
}

// ProviderConfirmStart asks for the message that confirms an address.
type ProviderConfirmStart struct {
	Address string `json:"address"`
}

// ProviderConfirm ends a pending sign-in with the code of that message.
type ProviderConfirm struct {
	Attempt string `json:"attempt"`
	Code    string `json:"code"`
}

// LinkedProvider is a provider account that signs in to an account. Nothing
// the provider said of it is kept but which one it is, so nothing else is
// shown.
type LinkedProvider struct {
	Provider string    `json:"provider"`
	LinkedAt time.Time `json:"linked_at"`
}

// LinkedProviders is the provider accounts of an account, the oldest first.
type LinkedProviders struct {
	Providers []LinkedProvider `json:"providers"`
}

// ProviderAppSet gives the enclave the application registered at a provider
// (operator only). The secret is kept sealed and never answered.
type ProviderAppSet struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// ProviderApp is what is said of a provider's application: never its secret.
type ProviderApp struct {
	Provider string    `json:"provider"`
	ClientID string    `json:"client_id"`
	SetAt    time.Time `json:"set_at"`
}

// ProviderApps is the providers that have an application set.
type ProviderApps struct {
	Providers []ProviderApp `json:"providers"`
}

// AdminEntry is one admin in the admin panel's list.
type AdminEntry struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Name    string `json:"name"`
	// Super is true for the super admin, who is listed first.
	Super bool `json:"super"`
	// Since is when the super admin made the account an admin; absent for
	// the super admin.
	Since time.Time `json:"since,omitzero"`
}

// AdminList is the admins: the super admin first, then the others, the
// oldest first.
type AdminList struct {
	Admins []AdminEntry `json:"admins"`
}

// LogAccount names an account in the admin log. Address is the account's
// contact address now; absent when the account is gone.
type LogAccount struct {
	ID      string `json:"id"`
	Address string `json:"address,omitempty"`
}

// LogEntry is one thing an admin did through the admin panel.
type LogEntry struct {
	At    time.Time  `json:"at"`
	Kind  string     `json:"kind"`
	Admin LogAccount `json:"admin"`
	// Account is the account it was done to; absent when it was not about one.
	Account *LogAccount `json:"account,omitempty"`
	// Invitation is the invitation it was about, made or revoked; absent
	// otherwise.
	Invitation string `json:"invitation,omitempty"`
}

// AdminLog is a page of the admin log, the newest first.
type AdminLog struct {
	Entries []LogEntry `json:"entries"`
	// Next is what to ask the following page with; absent on the last one.
	Next string `json:"next,omitempty"`
}

// AccountCounts counts the accounts by state. Blocked is apart from the
// state: a blocked account is also active or closing.
type AccountCounts struct {
	Active  int `json:"active"`
	Closing int `json:"closing"`
	Blocked int `json:"blocked"`
	Total   int `json:"total"`
}

// InvitationCounts counts the invitations not used yet.
type InvitationCounts struct {
	Unused int `json:"unused"`
}

// Stats is what the admin panel's first page counts.
type Stats struct {
	Accounts    AccountCounts    `json:"accounts"`
	Invitations InvitationCounts `json:"invitations"`
	// Complete is false when there were more accounts or invitations than
	// one request counts, or some could not be read.
	Complete bool `json:"complete"`
}
