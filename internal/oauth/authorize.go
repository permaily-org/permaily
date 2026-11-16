package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// Authorize reads an authorization request's parameters, each at most once,
// and checks them in ADR 0030's "The authorization request" order once the
// client is known (doc, fetched and checked by Document). proven says the
// redirect URI was: only then may a refusal be sent back to it, with the
// state p carries (empty when it was refused) and iss (Redirect). Unknown
// parameters are ignored, the pushed request's own ones (query, mail_since,
// ends_in_days) included: they are never taken from an address.
//
// A request with request_uri carries only client_id besides it, and p holds
// only those two: the pushed request it names is the service's to find.
func Authorize(q url.Values, doc *ClientDocument, c Config) (p *AuthorizeParams, proven bool, err error) {
	if once(q, "client_id") != doc.ClientID {
		return nil, false, refuseClient(contracts.ClientIDInvalid)
	}
	if _, has := q["request_uri"]; has {
		for name := range q {
			if name != "client_id" && name != "request_uri" {
				return nil, false, refuse(ErrInvalidRequest)
			}
		}
		uri := once(q, "request_uri")
		if !pushedURI(uri) {
			return nil, false, refuse(ErrInvalidRequest)
		}
		return &AuthorizeParams{ClientID: doc.ClientID, RequestURI: uri}, false, nil
	}
	redirect := once(q, "redirect_uri")
	if !RedirectAllowed(doc.RedirectURIs, redirect) {
		return nil, false, refuseClient(contracts.RedirectURIInvalid)
	}
	p = &AuthorizeParams{ClientID: doc.ClientID, RedirectURI: redirect}
	if state := once(q, "state"); len(state) <= StateMaxBytes {
		p.State = state
	}
	return p, true, p.read(q, c)
}

// read checks what follows the redirect URI, in order, for Authorize and
// Pushed alike.
func (p *AuthorizeParams) read(q url.Values, c Config) error {
	for name, values := range q {
		if len(values) > 1 {
			return refuse(ErrInvalidRequest)
		}
		if name == "response_mode" && values[0] != "query" {
			return refuse(ErrInvalidRequest)
		}
	}
	if p.ResponseType = q.Get("response_type"); p.ResponseType != "code" {
		return refuse(ErrUnsupportedResponse)
	}
	p.CodeChallenge, p.CodeChallengeMethod = q.Get("code_challenge"), q.Get("code_challenge_method")
	if !pkceValue(p.CodeChallenge) || p.CodeChallengeMethod != "S256" {
		return refuse(ErrInvalidRequest)
	}
	if p.Resource = q.Get("resource"); p.Resource == "" || (p.Resource != c.APIResource && p.Resource != c.MCPResource) {
		return refuse(ErrInvalidTarget)
	}
	scope, present := q["scope"]
	var err error
	if p.Scope, err = Scopes(strings.Join(scope, ""), present); err != nil {
		return err
	}
	if len(q.Get("state")) > StateMaxBytes {
		return refuse(ErrInvalidRequest)
	}
	return nil
}

// once is a parameter given once, or "" (given twice it is not given:
// Authorize refuses it).
func once(q url.Values, name string) string {
	if v := q[name]; len(v) == 1 {
		return v[0]
	}
	return ""
}

// pushedURI says s is a request_uri this server hands out.
func pushedURI(s string) bool {
	id, ok := strings.CutPrefix(s, RequestURIPrefix+PrefixPushed)
	return ok && secretValue(id)
}

// secretValue says s is what NewSecret writes after a prefix.
func secretValue(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == SecretBytes && !strings.ContainsAny(s, "\r\n")
}

