package query

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Problem is a refused formula: contracts.ErrInvalidFormula (errors.Is)
// with where and why (ADR 0027, "Errors"). It holds a path into the formula
// as sent and a fixed reason, never a value of the formula, so an error
// page or a log of one shows nothing a vendor or a person wrote.
type Problem struct{ contracts.FormulaProblem }

func (p *Problem) Error() string {
	return contracts.ErrInvalidFormula.Error() + " (" + string(p.Reason) + " at \"" + p.At + "\")"
}

// Unwrap makes errors.Is(err, contracts.ErrInvalidFormula) hold.
func (p *Problem) Unwrap() error { return contracts.ErrInvalidFormula }

func problem(at string, reason contracts.FormulaReason) *Problem {
	return &Problem{contracts.FormulaProblem{At: at, Reason: reason}}
}

// Parse reads a formula as a vendor or the web app sends it, strictly; checks
// it against the limits and the account's mailboxes (nil: any well-formed
// identifier); and returns its canonical form. A refusal is a *Problem, which
// is contracts.ErrInvalidFormula (errors.Is) and carries a
// contracts.FormulaProblem. A formula that is "never" is refused
// (FormulaNeverMatches).
//
// It answers the first problem in the order of the formula as sent: what
// the JSON says as it is read, and each condition once its object closes,
// its variable, then its operator, then its value. So the answer does not
// depend on the order the checks run in.
func Parse(data []byte, mailboxes []string) (*Canonical, error) {
	groups, p := read(data, mailboxes)
	if p != nil {
		return nil, p
	}
	c := canonical(groups, nil)
	if c.Never {
		return nil, problem("", contracts.FormulaNeverMatches)
	}
	return c, nil
}

// read walks the JSON's tokens with encoding/json's Decoder (no other
// parser in the image) and checks what it reads as it reads it.
func read(data []byte, mailboxes []string) ([][]cond, *Problem) {
	if len(data) > contracts.MaxFormulaBytes {
		return nil, problem("", contracts.FormulaTooLarge)
	}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) || !wellFormedText(data) {
		return nil, problem("", contracts.FormulaNotJSON)
	}
	r := &reader{dec: json.NewDecoder(bytes.NewReader(data)), mailboxes: mailboxes}
	r.dec.UseNumber()
	groups, p := r.formula()
	if p != nil {
		return nil, p
	}
	if rest := data[r.dec.InputOffset():]; len(bytes.Trim(rest, " \t\r\n")) != 0 {
		return nil, problem("", contracts.FormulaNotJSON)
	}
	return groups, nil
}

// wellFormedText reports valid UTF-8 whose \u escapes inside strings encode
// no lone surrogate: encoding/json would replace either with U+FFFD, and a
// formula is read as it was sent or not at all.
func wellFormedText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	in := false
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case c == '"':
			in = !in
		case c == '\\' && in:
			if i+1 < len(data) && data[i+1] == 'u' {
				u, ok := hex4(data, i+2)
				switch {
				case !ok:
					return true // not JSON: the decoder says so
				case u >= 0xDC00 && u <= 0xDFFF:
					return false
				case u >= 0xD800 && u <= 0xDBFF:
					lo, ok := hex4(data, i+8)
					if !ok || i+7 >= len(data) || data[i+6] != '\\' || data[i+7] != 'u' || lo < 0xDC00 || lo > 0xDFFF {
						return false
					}
					i += 6
				}
				i += 5
				continue
			}
			i++
		}
	}
	return true
}

func hex4(data []byte, at int) (int, bool) {
	if at+4 > len(data) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(data[at:at+4]), 16, 16)
	return int(n), err == nil
}

// reader is the token walk over one formula.
type reader struct {
	dec       *json.Decoder
	mailboxes []string
	limits    limits
}

// token is the next token; a syntax error is not_json.
func (r *reader) token() (json.Token, *Problem) {
	t, err := r.dec.Token()
	if err != nil {
		return nil, problem("", contracts.FormulaNotJSON)
	}
	return t, nil
}

// open reads the start of an object or an array at path at: d is '{' or
// '['. Anything else, null included, is wrong_type.
func (r *reader) open(at string, d json.Delim) *Problem {
	t, p := r.token()
	if p != nil {
		return p
	}
	if t != d {
		return problem(at, contracts.FormulaWrongType)
	}
	return nil
}

