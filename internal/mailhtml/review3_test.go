package mailhtml

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The third code review of pull request 5 (ADR 0026, "Changed after PR
// #92's third code review"): each finding's reproduction, written before
// its fix.

// allocatedBy is what f allocated on the heap in all, whatever it gave back:
// the work it did in bytes copied, without a clock.
func allocatedBy(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// joinFamilies are the inputs of finding A and its variants, as a unit
// repeated: text, then something the walk does not keep, so that the text
// on either side ends up in the same parent.
func joinFamilies() map[string]string {
	x := strings.Repeat("x", 28)
	return map[string]string{
		"unknown elements":              x + "<o:p></o:p>",
		"text in unknown elements":      "<o:p>" + x + "</o:p>",
		"comments":                      x + "<!---->",
		"short comments":                x + "<!>",
		"dropped elements":              x + "<script></script>",
		"details":                       x + "<details></details>",
		"entities around unknown ones":  x + "&amp;<o:p></o:p>&lt;",
		"unknown elements in a link":    "<o:p>" + x + "</o:p>",
		"unknown elements with a space": x + " <o:p> </o:p> ",
	}
}

// A: the walk joined the text on either side of an element it unwraps with
// last.Data += s, copying what it had gathered each time: 1,949,610 bytes of
// "x"×28 and "<o:p></o:p>" held 374.7 MiB of heap at the peak and took
// 8.3 s. Text is gathered as pieces and made into one text node once. For
// each family, in Render and in Links: the work (bytes allocated) at twice
// the size is about twice the work (a quadratic walk makes it four times),
// and at the largest size a part may be the peak of the heap stays within a
// render's memory.
func TestReviewThirdATextIsJoinedInLinearWork(t *testing.T) {
	ctx := context.Background()
	for name, unit := range joinFamilies() {
		wrap := func(n int) string {
			s := strings.Repeat(unit, n)
			if strings.HasSuffix(name, "in a link") {
				s = `<a href="https://a.example/">` + s + "</a>"
			}
			return s
		}
		for _, pass := range []struct {
			name string
			run  func(src string)
		}{
			{"Render", func(src string) { _, _ = Render(ctx, input(src), io.Discard) }},
			{"Links", func(src string) { _, _ = Links(ctx, input(src)) }},
		} {
			const n = 4000
			once := allocatedBy(func() { pass.run(wrap(n)) })
			twice := allocatedBy(func() { pass.run(wrap(2 * n)) })
			if twice > 3*once {
				t.Errorf("%s, %s: %d KiB at %d units, %d KiB at %d: more than linear", name, pass.name, once>>10, n, twice>>10, 2*n)
			}
		}
		// The largest part: memory within a render's, under a generous
		// deadline that only stops work that is not bounded at all.
		// As many units as a part may hold: within its bytes and its start
		// tags (one a unit at most).
		src := wrap(min(contracts.MessageHTMLMaxBytes/len(unit), contracts.MessageHTMLMaxStartTags) - 10)
		for _, pass := range []string{"Render", "Links"} {
			dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			heap, _ := peakMemory(func() {
				if pass == "Render" {
					_, _ = Render(dctx, input(src), io.Discard)
				} else {
					_, _ = Links(dctx, input(src))
				}
			})
			cancel()
			if heap > renderMemory {
				t.Errorf("%s, %s: %d bytes with %.1f MiB of heap, over the %d MiB of a render", name, pass, len(src), float64(heap)/(1<<20), renderMemory>>20)
			}
		}
	}
}

// shownText is the text a browser shows of src's body: its text nodes in
// order, as x/net/html (a parser of the HTML standard) builds them.
func shownText(t *testing.T, src string) string {
	t.Helper()
	doc, err := parse(context.Background(), []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	var body *html.Node
	for n := doc; n != nil; n = next(n, doc) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			break
		}
	}
	for n := body; n != nil; n = next(n, body) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
	}
	return b.String()
}

