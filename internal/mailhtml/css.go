package mailhtml

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// A tokeniser of CSS Syntax Level 3 (https://www.w3.org/TR/css-syntax-3/,
// "Tokenization"), so that escapes, comments and strings are read as a
// browser reads them: "\75 rl(" is the function url(. Nothing of the input's
// text is ever written back: the filter writes CSS again from the tokens it
// keeps (cssWrite), escaping what could end a <style> element.

type cssKind uint8

const (
	cssEOF cssKind = iota
	cssIdent
	cssFunction
	cssAtKeyword
	cssHash
	cssString
	cssBadString
	cssURL
	cssBadURL
	cssDelim
	cssNumber
	cssPercentage
	cssDimension
	cssWhitespace
	cssCDO
	cssCDC
	cssColon
	cssSemicolon
	cssComma
	cssLBracket
	cssRBracket
	cssLParen
	cssRParen
	cssLBrace
	cssRBrace
)

// cssToken is one token. Value is the name of an ident, function, at-keyword
// or hash, the value of a string or URL, or the delimiter; Num and Unit are
// a number's; Signed says the number was written with a sign; ID says a hash
// is a valid identifier.
type cssToken struct {
	Kind   cssKind
	Value  string
	Num    float64
	Unit   string
	Signed bool
	Neg    bool
	ID     bool
}

// cssMaxTokens bounds the tokens one stylesheet or declaration list makes:
// more than its bytes allow is impossible, this is a second bound.
const cssMaxTokens = 1 << 20

// cssTokenize reads s into tokens. It never fails: what is not valid becomes
// bad-string, bad-url or a delimiter, as the specification says.
func cssTokenize(s string) []cssToken {
	t := &cssTokenizer{s: cssPreprocess(s)}
	var out []cssToken
	for len(out) < cssMaxTokens {
		tok := t.next()
		if tok.Kind == cssEOF {
			break
		}
		out = append(out, tok)
	}
	return out
}

// cssPreprocess replaces CR, CRLF and FF with LF, and NUL and invalid UTF-8
// with U+FFFD ("Preprocessing the input stream").
func cssPreprocess(s string) string {
	if strings.ContainsAny(s, "\r\f\x00") || !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
		s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n", "\x00", "�").Replace(s)
	}
	return s
}

type cssTokenizer struct {
	s string
	i int
}

func (t *cssTokenizer) peek(k int) rune {
	j := t.i
	for n := 0; n < k; n++ {
		if j >= len(t.s) {
			return -1
		}
		_, size := utf8.DecodeRuneInString(t.s[j:])
		j += size
	}
	if j >= len(t.s) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(t.s[j:])
	return r
}

func (t *cssTokenizer) advance() rune {
	if t.i >= len(t.s) {
		return -1
	}
	r, size := utf8.DecodeRuneInString(t.s[t.i:])
	t.i += size
	return r
}

func isNameStart(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r >= 0x80
}

