package mailhtml

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The second code review of pull request 5 (ADR 0026, "Changed after PR
// #92's second code review"): each finding's reproduction, written before
// its fix.

// measure prepares src without the part's limits and parses the stream: the
// bounds, the nodes the parser made (the document included) and the bytes
// their attributes hold. An error is the parser's own cap of 512 open
// elements, or a stream past its limits.
func measure(src string) (prepared, int, int, error) {
	p, err := prepare(context.Background(), []byte(src), false)
	if err != nil {
		return p, 0, 0, err
	}
	doc, err := parse(context.Background(), p.stream)
	if err != nil {
		return p, 0, 0, err
	}
	nodes, attrBytes := 0, 0
	for n := doc; n != nil; n = next(n, doc) {
		nodes++
		for _, a := range n.Attr {
			attrBytes += len(a.Namespace) + len(a.Key) + len(a.Val)
		}
	}
	return p, nodes, attrBytes, nil
}

// boundHolds says, failing t otherwise, that what the parser makes of src's
// stream is within the bounds, with no slack; and returns the nodes made.
func boundHolds(t testing.TB, src string) int {
	t.Helper()
	p, nodes, attrBytes, err := measure(src)
	if errors.Is(err, errTooLarge) {
		return 0 // the parser refused a stack deeper than 512
	}
	if err != nil {
		t.Fatalf("%.80q: %v", src, err)
	}
	if nodes > p.nodes || attrBytes > p.attrBytes {
		t.Errorf("%.80q: bound %d nodes and %d attribute bytes, the parser made %d and %d", src, p.nodes, p.attrBytes, nodes, attrBytes)
	}
	return nodes
}

// clonedAttributes is N2's reproduction: a <div>, then 100 <b> with 63
// attributes more each, then "</div><div>x" repeated: the parser clones
// every <b> and its attributes at each new block.
func clonedAttributes(rounds int) string {
	var b strings.Builder
	b.WriteString("<div>")
	for i := range 100 {
		b.WriteString("<b id=b" + strconv.Itoa(i))
		for k := range 63 {
			b.WriteString(" a" + strconv.Itoa(k) + "=x")
		}
		b.WriteString(">")
	}
	b.WriteString(strings.Repeat("</div><div>x", rounds))
	return b.String()
}

// N2: the clones of formatting elements copy their attributes, and the
// bound did not charge for it: 42,595 bytes made 145.5 MiB of heap. A
// render stays within its stated memory, whatever it answers.
func TestReviewN2ClonedAttributesStayWithinTheRenderMemory(t *testing.T) {
	src := clonedAttributes(400)
	var st contracts.MessageHTML
	heap, _ := peakMemory(func() {
		var err error
		if st, err = Render(context.Background(), input(src), io.Discard); err != nil {
			t.Error(err)
		}
	})
	if heap > renderMemory {
		t.Fatalf("%d bytes: %s with %.1f MiB of heap, over the %d MiB of a render", len(src), st.Status, float64(heap)/(1<<20), renderMemory>>20)
	}
	boundHolds(t, src)
}

// N3: in the body, </p> and </br> each make an element, and the bound did
// not count them: 210,000 of them made 210,004 nodes, past the 200,000 the
// limits allow. The bound counts them, and such a part is too large.
func TestReviewN3EndTagsThatMakeElementsAreCounted(t *testing.T) {
	for _, tag := range []string{"</p>", "</br>"} {
		src := "<body>" + strings.Repeat(tag, contracts.MessageHTMLMaxTreeBound+10_000)
		if len(src) > contracts.MessageHTMLMaxBytes {
			t.Fatalf("%s: %d bytes, over the part's limit", tag, len(src))
		}
		if p, err := prepare(context.Background(), []byte(src), true); err == nil {
			t.Errorf("%s repeated: accepted with a bound of %d", tag, p.nodes)
		}
		boundHolds(t, src)
		if st, _ := renderOK(t, input(src)); st.Status != contracts.HTMLTooLarge {
			t.Errorf("%s repeated past the tree's limit: %s", tag, st.Status)
		}
	}
}

// The checklist the bound is built from: every place in golang.org/x/net
// v0.62.0's html/parse.go that creates a node or copies data the input does
// not spell out (read in its source: the calls of addElement, addChild,
// addText, parseImpliedToken, clone, copyAttributes and doc.AppendChild),
// each with an input that reaches it, and what the bound charges for it.
// TestTheBoundsChecklist asserts the bound on each, and
// TestTheChecklistIsOfThePinnedParser that the module is still that one.
var boundChecklist = []struct {
	site, charge, src string
}{
	// Created once a document (documentNodes).
	{"initialIM, beforeHTMLIM: the implied <html> (parse.go:570, :583)", "documentNodes", "x"},
	{"beforeHeadIM: the implied <head> (:609, :626)", "documentNodes", "<html>x"},
	{"afterHeadIM: the implied <body> (:849)", "documentNodes", "<head></head>x"},
	{"inHeadIM, afterHeadIM: a text's leading white space in the head, the rest in the body (:637, :801)", "documentNodes", "<head> \n x"},
	{"the end of input: html, head and body implied (initialIM to afterHeadIM)", "documentNodes", ""},
	// A node of the token's own.
	{"addElement: every start tag that makes its element (:563 … :2132)", "1 a start tag", "<div><span><p><img><br><hr><input><wbr>"},
	{"addText: a text in every mode (:897, :1457, :1475, :1937, :1987, :2087)", "1 a text", "a<table> <tr> b<td>c</td></tr></table><select>d</select>"},
	{"addFormattingElement: a formatting element (:362)", "1 a start tag; renamed, never active", "<a href=x>a<b>b<i>c<nobr>d"},
	{"doc.AppendChild: a doctype (:539)", "1 a doctype", "<!DOCTYPE html><!DOCTYPE x>"},
	{"comments (:532, :577, :616, :731, :839, :1241, :1545, :1615, :1702, :1909, :1923, :1973, :2025, :2041, :2089)", "0: comments are left out of the stream", "<!--a--><p><!--b--></p><table><!--c--></table></html><!--d-->"},
	// Elements implied by a token.
	{"inBodyIM: </p> with no <p> in button scope implies a <p> (:1217)", "1 a </p>", "<body></p></p></p>"},
	{"inBodyIM: </br> is read as <br> (:1233)", "1 a </br>", "<body></br></br></br>"},
	{"beforeHTMLIM, beforeHeadIM, inHeadIM, afterHeadIM: </br> and </body> imply html, head and body", "documentNodes", "</br>"},
	{"inTableIM: <col> implies <colgroup> (:1493)", "2 more a <col>", "<table><col><col><tbody></tbody><col>"},
	{"inColumnGroupIM: a text's leading white space stays in the colgroup, the rest is foster parented (:1608)", "the second more a <col> or <colgroup>", "<table><colgroup> x<colgroup> y<col> z"},
	{"inTableIM: <td>, <th>, <tr> imply <tbody> (:1501)", "2 more a <td> or <th>, 1 a <tr>", "<table><td>a<th>b<tr><td>c"},
	{"inTableBodyIM: <td>, <th> imply <tr> (:1671)", "2 more a <td> or <th>", "<table><tbody><td>a<th>b"},
	{"fosterParent: an element or text in a table goes before it (:259)", "as any element or text", "<table><b>x<div>y</div>z<tr><td>w"},
	// What the stream keeps the parser from doing.
	{"reconstructActiveFormattingElements: clones each active formatting element not open (:433)", "none: no formatting element is ever active", "<div><b id=1><i id=2></div><div>x</div><p>y"},
	{"inBodyEndTagFormatting, the adoption agency: clones in its inner loop and the formatting element (:1368, :1399)", "none: the end tag is any other end tag", "<b><p>x</b>y</p><a href=x><div>z</a>w"},
	{"addFormattingElement, Noah's Ark: removes the earliest of three identical (:362)", "none", "<b class=x><b class=x><b class=x><b class=x>q"},
	{"inBodyIM: a second <a> runs the adoption agency for the first (:1010)", "none: the stream ends the first <a>", "<a href=1>one<a href=2>two"},
	{"inBodyIM: <nobr> in scope runs the adoption agency (:1024)", "none", "<nobr>a<nobr>b<div><nobr>c"},
	// Data copied.
	{"copyAttributes: <html> and <body> past the first copy their attributes onto the first (:908, :919)", "their attribute bytes twice", `<html lang=a><body class=b><html dir=c data-x=1><body id=d style="e:f">`},
	{"clone(): copies an element's attributes (:433, :1368, :1399)", "none: nothing is cloned", `<div><b a=1 b=2 c=3></div>x<div>y`},
	{"appendText: text joined to the text before it (:307)", "no node; its bytes are the input's", "a<!--x-->b</x>c&amp;d"},
	// The tokenizer's reading, which this pass must share.
	{"NextIsNotRawText: <noscript> holds markup with scripting off (:660, :1111)", "read as markup by the pass", "<noscript><b>x<b>y</noscript>"},
	{"NextIsNotRawText: in foreign content raw text elements hold markup (:2137)", "read as markup once <svg> or <math> opened", "<svg><style><b>x</b></style></svg><p>y"},
	{"<noscript> ignored in a frameset: the tokenizer reads it as text (inFramesetIM)", "one text token for the run", "<frameset><noscript><!--</noscript><frame><frame>-->"},
	{"a <style> after </svg> is text to the parser (:668)", "one text token for the run", "<svg></svg><style><!--</style><p>x<p>y-->"},
	{"AllowCDATA: CDATA in foreign content (:2248)", "never written: comments are left out", "<svg><![CDATA[ > <!-- ]]><b>x<b>y-->"},
	{"ignoreTheRemainingTokens: <template> with foreign content (:687)", "as any token", "<svg><template><p>x<p>y"},
	{"inTemplateIM: <col> in a template (:1829)", "the second more a <col>", "<template><col> x <col> y"},
	{"inBodyIM: <frameset> removes the body (:927)", "as any element; the body counted once", "<frameset><frame><!--x--><noframes>y</noframes>"},
}

// The bound holds on each item of the checklist.
func TestTheBoundsChecklist(t *testing.T) {
	for _, c := range boundChecklist {
		t.Run(c.site, func(t *testing.T) {
			boundHolds(t, c.src)
			boundHolds(t, strings.Repeat(c.src, 300))
		})
	}
}

// The checklist was read in x/net v0.62.0's parse.go: when go.mod moves the
// module, the checklist must be read again in the new source.
func TestTheChecklistIsOfThePinnedParser(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "\tgolang.org/x/net v0.62.0\n") {
		t.Fatal("go.mod no longer pins golang.org/x/net v0.62.0: read parse.go again for boundChecklist")
	}
}

