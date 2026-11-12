package mail

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// message is a fixture of packages/mail: a message the service sends, with
// sample values, as internal/services/accounts writes it (and checks it is
// current).
type message struct {
	Name    string  `json:"name"`
	To      string  `json:"to"`
	Subject string  `json:"subject"`
	Blocks  []Block `json:"blocks"`
}

func messages(t *testing.T) []message {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "packages", "mail", "fixtures", "*.json"))
	if err != nil || len(files) < 30 {
		t.Fatalf("%d fixtures (%v): the checks of the HTML need every message", len(files), err)
	}
	var out []message
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: a fixture of this repository
		if err != nil {
			t.Fatal(err)
		}
		var m message
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, m)
	}
	return out
}

// element is a start tag of the HTML.
type element struct {
	name  string
	attrs map[string]string
}

// parsed is what a mail client would read of a message's HTML.
type parsed struct {
	elements []element
	// words is the visible text of the body, in order.
	words    []string
	styles   []string
	comments int
}

// parse reads HTML loosely, as browsers do, with the standard library alone.
func parse(t *testing.T, html string) parsed {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(html))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	var p parsed
	var open []string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the HTML does not parse: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			e := element{name: strings.ToLower(tok.Name.Local), attrs: map[string]string{}}
			for _, a := range tok.Attr {
				name := strings.ToLower(a.Name.Local)
				if a.Name.Space != "" {
					name = strings.ToLower(a.Name.Space) + ":" + name
				}
				e.attrs[name] = a.Value
			}
			p.elements = append(p.elements, e)
			open = append(open, e.name)
		case xml.EndElement:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		case xml.CharData:
			switch {
			case slices.Contains(open, "style"):
				p.styles = append(p.styles, string(tok))
			case slices.Contains(open, "body"):
				p.words = append(p.words, words(string(tok))...)
			}
		case xml.Comment:
			p.comments++
		}
	}
	return p
}

var word = regexp.MustCompile(`[\p{L}\p{N}]+`)

// words are the words of a text, without its punctuation and spaces.
func words(s string) []string { return word.FindAllString(s, -1) }

var urlInText = regexp.MustCompile(`[a-z][a-z0-9+.-]*://\S+`)

func TestTextOfEveryKind(t *testing.T) {
	got := Text([]Block{
		Lead("What it is:"),
		Code("123456"),
		Paragraph("A paragraph."),
		Action("https://app.example.org/x"),
		Facts(Fact{Label: "When", Value: "now", Machine: true}, Fact{Label: "How", Value: "so"}),
		Note("Small print."),
	})
	want := "What it is:\n\n    123456\n\nA paragraph.\n\n    https://app.example.org/x\n\n    When: now\n    How: so\n\nSmall print.\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestEveryMessageFetchesNothing: no image, font, stylesheet, script, frame,
// form or anything else a mail client would fetch or run.
func TestEveryMessageFetchesNothing(t *testing.T) {
	forbidden := []string{"img", "picture", "source", "video", "audio", "track", "script", "noscript", "iframe", "frame", "frameset", "object", "embed", "applet", "form", "input", "button", "select", "textarea", "link", "base", "svg", "math", "image", "use", "portal"}
	fetching := []string{"src", "srcset", "background", "poster", "action", "formaction", "data", "lowsrc", "dynsrc", "xlink:href", "ping", "manifest", "longdesc", "codebase", "cite"}
	remote := regexp.MustCompile(`(?i)url\s*\(|@import|expression\s*\(|behavior\s*:|-moz-binding|image-set|image\s*\(|://`)
	for _, m := range messages(t) {
		p := parse(t, HTML(m.Subject, m.Blocks))
		for _, e := range p.elements {
			if slices.Contains(forbidden, e.name) {
				t.Errorf("%s: <%s>", m.Name, e.name)
			}
			for name, value := range e.attrs {
				if slices.Contains(fetching, name) || strings.HasPrefix(name, "on") {
					t.Errorf("%s: <%s %s=%q>", m.Name, e.name, name, value)
				}
				if name == "style" && remote.MatchString(value) {
					t.Errorf("%s: <%s style=%q>", m.Name, e.name, value)
				}
				if name == "http-equiv" && !strings.EqualFold(value, "Content-Type") {
					t.Errorf("%s: <meta http-equiv=%q>", m.Name, value)
				}
				if name != "href" && strings.Contains(value, "://") {
					t.Errorf("%s: <%s %s=%q> names an address", m.Name, e.name, name, value)
				}
			}
		}
		for _, s := range p.styles {
			if remote.MatchString(s) {
				t.Errorf("%s: the stylesheet fetches something: %s", m.Name, s)
			}
		}
	}
}

// TestEveryLinkIsOneTheTextHas: the HTML links to what the plain text shows,
// and to nothing else; a message whose text has no link has none.
func TestEveryLinkIsOneTheTextHas(t *testing.T) {
	links := 0
	for _, m := range messages(t) {
		var hrefs []string
		for _, e := range parse(t, HTML(m.Subject, m.Blocks)).elements {
			if href, ok := e.attrs["href"]; ok {
				if e.name != "a" {
					t.Errorf("%s: href on <%s>", m.Name, e.name)
				}
				hrefs = append(hrefs, href)
			}
		}
		inText := urlInText.FindAllString(Text(m.Blocks), -1)
		slices.Sort(hrefs)
		slices.Sort(inText)
		if !slices.Equal(hrefs, inText) {
			t.Errorf("%s: links %q, the text has %q", m.Name, hrefs, inText)
		}
		links += len(hrefs)
	}
	if links == 0 {
		t.Error("no message has a link: the check proves nothing")
	}
}

// TestTheHTMLSaysWhatTheTextSays: the visible words of the HTML are the
// text's, in the same order, with the wordmark before them and nothing else.
func TestTheHTMLSaysWhatTheTextSays(t *testing.T) {
	for _, m := range messages(t) {
		got := parse(t, HTML(m.Subject, m.Blocks)).words
		want := append([]string{"Permaily"}, words(Text(m.Blocks))...)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the HTML says\n%q\nthe text says\n%q", m.Name, got, want)
		}
	}
}

