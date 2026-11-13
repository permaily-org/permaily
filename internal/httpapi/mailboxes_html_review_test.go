package httpapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
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
