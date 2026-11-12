package mailhtml

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	_ "image/png" // image.DecodeConfig of the 8192 x 8192 PNG
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/permaily-org/permaily/internal/contracts"
)

var update = flag.Bool("update", false, "write testdata/urls.json and the golden outputs again")

var testKey = []byte("a link key of the tests' mailbox")

func input(src string) Input {
	return Input{HTML: []byte(src), Mailbox: "mbx_test", Message: "18e7000000000000aaaaaaaaaaaaaaaa", LinkKey: testKey}
}

// renderOK renders and checks the output against the allowlist (test 18).
func renderOK(t *testing.T, in Input) (contracts.MessageHTML, string) {
	t.Helper()
	var out bytes.Buffer
	st, err := Render(context.Background(), in, &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOutput(out.Bytes()); err != nil {
		t.Fatalf("the output breaks the allowlist: %v\n%s", err, out.String())
	}
	if st.Status != contracts.HTMLShown && out.Len() != 0 {
		t.Fatalf("%s with output", st.Status)
	}
	return st, out.String()
}

// The images of item 15, by Content-ID.
func corpusInline(t *testing.T) map[string]InlinePart {
	return map[string]InlinePart{
		"text@x": {Type: "text/plain", Data: []byte("not an image")},
		"svg@x":  {Type: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)},
		"liar@x": {Type: "image/png", Data: gifImage(t, 2, 2, 1, false)},
		"big@x":  {Type: "image/png", TooLarge: true},
	}
}

// Items 1 to 10, 15 and 16: every file of the hostile corpus, with test 18
// on its output and what each case is about.
func TestHostileCorpus(t *testing.T) {
	files, err := filepath.Glob("testdata/hostile/*.html")
	if err != nil || len(files) < 15 {
		t.Fatalf("%v %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			in := input(string(b))
			in.Inline = corpusInline(t)
			st, out := renderOK(t, in)
			if st.Status != contracts.HTMLShown {
				t.Fatalf("%+v", st)
			}
			links, err := Links(context.Background(), in)
			if err != nil || len(links) != st.Links {
				t.Fatalf("Links %d, Render %d: %v", len(links), st.Links, err)
			}
			// No address or script of the message in any attribute but the
			// text of a title or an alt, nor in CSS (the output parsed, not
			// searched as text).
			for _, v := range attributeValues(t, out) {
				for _, word := range []string{"evil.example", "phish.example", "javascript", "alert(", "onerror"} {
					if strings.Contains(strings.ToLower(v), word) && !(name[:2] == "16" && strings.HasPrefix(v, "/inbox/")) {
						t.Errorf("%q in an attribute: %.80q", word, v)
					}
				}
			}
			corpusCase(t, name, st, out, links)
			if testing.Verbose() {
				t.Logf("%+v", st)
			}
		})
	}
}