// adversarialFamilies are generated shapes, each of n repetitions, aimed
// at every way the parser makes more than the input spells out. Each must
// hold the bound at every n, with no slack.
func adversarialFamilies() map[string]func(n int) string {
	bs := func(n int, attrs int) string {
		var b strings.Builder
		for i := range n {
			b.WriteString("<b id=b" + strconv.Itoa(i))
			for k := range attrs {
				b.WriteString(" a" + strconv.Itoa(k) + "=x")
			}
			b.WriteString(">")
		}
		return b.String()
	}
	return map[string]func(n int) string{
		"N2 cloned attributes": func(n int) string { return "<div>" + bs(100, 63) + strings.Repeat("</div><div>x", n) },
		"amplification":        func(n int) string { return "<div>" + bs(400, 0) + strings.Repeat("</div><div>x", n) },
		"amplification in cells": func(n int) string {
			return bs(50, 5) + strings.Repeat("<table><tr><td>x</td></tr></table>y<p>z</p>", n)
		},
		"N3 </p>":                   func(n int) string { return "<body>" + strings.Repeat("</p>", n) },
		"N3 </br>":                  func(n int) string { return strings.Repeat("</br>", n) },
		"</p> and </br> in a table": func(n int) string { return "<table>" + strings.Repeat("</p></br>", n) },
		"cells implied":             func(n int) string { return "<table>" + strings.Repeat("<td>x<th>y<tr>", n) },
		"column groups implied":     func(n int) string { return "<table>" + strings.Repeat("<col> x<colgroup> y", n) },
		"foster parenting":          func(n int) string { return "<table>" + strings.Repeat("<b>x<a href=y>z<div>w</div>", n) },
		"comments and doctypes":     func(n int) string { return strings.Repeat("<!><!DOCTYPE x><!---->", n) },
		"misnested blocks":          func(n int) string { return strings.Repeat("<b><p>x</b>y</p><a href=z><div>w</a>v</div>", n) },
		"open links":                func(n int) string { return strings.Repeat("<a href=x>y<div>", n) },
		"nobr":                      func(n int) string { return strings.Repeat("<nobr>x<div><nobr>y</div>", n) },
		"Noah's Ark":                func(n int) string { return strings.Repeat("<b class=x>", n) + "<div>y</div>" },
		"nested tables":             func(n int) string { return strings.Repeat("<table><table></table><td><p><b>x</p></td>y", n) },
		"noscript in select": func(n int) string {
			return "<select><noscript><!--</noscript></select>" + strings.Repeat("<b>x<p>y", n) + "-->"
		},
		"style after svg": func(n int) string { return "<svg></svg><style><!--</style>" + strings.Repeat("<p>x<b>y", n) + "-->" },
		"CDATA in svg":    func(n int) string { return "<svg><![CDATA[ > <!-- ]]>" + strings.Repeat("<b>x<p>y", n) + "-->" },
		"script in svg":   func(n int) string { return "<svg><script>" + strings.Repeat("<b>x<p>y", n) + "</script></svg>" },
		"frameset": func(n int) string {
			return "<frameset>" + strings.Repeat("<frame><!--x--> <noscript><!--</noscript>", n)
		},
		"templates": func(n int) string { return strings.Repeat("<template><col> x <col> y</template>", n) },
		"html and body attributes": func(n int) string {
			return strings.Repeat(`<html a=1 b=2><body c=3 d="`+strings.Repeat("e", 100)+`">`, n)
		},
		"text with NUL and entities":   func(n int) string { return strings.Repeat("a\x00b&amp;c&lt;d<span>\x00</span>", n) },
		"raw text holding end tags":    func(n int) string { return strings.Repeat("<noscript><style></noscript><b>x</style></noscript>", n) },
		"title holding markup":         func(n int) string { return strings.Repeat("<noscript><title></noscript><b>x</title>", n) },
		"head white space":             func(n int) string { return "<head>" + strings.Repeat(" <meta> \n", n) + " x" },
		"select and options":           func(n int) string { return strings.Repeat("<select><option>x<b>y<optgroup>z</select>", n) },
		"headings and lists":           func(n int) string { return strings.Repeat("<h1><h2>x<li>y<li>z<dd>w<dt>v", n) },
		"buttons and forms":            func(n int) string { return strings.Repeat("<form><button><b>x<form>y</button></form>", n) },
		"marquee and object markers":   func(n int) string { return strings.Repeat("<b><marquee><i>x</marquee>y<object><u>z</object>", n) },
		"plaintext":                    func(n int) string { return strings.Repeat("<b>x", n) + "<plaintext></plaintext><b>y" },
		"unterminated tags at the end": func(n int) string { return strings.Repeat("<b>x", n) + "<a href=" },
		"weird attribute keys":         func(n int) string { return strings.Repeat(`<div a"b=1 c<d=2 =e f'g=3>x</div>`, n) },
		"style holding the end of a noscript read as text": func(n int) string {
			return "<frameset><noscript><style></noscript>" + strings.Repeat("<frame>", n) + "</style>"
		},
		"title holding the end of a noscript read as text": func(n int) string {
			return "<frameset><noscript><title></noscript>" + strings.Repeat("<frame>", n) + "</title>"
		},
		"values that would end raw text": func(n int) string {
			return strings.Repeat(`<noscript><div title="</noscript><b>">x</div></noscript>`, n)
		},
		"doctypes that would end raw text": func(n int) string {
			return strings.Repeat(`<noscript><!DOCTYPE </noscript><b>></noscript>`, n)
		},
	}
}

