package webhooktest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
)

var start = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

func call(t *testing.T, srv *httptest.Server, k ed25519.PrivateKey, n contracts.WebhookNotice, at time.Time, tamper func(*http.Request)) *http.Response {
	t.Helper()
	body, err := webhook.Body(n)
	if err != nil {
		t.Fatal(err)
	}
	ts, sig, err := webhook.Sign([]ed25519.PrivateKey{k}, n.Subscription, n.ID, at, body)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/h", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(contracts.WebhookHeaderID, n.ID)
	r.Header.Set(contracts.WebhookHeaderTimestamp, ts)
	r.Header.Set(contracts.WebhookHeaderSignature, sig)
	if tamper != nil {
		tamper(r)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// The played vendor does what a vendor must: verifies, opens for its own
// URL, takes each notice once.
func TestThePlayedVendorVerifiesAndTakesEachNoticeOnce(t *testing.T) {
	k, _ := webhook.KeyFromSeed(make([]byte, 32))
	v := &Vendor{Keys: []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}, Endpoint: "sub_" + "00000000000000000000000001", Audience: "https://trip-app.com/h", Now: func() time.Time { return start }}
	srv := httptest.NewServer(v)
	defer srv.Close()
	n := contracts.WebhookNotice{V: 1, Type: contracts.NoticeMailMatched, ID: "ntc_" + "00000000000000000000000001", Subscription: "sub_" + "00000000000000000000000001",
		ClientID: "https://trip-app.com/oauth/client.json", Audience: "https://trip-app.com/h", CreatedAt: start, Since: start}
	if resp := call(t, srv, k, n, start, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	call(t, srv, k, n, start.Add(time.Minute), nil) // a retry of the same notice
	if got := v.Taken(); len(got) != 1 || got[0] != n {
		t.Fatalf("%+v", got)
	}
	other, _ := webhook.KeyFromSeed(bytes.Repeat([]byte{1}, 32))
	elsewhere := n
	elsewhere.Audience = "https://trip-app.com/other"
	another := n
	another.Subscription = "sub_" + "00000000000000000000000009"
	for name, resp := range map[string]*http.Response{
		"another key":          call(t, srv, other, n, start, nil),
		"another subscription": call(t, srv, k, another, start, nil),
		"too old":              call(t, srv, k, n, start.Add(-10*time.Minute), nil),
		"another URL":          call(t, srv, k, elsewhere, start, nil),
		"another id":           call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set(contracts.WebhookHeaderID, "ntc_x") }),
		"with a cookie":        call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set("Cookie", "a=b") }),
		"not JSON typed":       call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }),
	} {
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	if v.Refused() != 7 || v.Calls() != 9 {
		t.Errorf("refused %d of %d", v.Refused(), v.Calls())
	}
	v.Status = http.StatusGone
	n.ID = "ntc_" + "00000000000000000000000002"
	if resp := call(t, srv, k, n, start, nil); resp.StatusCode != http.StatusGone {
		t.Error(resp.Status)
	}
}

// A vendor keeps the identifiers it took for webhook.DedupKeepFor: when a
// notice's 2xx is lost and it is called again minutes and hours later,
// freshly signed each time, it takes it once; one that kept them only five
// minutes takes every retry again, which is why the duty is the retries'
// whole life (design review, finding 9; K15).
func TestAVendorTakesARetryOnceOnlyIfItKeepsIdentifiersLongEnough(t *testing.T) {
	k, _ := webhook.KeyFromSeed(make([]byte, 32))
	n := contracts.WebhookNotice{V: 1, Type: contracts.NoticeMailMatched, ID: "ntc_" + "00000000000000000000000001", Subscription: "sub_" + "00000000000000000000000001",
		ClientID: "https://trip-app.com/oauth/client.json", Audience: "https://trip-app.com/h", CreatedAt: start, Since: start}
	for name, c := range map[string]struct {
		keep  time.Duration
		taken int
	}{"for the retries' life": {0, 1}, "for five minutes": {webhook.TimestampTolerance, 4}} {
		now := start
		v := &Vendor{Keys: []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}, Endpoint: n.Subscription, Audience: n.Audience, Now: func() time.Time { return now }, DedupFor: c.keep}
		srv := httptest.NewServer(v)
		call(t, srv, k, n, now, nil) // taken; its answer lost on the way back
		for _, after := range []time.Duration{6 * time.Minute, 21 * time.Minute, webhook.GiveUpAfter} {
			now = start.Add(after)
			call(t, srv, k, n, now, nil) // the retries, each signed when sent
		}
		if got := len(v.Taken()); got != c.taken {
			t.Errorf("%s: taken %d times, want %d", name, got, c.taken)
		}
		// Older than any retry: refused, so nothing older needs keeping.
		now = start.Add(webhook.NoticeLife + time.Minute)
		if resp := call(t, srv, k, n, now, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: a notice older than its life: %s", name, resp.Status)
		}
		srv.Close()
	}
}

func TestTheScriptedSenderAnswersInTurn(t *testing.T) {
	s := &Sender{Outcomes: []webhook.Outcome{webhook.OutcomeNoAnswer, webhook.OutcomeSent}}
	ctx := context.Background()
	got := []webhook.Outcome{s.Send(ctx, webhook.Call{ID: "a"}), s.Send(ctx, webhook.Call{ID: "b"}), s.Send(ctx, webhook.Call{ID: "c"})}
	if got[0] != webhook.OutcomeNoAnswer || got[1] != webhook.OutcomeSent || got[2] != webhook.OutcomeSent || len(s.Calls()) != 3 {
		t.Fatal(got)
	}
	if (&Sender{}).Send(ctx, webhook.Call{}) != webhook.OutcomeSent {
		t.Fatal("none scripted")
	}
}
