package contracts

import "time"

// Webhooks (ADR 0019, "Delivery, three ways"; how in ADR 0039): a connection
// may tell its vendor, by a call to the vendor's own webhook, that there is
// mail to fetch: when a message it may read arrives, on a schedule the person
// approved, or both. The call carries a signed notice and never mail: the
// vendor fetches with its token, through the same checks, leaving the same
// receipts. Only an app asks for a webhook, in its pushed request; MCP
// clients send none.

// WebhookEvery is how often a scheduled notice is sent: one of two fixed
// choices (ADR 0039, "Schedules").
type WebhookEvery string

const (
	// EveryDay: every day at At, in the person's time zone.
	EveryDay WebhookEvery = "day"
	// EveryWeek: every week on On, at At, in the person's time zone.
	EveryWeek WebhookEvery = "week"
)

// WebhookSchedule is when a scheduled notice is sent, as wall time in the
// person's time zone (the zone is the person's, chosen when they allow, and
// never the vendor's to say): At is "HH:MM" on a quarter hour ("08:00",
// "17:45"); On is a weekday in lower case ("monday" to "sunday") for
// EveryWeek, and absent for EveryDay.
type WebhookSchedule struct {
	Every WebhookEvery `json:"every"`
	On    string       `json:"on,omitempty"`
	At    string       `json:"at"`
}

// WebhookRequest is what a vendor asks for in its pushed request's
// "webhook" parameter (ADR 0039, "Where the webhook comes from"): the
// webhook's URL, which must be on the host of the vendor's client_id, and
// what it is called for: new mail, a schedule, or both (at least one). A
// pushed request without the parameter asks for none.
type WebhookRequest struct {
	URL      string           `json:"url"`
	NewMail  bool             `json:"new_mail,omitempty"`
	Schedule *WebhookSchedule `json:"schedule,omitempty"`
}

// WebhookAsked is what the consent screen shows of a request's webhook: the
// host it is called on (always the vendor's own domain, Vendor.Domain), and
// when. The person accepts or refuses it with the request, as a whole.
type WebhookAsked struct {
	Host     string           `json:"host"`
	NewMail  bool             `json:"new_mail,omitempty"`
	Schedule *WebhookSchedule `json:"schedule,omitempty"`
}

// WebhookState is whether a connection's webhook is called.
type WebhookState string

const (
	// WebhookOn: it is called, when its connection gets mail at all
	// (Connection.Delivery is DeliveryOn and the connection has an active
	// device).
	WebhookOn WebhookState = "on"
	// WebhookOff: the person turned it off; only a new request turns it
	// on again.
	WebhookOff WebhookState = "off"
	// WebhookStopped: the vendor asked to stop (its webhook answered 410
	// Gone); only a new request turns it on again.
	WebhookStopped WebhookState = "stopped"
)

// WebhookFailure is why the last calls of a webhook failed, in fixed words
// only: never what the vendor's server sent.
type WebhookFailure string

const (
	WebhookNoAnswer WebhookFailure = "no_answer" // it could not be reached, or did not answer in time
	WebhookError    WebhookFailure = "error"     // it answered with a status other than 2xx, 410 and 3xx
	WebhookRedirect WebhookFailure = "redirect"  // it answered with a redirect, which is never followed
	WebhookRefused  WebhookFailure = "refused"   // its TLS or HTTP/2 could not be used
)

// ConnectionWebhook is a connection's webhook as the person's app shows it.
// NextAt is the next scheduled notice, when it has a schedule and is on.
// LastSentAt is when a notice was last taken by the vendor (a 2xx).
// FailingSince and Failure say since when, and why, the calls have failed;
// absent while they do not.
type ConnectionWebhook struct {
	Host         string           `json:"host"`
	NewMail      bool             `json:"new_mail,omitempty"`
	Schedule     *WebhookSchedule `json:"schedule,omitempty"`
	TimeZone     string           `json:"time_zone,omitempty"`
	State        WebhookState     `json:"state"`
	NextAt       time.Time        `json:"next_at,omitzero"`
	LastSentAt   time.Time        `json:"last_sent_at,omitzero"`
	FailingSince time.Time        `json:"failing_since,omitzero"`
	Failure      WebhookFailure   `json:"failure,omitempty"`
}