func corpusCase(t *testing.T, name string, st contracts.MessageHTML, out string, links []Link) {
	t.Helper()
	switch name[:2] {
	case "01", "02", "07", "08":
		if st.Links > 1 {
			t.Errorf("%d links", st.Links)
		}
	case "03":
		if st.Links != 0 {
			t.Errorf("a link of a scheme that is not http(s) kept: %+v", links)
		}
		if st.RemoteImages != 1 {
			t.Errorf("remote images %d", st.RemoteImages)
		}
	case "04":
		if st.RemoteImages < 7 || st.RemoteOther < 4 {
			t.Errorf("%+v", st)
		}
		if !strings.Contains(out, "color:green") || strings.Contains(out, "position") {
			t.Error(out)
		}
	case "05":
		if st.RemoteImages != 9 || st.RemoteOther != 9 || strings.Count(out, "Remote image not loaded") != 2 {
			t.Errorf("%+v %d boxes", st, strings.Count(out, "Remote image not loaded"))
		}
	case "06":
		if !strings.Contains(out, "Password") || strings.Contains(out, "dialog") {
			t.Error(out)
		}
	case "09":
		if st.Links < 1 {
			t.Fatalf("%+v", st)
		}
		for _, bad := range []string{"70vh", "300px", "-9999", "color:transparent", "visibility", "clip-path", "pointer-events", "font-size:0", "line-height:0", "translatey"} {
			if strings.Contains(strings.ReplaceAll(strings.ToLower(out), "background-color:transparent", ""), bad) {
				t.Errorf("%q in the output", bad)
			}
		}
	case "10":
		if st.Links != 0 {
			t.Errorf("a refused host kept: %+v", links)
		}
		if got := bodyText(t, out); got != "a b c d e f g h i j k l m" {
			t.Errorf("not kept as text: %q", got)
		}
	case "15":
		want := contracts.MessageImagesOmitted{TooLarge: 1, Unsupported: 3}
		if st.InlineImagesOmittedBy != want || st.InlineImages != 1 || !strings.Contains(out, "data:image/gif;base64,") {
			t.Errorf("%+v", st)
		}
	case "16":
		if len(links) != 3 {
			t.Fatalf("%+v", links)
		}
		if l := links[0]; l.URL != "https://evil.example/login" || l.Host != "evil.example" || l.Shown != "bank.example" {
			t.Errorf("text naming another host: %+v", l)
		}
		// An IDN look-alike: its host in ASCII (xn--), so it is seen.
		for _, l := range links[1:] {
			if !strings.HasPrefix(l.Host, "xn--") || !strings.HasPrefix(l.URL, "https://xn--") {
				t.Errorf("look-alike: %+v", l)
			}
		}
		if links[1].Shown != "paypal.example" || links[2].Shown != "" {
			t.Errorf("%+v", links[1:])
		}
		for _, text := range []string{"ceo@bank.example [boss@evil.example]", "same@bank.example ", "call us [+15551234]", "top"} {
			if !strings.Contains(out, text) {
				t.Errorf("%q not in the output", text)
			}
		}
		if strings.Contains(out, "same@bank.example [") {
			t.Error("an address written twice")
		}
	}
}

// pngImage is a PNG of the given size whose structure reads: IHDR, one IDAT
// (valid, of zeros, when valid is set; a few bytes otherwise), extra chunks,
// IEND.
func pngImage(t testing.TB, w, h int, valid bool, extra ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 6 // 8 bits, RGBA
	chunk("IHDR", ihdr)
	for _, e := range extra {
		chunk(e, make([]byte, 8))
	}
	idat := []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01}
	if valid {
		var z bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
		row := make([]byte, 1+4*w)
		for range h {
			_, _ = zw.Write(row)
		}
		_ = zw.Close()
		idat = z.Bytes()
	}
	chunk("IDAT", idat)
	chunk("IEND", nil)
	return b.Bytes()
}

