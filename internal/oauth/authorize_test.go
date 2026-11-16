package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

var config = Config{
	Issuer:      "https://api.permaily.com",
	AppOrigin:   "https://app.permaily.com",
	APIResource: "https://api.permaily.com/v1",
	MCPResource: "https://mcp.permaily.com/mcp",
}

// The verifier and challenge of RFC 7636, appendix B.
const (
	rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func claudeCode() *ClientDocument {
	return &ClientDocument{ClientID: codeID, RedirectURIs: []string{"http://localhost/callback", "http://127.0.0.1/callback"}, TokenEndpointAuthMethod: AuthNone}
}

// request is a valid authorization request of Claude Code's.
func request() url.Values {
	return url.Values{
		"client_id":             {codeID},
		"redirect_uri":          {"http://localhost:53682/callback"},
		"response_type":         {"code"},
		"code_challenge":        {rfcChallenge},
		"code_challenge_method": {"S256"},
		"resource":              {config.MCPResource},
		"scope":                 {"mail.sender mail.subject mail.body"},
		"state":                 {"st"},
	}
}

func with(q url.Values, changes ...func(url.Values)) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = slices.Clone(v)
	}
	for _, c := range changes {
		c(out)
	}
	return out
}

func set(name string, values ...string) func(url.Values) {
	return func(q url.Values) {
		if values == nil {
			delete(q, name)
			return
		}
		q[name] = values
	}
}

