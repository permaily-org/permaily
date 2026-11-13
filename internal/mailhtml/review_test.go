package mailhtml

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// riff is a WebP file of the chunks given, each a fourcc and its data,
// padded to an even length, with the RIFF length right.
func riff(chunks ...riffChunk) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range chunks {
		body.WriteString(c.fourcc)
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(c.data)))
		body.Write(c.data)
		if len(c.data)%2 == 1 {
			body.WriteByte(0)
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}

type riffChunk struct {
	fourcc string
	data   []byte
}

// vp8l is a lossless image's header for w x h.
func vp8l(w, h int) riffChunk {
	d := []byte{0x2f, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(d[1:], uint32(w-1)|uint32(h-1)<<14)
	return riffChunk{"VP8L", d}
}

// vp8 is a lossy key frame's header for w x h.
func vp8(w, h int) riffChunk {
	d := []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(d[6:], uint16(w))
	binary.LittleEndian.PutUint16(d[8:], uint16(h))
	return riffChunk{"VP8 ", d}
}

// vp8x is the extended header: its flags and canvas.
func vp8x(flags byte, w, h int) riffChunk {
	x := make([]byte, 10)
	x[0] = flags
	x[4], x[5], x[6] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	x[7], x[8], x[9] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	return riffChunk{"VP8X", x}
}

func other(fourcc string) riffChunk { return riffChunk{fourcc, make([]byte, 6)} }

// pngChunks is a PNG of the chunks named, in order: IHDR:WxH is a header of
// that size, any other name a chunk of eight zero bytes (IEND empty).
func pngChunks(names ...string) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	for _, name := range names {
		var data []byte
		typ := name
		if w, h, ok := strings.Cut(strings.TrimPrefix(name, "IHDR:"), "x"); ok && strings.HasPrefix(name, "IHDR:") {
			typ = "IHDR"
			data = make([]byte, 13)
			wi, _ := strconv.Atoi(w)
			hi, _ := strconv.Atoi(h)
			binary.BigEndian.PutUint32(data, uint32(wi))
			binary.BigEndian.PutUint32(data[4:], uint32(hi))
			data[8], data[9] = 8, 6
		} else if typ != "IEND" {
			data = make([]byte, 8)
		}
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	return b.Bytes()
}

// sof0 is a baseline frame header for w x h, one component.
func sof0(w, h int) []byte {
	return []byte{0xFF, 0xC0, 0, 11, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x11, 0}
}

// Finding 2: an image is accepted only when its structure is unambiguous,
// so that the size the sanitiser measures is the one a decoder decodes. A
// WebP of an 8193 x 1 lossless image followed by a 1 x 1 one's chunk was
// measured 1 x 1; libwebp opens it as 8193 x 1.
func TestReview2AnImageMeansOneThing(t *testing.T) {
	refused := ambiguousImages(t)
	for name, b := range refused {
		if info, ok := imageStructure(b); ok {
			t.Errorf("%s: read as %+v", name, info)
		}
		// As the frame counts it: unsupported, not shown.
		st, _ := renderOK(t, input(imgs(dataURI("image/png", b))))
		if st.InlineImages != 0 || st.InlineImagesOmittedBy.Unsupported != 1 {
			t.Errorf("%s: %+v", name, st)
		}
	}
	jpg := jpegImage(t, 3, 2)
	accepted := map[string]struct {
		b    []byte
		w, h int
	}{
		"WebP: lossless":                        {riff(vp8l(3, 2)), 3, 2},
		"WebP: lossy":                           {riff(vp8(3, 2)), 3, 2},
		"WebP: extended, every optional chunk":  {riff(vp8x(0x3c, 3, 2), other("ICCP"), other("ALPH"), vp8(3, 2), other("EXIF"), other("XMP ")), 3, 2},
		"WebP: extended lossless":               {riff(vp8x(0, 3, 2), vp8l(3, 2)), 3, 2},
		"PNG: PLTE, ancillary chunks, two IDAT": {pngChunks("IHDR:3x2", "PLTE", "tEXt", "IDAT", "IDAT", "tIME", "IEND"), 3, 2},
		"JPEG: Go's own":                        {jpg, 3, 2},
		"JPEG: bytes after its end (a phone's)": {append(append([]byte{}, jpg...), "trailer of a motion photo"...), 3, 2},
	}
	for name, c := range accepted {
		if info, ok := imageStructure(c.b); !ok || info.Width != c.w || info.Height != c.h || info.Animated {
			t.Errorf("%s: %+v %v", name, info, ok)
		}
	}
	// GIF: a second image descriptor is animation, never shown, whatever
	// its size (confirmed, unchanged).
	if info, ok := imageStructure(gifImage(t, 3, 2, 2, false)); !ok || !info.Animated {
		t.Errorf("two GIF images: %+v %v", info, ok)
	}
}

// adversarialCSS are the shapes of finding 1 and their siblings, each a
// message's HTML of at most the 2 MiB a part may be: blocks opened and
// never closed, strings and comments that never end, more rules and
// declarations than the limits, in <style> (over its 256 KiB, and under)
// and in style attributes.
func adversarialCSS() map[string]string {
	const max = contracts.MessageHTMLMaxBytes - 64
	fill := func(head, unit, tail string, size int) string {
		n := (size - len(head) - len(tail)) / len(unit)
		return head + strings.Repeat(unit, n) + tail
	}
	sheet := func(unit string, size int) string { return fill("<style>", unit, "</style><p>hello</p>", size) }
	underSheet := contracts.MessageHTMLMaxStyleBytes - 64
	attrs := func(unit string) string {
		one := `<p style="` + strings.Repeat(unit, (contracts.MessageHTMLMaxStyleAttributeBytes-16)/len(unit)) + `">x</p>`
		return strings.Repeat(one, max/len(one))
	}
	return map[string]string{
		"( to 2 MiB":              sheet("(", max),
		"[ to 2 MiB":              sheet("[", max),
		"{ to 2 MiB":              sheet("{", max),
		"([{ mixed to 2 MiB":      sheet("([{", max),
		"url( to 2 MiB":           sheet("url(", max),
		"( under the sheet limit": sheet("(", underSheet),
		"[ deep, within the tokens a sheet may be": "<style>a{x:" + strings.Repeat("[", cssMaxTokens-16) + "}</style><p>hello</p>",
		"(( deep in a function, within the tokens": "<style>a{x:" + strings.Repeat("f(", (cssMaxTokens-16)/2) + "}</style><p>hello</p>",
		"([{ under the sheet limit":                sheet("([{", underSheet),
		"an unterminated string of 2 MiB":          fill(`<style>a{font-family:"`, "x", "</style><p>hello</p>", max),
		"an unterminated comment of 2 MiB":         fill("<style>/*", "x", "</style><p>hello</p>", max),
		"tiny rules to 2 MiB":                      sheet("a{}", max),
		"tiny rules under the sheet limit":         sheet("a{}", underSheet),
		"declarations to 2 MiB":                    fill("<style>a{", "b:c;", "}</style><p>hello</p>", max),
		"declarations under the sheet limit":       fill("<style>a{", "b:c;", "}</style><p>hello</p>", underSheet),
		"kept declarations under the limit":        fill("<style>a{", "color:red;", "}</style><p>hello</p>", underSheet),
		"( in style attributes":                    attrs("("),
		"([{ in style attributes":                  attrs("([{"),
		"declarations in style attributes":         attrs("color:red;"),
	}
}

// peakMemory runs f and says the most the heap and the goroutine stacks
// held above where they started, sampled every 100 microseconds.
func peakMemory(f func()) (heap, stack uint64) {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peakHeap, peakStack atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		tick := time.NewTicker(100 * time.Microsecond)
		defer tick.Stop()
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > base.HeapAlloc && m.HeapAlloc-base.HeapAlloc > peakHeap.Load() {
				peakHeap.Store(m.HeapAlloc - base.HeapAlloc)
			}
			if m.StackInuse > base.StackInuse && m.StackInuse-base.StackInuse > peakStack.Load() {
				peakStack.Store(m.StackInuse - base.StackInuse)
			}
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	f()
	close(stop)
	wg.Wait()
	return peakHeap.Load(), peakStack.Load()
}

// renderMemory is the most one render may hold at its peak (ADR 0026,
// "Limits": 64 MiB), and the stack it may grow by.
const (
	renderMemory = 64 << 20
	renderStack  = 4 << 20
	// deadlineMargin is how long past its deadline a render may take to
	// stop. It is wide on purpose: on a shared CI runner the bounded work
	// of these shapes alone took 250 to 310 ms, so a tight margin measures
	// the machine and not the code. What it still catches is work that is
	// not bounded at all; that each pass checks its context is shown, without
	// a clock, by TestReview1EveryCSSPassChecksItsContext.
	deadlineMargin = 2 * time.Second
)

// Finding 1: CSS nesting was bounded after the recursion, a stylesheet over
// its limit was still made into tokens and components to count its URLs,
// none of it checked the deadline, and Links paid for it too. Each shape
// ends within its deadline and a margin, within the render's memory and
// without a deep stack, in Render and in Links.
func TestReview1CSSWorkIsBoundedAndStopsAtTheDeadline(t *testing.T) {
	const deadline = 100 * time.Millisecond
	for name, src := range adversarialCSS() {
		if len(src) > contracts.MessageHTMLMaxBytes {
			t.Fatalf("%s: %d bytes", name, len(src))
		}
		for _, links := range []bool{false, true} {
			var err error
			var took time.Duration
			heap, stack := peakMemory(func() {
				ctx, cancel := context.WithTimeout(context.Background(), deadline)
				defer cancel()
				start := time.Now()
				if links {
					_, err = Links(ctx, input(src))
				} else {
					_, err = Render(ctx, input(src), nil)
				}
				took = time.Since(start)
			})
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrNotShown) {
				t.Errorf("%s (links %v): %v", name, links, err)
			}
			if testing.Verbose() {
				t.Logf("%s (links %v): %v, heap %.1f MiB, stack %.2f MiB, %v", name, links, took,
					float64(heap)/(1<<20), float64(stack)/(1<<20), err)
			}
			if took > deadline+deadlineMargin || heap > renderMemory || stack > renderStack {
				t.Errorf("%s (links %v): %v, heap %.1f MiB, stack %.1f MiB", name, links, took,
					float64(heap)/(1<<20), float64(stack)/(1<<20))
			}
		}
	}
}

