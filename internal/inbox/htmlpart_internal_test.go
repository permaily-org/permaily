package inbox

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The HTML part a mail client would show (ADR 0026, "The sanitiser",
// "Input"): the last text/html of a multipart/alternative, else the first
// inline text/html; the parts of its multipart/related by Content-ID; its
// bytes as the sender wrote them, with the charset its header names.
func TestTheHTMLPartAMailClientWouldShow(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, raw, want, charset string
		inline                   []string
	}{
		{"html alone", "Content-Type: text/html; charset=iso-8859-1\r\n\r\n<p>Caf\xe9</p>", "<p>Caf\xe9</p>", "iso-8859-1", nil},
		{"the last of an alternative",
			"Content-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nplain\r\n" +
				"--a\r\nContent-Type: text/html\r\n\r\n<p>first</p>\r\n--a\r\nContent-Type: text/html\r\n\r\n<p>last</p>\r\n--a--\r\n", "<p>last</p>", "", nil},
		{"the first inline html of a mixed",
			"Content-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/html\r\nContent-Disposition: attachment\r\n\r\n<p>attached</p>\r\n" +
				"--m\r\nContent-Type: text/html\r\n\r\n<p>one</p>\r\n--m\r\nContent-Type: text/html\r\n\r\n<p>two</p>\r\n--m--\r\n", "<p>one</p>", "", nil},
		{"related, its parts by Content-ID",
			"Content-Type: multipart/related; boundary=r\r\n\r\n--r\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\nx\r\n--a\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n<img src=3D\"cid:logo@x\">\r\n--a--\r\n" +
				"--r\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n" +
				"--r\r\nContent-Type: image/gif\r\nContent-ID: <logo@x>\r\n\r\nsecond of the same name\r\n--r\r\nContent-Type: image/gif\r\n\r\nno id\r\n--r--\r\n",
			"<img src=\"cid:logo@x\">", "", []string{"logo@x"}},
	}
	for _, c := range cases {
		p, err := findHTML(ctx, []byte(c.raw))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(bytes.TrimSpace(p.html)) != c.want || p.charset != c.charset || len(p.inline) != len(c.inline) {
			t.Errorf("%s: %q %q %v", c.name, p.html, p.charset, p.inline)
		}
		for _, id := range c.inline {
			if part := p.inline[id]; part.Type != "image/png" || string(part.Data) != "hello" {
				t.Errorf("%s: %s is %+v", c.name, id, part)
			}
		}
	}
	for _, raw := range []string{"Content-Type: text/plain\r\n\r\nno html", "not a header", "Content-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: image/png\r\n\r\nx\r\n--m--\r\n"} {
		if _, err := findHTML(ctx, []byte(raw)); !errors.Is(err, errNoHTML) {
			t.Errorf("%q: %v", raw, err)
		}
	}
	// A part over 2 MiB is too large; inline parts past what all of them
	// may read are too large too.
	big := "Content-Type: text/html\r\n\r\n" + strings.Repeat("a", contracts.MessageHTMLMaxBytes+1)
	if p, err := findHTML(ctx, []byte(big)); err != nil || !p.tooLarge {
		t.Errorf("a part over 2 MiB: %v", err)
	}
	var b strings.Builder
	b.WriteString("Content-Type: multipart/related; boundary=r\r\n\r\n--r\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n")
	for i := range 5 {
		b.WriteString("--r\r\nContent-Type: image/png\r\nContent-ID: <p" + string(rune('a'+i)) + ">\r\n\r\n" + strings.Repeat("z", 1900<<10) + "\r\n")
	}
	b.WriteString("--r--\r\n")
	p, err := findHTML(ctx, []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if p.inline["pa"].TooLarge || p.inline["pc"].TooLarge || !p.inline["pd"].TooLarge || !p.inline["pe"].TooLarge {
		t.Errorf("past what inline parts may read: %v %v %v", p.inline["pc"].TooLarge, p.inline["pd"].TooLarge, p.inline["pe"].TooLarge)
	}
	// A context that ended stops the walk.
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := findHTML(ended, []byte(cases[1].raw)); !errors.Is(err, context.Canceled) {
		t.Errorf("an ended context: %v", err)
	}
	// Too deep, too many parts, a header too long, no type at all.
	deep := "<p>deep</p>"
	for i := range MaxMIMEDepth + 1 {
		bd := "b" + strings.Repeat("x", i)
		deep = "Content-Type: multipart/mixed; boundary=" + bd + "\r\n\r\n--" + bd + "\r\n" + deep + "\r\n--" + bd + "--\r\n"
		if i == 0 {
			deep = strings.Replace(deep, "--b\r\n<p>", "--b\r\nContent-Type: text/html\r\n\r\n<p>", 1)
		}
	}
	var many strings.Builder
	many.WriteString("Content-Type: multipart/related; boundary=r\r\n\r\n--r\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n")
	for i := range MaxMIMEParts + 1 {
		many.WriteString("--r\r\nContent-Type: image/png\r\nContent-ID: <" + strings.Repeat("i", i%7+1) + ">\r\n\r\nx\r\n")
	}
	many.WriteString("--r--\r\n")
	for name, raw := range map[string]string{
		"too deep":       deep,
		"too many parts": many.String(),
		"a long header":  "X-Long: " + strings.Repeat("a", MaxHeaderBytes+10) + "\r\nContent-Type: text/html\r\n\r\n<p>x</p>",
		"no type":        "Subject: x\r\n\r\n<p>x</p>",
	} {
		if _, err := findHTML(ctx, []byte(raw)); !errors.Is(err, errNoHTML) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A multipart that breaks after its HTML keeps what it found; a
	// multipart part of a related is not an image.
	broken := "Content-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/html\r\n\r\n<p>kept</p>\r\n--a\r\nno colon here\r\n\r\nx\r\n"
	if p, err := findHTML(ctx, []byte(broken)); err != nil || !strings.Contains(string(p.html), "kept") {
		t.Errorf("broken after the HTML: %v", err)
	}
	nested := "Content-Type: multipart/related; boundary=r\r\n\r\n--r\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n--r\r\nContent-Type: multipart/mixed; boundary=m\r\nContent-ID: <n>\r\n\r\n--m--\r\n--r--\r\n"
	if p, err := findHTML(ctx, []byte(nested)); err != nil || len(p.inline) != 0 {
		t.Errorf("a multipart in a related: %v %v", p, err)
	}
}

func TestHTMLInputAndTheLinkKey(t *testing.T) {
	ctx := context.Background()
	b := Box{MailboxID: "mbx", Key: bytes.Repeat([]byte{7}, 32)}
	k1 := LinkKey(b.Key)
	k2 := LinkKey(bytes.Repeat([]byte{8}, 32))
	if len(k1) != 32 || bytes.Equal(k1, k2) || bytes.Equal(k1, b.Key) {
		t.Fatal("the link key is not derived from the mailbox's")
	}
	in, status, ok, err := HTMLInput(ctx, []byte("Content-Type: text/html; charset=windows-1252\r\n\r\n<p>Caf\xe9</p>"), b, "m1")
	if err != nil || !ok || status != "" || string(in.HTML) != "<p>Café</p>" || in.Mailbox != "mbx" || in.Message != "m1" || !bytes.Equal(in.LinkKey, k1) {
		t.Fatalf("%+v %q %v %v", in, status, ok, err)
	}
	for raw, want := range map[string]string{
		"Content-Type: text/html; charset=utf-7\r\n\r\n<p>x</p>":                                 contracts.HTMLUnreadable,
		"Content-Type: text/html\r\n\r\n" + strings.Repeat("a", contracts.MessageHTMLMaxBytes+1): contracts.HTMLTooLarge,
	} {
		if _, status, ok, err := HTMLInput(ctx, []byte(raw), b, "m1"); status != want || !ok || err != nil {
			t.Errorf("%q: %q %v %v", raw[:40], status, ok, err)
		}
	}
	if _, _, ok, err := HTMLInput(ctx, []byte("Content-Type: text/plain\r\n\r\nx"), b, "m1"); ok || err != nil {
		t.Errorf("no HTML: %v %v", ok, err)
	}
	// What Get says of each.
	if h := messageHTML(ctx, []byte("Content-Type: text/plain\r\n\r\nx"), b, "m1"); h != nil {
		t.Errorf("no HTML: %+v", h)
	}
	if h := messageHTML(ctx, []byte("Content-Type: text/html; charset=utf-7\r\n\r\nx"), b, "m1"); h == nil || h.Status != contracts.HTMLUnreadable {
		t.Errorf("unreadable: %+v", h)
	}
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if h := messageHTML(ended, []byte("Content-Type: text/html\r\n\r\nx"), b, "m1"); h != nil {
		t.Errorf("an ended request: %+v", h)
	}
	busy := WithRenderAdmission(ctx, refusingSlots{})
	if h := messageHTML(busy, []byte("Content-Type: text/html\r\n\r\n<p>x</p>"), b, "m1"); h == nil || h.Status != contracts.HTMLUnavailable {
		t.Errorf("no slot: %+v", h)
	}
}

type refusingSlots struct{}

type grantingSlots struct{ taken, given *int }

func (g grantingSlots) Admit(ctx context.Context) (context.Context, func(), error) {
	*g.taken++
	c, cancel := context.WithTimeout(ctx, RenderDeadline)
	return c, func() { *g.given++; cancel() }, nil
}

// Get renders under the slot it takes, gives it back, and is unavailable
// when the render's deadline passes.
func TestGetRendersUnderASlotAndItsDeadline(t *testing.T) {
	ctx := context.Background()
	b := Box{MailboxID: "mbx", Key: bytes.Repeat([]byte{7}, 32)}
	var taken, given int
	h := messageHTML(WithRenderAdmission(ctx, grantingSlots{&taken, &given}), []byte("Content-Type: text/html\r\n\r\n<p><a href=\"https://a.example/\">a</a></p>"), b, "m1")
	if h == nil || h.Status != contracts.HTMLShown || h.Links != 1 || taken != 1 || given != 1 {
		t.Fatalf("%+v %d %d", h, taken, given)
	}
	old := RenderDeadline
	RenderDeadline = 0
	defer func() { RenderDeadline = old }()
	if h := messageHTML(ctx, []byte("Content-Type: text/html\r\n\r\n<p>x</p>"), b, "m1"); h == nil || h.Status != contracts.HTMLUnavailable {
		t.Fatalf("past the deadline: %+v", h)
	}
}

func (refusingSlots) Admit(context.Context) (context.Context, func(), error) {
	return nil, nil, contracts.ErrBusy
}
