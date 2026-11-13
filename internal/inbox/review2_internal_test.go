package inbox

import (
	"bytes"
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The second code review of pull request 5, N8: a plain-text message whose
// body says "/html" (a URL) asked for a render slot and, when the slots
// were busy, said its formatted view was unavailable, though it has no HTML
// part. Whether a message has one comes from the MIME walk that reads its
// text: none means no html field and no slot, whatever the body says.
func TestReviewN8APlainMessageAsksNoSlotWhateverItsBodySays(t *testing.T) {
	b := Box{MailboxID: "mbx", Key: bytes.Repeat([]byte{7}, 32)}
	asked := 0
	busy := admitFunc(func(context.Context) (context.Context, func(), error) {
		asked++
		return nil, nil, contracts.ErrBusy
	})
	ctx := WithRenderAdmission(context.Background(), busy)
	for _, raw := range []string{
		"Content-Type: text/plain\r\n\r\nthe page is at https://e.example/index/html and /HTML",
		"Content-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nsee /html\r\n" +
			"--m\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"page.html\"\r\n\r\n<p>x</p>\r\n--m--\r\n",
		"Content-Type: text/plain; x-note=\"text/html\"\r\n\r\nhello",
	} {
		asked = 0
		text, _, _, h := messageTextAndHTML(ctx, []byte(raw), b, "m1")
		if h != nil || asked != 0 || text == "" {
			t.Errorf("%.60q: html %+v, a slot asked %d times, text %q", raw, h, asked, text)
		}
	}
	// An HTML part after the text part is seen by the same walk: it asks
	// for its slot, and busy says so.
	alt := "Content-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--a\r\nContent-Type: text/html\r\n\r\n<p>hello</p>\r\n--a--\r\n"
	asked = 0
	if text, from, _, h := messageTextAndHTML(ctx, []byte(alt), b, "m1"); h == nil || h.Status != contracts.HTMLUnavailable || asked != 1 || text != "hello" || from != contracts.TextFromPlain {
		t.Errorf("an HTML part after the text: %+v, asked %d, %q from %s", h, asked, text, from)
	}
}
