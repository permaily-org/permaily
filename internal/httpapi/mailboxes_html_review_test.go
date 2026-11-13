package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/store"
)

// The code review of pull request 5 (#92), the findings on the routes:
// each test failed before its fix, and fails again when the fix is broken
// on purpose.

// Finding 4 (b): the frame's route and the link route took a slot before
// the raw read but started the render's deadline after it: with a deadline
// of 20 ms and a raw read held 100 ms, the frame answered 200 after 100 ms,
// holding its slot. The deadline starts when the slot is taken and covers
// the raw read, the MIME walk, the charset and the render.
func TestReview4BTheRenderDeadlineCoversTheRawRead(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 1, 2, time.Second
	})
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	base := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg
	doc := frameCall(a.h, "alice", base+"/html", "iframe", "same-origin")
	if doc.Code != 200 {
		t.Fatalf("%d", doc.Code)
	}
	token := linkTokens(t, doc.Body.String())[0]

	old := inbox.RenderDeadline
	inbox.RenderDeadline = 20 * time.Millisecond
	defer func() { inbox.RenderDeadline = old }()
	// Each read of the message's raw object takes 100 ms, or until the
	// request's context ends.
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step != store.StepRead || !strings.HasPrefix(key, "msg#") {
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { a.mem.Interrupt = nil }()

	start := time.Now()
	if rec := frameCall(a.h, "alice", base+"/html", "iframe", "same-origin"); rec.Code != 503 || time.Since(start) > 90*time.Millisecond {
		t.Errorf("the frame: %d after %v", rec.Code, time.Since(start))
	}
	start = time.Now()
	if rec := a.call("alice", "GET", base+"/links/"+token, ""); rec.Code != 503 || !strings.Contains(rec.Body.String(), `"busy"`) || time.Since(start) > 90*time.Millisecond {
		t.Errorf("the link: %d %s after %v", rec.Code, rec.Body, time.Since(start))
	}
	// The text is not a render: GET message answers it, whatever the
	// render's deadline did to the HTML.
	var m contracts.Message
	read(t, a.call("alice", "GET", base, ""), 200, &m)
	if m.Text == "" {
		t.Errorf("the text: %+v", m)
	}
	// Each of them gave its one slot back: one slot, so a render runs.
	a.mem.Interrupt = nil
	inbox.RenderDeadline = old
	if rec := frameCall(a.h, "alice", base+"/html", "iframe", "same-origin"); rec.Code != 200 {
		t.Errorf("after: %d", rec.Code)
	}
}

// Finding 4: GET message answers its text whatever the renders do: it
// waits for a slot for the HTML summary at most SummaryWait, not the
// queue's wait, and then says the formatted view is unavailable.
func TestReview4OpeningAMessageWaitsBehindRendersAtMostASecond(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 1, 4, 5*time.Second
	})
	box := a.fake.Add(alice, "Mine")
	bobBox := a.fake.Add(bob, "His")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	a.mem.Interrupt = func(ctx context.Context, step store.Step, partition, key string) error {
		if step == store.StepRead && partition == "mailbox#"+box.ID && key == "msg#"+msg {
			select {
			case held <- struct{}{}:
				<-release
			default:
			}
		}
		return nil
	}
	done := make(chan int, 1)
	go func() {
		done <- frameCall(a.h, "alice", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/html", "iframe", "same-origin").Code
	}()
	<-held // Alice's render holds the one slot
	start := time.Now()
	var m contracts.Message
	read(t, a.call("bob", "GET", "/api/v1/mailboxes/"+bobBox.ID+"/messages/"+bobMsg, ""), 200, &m)
	took := time.Since(start)
	if m.Text == "" || m.HTML == nil || m.HTML.Status != contracts.HTMLUnavailable || took < httpapi.SummaryWait || took > httpapi.SummaryWait+time.Second {
		t.Errorf("%+v after %v", m.HTML, took)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Errorf("the render that held the slot: %d", code)
	}
}

