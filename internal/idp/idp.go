// Package idp is the client side of signing in with Google, GitHub, Meta and
// Microsoft (ADR 0017): where to send the browser, and who came back.
//
// It is this repository's own code because it runs inside the enclave: an
// OAuth or OpenID Connect library would bring every algorithm, every flow and
// their dependencies into the measured image. What is here is one flow (the
// authorization code with PKCE) and one signature (RS256). The check of an ID
// token is compared with a known library in tests/idp-differential.
//
// A provider is trusted for who signed in and nothing more. Its answers reach
// the enclave over TLS that the host cannot open, and are still read as input:
// strictly, bounded, and never repeated in an error, since errors are logged
// and logs are public.
package idp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider is who a person signs in with.
type Provider string

const (
	Google    Provider = "google"
	GitHub    Provider = "github"
	Meta      Provider = "meta"
	Microsoft Provider = "microsoft"
)

// Providers are the ones there are, in the order to offer them.
var Providers = []Provider{Google, GitHub, Meta, Microsoft}

// App is an application registered with a provider.
type App struct{ ClientID, ClientSecret string }

// Client talks to the providers. Set HTTP and RedirectURL.
type Client struct {
	HTTP        *http.Client // reaches the providers; in the enclave, through the host's proxy
	RedirectURL string       // https://app.permaily.com/api/v1/auth/provider/callback
	Now         func() time.Time
	// Endpoints replaces where a provider is (tests); nil for the real ones.
	Endpoints map[Provider]Endpoints

	// sets are the keys of each provider that signs, by where they are
	// published. A map and no lock of its own, so a Client can be copied.
	sets map[string]*keySet
}

// setsLock guards the sets of every Client. It is held for a lookup, never
// while a provider is asked.
var setsLock sync.Mutex

// Endpoints are the addresses of one provider. Issuer is who its ID tokens
// say made them; "{tenantid}" in it stands for the token's own tenant.
type Endpoints struct{ Authorize, Token, Keys, User, Emails, Issuer string }

// Started is what must be kept to end what Begin started.
type Started struct{ URL, Verifier, Nonce string }

// Identity is who a provider says signed in.
type Identity struct {
	Provider Provider
	Subject  string // the provider's stable identifier of the account, never an address
	Address  string // may be empty
	Verified bool   // the provider vouches that Address belongs to the account, by the rules of the ADR
	Name     string
}

// ErrInvalid is every refusal of what a provider answered, or of what the
// browser brought back from it. It is wrapped with the check that failed, in
// words that hold nothing of the answer.
var ErrInvalid = errors.New("idp: answer refused")

// ErrUnreachable is a provider that could not be reached, or that answered
// with an error of its own (a code it does not know, a secret it refuses).
var ErrUnreachable = errors.New("idp: provider unreachable")

// The real providers. Where each address comes from:
//
//   - Google: https://accounts.google.com/.well-known/openid-configuration
//   - GitHub: https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps
//     and https://docs.github.com/en/rest/users
//   - Meta: https://developers.facebook.com/docs/facebook-login/guides/advanced/manual-flow
//     (the version in the path is one of the Graph API, and each lasts about two years)
//   - Microsoft: https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration
//     ("common" takes personal accounts and those of every organisation)
//
//nolint:gosec // public addresses: "Token" names an endpoint here, not a secret
var real = map[Provider]Endpoints{
	Google: {
		Authorize: "https://accounts.google.com/o/oauth2/v2/auth",
		Token:     "https://oauth2.googleapis.com/token",
		Keys:      "https://www.googleapis.com/oauth2/v3/certs",
		Issuer:    "https://accounts.google.com",
	},
	GitHub: {
		Authorize: "https://github.com/login/oauth/authorize",
		Token:     "https://github.com/login/oauth/access_token",
		User:      "https://api.github.com/user",
		Emails:    "https://api.github.com/user/emails",
	},
	Meta: {
		Authorize: "https://www.facebook.com/v27.0/dialog/oauth",
		Token:     "https://graph.facebook.com/v27.0/oauth/access_token",
		User:      "https://graph.facebook.com/v27.0/me",
	},
	Microsoft: {
		Authorize: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		Token:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		Keys:      "https://login.microsoftonline.com/common/discovery/v2.0/keys",
		Issuer:    "https://login.microsoftonline.com/" + tenantPlace + "/v2.0",
	},
}

// scopes are the least that give the identifier, the address and the name.
var scopes = map[Provider]string{
	Google:    "openid email profile",
	GitHub:    "read:user user:email",
	Meta:      "email public_profile",
	Microsoft: "openid email profile",
}

