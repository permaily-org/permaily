package mailhtml

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Attributes, as ADR 0026's allowlist keeps and writes them.
func TestAttributes(t *testing.T) {
	cases := map[string]string{
		`<p dir="RTL" lang="en-GB" title="t" align="center" class="a  b c$ " id="x">p</p>`: `<p align="center" class="a b" dir="rtl" id="x" lang="en-GB" title="t">p</p>`,
		`<p dir="sideways" lang="e n" id="a b" align="left;x">p</p>`:                       `<p>p</p>`,
		`<table width="600px" height="50%" border="0" cellpadding="4" cellspacing="0" bgcolor="FFFFFF"><tr><td nowrap="nowrap" colspan="2" rowspan="1" scope="col" headers="h1 h2" valign="top" bgcolor="red">x</td></tr></table>`: `<table bgcolor="#ffffff" border="0" cellpadding="4" cellspacing="0" height="50%" width="600"><tbody><tr><td bgcolor="red" colspan="2" headers="h1 h2" nowrap="" rowspan="1" scope="col" valign="top">x</td></tr></tbody></table>`,
		`<table width="-1" bgcolor="#12" border="x"><tr><td colspan="99999" scope="cell">x</td></tr></table>`:                                                                                                                      `<table><tbody><tr><td>x</td></tr></tbody></table>`,
		`<ol start="3" type="i" reversed><li value="7">x</li></ol>`:                                                        `<ol reversed="" start="3" type="i"><li value="7">x</li></ol>`,
		`<font color="#00F" face="Arial, 'Helvetica'" size="+1">x</font><font face="a;b" size="9" color="url(x)">y</font>`: `<font color="#00f" face="Arial, &#39;Helvetica&#39;" size="+1">x</font><font>y</font>`,
		`<time datetime="2026-11-12T09:00Z">t</time><time datetime="soon">u</time>`:                                        `<time datetime="2026-11-12T09:00Z">t</time><time>u</time>`,
		`<table><colgroup span="2" width="10"><col span="1" width="50%"></colgroup><tr><td>x</td></tr></table>`:            `<table><colgroup span="2" width="10"><col span="1" width="50%"/></colgroup><tbody><tr><td>x</td></tr></tbody></table>`,
		`<p onclick="x" data-x="y" hidden style="color:red;position:fixed">p</p>`:                                          `<p style="color:red">p</p>`,
		`<td style="height:0;overflow:hidden">x`:                                                                           `x`,
		`<table><tr><td style="height:0;overflow:hidden">x</td></tr></table>`:                                              `<table><tbody><tr><td style="height:0;overflow:hidden;overflow:visible !important">x</td></tr></tbody></table>`,
		`<p style="color:red">x<table><tr><td>t</td></tr></table></p>`:                                                     `<div style="margin-top:1em;margin-bottom:1em;color:red">x<table><tbody><tr><td>t</td></tr></tbody></table></div></body>`,
		`<p>x<marquee><div>d</div></marquee></p>`:                                                                          `<div style="margin-top:1em;margin-bottom:1em">x<div>d</div></div>`,
		`<p>a<td>b</td></p>`: `<p>ab</p>`,
		`<a href="https://a.example/" style="color:red;font-weight:bold"><span style="display:block;color:blue">x</span><span style="display:none">y</span></a>`: `font-weight:bold;` + linkStyle,
		`<body bgcolor="#eee" text="black" background="https://e.example/b.png"><p>x</p></body>`:                                                                 `<body bgcolor="#eee" text="black"><p>x</p></body>`,
		`<html lang="fr" dir="rtl"><p>x</p></html>`:                                 `<html dir="rtl" lang="fr" style=`,
		`<html lang="f r" dir="up"><p>x</p></html>`:                                 `<html style=`,
		`<a>no href</a><a href="#top">fragment</a><a href="/relative">relative</a>`: `no hreffragmentrelative`,
		`<a href="https://a.example/"><a href="https://b.example/">nested</a></a>`:  `nested</a>`,
		`<img src="data:image/png,abc"><img src="data:text/plain;base64,eA=="><img src="data:image/png;base64,!!!"><img><img src="ftp://e.example/x">`: `Image not shown</span><span`,
		`<img src="https://e.example/x.png" style="width : 1px"><img src="https://e.example/y.png" width="2px" alt="a">`:                               `<body></body>`,
		`<blink>b</blink><o:p>o</o:p><custom-tag>c</custom-tag><form><fieldset><legend>l</legend><label>n</label></fieldset></form>`:                   `bocln`,
	}
	for in, want := range cases {
		st, out := renderOK(t, input(in))
		if st.Status != contracts.HTMLShown || !strings.Contains(out, want) {
			t.Errorf("%s\n  got %s\n  want %s", in, out[strings.Index(out, "<body")-0:], want)
		}
	}
	if st, out := renderOK(t, input(`<a href="https://a.example/"><span style="display:block">x</span><span style="display:none">y</span><span>z</span></a>`)); st.Links != 1 ||
		!strings.Contains(out, "display:block !important") || !strings.Contains(out, "display:revert !important") || strings.Contains(out, "display:none") {
		t.Errorf("inside a link: %s", out)
	}
	if colour, ok := colour("12345"); ok {
		t.Errorf("%q", colour)
	}
}

// What does not parse back to itself is unreadable: a heading the walk
// would put inside a heading (by unwrapping a <label> between them) is
// read as two siblings when parsed again.
func TestWhatDoesNotParseBackIsUnreadable(t *testing.T) {
	st, out := renderOK(t, input(`<h1><label><h2>x</h2></label></h1>`))
	if st.Status != contracts.HTMLUnreadable || out != "" {
		t.Fatalf("%+v %q", st, out)
	}
	if _, err := Links(context.Background(), input(`<h1><label><h2><a href="https://a.example/">x</a></h2></label></h1>`)); err != nil {
		t.Errorf("Links does not check the round trip: %v", err)
	}
}

type failingWriter struct{}

var errWrite = errors.New("write failed")

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestRenderFailsWithItsWriter(t *testing.T) {
	if _, err := Render(context.Background(), input("<p>x</p>"), failingWriter{}); !errors.Is(err, errWrite) {
		t.Fatal(err)
	}
	// Invalid UTF-8 is replaced, within the limit.
	st, out := renderOK(t, input("<p>a\xffb</p>"))
	if st.Status != contracts.HTMLShown || !strings.Contains(out, "a�b") {
		t.Fatal(out)
	}
	if st, _ := renderOK(t, input(strings.Repeat("\xffa", contracts.MessageHTMLMaxBytes/2))); st.Status != contracts.HTMLTooLarge {
		t.Fatal("invalid UTF-8 that grows past 2 MiB")
	}
	if splitType, _, ok := splitDataURI("data:image/png"); ok || splitType != "" {
		t.Fatal("a data: URI without a comma")
	}
}
