package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// The third code review of pull request 5, B and E: each test failed before
// its fix, and fails again when the fix is broken on purpose.

// until waits for cond, at most a bound that only a hung test reaches.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

// withState is the app a's configuration served again, with the API's own
// state for the test, edited by edit.
func withState(a *inboxApp, edit func(*httpapi.MailboxesConfig)) (http.Handler, httpapi.APIState) {
	cfg := a.cfg
	cfg.AppOrigin = appOrigin
	if edit != nil {
		edit(&cfg)
	}
	return httpapi.MailboxesAPIWithState(cfg)
}

// B: the frame's document was made, its render slot given back, and only
// then was the read reservation enlarged to its size: while that waited,
// the document was held with only the raw message's smaller reservation
// charged, by as many accounts as cared to (the reviewer held four
// documents of 717,633 bytes with 128,096 bytes charged). A finished
// document is charged to the read budget before its slot is given back: it
// waits for room under its slot (and the render's deadline), and a
// document refused is discarded, its reservation given back.
func TestReviewThirdBNoDocumentWaitsUncharged(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.AppOrigin = appOrigin })
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"
	size := int64(frameCall(a.h, "alice", path, "iframe", "same-origin").Body.Len())
	if size <= int64(len(htmlMessage(t))) {
		t.Fatalf("a document of %d bytes for a message of %d", size, len(htmlMessage(t)))
	}

	// Room for the raw message, never for the document: the document waits
	// for room until the request ends.
	h, state := withState(a, func(c *httpapi.MailboxesConfig) {
		c.ReadBudget, c.Wait, c.Renders = size-1, time.Minute, 2
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequestWithContext(ctx, "GET", "https://app.permaily.test"+path, nil)
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		done <- rec
	}()
	until(t, "the document waits for the read budget", func() bool { _, waiting := state.Held(); return waiting == 1 })
	held, _ := state.Held()
	if running := state.Running(); running != 1 {
		t.Errorf("a finished document of %d bytes waits with %d charged and its slot given back", size, held)
	}
	cancel()
	rec := <-done
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("the request ended while its document waited: %d", rec.Code)
	}
	wantFrameHeaders(t, "ended", rec.Header())
	if held, waiting := state.Held(); held != 0 || waiting != 0 || state.Running() != 0 || state.Documents() != 0 {
		t.Errorf("after: %d bytes held, %d waiting, %d running, %d documents", held, waiting, state.Running(), state.Documents())
	}

	// Refused at the end of the wait: discarded, answered as before (429),
	// and everything it held given back.
	h, state = withState(a, func(c *httpapi.MailboxesConfig) { c.ReadBudget, c.Wait = size-1, 50*time.Millisecond })
	rec = frameCall(h, "alice", path, "iframe", "same-origin")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("a document past the read budget: %d", rec.Code)
	}
	wantFrameHeaders(t, "refused", rec.Header())
	if held, waiting := state.Held(); held != 0 || waiting != 0 || state.Running() != 0 || state.Documents() != 0 {
		t.Errorf("after a refusal: %d bytes held, %d waiting, %d running, %d documents", held, waiting, state.Running(), state.Documents())
	}
}

// B: a document written to a reader that does not read is charged in full,
// and holds no slot.
func TestReviewThirdBAWaitingDocumentIsCharged(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.AppOrigin = appOrigin })
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"
	size := int64(frameCall(a.h, "alice", path, "iframe", "same-origin").Body.Len())
	h, state := withState(a, nil)
	w := newBlockedWriter()
	finished := make(chan struct{})
	go func() { frameTo(h, w, "alice", path); close(finished) }()
	<-w.writing
	if held, _ := state.Held(); held < size || state.Running() != 0 {
		t.Errorf("a document of %d bytes waits for its reader with %d charged and %d slots", size, held, state.Running())
	}
	close(w.release)
	<-finished
	if held, _ := state.Held(); held != 0 || state.Documents() != 0 {
		t.Errorf("after: %d held, %d documents", held, state.Documents())
	}
}

