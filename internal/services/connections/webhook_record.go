package connections

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/webhook"
)

// A connection's webhook in its record (ADR 0039, "Where the webhook comes
// from", "The consent screen", "Asking again"): what decides is readable
// and signed with the record (whether it has one, what it is told and its
// state); its URL, its subscription and the person's time zone are sealed
// under PurposeWebhookSeal, bound to the connection, so a reader of the
// table finds none of them (K25).

// webhookDoc is a connection's webhook as its record keeps it.
type webhookDoc struct {
	NewMail  bool                       `json:"new_mail,omitempty"`
	Schedule *contracts.WebhookSchedule `json:"schedule,omitempty"`
	State    contracts.WebhookState     `json:"state"`
	// Sealed is webhookPrivate, sealed under PurposeWebhookSeal.
	Sealed []byte `json:"sealed"`

	private *webhookPrivate
}

// webhookPrivate is what only the enclave reads of a webhook.
type webhookPrivate struct {
	URL          string `json:"url"`
	Subscription string `json:"subscription"`
	TimeZone     string `json:"time_zone,omitempty"`
}

func webhookAAD(id string) []byte { return envelope.AAD("connection webhook", id) }

// sealWebhook seals a webhook's private part for connection id.
func (s *Service) sealWebhook(id string, w *webhookDoc) error {
	raw, err := json.Marshal(w.private)
	if err != nil {
		return err
	}
	w.Sealed, err = envelope.Seal(s.hooks.key, raw, webhookAAD(id))
	return err
}

// openWebhook opens the webhook of a record read: a part that does not
// open (moved from another record, made by hand) is errDocument.
func (s *Service) openWebhook(d *connDoc) error {
	if d.Webhook == nil {
		return nil
	}
	raw, err := envelope.Open(s.hooks.key, d.Webhook.Sealed, webhookAAD(d.ID))
	var p webhookPrivate
	if err != nil || json.Unmarshal(raw, &p) != nil || !strings.HasPrefix(p.Subscription, webhook.SubscriptionPrefix) {
		return errDocument
	}
	d.Webhook.private = &p
	return nil
}

// clone is a copy of w that shares nothing with it.
func (w *webhookDoc) clone() *webhookDoc {
	if w == nil {
		return nil
	}
	c := *w
	c.Sealed = append([]byte(nil), w.Sealed...)
	if w.Schedule != nil {
		sc := *w.Schedule
		c.Schedule = &sc
	}
	if w.private != nil {
		p := *w.private
		c.private = &p
	}
	return &c
}

// asRequest is the webhook as a request would ask for it (webhook.Same).
func (w *webhookDoc) asRequest() *contracts.WebhookRequest {
	if w == nil || w.private == nil {
		return nil
	}
	return &contracts.WebhookRequest{URL: w.private.URL, NewMail: w.NewMail, Schedule: copySchedule(w.Schedule)}
}

