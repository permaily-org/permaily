package mailhtml

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The code review of pull request 5 (#92): each finding is a test named for
// it that failed before its fix, and fails again when the fix is broken on
// purpose.

// Finding 7: a <meta> that names no WHATWG encoding makes the HTML
// unreadable, as a label in the MIME header does; it does not fall through
// to the next step of the order.
func TestReview7AnUnknownMetaCharsetIsUnreadable(t *testing.T) {
	for _, head := range []string{
		`<meta charset="utf-7">`,
		`<meta charset="made-up">`,
		`<meta http-equiv="Content-Type" content="text/html; charset=utf-7">`,
		`<meta http-equiv="content-type" content="text/html; charset=made-up">`,
	} {
		_, status := DecodeCharset([]byte(head+"<p>hello</p>"), "")
		if status != contracts.HTMLUnreadable {
			t.Errorf("%s: %q, want %q", head, status, contracts.HTMLUnreadable)
		}
	}
	// A label the standard knows still decides, and a part with no <meta>
	// is still UTF-8 or Windows-1252.
	for head, want := range map[string]string{`<meta charset="latin1">`: "é", ``: "é"} {
		out, status := DecodeCharset([]byte(head+"<p>\xe9</p>"), "")
		if status != "" || !strings.Contains(string(out), want) {
			t.Errorf("%q: %q %q", head, status, out)
		}
	}
}

// adrDropped are the elements ADR 0026 ("Elements") drops with everything
// inside, as it lists them; adrUnwrapped those whose children stay.
var (
	adrDropped = strings.Fields(`script noscript template style title head iframe frame frameset object embed applet
		param svg math canvas audio video source track picture map area link meta base input select option optgroup
		textarea button datalist output progress meter dialog portal fencedframe permission geolocation slot xmp
		plaintext noembed noframes listing`)
	adrUnwrapped = strings.Fields(`html body form label fieldset legend marquee blink o:p custom-tag`)
)

// walkOne walks a document whose body holds one element named name with the
// text INSIDE, built by hand so that the parser's own rules for the name
// (a <frame> in a body is ignored, a <title> moves to the head) do not
// decide what the test sees, and renders what the walk kept.
func walkOne(t *testing.T, name string) string {
	t.Helper()
	el := &html.Node{Type: html.ElementNode, Data: name, DataAtom: atom.Lookup([]byte(name))}
	el.AppendChild(&html.Node{Type: html.TextNode, Data: "INSIDE"})
	doc := &html.Node{Type: html.DocumentNode}
	doc.AppendChild(el)
	w := &walker{ctx: context.Background(), in: input("")}
	body, err := w.document(doc)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

// Finding 8: what is inside <portal> survives; every element the ADR drops
// with its content has a test, and so has every one it unwraps.
func TestReview8EveryDroppedElementGoesWithItsContent(t *testing.T) {
	for _, name := range adrDropped {
		if out := walkOne(t, name); strings.Contains(out, "INSIDE") || strings.Contains(out, "<"+name) {
			t.Errorf("<%s>: %s", name, out)
		}
	}
	for _, name := range adrUnwrapped {
		if out := walkOne(t, name); !strings.Contains(out, "INSIDE") || strings.Contains(out, "<"+name) {
			t.Errorf("<%s> is not unwrapped: %s", name, out)
		}
	}
	// And through the parser, for the names its atom table does not have.
	for _, name := range []string{"portal", "fencedframe", "permission", "geolocation"} {
		_, out := renderOK(t, input(`<p>before</p><`+name+` src="https://evil.example/"><p>INSIDE</p><a href="https://example.com/">x</a></`+name+`><p>after</p>`))
		if strings.Contains(out, "INSIDE") || strings.Contains(out, "<"+name) || !strings.Contains(out, "after") {
			t.Errorf("<%s>: %s", name, out)
		}
	}
}

// Finding 6: the limit of 32 compound selectors counts every boundary once,
// whether it is a descendant's white space or an explicit combinator, with
// or without white space around it.
func TestReview6CombinatorsCountAsCompoundBoundaries(t *testing.T) {
	chain := func(n int, sep string) string { return strings.Repeat("div"+sep, n-1) + "a{color:red}" }
	for _, c := range []struct {
		name string
		css  string
		kept bool
	}{
		{"40 with >", chain(40, ">"), false},
		{"40 with + and ~", strings.Repeat("p+p~", 20) + "a{color:red}", false},
		{"32 with >", chain(32, ">"), true},
		{"33 with >", chain(33, ">"), false},
		{"32 with ' > '", chain(32, " > "), true},
		{"33 with ' > '", chain(33, " > "), false},
		{"32 descendants", chain(32, " "), true},
		{"33 descendants", chain(33, " "), false},
		{"mixed, 32", strings.Repeat("div > p ", 16)[:len(strings.Repeat("div > p ", 16))-1] + "{color:red}", true},
		{"mixed, 33", strings.Repeat("div > p ", 16) + "a{color:red}", false},
		{"a list of 32", strings.Repeat("a, ", 31) + "a{color:red}", true},
		{"a list of 33", strings.Repeat("a, ", 32) + "a{color:red}", false},
	} {
		if got := sheet(c.css) != ""; got != c.kept {
			t.Errorf("%s: kept %v, want %v", c.name, got, c.kept)
		}
	}
	// What is written still means what was read.
	if got := sheet("div  >  p+a ~ b c{color:red}"); got != "div>p+a~b c{color:red}" {
		t.Errorf("%q", got)
	}
}