// gifImage is a GIF of frames frames; loop adds a NETSCAPE2.0 extension.
func gifImage(t testing.TB, w, h, frames int, loop bool) []byte {
	t.Helper()
	p := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for range frames {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, w, h), p))
		g.Delay = append(g.Delay, 1)
	}
	if !loop {
		g.LoopCount = -1
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpImage is a WebP: VP8X with flags and the chunks given.
func webpImage(w, h int, flags byte, chunks ...string) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	x := make([]byte, 10)
	x[0] = flags
	x[4], x[5], x[6] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	x[7], x[8], x[9] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	write := func(fourcc string, data []byte) {
		body.WriteString(fourcc)
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(data)))
		body.Write(data)
		if len(data)%2 == 1 {
			body.WriteByte(0)
		}
	}
	write("VP8X", x)
	for _, c := range chunks {
		if c == "VP8L" {
			bits := uint32(w-1) | uint32(h-1)<<14
			d := []byte{0x2f, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(d[1:], bits)
			write(c, d)
			continue
		}
		write(c, make([]byte, 16))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}

func dataURI(typ string, b []byte) string {
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func imgs(srcs ...string) string {
	var b strings.Builder
	for _, s := range srcs {
		b.WriteString(`<img alt="i" src="` + s + `">`)
	}
	return b.String()
}

// Item 11: the images that cost the browser to decode, each counted under
// its reason.
func TestImages11(t *testing.T) {
	bomb := decodingBomb(t)
	if len(bomb) > 300_000 {
		t.Fatalf("the PNG is %d bytes", len(bomb))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(bomb))
	if err != nil || cfg.Width != 8192 || cfg.Height != 8192 {
		t.Fatalf("DecodeConfig: %+v %v", cfg, err)
	}
	t.Logf("the decoding-cost PNG is %d bytes", len(bomb))
	cases := []struct {
		name string
		src  string
		want contracts.MessageImagesOmitted
	}{
		{"8192 x 8192 in 260 KB", dataURI("image/png", bomb), contracts.MessageImagesOmitted{TooLarge: 1}},
		{"8,193 wide", dataURI("image/png", pngImage(t, 8193, 1, false)), contracts.MessageImagesOmitted{TooLarge: 1}},
		{"exactly 8,192 x 2,048", dataURI("image/png", pngImage(t, 8192, 2048, false)), contracts.MessageImagesOmitted{}},
		{"APNG", dataURI("image/png", pngImage(t, 4, 4, false, "acTL")), contracts.MessageImagesOmitted{Animated: 1}},
		{"two image descriptors", dataURI("image/gif", gifImage(t, 4, 4, 2, false)), contracts.MessageImagesOmitted{Animated: 1}},
		{"NETSCAPE2.0 loop", dataURI("image/gif", gifLoop(t)), contracts.MessageImagesOmitted{Animated: 1}},
		{"a frame outside the screen", dataURI("image/gif", gifOutside(t)), contracts.MessageImagesOmitted{Unsupported: 1}},
		{"WebP animation flag", dataURI("image/webp", webpImage(4, 4, 0x02, "VP8L")), contracts.MessageImagesOmitted{Animated: 1}},
		{"WebP ANMF without the flag", dataURI("image/webp", webpImage(4, 4, 0, "ANMF")), contracts.MessageImagesOmitted{Animated: 1}},
		{"static WebP", dataURI("image/webp", webpImage(4, 4, 0, "VP8L")), contracts.MessageImagesOmitted{}},
		{"truncated chunk", dataURI("image/png", pngImage(t, 4, 4, false)[:40]), contracts.MessageImagesOmitted{Unsupported: 1}},
		{"SVG", dataURI("image/svg+xml", []byte("<svg/>")), contracts.MessageImagesOmitted{Unsupported: 1}},
		{"JPEG", dataURI("image/jpeg", jpegImage(t, 640, 480)), contracts.MessageImagesOmitted{}},
	}
	for _, c := range cases {
		st, _ := renderOK(t, input(imgs(c.src)))
		shown := c.want == (contracts.MessageImagesOmitted{})
		if st.InlineImagesOmittedBy != c.want || (st.InlineImages == 1) != shown {
			t.Errorf("%s: %+v", c.name, st)
		}
	}

	// 50 images whose pixels together are the limit, and one past it.
	var at, past []string
	for i := range 50 {
		w, h := 1024, 640
		if i >= 48 {
			h = 1024
		}
		at = append(at, dataURI("image/png", pngImage(t, w, h, false)))
		if i == 49 {
			h++
		}
		past = append(past, dataURI("image/png", pngImage(t, w, h, false)))
	}
	if st, _ := renderOK(t, input(imgs(at...))); st.InlineImages != 50 || st.InlineImagesOmitted != 0 {
		t.Errorf("at the limit: %+v", st)
	}
	if st, _ := renderOK(t, input(imgs(past...))); st.InlineImages != 49 || st.InlineImagesOmittedBy.TooLarge != 1 {
		t.Errorf("past the limit: %+v", st)
	}
	// The 51st image, and bytes past 5 MiB together.
	if st, _ := renderOK(t, input(imgs(append(at[:1:1], slices.Repeat(at[:1], 50)...)...))); st.InlineImages != 50 || st.InlineImagesOmittedBy.TooLarge != 1 {
		t.Errorf("51 images: %+v", st)
	}
	// Through cid: parts, the only way an image of more than the part's
	// 2 MiB comes: one over 2 MiB, and three of 1.9 MiB, past 5 MiB
	// together.
	big := append(pngImage(t, 4, 4, false), make([]byte, 1900<<10)...)
	in := input(imgs("cid:a", "cid:a", "cid:a", "cid:over", "cid:cut"))
	in.Inline = map[string]InlinePart{
		"a":    {Type: "image/png", Data: big},
		"over": {Type: "image/png", Data: append(pngImage(t, 4, 4, false), make([]byte, 2<<20)...)},
		"cut":  {Type: "image/png", TooLarge: true},
	}
	if st, _ := renderOK(t, in); st.InlineImages != 2 || st.InlineImagesOmittedBy.TooLarge != 3 {
		t.Errorf("past 5 MiB, over 2 MiB: %+v", st)
	}
}

var (
	bombOnce sync.Once
	bomb     []byte
)

// decodingBomb is item 11's PNG: valid, 8192 x 8192 RGBA, its one IDAT
// deflated zeros; made once.
func decodingBomb(t *testing.T) []byte {
	bombOnce.Do(func() { bomb = pngImage(t, 8192, 8192, true) })
	return bomb
}

// gifLoop is one frame with a NETSCAPE2.0 loop extension (Go's encoder
// writes one only for several frames).
func gifLoop(t *testing.T) []byte {
	b := gifImage(t, 4, 4, 1, false)
	at := 13
	if b[10]&0x80 != 0 {
		at += 3 << (int(b[10]&7) + 1)
	}
	ext := append([]byte{0x21, 0xFF, 11}, "NETSCAPE2.0\x03\x01\x00\x00\x00"...)
	return append(append(append([]byte{}, b[:at]...), ext...), b[at:]...)
}

func gifOutside(t *testing.T) []byte {
	b := gifImage(t, 4, 4, 1, false)
	// The image descriptor follows the header, the screen and the colour
	// table; its left edge is moved past the screen.
	i := bytes.IndexByte(b[13+6:], 0x2C) + 13 + 6
	binary.LittleEndian.PutUint16(b[i+1:], 3)
	return b
}

func jpegImage(t *testing.T, w, h int) []byte {
	t.Helper()
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	sof := []byte{0xFF, 0xC0, 0, 11, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x11, 0}
	return append(append(b, sof...), 0xFF, 0xD9)
}

// Item 12: 2 MiB of "&" is refused at 4 MiB of output, while it is
// written: the writer stops at its cap.
func TestOutputBound12(t *testing.T) {
	src := strings.Repeat("&", contracts.MessageHTMLMaxBytes)
	if st, _ := renderOK(t, input(src)); st.Status != contracts.HTMLTooLarge {
		t.Fatalf("%+v", st)
	}
	wk, body, err := walkInput(context.Background(), input(src), false)
	if err != nil {
		t.Fatal(err)
	}
	w := &cappedWriter{max: contracts.MessageHTMLMaxMarkupBytes}
	if err := html.Render(w, document(wk, body)); !errors.Is(err, errOutputCap) || w.buf.Len() > contracts.MessageHTMLMaxMarkupBytes {
		t.Fatalf("%v, %d bytes kept", err, w.buf.Len())
	}
}

// amplification is item 13's case: <div>, 400 <b id=N>, then </div><div>x
// to size bytes.
func amplification(size int) string {
	var b strings.Builder
	b.WriteString("<div>")
	for i := range 400 {
		b.WriteString(`<b id=` + strconv.Itoa(i) + `>`)
	}
	for b.Len() < size-12 {
		b.WriteString("</div><div>x")
	}
	return b.String()
}

// allocated is what f allocates, in bytes.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Item 13 and test 19: the tree bound refuses before any tree is built.
func TestTreeBound13(t *testing.T) {
	brs := strings.Repeat("<br>", contracts.MessageHTMLMaxBytes/4)
	amp := amplification(contracts.MessageHTMLMaxBytes)
	for name, src := range map[string]string{"524,288 <br>": brs, "amplification": amp} {
		if _, err := treeBound(context.Background(), []byte(src)); !errors.Is(err, errTooLarge) {
			t.Errorf("%s: the tokeniser pass said %v", name, err)
		}
		var st contracts.MessageHTML
		n := allocated(func() { st, _ = Render(context.Background(), input(src), nil) })
		if st.Status != contracts.HTMLTooLarge || n > 8<<20 {
			t.Errorf("%s: %s, %d bytes allocated (a tree was built?)", name, st.Status, n)
		}
	}
	// Without the bound, the parser's reconstruction clones 400 elements a
	// round: twenty rounds already make 8,000 nodes.
	small := amplification(5 + 400*12 + 20*12)
	doc, err := html.Parse(strings.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if n := countNodes(doc); n < 8000 {
		t.Errorf("the amplification case makes %d nodes", n)
	}
	if bound, err := treeBound(context.Background(), []byte(small)); err != nil || bound < countNodes(doc) {
		t.Errorf("bound %d for %d nodes: %v", bound, countNodes(doc), err)
	}

	at := strings.Repeat("<br>", contracts.MessageHTMLMaxStartTags)
	if st, _ := renderOK(t, input(at)); st.Status != contracts.HTMLShown {
		t.Errorf("50,000 start tags: %s", st.Status)
	}
	if st, _ := renderOK(t, input(at+"<br>")); st.Status != contracts.HTMLTooLarge {
		t.Errorf("50,001 start tags: %s", st.Status)
	}
	for n, want := range map[int]string{256: contracts.HTMLShown, 257: contracts.HTMLTooLarge, 513: contracts.HTMLTooLarge} {
		src := strings.Repeat("<div>", n) + "x"
		if st, _ := renderOK(t, input(src)); st.Status != want {
			t.Errorf("nesting %d: %s", n, st.Status)
		}
	}
	// Attributes: 64 on one element, then 65; an attribute's value past
	// 8 KiB goes alone.
	attrs := func(n int) string {
		var b strings.Builder
		b.WriteString("<p")
		for i := range n {
			b.WriteString(" data-" + strconv.Itoa(i) + "=x")
		}
		return b.String() + ">x</p>"
	}
	if st, _ := renderOK(t, input(attrs(64))); st.Status != contracts.HTMLShown {
		t.Error("64 attributes")
	}
	if st, _ := renderOK(t, input(attrs(65))); st.Status != contracts.HTMLTooLarge {
		t.Error("65 attributes")
	}
	long := strings.Repeat("a", contracts.MessageHTMLMaxAttributeBytes)
	if _, out := renderOK(t, input(`<p title="`+long+`">x</p><p title="`+long+`b">y</p>`)); strings.Count(out, "title=") != 1 {
		t.Error("an attribute past 8 KiB kept")
	}
	if st, _ := renderOK(t, input(strings.Repeat("a", contracts.MessageHTMLMaxBytes+1))); st.Status != contracts.HTMLTooLarge {
		t.Error("a part over 2 MiB")
	}
}

// attributeValues are the values of every attribute of the output but
// title and alt, and the text of its style elements.
func attributeValues(t *testing.T, out string) []string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var vals []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key != "title" && a.Key != "alt" {
					vals = append(vals, a.Val)
				}
			}
			if n.Data == "style" && n.FirstChild != nil {
				vals = append(vals, n.FirstChild.Data)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return vals
}

// bodyText is the text of the output's body, its white space collapsed.
func bodyText(t *testing.T, out string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, in bool) {
		if n.Type == html.TextNode && in {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, in || n.Data == "body")
		}
	}
	walk(doc, false)
	return strings.Join(strings.Fields(b.String()), " ")
}