// Finding 1: each pass of the CSS work stops with its context: the
// tokeniser, the parser into components, the skip of a block too deep, the
// filter and the count of what is discarded, given an ended context, stop
// with its error, and Render answers it.
func TestReview1EveryCSSPassChecksItsContext(t *testing.T) {
	ended, end := context.WithCancel(context.Background())
	end()
	big := strings.Repeat("a{color:red}", 20_000)
	if _, err := cssTokenize(ended, big, cssMaxTokens); !errors.Is(err, context.Canceled) {
		t.Errorf("the tokeniser: %v", err)
	}
	if err := cssScan(ended, big, func(cssToken) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("the scan: %v", err)
	}
	toks := tokenize(t, big)
	if _, err := cssComponents(ended, toks); !errors.Is(err, context.Canceled) {
		t.Errorf("the components: %v", err)
	}
	deep := tokenize(t, strings.Repeat("(", 40)+strings.Repeat("[", 20_000))
	if _, err := cssSkipBlock(ended, deep, 39); !errors.Is(err, context.Canceled) {
		t.Errorf("the skip: %v", err)
	}
	nodes, err := cssComponents(context.Background(), toks)
	if err != nil {
		t.Fatal(err)
	}
	b := &cssBudget{ctx: ended}
	var out strings.Builder
	if b.rules0(nodes, &out, 0); !errors.Is(b.err, context.Canceled) {
		t.Errorf("the filter: %v", b.err)
	}
	b = &cssBudget{ctx: ended}
	if got := b.filterStylesheet(big); got != "" || !errors.Is(b.err, context.Canceled) {
		t.Errorf("a stopped filter wrote %d bytes: %v", len(got), b.err)
	}
	b = &cssBudget{ctx: ended}
	if b.countDiscarded(big); !errors.Is(b.err, context.Canceled) {
		t.Errorf("the count of what is discarded: %v", b.err)
	}
	b = &cssBudget{ctx: ended}
	if d := b.filterStyleAttribute(strings.Repeat("color:red;", 800), "td"); d != nil || !errors.Is(b.err, context.Canceled) {
		t.Errorf("a style attribute: %v", b.err)
	}
	// Through the walk: a context that ends while a <style> is filtered.
	doc, err := parse(context.Background(), []byte("<style>"+big+"</style><p>x</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&walker{ctx: ended, in: input("")}).document(doc); !errors.Is(err, context.Canceled) {
		t.Errorf("the walk: %v", err)
	}
}

// Finding 1: CSS too deep is kept as empty blocks, so the rule around it
// still reads as one, the rest of the sheet is kept, and a rule past the
// depth limit is dropped, not read as something else.
func TestReview1DeepCSSIsSkippedNotMisread(t *testing.T) {
	deep := strings.Repeat("(", 40) + "x" + strings.Repeat(")", 40)
	for _, c := range []struct{ in, want string }{
		{"a{color:red}b{x:" + deep + "}c{color:blue}", "a{color:red}c{color:blue}"},
		{"a{color:red}" + strings.Repeat("[", 50_000) + "}c{color:blue}", "a{color:red}"},
		{"@media print{" + strings.Repeat("{", 40) + "}p{color:red}", ""},
		{"a{color:red}b{x:" + strings.Repeat("(", 50) + "url(https://e.example/x)" + "}", "a{color:red}"},
	} {
		if got := sheet(c.in); got != c.want {
			t.Errorf("%.40q: %q, want %q", c.in, got, c.want)
		}
	}
	// Past the tokens one sheet may be, it goes, and its addresses are
	// counted by the pass that keeps nothing.
	b := &cssBudget{ctx: context.Background()}
	over := strings.Repeat("a{}", cssMaxTokens/3+1) + "@import url(https://e.example/a.css);@import 'http://e.example/b.css';"
	if got := b.filterStylesheet(over); got != "" || b.remoteOther != 2 || b.err != nil {
		t.Errorf("%q, %d remote, %v", got, b.remoteOther, b.err)
	}
}

// Finding 1: Links reads no CSS at all, in <style> or in style attributes:
// the work it does is the token pass, the parse and the walk.
func TestReview1LinksReadsNoCSS(t *testing.T) {
	src := "<style>@import url(https://e.example/a.css);" + strings.Repeat("a{color:red}", 1000) + "</style>" +
		`<p style="color:red;background:url(https://e.example/b.png)"><a href="https://example.com/" style="color:red">x</a></p>`
	wk, _, err := walkInput(context.Background(), input(src), true)
	if err != nil {
		t.Fatal(err)
	}
	if wk.css.steps != 0 || wk.styleIn != 0 || wk.css.remoteOther != 0 || wk.css.remoteImages != 0 || len(wk.links) != 1 {
		t.Errorf("Links did CSS work: %d steps, %d bytes of <style>, %+v", wk.css.steps, wk.styleIn, wk.css)
	}
}

// styleOf is the style attribute of the first element of the output whose
// id is id.
func styleOf(t *testing.T, out, id string) (string, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil && found == nil; c = c.NextSibling {
			if v, ok := attr(c, "id"); ok && v == id {
				found = c
				return
			}
			walk(c)
		}
	}
	walk(doc)
	if found == nil {
		t.Fatalf("no #%s in %s", id, out)
	}
	_, hasHeight := attr(found, "height")
	s, _ := attr(found, "style")
	return s, hasHeight
}