// Limits of one sign-in. Each is well above what a provider sends: a code of
// one or two thousand bytes (Microsoft's are the longest), a token answer of
// a few thousand, a user of two thousand.
const (
	maxCode        = 8 << 10
	maxTokenAnswer = 64 << 10
	maxUserAnswer  = 64 << 10
	maxAccessToken = 4 << 10
	maxName        = 200
	randomSize     = 32
	githubPage     = 100 // the most addresses GitHub gives in one answer
	githubVersion  = "2022-11-28"
)

// timeout is how long one request to a provider may take. It is a variable
// so a test does not have to wait for it.
var timeout = 15 * time.Second

func refuse(why string) error      { return fmt.Errorf("%w: %s", ErrInvalid, why) }
func unreachable(why string) error { return fmt.Errorf("%w: %s", ErrUnreachable, why) }

// signs says whether a provider signs who the person is (an ID token).
func signs(p Provider) bool { return p == Google || p == Microsoft }

// endpoints is where a provider is. Only the four there are have any:
// Endpoints replaces where one of them is, and adds none.
func (c *Client) endpoints(p Provider) (Endpoints, bool) {
	e, ok := real[p]
	if replaced, has := c.Endpoints[p]; ok && has {
		e = replaced
	}
	return e, ok
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// random is a fresh value nobody can guess, in the alphabet PKCE asks for.
func random() string {
	b := make([]byte, randomSize)
	_, _ = rand.Read(b) // crypto/rand does not fail: it stops the program when there is no randomness
	return base64.RawURLEncoding.EncodeToString(b)
}

// challenge is what the provider is shown of a verifier (RFC 7636, S256).
func challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// Begin returns where to send the browser, for state (an opaque value of the caller).
//
// Every provider is sent a PKCE challenge, the ones that hold a client secret
// too: the code then works only with the verifier, which never leaves the
// enclave. All four take it (Google and Microsoft name it in their discovery
// documents, GitHub and Meta in their guides).
func (c *Client) Begin(p Provider, app App, state string) (*Started, error) {
	e, ok := c.endpoints(p)
	if !ok {
		return nil, errors.New("idp: unknown provider")
	}
	if app.ClientID == "" || c.RedirectURL == "" || state == "" {
		return nil, errors.New("idp: a client identifier, a redirect address and a state are needed to begin")
	}
	u, err := url.Parse(e.Authorize)
	if err != nil || u.Host == "" {
		return nil, errors.New("idp: the authorization address is not one")
	}
	s := &Started{Verifier: random()}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", app.ClientID)
	q.Set("redirect_uri", c.RedirectURL)
	q.Set("scope", scopes[p])
	q.Set("state", state)
	q.Set("code_challenge", challenge(s.Verifier))
	q.Set("code_challenge_method", "S256")
	if signs(p) {
		// The nonce comes back inside what the provider signs, so a token made
		// for another sign-in cannot end this one.
		s.Nonce = random()
		q.Set("nonce", s.Nonce)
	}
	if p == Microsoft {
		// Said, not left to the default: the code must come in the query, where the callback reads it.
		q.Set("response_mode", "query")
	}
	u.RawQuery = q.Encode()
	s.URL = u.String()
	return s, nil
}

// Finish exchanges the code and finds out who it is.
//
// The access token of GitHub and Meta is used here and thrown away; nothing
// of a provider is kept but what Identity holds.
func (c *Client) Finish(ctx context.Context, p Provider, app App, code string, s Started) (*Identity, error) {
	e, ok := c.endpoints(p)
	if !ok {
		return nil, errors.New("idp: unknown provider")
	}
	if c.HTTP == nil || app.ClientID == "" || app.ClientSecret == "" || c.RedirectURL == "" {
		return nil, errors.New("idp: an HTTP client, the application and a redirect address are needed to finish")
	}
	// The code and what was started come with the browser: anyone can send them.
	if code == "" || len(code) > maxCode {
		return nil, refuse("no code, or one too long")
	}
	if s.Verifier == "" {
		return nil, refuse("started without a verifier")
	}
	// A missing nonce must never match a token that has none either.
	if signs(p) && s.Nonce == "" {
		return nil, refuse("started without a nonce")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"code_verifier": {s.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("idp: the token address is not one")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // GitHub answers a form without it
	body, err := c.send(req, "token endpoint", maxTokenAnswer)
	if err != nil {
		return nil, err
	}
	answer, err := parseTokenAnswer(body)
	if err != nil {
		return nil, err
	}

	switch p {
	case GitHub:
		if answer.access == "" {
			return nil, refuse("token answer without an access token")
		}
		return c.github(ctx, e, answer.access)
	case Meta:
		if answer.access == "" {
			return nil, refuse("token answer without an access token")
		}
		return c.meta(ctx, e, app, answer.access)
	default:
		if answer.id == "" {
			return nil, refuse("token answer without an ID token")
		}
		return c.idToken(ctx, p, e, app.ClientID, s.Nonce, answer.id)
	}
}

// github asks GitHub who holds the access token, and for their addresses:
// the one of the profile is whatever the person chose to show, verified or not.
func (c *Client) github(ctx context.Context, e Endpoints, access string) (*Identity, error) {
	header := http.Header{
		"Authorization":        {"Bearer " + access},
		"Accept":               {"application/vnd.github+json"},
		"X-Github-Api-Version": {githubVersion},
	}
	body, err := c.get(ctx, e.User, nil, header, "user endpoint", maxUserAnswer)
	if err != nil {
		return nil, err
	}
	id, err := parseGitHubUser(body)
	if err != nil {
		return nil, err
	}
	// One answer holds every address of nearly everyone. Somebody with more
	// than a hundred, whose primary one is not among them, is taken as having
	// none: the address is then confirmed by mail, which is the safe side.
	page := url.Values{"per_page": {strconv.Itoa(githubPage)}}
	if body, err = c.get(ctx, e.Emails, page, header, "addresses endpoint", maxUserAnswer); err != nil {
		return nil, err
	}
	if id.Address, err = parseGitHubEmails(body); err != nil {
		return nil, err
	}
	id.Verified = id.Address != ""
	return id, nil
}

// meta asks Meta who holds the access token. The proof shows the call is the
// application's own (the token alone, taken from somewhere else, would not do).
func (c *Client) meta(ctx context.Context, e Endpoints, app App, access string) (*Identity, error) {
	mac := hmac.New(sha256.New, []byte(app.ClientSecret))
	mac.Write([]byte(access))
	query := url.Values{"fields": {"id,name,email"}, "appsecret_proof": {hex.EncodeToString(mac.Sum(nil))}}
	// The token goes in a header, not in the address: an address ends up in more places.
	header := http.Header{"Authorization": {"Bearer " + access}, "Accept": {"application/json"}}
	body, err := c.get(ctx, e.User, query, header, "user endpoint", maxUserAnswer)
	if err != nil {
		return nil, err
	}
	return parseMetaUser(body)
}

// get asks an address of a provider, with more parameters and these headers.
func (c *Client) get(ctx context.Context, address string, add url.Values, header http.Header, what string, limit int) ([]byte, error) {
	u, err := url.Parse(address)
	if err == nil && u.Host == "" {
		err = errors.New("no host")
	}
	var req *http.Request
	if err == nil {
		q := u.Query()
		for name, values := range add {
			q[name] = values
		}
		u.RawQuery = q.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	}
	if err != nil {
		return nil, errors.New("idp: the address of the " + what + " is not one")
	}
	req.Header = header
	return c.send(req, what, limit)
}

// send makes one request and returns what was answered, when it is a success
// and no longer than limit.
//
// No error of the HTTP client is passed on: it names the address asked, which
// for Meta holds the proof made with the secret.
func (c *Client) send(req *http.Request, what string, limit int) ([]byte, error) {
	// A copy, so the caller's client is left as it is. A provider that sends
	// the request elsewhere is not followed (the secret would go along), and
	// no cookie of one sign-in is kept for the next.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil

	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, lost(ctx, what)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: the %s answered HTTP %d", ErrUnreachable, what, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, lost(ctx, what)
	}
	if len(body) > limit {
		return nil, refuse("the " + what + " answered too much")
	}
	return body, nil
}

// lost is a request with no answer. It says whether the time ran out or the
// caller gave up, which hold nothing of the request.
func lost(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: no answer from the %s: %w", ErrUnreachable, what, err)
	}
	return unreachable("no answer from the " + what)
}

// Hosts are the host:port the enclave must be let reach, for the host's
// allowlist. Where the browser is sent is not among them: the person's
// browser goes there, not the enclave.
func Hosts() []string {
	var hosts []string
	for _, p := range Providers {
		e := real[p]
		for _, address := range []string{e.Token, e.Keys, e.User, e.Emails} {
			if address == "" {
				continue
			}
			u, _ := url.Parse(address) // the addresses above are constants that parse
			host := u.Host
			if u.Port() == "" {
				host += ":443"
			}
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	slices.Sort(hosts)
	return hosts
}