// F: the prepared stream left comments out, so the text on either side of
// one was read as one text: "&am<!>p;", which a browser shows as "&amp;",
// was shown "&". A dropped comment leaves an empty comment in its place,
// charged as one node, and no comment reaches the output: the text shown is
// the original's, for this input and the joins fuzzing found before.
func TestReviewThirdFADroppedCommentJoinsNothing(t *testing.T) {
	for _, src := range []string{
		"&am<!>p;", "&am<!---->p;", "x&lt<!-- y -->;z", "<<!>td>x", "<<!---->/p>", "a<<!-- x -->b>c",
		"<table><tr><td>&am<!>p;</td></tr></table>",
	} {
		st, out := renderOK(t, input(src))
		if st.Status != contracts.HTMLShown {
			t.Errorf("%q: %s", src, st.Status)
			continue
		}
		if got, want := shownText(t, out), shownText(t, src); got != want {
			t.Errorf("%q: shows %q, a browser %q", src, got, want)
		}
		if strings.Contains(out, "<!--") {
			t.Errorf("%q: a comment in the output: %s", src, out)
		}
		boundHolds(t, src)
	}
}

// textLink is a text of the output and where it leads: the destination its
// innermost link resolves to through Links and Find, "" when it is in none.
type textLink struct{ text, url string }

// linkedTexts reads the output as a browser does (x/net/html, without the
// prepared stream) and says, for each text that is not white space, where
// it leads: the token of the innermost <a> around it, resolved by Find
// among Links(in), which must be the address the <a>'s title names.
func linkedTexts(t *testing.T, in Input, out string) []textLink {
	t.Helper()
	links, err := Links(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var got []textLink
	for n := doc; n != nil; n = next(n, doc) {
		if n.Type != html.TextNode || strings.Trim(n.Data, " \t\n\f\r") == "" || n.Parent.DataAtom == atom.Style {
			continue
		}
		tl := textLink{text: strings.TrimSpace(n.Data)}
		for p := n.Parent; p != nil; p = p.Parent {
			if p.DataAtom != atom.A {
				continue
			}
			href, _ := attr(p, "href")
			title, _ := attr(p, "title")
			l, ok := Find(links, href[strings.LastIndexByte(href, '/')+1:])
			if !ok || l.URL != title {
				t.Errorf("%q: %q resolves to %+v, its title says %q", tl.text, href, l, title)
			}
			tl.url = l.URL
			break
		}
		got = append(got, tl)
	}
	return got
}

// C: a link inside a link. A browser ends the outer link where the inner
// one starts (the adoption agency); the sanitiser, having renamed <a> in the
// prepared stream, built them nested and unwrapped the inner one, so "B"
// led to a.example. The text of a link leads to the destination the sender
// wrote for the innermost link around it: the outer link ends where an
// inner one starts and goes on after it as a link of its own to the same
// destination; an inner link that is not kept (no href, a refused
// destination, mailto:) leads nowhere, not to the outer one's.
func TestReviewThirdCNestedLinksLeadWhereTheSenderWrote(t *testing.T) {
	const a, b, c = "https://a.example/", "https://b.example/", "https://c.example/"
	cases := []struct {
		name, src string
		want      []textLink
	}{
		{"the reviewer's", `<a href="https://a.example"><div><a href="https://b.example">B</a></div></a>`,
			[]textLink{{"B", b}}},
		{"text around it", `<a href="https://a.example">A1<div><a href="https://b.example">B</a></div>A2</a>`,
			[]textLink{{"A1", a}, {"B", b}, {"A2", a}}},
		{"three levels", `<a href="https://a.example">A1<div>A2<a href="https://b.example">B1<div><a href="https://c.example">C</a></div>B2</a>A3</div>A4</a>`,
			[]textLink{{"A1", a}, {"A2", a}, {"B1", b}, {"C", c}, {"B2", b}, {"A3", a}, {"A4", a}}},
		// Where no block stands between them, the prepared stream ends the
		// first link before the second as a browser's parser does (with
		// the inline elements open inside it): what follows leads nowhere,
		// in a browser too.
		{"in formatting", `<a href="https://a.example"><b>A1<span><a href="https://b.example"><i>B</i></a></span>A2</b></a>`,
			[]textLink{{"A1", a}, {"B", b}, {"A2", ""}}},
		{"side by side", `<a href="https://a.example">A1<a href="https://b.example">B</a>A2</a>`,
			[]textLink{{"A1", a}, {"B", b}, {"A2", ""}}},
		{"an inner link with no href", `<a href="https://a.example">A1<div><a>N</a></div>A2</a>`,
			[]textLink{{"A1", a}, {"N", ""}, {"A2", a}}},
		{"an inner link refused", `<a href="https://a.example">A1<div><a href="javascript:alert(1)">J</a></div>A2</a>`,
			[]textLink{{"A1", a}, {"J", ""}, {"A2", a}}},
		{"an inner mailto:", `<a href="https://a.example">A1<div><a href="mailto:x@b.example">M</a></div>A2</a>`,
			[]textLink{{"A1", a}, {"M [x@b.example]", ""}, {"A2", a}}},
		{"an inner link refused, holding a link", `<a href="https://a.example">A1<div><a href="ftp://x">F<div><a href="https://c.example">C</a></div></a></div>A2</a>`,
			[]textLink{{"A1", a}, {"F", ""}, {"C", c}, {"A2", a}}},
		{"a sender's placeholder", `<a href="https://a.example">A1<permaily-a href="https://b.example">P</permaily-a></a>`,
			[]textLink{{"A1P", a}}},
	}
	for _, tc := range cases {
		in := input(tc.src)
		st, out := renderOK(t, in)
		if st.Status != contracts.HTMLShown {
			t.Errorf("%s: %s", tc.name, st.Status)
			continue
		}
		if got := linkedTexts(t, in, out); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v\n%s", tc.name, got, tc.want, out)
		}
		if strings.Contains(out, "permaily-") {
			t.Errorf("%s: a placeholder in the output: %s", tc.name, out)
		}
	}
}