// ancestorHolds are the declarations every element that holds a link
// carries, attached to it with !important, which no rule of a stylesheet
// can beat.
var ancestorHolds = []string{"overflow:visible !important", "height:auto !important", "max-height:none !important", "min-width:min-content !important"}

// Finding 3: a link's forced style did not stop its ancestors from hiding it
// or laying it over what follows. On every element that holds a link
// (html and body included): nothing hides it (display:none goes, from its
// style and from every rule), nothing clips it (overflow is visible), and
// no size of the ancestor holds it smaller than what it holds (height
// auto, no maximum height, at least its min-content width), whatever a
// stylesheet says; table-layout:fixed, which lets content leave its cells,
// is not kept.
func TestReview3AncestorsCannotHideOrMoveALink(t *testing.T) {
	link := `<a href="https://example.com/">visible?</a>`
	cases := []struct {
		name, src string
	}{
		{"a width of 0 that clips", `<div id="x" style="width:0;overflow:hidden">` + link + `</div>`},
		{"a table made a block of height 0", `<table id="x" style="display:block;height:0"><tr><td>` + link + `</td></tr></table><div>NEXT CONTENT</div>`},
		{"a parent hidden", `<div id="x" style="display:none"><p>` + link + `</p></div>`},
		{"a cell's height attribute", `<table><tr><td id="x" height="2" style="height:2px;max-height:2px">` + link + `</td></tr></table>`},
		{"rules on the ancestor", `<style>#x{display:none!important;overflow:hidden!important;height:0!important;max-height:0!important;width:0!important;max-width:0!important;table-layout:fixed}</style><table id="x"><tr><td>` + link + `</td></tr></table>`},
	}
	for _, c := range cases {
		_, out := renderOK(t, input(c.src))
		style, height := styleOf(t, out, "x")
		for _, want := range ancestorHolds {
			if !strings.Contains(style, want) {
				t.Errorf("%s: %q lacks %q", c.name, style, want)
			}
		}
		for _, gone := range []string{"display:none", "overflow:hidden", "height:0", "height:2px", "max-height:2px"} {
			if strings.Contains(style, gone) {
				t.Errorf("%s: %q keeps %q", c.name, style, gone)
			}
		}
		if height {
			t.Errorf("%s: the height attribute is kept", c.name)
		}
	}
	// The html and body the sanitiser writes hold every link.
	_, out := renderOK(t, input(`<style>html,body{overflow:hidden;height:0}</style>`+link))
	doc, _ := html.Parse(strings.NewReader(out))
	for _, n := range []*html.Node{doc.LastChild, doc.LastChild.LastChild} {
		s, _ := attr(n, "style")
		for _, want := range ancestorHolds {
			if !strings.Contains(s, want) {
				t.Errorf("<%s style=%q> lacks %q", n.Data, s, want)
			}
		}
	}
	// No rule hides anything: display:none goes from every rule, in @media
	// too; table-layout goes everywhere. The other values of display stay.
	for in, want := range map[string]string{
		".hide{display:none}": "",
		"@media (max-width:600px){.desk{display:none!important}}":   "",
		".col{display:block;color:red}":                             ".col{display:block;color:red}",
		"@media (max-width:600px){td.col{display:block!important}}": "@media (max-width:600px){td.col{display:block !important}}",
		"table{table-layout:fixed;color:red}":                       "table{color:red}",
	} {
		if got := sheet(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	// What holds no link keeps its own: a cell's height, a hidden block.
	_, out = renderOK(t, input(`<table><tr><td id="x" height="40" style="height:40px">spacer</td></tr></table><div id="y" style="display:none">preheader</div>`+link))
	if style, height := styleOf(t, out, "x"); !strings.Contains(style, "height:40px") || !height || strings.Contains(style, "min-content") {
		t.Errorf("a cell without a link: %q %v", style, height)
	}
	if style, _ := styleOf(t, out, "y"); !strings.Contains(style, "display:none") {
		t.Errorf("a block without a link: %q", style)
	}
}

// Test weakness of the review: the deadline was shown stopping the HTML
// parser's reader only. The image walkers check it in every loop: a PNG of
// many chunks, a JPEG of many markers and scans, a GIF of many blocks and a
// WebP of many chunks, read under an ended context, stop with its error,
// and the walk answers it.
func TestReviewTheImageWalkStopsAtTheDeadline(t *testing.T) {
	ended, end := context.WithCancel(context.Background())
	end()
	png := pngChunks(append(append([]string{"IHDR:2x2"}, slices.Repeat([]string{"tEXt"}, 5000)...), "IDAT", "IEND")...)
	jpg := jpegImage(t, 2, 2)
	app := []byte{0xFF, 0xE1, 0, 4, 0, 0}
	jpgMany := append(append(append([]byte{}, jpg[:2]...), bytes.Repeat(app, 5000)...), jpg[2:]...)
	gifMany := gifImage(t, 2, 2, 1, false)
	ext := []byte{0x21, 0xFE, 1, 'x', 0}
	gifMany = append(append(append([]byte{}, gifMany[:len(gifMany)-1]...), bytes.Repeat(ext, 5000)...), 0x3B)
	chunks := []riffChunk{vp8x(0, 2, 2)}
	for range 5000 {
		chunks = append(chunks, other("ANMF"))
	}
	for name, b := range map[string][]byte{"PNG": png, "JPEG": jpgMany, "GIF": gifMany, "WebP": riff(chunks...)} {
		if _, ok := imageStructure(b); !ok && name != "WebP" {
			t.Errorf("%s does not read without a deadline", name)
		}
		if _, ok, err := readImage(ended, b); ok || !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
	if _, err := Render(ended, input(imgs(dataURI("image/png", png))), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Render: %v", err)
	}
}

// ambiguousImages are the images of finding 2, built here: each one a
// decoder could read as something else than the walker measures.
func ambiguousImages(t testing.TB) map[string][]byte {
	jpg := jpegImage(t, 3, 2)
	sof := bytes.Index(jpg, []byte{0xFF, 0xC0})
	sofLen := int(binary.BigEndian.Uint16(jpg[sof+2:])) + 2
	eoi := len(jpg) - 2
	splice := func(at int, add []byte) []byte {
		return append(append(append([]byte{}, jpg[:at]...), add...), jpg[at:]...)
	}
	return map[string][]byte{
		"WebP: two lossless payloads":              riff(vp8l(8193, 1), vp8l(1, 1)),
		"WebP: lossless then lossy":                riff(vp8l(8193, 1), vp8(1, 1)),
		"WebP: a chunk after the payload":          riff(vp8l(2, 2), other("ABCD")),
		"WebP: EXIF in the simple form":            riff(vp8l(2, 2), other("EXIF")),
		"WebP: a canvas that is not the payload's": riff(vp8x(0, 1, 1), vp8l(8193, 1)),
		"WebP: two payloads under VP8X":            riff(vp8x(0, 2, 2), vp8l(2, 2), vp8l(2, 2)),
		"WebP: an unknown chunk under VP8X":        riff(vp8x(0, 2, 2), other("ABCD"), vp8l(2, 2)),
		"WebP: two ICCP":                           riff(vp8x(0x20, 2, 2), other("ICCP"), other("ICCP"), vp8l(2, 2)),
		"WebP: ICCP after the payload":             riff(vp8x(0x20, 2, 2), vp8l(2, 2), other("ICCP")),
		"WebP: ALPH with a lossless payload":       riff(vp8x(0x10, 2, 2), other("ALPH"), vp8l(2, 2)),
		"WebP: two EXIF":                           riff(vp8x(0x08, 2, 2), vp8l(2, 2), other("EXIF"), other("EXIF")),
		"WebP: VP8X twice":                         riff(vp8x(0, 2, 2), vp8x(0, 2, 2), vp8l(2, 2)),
		"WebP: no payload":                         riff(vp8x(0, 2, 2)),
		"PNG: two IHDR":                            pngChunks("IHDR:1x1", "IHDR:8193x1", "IDAT", "IEND"),
		"PNG: IHDR again after IDAT":               pngChunks("IHDR:1x1", "IDAT", "IHDR:8193x1", "IDAT", "IEND"),
		"PNG: IDAT not contiguous":                 pngChunks("IHDR:1x1", "IDAT", "tEXt", "IDAT", "IEND"),
		"PNG: PLTE after IDAT":                     pngChunks("IHDR:1x1", "IDAT", "PLTE", "IEND"),
		"PNG: no IDAT":                             pngChunks("IHDR:1x1", "IEND"),
		"PNG: a chunk after IEND":                  pngChunks("IHDR:1x1", "IDAT", "IEND", "IHDR:8193x1"),
		"PNG: bytes after IEND":                    append(pngChunks("IHDR:1x1", "IDAT", "IEND"), 0),
		"JPEG: two frame headers":                  splice(sof+sofLen, sof0(8193, 1)),
		"JPEG: a frame header after the scan":      splice(eoi, sof0(8193, 1)),
		"JPEG: no end of image":                    jpg[:eoi],
	}
}

// Test weakness of the review: the CSS oracle of test 18 reads the output
// with the production tokeniser, so a tokeniser bug would hide from it.
// These expectations are written by hand from CSS Syntax Level 3, token by
// token, and the filter's output for each hostile spelling written out.
func TestReviewCSSExpectationsWrittenByHand(t *testing.T) {
	type tk struct {
		kind  cssKind
		value string
	}
	for in, want := range map[string][]tk{
		`\75 rl(x)`:      {{cssURL, "x"}},
		`u\72l( "a" )`:   {{cssFunction, "url"}, {cssWhitespace, ""}, {cssString, "a"}, {cssWhitespace, ""}, {cssRParen, ""}},
		`u/**/rl(x)`:     {{cssIdent, "u"}, {cssFunction, "rl"}, {cssIdent, "x"}, {cssRParen, ""}},
		`exp\72 ession(`: {{cssFunction, "expression"}},
		`\@import`:       {{cssIdent, "@import"}},
		`@\69mport`:      {{cssAtKeyword, "import"}},
		`"a\"b`:          {{cssString, `a"b`}},
		"'line\nbreak'":  {{cssBadString, ""}, {cssWhitespace, ""}, {cssIdent, "break"}, {cssString, ""}},
		`url(a b)`:       {{cssBadURL, ""}},
		`#-x #1a`:        {{cssHash, "-x"}, {cssWhitespace, ""}, {cssHash, "1a"}},
		`<!-- -->`:       {{cssCDO, ""}, {cssWhitespace, ""}, {cssCDC, ""}},
		`-moz-binding:x`: {{cssIdent, "-moz-binding"}, {cssColon, ""}, {cssIdent, "x"}},
		`\3c /style>`:    {{cssIdent, "<"}, {cssDelim, "/"}, {cssIdent, "style"}, {cssDelim, ">"}},
	} {
		got := tokenize(t, in)
		if len(got) != len(want) {
			t.Errorf("%q: %d tokens %+v, want %+v", in, len(got), got, want)
			continue
		}
		for i := range want {
			if got[i].Kind != want[i].kind || want[i].value != "" && got[i].Value != want[i].value {
				t.Errorf("%q: token %d is %+v, want %+v", in, i, got[i], want[i])
			}
		}
	}
	// The filter, on hostile spellings: what is kept, written by hand.
	for in, want := range map[string]string{
		`p{background:\75 rl(https://e.example/x)}`:   ``,
		`p{width:exp\72 ession(alert(1))}`:            ``,
		`p{color:red;-moz-binding:url(x.xml#y)}`:      `p{color:red}`,
		`p{color:red}</style><script>x</script>`:      `p{color:red}`,
		`p{font-family:"<\/style>"}`:                  `p{font-family:"\3c /style\3e "}`,
		`a{display:none}b{display:block}`:             `b{display:block}`,
		`p{color:#ABC;margin:0 auto;padding:1px 2px}`: `p{color:#abc;margin:0 auto;padding:1px 2px}`,
		`p{margin:-5px;text-indent:-9999px}`:          ``,
		`p{position:fixed;top:0;z-index:9;color:red}`: `p{color:red}`,
		`@import url(x.css);p{color:red}`:             `p{color:red}`,
		`div>p+a~b c{color:red}`:                      `div>p+a~b c{color:red}`,
	} {
		if got := sheet(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// Found by FuzzSanitise after the review (testdata/fuzz/FuzzSanitise/
// a26e07e044c78bc2): comments and doctypes are nodes of the tree too, and
// the bound did not count them, so a part of 2 MiB of "<!>" made about
// 700,000 nodes past the 200,000 of the bound. Each counts one.
func TestFuzzFoundCommentsAreNodesToTheBound(t *testing.T) {
	for _, src := range []string{"<!><!><!><!><!0", "<!--a--><!DOCTYPE x><!--b--><p>x</p><!---->", "<!DOCTYPE html><!DOCTYPE html>"} {
		bound, err := treeBound(context.Background(), []byte(src))
		doc, _ := parse(context.Background(), []byte(src))
		if err != nil || countNodes(doc) > 3*bound+8 || bound < strings.Count(src, "<!") {
			t.Errorf("%q: bound %d, %d nodes, %v", src, bound, countNodes(doc), err)
		}
	}
	st, _ := renderOK(t, input(strings.Repeat("<!>", contracts.MessageHTMLMaxBytes/3)))
	if st.Status != contracts.HTMLTooLarge {
		t.Errorf("2 MiB of comments: %+v", st)
	}
}