// V4. A valid request is read whole; anything else is refused in the order
// of "The authorization request", and nothing before the redirect URI is
// proven may be redirected.
func TestV4AuthorizeRefusesInItsOrder(t *testing.T) {
	p, proven, err := Authorize(with(request(), set("prompt", "consent"), set("nonce", "n"), set("login_hint", "x"), set("response_mode", "query"),
		set("query", `{"v":1}`), set("mail_since", "new"), set("ends_in_days", "3")), claudeCode(), config)
	if err != nil || !proven {
		t.Fatal(err)
	}
	want := AuthorizeParams{ResponseType: "code", ClientID: codeID, RedirectURI: "http://localhost:53682/callback", State: "st", CodeChallenge: rfcChallenge,
		CodeChallengeMethod: "S256", Resource: config.MCPResource, Scope: []string{ScopeSender, ScopeSubject, ScopeBody}}
	if !equalParams(*p, want) {
		t.Fatalf("%+v", p)
	}
	if p.Query != nil || p.Since != "" || p.EndsInDays != 0 {
		t.Fatal("a pushed request's parameter taken from the address")
	}

	// Each step, with it and every later one broken: the refusal is that
	// step's, and only steps after the redirect URI are proven.
	steps := []struct {
		name   string
		breaks func(url.Values)
		code   ErrorCode
		reason string
	}{
		{"client_id", set("client_id", "https://claude.ai/oauth/other"), ErrInvalidClient, string(contracts.ClientIDInvalid)},
		{"redirect_uri", set("redirect_uri", "http://127.0.0.1:1/callback/"), ErrInvalidClient, string(contracts.RedirectURIInvalid)},
		{"response_type", set("response_type", "token"), ErrUnsupportedResponse, string(ErrUnsupportedResponse)},
		{"code_challenge", set("code_challenge", "short"), ErrInvalidRequest, string(ErrInvalidRequest)},
		{"code_challenge_method", set("code_challenge_method", "plain"), ErrInvalidRequest, string(ErrInvalidRequest)},
		{"resource", set("resource", "https://api.permaily.com/v1/"), ErrInvalidTarget, string(ErrInvalidTarget)},
		{"scope", set("scope", "mail.everything"), ErrInvalidScope, string(ErrInvalidScope)},
		{"state", set("state", strings.Repeat("s", StateMaxBytes+1)), ErrInvalidRequest, string(ErrInvalidRequest)},
	}
	for i, s := range steps {
		q := request()
		for _, later := range steps[i:] {
			later.breaks(q)
		}
		p, proven, err := Authorize(q, claudeCode(), config)
		code(t, err, s.code, s.reason)
		if proven != (i >= 2) {
			t.Errorf("%s: proven %v", s.name, proven)
		}
		if proven && (p == nil || p.RedirectURI != "http://localhost:53682/callback") {
			t.Errorf("%s: no redirect URI to send the refusal to", s.name)
		}
		if !proven && p != nil {
			t.Errorf("%s: parameters with an unproven refusal", s.name)
		}
		if proven && p.State != "" && i == len(steps)-1 {
			t.Errorf("a state too long sent back")
		}
	}
	// A proven refusal carries the state as sent, for the redirect.
	p, _, err = Authorize(with(request(), set("response_type", "token")), claudeCode(), config)
	if err == nil || p.State != "st" {
		t.Fatalf("%v %+v", err, p)
	}

	for name, c := range map[string]struct {
		change func(url.Values)
		code   ErrorCode
		proven bool
	}{
		"client_id twice":            {set("client_id", codeID, codeID), ErrInvalidClient, false},
		"redirect_uri twice":         {set("redirect_uri", "http://localhost/callback", "http://localhost/callback"), ErrInvalidClient, false},
		"no redirect_uri":            {set("redirect_uri"), ErrInvalidClient, false},
		"state twice":                {set("state", "a", "b"), ErrInvalidRequest, true},
		"scope twice":                {set("scope", "mail.sender", "mail.body"), ErrInvalidRequest, true},
		"an unknown parameter twice": {set("x", "a", "b"), ErrInvalidRequest, true},
		"plain PKCE":                 {set("code_challenge_method", "plain"), ErrInvalidRequest, true},
		"no PKCE method":             {set("code_challenge_method"), ErrInvalidRequest, true},
		"no code_challenge":          {set("code_challenge"), ErrInvalidRequest, true},
		"a challenge of 129":         {set("code_challenge", strings.Repeat("a", 129)), ErrInvalidRequest, true},
		"a challenge with +":         {set("code_challenge", strings.Repeat("a", 42)+"+"), ErrInvalidRequest, true},
		"no response_type":           {set("response_type"), ErrUnsupportedResponse, true},
		"response_type code token":   {set("response_type", "code token"), ErrUnsupportedResponse, true},
		"no resource":                {set("resource"), ErrInvalidTarget, true},
		"another resource":           {set("resource", "https://mcp.permaily.com"), ErrInvalidTarget, true},
		"an unknown scope":           {set("scope", "mail.sender openid"), ErrInvalidScope, true},
		"an empty scope":             {set("scope", ""), ErrInvalidScope, true},
		"response_mode fragment":     {set("response_mode", "fragment"), ErrInvalidRequest, true},
		"request_uri with others":    {set("request_uri", RequestURIPrefix+PrefixPushed+strings.Repeat("A", 43)), ErrInvalidRequest, false},
	} {
		_, proven, err := Authorize(with(request(), c.change), claudeCode(), config)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
		if proven != c.proven {
			t.Errorf("%s: proven %v", name, proven)
		}
	}

	// A request with request_uri carries client_id alone besides it.
	uri := RequestURIPrefix + PrefixPushed + strings.Repeat("A", 43)
	p, proven, err = Authorize(url.Values{"client_id": {codeID}, "request_uri": {uri}}, claudeCode(), config)
	if err != nil || proven || p.RequestURI != uri || p.ClientID != codeID || p.RedirectURI != "" {
		t.Fatalf("%+v %v %v", p, proven, err)
	}
	for _, bad := range []string{"", PrefixPushed + strings.Repeat("A", 43), RequestURIPrefix + PrefixPushed + strings.Repeat("A", 42), RequestURIPrefix + PrefixCode + strings.Repeat("A", 43), RequestURIPrefix + PrefixPushed + strings.Repeat("A", 42) + "B"} {
		if _, _, err := Authorize(url.Values{"client_id": {codeID}, "request_uri": {bad}}, claudeCode(), config); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := Authorize(url.Values{"client_id": {codeID}, "request_uri": {uri, uri}}, claudeCode(), config); err == nil {
		t.Error("request_uri twice")
	}
	// The API's resource is the other one taken.
	if p, _, err := Authorize(with(request(), set("resource", config.APIResource)), claudeCode(), config); err != nil || p.Resource != config.APIResource {
		t.Fatal(err)
	}
}

func equalParams(a, b AuthorizeParams) bool {
	return a.ResponseType == b.ResponseType && a.ClientID == b.ClientID && a.RedirectURI == b.RedirectURI && a.State == b.State &&
		a.CodeChallenge == b.CodeChallenge && a.CodeChallengeMethod == b.CodeChallengeMethod && a.Resource == b.Resource &&
		slices.Equal(a.Scope, b.Scope) && a.RequestURI == b.RequestURI && string(a.Query) == string(b.Query) && a.Since == b.Since && a.EndsInDays == b.EndsInDays
}

const formula = `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]}]}`

func pushed() url.Values {
	return with(request(), set("redirect_uri", "https://claude.ai/api/mcp/auth_callback"), set("resource", config.APIResource),
		set("query", formula), set("mail_since", "2026-10-01T00:00:00Z"), set("ends_in_days", "30"))
}

func claudeAI() *ClientDocument {
	return &ClientDocument{ClientID: codeID, RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}, TokenEndpointAuthMethod: AuthNone}
}