// VendorWebhook is what a vendor is told of its connection's webhook: the
// subscription its notices name (so it can tell which of its tokens to
// fetch with), which is also the endpoint identifier every notice's "v2a"
// signature is bound to (Standard Webhooks: the vendor verifies with the
// one it was given here, never one taken from a call), the URL it asked
// for, what it is called for, and its state.
// Never the person's time zone: the vendor asked for wall time, and the
// zone says where the person is.
type VendorWebhook struct {
	Subscription string           `json:"subscription"`
	URL          string           `json:"url"`
	NewMail      bool             `json:"new_mail,omitempty"`
	Schedule     *WebhookSchedule `json:"schedule,omitempty"`
	State        WebhookState     `json:"state"`
}

// WebhookNoticeType is what a notice says.
type WebhookNoticeType string

const (
	// NoticeMailMatched: mail that the connection may read arrived, every
	// rule applied and the model asked where it had to be; fetch from
	// Since.
	NoticeMailMatched WebhookNoticeType = "mail.matched"
	// NoticeScheduleDue: the time the person approved came; sent whether
	// or not any mail matches, so it says nothing of the mail.
	NoticeScheduleDue WebhookNoticeType = "schedule.due"
)

// WebhookNoticeVersion is the version of WebhookNotice.
const WebhookNoticeVersion = 1

// WebhookNotice is the body of a webhook call, signed as Standard Webhooks'
// asymmetric "v2a" says (Ed25519 over the SHA-256 of the subscription, the
// identifier, the timestamp and the body), with the headers WebhookHeaderID,
// WebhookHeaderTimestamp and WebhookHeaderSignature (ADR 0039, "The notice",
// "Signatures"). It holds no mail, no address, no account, mailbox,
// connection or message identifier and no count: only that there is
// something to fetch, for which subscription, on which webhook (Audience,
// the URL called: a vendor refuses a notice whose Audience is not its own)
// and from when. ID and the whole body are frozen once the first call of a
// notice starts: every retry carries both, so the vendor takes it once (and
// keeps its identifier for webhook.DedupKeepFor); what happens meanwhile is
// told by a notice of its own, with another identifier. CreatedAt is when
// it was made. Since is the minute from which to fetch: for
// NoticeMailMatched the minute the earliest message it is about arrived
// in, for NoticeScheduleDue the coverage boundary (the scheduled time of
// the last scheduled notice the vendor took, or the minute the connection
// was made), so a late notice standing for several runs covers all of
// them. ScheduledAt is the scheduled time a NoticeScheduleDue is for, and
// Late says it is sent later than that (the service was down, or runs were
// missed: one notice stands for all of them).
type WebhookNotice struct {
	V            int               `json:"v"`
	Type         WebhookNoticeType `json:"type"`
	ID           string            `json:"id"`
	Subscription string            `json:"subscription"`
	ClientID     string            `json:"client_id"`
	Audience     string            `json:"aud"`
	CreatedAt    time.Time         `json:"created_at"`
	Since        time.Time         `json:"since"`
	ScheduledAt  time.Time         `json:"scheduled_at,omitzero"`
	Late         bool              `json:"late,omitempty"`
}

// The headers of a webhook call (Standard Webhooks).
const (
	WebhookHeaderID        = "webhook-id"
	WebhookHeaderTimestamp = "webhook-timestamp"
	WebhookHeaderSignature = "webhook-signature"
)

// ErrTimeZoneInvalid refuses an approval of a request with a schedule
// whose time zone is absent or not one the image knows (an IANA name).
var ErrTimeZoneInvalid = &Error{"time_zone_invalid", "that time zone is not one Permaily knows; choose another"}
