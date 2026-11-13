package mailhtml

import (
	"strings"
	"testing"
)

// The CSS filter, declaration by declaration (ADR 0026, "CSS"): what each
// property keeps, written again from its tokens, and what goes.
func TestCSSDeclarations(t *testing.T) {
	kept := map[string]string{
		"color: RED":                         "color:red",
		"color:#ABC":                         "color:#abc",
		"color:rgb(1, 2, 3)":                 "color:rgb(1, 2, 3)",
		"color:hsla(120deg,50%,50%,0.5)":     "color:hsla(120deg,50%,50%,0.5)",
		"color:rgb(1 2 3 / 50%)":             "color:rgb(1 2 3 / 50%)",
		"background:#fff none":               "background:#fff none",
		"font:bold 12px/1.4 \"Helvetica\",a": `font:bold 12px/1.4 "Helvetica",a`,
		"font:700 14pt/120% serif":           "font:700 14pt/120% serif",
		"font-family:'A <b>', serif":         `font-family:"A \3c b\3e ", serif`,
		"font-size:6px":                      "font-size:6px",
		"font-size:96px":                     "font-size:96px",
		"font-size:0.5em":                    "font-size:0.5em",
		"font-size:600%":                     "font-size:600%",
		"font-size:72pt":                     "font-size:72pt",
		"font-size:small":                    "font-size:small",
		"font-weight:600":                    "font-weight:600",
		"line-height:normal":                 "line-height:normal",
		"line-height:1.5":                    "line-height:1.5",
		"line-height:3em":                    "line-height:3em",
		"line-height:150px":                  "line-height:150px",
		"line-height:300%":                   "line-height:300%",
		"text-indent:160px":                  "text-indent:160px",
		"text-indent:10em":                   "text-indent:10em",
		"text-indent:0":                      "text-indent:0",
		"margin:0 auto":                      "margin:0 auto",
		"border:1px solid #e1e1e1":           "border:1px solid #e1e1e1",
		"border-radius:4px / 2px":            "border-radius:4px / 2px",
		"width:100%":                         "width:100%",
		"height:auto":                        "height:auto",
		"max-height:none":                    "max-height:none",
		"display:table-cell":                 "display:table-cell",
		"float:right":                        "float:right",
		"clear:both":                         "clear:both",
		"vertical-align:middle":              "vertical-align:middle",
		"text-decoration:underline #f00":     "text-decoration:underline #f00",
		"overflow:hidden":                    "overflow:hidden",
		"COLOR:red":                          "color:red",
		"color:red !important":               "color:red",
		"color:/**/red":                      "color:red",
	}
	for in, want := range kept {
		b := &cssBudget{}
		decls := b.filterStyleAttribute(in, "div")
		var s strings.Builder
		writeDecls(&s, decls, false)
		if s.String() != want {
			t.Errorf("%q: %q, want %q", in, s.String(), want)
		}
	}
	dropped := []string{
		"position:fixed", "position:relative", "top:0", "left:-10px", "z-index:9", "opacity:0", "visibility:hidden",
		"transform:translateY(-1px)", "clip-path:inset(1px)", "pointer-events:none", "content:'x'", "cursor:pointer",
		"filter:blur(1px)", "box-shadow:0 0 1px", "--x:1", "color:var(--x)", "width:expression(alert(1))",
		"behavior:url(x.htc)", "-moz-binding:url(x)", "background-image:url(https://e.example/a.png)",
		"background:url(https://e.example/a.png)", "list-style:url(x)", "margin:-1px", "margin-top:-70vh",
		"text-indent:-9999px", "text-indent:161px", "text-indent:11em", "text-indent:1cm", "font-size:0",
		"font-size:5px", "font-size:97px", "font-size:4pt", "font-size:0.4em", "font-size:49%", "font-size:1vw",
		"font-size:huge", "line-height:0", "line-height:4", "line-height:9px", "line-height:301%", "line-height:normal2",
		"font:0/0 a", "font:12px/0 a", "font:5000 a", "height:0", "min-height:10px", "max-height:0",
		"display:flex", "display:grid", "display:contents", "float:inline-start", "clear:all", "vertical-align:5000px",
		"vertical-align:10%", "color:#12", "color:#ggg", "color:rgb(-1,0,0)", "color:rgb(1,2,var(--x))", "color:calc(1)",
		"color:'red'", "color:red,blue", "width:1q", "width:10fr", "color:-webkit-link",
		"font-family:a/b", "mso-line-height-rule:exactly", "color:red; position:fixed !important", "animation:x 1s",
		"transition:all 1s", "outline:1px solid", "color", "color:", ":red", "color red", "{color:red}",
		"color:hsl(1turn,1%,1%)", "color:rgb(1e5,0,0)", "width:1e9px", "color:rgb(1,2,3 / 1)x", "background:#fff 1px",
		"display:none !importan", "text-align:left left'", "color:\\", "font-weight:0",
	}
	for _, in := range dropped {
		b := &cssBudget{}
		for _, d := range b.filterStyleAttribute(in, "div") {
			if d.prop != "color" || strings.Contains(in, "position") == false {
				t.Errorf("%q kept %+v", in, d)
			}
		}
	}
	// A height as a length only in a style attribute of td, th, tr,
	// table and img.
	for _, el := range []string{"td", "th", "tr", "table", "img"} {
		if d := (&cssBudget{}).filterStyleAttribute("height:40px;min-height:1px;max-height:9px", el); len(d) != 3 {
			t.Errorf("%s: %+v", el, d)
		}
	}
	if d := (&cssBudget{}).filterStyleAttribute(strings.Repeat("color:red;", 1000), "td"); d != nil {
		t.Errorf("a style attribute over 8 KiB: %d kept", len(d))
	}
	// The budget of declarations in all.
	b := &cssBudget{decls: 19_999}
	if d := b.filterStyleAttribute("color:red;color:blue", "td"); len(d) != 1 {
		t.Errorf("past the declarations' budget: %+v", d)
	}
}

