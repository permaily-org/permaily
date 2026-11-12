package inbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-message"

	"github.com/permaily-org/permaily/internal/contracts"
)

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
		got, truncated := readHTMLText([]byte(c.in), 1<<10)
		if got != c.want || truncated {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	got, truncated := readHTMLText([]byte("<p>"+strings.Repeat("é", 100)+"</p>"), 11)
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
	if text, from, _ := readText(ctx, []byte(nested(MaxMIMEDepth, "deep")), 1<<10); text != "deep" || from != contracts.TextFromPlain {
		t.Errorf("at the limit: %q %s", text, from)
	}
	if text, from, _ := readText(ctx, []byte(nested(MaxMIMEDepth+1, "deeper")), 1<<10); text != "" || from != contracts.TextFromNone {
		t.Errorf("past it: %q %s", text, from)
	}
	var b strings.Builder
	b.WriteString("From: a@example.org\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := range MaxMIMEParts + 10 {
		fmt.Fprintf(&b, "--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment\r\n\r\n%d\r\n", i)
	}
	b.WriteString("--x--\r\n")
	visited := 0
	if err := walkParts(ctx, []byte(b.String()), func(*message.Entity, bool) (bool, error) { visited++; return true, nil }); err != nil {
		t.Fatal(err)
	}
	// The message itself is a part, and the walk stops at the 500th.
	if visited != MaxMIMEParts-1 {
		t.Errorf("visited %d", visited)
	}
	head := "From: a@example.org\r\nX-Big: " + strings.Repeat("a", MaxHeaderBytes) + "\r\n\r\nbody"
	if text, from, _ := readText(ctx, []byte(head), 1<<10); text != "" || from != contracts.TextFromNone {
		t.Errorf("a header past the limit: %q %s", text, from)
	}
	if err := walkParts(ctx, []byte(head), func(*message.Entity, bool) (bool, error) { return true, nil }); err == nil {
		t.Error("no error for a header past the limit")
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

// FuzzReadText: whatever a message holds, its text is valid UTF-8 within its
// bound, from one of the three sources, and reading it never panics.
func FuzzReadText(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		text, from, truncated := readText(context.Background(), raw, 512)
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
		html, _ := readHTMLText(raw, 512)
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