func countNodes(n *html.Node) int {
	c := 1
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += countNodes(k)
	}
	return c
}

// countingReader counts reads, and ends ctx at the stop-th.
type countingReader struct {
	r      *strings.Reader
	reads  int
	stop   int
	cancel context.CancelFunc
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == c.stop {
		c.cancel()
	}
	return c.r.Read(p)
}

// Test 19: each budget stops the work while it runs.
func TestBudgets19(t *testing.T) {
	// The parse stops within one read of the deadline.
	ctx, cancel := context.WithCancel(context.Background())
	src := strings.Repeat("<p>paragraph</p>", 10_000)
	cr := &countingReader{r: strings.NewReader(src), stop: 3, cancel: cancel}
	_, err := html.ParseWithOptions(&deadlineReader{ctx: ctx, r: cr}, html.ParseOptionEnableScripting(false))
	if !errors.Is(err, context.Canceled) || cr.reads != 3 {
		t.Errorf("%v after %d reads", err, cr.reads)
	}
	if n, _ := (&deadlineReader{ctx: context.Background(), r: strings.NewReader(src)}).Read(make([]byte, 1<<20)); n != readChunk {
		t.Errorf("a read of %d bytes", n)
	}
	// A deadline that has passed: the render stops with it, for the caller
	// to say the view is unavailable.
	done, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := Render(done, input(src), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("past the deadline: %v", err)
	}
	if _, err := Links(done, input(src)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Links past the deadline: %v", err)
	}
	// The walk checks its context every 1,024 nodes.
	doc, err := parse(context.Background(), []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ended, end := context.WithCancel(context.Background())
	end()
	if _, err := (&walker{ctx: ended}).document(doc); !errors.Is(err, context.Canceled) {
		t.Errorf("the walk: %v", err)
	}
	// The tokeniser pass checks it too.
	if _, err := treeBound(ended, []byte(src)); !errors.Is(err, context.Canceled) {
		t.Errorf("the token pass: %v", err)
	}
}

// Test 20: the charset order of item 14, step by step.
func TestCharset20(t *testing.T) {
	ascii := strings.Repeat("a", 1024)
	late := []byte(ascii + "<p>Café</p>")
	if enc, name, _ := charset.DetermineEncoding(late, ""); name != "windows-1252" || enc == nil {
		t.Fatalf("DetermineEncoding says %s here", name)
	}
	cases := []struct {
		name, header string
		part         []byte
		want, status string
	}{
		{"1. the header's label", "iso-8859-1", []byte("Caf\xe9"), "Café", ""},
		{"1. the header wins over a BOM", "iso-8859-1", []byte("\xef\xbb\xbfx"), "ï»¿x", ""},
		{"1. a wrong label reads as it says", "iso-8859-1", []byte("Café"), "CafÃ©", ""},
		{"1. an unknown label", "x-no-such-charset", []byte("x"), "", contracts.HTMLUnreadable},
		{"1. UTF-7", "utf-7", []byte("+ADw-script+AD4-"), "", contracts.HTMLUnreadable},
		{"1. iso-2022-kr is the replacement encoding", "iso-2022-kr", []byte("x"), "", contracts.HTMLUnreadable},
		{"1. invalid UTF-8 under utf-8", "utf-8", []byte("a\xffb"), "a\uFFFDb", ""},
		{"2. a UTF-8 BOM", "", []byte("\xef\xbb\xbfCafé"), "Café", ""},
		{"2. a UTF-16LE BOM", "", []byte("\xff\xfeC\x00a\x00"), "Ca", ""},
		{"2. a UTF-16BE BOM", "", []byte("\xfe\xff\x00C\x00a"), "Ca", ""},
		{"3. <meta charset>", "", []byte(`<meta charset="koi8-r">` + "\xc1"), `<meta charset="koi8-r">а`, ""},
		{"3. <meta http-equiv>", "", []byte(`<meta http-equiv="Content-Type" content="text/html; charset=iso-8859-2">` + "\xb1"), `<meta http-equiv="Content-Type" content="text/html; charset=iso-8859-2">ą`, ""},
		{"3. a <meta> after 1,024 bytes is ignored", "", []byte(ascii + `<meta charset="iso-8859-1">Café`), ascii + `<meta charset="iso-8859-1">Café`, ""},
		{"3. a <meta> naming UTF-16 is UTF-8", "", []byte(`<meta charset="utf-16">Café`), `<meta charset="utf-16">Café`, ""},
		{"4. UTF-8 after 1,024 ASCII bytes", "", late, ascii + "<p>Café</p>", ""},
		{"5. Windows-1252", "", []byte("Caf\xe9 \x80"), "Café €", ""},
		{"a part that grows past 2 MiB", "windows-1252", bytes.Repeat([]byte{0xe9}, contracts.MessageHTMLMaxBytes), "", contracts.HTMLTooLarge},
		{"a part over 2 MiB", "", bytes.Repeat([]byte("a"), contracts.MessageHTMLMaxBytes+1), "", contracts.HTMLTooLarge},
	}
	for _, c := range cases {
		out, status := DecodeCharset(c.part, c.header)
		if status != c.status || status == "" && string(out) != c.want {
			t.Errorf("%s: %q %q", c.name, out, status)
		}
	}
	if got := charsetInContent(`text/html; CHARSET = "utf-8"`); got != "utf-8" {
		t.Errorf("%q", got)
	}
	for _, s := range []string{"text/html", "charset", `charset="x`, "charset=", "charsetx charset=y"} {
		_ = charsetInContent(s)
	}
}

// Item 16: 1,000 and 1,001 links; a URL of 4,096 and 4,097 bytes.
func TestLinks16(t *testing.T) {
	var b strings.Builder
	for i := range 1001 {
		fmt.Fprintf(&b, `<a href="https://l%d.example/">l%d</a> `, i, i)
	}
	st, out := renderOK(t, input(b.String()))
	if st.Links != 1000 || !strings.Contains(out, "l1000") || strings.Contains(out, "l1000.example") {
		t.Errorf("%d links", st.Links)
	}
	prefix := "https://long.example/"
	at := prefix + strings.Repeat("a", 4096-len(prefix))
	if _, ok := parseLink(at); !ok {
		t.Error("a URL of 4,096 bytes")
	}
	if _, ok := parseLink(at + "a"); ok {
		t.Error("a URL of 4,097 bytes")
	}
}

// Item 17: link tokens.
func TestLinkTokens17(t *testing.T) {
	two := `<a href="https://first.example/">1</a><a href="https://second.example/">2</a>`
	links, err := Links(context.Background(), input(two))
	if err != nil || len(links) != 2 {
		t.Fatal(links, err)
	}
	for _, l := range links {
		if !contracts.MessageLinkToken(l.Token()) {
			t.Fatalf("%q", l.Token())
		}
		if got, ok := Find(links, l.Token()); !ok || got != l {
			t.Fatal(l)
		}
	}
	// Checked by a sanitiser that dropped the earlier link: both tokens are
	// link_changed, never the other address.
	later, _ := Links(context.Background(), input(`<a href="https://second.example/">2</a>`))
	for _, l := range links {
		if _, ok := Find(later, l.Token()); ok {
			t.Errorf("%s found after a link was dropped", l.Token())
		}
	}
	// Another message, another mailbox.
	other := input(two)
	other.Message = "18e7000000000000bbbbbbbbbbbbbbbb"
	otherLinks, _ := Links(context.Background(), other)
	keyed := input(two)
	keyed.LinkKey = []byte("another mailbox's key")
	keyedLinks, _ := Links(context.Background(), keyed)
	for _, l := range links {
		if _, ok := Find(otherLinks, l.Token()); ok {
			t.Error("a tag of another message")
		}
		if _, ok := Find(keyedLinks, l.Token()); ok {
			t.Error("a tag of another mailbox")
		}
	}
	for _, bad := range []string{"", "x", "01.AAAAAAAAAAAAAAAA", "1000.AAAAAAAAAAAAAAAA", "1.AAAA", "1.AAAAAAAAAAAAAAA+"} {
		if contracts.MessageLinkToken(bad) {
			t.Errorf("%q has the shape of a token", bad)
		}
		if _, ok := Find(links, bad); ok {
			t.Errorf("%q found", bad)
		}
	}
	if _, err := Links(context.Background(), input(strings.Repeat("<br>", 50_001))); !errors.Is(err, ErrNotShown) {
		t.Errorf("too large: %v", err)
	}
}

// The structure walkers on what does not read.
func TestImageStructureRefuses(t *testing.T) {
	good := [][]byte{pngImage(t, 3, 2, false), gifImage(t, 3, 2, 1, false), webpImage(3, 2, 0, "VP8L"), jpegImage(t, 3, 2)}
	for _, b := range good {
		info, ok := imageStructure(b)
		if !ok || info.Width != 3 || info.Height != 2 || info.Animated {
			t.Errorf("%+v %v", info, ok)
		}
		for n := range len(b) - 1 {
			if info, ok := imageStructure(b[:n]); ok && n < len(b)-1 && info.Type != "image/jpeg" {
				t.Errorf("%s cut at %d reads", info.Type, n)
			}
		}
	}
	vp8 := []byte("RIFF\x16\x00\x00\x00WEBPVP8 \x0a\x00\x00\x00\x00\x00\x00\x9d\x01\x2a\x03\x00\x02\x00")
	if info, ok := imageStructure(vp8); !ok || info.Width != 3 || info.Height != 2 {
		t.Errorf("VP8: %+v %v", info, ok)
	}
	for _, b := range [][]byte{nil, []byte("GIF89a"), []byte("RIFF\xff\xff\xff\xffWEBP"), []byte("\xff\xd8\xff\xda"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIDAT")} {
		if _, ok := imageStructure(b); ok {
			t.Errorf("%q reads", b)
		}
	}
}

// The link grammar, case by case.
func TestParseLink(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Example.COM":                   "https://example.com/",
		"https://example.com:443/":              "https://example.com/",
		"http://example.com:80/a":               "http://example.com/a",
		"http://example.com:8080/a/./b/../c":    "http://example.com:8080/a/c",
		"https://example.com/a/%2e%2E/b/.":      "https://example.com/b/",
		"https://example.com/é ' \"<>`{}|^[]":   "https://example.com/%C3%A9%20%27%20%22%3C%3E%60%7B%7D%7C%5E%5B%5D",
		"https://example.com/?q=a b&r='#f g#h":  "https://example.com/?q=a%20b&r=%27#f%20g%23h",
		"https://example.com/%zz%41":            "https://example.com/%25zz%41",
		"https://bücher.example/":               "https://xn--bcher-kva.example/",
		"https://ｅｘａｍｐｌｅ.com/":                  "https://example.com/",
		"https://example.com/..":                "https://example.com/",
		"https://example.com?x":                 "https://example.com/?x",
		"https://example.com#":                  "https://example.com/#",
		"https://xn--bcher-kva.example/":        "https://xn--bcher-kva.example/",
		"https://a.b.c.example.museum/":         "https://a.b.c.example.museum/",
		"https://example.com:65535/":            "https://example.com:65535/",
		"https://sub-domain.example.co.uk/path": "https://sub-domain.example.co.uk/path",
	}
	for in, want := range cases {
		got, ok := parseLink(in)
		if !ok || got.URL != want {
			t.Errorf("%q: %q %v, want %q", in, got.URL, ok, want)
		}
	}
	for _, in := range []string{"", "ftp://example.com/", "http:example.com", "http:/example.com", "http:///example.com",
		"https://example.com:0/", "https://example.com:65536/", "https://example.com:/", "https://-a.example/",
		"https://a-.example/", "https://a_b.example/", "https://ab--cd.example/", "https://example.1/", "https://example.c/",
		"https://example/", "https://" + strings.Repeat("a", 64) + ".example/", "https://xn--.example/x", "https://exa mple.com/",
		"https://example.com\\x/", "https://example.xn--/", "https://ex%41mple.com", "mailto:a@b.example"} {
		if got, ok := parseLink(in); ok {
			t.Errorf("%q kept as %q", in, got.URL)
		}
	}
	for in, want := range map[string]string{"https://bank.example/login": "bank.example", "www.bank.example": "www.bank.example",
		"bank.example": "bank.example", "Click here": "", "file.x": "", "see bank.example now": "", "https://exa mple.com": ""} {
		if got := textHost(in); got != want {
			t.Errorf("textHost(%q) = %q", in, got)
		}
	}
	if got := whatwgTrim(" \x00 ht\ttp\n:// \x1f"); got != "http://" {
		t.Errorf("%q", got)
	}
}