// Pushed reads a pushed request's form (RFC 9126) for a client whose
// authentication, when its document asks for one, the caller has checked:
// the redirect URI first, then the size of the form as encoded and of the
// formula, then Authorize's checks in its order, then query (a formula
// query.Parse takes with any well-formed mailbox), mail_since ("new" or an
// RFC 3339 time; that it is in the past is the caller's to check, with its
// clock) and ends_in_days (1 to ConnectionMaxDays). Every refusal is the
// endpoint's JSON answer, never a redirect; a formula refused is
// invalid_request with nothing of why, which would echo it.
func Pushed(form url.Values, doc *ClientDocument, c Config) (*AuthorizeParams, error) {
	for _, name := range []string{"client_id", "redirect_uri"} {
		if len(form[name]) > 1 {
			return nil, refuse(ErrInvalidRequest)
		}
	}
	if form.Get("client_id") != doc.ClientID {
		return nil, refuseClient(contracts.ClientIDInvalid)
	}
	p := &AuthorizeParams{ClientID: doc.ClientID, RedirectURI: form.Get("redirect_uri")}
	if !RedirectAllowed(doc.RedirectURIs, p.RedirectURI) {
		return nil, refuseClient(contracts.RedirectURIInvalid)
	}
	if _, has := form["request_uri"]; has || len(form.Encode()) > FormMaxBytes || len(form.Get("query")) > contracts.MaxFormulaBytes {
		return nil, refuse(ErrInvalidRequest)
	}
	if err := p.read(form, c); err != nil {
		return nil, err
	}
	p.State = form.Get("state")
	if _, has := form["query"]; has {
		p.Query = []byte(form.Get("query"))
		if _, err := query.Parse(p.Query, nil); err != nil {
			return nil, refuse(ErrInvalidRequest)
		}
	}
	if since, has := form["mail_since"]; has {
		if _, err := time.Parse(time.RFC3339, since[0]); since[0] != "new" && err != nil {
			return nil, refuse(ErrInvalidRequest)
		}
		p.Since = since[0]
	}
	if days, has := form["ends_in_days"]; has {
		n, err := strconv.Atoi(days[0])
		if err != nil || strconv.Itoa(n) != days[0] || n < 1 || n > ConnectionMaxDays {
			return nil, refuse(ErrInvalidRequest)
		}
		p.EndsInDays = n
	}
	return p, nil
}

// pkceValue says s is 43 to 128 characters of [A-Za-z0-9-._~] (RFC 7636,
// section 4.1): a verifier, or a challenge.
func pkceValue(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !unreserved(c) {
			return false
		}
	}
	return true
}

// unreserved is RFC 3986's unreserved: [A-Za-z0-9-._~].
func unreserved(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0
}

// VerifyPKCE checks a verifier against an S256 challenge (RFC 7636, section
// 4.6), in constant time: the verifier 43 to 128 unreserved characters, its
// SHA-256 in unpadded base64url the challenge.
func VerifyPKCE(challenge, verifier string) bool {
	if !pkceValue(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// Scopes parses a scope parameter (RFC 6749, section 3.3): absent, the
// DefaultScopes; present, scope tokens each separated by one space, each
// one of the four and none twice, or invalid_scope. ScopeSender is added
// when not asked (it is part of every connection), and the answer is in
// ScopeParts' order.
func Scopes(s string, present bool) ([]string, error) {
	if !present {
		return slices.Clone(DefaultScopes), nil
	}
	asked := map[string]bool{ScopeSender: true}
	seen := map[string]bool{}
	for _, token := range strings.Split(s, " ") {
		if seen[token] || !known(token) {
			return nil, refuse(ErrInvalidScope)
		}
		seen[token], asked[token] = true, true
	}
	var out []string
	for _, sp := range ScopeParts {
		if asked[sp.Scope] {
			out = append(out, sp.Scope)
		}
	}
	return out, nil
}

func known(scope string) bool {
	for _, sp := range ScopeParts {
		if sp.Scope == scope {
			return true
		}
	}
	return false
}

// Parts maps scopes to parts, in ScopeParts' order, PartSender always first
// whether or not it is among them; an unknown scope gives nothing.
func Parts(scopes []string) []contracts.Part {
	out := []contracts.Part{contracts.PartSender}
	for _, sp := range ScopeParts[1:] {
		if slices.Contains(scopes, sp.Scope) {
			out = append(out, sp.Part)
		}
	}
	return out
}

// Redirect is a redirect URI with params and iss (RFC 9207) added to its
// query, which it keeps as it was (RFC 6749, section 3.1.2). redirectURI is
// one RedirectAllowed accepted; "" when it does not parse.
func Redirect(redirectURI string, params url.Values, issuer string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	added := url.Values{}
	for name, values := range params {
		added[name] = slices.Clone(values)
	}
	added.Set("iss", issuer)
	if u.RawQuery != "" {
		u.RawQuery += "&" + added.Encode()
	} else {
		u.RawQuery = added.Encode()
	}
	u.ForceQuery = false
	return u.String()
}