func copySchedule(s *contracts.WebhookSchedule) *contracts.WebhookSchedule {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// webhookHost is a webhook's URL's host, which is its client_id's
// (webhook.URL).
func webhookHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// deployment is the domain the service runs under, from its issuer
// ("https://api.<domain>"): a webhook may not be on it (webhook.URL).
func (s *Service) deployment() string {
	host := webhookHost(s.cfg.OAuth.Issuer)
	return strings.TrimPrefix(host, "api.")
}

// parseWebhook reads a pushed request's webhook for its client: nil when
// none was asked. A request with one is refused while the service has no
// queue to call it (fail closed).
func (s *Service) parseWebhook(raw, clientID string) (*contracts.WebhookRequest, error) {
	if raw == "" {
		return nil, nil
	}
	if s.cfg.Webhooks == nil {
		return nil, webhook.ErrRequest
	}
	return webhook.ParseRequest(raw, clientID, s.deployment())
}

// webhookAsked is what the consent screen shows of a request's webhook.
func webhookAsked(req *requestEntry) *contracts.WebhookAsked {
	if req.Webhook == nil {
		return nil
	}
	return &contracts.WebhookAsked{Host: webhookHost(req.Webhook.URL), NewMail: req.Webhook.NewMail, Schedule: copySchedule(req.Webhook.Schedule)}
}

// webhookZone checks the person's time zone sent with an approval: a
// request whose webhook has a schedule needs one the image knows
// (contracts.ErrTimeZoneInvalid); any other's is ignored ("").
func webhookZone(req *requestEntry, zone string) (string, error) {
	if req.Webhook == nil || req.Webhook.Schedule == nil {
		return "", nil
	}
	if _, err := webhook.Zone(zone); err != nil {
		return "", contracts.ErrTimeZoneInvalid
	}
	return zone, nil
}

// keepsWebhook says a request may be added to the client's connection as
// another device as far as its webhook goes (ADR 0039, "Asking again"): it
// asks for none, or for the same one (webhook.Same) while it is on. Any
// other (a webhook where there was none, another URL, another schedule,
// or one asked again after it was turned off or stopped, which only a new
// request turns on) replaces the connection.
func keepsWebhook(cur *connDoc, req *requestEntry) bool {
	if req.Webhook == nil {
		return true
	}
	return cur.Webhook != nil && cur.Webhook.State == contracts.WebhookOn && webhook.Same(cur.Webhook.asRequest(), req.Webhook)
}

// newWebhook is the webhook of a connection made for req, on, with a
// subscription of its own; nil when the request asked for none.
func (s *Service) newWebhook(req *requestEntry, id string) (*webhookDoc, error) {
	if req.Webhook == nil {
		return nil, nil
	}
	b := make([]byte, 16)
	if err := s.cfg.Random(b); err != nil {
		return nil, err
	}
	w := &webhookDoc{
		NewMail: req.Webhook.NewMail, Schedule: copySchedule(req.Webhook.Schedule), State: contracts.WebhookOn,
		private: &webhookPrivate{URL: req.Webhook.URL, Subscription: webhook.SubscriptionPrefix + idEncoding.EncodeToString(b), TimeZone: req.timeZone},
	}
	if err := s.sealWebhook(id, w); err != nil {
		return nil, err
	}
	return w, nil
}

// webhookAtMost is the largest webhook a request may carry, for
// requestFits.
func webhookAtMost() *webhookDoc {
	return &webhookDoc{
		NewMail: true, Schedule: &contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "wednesday", At: "23:45"}, State: contracts.WebhookStopped,
		Sealed: make([]byte, webhook.RequestBytesMax+webhook.ZoneNameBytesMax+128),
	}
}

// consentWebhook is the webhook a consent receipt records: what was asked,
// and the zone the person chose.
func consentWebhook(req *requestEntry) *receipts.ConsentWebhook {
	if req.Webhook == nil {
		return nil
	}
	return &receipts.ConsentWebhook{Host: webhookHost(req.Webhook.URL), NewMail: req.Webhook.NewMail, Schedule: copySchedule(req.Webhook.Schedule), TimeZone: req.timeZone, State: contracts.WebhookOn}
}

// connectionWebhook is the connection's webhook as the person's app shows
// it: from the record, with the queue's state (WebhookStates) when it can
// be read; nothing of the queue decides anything shown here but the times.
func (s *Service) connectionWebhook(ctx context.Context, d *connDoc) *contracts.ConnectionWebhook {
	if d.Webhook == nil || d.Webhook.private == nil {
		return nil
	}
	w := &contracts.ConnectionWebhook{
		Host: webhookHost(d.Webhook.private.URL), NewMail: d.Webhook.NewMail, Schedule: copySchedule(d.Webhook.Schedule),
		TimeZone: d.Webhook.private.TimeZone, State: d.Webhook.State,
	}
	if s.cfg.WebhookStates == nil {
		return w
	}
	st, err := s.cfg.WebhookStates.States(ctx, d.ID)
	if err != nil || st == nil {
		return w
	}
	w.LastSentAt, w.FailingSince, w.Failure = st.LastSentAt, st.FailingSince, st.Failure
	if w.State == contracts.WebhookOn && d.live(s.now()) {
		w.NextAt = st.NextAt
	}
	return w
}

// vendorWebhook is what the vendor is told of its connection's webhook:
// never the person's time zone.
func vendorWebhook(d *connDoc) *contracts.VendorWebhook {
	if d.Webhook == nil || d.Webhook.private == nil {
		return nil
	}
	return &contracts.VendorWebhook{
		Subscription: d.Webhook.private.Subscription, URL: d.Webhook.private.URL, NewMail: d.Webhook.NewMail,
		Schedule: copySchedule(d.Webhook.Schedule), State: d.Webhook.State,
	}
}

// webhookWriteBy is when a call admitted on a read that began at read is
// written by, or not at all: oauth.ConnectionFresh after it, never after
// the connection's end.
func webhookWriteBy(read, end time.Time) time.Time {
	by := read.Add(oauth.ConnectionFresh)
	if end.Before(by) {
		return end
	}
	return by
}
