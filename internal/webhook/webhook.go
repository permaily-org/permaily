// Package webhook is what a connection's webhook decides from its arguments
// alone (ADR 0039): which URL a vendor may ask to be called on, the shape of
// what it asks, when a schedule falls in the person's time zone, the notice
// and its signature, and the bounds every implementation keeps. It is pure
// (depguard "webhook-is-pure"): no network, no storage, no clock of its own.
// Calling a webhook, the queue and the scheduler are internal/services/webhooks';
// what a connection approved is internal/services/connections'.
//
// This package is the seam committed with the design: ADR 0039's pull
// requests consume it and none redefines it.
package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

// Bounds every implementation keeps (ADR 0039, "Bounds").
const (
	// RequestBytesMax bounds the pushed request's "webhook" parameter.
	RequestBytesMax = 1 << 10
	// URLBytesMax bounds a webhook's URL: a client_id's.
	URLBytesMax = oauth.ClientIDMaxBytes
	// NoticeBytesMax bounds a notice's body: its fixed members and two URLs
	// of URLBytesMax (the client_id and the audience) fit.
	NoticeBytesMax = 2 << 10
	// AnswerBytesMax is the most of a webhook's answer read before the
	// connection is closed: its status decides, its body never does.
	AnswerBytesMax = 4 << 10
	// AttemptTimeout bounds one call, from the dial to the answer's status.
	AttemptTimeout = 10 * time.Second
	// MinInterval is the least time between two notices of one connection:
	// what arrives meanwhile is told by the next one (coalescing).
	MinInterval = time.Minute
	// GiveUpAfter is how long a notice is tried from when it was made:
	// past it, it is dropped, and the next match or schedule makes a new one.
	GiveUpAfter = 72 * time.Hour
	// LeaseFor is how long an enclave holds a connection's queue entry while
	// it calls the webhook: the other one takes it over after that.
	LeaseFor = 30 * time.Second
	// InFlightMax bounds the calls one enclave makes at once, every vendor
	// together; AttemptsPerMinute the calls it starts in a minute.
	InFlightMax       = 16
	AttemptsPerMinute = 600
	// ArrivalLag is how far back an evaluation of new mail looks again
	// before its watermark, for a message whose storing finished after a
	// later one's (a delivery's deadline is a minute from its start).
	ArrivalLag = 2 * time.Minute
	// ArrivalPage is the most messages one evaluation of new mail reads; more
	// are read by the next one.
	ArrivalPage = 50
	// RepairEvery is how often every queue entry is walked: a hint lost in
	// a crash, a due entry deleted, a connection ended without a word.
	RepairEvery = time.Hour
	// TimestampTolerance is how far a notice's timestamp may be from the
	// vendor's clock (Standard Webhooks' recommendation).
	TimestampTolerance = 5 * time.Minute
	// LateAfter is how long after its scheduled time a scheduled notice is
	// still on time; later, it says Late.
	LateAfter = 15 * time.Minute
)

// Backoff is the wait after each failed call of one notice: the first call
// is made at once (MinInterval after the notice before), then after one
// minute, five, fifteen, an hour, three, six and twelve, and every twelve
// hours after that, until GiveUpAfter: about a dozen calls in all.
var Backoff = []time.Duration{
	time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour,
	3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
}

// Wait is how long to wait after the failed call number attempt (1 for the
// first).
func Wait(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(Backoff) {
		return Backoff[len(Backoff)-1]
	}
	return Backoff[attempt-1]
}

// Prefixes of what the service makes.
const (
	// NoticePrefix + 26 base32 characters is a notice's identifier
	// (WebhookNotice.ID, the webhook-id header).
	NoticePrefix = "ntc_"
	// SubscriptionPrefix + 26 base32 characters names a connection's
	// webhook to its vendor (VendorWebhook.Subscription): never the
	// connection's identifier.
	SubscriptionPrefix = "sub_"
)

// Signing keys (ADR 0039, "Signatures").
const (
	// KeyID is the key notices are signed with; KeyPurpose derives its
	// Ed25519 seed from the root secret, so every enclave of every release
	// that receives the secret signs with the same key.
	KeyID      = "wk1"
	KeyPurpose = "permaily webhooks v1 " + KeyID
	// KeysPath is where the api host publishes the keys, as a JWK set.
	KeysPath = "/.well-known/permaily-webhook-keys"
)

// Errors, each of a fixed kind: their words hold nothing of a request.
var (
	ErrRequest  = errors.New("webhook: not a webhook a vendor may ask for")
	ErrURL      = errors.New("webhook: not a URL on the vendor's own host")
	ErrSchedule = errors.New("webhook: not one of the schedules")
	ErrTimeZone = errors.New("webhook: not a time zone the image knows")
	ErrNotice   = errors.New("webhook: not a notice")
	ErrVerify   = errors.New("webhook: the notice's signature does not verify")
)

// URL checks a webhook's URL against its vendor's client_id: an https URL
// under the rules of a client_id (oauth.ClientID: no user, no port, no
// query, no fragment, a path, a public DNS name that is not the
// deployment's) whose host is exactly the client_id's host. It returns that
// host.
func URL(raw, clientID, deployment string) (string, error) {
	host, err := oauth.ClientID(raw, deployment)
	if err != nil {
		return "", ErrURL
	}
	client, err := oauth.ClientID(clientID, deployment)
	if err != nil || client != host {
		return "", ErrURL
	}
	return host, nil
}

// ParseRequest reads a pushed request's "webhook" parameter: one JSON object
// of at most RequestBytesMax with no member but WebhookRequest's, a URL that
// URL accepts for clientID, and new mail, a schedule or both.
func ParseRequest(raw, clientID, deployment string) (*contracts.WebhookRequest, error) {
	if len(raw) > RequestBytesMax {
		return nil, ErrRequest
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	var r contracts.WebhookRequest
	if err := d.Decode(&r); err != nil || d.More() {
		return nil, ErrRequest
	}
	if _, err := URL(r.URL, clientID, deployment); err != nil {
		return nil, err
	}
	if !r.NewMail && r.Schedule == nil {
		return nil, ErrRequest
	}
	if r.Schedule != nil {
		if err := CheckSchedule(*r.Schedule); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// Same says whether two webhooks are the same one (the same URL, called for
// the same things). For "Asking again" (ADR 0039): a request whose webhook
// is absent, or Same as its client's connection's, may be added to that
// connection as another device; any other one replaces it.
func Same(a, b *contracts.WebhookRequest) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.URL != b.URL || a.NewMail != b.NewMail || (a.Schedule == nil) != (b.Schedule == nil) {
		return false
	}
	return a.Schedule == nil || *a.Schedule == *b.Schedule
}
