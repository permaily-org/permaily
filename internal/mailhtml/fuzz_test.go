package mailhtml

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test 21: the fuzz targets, run by make fuzz.

func corpusSeeds(f *testing.F) {
	files, _ := filepath.Glob("testdata/hostile/*.html")
	golden, _ := filepath.Glob("testdata/golden/*.html")
	for _, p := range append(files, golden...) {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(amplification(20_000)))
	f.Add([]byte("<table><b><tr><td><i>x</td><td>y</table><b>z</b>"))
	f.Add([]byte("<p><table><tr><td>quirks</td></tr></table></p><p><marquee><div>x</div></marquee></p>"))
}

// FuzzSanitise holds test 18 on any input: the output parses back to
// itself and holds nothing outside the allowlist; Links numbers what Render
// numbers; and the tree bound is a bound of the nodes the parser makes.
func FuzzSanitise(f *testing.F) {
	corpusSeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 256<<10 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		in := input(string(b))
		var out bytes.Buffer
		st, err := Render(ctx, in, &out)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if err := checkOutput(out.Bytes()); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if st.Status == "shown" {
			links, err := Links(ctx, in)
			if err != nil || len(links) != st.Links {
				t.Fatalf("Links %d, Render %d: %v", len(links), st.Links, err)
			}
		}
		if !bytes.Equal(bytes.ToValidUTF8(b, []byte("\uFFFD")), b) {
			return
		}
		bound, err := treeBound(ctx, b)
		if err != nil {
			return
		}
		doc, err := parse(ctx, b)
		if err != nil {
			return
		}
		// Each token may also imply elements (html, head, body; tbody and
		// tr for a cell): at most two a token, and four for the document.
		if n := countNodes(doc); n > 3*bound+8 {
			t.Fatalf("the bound %d is below the %d nodes the parser made", bound, n)
		}
	})
}

// FuzzCSS holds the CSS filter's promise on any input: what it writes reads
// back with no url, no function or at-rule outside the allowlist, no
// property outside it and no "<"; and it filters to itself.
func FuzzCSS(f *testing.F) {
	for _, s := range []string{
		"a{color:red}", `div{background:\75 rl(https://e.example/x)}`, "@media (max-width:600px){td{width:100% !important}}",
		"p{font:bold 12px/1.4 \"Helvetica Neue\",Arial}", "x{color:rgb(1,2,3)}", "/* c */ a:hover{color:#fff}",
		"td{height:0;overflow:hidden}", "@import 'x'; @font-face{src:url(x)}", "a[href^=\"x\"]:not(.y){color:red}",
		"li:nth-child(2n+1){margin:0 auto}", "\\3c/style\\3e", "a{font-family:'</style><script>'}",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 64<<10 {
			return
		}
		b := &cssBudget{}
		sheet := b.filterStylesheet(s)
		if err := checkCSS(sheet, false); err != nil {
			t.Fatalf("%v: %q", err, sheet)
		}
		if again := (&cssBudget{}).filterStylesheet(sheet); again != sheet {
			t.Fatalf("not a fixed point:\n%q\n%q", sheet, again)
		}
		var attr strings.Builder
		writeDecls(&attr, b.filterStyleAttribute(s, "td"), false)
		if err := checkCSS(attr.String(), true); err != nil {
			t.Fatalf("%v: %q", err, attr.String())
		}
	})
}

// FuzzImageStructure holds the walkers on any bytes: no panic, and an
// image that reads has a size and a type.
func FuzzImageStructure(f *testing.F) {
	f.Add(pngImage(f, 3, 2, false, "acTL"))
	f.Add(pngImage(f, 3, 2, true))
	f.Add(gifImage(f, 3, 2, 2, false))
	f.Add(gifImage(f, 3, 2, 1, false))
	f.Add(webpImage(3, 2, 2, "VP8L", "ANMF"))
	f.Add(jpegImage(f, 3, 2))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 8, 0, 2, 0, 3, 1, 1, 0x11, 0})
	// The ambiguities of finding 2 of the review, each refused.
	for _, b := range ambiguousImages(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		info, ok := imageStructure(b)
		if !ok {
			return
		}
		if info.Width <= 0 || info.Height <= 0 || info.Type == "" {
			t.Fatalf("%+v", info)
		}
		// The differential: where Go's own decoder (PNG, JPEG, GIF) reads
		// a file the walker accepted, the size it decodes is the size the
		// walker measured. A file it cannot decode shows nothing in a
		// browser either. Pixels are decoded only for small images.
		if info.Animated || info.Type == "image/webp" || info.Width*info.Height > 1<<16 {
			return
		}
		// A GIF's measure is its logical screen, the canvas a browser
		// draws; Go decodes its one frame, which lies inside it
		// (testdata/fuzz/FuzzImageStructure/3c69cc3eafe0ecf5: a frame of
		// 3 x 2 at 32,48 on a screen of 48 x 304).
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil && (cfg.Width != info.Width || cfg.Height != info.Height) {
			t.Fatalf("measured %dx%d, configured %dx%d", info.Width, info.Height, cfg.Width, cfg.Height)
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		switch {
		case err != nil:
		case info.Type == "image/gif":
			if !img.Bounds().In(image.Rect(0, 0, info.Width, info.Height)) {
				t.Fatalf("measured %dx%d, a frame at %v", info.Width, info.Height, img.Bounds())
			}
		case img.Bounds().Dx() != info.Width || img.Bounds().Dy() != info.Height:
			t.Fatalf("measured %dx%d, decoded %v", info.Width, info.Height, img.Bounds())
		}
	})
}

// Found by FuzzSanitise (testdata/fuzz/FuzzSanitise/6c09f66481f61887): a
// <script> in <svg> is markup to the parser, and was one text token to the
// bound's tokeniser, which then counted 3 for 18 nodes.
func TestFuzzFoundScriptInForeignContentIsMarkupToTheBound(t *testing.T) {
	for _, src := range []string{
		"<svg><sCript><A>0<A>0<A>0<A>0<A><A>0<A>",
		"<math></svg><style><b><b><b><b>x<b>y</style>",
		"<noscript><b><b><b>x<b>y<b>z</noscript>",
	} {
		bound, err := treeBound(context.Background(), []byte(src))
		doc, _ := parse(context.Background(), []byte(src))
		if err != nil || countNodes(doc) > 3*bound+8 {
			t.Errorf("%q: bound %d, %d nodes, %v", src, bound, countNodes(doc), err)
		}
	}
}

// A formatting element whose end tag comes after a block opened inside it
// stays active as a clone (the adoption agency's furthest block), and is
// cloned again at each text after the block closes: the bound keeps
// counting it.
func TestTheBoundCountsWhatTheAdoptionAgencyKeeps(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "<b id=%d><div>", i)
	}
	for range 200 {
		b.WriteString("</b>")
	}
	b.WriteString(strings.Repeat("</div>x<div></b>", 200))
	src := b.String()
	bound, err := treeBound(context.Background(), []byte(src))
	doc, _ := parse(context.Background(), []byte(src))
	if err == nil && countNodes(doc) > 3*bound+8 {
		t.Errorf("bound %d, %d nodes", bound, countNodes(doc))
	}
}