// TestNothingIdentifyingIsInTheMarkup: no identifier, code, token or address
// in an attribute, a comment or hidden text; the only value in an attribute
// is the link of an action, which the text shows.
func TestNothingIdentifyingIsInTheMarkup(t *testing.T) {
	for _, m := range messages(t) {
		p := parse(t, HTML(m.Subject, m.Blocks))
		if p.comments != 0 {
			t.Errorf("%s: %d comments", m.Name, p.comments)
		}
		var values []string
		for _, b := range m.Blocks {
			if b.Kind == KindCode {
				values = append(values, b.Text)
			}
			if b.Kind == KindAction {
				values = append(values, strings.SplitN(b.URL, "#", 2)...)
			}
		}
		values = append(values, m.To)
		for _, e := range p.elements {
			if _, ok := e.attrs["id"]; ok {
				t.Errorf("%s: <%s id>", m.Name, e.name)
			}
			if e.attrs["hidden"] != "" || strings.Contains(strings.ReplaceAll(e.attrs["style"], " ", ""), "display:none") {
				t.Errorf("%s: hidden <%s>", m.Name, e.name)
			}
			for name, value := range e.attrs {
				for _, v := range values {
					if name != "href" && v != "" && strings.Contains(value, v) {
						t.Errorf("%s: <%s %s> holds %q", m.Name, e.name, name, v)
					}
				}
			}
		}
	}
}

// TestEverythingInsertedIsEscaped puts hostile values in every place a value
// goes: the subject, each kind of block, a link, a fact.
func TestEverythingInsertedIsEscaped(t *testing.T) {
	hostile := `<script>alert(1)</script>"'><img src=x onerror=alert(1)>`
	for name, link := range map[string]string{
		"javascript": "javascript:alert(1)",
		"data":       "data:text/html,<script>alert(1)</script>",
		"quotes":     `https://app.example.org/"><script>alert(1)</script>`,
		"vbscript":   "vbscript:msgbox(1)",
	} {
		html := HTML(hostile, []Block{
			Lead(hostile), Paragraph(hostile), Note(hostile), Code(hostile), Action(link),
			Facts(Fact{Label: hostile, Value: hostile, Machine: true}, Fact{Label: hostile, Value: hostile}),
		})
		if html == "" {
			t.Fatalf("%s: no HTML", name)
		}
		for _, e := range parse(t, html).elements {
			if e.name == "script" || e.name == "img" {
				t.Errorf("%s: <%s> got through", name, e.name)
			}
			for a, v := range e.attrs {
				if strings.HasPrefix(a, "on") {
					t.Errorf("%s: %s=%q got through", name, a, v)
				}
				if a == "href" && !strings.HasPrefix(v, "https://") && v != "#ZgotmplZ" {
					t.Errorf("%s: href=%q got through", name, v)
				}
			}
		}
		if strings.Contains(html, "<script") || strings.Contains(html, "<img") {
			t.Errorf("%s: a tag got through unescaped", name)
		}
	}
}

// TestEveryMessageIsSmall: Gmail cuts a message near 102 KB.
func TestEveryMessageIsSmall(t *testing.T) {
	for _, m := range messages(t) {
		if n := len(HTML(m.Subject, m.Blocks)); n > 40<<10 {
			t.Errorf("%s: %d bytes of HTML", m.Name, n)
		}
	}
}

// TestEveryMessageFollowsTheSystemTheme: the HTML says it has both themes,
// and has the dark values for the clients that read them.
func TestEveryMessageFollowsTheSystemTheme(t *testing.T) {
	for _, m := range messages(t) {
		html := HTML(m.Subject, m.Blocks)
		for _, want := range []string{`<meta name="color-scheme" content="light dark"`, "@media (prefers-color-scheme:dark)", `<html dir="ltr" lang="en">`} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: no %s", m.Name, want)
			}
		}
	}
}

// TestTheCodeStaysOnOneLine: a code is one piece of text that does not wrap.
func TestTheCodeStaysOnOneLine(t *testing.T) {
	html := HTML("s", []Block{Code("428913")})
	i := strings.Index(html, "428913")
	start := strings.LastIndex(html[:i], "<p")
	if start < 0 || !strings.Contains(html[start:i], "white-space:nowrap") || strings.Count(html, "428913") != 1 {
		t.Errorf("the code is not one line that does not wrap:\n%s", html)
	}
}

func TestNoHTMLForAKindItDoesNotKnow(t *testing.T) {
	if got := HTML("s", []Block{Paragraph("p"), {Kind: "image", URL: "https://example.org/x.png"}}); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestNoHTMLWhenTheTemplateFails(t *testing.T) {
	defer func(e func(io.Writer, string, any) error) { execute = e }(execute)
	execute = func(io.Writer, string, any) error { return errors.New("broken") }
	if got := HTML("s", []Block{Paragraph("p")}); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestTheSameMessageIsTheSameHTML(t *testing.T) {
	blocks := []Block{Lead("a"), Code("1"), Action("https://app.example.org/")}
	first := HTML("s", blocks)
	if again := HTML("s", blocks); again != first {
		t.Error("two renderings differ")
	}
}
