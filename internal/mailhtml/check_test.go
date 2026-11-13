package mailhtml

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/permaily-org/permaily/internal/contracts"
)

// checkOutput is ADR 0026's test 18, independent of the walk's own tables
// where it can be: the output, parsed with x/net/html (not searched as
// text), holds no element, attribute, URL scheme, CSS property, value,
// function or at-rule outside the allowlist; it parses back to itself; it
// is within every limit; and every <a> carries the forced style. It returns
// the first thing that does not hold.
func checkOutput(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(out))
	if err != nil {
		return err
	}
	var again bytes.Buffer
	if err := html.Render(&again, doc); err != nil || !bytes.Equal(again.Bytes(), out) {
		return fmt.Errorf("does not parse back to itself")
	}
	elements, maxDepth := 0, 0
	var problem error
	var walk func(n *html.Node, depth int, inHead bool)
	walk = func(n *html.Node, depth int, inHead bool) {
		for c := n.FirstChild; c != nil && problem == nil; c = c.NextSibling {
			switch c.Type {
			case html.CommentNode:
				problem = fmt.Errorf("a comment")
			case html.TextNode:
				if c.Parent.Data == "style" {
					problem = checkCSS(c.Data, false)
				}
			case html.ElementNode:
				elements++
				maxDepth = max(maxDepth, depth)
				problem = checkElement(c, inHead)
				walk(c, depth+1, inHead || c.Data == "head")
			}
		}
	}
	walk(doc, 0, false)
	if problem != nil {
		return problem
	}
	if elements > contracts.MessageHTMLMaxElements+10 || maxDepth > contracts.MessageHTMLMaxDepth+3 {
		return fmt.Errorf("over a limit: %d elements, %d deep", elements, maxDepth)
	}
	return nil
}