// Every family holds the bound, small and large.
func TestTheBoundHoldsOnAdversarialFamilies(t *testing.T) {
	families := adversarialFamilies()
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			for _, n := range []int{0, 1, 2, 3, 7, 50, 400, 3000} {
				boundHolds(t, families[name](n))
			}
		})
	}
}

// largestShown is gen's largest repetition whose part is within the limits
// of the token pass and of a part's size.
func largestShown(gen func(n int) string) int {
	accepted := func(n int) bool {
		src := gen(n)
		if len(src) > contracts.MessageHTMLMaxBytes {
			return false
		}
		_, err := prepare(context.Background(), []byte(src), true)
		return err == nil
	}
	if !accepted(0) {
		return -1
	}
	hi := 1
	for accepted(hi) {
		hi *= 2
		if hi > 1<<22 {
			return hi
		}
	}
	lo := hi / 2
	for hi-lo > 1 {
		if mid := (lo + hi) / 2; accepted(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// Every family, at the largest size the token pass accepts, renders within
// the stated memory of a render.
func TestAdversarialFamiliesStayWithinTheRenderMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("renders every family at its largest")
	}
	families := adversarialFamilies()
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			n := largestShown(families[name])
			if n < 0 {
				t.Skip("refused at its smallest")
			}
			src := families[name](n)
			var st contracts.MessageHTML
			heap, stack := peakMemory(func() {
				var err error
				if st, err = Render(context.Background(), input(src), io.Discard); err != nil {
					t.Error(err)
				}
			})
			t.Logf("n=%d, %d bytes: %s, %.1f MiB of heap", n, len(src), st.Status, float64(heap)/(1<<20))
			if heap > renderMemory || stack > renderStack {
				t.Errorf("n=%d, %d bytes: %.1f MiB of heap and %.1f MiB of stack", n, len(src), float64(heap)/(1<<20), float64(stack)/(1<<20))
			}
		})
	}
}

// The output parses back through its own stream: what the frame is sent
// reads as the same tree to the bounded parse and to the plain one.
func TestTheOutputParsesBackBoundedAndPlain(t *testing.T) {
	for _, src := range []string{
		"<p><b>x</b></p><b><div>y</div></b><a href=https://example.com>z</a>",
		"<b><b><b><b>w</b></b></b></b><table><tr><td><a href=https://example.com>v</a></td></tr></table>",
		"<p><marquee><b>u<div>t</div>s</b></marquee></p>",
	} {
		st, out := renderOK(t, input(src))
		if st.Status != contracts.HTMLShown {
			t.Fatalf("%q: %s", src, st.Status)
		}
		plain, err := html.Parse(strings.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if got := boundHolds(t, out); got != countNodes(plain) {
			t.Errorf("%q: %d nodes through the stream, %d plain", src, got, countNodes(plain))
		}
	}
}
