package attestation

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// webhooksStatus is a section that keeps every promise, against
// webhooksWant.
func webhooksStatus() *WebhooksStatus {
	w := &WebhooksStatus{
		Signature: WebhookSignatureV2a, SpecRevision: "3d65b1824b6697f05a5a4584e7062bd23512fc8e",
		Keys:     []WebhookKey{{ID: "wk1", X: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}},
		KeysURL:  "https://api.permaily.com/.well-known/permaily-webhook-keys",
		SameHost: true, Redirects: "none", Transport: "h2", NewMail: WebhookNewMailAfterRules, RevocationSeconds: 5,
		MinIntervalSeconds: 60, TimeoutSeconds: 10, GiveUpSeconds: 259200, DedupSeconds: 259800, HostInFlight: 2, HostAttemptsPerMinute: 60,
		Schedules: []string{"day", "week"}, Since: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		Outcomes: map[string]int64{}, Skipped: map[string]int64{},
	}
	for _, k := range WebhookOutcomes {
		w.Outcomes[k] = 0
	}
	for _, k := range WebhookSkips {
		w.Skipped[k] = 0
	}
	return w
}

var webhooksWant = WebhooksStatus{
	SpecRevision: "3d65b1824b6697f05a5a4584e7062bd23512fc8e", KeysURL: "https://api.permaily.com/.well-known/permaily-webhook-keys",
	MinIntervalSeconds: 60, TimeoutSeconds: 10, GiveUpSeconds: 259200, DedupSeconds: 259800, HostInFlight: 2, HostAttemptsPerMinute: 60,
	Schedules: []string{"day", "week"},
}

// K58 (ADR 0039, "What /status and permaily-verify show"), the library's
// half: Check passes a section that keeps every promise, with counts, and
// fails on each case the design names, a key that is none, and counts of
// a kind missing, unknown or below zero.
func TestK58TheWebhooksCheck(t *testing.T) {
	if err := webhooksStatus().Check(webhooksWant); err != nil {
		t.Fatalf("a section that keeps every promise: %v", err)
	}
	counted := webhooksStatus()
	counted.Outcomes["sent"], counted.Skipped["changed"] = 3, 1
	counted.RevocationSeconds = 0
	if err := counted.Check(webhooksWant); err != nil {
		t.Fatalf("with counts: %v", err)
	}
	for name, c := range map[string]struct {
		change func(*WebhooksStatus)
		says   string
	}{
		"v1, an HMAC":                {func(w *WebhooksStatus) { w.Signature = "standard-webhooks-v1" }, "signed"},
		"v1a, deprecated":            {func(w *WebhooksStatus) { w.Signature = "standard-webhooks-v1a" }, "signed"},
		"another revision":           {func(w *WebhooksStatus) { w.SpecRevision = "0000000" }, "revision"},
		"mail in a notice":           {func(w *WebhooksStatus) { w.NoticeHoldsMail = true }, "holds mail"},
		"another host":               {func(w *WebhooksStatus) { w.SameHost = false }, "another host"},
		"redirects followed":         {func(w *WebhooksStatus) { w.Redirects = "same_host" }, "redirects"},
		"HTTP/1.1":                   {func(w *WebhooksStatus) { w.Transport = "http/1.1" }, "not h2"},
		"new mail before the rules":  {func(w *WebhooksStatus) { w.NewMail = "exact" }, "new mail"},
		"six seconds":                {func(w *WebhooksStatus) { w.RevocationSeconds = 6 }, "6 seconds"},
		"below zero seconds":         {func(w *WebhooksStatus) { w.RevocationSeconds = -1 }, "-1 seconds"},
		"keys elsewhere":             {func(w *WebhooksStatus) { w.KeysURL = "https://keys.evil.example/.well-known/permaily-webhook-keys" }, "published at"},
		"another interval":           {func(w *WebhooksStatus) { w.MinIntervalSeconds = 1 }, "bounds"},
		"another give-up":            {func(w *WebhooksStatus) { w.GiveUpSeconds = 1 }, "bounds"},
		"a shorter keeping":          {func(w *WebhooksStatus) { w.DedupSeconds = 300 }, "bounds"},
		"a larger share":             {func(w *WebhooksStatus) { w.HostInFlight = 16 }, "share"},
		"cron":                       {func(w *WebhooksStatus) { w.Schedules = append(w.Schedules, "cron") }, "schedules"},
		"no key":                     {func(w *WebhooksStatus) { w.Keys = nil }, "no key"},
		"a key named twice":          {func(w *WebhooksStatus) { w.Keys = append(w.Keys, w.Keys[0]) }, "twice"},
		"a key without a name":       {func(w *WebhooksStatus) { w.Keys[0].ID = "" }, "empty or twice"},
		"a key that is none":         {func(w *WebhooksStatus) { w.Keys[0].X = "AAAA" }, "not an Ed25519"},
		"a key padded":               {func(w *WebhooksStatus) { w.Keys[0].X += "=" }, "not an Ed25519"},
		"an outcome not counted":     {func(w *WebhooksStatus) { delete(w.Outcomes, "cut") }, `"cut" are not counted`},
		"an outcome unknown":         {func(w *WebhooksStatus) { w.Outcomes["https://trip-app.com"] = 1 }, "does not know"},
		"an outcome below zero":      {func(w *WebhooksStatus) { w.Outcomes["sent"] = -1 }, "below zero"},
		"a reason not counted":       {func(w *WebhooksStatus) { delete(w.Skipped, "changed") }, `"changed" are not counted`},
		"a reason unknown":           {func(w *WebhooksStatus) { w.Skipped["sub_x"] = 0 }, "does not know"},
		"a reason below zero":        {func(w *WebhooksStatus) { w.Skipped["off"] = -2 }, "below zero"},
		"a nil section":              {nil, "nothing of webhooks"},
		"the schedules out of order": {func(w *WebhooksStatus) { w.Schedules = []string{"week", "day"} }, "schedules"},
	} {
		var w *WebhooksStatus
		if c.change != nil {
			w = webhooksStatus()
			c.change(w)
		}
		if err := w.Check(webhooksWant); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// K58: the published key set is compared with /status's keys exactly.
func TestK58SameWebhookKeys(t *testing.T) {
	w := webhooksStatus()
	x := w.Keys[0].X
	if err := w.SameWebhookKeys(map[string]string{"wk1": x}); err != nil {
		t.Fatal(err)
	}
	other := base64.RawURLEncoding.EncodeToString(append(make([]byte, 31), 1))
	for name, published := range map[string]map[string]string{
		"another key":       {"wk1": other},
		"another name":      {"wk2": x},
		"a key more":        {"wk1": x, "wk2": other},
		"none":              {},
		"the right one too": {"wk0": other, "wk1": x},
	} {
		if err := w.SameWebhookKeys(published); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// FetchAttested reads only over HTTPS, from the attested key, a 200.
func TestFetchAttested(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer srv.Close()
	client := srv.Client()
	key := SPKIHash(srv.Certificate())
	ctx := context.Background()
	if b, err := FetchAttested(ctx, client, srv.URL+"/keys", key); err != nil || string(b) != `{"keys":[]}` {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := FetchAttested(ctx, client, srv.URL+"/keys", make([]byte, 32)); err != ErrNotBound {
		t.Fatalf("another key: %v", err)
	}
	if _, err := FetchAttested(ctx, client, srv.URL+"/missing", key); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("a 404: %v", err)
	}
	if _, err := FetchAttested(ctx, client, "http"+strings.TrimPrefix(srv.URL, "https")+"/keys", key); err != ErrNotTLS {
		t.Fatalf("over http: %v", err)
	}
}
