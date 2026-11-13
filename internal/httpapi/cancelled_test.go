package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// A request its client abandoned (the inbox replaces a search still on its
// way, a tab is closed) ends with the request's own context cancelled. That
// is not an error of the service, and the log, which is public and read for
// alarms, says nothing of it.

// leaves runs h on a request whose client goes away while the service works:
// leave is called from inside the service, and cancels the request's context
// as net/http does when the connection closes.
func leaves(h http.Handler, cookie, method, url string) (leave context.CancelFunc, run func()) {
	ctx, cancel := context.WithCancel(context.Background())
	return cancel, func() {
		r := httptest.NewRequestWithContext(ctx, method, url, nil)
		r.Header.Set("Origin", appOrigin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
}

func errorLines(logs string) []string {
	var out []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"level":"ERROR"`) || strings.Contains(l, "level=ERROR") {
			out = append(out, l)
		}
	}
	return out
}

func TestAMailboxRequestItsClientLeftLogsNoError(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	a.seed(box.ID, plainRaw, inbox.StoredMeta{})
	leave, run := leaves(a.h, "alice", "GET", "https://app.permaily.test/api/v1/mailboxes/"+box.ID+"/messages")
	a.mem.Interrupt = func(ctx context.Context, _ store.Step, _, _ string) error {
		leave()
		<-ctx.Done()
		return fmt.Errorf("store: read: %w", ctx.Err())
	}
	run()
	if lines := errorLines(a.logs.String()); len(lines) != 0 {
		t.Fatalf("a client that left is logged as an error:\n%s", strings.Join(lines, "\n"))
	}
}

// The deadline the service sets itself is still the service's failure:
// answered and logged as before.
func TestAMailboxRequestPastTheServicesDeadlineIsStillLogged(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.Deadline = 20 * time.Millisecond })
	box := a.fake.Add(alice, "Mine")
	a.seed(box.ID, plainRaw, inbox.StoredMeta{})
	a.mem.Interrupt = func(ctx context.Context, _ store.Step, _, _ string) error {
		<-ctx.Done()
		return fmt.Errorf("store: read: %w", ctx.Err())
	}
	want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages", ""), 500, "internal")
	if lines := errorLines(a.logs.String()); len(lines) != 1 || !strings.Contains(lines[0], "past the request's deadline") {
		t.Fatalf("log:\n%s", a.logs)
	}
}

// waiting is an accounts service whose every call waits for its context, and
// leave is called once it started waiting.
type waiting struct {
	httpapi.AccountsService
	leave func()
}

func (s waiting) StartEmail(ctx context.Context, _ contracts.EmailStart) (*contracts.EmailStarted, error) {
	s.leave()
	<-ctx.Done()
	return nil, fmt.Errorf("store: entries: %w", ctx.Err())
}

func TestAnAccountRequestItsClientLeftLogsNoError(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	h := httpapi.AccountsAPI(waiting{leave: cancel}, log)
	r := httptest.NewRequestWithContext(ctx, "POST", appOrigin+"/api/v1/auth/email/start", strings.NewReader(`{"address":"ana@example.org"}`))
	r.Header.Set("Origin", appOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if lines := errorLines(logs.String()); len(lines) != 0 {
		t.Fatalf("a client that left is logged as an error:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAnAccountRequestPastADeadlineIsStillLogged(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	// A deadline under the request's context, as a service sets one: the
	// request's own context is cancelled by nobody.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Millisecond)
		defer cancel()
		httpapi.AccountsAPI(waiting{leave: func() {}}, log).ServeHTTP(w, r.WithContext(ctx))
	})
	r := httptest.NewRequest("POST", appOrigin+"/api/v1/auth/email/start", strings.NewReader(`{"address":"ana@example.org"}`))
	r.Header.Set("Origin", appOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	want(t, rec, 500, "internal")
	if lines := errorLines(logs.String()); len(lines) != 1 || !strings.Contains(lines[0], "api: internal error") {
		t.Fatalf("log:\n%s", logs.String())
	}
}

// The operator's and the admins' mailbox routes answer through the same
// helper: a client that left is not logged there either.
func TestAnOperatorRequestItsClientLeftLogsNoError(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	h := served(nil, httpapi.Mailboxes(leavingMailboxes{leave: cancel}, slog.New(slog.NewTextHandler(&logs, nil))))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "POST", "https://api.permaily.test/v1/admin/mailboxes/mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa/pause", nil))
	if lines := errorLines(logs.String()); len(lines) != 0 {
		t.Fatalf("a client that left is logged as an error:\n%s", strings.Join(lines, "\n"))
	}
}

type leavingMailboxes struct {
	brokenMailboxes
	leave func()
}

func (m leavingMailboxes) PauseMailbox(ctx context.Context, _ string) (*contracts.MailboxChange, error) {
	m.leave()
	<-ctx.Done()
	return nil, fmt.Errorf("store: %w", ctx.Err())
}
