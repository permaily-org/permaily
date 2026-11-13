package mailhtml

import (
	"context"
	"io"
	"runtime"
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
func TestReview3ATextIsJoinedInLinearWork(t *testing.T) {
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
func TestReview3FADroppedCommentJoinsNothing(t *testing.T) {
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