var (
	allowedOutput = map[string]bool{}
	reLinkPath    = regexp.MustCompile(`^/inbox/[^/]+/[^/]+/links/(0|[1-9][0-9]{0,2})\.[A-Za-z0-9_-]{16}$`)
	reDataImage   = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/]+=*$`)
)

func init() {
	for _, e := range strings.Fields("a abbr address article aside b bdi bdo big blockquote br caption center cite code col colgroup dd del details dfn div dl dt em figcaption figure font footer h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p pre q rp rt ruby s samp section small span strike strong sub summary sup table tbody td tfoot th thead time tr tt u ul var wbr") {
		allowedOutput[e] = true
	}
}

// allowedAttributes is the allowlist of ADR 0026, "Attributes", written out
// again here rather than taken from the walk.
func allowedAttribute(element, key string) bool {
	switch key {
	case "dir", "lang", "title", "align", "valign", "class", "id", "style":
		return true
	}
	byElement := map[string]string{
		"table": "width height border cellpadding cellspacing bgcolor colspan rowspan nowrap scope headers",
		"col":   "span width", "colgroup": "span width", "ol": "start type reversed", "li": "value",
		"font": "color face size", "time": "datetime", "img": "src alt width height border hspace vspace",
		"a": "href target rel",
	}
	for _, cell := range []string{"tr", "td", "th"} {
		byElement[cell] = byElement["table"]
	}
	for _, k := range strings.Fields(byElement[element]) {
		if k == key {
			return true
		}
	}
	return false
}

func checkElement(n *html.Node, inHead bool) error {
	if n.Namespace != "" {
		return fmt.Errorf("<%s> in namespace %s", n.Data, n.Namespace)
	}
	switch n.Data {
	case "html":
		for _, a := range n.Attr {
			if a.Key != "lang" && a.Key != "dir" && a.Key != "translate" && (a.Key != "style" || a.Val != canvasStyle) {
				return fmt.Errorf("html %s", a.Key)
			}
		}
		return nil
	case "head":
		return nil
	case "body":
		for _, a := range n.Attr {
			if a.Key != "bgcolor" && a.Key != "text" {
				return fmt.Errorf("body %s", a.Key)
			}
		}
		return nil
	case "meta":
		if !inHead {
			return fmt.Errorf("a meta outside the head")
		}
		for _, a := range n.Attr {
			ok := a.Key == "charset" && a.Val == "utf-8" || a.Key == "name" && (a.Val == "referrer" || a.Val == "google") ||
				a.Key == "content" && (a.Val == "no-referrer" || a.Val == "notranslate")
			if !ok {
				return fmt.Errorf("meta %s=%q", a.Key, a.Val)
			}
		}
		return nil
	case "style":
		if !inHead || len(n.Attr) > 0 {
			return fmt.Errorf("a style element outside the head")
		}
		return nil
	}
	if !allowedOutput[n.Data] {
		return fmt.Errorf("<%s>", n.Data)
	}
	for _, a := range n.Attr {
		if a.Namespace != "" || !allowedAttribute(n.Data, a.Key) {
			return fmt.Errorf("<%s %s>", n.Data, a.Key)
		}
		switch a.Key {
		case "href":
			if !reLinkPath.MatchString(a.Val) {
				return fmt.Errorf("href %q", a.Val)
			}
		case "src":
			if !reDataImage.MatchString(a.Val) {
				return fmt.Errorf("src %.40q", a.Val)
			}
		case "target":
			if a.Val != "_blank" {
				return fmt.Errorf("target %q", a.Val)
			}
		case "rel":
			if a.Val != "noopener noreferrer" {
				return fmt.Errorf("rel %q", a.Val)
			}
		case "style":
			if err := checkCSS(a.Val, true); err != nil {
				return err
			}
		}
	}
	if n.Data == "a" {
		style, _ := attr(n, "style")
		href, _ := attr(n, "href")
		if !strings.HasSuffix(style, linkStyle) || href == "" {
			return fmt.Errorf("a link without its forced style: %q", style)
		}
		if t, _ := attr(n, "target"); t != "_blank" {
			return fmt.Errorf("a link without target")
		}
	}
	return nil
}

// ownProperties are the properties the sanitiser writes itself.
var ownProperties = set("display", "max-width", "overflow", "color", "background-color", "text-decoration",
	"font-size", "line-height", "letter-spacing", "word-spacing", "text-indent", "vertical-align", "width",
	"height", "min-width", "min-height", "max-height", "margin", "padding", "border", "box-sizing", "background",
	"font", "overflow-wrap", "margin-top", "margin-bottom", "color-scheme")

// forbiddenProperties are never in the output, whatever else is.
var forbiddenProperties = set("position", "top", "right", "bottom", "left", "inset", "transform", "translate",
	"rotate", "scale", "z-index", "opacity", "visibility", "clip", "clip-path", "mask", "pointer-events",
	"mix-blend-mode", "filter", "backdrop-filter", "content", "cursor", "outline", "box-shadow", "text-shadow",
	"order", "behavior", "-moz-binding", "animation", "transition", "background-image", "list-style-image",
	"border-image")

// checkCSS reads CSS of the output with the tokeniser and refuses a url,
// a function other than the colour functions (and the max() of the link's
// style, the :nth and :not of selectors), an at-rule other than @media, a
// property outside the allowlist, a custom property, or a "<".
func checkCSS(text string, attribute bool) error {
	if strings.ContainsAny(text, "<") {
		return fmt.Errorf("a < in CSS")
	}
	toks, err := cssTokenize(context.Background(), text, cssMaxTokens)
	if err != nil {
		return err
	}
	expectProperty := true
	var blocks []bool // for each open block: does it hold declarations (not rules, as @media's)?
	media := false
	for i, t := range toks {
		switch t.Kind {
		case cssURL, cssBadURL, cssBadString:
			return fmt.Errorf("a url or a bad token in CSS: %q", text)
		case cssFunction:
			switch strings.ToLower(t.Value) {
			case "rgb", "rgba", "hsl", "hsla", "max", "nth-child", "nth-of-type", "not":
			default:
				return fmt.Errorf("function %s()", t.Value)
			}
		case cssAtKeyword:
			if strings.ToLower(t.Value) != "media" {
				return fmt.Errorf("@%s", t.Value)
			}
			media = true
		case cssLBrace:
			blocks = append(blocks, !media)
			media = false
			expectProperty = true
		case cssRBrace:
			if len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
			expectProperty = true
		case cssSemicolon:
			expectProperty = true
		case cssIdent:
			inDeclarations := attribute || len(blocks) > 0 && blocks[len(blocks)-1]
			if expectProperty && inDeclarations && i+1 < len(toks) && toks[i+1].Kind == cssColon {
				p := strings.ToLower(t.Value)
				if forbiddenProperties[p] || strings.HasPrefix(p, "--") || strings.HasPrefix(p, "-") ||
					(cssProperties[p] == 0 && !ownProperties[p]) {
					return fmt.Errorf("property %s", p)
				}
				expectProperty = false
			}
			if strings.EqualFold(t.Value, "expression") || strings.EqualFold(t.Value, "var") {
				return fmt.Errorf("%s in CSS", t.Value)
			}
		case cssDimension, cssNumber, cssPercentage:
			if t.Neg {
				return fmt.Errorf("a negative number in CSS: %q", text)
			}
		}
	}
	return nil
}
