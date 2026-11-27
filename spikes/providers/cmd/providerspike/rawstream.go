package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// streamRaw reads a Gmail messages.get?format=raw response — one JSON
// object — and streams its "raw" member, base64url, decoded into sink,
// without holding the encoded or decoded message in memory. The other
// members are returned as raw JSON, each at most maxOther bytes. This is the
// spike's version of the streaming decoder spike item 3 asks about.
func streamRaw(body io.Reader, sink io.Writer, maxOther int) (others map[string]json.RawMessage, decoded int64, err error) {
	r := bufio.NewReaderSize(body, 64<<10)
	others = map[string]json.RawMessage{}
	if err := expect(r, '{'); err != nil {
		return nil, 0, err
	}
	for first := true; ; first = false {
		c, err := next(r)
		if err != nil {
			return nil, 0, err
		}
		if c == '}' {
			return others, decoded, nil
		}
		if !first {
			if c != ',' {
				return nil, 0, fmt.Errorf("expected ',' got %q", c)
			}
			if c, err = next(r); err != nil {
				return nil, 0, err
			}
		}
		if c != '"' {
			return nil, 0, fmt.Errorf("expected a member name, got %q", c)
		}
		key, err := readString(r, 256)
		if err != nil {
			return nil, 0, err
		}
		if err := expect(r, ':'); err != nil {
			return nil, 0, err
		}
		c, err = next(r)
		if err != nil {
			return nil, 0, err
		}
		if key == "raw" {
			if c != '"' {
				return nil, 0, errors.New("raw is not a string")
			}
			n, err := io.Copy(sink, base64.NewDecoder(base64.RawURLEncoding, &b64urlString{r: r}))
			if err != nil {
				return nil, 0, fmt.Errorf("raw: %w", err)
			}
			decoded = n
			continue
		}
		if err := r.UnreadByte(); err != nil {
			return nil, 0, err
		}
		v, err := readValue(r, maxOther)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", key, err)
		}
		others[key] = v
	}
}

func next(r *bufio.Reader) (byte, error) {
	for {
		c, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c, nil
	}
}

func expect(r *bufio.Reader, want byte) error {
	c, err := next(r)
	if err != nil {
		return err
	}
	if c != want {
		return fmt.Errorf("expected %q, got %q", want, c)
	}
	return nil
}

// readString reads the rest of a JSON string whose opening quote was read.
func readString(r *bufio.Reader, limit int) (string, error) {
	buf := []byte{'"'}
	for {
		c, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		buf = append(buf, c)
		if len(buf) > limit {
			return "", errors.New("string too long")
		}
		if c == '\\' {
			e, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			buf = append(buf, e)
			continue
		}
		if c == '"' {
			var s string
			err := json.Unmarshal(buf, &s)
			return s, err
		}
	}
}

// readValue reads one JSON value (bounded), tracking strings and nesting.
func readValue(r *bufio.Reader, limit int) (json.RawMessage, error) {
	var buf []byte
	depth, inString, escaped := 0, false, false
	for {
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if !inString && depth == 0 && len(buf) > 0 && (c == ',' || c == '}') {
			if err := r.UnreadByte(); err != nil {
				return nil, err
			}
			break
		}
		if !inString && (c == ' ' || c == '\t' || c == '\r' || c == '\n') && depth == 0 {
			if len(buf) == 0 {
				continue
			}
		}
		buf = append(buf, c)
		if len(buf) > limit {
			return nil, errors.New("member too large")
		}
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
			if !inString && depth == 0 {
				return trimValue(buf)
			}
		case !inString && (c == '{' || c == '['):
			depth++
		case !inString && (c == '}' || c == ']'):
			depth--
			if depth == 0 {
				return trimValue(buf)
			}
		}
	}
	return trimValue(buf)
}

func trimValue(b []byte) (json.RawMessage, error) {
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	if !json.Valid(b) {
		return nil, errors.New("invalid JSON value")
	}
	return json.RawMessage(b), nil
}

// b64urlString yields the characters of a JSON string holding base64url up to
// its closing quote, dropping '=' padding. Base64url never needs a JSON
// escape, so a backslash is an error.
type b64urlString struct {
	r    *bufio.Reader
	done bool
}

func (s *b64urlString) Read(p []byte) (int, error) {
	if s.done {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		c, err := s.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, io.ErrUnexpectedEOF
			}
			return n, err
		}
		switch c {
		case '"':
			s.done = true
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		case '=':
			continue
		case '\\':
			return n, errors.New("escape inside base64url")
		}
		p[n] = c
		n++
	}
	return n, nil
}