// E: the frame's route and the link route wrote their early errors (no
// such message, no HTML part, HTML not shown, the render's deadline) while
// they held their render slot, which a reader that does not read then held:
// with one slot, a second account was answered 503 behind it. No answer is
// written while a slot is held: for each answer, alice's reader does not
// read, and bob's render runs.
func TestReviewThirdENoAnswerIsWrittenUnderASlot(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 1, 2, time.Second
	})
	box := a.fake.Add(alice, "Mine")
	page := a.seed(box.ID, htmlMessage(t), plainMeta())
	text := a.seed(box.ID, "From: Ann <ann@example.org>\r\nSubject: Words\r\nContent-Type: text/plain\r\n\r\nOnly words\r\n", plainMeta())
	large := a.seed(box.ID, "From: Ann <ann@example.org>\r\nSubject: Large\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+strings.Repeat("</p>", 210_000)+"\r\n", plainMeta())
	utf7 := a.seed(box.ID, "From: Ann <ann@example.org>\r\nSubject: Seven\r\nContent-Type: text/html; charset=utf-7\r\n\r\n+ADw-p+AD4-Hallo\r\n", plainMeta())
	slow := a.seed(box.ID, htmlMessage(t), plainMeta())
	bobBox := a.fake.Add(bob, "His")
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	base := "/api/v1/mailboxes/" + box.ID + "/messages/"
	token := linkTokens(t, frameCall(a.h, "alice", base+page+"/html", "iframe", "same-origin").Body.String())[0]
	changed := token[:strings.IndexByte(token, '.')] + ".AAAAAAAAAAAAAAAA"

	// The raw read of the slow message waits for the render's deadline.
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == "msg#"+slow {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	defer func() { a.mem.Interrupt = nil }()

	frame := func(id string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) { frameTo(a.h, w, "alice", base+id+"/html") }
	}
	link := func(id, tok string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			r := httptest.NewRequest("GET", "https://app.permaily.test"+base+id+"/links/"+tok, nil)
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
			a.h.ServeHTTP(w, r)
		}
	}
	for _, c := range []struct {
		name   string
		ask    func(http.ResponseWriter)
		status int
	}{
		{"the frame: a document", frame(page), 200},
		{"the frame: no such message", frame("ffffffffffffffffffffffffffffffff"), 404},
		{"the frame: no HTML part", frame(text), 404},
		{"the frame: HTML too large", frame(large), 404},
		{"the frame: a charset not read", frame(utf7), 404},
		{"the frame: the render's deadline", frame(slow), 503},
		{"the link: its destination", link(page, token), 200},
		{"the link: changed", link(page, changed), 409},
		{"the link: no such message", link("ffffffffffffffffffffffffffffffff", token), 404},
		{"the link: no HTML part", link(text, token), 404},
		{"the link: HTML too large", link(large, token), 404},
		{"the link: the render's deadline", link(slow, token), 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := inbox.RenderDeadline
			if strings.HasSuffix(c.name, "deadline") {
				inbox.RenderDeadline = 30 * time.Millisecond
			}
			w := newBlockedWriter()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); c.ask(w) }()
			defer func() { close(w.release); wg.Wait() }()
			select {
			case <-w.writing:
			case <-time.After(10 * time.Second):
				t.Fatal("never written")
			}
			inbox.RenderDeadline = old                                              // bob's render runs under the real one
			if code := w.code; code != c.status && (code != 0 || c.status != 200) { // 0: written without WriteHeader, 200
				t.Errorf("alice's answer: %d, want %d", code, c.status)
			}
			if rec := frameCall(a.h, "bob", "/api/v1/mailboxes/"+bobBox.ID+"/messages/"+bobMsg+"/html", "iframe", "same-origin"); rec.Code != 200 {
				t.Errorf("bob's render behind alice's reader: %d", rec.Code)
			}
		})
	}
}
