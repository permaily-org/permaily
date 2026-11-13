package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-message"

	"github.com/permaily-org/permaily/internal/contracts"
)

// What PR #90 wrote and fuzzed for reading, brought here as the one
// extraction of the tree (review 3), with the cases of the review.

// The text taken out of HTML: text nodes, a line per block, nothing of
// script, style or head, entities decoded, a link's target after its text.
func TestTextFromHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<p>a</p><p>b</p>", "a\nb"},
		{"a<br><br>b", "a\n\nb"},
		{"<head><title>T</title><meta charset=utf-8></head><body>x</body>", "x"},
		{"<script>var a = '<p>no</p>';</script>yes", "yes"},
		{"<style>p { color: red }</style>yes", "yes"},
		{"<template><p>no</p></template>yes", "yes"},
		{"&lt;b&gt; &amp; &#233; &eacute;", "<b> & é é"},
		{"<a href='https://e.example/a'>here</a>", "here [https://e.example/a]"},
		{"<a href='https://e.example/a'>https://e.example/a</a>", "https://e.example/a"},
		{"<a href='#top'>top</a>", "top"},
		{"<a>bare</a> <a href=''>empty</a>", "bare empty"},
		{"<pre>  two\n  lines</pre>", "  two\n  lines"},
		{"spaced   \n\t out", "spaced out"},
		{"<ul><li>one</li><li>two</li></ul>", "one\ntwo"},
		{"<div>unclosed <b>bold", "unclosed bold"},
		{"</a></script></style>stray", "stray"},
		{"<scr<script>ipt>alert(1)</script>", "ipt>alert(1)"},
		{"\xff\xfeinvalid", "\uFFFDinvalid"},
	} {
		got, truncated := readHTMLText(context.Background(), []byte(c.in), 1<<10)
		if got != c.want || truncated {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	got, truncated := readHTMLText(context.Background(), []byte("<p>"+strings.Repeat("é", 100)+"</p>"), 11)
	if !truncated || got != strings.Repeat("é", 5) {
		t.Errorf("cut: %q %v", got, truncated)
	}
}

func TestCutText(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
		cut   bool
	}{
		{"abc", 3, "abc", false},
		{"abcd", 3, "abc", true},
		{"aé", 2, "a", true},
		{"€€", 4, "€", true},
		{"x", 0, "", true},
		{"x", -1, "", true},
	} {
		if got, cut := cutText(c.in, c.limit); got != c.want || cut != c.cut {
			t.Errorf("%q %d: %q %v", c.in, c.limit, got, cut)
		}
	}
}

func nested(depth int, leaf string) string {
	var b strings.Builder
	b.WriteString("From: a@example.org\r\nMIME-Version: 1.0\r\n")
	for i := range depth {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\n" + leaf + "\r\n")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--b%d--\r\n", i)
	}
	return b.String()
}

// The MIME walk stops at the limits of ADR 0026 ("What a message may hold"):
// sixteen levels of nesting and five hundred parts.
func TestTheWalkStopsAtItsLimits(t *testing.T) {
	ctx := context.Background()
	if text, from, _ := MessageText(ctx, []byte(nested(MaxMIMEDepth, "deep")), 1<<10); text != "deep" || from != contracts.TextFromPlain {
		t.Errorf("at the limit: %q %s", text, from)
	}
	if text, from, _ := MessageText(ctx, []byte(nested(MaxMIMEDepth+1, "deeper")), 1<<10); text != "" || from != contracts.TextFromNone {
		t.Errorf("past it: %q %s", text, from)
	}
	var b strings.Builder
	b.WriteString("From: a@example.org\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := range MaxMIMEParts + 10 {
		fmt.Fprintf(&b, "--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment\r\n\r\n%d\r\n", i)
	}
	b.WriteString("--x--\r\n")
	visited := 0
	if err := walkParts(ctx, []byte(b.String()), func(*message.Entity, bool) (bool, error) { visited++; return true, nil }); !errors.Is(err, errLimit) {
		t.Fatalf("past the parts: %v", err)
	}
	// The message itself is a part, and the walk stops at the 500th.
	if visited != MaxMIMEParts-1 {
		t.Errorf("visited %d", visited)
	}
	head := "From: a@example.org\r\nX-Big: " + strings.Repeat("a", MaxHeaderBytes) + "\r\n\r\nbody"
	if text, from, _ := MessageText(ctx, []byte(head), 1<<10); text != "" || from != contracts.TextFromNone {
		t.Errorf("a header past the limit: %q %s", text, from)
	}
	if err := walkParts(ctx, []byte(head), func(*message.Entity, bool) (bool, error) { return true, nil }); err == nil {
		t.Error("no error for a header past the limit")
	}
	// A part's header past the limit ends the walk too (review 8).
	part := "From: a@example.org\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\nX-Big: " +
		strings.Repeat("a", MaxHeaderBytes) + "\r\n\r\nbody\r\n--x--\r\n"
	if err := walkParts(ctx, []byte(part), func(*message.Entity, bool) (bool, error) { return true, nil }); !errors.Is(err, errLimit) {
		t.Errorf("a part's header past the limit: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := walkParts(cancelled, []byte(plain), func(*message.Entity, bool) (bool, error) { return true, nil }); err == nil {
		t.Error("a walk past its context")
	}
}

const plain = "From: a@example.org\r\nContent-Type: text/plain\r\n\r\nhello\r\n"

var fuzzSeeds = []string{
	plain,
	nested(3, "x"),
	"Content-Type: text/html\r\n\r\n<p>a<a href=x>b</a><script>c</script>",
	"Content-Type: multipart/alternative; boundary=q\r\n\r\n--q\r\nContent-Type: text/html\r\n\r\n<b>h</b>\r\n--q\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n=E9t=E9\r\n--q--\r\n",
	"Content-Type: multipart/mixed; boundary=q\r\n\r\n--q\r\nContent-Disposition: attachment; filename=\"../../x\"\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!\r\n--q",
	"Content-Type: text/plain; charset=utf-7\r\n\r\n+AGEAYgBj-",
	"\r\n\r\n",
	"",
}

// FuzzMessageText: whatever a message holds, its text is valid UTF-8 within its
// bound, from one of the three sources, and reading it never panics.
func FuzzMessageText(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		text, from, truncated := MessageText(context.Background(), raw, 512)
		if !utf8.ValidString(text) || len(text) > 512 {
			t.Fatalf("%d bytes, valid %v", len(text), utf8.ValidString(text))
		}
		switch from {
		case contracts.TextFromPlain, contracts.TextFromHTML:
		case contracts.TextFromNone:
			if text != "" || truncated {
				t.Fatal("text from none")
			}
		default:
			t.Fatalf("from %q", from)
		}
		html, _ := readHTMLText(context.Background(), raw, 512)
		if !utf8.ValidString(html) || len(html) > 512 {
			t.Fatal("html text")
		}
	})
}

