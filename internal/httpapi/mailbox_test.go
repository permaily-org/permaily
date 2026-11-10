package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

const (
	token   = "pmt_test_token_0123456789abcdef"
	address = "u-test@inbox.permaily.test"
)

func tokenHash() string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

func setup(t *testing.T) (http.Handler, *inbox.Service) {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	svc := inbox.New(inbox.Config{MailboxID: "test", Address: address}, store.NewMemory(), k, nil, slog.New(slog.DiscardHandler))
	if err := svc.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return httpapi.TokenAuth(tokenHash(), httpapi.MailboxAPI(svc)), svc
}

func do(h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMailboxAuth(t *testing.T) {
	h, _ := setup(t)
	for _, auth := range []string{"", "wrong", token + "x"} {
		rec := do(h, "GET", "/v1/mailbox", auth, "")
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthorized"`) {
			t.Errorf("auth %q: %d %s", auth, rec.Code, rec.Body)
		}
	}
	if rec := do(httpapi.TokenAuth("not-hex", httpapi.MailboxAPI(nil)), "GET", "/v1/mailbox", token, ""); rec.Code != 401 {
		t.Fatalf("misconfigured hash must reject: %d", rec.Code)
	}
}

func TestMailboxAPI(t *testing.T) {
	h, svc := setup(t)
	ctx := context.Background()
	for i := range 3 {
		msg := "From: a@example.org\r\nSubject: Message " + string(rune('A'+i)) + "\r\n\r\nbody\r\n"
		if _, err := svc.Ingest(ctx, "a@example.org", []string{address}, []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}

	rec := do(h, "GET", "/v1/mailbox", token, "")
	var view inbox.MailboxView
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.Address != address || view.RetentionDays != 90 {
		t.Fatalf("mailbox: %d %s", rec.Code, rec.Body)
	}

	if rec := do(h, "PATCH", "/v1/mailbox", token, `{"retention_days": 30}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"retention_days":30`) {
		t.Fatalf("set retention: %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"retention_days": 31}`, `{}`, `{"retention_days": 30, "x": 1}`, `nope`} {
		if rec := do(h, "PATCH", "/v1/mailbox", token, body); rec.Code != 400 {
			t.Errorf("bad body %s: %d", body, rec.Code)
		}
	}

	rec = do(h, "GET", "/v1/mailbox/messages?limit=2", token, "")
	var page struct {
		Messages []inbox.Summary `json:"messages"`
		Next     string          `json:"next"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Messages) != 2 || page.Next == "" || page.Messages[0].Subject != "Message C" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/v1/mailbox/messages?before="+page.Next, token, "")
	if !strings.Contains(rec.Body.String(), "Message A") || strings.Contains(rec.Body.String(), "Message B") {
		t.Fatalf("second page: %s", rec.Body)
	}
	if rec := do(h, "GET", "/v1/mailbox/messages?limit=0", token, ""); rec.Code != 400 {
		t.Fatalf("limit=0: %d", rec.Code)
	}

	id := page.Messages[0].ID
	if rec := do(h, "GET", "/v1/mailbox/messages/"+id, token, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"subject":"Message C"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/v1/mailbox/messages/"+id+"/raw", token, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "message/rfc822" || !strings.HasPrefix(rec.Body.String(), "From: a@example.org") {
		t.Fatalf("raw: %d %s", rec.Code, rec.Header())
	}
	if rec := do(h, "DELETE", "/v1/mailbox/messages/"+id, token, ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/mailbox/messages/"+id, token, ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
	}
}
