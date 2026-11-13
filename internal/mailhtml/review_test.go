package mailhtml

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"strconv"
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
	jpg := jpegImage(t, 3, 2)
	sof := bytes.Index(jpg, []byte{0xFF, 0xC0})
	sofLen := int(binary.BigEndian.Uint16(jpg[sof+2:])) + 2
	eoi := len(jpg) - 2
	splice := func(at int, add []byte) []byte {
		return append(append(append([]byte{}, jpg[:at]...), add...), jpg[at:]...)
	}
	refused := map[string][]byte{
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