// Finding 9: every answer for a path of the form
// /api/v1/mailboxes/{id}/messages/{m}/html, whatever the method and
// whatever refuses it, carries the frame's headers and a fixed body: never
// the app's JSON under the app's policy, which a browser could render as a
// document of the app's origin. Through the app's host as the enclave
// mounts it: the router's headers, the cross-site rule, the API.
func TestReview9EveryAnswerOfTheFramePathIsTheFrames(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 1, 2, 2*time.Second
	})
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	bobBox := a.fake.Add(bob, "His")
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	app := http.NewServeMux()
	app.Handle("/api/v1/mailboxes/", a.h)
	app.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("the app")) }))
	h := router.New(nil, map[string]http.Handler{"app.permaily.test": httpapi.RefuseCrossSite(app)})
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"

	ask := func(method, p, cookie string, header map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://app.permaily.test/", nil)
		r.URL.Path, r.URL.RawPath = p, ""
		if strings.Contains(p, "%") {
			r.URL.RawPath = p
			r.URL.Path, _ = url.PathUnescape(p)
		}
		for k, v := range header {
			r.Header.Set(k, v)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	frame := map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate"}
	app2 := map[string]string{"Origin": appOrigin, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}
	rows := []struct {
		name, method, path, cookie string
		header                     map[string]string
		status                     int
	}{
		{"the document", "GET", path, "alice", frame, 200},
		{"HEAD", "HEAD", path, "alice", frame, 200},
		{"cross-site, with a session", "GET", path, "alice", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Dest": "iframe"}, 403},
		{"cross-site, without one", "GET", path, "", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Dest": "document"}, 403},
		{"same-site", "GET", path, "alice", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Dest": "iframe"}, 403},
		{"cross-site POST", "POST", path, "alice", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"POST from the app", "POST", path, "alice", app2, 405},
		{"POST from nowhere", "POST", path, "alice", nil, 405},
		{"PUT", "PUT", path, "alice", app2, 405},
		{"DELETE", "DELETE", path, "alice", app2, 405},
		{"OPTIONS", "OPTIONS", path, "", nil, 405},
		{"a path that is not clean", "GET", "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/./html", "alice", frame, 404},
		{"a trailing slash", "GET", path + "/", "alice", frame, 404},
		{"an escaped slash in the mailbox", "GET", "/api/v1/mailboxes/a%2Fb/messages/" + msg + "/html", "alice", frame, 404},
		{"opened as a document", "GET", path, "alice", map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin"}, 404},
		{"no session", "GET", path, "", frame, 401},
		{"another account's", "GET", path, "bob", frame, 404},
		{"no such message", "GET", "/api/v1/mailboxes/" + box.ID + "/messages/ffffffffffffffffffffffffffffffff/html", "alice", frame, 404},
	}
	for _, r := range rows {
		rec := ask(r.method, r.path, r.cookie, r.header)
		if rec.Code != r.status {
			t.Errorf("%s: %d, want %d: %q", r.name, rec.Code, r.status, rec.Body)
			continue
		}
		wantFrameHeaders(t, r.name, rec.Header())
		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Errorf("%s: the router's HSTS is gone", r.name)
		}
		if r.status == 405 && rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: Allow %q", r.name, rec.Header().Get("Allow"))
		}
		body := rec.Body.String()
		switch {
		case r.method == "HEAD":
		case r.status == 200:
			frameDocumentCheck(t, r.name, rec)
		case r.status == 401:
			if !strings.Contains(body, "Choose Text above") || !strings.HasPrefix(body, "<!DOCTYPE html>") {
				t.Errorf("%s: %q", r.name, body)
			}
		case !strings.HasPrefix(body, "<!DOCTYPE html>") || !strings.Contains(body, "This message can't be shown here.") || strings.Contains(body, "{"):
			t.Errorf("%s: %q", r.name, body)
		}
	}
	// Busy and the account's share, through the same host.
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == "msg#"+msg {
			select {
			case held <- struct{}{}:
				select {
				case <-release:
				case <-ctx.Done():
				}
			default:
			}
		}
		return nil
	}
	done := make(chan int, 3)
	go func() { done <- ask("GET", path, "alice", frame).Code }()
	<-held // running
	for range 2 {
		go func() { done <- ask("GET", path, "alice", frame).Code }() // waiting: the queue is full
	}
	time.Sleep(30 * time.Millisecond)
	for _, c := range []struct {
		name, cookie, path string
		want               int
	}{
		{"past the account's share", "alice", path, 429},
		{"past the queue", "bob", "/api/v1/mailboxes/" + bobBox.ID + "/messages/" + bobMsg + "/html", 503},
	} {
		rec := ask("GET", c.path, c.cookie, frame)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), "This message can't be shown here.") {
			t.Errorf("%s: %d %q", c.name, rec.Code, rec.Body)
		}
		wantFrameHeaders(t, c.name, rec.Header())
	}
	close(release)
	for range 3 {
		if code := <-done; code != 200 {
			t.Errorf("a render: %d", code)
		}
	}
	a.mem.Interrupt = nil
}