// FuzzWalkParts: the MIME walk visits at most MaxMIMEParts parts, reads
// every attachment's body within the message's size, and never panics.
func FuzzWalkParts(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		visited := 0
		_ = walkParts(context.Background(), raw, func(p *message.Entity, attachment bool) (bool, error) {
			visited++
			if attachment {
				buf := make([]byte, 64)
				n := 0
				for {
					k, err := p.Body.Read(buf)
					n += k
					if err != nil || n > 4*len(raw)+64 {
						break
					}
				}
				if n > 4*len(raw)+64 {
					t.Fatalf("an attachment of %d bytes from a message of %d", n, len(raw))
				}
			}
			return true, nil
		})
		if visited > MaxMIMEParts {
			t.Fatalf("%d parts", visited)
		}
	})
}

// Review 3: what the extraction keeps while it scans is bounded: past
// maxOpenLinks open links a link keeps no target, and a target longer than
// maxLinkTarget is not kept, so neither is ever compared or written.
func TestReview3TheLinkStateIsBounded(t *testing.T) {
	ctx := context.Background()
	in := strings.Repeat("<a href=t>", maxOpenLinks) + "<a href=deep>x</a>" + strings.Repeat("</a>", maxOpenLinks)
	if got, _ := readHTMLText(ctx, []byte(in), 1<<10); got != "x"+strings.Repeat(" [t]", maxOpenLinks) {
		t.Fatalf("%q", got)
	}
	long := "<a href=" + strings.Repeat("h", maxLinkTarget+1) + ">y</a>"
	if got, _ := readHTMLText(ctx, []byte(long), 1<<20); got != "y" {
		t.Fatalf("a target of %d bytes: %d bytes written", maxLinkTarget+1, len(got))
	}
	// The context is checked between tokens, not only before the walk.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, truncated := readHTMLText(cancelled, []byte(strings.Repeat("<p>w</p>", 10)), 1<<10); got != "" || !truncated {
		t.Fatalf("%q %v", got, truncated)
	}
}

// Review 8: boundMeta empties the metadata in its order until it fits, and
// leaves what fits alone.
func TestReview8BoundMetaShrinksInOrder(t *testing.T) {
	big := strings.Repeat("<", MaxMetaBytes/5)
	addr := []contracts.MailAddress{{Name: big}}
	fits := StoredMeta{Subject: "x"}
	boundMeta(&fits)
	if fits.Partial || fits.Subject != "x" {
		t.Fatal("what fits was changed")
	}
	for name, c := range map[string]struct {
		m    StoredMeta
		kept func(m *StoredMeta) bool
	}{
		"recipients": {StoredMeta{To: addr, From: []contracts.MailAddress{{Name: "Ana"}}}, func(m *StoredMeta) bool { return len(m.To) == 0 && len(m.From) == 1 }},
		"message id": {StoredMeta{MessageID: big, Date: big, Snippet: "s"}, func(m *StoredMeta) bool { return m.MessageID == "" && m.Snippet == "s" }},
		"names": {StoredMeta{Attachments: []contracts.MessageAttachment{{Name: big}, {Name: big}}, Snippet: "s"}, func(m *StoredMeta) bool {
			return m.Attachments[0].Name == "" && len(m.Attachments) == 2 && m.Snippet == "s"
		}},
		"senders": {StoredMeta{From: addr, Snippet: "s", Subject: "x"}, func(m *StoredMeta) bool { return len(m.From) == 0 && m.Snippet == "" && m.Subject == "x" }},
		"the rest": {StoredMeta{EnvelopeFrom: strings.Repeat("<", MaxMetaBytes), Auth: []contracts.AuthCheck{{Domain: big, Result: big}}}, func(m *StoredMeta) bool {
			return len(m.EnvelopeFrom) == 255 && len(m.Auth[0].Domain) == 255
		}},
	} {
		m := c.m
		boundMeta(&m)
		if !m.Partial || !c.kept(&m) {
			t.Errorf("%s: partial %v", name, m.Partial)
		}
	}
	// A part with a Content-Type that cannot be read is no text: it is listed.
	w := walkMessage(context.Background(), []byte("Content-Type: multipart/mixed; boundary=p\r\n\r\n--p\r\nContent-Type: ;;;\r\n\r\nx\r\n--p--\r\n"), 100, true, false)
	if len(w.attachments) != 1 || w.plainSeen {
		t.Fatalf("%+v", w.attachments)
	}
}