// C: the formatting elements are renamed in the prepared stream to names
// the parser builds as unknown elements; a sender who wrote those names got
// a real link or a real <b>. The names carry a suffix drawn for each render
// (crypto/rand) that the sender cannot write, and an element whose name
// starts with the reserved prefix is an unknown element: unwrapped.
func TestReviewThirdCTheSendersPlaceholdersAreUnknownElements(t *testing.T) {
	for _, src := range []string{
		`<permaily-a href="https://b.example">P</permaily-a>`,
		`<permaily-b>P</permaily-b>`,
		`<PERMAILY-A HREF="https://b.example">P</PERMAILY-A>`,
		`<permaily-strong>P</permaily-strong><permaily-font color=red>Q</permaily-font>`,
		`<b><permaily-a href="https://b.example">P</permaily-a></b>`,
	} {
		in := input(src)
		st, out := renderOK(t, in)
		if st.Status != contracts.HTMLShown || st.Links != 0 {
			t.Errorf("%q: %s with %d links: %s", src, st.Status, st.Links, out)
		}
		for _, tag := range []string{"<a ", "<strong", "<font", "permaily-"} {
			if strings.Contains(out, tag) {
				t.Errorf("%q: %s in %s", src, tag, out)
			}
		}
		if strings.Contains(src, "<b>") != strings.Contains(out, "<b>") {
			t.Errorf("%q: %s", src, out)
		}
		if links, err := Links(context.Background(), in); err != nil || len(links) != 0 {
			t.Errorf("%q: links %v %v", src, links, err)
		}
	}
	// Two prepared streams of the same part name the formatting elements
	// differently.
	one, err := prepare(context.Background(), []byte("<b>x</b>"), false)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := prepare(context.Background(), []byte("<b>x</b>"), false)
	if bytes.Equal(one.stream, two.stream) || !strings.HasPrefix(one.prefix, formattingPrefix) || len(one.prefix) < len(formattingPrefix)+16 {
		t.Errorf("%q %q", one.stream, two.stream)
	}
}