func isNameCode(r rune) bool {
	return isNameStart(r) || r >= '0' && r <= '9' || r == '-'
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isCSSSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

func validEscape(a, b rune) bool { return a == '\\' && b != '\n' && b != -1 }

// startsIdent says whether the three code points start an ident sequence.
func startsIdent(a, b, c rune) bool {
	switch {
	case a == '-':
		return isNameStart(b) || b == '-' || validEscape(b, c)
	case isNameStart(a):
		return true
	case a == '\\':
		return validEscape(a, b)
	}
	return false
}

func startsNumber(a, b, c rune) bool {
	switch {
	case a == '+' || a == '-':
		return isDigit(b) || b == '.' && isDigit(c)
	case a == '.':
		return isDigit(b)
	}
	return isDigit(a)
}

func (t *cssTokenizer) next() cssToken {
	// Comments.
	for strings.HasPrefix(t.s[t.i:], "/*") {
		end := strings.Index(t.s[t.i+2:], "*/")
		if end < 0 {
			t.i = len(t.s)
			return cssToken{Kind: cssEOF}
		}
		t.i += end + 4
	}
	c := t.advance()
	switch {
	case c == -1:
		return cssToken{Kind: cssEOF}
	case isCSSSpace(c):
		for isCSSSpace(t.peek(0)) {
			t.advance()
		}
		return cssToken{Kind: cssWhitespace}
	case c == '"' || c == '\'':
		return t.string(c)
	case c == '#':
		if isNameCode(t.peek(0)) || validEscape(t.peek(0), t.peek(1)) {
			id := startsIdent(t.peek(0), t.peek(1), t.peek(2))
			return cssToken{Kind: cssHash, Value: t.name(), ID: id}
		}
		return cssToken{Kind: cssDelim, Value: "#"}
	case c == '(':
		return cssToken{Kind: cssLParen}
	case c == ')':
		return cssToken{Kind: cssRParen}
	case c == '+' || c == '.':
		if startsNumber(c, t.peek(0), t.peek(1)) {
			t.i -= 1
			return t.numeric()
		}
		return cssToken{Kind: cssDelim, Value: string(c)}
	case c == ',':
		return cssToken{Kind: cssComma}
	case c == '-':
		if startsNumber(c, t.peek(0), t.peek(1)) {
			t.i--
			return t.numeric()
		}
		if t.peek(0) == '-' && t.peek(1) == '>' {
			t.advance()
			t.advance()
			return cssToken{Kind: cssCDC}
		}
		if startsIdent(c, t.peek(0), t.peek(1)) {
			t.i--
			return t.identLike()
		}
		return cssToken{Kind: cssDelim, Value: "-"}
	case c == ':':
		return cssToken{Kind: cssColon}
	case c == ';':
		return cssToken{Kind: cssSemicolon}
	case c == '<':
		if strings.HasPrefix(t.s[t.i:], "!--") {
			t.i += 3
			return cssToken{Kind: cssCDO}
		}
		return cssToken{Kind: cssDelim, Value: "<"}
	case c == '@':
		if startsIdent(t.peek(0), t.peek(1), t.peek(2)) {
			return cssToken{Kind: cssAtKeyword, Value: t.name()}
		}
		return cssToken{Kind: cssDelim, Value: "@"}
	case c == '[':
		return cssToken{Kind: cssLBracket}
	case c == '\\':
		if validEscape(c, t.peek(0)) {
			t.i--
			return t.identLike()
		}
		return cssToken{Kind: cssDelim, Value: "\\"}
	case c == ']':
		return cssToken{Kind: cssRBracket}
	case c == '{':
		return cssToken{Kind: cssLBrace}
	case c == '}':
		return cssToken{Kind: cssRBrace}
	case isDigit(c):
		t.i--
		return t.numeric()
	case isNameStart(c):
		t.i -= utf8.RuneLen(c)
		return t.identLike()
	}
	return cssToken{Kind: cssDelim, Value: string(c)}
}

// escape reads an escape after its backslash ("Consume an escaped code
// point").
func (t *cssTokenizer) escape() rune {
	c := t.advance()
	switch {
	case c == -1:
		return utf8.RuneError
	case isHexRune(c):
		v := int(hexVal(c))
		for n := 0; n < 5 && isHexRune(t.peek(0)); n++ {
			v = v*16 + int(hexVal(t.advance()))
		}
		if isCSSSpace(t.peek(0)) {
			t.advance()
		}
		if v == 0 || v > utf8.MaxRune || v >= 0xD800 && v <= 0xDFFF {
			return utf8.RuneError
		}
		return rune(v)
	}
	return c
}

func isHexRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func hexVal(r rune) rune {
	switch {
	case r >= '0' && r <= '9':
		return r - '0'
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10
	}
	return r - 'A' + 10
}

// name reads an ident sequence.
func (t *cssTokenizer) name() string {
	var b strings.Builder
	for {
		c := t.peek(0)
		switch {
		case isNameCode(c):
			b.WriteRune(t.advance())
		case validEscape(c, t.peek(1)):
			t.advance()
			b.WriteRune(t.escape())
		default:
			return b.String()
		}
	}
}

func (t *cssTokenizer) identLike() cssToken {
	name := t.name()
	if t.peek(0) != '(' {
		return cssToken{Kind: cssIdent, Value: name}
	}
	t.advance()
	if strings.EqualFold(name, "url") {
		// Whitespace, then a quote: a function whose argument is a string.
		for isCSSSpace(t.peek(0)) && isCSSSpace(t.peek(1)) {
			t.advance()
		}
		a, b := t.peek(0), t.peek(1)
		if a == '"' || a == '\'' || isCSSSpace(a) && (b == '"' || b == '\'') {
			return cssToken{Kind: cssFunction, Value: name}
		}
		return t.url()
	}
	return cssToken{Kind: cssFunction, Value: name}
}

func (t *cssTokenizer) url() cssToken {
	var b strings.Builder
	for isCSSSpace(t.peek(0)) {
		t.advance()
	}
	for {
		c := t.advance()
		switch {
		case c == ')' || c == -1:
			return cssToken{Kind: cssURL, Value: b.String()}
		case isCSSSpace(c):
			for isCSSSpace(t.peek(0)) {
				t.advance()
			}
			if p := t.peek(0); p == ')' || p == -1 {
				t.advance()
				return cssToken{Kind: cssURL, Value: b.String()}
			}
			t.badURL()
			return cssToken{Kind: cssBadURL}
		case c == '"' || c == '\'' || c == '(' || c < 0x20 && c != '\t' || c == 0x7f:
			t.badURL()
			return cssToken{Kind: cssBadURL}
		case c == '\\':
			if validEscape(c, t.peek(0)) {
				b.WriteRune(t.escape())
				continue
			}
			t.badURL()
			return cssToken{Kind: cssBadURL}
		default:
			b.WriteRune(c)
		}
	}
}

func (t *cssTokenizer) badURL() {
	for {
		c := t.advance()
		switch {
		case c == ')' || c == -1:
			return
		case validEscape(c, t.peek(0)):
			t.escape()
		}
	}
}

func (t *cssTokenizer) string(quote rune) cssToken {
	var b strings.Builder
	for {
		c := t.advance()
		switch {
		case c == quote || c == -1:
			return cssToken{Kind: cssString, Value: b.String()}
		case c == '\n':
			t.i--
			return cssToken{Kind: cssBadString}
		case c == '\\':
			switch p := t.peek(0); {
			case p == -1:
			case p == '\n':
				t.advance()
			default:
				b.WriteRune(t.escape())
			}
		default:
			b.WriteRune(c)
		}
	}
}

func (t *cssTokenizer) numeric() cssToken {
	start := t.i
	signed, neg := false, false
	if c := t.peek(0); c == '+' || c == '-' {
		signed, neg = true, c == '-'
		t.advance()
	}
	for isDigit(t.peek(0)) {
		t.advance()
	}
	if t.peek(0) == '.' && isDigit(t.peek(1)) {
		t.advance()
		for isDigit(t.peek(0)) {
			t.advance()
		}
	}
	if c := t.peek(0); c == 'e' || c == 'E' {
		n1 := t.peek(1)
		if isDigit(n1) || (n1 == '+' || n1 == '-') && isDigit(t.peek(2)) {
			t.advance()
			if n1 == '+' || n1 == '-' {
				t.advance()
			}
			for isDigit(t.peek(0)) {
				t.advance()
			}
		}
	}
	v, err := strconv.ParseFloat(t.s[start:t.i], 64)
	if err != nil {
		v = 1e308 // out of every bound: refused by the filter
	}
	tok := cssToken{Num: v, Signed: signed, Neg: neg || v < 0}
	switch {
	case startsIdent(t.peek(0), t.peek(1), t.peek(2)):
		tok.Kind, tok.Unit = cssDimension, t.name()
	case t.peek(0) == '%':
		t.advance()
		tok.Kind = cssPercentage
	default:
		tok.Kind = cssNumber
	}
	return tok
}

// cssWriteIdent writes an ident so that a tokeniser reads it back as the
// same ident, and so that it holds no "<", quote, backslash or control
// character as itself: anything but [A-Za-z0-9_-] and non-ASCII letters is
// written as a hexadecimal escape followed by a space.
func cssWriteIdent(b *strings.Builder, s string) {
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-' && i > 0 ||
			r >= '0' && r <= '9' && i > 0 || r >= 0xA0 && r != utf8.RuneError && r != 0x2028 && r != 0x2029
		if r == '-' && i == 0 && len(s) > 1 && (s[1] == '-' || isNameStart(rune(s[1]))) {
			ok = true
		}
		if ok {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('\\')
		b.WriteString(strconv.FormatInt(int64(r), 16))
		b.WriteByte(' ')
	}
}

// cssWriteString writes a string in double quotes, escaping what would end
// it, a line or the <style> element.
func cssWriteString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029:
			b.WriteByte('\\')
			b.WriteString(strconv.FormatInt(int64(r), 16))
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// cssWriteNumber writes a non-negative number in plain decimal.
func cssWriteNumber(b *strings.Builder, v float64) {
	b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
}
