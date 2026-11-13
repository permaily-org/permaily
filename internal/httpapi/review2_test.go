package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
)

// The second code review of pull request 5, N6 and N8: each test failed
// before its fix, and fails again when the fix is broken on purpose.

// blockedWriter is a reader that does not read: its first Write says so on
// writing and waits for release.
type blockedWriter struct {
	header  http.Header
	code    int
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockedWriter() *blockedWriter {
	return &blockedWriter{header: http.Header{}, writing: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockedWriter) Header() http.Header { return b.header }
func (b *blockedWriter) WriteHeader(c int)   { b.code = c }
func (b *blockedWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.writing) })
	<-b.release
	return len(p), nil
}

// frameTo asks for the frame's document as alice's or bob's frame, into w.
func frameTo(h http.Handler, w http.ResponseWriter, cookie, path string) {
	r := httptest.NewRequest("GET", "https://app.permaily.test"+path, nil)
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
	h.ServeHTTP(w, r)
}

// N6: a render slot covered the transfer: the slot was given back by a
// defer after the response was written, which only the server's write
// timeout bounds, so a reader that does not read held it, and a second
// account was answered 503 behind it. The slot covers the render; the
// finished document waits for its reader against the read budget, and an
// account has at most two waiting.
func TestReviewN6ASlowReaderDoesNotHoldARenderSlot(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 1, 2, 300*time.Millisecond
	})
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	bobBox := a.fake.Add(bob, "His")
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"

	token := linkTokens(t, frameCall(a.h, "alice", path, "iframe", "same-origin").Body.String())[0]
	var blocked []*blockedWriter
	var done sync.WaitGroup
	defer func() {
		for _, b := range blocked {
			close(b.release)
		}
		done.Wait()
	}()
	for range httpapi.DocumentsWaitingPerAccount {
		b := newBlockedWriter()
		blocked = append(blocked, b)
		done.Add(1)
		go func() {
			defer done.Done()
			frameTo(a.h, b, "alice", path)
		}()
		select {
		case <-b.writing:
		case <-time.After(5 * time.Second):
			t.Fatal("the frame's document was never written")
		}
	}
	// The link route too: its answer waits for a reader that does not read,
	// without the slot.
	lw := newBlockedWriter()
	blocked = append(blocked, lw)
	done.Add(1)
	go func() {
		defer done.Done()
		r := httptest.NewRequest("GET", "https://app.permaily.test/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/links/"+token, nil)
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
		a.h.ServeHTTP(lw, r)
	}()
	select {
	case <-lw.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the link's answer was never written")
	}
	// Alice's renders are done; their answers wait for her. Bob's render
	// takes the one slot at once.
	if rec := frameCall(a.h, "bob", "/api/v1/mailboxes/"+bobBox.ID+"/messages/"+bobMsg+"/html", "iframe", "same-origin"); rec.Code != 200 {
		t.Fatalf("a second account behind a slow reader: %d", rec.Code)
	}
	// Alice cannot leave more documents waiting than her share.
	rec := frameCall(a.h, "alice", path, "iframe", "same-origin")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("a third document waiting for alice: %d", rec.Code)
	}
	wantFrameHeaders(t, "too many waiting", rec.Header())
}

// N6: the finished documents waiting for their readers are held against
// the read budget: when it is full of them, a render's document waits for
// room at most the admission's wait, and is answered 429 after it.
func TestReviewN6WaitingDocumentsAreHeldAgainstTheReadBudget(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"
	size := int64(frameCall(a.h, "alice", path, "iframe", "same-origin").Body.Len())
	bobBox := a.fake.Add(bob, "His")
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	// Room for one document and a raw message, not for two documents.
	b := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.ReadBudget = size + int64(len(htmlMessage(t))) + 64
		c.Wait = 100 * time.Millisecond
	})
	b.fake, b.mem = a.fake, a.mem
	b.reader = a.reader
	b.cfg.Service, b.cfg.Reader = a.cfg.Service, a.cfg.Reader
	h := httpapi.MailboxesAPI(b.cfg)
	w := newBlockedWriter()
	go frameTo(h, w, "alice", path)
	defer close(w.release)
	select {
	case <-w.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the frame's document was never written")
	}
	if rec := frameCall(h, "bob", "/api/v1/mailboxes/"+bobBox.ID+"/messages/"+bobMsg+"/html", "iframe", "same-origin"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a second document past the read budget: %d", rec.Code)
	}
}

// What the renders and the mail open for reading may hold together stays a
// quarter of the enclave's 2,048 MiB, so that with Go's collector, which
// lets the heap grow to twice what is live, they take at most half of it
// (ADR 0026, "Limits"): two renders at their peak, and the read budget,
// which holds the raw messages being read and the documents waiting for
// their readers.
func TestRendersAndReadsFitTheEnclave(t *testing.T) {
	if total := contracts.MessageHTMLRenders*contracts.MessageHTMLRenderMemory + httpapi.MailReadBudget; total > 512<<20 {
		t.Fatalf("%d MiB", total>>20)
	}
}
