package inbox

import (
	"bytes"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MessageText is the text of a message as the text view and search show it
// (ADR 0026, "What a message returns"): its first text/plain part, decoded;
// with none, the text taken out of its first text/html part; with neither,
// nothing. At most max bytes, truncated when there was more. from is
// contracts.TextFromPlain, TextFromHTML or TextFromNone.
func MessageText(raw []byte, max int) (text, from string, truncated bool) {
	head, _ := boundHeader(raw)
	w := walker{}
	if e, _ := readEntity(head); e != nil {
		w.walk(e, 0)
	}
	return w.text(max)
}

// htmlText takes the text out of HTML: its text, a line break for each block
// element, nothing of script, style, head or title, entities decoded, and
// each link's target in brackets after its text. It is text, never shown as
// HTML; what it cannot read it leaves out.
func htmlText(doc string) string {
	var out strings.Builder
	var href, linkText string
	inLink := false
	text := func(s string) {
		s = collapse(html.UnescapeString(s))
		out.WriteString(s)
		if inLink {
			linkText += s
		}
	}
	for doc != "" {
		lt := strings.IndexByte(doc, '<')
		if lt < 0 {
			text(doc)
			break
		}
		text(doc[:lt])
		doc = doc[lt:]
		if strings.HasPrefix(doc, "<!--") {
			end := strings.Index(doc[4:], "-->")
			if end < 0 {
				break
			}
			doc = doc[4+end+3:]
			continue
		}
		gt := tagEnd(doc)
		if gt < 0 {
			break
		}
		tag := doc[1:gt]
		doc = doc[gt+1:]
		closing := strings.HasPrefix(tag, "/")
		name, attrs := tagName(strings.TrimPrefix(tag, "/"))
		switch {
		case name == "":
			// A declaration (<!DOCTYPE>), a processing instruction or a
			// stray "<": nothing of it is text.
		case !closing && skipped[name]:
			end := strings.Index(strings.ToLower(doc), "</"+name)
			if end < 0 {
				doc = ""
				continue
			}
			doc = doc[end:]
		case name == "a" && !closing:
			href, linkText, inLink = attribute(attrs, "href"), "", true
		case name == "a" && closing:
			if inLink && href != "" && strings.TrimSpace(linkText) != href {
				out.WriteString(" [" + href + "]")
			}
			inLink, href = false, ""
		case block[name]:
			out.WriteByte('\n')
		}
	}
	return tidy(out.String())
}

// skipped are the elements whose content is not text.
var skipped = map[string]bool{"script": true, "style": true, "head": true, "title": true, "template": true, "noscript": true}

// block are the elements that start a line of their own.
var block = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true, "dd": true, "div": true,
	"dl": true, "dt": true, "figcaption": true, "figure": true, "footer": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true, "main": true, "nav": true, "ol": true,
	"p": true, "pre": true, "section": true, "table": true, "td": true, "th": true, "tr": true, "ul": true,
}

// tagEnd is where the tag that doc starts with ends, outside quoted values.
func tagEnd(doc string) int {
	var quote byte
	for i := 1; i < len(doc); i++ {
		switch c := doc[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

// tagName splits a tag's inside into its name, lower case, and the rest; the
// name is "" when the tag is not an element.
func tagName(tag string) (string, string) {
	i := 0
	for i < len(tag) && (isLetter(tag[i]) || (i > 0 && (tag[i] >= '0' && tag[i] <= '9' || tag[i] == '-' || tag[i] == ':'))) {
		i++
	}
	return strings.ToLower(tag[:i]), tag[i:]
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// attribute is the value of the attribute name in attrs, decoded, or "".
func attribute(attrs, name string) string {
	lower := strings.ToLower(attrs)
	for i := 0; ; {
		j := strings.Index(lower[i:], name)
		if j < 0 {
			return ""
		}
		j += i
		before := j == 0 || strings.ContainsRune(" \t\r\n/\"'", rune(lower[j-1]))
		rest := strings.TrimLeft(attrs[j+len(name):], " \t\r\n")
		i = j + len(name)
		if !before || !strings.HasPrefix(rest, "=") {
			continue
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
		var v string
		switch {
		case rest == "":
			return ""
		case rest[0] == '"' || rest[0] == '\'':
			end := strings.IndexByte(rest[1:], rest[0])
			if end < 0 {
				return ""
			}
			v = rest[1 : 1+end]
		default:
			end := strings.IndexAny(rest, " \t\r\n>")
			if end < 0 {
				end = len(rest)
			}
			v = rest[:end]
		}
		return strings.TrimSpace(html.UnescapeString(v))
	}
}

// collapse turns every run of white space into one space, as HTML shows it.
func collapse(s string) string {
	if strings.TrimSpace(s) == "" {
		if s == "" {
			return ""
		}
		return " "
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	lead, trail := unicode.IsSpace(first), unicode.IsSpace(last)
	s = strings.Join(strings.Fields(s), " ")
	if lead {
		s = " " + s
	}
	if trail {
		s += " "
	}
	return s
}

// tidy trims each line and keeps at most one empty line in a row.
func tidy(s string) string {
	var out bytes.Buffer
	blank := 0
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, " ", " "))
		if line == "" {
			blank++
			if blank > 1 || out.Len() == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}
