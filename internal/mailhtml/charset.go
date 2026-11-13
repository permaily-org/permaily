package mailhtml

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"

	"github.com/permaily-org/permaily/internal/contracts"
)

// DecodeCharset makes an HTML part UTF-8, deciding its charset in the order
// of ADR 0026 ("The sanitiser", "Input"), written here rather than taken
// from charset.DetermineEncoding (which reads 1,024 bytes only, puts a byte
// order mark before the header, and falls back to Windows-1252):
//
//  1. the MIME header's charset, when it is a label of the WHATWG Encoding
//     Standard;
//  2. a byte order mark (UTF-8, UTF-16LE, UTF-16BE), removed;
//  3. <meta charset> or <meta http-equiv=content-type> in the first 1,024
//     bytes (the HTML standard's prescan);
//  4. UTF-8, when the whole part is valid UTF-8;
//  5. Windows-1252.
//
// A label, in the header or in a <meta>, that names the "replacement"
// encoding or no WHATWG encoding (UTF-7 among them) makes the HTML
// unreadable. The size limit applies before and after. status
// is "" when the result can be rendered, else contracts.HTMLTooLarge or
// contracts.HTMLUnreadable.
func DecodeCharset(part []byte, mimeCharset string) (out []byte, status string) {
	if len(part) > contracts.MessageHTMLMaxBytes {
		return nil, contracts.HTMLTooLarge
	}
	enc, rest, status := chooseEncoding(part, mimeCharset)
	if status != "" {
		return nil, status
	}
	var decoded []byte
	if enc == nil {
		decoded = rest
	} else {
		d, err := enc.NewDecoder().Bytes(rest)
		if err != nil {
			return nil, contracts.HTMLUnreadable
		}
		decoded = d
	}
	decoded = bytes.ToValidUTF8(decoded, []byte("�"))
	if len(decoded) > contracts.MessageHTMLMaxBytes {
		return nil, contracts.HTMLTooLarge
	}
	return decoded, ""
}

// chooseEncoding is the encoding of part (nil for UTF-8 as it is) and the
// bytes to decode with it.
func chooseEncoding(part []byte, mimeCharset string) (encoding.Encoding, []byte, string) {
	if label := strings.TrimSpace(mimeCharset); label != "" {
		enc, err := htmlindex.Get(label)
		if err != nil || enc == encoding.Replacement {
			return nil, nil, contracts.HTMLUnreadable
		}
		return utf8OrNil(enc), stripBOM(part, enc), ""
	}
	switch {
	case bytes.HasPrefix(part, []byte{0xEF, 0xBB, 0xBF}):
		return nil, part[3:], ""
	case bytes.HasPrefix(part, []byte{0xFF, 0xFE}):
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), part[2:], ""
	case bytes.HasPrefix(part, []byte{0xFE, 0xFF}):
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), part[2:], ""
	}
	if label := prescan(part[:min(len(part), 1024)]); label != "" {
		enc, err := htmlindex.Get(label)
		if err != nil || enc == encoding.Replacement {
			return nil, nil, contracts.HTMLUnreadable // as a header's label: UTF-7, or a name of nothing
		}
		if name, _ := htmlindex.Name(enc); name == "utf-16le" || name == "utf-16be" {
			return nil, part, "" // the prescan's rule: UTF-16 named in the document is UTF-8
		}
		return utf8OrNil(enc), part, ""
	}
	if utf8.Valid(part) {
		return nil, part, ""
	}
	enc, _ := htmlindex.Get("windows-1252")
	return enc, part, ""
}

// utf8OrNil is nil for UTF-8 (decoded as it is, invalid sequences replaced).
func utf8OrNil(enc encoding.Encoding) encoding.Encoding {
	if name, _ := htmlindex.Name(enc); name == "utf-8" {
		return nil
	}
	return enc
}

// stripBOM removes a byte order mark of the header's own encoding: the
// header wins over a mark of another one, which is then decoded as text.
func stripBOM(part []byte, enc encoding.Encoding) []byte {
	name, _ := htmlindex.Name(enc)
	switch {
	case name == "utf-8" && bytes.HasPrefix(part, []byte{0xEF, 0xBB, 0xBF}):
		return part[3:]
	case name == "utf-16le" && bytes.HasPrefix(part, []byte{0xFF, 0xFE}):
		return part[2:]
	case name == "utf-16be" && bytes.HasPrefix(part, []byte{0xFE, 0xFF}):
		return part[2:]
	}
	return part
}

// prescan is the charset a <meta> in head names: <meta charset=…>, or
// <meta http-equiv="content-type" content="…; charset=…">. Only what the
// tokenizer reads in the bytes given counts.
func prescan(head []byte) string {
	z := html.NewTokenizer(bytes.NewReader(head))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "meta" {
				continue
			}
			var charset, content string
			httpEquiv := false
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch string(k) {
				case "charset":
					if charset == "" {
						charset = string(v)
					}
				case "http-equiv":
					httpEquiv = strings.EqualFold(strings.TrimSpace(string(v)), "content-type")
				case "content":
					content = string(v)
				}
			}
			if charset = strings.TrimSpace(charset); charset != "" {
				return charset
			}
			if httpEquiv {
				if c := charsetInContent(content); c != "" {
					return c
				}
			}
		}
	}
}

// charsetInContent is the charset a content attribute names ("text/html;
// charset=utf-8"), as the HTML standard's "extracting a character encoding
// from a meta element" reads it.
func charsetInContent(s string) string {
	lower := strings.ToLower(s)
	i := strings.Index(lower, "charset")
	for i >= 0 {
		rest := strings.TrimLeft(s[i+len("charset"):], " \t\n\r\f")
		if strings.HasPrefix(rest, "=") {
			rest = strings.TrimLeft(rest[1:], " \t\n\r\f")
			if rest == "" {
				return ""
			}
			if q := rest[0]; q == '"' || q == '\'' {
				if end := strings.IndexByte(rest[1:], q); end >= 0 {
					return rest[1 : 1+end]
				}
				return ""
			}
			end := strings.IndexAny(rest, " \t\n\r\f;")
			if end < 0 {
				end = len(rest)
			}
			return rest[:end]
		}
		next := strings.Index(lower[i+len("charset"):], "charset")
		if next < 0 {
			return ""
		}
		i += len("charset") + next
	}
	return ""
}