// C: a start tag that carries the drawn prefix (which the sender would have
// had to guess) makes the part unreadable, in Render and in Links.
func TestReviewThirdCTheDrawnNameMakesThePartUnreadable(t *testing.T) {
	old := placeholderPrefix
	defer func() { placeholderPrefix = old }()
	placeholderPrefix = func() string { return formattingPrefix + "0123456789abcdef-" }
	for _, src := range []string{`<permaily-0123456789abcdef-a href="https://b.example">P</permaily-0123456789abcdef-a>`, `<p><permaily-0123456789ABCDEF-b>P`} {
		if st, err := Render(context.Background(), input(src), nil); err != nil || st.Status != contracts.HTMLUnreadable {
			t.Errorf("%q: %s %v", src, st.Status, err)
		}
		if _, err := Links(context.Background(), input(src)); !errors.Is(err, ErrNotShown) {
			t.Errorf("%q: %v", src, err)
		}
	}
}

// insideLink are the declarations everything inside a kept link carries
// but its images, with !important (ADR 0026, "What each link carries"):
// what a holder carries, so nothing inside collapses or clips the link,
// and square corners.
var insideLink = append(slices.Clone(ancestorHolds), "border-radius:0!important")

// D: an element inside a link could collapse it: a span made an inline
// block of no width and a hidden overflow left the link's box zero wide in
// three engines. Everything inside a kept link carries what a holder
// carries (overflow:visible, height:auto, max-height:none, max-width:none,
// min-width:min-content), as well as the colour, font and display it
// carried; an image inside keeps its own treatment (the frame's
// max-width:100% and its own width and height).
func TestReviewThirdDNothingInsideALinkCollapsesIt(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{`<a href="https://a.example"><span id="x" style="display:inline-block;width:0;max-width:0;overflow:hidden">LINK</span></a>`, insideLink},
		{`<style>.c{width:0!important;max-width:0!important;overflow:hidden!important;height:0!important}</style><a href="https://a.example"><b><span id="x" class="c" style="max-height:0;min-width:0;border-radius:50%">LINK</span></b></a>`, insideLink},
		// In a link that holds another, the span holds a piece of it: a
		// holder.
		{`<a href="https://a.example"><div><span id="x" style="display:inline-block;width:0;overflow:hidden">LINK</span><a href="https://b.example">B</a></div></a>`, ancestorHolds},
	} {
		src := tc.src
		_, out := renderOK(t, input(src))
		style, _ := styleOf(t, out, "x")
		for _, d := range tc.want {
			if !strings.Contains(style, d) {
				t.Errorf("%.60q: no %s in %q", src, d, style)
			}
		}
		for _, own := range []string{"max-width:0", "overflow:hidden", "max-height:0", "min-width:0", "border-radius:50%"} {
			if strings.Contains(style, own) {
				t.Errorf("%.60q: %s kept in %q", src, own, style)
			}
		}
	}
	// An image inside a link keeps its own width: no min-content to hold it
	// wider than the frame.
	_, out := renderOK(t, input(`<a href="https://a.example"><img id="i" src="`+dataURI("image/png", pngImage(t, 4, 4, true))+`" width="40" style="width:40px"></a>`))
	style, _ := styleOf(t, out, "i")
	if strings.Contains(style, "min-width") || strings.Contains(style, "max-width") || !strings.Contains(style, "border-radius:0!important") {
		t.Errorf("an image inside a link: %q", style)
	}
}

// G: a link whose corners are rounded does not take the click at them: a
// border-radius of 100% let the points inside its box's corners reach the
// body in Chromium and Firefox. A link, and everything inside it, has
// square corners, whatever its style or a rule says.
func TestReviewThirdGALinksCornersAreSquare(t *testing.T) {
	src := `<style>a{border-radius:100%!important}.r{border-top-left-radius:9px}</style>` +
		`<a id="l" class="r" href="https://a.example" style="border-radius:100%;border-bottom-right-radius:50%">ROUND</a>`
	_, out := renderOK(t, input(src))
	style, _ := styleOf(t, out, "l")
	if !strings.HasSuffix(style, "border-radius:0!important") && !strings.Contains(style, "border-radius:0!important;") {
		t.Errorf("no square corners: %q", style)
	}
	for _, own := range []string{"border-radius:100%", "border-bottom-right-radius"} {
		if strings.Contains(style, own) {
			t.Errorf("%s kept: %q", own, style)
		}
	}
}