// V5. A pushed request is where a formula, a mail since and an end are
// taken, each checked; a refusal is the endpoint's answer, and one about
// the redirect URI comes before anything else.
func TestV5PushedTakesTheQueryOnlyThere(t *testing.T) {
	p, err := Pushed(pushed(), claudeAI(), config)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Query) != formula || p.Since != "2026-10-01T00:00:00Z" || p.EndsInDays != 30 || p.State != "st" || p.Resource != config.APIResource || p.RedirectURI != "https://claude.ai/api/mcp/auth_callback" {
		t.Fatalf("%+v", p)
	}
	p, err = Pushed(with(pushed(), set("query"), set("mail_since", "new"), set("ends_in_days")), claudeAI(), config)
	if err != nil || p.Query != nil || p.Since != "new" || p.EndsInDays != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, days := range []string{"1", "365"} {
		if _, err := Pushed(with(pushed(), set("ends_in_days", days)), claudeAI(), config); err != nil {
			t.Fatalf("%s days: %v", days, err)
		}
	}

	// The form as encoded, and the formula as decoded, are each bounded on
	// their own.
	base := len(pushed().Encode()) + len("&pad=")
	if _, err := Pushed(with(pushed(), set("pad", strings.Repeat("a", FormMaxBytes-base))), claudeAI(), config); err != nil {
		t.Fatalf("a form of 64 KiB: %v", err)
	}
	big := `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"` + strings.Repeat("a", 100) + `"}]}]}`
	for len(big) < contracts.MaxFormulaBytes+1 {
		big += " "
	}
	for name, c := range map[string]struct {
		change func(url.Values)
		code   ErrorCode
		reason string
	}{
		"a form of 64 KiB and one byte":     {set("pad", strings.Repeat("a", FormMaxBytes-base+1)), ErrInvalidRequest, ""},
		"a query of 64 KiB and one byte":    {set("query", big[:contracts.MaxFormulaBytes+1]), ErrInvalidRequest, ""},
		"a formula query.Parse refuses":     {set("query", `{"v":1,"all":[{"any":[{"var":"nope","op":"is","value":"x"}]}]}`), ErrInvalidRequest, ""},
		"a formula that is not JSON":        {set("query", "subject:invoice"), ErrInvalidRequest, ""},
		"query twice":                       {set("query", formula, formula), ErrInvalidRequest, ""},
		"mail_since that is no time":        {set("mail_since", "yesterday"), ErrInvalidRequest, ""},
		"mail_since twice":                  {set("mail_since", "new", "new"), ErrInvalidRequest, ""},
		"ends_in_days 0":                    {set("ends_in_days", "0"), ErrInvalidRequest, ""},
		"ends_in_days 366":                  {set("ends_in_days", "366"), ErrInvalidRequest, ""},
		"ends_in_days +5":                   {set("ends_in_days", "+5"), ErrInvalidRequest, ""},
		"ends_in_days 05":                   {set("ends_in_days", "05"), ErrInvalidRequest, ""},
		"ends_in_days x":                    {set("ends_in_days", "x"), ErrInvalidRequest, ""},
		"request_uri in a pushed request":   {set("request_uri", "urn:x"), ErrInvalidRequest, ""},
		"plain PKCE":                        {set("code_challenge_method", "plain"), ErrInvalidRequest, ""},
		"no resource":                       {set("resource"), ErrInvalidTarget, ""},
		"another client":                    {set("client_id", "https://claude.ai/oauth/other"), ErrInvalidClient, string(contracts.ClientIDInvalid)},
		"client_id twice":                   {set("client_id", codeID, codeID), ErrInvalidRequest, ""},
		"redirect_uri twice":                {set("redirect_uri", "a", "a"), ErrInvalidRequest, ""},
		"a redirect URI not the document's": {set("redirect_uri", "https://claude.ai/elsewhere"), ErrInvalidClient, string(contracts.RedirectURIInvalid)},
		"a bad redirect URI and everything bad": {func(q url.Values) {
			q.Set("redirect_uri", "https://evil.example.com/")
			q.Set("query", "x")
			q.Set("response_type", "token")
		}, ErrInvalidClient, string(contracts.RedirectURIInvalid)},
	} {
		_, err := Pushed(with(pushed(), c.change), claudeAI(), config)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		reason := c.reason
		if reason == "" {
			reason = string(c.code)
		}
		code(t, err, c.code, reason)
	}
}