// members reads an object's members one by one: each key once (unknown
// and repeated keys refused), and its value through member. at is the
// object's path; a member's path is at.key (just key at the top).
func (r *reader) members(at string, known []string, member func(key, path string) *Problem) *Problem {
	seen := map[string]bool{}
	for r.dec.More() {
		t, p := r.token()
		if p != nil {
			return p
		}
		key, _ := t.(string) // the decoder gives an object only string keys
		path := key
		if at != "" {
			path = at + "." + key
		}
		if !slices.Contains(known, key) {
			return problem(at, contracts.FormulaUnknownField) // the key is the sender's: it is not echoed
		}
		if seen[key] {
			return problem(path, contracts.FormulaDuplicateField)
		}
		seen[key] = true
		if p := member(key, path); p != nil {
			return p
		}
	}
	_, p := r.token() // '}'
	return p
}

// formula reads {"v": 1, "all": [...]}.
func (r *reader) formula() ([][]cond, *Problem) {
	if t, p := r.token(); p != nil || t != json.Delim('{') {
		return nil, problem("", contracts.FormulaNotJSON)
	}
	var groups [][]cond
	sawV, sawAll := false, false
	p := r.members("", []string{"v", "all"}, func(key, path string) *Problem {
		if key == "v" {
			sawV = true
			t, p := r.token()
			if p != nil {
				return p
			}
			n, ok := t.(json.Number)
			if !ok {
				return problem(path, contracts.FormulaWrongType)
			}
			if n.String() != strconv.Itoa(contracts.FormulaVersion) {
				return problem(path, contracts.FormulaUnsupportedVersion)
			}
			return nil
		}
		sawAll = true
		var p *Problem
		groups, p = r.groups(path)
		return p
	})
	switch {
	case p != nil:
		return nil, p
	case !sawV:
		return nil, problem("v", contracts.FormulaUnsupportedVersion)
	case !sawAll:
		return nil, problem("all", contracts.FormulaWrongType)
	}
	return groups, nil
}

// groups reads "all": an array of {"any": [...]}.
func (r *reader) groups(at string) ([][]cond, *Problem) {
	if p := r.open(at, '['); p != nil {
		return nil, p
	}
	groups := [][]cond{}
	for i := 0; r.dec.More(); i++ {
		gat := at + "[" + strconv.Itoa(i) + "]"
		if p := r.limits.group(gat); p != nil {
			return nil, p
		}
		if p := r.open(gat, '{'); p != nil {
			return nil, p
		}
		var g []cond
		sawAny := false
		p := r.members(gat, []string{"any"}, func(_, path string) *Problem {
			sawAny = true
			var p *Problem
			g, p = r.conditions(path)
			return p
		})
		if p != nil {
			return nil, p
		}
		if !sawAny {
			return nil, problem(gat+".any", contracts.FormulaWrongType)
		}
		groups = append(groups, g)
	}
	_, p := r.token() // ']'
	return groups, p
}

// conditions reads "any": an array of conditions, at least one.
func (r *reader) conditions(at string) ([]cond, *Problem) {
	if p := r.open(at, '['); p != nil {
		return nil, p
	}
	var g []cond
	for i := 0; r.dec.More(); i++ {
		cat := at + "[" + strconv.Itoa(i) + "]"
		if p := r.limits.condition(cat); p != nil {
			return nil, p
		}
		c, p := r.condition(cat)
		if p != nil {
			return nil, p
		}
		g = append(g, c)
	}
	if _, p := r.token(); p != nil { // ']'
		return nil, p
	}
	if len(g) == 0 {
		return nil, problem(at, contracts.FormulaEmptyGroup)
	}
	return g, nil
}

// condition reads {"var", "op", "value", "not"} and checks it once its
// object closes. A group inside a group ("any" in a condition) is
// wrong_type.
func (r *reader) condition(at string) (cond, *Problem) {
	if p := r.open(at, '{'); p != nil {
		return cond{}, p
	}
	var c contracts.Condition
	sawValue := false
	p := r.members(at, []string{"var", "op", "value", "not", "any"}, func(key, path string) *Problem {
		if key == "any" {
			return problem(path, contracts.FormulaWrongType)
		}
		t, p := r.token()
		if p != nil {
			return p
		}
		if key == "not" {
			b, ok := t.(bool)
			if !ok {
				return problem(path, contracts.FormulaWrongType)
			}
			c.Not = b
			return nil
		}
		s, ok := t.(string)
		if !ok {
			return problem(path, contracts.FormulaWrongType)
		}
		switch key {
		case "var":
			c.Var = contracts.Variable(s)
		case "op":
			c.Op = contracts.Operator(s)
		default:
			c.Value, sawValue = s, true
		}
		return nil
	})
	if p != nil {
		return cond{}, p
	}
	// A missing var or op is "" and refused as unknown below.
	out, where, reason := checkCondition(c, sawValue, r.mailboxes)
	if reason != "" {
		return cond{}, problem(at+where, reason)
	}
	if p := r.limits.model(at, out); p != nil {
		return cond{}, p
	}
	return out, nil
}
