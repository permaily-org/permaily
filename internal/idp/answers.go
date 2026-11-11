package idp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/domain"
)

// object reads the members of one JSON object. encoding/json alone would take
// the last of two members with one name, and names in any case; here a name
// twice is refused and names are matched exactly by the caller.
func object(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := token.(string) // inside an object the decoder gives only names here
		if _, twice := members[name]; twice {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // and nothing after it
		return nil, false
	}
	return members, true
}

// list reads the items of one JSON array, at most limit of them.
func list(raw []byte, limit int) ([]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('[') {
		return nil, false
	}
	var items []json.RawMessage
	for dec.More() {
		var item json.RawMessage
		if len(items) == limit || dec.Decode(&item) != nil {
			return nil, false
		}
		items = append(items, item)
	}
	if _, err := dec.Token(); err != nil { // the closing bracket
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // and nothing after it
		return nil, false
	}
	return items, true
}

// text is a member that is a JSON string. A null would otherwise read as "".
func text(value json.RawMessage) (string, bool) {
	var s string
	if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &s) != nil {
		return "", false
	}
	return s, true
}

// optional is a member that is a JSON string, or is null, or is not there:
// the last two read as "". Anything else is not what a provider writes.
func optional(value json.RawMessage) (string, bool) {
	if len(value) == 0 || string(value) == "null" {
		return "", true
	}
	return text(value)
}

// whole is a member that is a whole number written plainly, as every provider
// writes a time and an identifier: no fraction, no exponent, not negative,
// and short enough to be exact. JSON itself allows no zero in front.
func whole(value json.RawMessage) (int64, bool) {
	if len(value) > 15 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	return n, err == nil && n >= 0
}

// address is an address a provider gave, as accounts are found by it. One
// that is not plainly an address is treated as none: the person can still
// sign in, and confirms an address by mail.
func address(s string) string {
	a, err := domain.NormalizeAddress(s)
	if err != nil {
		return ""
	}
	return a
}

// name is a name a provider gave, when it is one short line that can be
// shown. Anything else is no name: a name is not a reason to refuse a person.
func name(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxName || strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return ""
	}
	return s
}

// visible says whether s is made of ASCII that shows and has no space, and is
// between one and limit bytes: what an identifier or a token is.
func visible(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// tokenAnswer is what a token endpoint gave: one, the other or both.
type tokenAnswer struct{ access, id string }

// parseTokenAnswer reads the answer to exchanging a code (RFC 6749, section 5.1).
func parseTokenAnswer(body []byte) (*tokenAnswer, error) {
	members, ok := object(body)
	if !ok {
		return nil, refuse("token answer is not one JSON object")
	}
	// GitHub says a code is wrong with a success that holds an error.
	if _, failed := members["error"]; failed {
		return nil, unreachable("the token endpoint answered with an error")
	}
	if kind, ok := text(members["token_type"]); !ok || !strings.EqualFold(kind, "bearer") {
		return nil, refuse("token answer of another kind")
	}
	var answer tokenAnswer
	if value, has := members["access_token"]; has {
		// It goes into a header, so it must be what a header can carry.
		if answer.access, ok = text(value); !ok || !visible(answer.access, maxAccessToken) {
			return nil, refuse("malformed access token")
		}
	}
	if value, has := members["id_token"]; has {
		// How long it may be, and everything else of it, is for verify to say.
		if answer.id, ok = text(value); !ok || answer.id == "" {
			return nil, refuse("malformed ID token")
		}
	}
	return &answer, nil
}

// parseGitHubUser reads GitHub's answer to GET /user: the number that stays
// when the person changes their login, and their name.
func parseGitHubUser(body []byte) (*Identity, error) {
	members, ok := object(body)
	if !ok {
		return nil, refuse("user is not one JSON object")
	}
	id, ok := whole(members["id"])
	if !ok || id < 1 {
		return nil, refuse("user without a numeric identifier")
	}
	given, ok := optional(members["name"])
	if !ok {
		return nil, refuse("user with a name that is not text")
	}
	return &Identity{Provider: GitHub, Subject: strconv.FormatInt(id, 10), Name: name(given)}, nil
}

// parseGitHubEmails reads GitHub's answer to GET /user/emails and returns the
// address that is both primary and verified, or none.
func parseGitHubEmails(body []byte) (string, error) {
	items, ok := list(body, githubPage)
	if !ok {
		return "", refuse("addresses are not one JSON array of a page")
	}
	found, primaries := "", 0
	for _, item := range items {
		members, ok := object(item)
		if !ok {
			return "", refuse("an address is not one JSON object")
		}
		email, ok := text(members["email"])
		primary, verified := string(members["primary"]), string(members["verified"])
		if !ok || (primary != "true" && primary != "false") || (verified != "true" && verified != "false") {
			return "", refuse("an address without its three members")
		}
		if primary != "true" {
			continue
		}
		// Two primary addresses would leave the choice to their order.
		if primaries++; primaries > 1 {
			return "", refuse("more than one primary address")
		}
		if verified == "true" {
			found = address(email)
		}
	}
	return found, nil
}

// parseMetaUser reads Meta's answer to GET /me. Meta says nothing of whether
// an address is verified, so it never is (ADR 0017).
func parseMetaUser(body []byte) (*Identity, error) {
	members, ok := object(body)
	if !ok {
		return nil, refuse("user is not one JSON object")
	}
	// A failure comes with its own status; one inside a success is still one.
	if _, failed := members["error"]; failed {
		return nil, unreachable("the user endpoint answered with an error")
	}
	id, ok := text(members["id"])
	if !ok || !digits(id) {
		return nil, refuse("user without a numeric identifier")
	}
	given, ok := optional(members["name"])
	if !ok {
		return nil, refuse("user with a name that is not text")
	}
	email, ok := optional(members["email"])
	if !ok {
		return nil, refuse("user with an address that is not text")
	}
	return &Identity{Provider: Meta, Subject: id, Address: address(email), Name: name(given)}, nil
}

// digits says whether s is a decimal identifier: digits, no leading zero, short.
func digits(s string) bool {
	if s == "" || len(s) > 32 || s[0] == '0' {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
