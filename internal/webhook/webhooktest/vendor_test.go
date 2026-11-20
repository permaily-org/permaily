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
	ts, sig := webhook.Sign([]ed25519.PrivateKey{k}, n.ID, at, body)
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
	v := &Vendor{Keys: []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}, Audience: "https://trip-app.com/h", Now: func() time.Time { return start }}
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
	for name, resp := range map[string]*http.Response{
		"another key":    call(t, srv, other, n, start, nil),
		"too old":        call(t, srv, k, n, start.Add(-10*time.Minute), nil),
		"another URL":    call(t, srv, k, elsewhere, start, nil),
		"another id":     call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set(contracts.WebhookHeaderID, "ntc_x") }),
		"with a cookie":  call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set("Cookie", "a=b") }),
		"not JSON typed": call(t, srv, k, n, start, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }),
	} {
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	if v.Refused() != 6 || v.Calls() != 8 {
		t.Errorf("refused %d of %d", v.Refused(), v.Calls())
	}
	v.Status = http.StatusGone
	n.ID = "ntc_" + "00000000000000000000000002"
	if resp := call(t, srv, k, n, start, nil); resp.StatusCode != http.StatusGone {
		t.Error(resp.Status)
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