// V6. PKCE S256 against RFC 7636's vector and random ones.
func TestV6VerifyPKCE(t *testing.T) {
	if !VerifyPKCE(rfcChallenge, rfcVerifier) {
		t.Fatal("RFC 7636's vector")
	}
	if VerifyPKCE(rfcChallenge, rfcVerifier[:42]+"l") || VerifyPKCE(rfcChallenge[:42]+"N", rfcVerifier) || VerifyPKCE("", rfcVerifier) {
		t.Fatal("another verifier or challenge")
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	for n := 43; n <= 128; n++ {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		sum := sha256.Sum256(b)
		if !VerifyPKCE(base64.RawURLEncoding.EncodeToString(sum[:]), string(b)) {
			t.Fatalf("a verifier of %d", n)
		}
	}
	for _, v := range []string{strings.Repeat("a", 42), strings.Repeat("a", 129), strings.Repeat("a", 42) + "+", strings.Repeat("a", 42) + "/", strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + " ", strings.Repeat("a", 42) + "é"} {
		sum := sha256.Sum256([]byte(v))
		if VerifyPKCE(base64.RawURLEncoding.EncodeToString(sum[:]), v) {
			t.Errorf("%q accepted", v)
		}
	}
}

// V7. Scopes are the parts: absent the default three, sender always, in a
// fixed order; an unknown or repeated one refused.
func TestV7ScopesAndParts(t *testing.T) {
	got, err := Scopes("", false)
	if err != nil || !slices.Equal(got, DefaultScopes) {
		t.Fatal(got, err)
	}
	got[0] = "changed"
	if DefaultScopes[0] != ScopeSender {
		t.Fatal("the default scopes were handed out")
	}
	for s, want := range map[string][]string{
		"mail.body":                    {ScopeSender, ScopeBody},
		"mail.attachments mail.sender": {ScopeSender, ScopeAttachments},
		"mail.body mail.subject":       {ScopeSender, ScopeSubject, ScopeBody},
		"mail.attachments mail.body mail.subject mail.sender": {ScopeSender, ScopeSubject, ScopeBody, ScopeAttachments},
	} {
		got, err := Scopes(s, true)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	for _, s := range []string{"", " ", "mail.body mail.body", "mail.body  mail.subject", " mail.body", "mail.body ", "Mail.Body", "openid", "mail.sender,mail.body", "mail.body\tmail.subject"} {
		if _, err := Scopes(s, true); err == nil {
			t.Errorf("%q accepted", s)
		} else {
			code(t, err, ErrInvalidScope, string(ErrInvalidScope))
		}
	}
	for scopes, want := range map[string][]contracts.Part{
		"":                              {contracts.PartSender},
		"mail.body":                     {contracts.PartSender, contracts.PartBody},
		"mail.attachments mail.subject": {contracts.PartSender, contracts.PartSubject, contracts.PartAttachments},
		"mail.sender mail.subject mail.body mail.attachments": {contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments},
		"openid mail.body": {contracts.PartSender, contracts.PartBody},
	} {
		if got := Parts(strings.Fields(scopes)); !slices.Equal(got, want) {
			t.Errorf("%q: %v", scopes, got)
		}
	}
}

// Redirect adds its parameters and iss, keeping the redirect URI's own query.
func TestRedirectKeepsTheQueryAndAddsIss(t *testing.T) {
	for uri, want := range map[string]string{
		"https://claude.ai/api/mcp/auth_callback":       "https://claude.ai/api/mcp/auth_callback?code=pmc_x&iss=https%3A%2F%2Fapi.permaily.com&state=a+b%26c",
		"http://localhost:53682/callback?from=permaily": "http://localhost:53682/callback?from=permaily&code=pmc_x&iss=https%3A%2F%2Fapi.permaily.com&state=a+b%26c",
	} {
		if got := Redirect(uri, url.Values{"code": {"pmc_x"}, "state": {"a b&c"}, "iss": {"forged"}}, config.Issuer); got != want {
			t.Errorf("%s:\n got %s\nwant %s", uri, got, want)
		}
	}
	if got := Redirect("https://claude.ai/cb", url.Values{"error": {"access_denied"}}, config.Issuer); got != "https://claude.ai/cb?error=access_denied&iss=https%3A%2F%2Fapi.permaily.com" {
		t.Fatal(got)
	}
	if got := Redirect("http://[::1:x/", nil, config.Issuer); got != "" {
		t.Fatal(got)
	}
}
