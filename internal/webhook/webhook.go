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
	"encoding/json"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
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
	// HostInFlightMax and HostAttemptsPerMinute are one vendor's share of
	// them: the calls one enclave makes at once, and starts in a minute, to
	// one canonical host (the client_id's, which is the webhook's), so a
	// vendor whose receiver is slow or hostile delays its own notices only
	// (design review, finding 8). HostReadyMax bounds the notices of one
	// host an enclave holds ready to call, ReadyMax every host's together;
	// what is past them stays in the due index for the next pass.
	HostInFlightMax       = 2
	HostAttemptsPerMinute = 60
	HostReadyMax          = 64
	ReadyMax              = 1024
	// EvaluationsInFlightMax bounds the evaluations of new mail one enclave
	// runs at once, AccountEvaluationsInFlightMax one account's of them, and
	// EvaluationsPerMinute those it starts in a minute: the model's asks
	// they make are bounded by the account's share and the spend guard too.
	EvaluationsInFlightMax        = 4
	AccountEvaluationsInFlightMax = 1
	EvaluationsPerMinute          = 120
	// ResponseHeaderBytesMax bounds the header block of a webhook's answer
	// the sender reads (HTTP/2's SETTINGS_MAX_HEADER_LIST_SIZE, and the most
	// it decodes); IdleConnectionsMax is how many connections to a webhook
	// it keeps once a call ends: none, every call dials its own.
	ResponseHeaderBytesMax = 8 << 10
	IdleConnectionsMax     = 0
	// RequestWriteBytesMax is the most a call's request may be once framed
	// (its HEADERS and its one DATA frame): within one TLS record of
	// plaintext, so the request is written in one Write, whole or not at
	// all (Call).
	RequestWriteBytesMax = 16 << 10
	// ArrivalLag is how far back the late sweep of new mail looks before
	// the cursor, for a message whose storing finished after a later one's
	// (a delivery's deadline is a minute from its start).
	ArrivalLag = 2 * time.Minute
	// ArrivalPage is the most messages one evaluation of new mail reads,
	// forward from the cursor and in the late sweep together; more are read
	// by the next one.
	ArrivalPage = 50
	// MatchMessagesMax bounds the messages a match names (the earliest that
	// would leave): what a check before every attempt looks for again.
	MatchMessagesMax = 16
	// RepairEvery is how often every queue entry is walked: a hint lost in
	// a crash, a due entry deleted, a connection ended without a word.
	RepairEvery = time.Hour
	// TimestampTolerance is how far a notice's timestamp may be from the
	// vendor's clock (Standard Webhooks' recommendation).
	TimestampTolerance = 5 * time.Minute
	// DedupKeepFor is how long a vendor must keep a notice's identifier
	// after it first took it (design review, finding 9): a retry may come
	// until GiveUpAfter from the notice's making, freshly signed, and each
	// end of that span may be TimestampTolerance off the vendor's clock.
	// Open refuses a notice made longer ago than NoticeLife, so nothing
	// older needs keeping.
	DedupKeepFor = GiveUpAfter + 2*TimestampTolerance
	// NoticeLife is how old (by its created_at) a notice may be when a
	// vendor opens it: GiveUpAfter, and the tolerance of the clocks.
	NoticeLife = GiveUpAfter + TimestampTolerance
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
// of at most RequestBytesMax, read exactly (jws.Object: valid UTF-8, nothing
// after it, a name twice in any case refused) with no member but
// WebhookRequest's, each of its type ("url" a string, "new_mail" true or
// false, "schedule" an object read the same way with "every", "on" and "at"
// strings), a URL that URL accepts for clientID, and new mail, a schedule
// or both.
func ParseRequest(raw, clientID, deployment string) (*contracts.WebhookRequest, error) {
	if len(raw) > RequestBytesMax {
		return nil, ErrRequest
	}
	m, ok := members([]byte(raw), "url", "new_mail", "schedule")
	if !ok {
		return nil, ErrRequest
	}
	var r contracts.WebhookRequest
	if r.URL, ok = jws.Text(m["url"]); !ok {
		return nil, ErrRequest
	}
	if v, there := m["new_mail"]; there {
		if r.NewMail, ok = boolean(v); !ok {
			return nil, ErrRequest
		}
	}
	if v, there := m["schedule"]; there {
		s, ok := members(v, "every", "on", "at")
		if !ok {
			return nil, ErrRequest
		}
		var every string
		r.Schedule = &contracts.WebhookSchedule{}
		if every, ok = jws.Text(s["every"]); !ok {
			return nil, ErrRequest
		}
		r.Schedule.Every = contracts.WebhookEvery(every)
		if r.Schedule.At, ok = jws.Text(s["at"]); !ok {
			return nil, ErrRequest
		}
		if v, there := s["on"]; there {
			if r.Schedule.On, ok = jws.Text(v); !ok || r.Schedule.On == "" {
				return nil, ErrRequest
			}
		}
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

// members reads one object exactly (jws.Object) whose names are all among
// allowed, byte for byte.
func members(raw []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	m, ok := jws.Object(raw)
	if !ok {
		return nil, false
	}
	for name := range m {
		known := false
		for _, a := range allowed {
			known = known || name == a
		}
		if !known {
			return nil, false
		}
	}
	return m, true
}

// boolean is a member that is the JSON literal true or false: never null,
// a string or a number.
func boolean(v json.RawMessage) (bool, bool) {
	switch string(v) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
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