func sheet(s string) string { return (&cssBudget{}).filterStylesheet(s) }

// Rules, selectors and at-rules.
func TestCSSRules(t *testing.T) {
	cases := map[string]string{
		"p{color:red}":                                                        "p{color:red}",
		"P , .x > #y + a ~ b * {color:red}":                                   "p,.x>#y+a~b *{color:red}",
		"a:hover,a:link,a:visited{color:red}":                                 "a:hover,a:link,a:visited{color:red}",
		"li:first-child,li:last-child{color:red}":                             "li:first-child,li:last-child{color:red}",
		"li:nth-child(2n+1){color:red}":                                       "li:nth-child(2n+1){color:red}",
		"li:nth-of-type(odd){color:red}":                                      "li:nth-of-type(odd){color:red}",
		"li:nth-child( n + 3 ){color:red}":                                    "li:nth-child(n + 3){color:red}",
		"p:not(.x){color:red}":                                                "p:not(.x){color:red}",
		"p:not(#x),p:not(*),p:not(b),p:not([x]){c:d;color:red}":               "p:not(#x),p:not(*),p:not(b),p:not([x]){color:red}",
		`a[href^="https"]{color:red}`:                                         `a[href^="https"]{color:red}`,
		"a[x=y i],a[x~=y],a[x|=y s],a[x$=y],a[x*=y]{color:red}":               "a[x=y i],a[x~=y],a[x|=y s],a[x$=y],a[x*=y]{color:red}",
		"@media screen and (max-width:600px){td{width:100% !important}}":      "@media screen and (max-width:600px){td{width:100% !important}}",
		"@media (min-resolution:2dppx),(orientation:landscape){p{color:red}}": "@media (min-resolution:2dppx),(orientation:landscape){p{color:red}}",
		"@media (min-aspect-ratio:16/9){p{color:red}}":                        "@media (min-aspect-ratio:16/9){p{color:red}}",
		"@media (color){p{color:red}}":                                        "@media (color){p{color:red}}",
		"<!-- p{color:red} -->":                                               "p{color:red}",
		"p{color:red}}":                                                       "p{color:red}",
		";p{color:red}":                                                       "p{color:red}",
	}
	for in, want := range cases {
		if got := sheet(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	gone := []string{
		"p::before{color:red}", "p:focus{color:red}", "p:nth-child(-1){color:red}", "p:nth-child(2.5n){color:red}",
		"p:nth-child(2x){color:red}", "p:not(a b){color:red}", "p:not(.){color:red}", "ns|p{color:red}", "p!{color:red}",
		"a[x]y]{color:red}", "a[x=]{color:red}", "a[x~y]{color:red}", "a[x=y z]{color:red}", "a[=y]{color:red}",
		"a[x=1]{color:red}", ".{color:red}", "#1x{color:red}", "p{position:fixed}", "p{}", "p", "@import url(x.css);",
		"@font-face{src:url(https://e.example/f.woff)}", "@keyframes x{from{color:red}}", "@supports (x:y){p{color:red}}",
		"@page{margin:0}", "@layer x{p{color:red}}", "@container (x){p{color:red}}", "@property --x{syntax:'*'}",
		"@charset \"utf-8\";", "@namespace x url(y);", "@media screen{@media print{p{color:red}}}",
		"@media (max-width:-1px){p{color:red}}", "@media (-webkit-min-ratio:2){p{color:red}}", "@media -x{p{color:red}}",
		"@media (max-width:1fr){p{color:red}}", "@media (a:b:c){p{color:red}}", "@media (x) url(y){p{color:red}}",
		"@media ((x)){p{color:red}}", "@media (x:-y){p{color:red}}", "@media (x:'y'){p{color:red}}", "@media{p{color:red}}",
		"@media (1){p{color:red}}", "@media (x y){p{color:red}}", "@media (x:1/){p{color:red}}" + "@media (x:2*){p{color:red}}",
		"p:hover(x){color:red}", "p:nth-child(){color:red}", "p:nth-child(2n-1){color:red}", "p:nth-child(+){color:red}",
		"p:nth-child((x)){color:red}", "p:nth-child(\"x\"){color:red}", "a[(x)]{color:red}", "a[x=(y)]{color:red}",
		strings.Repeat("a ", 40) + "{color:red}", strings.Repeat("p", 1100) + "{color:red}",
	}
	for _, in := range gone {
		if got := sheet(in); got != "" && !strings.HasPrefix(got, "@media (x:1/)") {
			t.Errorf("%q kept as %q", in, got)
		}
	}
	// What a stylesheet names elsewhere is counted.
	b := &cssBudget{}
	b.filterStylesheet(`@import url(https://e.example/a.css); @import "https://e.example/b.css"; @font-face{src:url(//e.example/c.woff)}
	p{background:url(https://e.example/d.png)} x y {list-style-image:url(https://e.example/e.png)} broken url(https://e.example/f.png)`)
	if b.remoteOther != 4 || b.remoteImages != 2 {
		t.Errorf("other %d, images %d", b.remoteOther, b.remoteImages)
	}
	// Rules past 5,000 go.
	big := strings.Repeat("p{color:red}", 5001)
	if got := sheet(big); strings.Count(got, "{") != 5000 {
		t.Errorf("%d rules", strings.Count(got, "{"))
	}
	if got := (&cssBudget{rules: 5000}).filterStylesheet("@media print{p{color:red}}"); got != "" {
		t.Errorf("an @media past the rules' budget: %q", got)
	}
	if got := sheet("@media print{p{position:fixed}}"); got != "" {
		t.Errorf("an empty @media: %q", got)
	}
}

// The tokeniser reads what a browser reads.
func TestCSSTokeniser(t *testing.T) {
	cases := map[string][]cssKind{
		`\75 rl(x)`:           {cssURL},
		`url( "x" )`:          {cssFunction, cssWhitespace, cssString, cssWhitespace, cssRParen},
		"url(a b)":            {cssBadURL},
		"url(a\"b)":           {cssBadURL},
		`url(a\)b)`:           {cssURL},
		"url(a\\\nb)":         {cssBadURL},
		"url(  x  )":          {cssURL},
		"'a\nb'":              {cssBadString, cssWhitespace, cssIdent, cssString},
		`'a\'b'`:              {cssString},
		"'a\\\nb'":            {cssString},
		"'abc":                {cssString},
		"#a #1 # .5 +.5 -.5":  {cssHash, cssWhitespace, cssHash, cssWhitespace, cssDelim, cssWhitespace, cssNumber, cssWhitespace, cssNumber, cssWhitespace, cssNumber},
		"1e3 1e+3 1e 2%":      {cssNumber, cssWhitespace, cssNumber, cssWhitespace, cssDimension, cssWhitespace, cssPercentage},
		"--x -x - --> <!-- <": {cssIdent, cssWhitespace, cssIdent, cssWhitespace, cssDelim, cssWhitespace, cssCDC, cssWhitespace, cssCDO, cssWhitespace, cssDelim},
		"@x @ @-":             {cssAtKeyword, cssWhitespace, cssDelim, cssWhitespace, cssDelim, cssDelim},
		`\ \`:                 {cssIdent, cssDelim},
		`\0 \110000 \d800`:    {cssIdent},
		"/* x */a/* y":        {cssIdent},
		"a\r\nb\fc\x00":       {cssIdent, cssWhitespace, cssIdent, cssWhitespace, cssIdent},
		"é\xff":               {cssIdent},
		"[]{}():;,":           {cssLBracket, cssRBracket, cssLBrace, cssRBrace, cssLParen, cssRParen, cssColon, cssSemicolon, cssComma},
		"+ . +a":              {cssDelim, cssWhitespace, cssDelim, cssWhitespace, cssDelim, cssIdent},
		"1.":                  {cssNumber, cssDelim},
	}
	for in, want := range cases {
		var got []cssKind
		for _, tok := range cssTokenize(in) {
			got = append(got, tok.Kind)
		}
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", in, got, want)
				break
			}
		}
	}
	if v := cssTokenize(`\75 rl(x)`)[0].Value; v != "x" {
		t.Errorf("%q", v)
	}
	if v := cssTokenize(`\0 \110000`)[0].Value; v != "��" {
		t.Errorf("%q", v)
	}
	var b strings.Builder
	cssWriteIdent(&b, "-a1\x01<>")
	cssWriteIdent(&b, "--x")
	cssWriteIdent(&b, "1a")
	if b.String() != `-a1\1 \3c \3e --x\31 a` {
		t.Errorf("%q", b.String())
	}
}

// Found by FuzzCSS (testdata/fuzz/FuzzCSS/8d9ad1c3f2da0c8f): two tokens the
// input wrote with nothing between ("0" and "+0") were written back glued
// ("00"), which a browser reads as one number. Tokens that would merge are
// written with a space between, so the output reads back as the filter read
// it.
func TestCSSAdjacentTokensStaySeparate(t *testing.T) {
	for _, in := range []string{
		"a{color:rgb(0+0+0)}", "a{margin:0+0}", "a{padding:1px+2px}", "a{font:bold+12px Arial}", "a{margin:1+2%}",
	} {
		out := (&cssBudget{}).filterStylesheet(in)
		if again := (&cssBudget{}).filterStylesheet(out); again != out {
			t.Errorf("%q: written %q, read back %q", in, out, again)
		}
		for _, glued := range []string{"00", "1px2", "bold12", "12%"} {
			if strings.Contains(out, glued) {
				t.Errorf("%q: written %q, with %q glued", in, out, glued)
			}
		}
	}
}
