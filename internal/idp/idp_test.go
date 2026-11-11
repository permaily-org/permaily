package idp_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

const (
	redirect = "https://app.permaily.com/api/v1/auth/provider/callback"
	state    = "state-of-the-caller"
)

// start is the time of the tests: the fake dates its tokens with it and the
// client checks them against it.
var start = time.Unix(1_800_000_000, 0)

// world is a fake of the four providers and a client that talks to it, on one clock.
type world struct {
	fake   *idptest.Server
	client *idp.Client
	clock  atomic.Int64 // seconds
}

func newWorld(t testing.TB) *world {
	t.Helper()
	w := &world{fake: idptest.New()}
	t.Cleanup(w.fake.Close)
	w.clock.Store(start.Unix())
	w.fake.Now = w.now
	w.forget()
	return w
}

// forget gives the world a client that has kept nothing: no keys.
func (w *world) forget() {
	w.client = &idp.Client{HTTP: &http.Client{}, RedirectURL: redirect, Now: w.now, Endpoints: w.fake.Endpoints()}
}

func (w *world) now() time.Time          { return time.Unix(w.clock.Load(), 0) }
func (w *world) advance(d time.Duration) { w.clock.Add(int64(d / time.Second)) }

// goneOnce is the context of a person who goes away as soon as an endpoint
// of a provider is asked.
func (w *world) goneOnce(p idp.Provider, endpoint string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for w.fake.Count(p, endpoint) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	return ctx
}

// begin starts a sign-in and takes the browser to the provider and back.
func (w *world) begin(t testing.TB, p idp.Provider) (code string, started idp.Started) {
	t.Helper()
	s, err := w.client.Begin(p, w.fake.App(p), state)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	code, back, err := w.fake.Approve(s.URL)
	if err != nil {
		t.Fatalf("the provider did not take the address Begin made: %v", err)
	}
	if back != state {
		t.Fatalf("state came back as %q", back)
	}
	return code, *s
}

// signIn is a whole sign-in at a provider.
func (w *world) signIn(t testing.TB, p idp.Provider) (*idp.Identity, error) {
	t.Helper()
	code, started := w.begin(t, p)
	return w.client.Finish(context.Background(), p, w.fake.App(p), code, started)
}

// refused fails unless err is exactly sentinel with why. The whole text is
// compared: an error that carried anything of an answer would not match.
func refused(t testing.TB, id *idp.Identity, err, sentinel error, why string) {
	t.Helper()
	if id != nil {
		t.Fatalf("an identity came with the error: %+v", id)
	}
	if !errors.Is(err, sentinel) || err.Error() != sentinel.Error()+": "+why {
		t.Fatalf("got %v, want %v: %s", err, sentinel, why)
	}
}

func invalid(t testing.TB, id *idp.Identity, err error, why string) {
	t.Helper()
	refused(t, id, err, idp.ErrInvalid, why)
}

func unreachable(t testing.TB, id *idp.Identity, err error, why string) {
	t.Helper()
	refused(t, id, err, idp.ErrUnreachable, why)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestBeginSendsTheBrowserToTheProvider(t *testing.T) {
	want := map[idp.Provider]struct{ address, scope string }{
		idp.Google:    {"https://accounts.google.com/o/oauth2/v2/auth", "openid email profile"},
		idp.GitHub:    {"https://github.com/login/oauth/authorize", "read:user user:email"},
		idp.Meta:      {"https://www.facebook.com/v27.0/dialog/oauth", "email public_profile"},
		idp.Microsoft: {"https://login.microsoftonline.com/common/oauth2/v2.0/authorize", "openid email profile"},
	}
	client := &idp.Client{RedirectURL: redirect} // the real providers
	for _, p := range idp.Providers {
		t.Run(string(p), func(t *testing.T) {
			s, err := client.Begin(p, idp.App{ClientID: "the-client"}, state)
			if err != nil {
				t.Fatal(err)
			}
			address, query, _ := strings.Cut(s.URL, "?")
			if address != want[p].address {
				t.Fatalf("sends the browser to %s, want %s", address, want[p].address)
			}
			q, err := url.ParseQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(s.Verifier))
			expect := url.Values{
				"response_type":         {"code"},
				"client_id":             {"the-client"},
				"redirect_uri":          {redirect},
				"state":                 {state},
				"scope":                 {want[p].scope},
				"code_challenge":        {b64(sum[:])},
				"code_challenge_method": {"S256"},
			}
			signs := p == idp.Google || p == idp.Microsoft
			if signs {
				expect.Set("nonce", s.Nonce)
			}
			if p == idp.Microsoft {
				expect.Set("response_mode", "query")
			}
			if q.Encode() != expect.Encode() {
				t.Fatalf("asks\n%s\nwant\n%s", q.Encode(), expect.Encode())
			}
			if raw, err := base64.RawURLEncoding.DecodeString(s.Verifier); err != nil || len(raw) != 32 {
				t.Fatalf("the verifier is not 32 random bytes: %q", s.Verifier)
			}
			if raw, err := base64.RawURLEncoding.DecodeString(s.Nonce); signs && (err != nil || len(raw) != 32) {
				t.Fatalf("the nonce is not 32 random bytes: %q", s.Nonce)
			}
			if !signs && s.Nonce != "" {
				t.Fatalf("a nonce for a provider that signs nothing: %q", s.Nonce)
			}
			// The secret is never shown to the browser.
			again, err := client.Begin(p, idp.App{ClientID: "the-client", ClientSecret: "the-secret"}, state)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(again.URL, "the-secret") {
				t.Fatal("the secret is in the address the browser is sent to")
			}
			if again.Verifier == s.Verifier || (signs && again.Nonce == s.Nonce) {
				t.Fatal("two sign-ins share a verifier or a nonce")
			}
		})
	}
}

func TestBeginKeepsWhatTheAddressAlreadyAsks(t *testing.T) {
	client := &idp.Client{RedirectURL: redirect, Endpoints: map[idp.Provider]idp.Endpoints{
		idp.GitHub: {Authorize: "https://provider.example/authorize?tenant=one"},
	}}
	s, err := client.Begin(idp.GitHub, idp.App{ClientID: "the-client"}, state)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Query().Get("tenant") != "one" || u.Query().Get("state") != state {
		t.Fatalf("made %s (%v)", s.URL, err)
	}
}

func TestBeginRefusesWhatCannotBegin(t *testing.T) {
	app := idp.App{ClientID: "the-client"}
	bad := func(address string) *idp.Client {
		return &idp.Client{RedirectURL: redirect, Endpoints: map[idp.Provider]idp.Endpoints{idp.Google: {Authorize: address}}}
	}
	for name, begin := range map[string]func() (*idp.Started, error){
		"unknown provider": func() (*idp.Started, error) { return (&idp.Client{RedirectURL: redirect}).Begin("apple", app, state) },
		"a provider that is added": func() (*idp.Started, error) {
			added := map[idp.Provider]idp.Endpoints{"apple": {Authorize: "https://appleid.apple.com/auth/authorize"}}
			return (&idp.Client{RedirectURL: redirect, Endpoints: added}).Begin("apple", app, state)
		},
		"no client": func() (*idp.Started, error) {
			return (&idp.Client{RedirectURL: redirect}).Begin(idp.Google, idp.App{}, state)
		},
		"no state":            func() (*idp.Started, error) { return (&idp.Client{RedirectURL: redirect}).Begin(idp.Google, app, "") },
		"no redirect address": func() (*idp.Started, error) { return (&idp.Client{}).Begin(idp.Google, app, state) },
		"address not one":     func() (*idp.Started, error) { return bad("://nowhere").Begin(idp.Google, app, state) },
		"address with no host": func() (*idp.Started, error) {
			return bad("/authorize").Begin(idp.Google, app, state)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := begin()
			if err == nil || s != nil || errors.Is(err, idp.ErrInvalid) || errors.Is(err, idp.ErrUnreachable) {
				t.Fatalf("got %v, %v", s, err)
			}
		})
	}
}

const (
	work   = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	object = "00000000-0000-0000-66f3-3332eca7ea81"
)

func TestSignIn(t *testing.T) {
	for _, c := range []struct {
		name string
		p    idp.Provider
		who  idptest.Person
		want idp.Identity
	}{
		{"google, verified", idp.Google,
			idptest.Person{Subject: "10769150350006150715113082367", Address: "Ada@Example.com", Verified: true, Name: " Ada Lovelace "},
			idp.Identity{Subject: "10769150350006150715113082367", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"google, not verified", idp.Google,
			idptest.Person{Subject: "42", Address: "ada@example.com", Name: "Ada"},
			idp.Identity{Subject: "42", Address: "ada@example.com", Name: "Ada"}},
		{"google, no address", idp.Google,
			idptest.Person{Subject: "42", Verified: true},
			idp.Identity{Subject: "42"}},
		{"github, primary and verified", idp.GitHub,
			idptest.Person{Subject: "583231", Address: "Octo@Example.com", Verified: true, Name: "The Octocat"},
			idp.Identity{Subject: "583231", Address: "octo@example.com", Verified: true, Name: "The Octocat"}},
		{"github, primary and not verified", idp.GitHub,
			idptest.Person{Subject: "583231", Address: "octo@example.com", Name: "The Octocat"},
			idp.Identity{Subject: "583231", Name: "The Octocat"}},
		{"github, no address and no name", idp.GitHub,
			idptest.Person{Subject: "7"},
			idp.Identity{Subject: "7"}},
		{"meta, never verified", idp.Meta,
			idptest.Person{Subject: "10158123456789012", Address: "Ada@Example.com", Verified: true, Name: "Ada Lovelace"},
			idp.Identity{Subject: "10158123456789012", Address: "ada@example.com", Name: "Ada Lovelace"}},
		{"meta, no address", idp.Meta,
			idptest.Person{Subject: "10158123456789012", Name: "Ada"},
			idp.Identity{Subject: "10158123456789012", Name: "Ada"}},
		{"microsoft, personal account", idp.Microsoft,
			idptest.Person{Subject: "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", Address: "Ada@Outlook.com", Verified: true, Name: "Ada", Object: object},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Address: "ada@outlook.com", Verified: true, Name: "Ada"}},
		{"microsoft, work account whose domain was verified", idp.Microsoft,
			idptest.Person{Subject: "pairwise", Address: "ada@contoso.com", Verified: true, Tenant: work, Object: object},
			idp.Identity{Subject: work + "." + object, Address: "ada@contoso.com", Verified: true}},
		{"microsoft, work account that says nothing of its address", idp.Microsoft,
			idptest.Person{Subject: "pairwise", Address: "ceo@victim.example", Tenant: work, Object: object},
			idp.Identity{Subject: work + "." + object, Address: "ceo@victim.example"}},
		{"microsoft, no object", idp.Microsoft,
			idptest.Person{Subject: "pairwise-subject", Address: "ada@outlook.com", Verified: true},
			idp.Identity{Subject: "pairwise-subject", Address: "ada@outlook.com", Verified: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.SignInAs(c.p, c.who)
			got, err := w.signIn(t, c.p)
			if err != nil {
				t.Fatal(err)
			}
			c.want.Provider = c.p
			if *got != c.want {
				t.Fatalf("got %+v, want %+v", *got, c.want)
			}
		})
	}
}

// The fake refuses a token request that lacks the secret, the verifier, the
// redirect address or the form, as the real ones do; here the request itself
// is read too.
func TestFinishExchangesTheCode(t *testing.T) {
	for _, p := range idp.Providers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			code, started := w.begin(t, p)
			app := w.fake.App(p)
			if _, err := w.client.Finish(context.Background(), p, app, code, started); err != nil {
				t.Fatal(err)
			}
			seen := w.fake.Seen(p, idptest.Token)
			if len(seen) != 1 {
				t.Fatalf("the token endpoint was asked %d times", len(seen))
			}
			r := seen[0]
			want := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {redirect},
				"client_id":     {app.ClientID},
				"client_secret": {app.ClientSecret},
				"code_verifier": {started.Verifier},
			}
			if r.Method != http.MethodPost || r.Form.Encode() != want.Encode() {
				t.Fatalf("%s %s, want POST %s", r.Method, r.Form.Encode(), want.Encode())
			}
			if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Fatalf("Content-Type %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/json" {
				t.Fatalf("Accept %q", got)
			}
			// Nothing secret in the address, which a proxy or a log would keep.
			if len(r.Query) != 0 {
				t.Fatalf("the token request has a query: %v", r.Query)
			}
			// A code works once.
			id, err := w.client.Finish(context.Background(), p, app, code, started)
			if p == idp.GitHub {
				unreachable(t, id, err, "the token endpoint answered with an error")
			} else {
				unreachable(t, id, err, "the token endpoint answered HTTP 400")
			}
		})
	}
}

// What the browser brings back is only good with what the enclave kept.
func TestFinishNeedsWhatBeginGave(t *testing.T) {
	for _, p := range idp.Providers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			app := w.fake.App(p)
			ctx := context.Background()

			// The verifier of another sign-in: the provider refuses.
			code, _ := w.begin(t, p)
			_, other := w.begin(t, p)
			id, err := w.client.Finish(ctx, p, app, code, other)
			if !errors.Is(err, idp.ErrUnreachable) || id != nil {
				t.Fatalf("a code was exchanged with another sign-in's verifier: %v", err)
			}

			// Another secret.
			code, started := w.begin(t, p)
			id, err = w.client.Finish(ctx, p, idp.App{ClientID: app.ClientID, ClientSecret: "another"}, code, started)
			if !errors.Is(err, idp.ErrUnreachable) || id != nil {
				t.Fatalf("a code was exchanged with another secret: %v", err)
			}

			// Another redirect address.
			code, started = w.begin(t, p)
			elsewhere := &idp.Client{HTTP: &http.Client{}, RedirectURL: "https://elsewhere.example/callback", Now: w.now, Endpoints: w.fake.Endpoints()}
			id, err = elsewhere.Finish(ctx, p, app, code, started)
			if !errors.Is(err, idp.ErrUnreachable) || id != nil {
				t.Fatalf("a code was exchanged for another redirect address: %v", err)
			}

			// Nothing is asked of the provider for what cannot be right.
			before := w.fake.Count(p, idptest.Token)
			code, started = w.begin(t, p)
			id, err = w.client.Finish(ctx, p, app, "", started)
			invalid(t, id, err, "no code, or one too long")
			id, err = w.client.Finish(ctx, p, app, strings.Repeat("c", idp.MaxCode+1), started)
			invalid(t, id, err, "no code, or one too long")
			id, err = w.client.Finish(ctx, p, app, code, idp.Started{Nonce: started.Nonce})
			invalid(t, id, err, "started without a verifier")
			if p == idp.Google || p == idp.Microsoft {
				id, err = w.client.Finish(ctx, p, app, code, idp.Started{Verifier: started.Verifier})
				invalid(t, id, err, "started without a nonce")
			}
			if w.fake.Count(p, idptest.Token) != before {
				t.Fatal("the provider was asked")
			}
		})
	}
}

func TestFinishRefusesWhatCannotFinish(t *testing.T) {
	w := newWorld(t)
	app := w.fake.App(idp.Google)
	code, started := w.begin(t, idp.Google)
	with := func(change func(c *idp.Client)) *idp.Client {
		c := &idp.Client{HTTP: &http.Client{}, RedirectURL: redirect, Now: w.now, Endpoints: w.fake.Endpoints()}
		change(c)
		return c
	}
	endpoints := func(p idp.Provider, change func(e *idp.Endpoints)) func(c *idp.Client) {
		return func(c *idp.Client) {
			e := c.Endpoints[p]
			change(&e)
			c.Endpoints[p] = e
		}
	}
	for name, c := range map[string]struct {
		client *idp.Client
		p      idp.Provider
		app    idp.App
	}{
		"unknown provider":         {w.client, "apple", app},
		"a provider that is added": {with(func(c *idp.Client) { c.Endpoints["apple"] = c.Endpoints[idp.Google] }), "apple", app},
		"no HTTP client":           {with(func(c *idp.Client) { c.HTTP = nil }), idp.Google, app},
		"no redirect address":      {with(func(c *idp.Client) { c.RedirectURL = "" }), idp.Google, app},
		"no client":                {w.client, idp.Google, idp.App{ClientSecret: app.ClientSecret}},
		"no secret":                {w.client, idp.Google, idp.App{ClientID: app.ClientID}},
		"token address":            {with(endpoints(idp.Google, func(e *idp.Endpoints) { e.Token = "://nowhere" })), idp.Google, app},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := c.client.Finish(context.Background(), c.p, c.app, code, started)
			if err == nil || id != nil || errors.Is(err, idp.ErrInvalid) || errors.Is(err, idp.ErrUnreachable) {
				t.Fatalf("got %v, %v", id, err)
			}
		})
	}

	// The addresses asked after the code is exchanged.
	for name, c := range map[string]struct {
		p      idp.Provider
		change func(e *idp.Endpoints)
	}{
		"no issuer":               {idp.Google, func(e *idp.Endpoints) { e.Issuer = "" }},
		"no keys":                 {idp.Microsoft, func(e *idp.Endpoints) { e.Keys = "" }},
		"keys address":            {idp.Google, func(e *idp.Endpoints) { e.Keys = "://nowhere" }},
		"github user address":     {idp.GitHub, func(e *idp.Endpoints) { e.User = "://nowhere" }},
		"github emails address":   {idp.GitHub, func(e *idp.Endpoints) { e.Emails = "://nowhere" }},
		"github emails, no host":  {idp.GitHub, func(e *idp.Endpoints) { e.Emails = "/user/emails" }},
		"meta user address":       {idp.Meta, func(e *idp.Endpoints) { e.User = "://nowhere" }},
		"meta user address, odd":  {idp.Meta, func(e *idp.Endpoints) { e.User = "http://host/\x7f" }},
		"github user address odd": {idp.GitHub, func(e *idp.Endpoints) { e.User = "http://host/\x7f" }},
	} {
		t.Run(name, func(t *testing.T) {
			client := with(endpoints(c.p, c.change))
			s, err := client.Begin(c.p, w.fake.App(c.p), state)
			if err != nil {
				t.Fatal(err)
			}
			code, _, err := w.fake.Approve(s.URL)
			if err != nil {
				t.Fatal(err)
			}
			id, err := client.Finish(context.Background(), c.p, w.fake.App(c.p), code, *s)
			if err == nil || id != nil || errors.Is(err, idp.ErrInvalid) || errors.Is(err, idp.ErrUnreachable) {
				t.Fatalf("got %v, %v", id, err)
			}
		})
	}
}

func TestGitHubIsAskedWithTheAccessToken(t *testing.T) {
	w := newWorld(t)
	var access string
	w.fake.Misbehave(idp.GitHub, idptest.Faults{EditTokenAnswer: func(m map[string]any) { access = m["access_token"].(string) }})
	if _, err := w.signIn(t, idp.GitHub); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{idptest.User, idptest.Emails} {
		seen := w.fake.Seen(idp.GitHub, endpoint)
		if len(seen) != 1 {
			t.Fatalf("%s was asked %d times", endpoint, len(seen))
		}
		r := seen[0]
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+access {
			t.Fatalf("%s was asked with %s and %q", endpoint, r.Method, r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Fatalf("%s was asked with %v", endpoint, r.Header)
		}
		// The token is in a header, never in the address.
		if strings.Contains(r.Query.Encode(), access) {
			t.Fatal("the access token is in the address")
		}
	}
	if got := w.fake.Seen(idp.GitHub, idptest.Emails)[0].Query.Get("per_page"); got != "100" {
		t.Fatalf("asks for %q addresses at a time", got)
	}
}

func TestMetaIsAskedWithTheProof(t *testing.T) {
	w := newWorld(t)
	var access string
	w.fake.Misbehave(idp.Meta, idptest.Faults{EditTokenAnswer: func(m map[string]any) { access = m["access_token"].(string) }})
	if _, err := w.signIn(t, idp.Meta); err != nil {
		t.Fatal(err)
	}
	seen := w.fake.Seen(idp.Meta, idptest.User)
	if len(seen) != 1 {
		t.Fatalf("/me was asked %d times", len(seen))
	}
	mac := hmac.New(sha256.New, []byte(w.fake.App(idp.Meta).ClientSecret))
	mac.Write([]byte(access))
	want := url.Values{"fields": {"id,name,email"}, "appsecret_proof": {hex.EncodeToString(mac.Sum(nil))}}
	r := seen[0]
	if r.Method != http.MethodGet || r.Query.Encode() != want.Encode() {
		t.Fatalf("asked %s %s, want GET %s", r.Method, r.Query.Encode(), want.Encode())
	}
	if r.Header.Get("Authorization") != "Bearer "+access {
		t.Fatalf("asked with %q", r.Header.Get("Authorization"))
	}

	// With the proof of another secret Meta refuses, as it does when the
	// application asks for proofs.
	code, started := w.begin(t, idp.Meta)
	other := idp.App{ClientID: w.fake.App(idp.Meta).ClientID, ClientSecret: "another"}
	w.fake.Misbehave(idp.Meta, idptest.Faults{Token: nil})
	id, err := w.client.Finish(context.Background(), idp.Meta, other, code, started)
	if !errors.Is(err, idp.ErrUnreachable) || id != nil {
		t.Fatalf("got %v, %v", id, err)
	}
}

// answers are the endpoints a provider is asked after the browser is back,
// with how to replace each one's answer.
var answers = []struct {
	p        idp.Provider
	endpoint string
	what     string
	limit    int
	fault    func(a *idptest.Answer) idptest.Faults
}{
	{idp.Google, idptest.Token, "token endpoint", idp.MaxTokenAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Token: a} }},
	{idp.Google, idptest.Keys, "keys endpoint", idp.MaxKeySet, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Keys: a} }},
	{idp.Microsoft, idptest.Token, "token endpoint", idp.MaxTokenAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Token: a} }},
	{idp.Microsoft, idptest.Keys, "keys endpoint", idp.MaxKeySet, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Keys: a} }},
	{idp.GitHub, idptest.Token, "token endpoint", idp.MaxTokenAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Token: a} }},
	{idp.GitHub, idptest.User, "user endpoint", idp.MaxUserAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{User: a} }},
	{idp.GitHub, idptest.Emails, "addresses endpoint", idp.MaxUserAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Emails: a} }},
	{idp.Meta, idptest.Token, "token endpoint", idp.MaxTokenAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{Token: a} }},
	{idp.Meta, idptest.User, "user endpoint", idp.MaxUserAnswer, func(a *idptest.Answer) idptest.Faults { return idptest.Faults{User: a} }},
}

func TestAProviderThatAnswersWithAnError(t *testing.T) {
	// What a provider says with its error names the code, the address or the
	// account; none of it may be passed on.
	body := []byte(`{"error":"invalid_grant","error_description":"the code c0de-of-ada for ada@example.com"}`)
	for _, a := range answers {
		for _, status := range []int{400, 401, 403, 404, 429, 500, 503} {
			t.Run(string(a.p)+" "+a.endpoint+" "+strconv.Itoa(status), func(t *testing.T) {
				w := newWorld(t)
				w.fake.Misbehave(a.p, a.fault(&idptest.Answer{Status: status, Body: body}))
				id, err := w.signIn(t, a.p)
				unreachable(t, id, err, "the "+a.what+" answered HTTP "+strconv.Itoa(status))
			})
		}
	}
}

// A provider that sends the request elsewhere is not followed: the secret,
// the code and the access token would go along.
func TestNoRedirectIsFollowed(t *testing.T) {
	for _, a := range answers {
		t.Run(string(a.p)+" "+a.endpoint, func(t *testing.T) {
			var followed atomic.Int32
			elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				followed.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer elsewhere.Close()
			w := newWorld(t)
			w.fake.Misbehave(a.p, a.fault(&idptest.Answer{Redirect: elsewhere.URL + "/taken"}))
			id, err := w.signIn(t, a.p)
			unreachable(t, id, err, "the "+a.what+" answered HTTP 302")
			if followed.Load() != 0 {
				t.Fatal("the redirect was followed")
			}
			// The caller's client is left as it was.
			if w.client.HTTP.CheckRedirect != nil {
				t.Fatal("the caller's HTTP client was changed")
			}
		})
	}
}

func TestAnAnswerTooLong(t *testing.T) {
	for _, a := range answers {
		t.Run(string(a.p)+" "+a.endpoint, func(t *testing.T) {
			w := newWorld(t)
			// Right JSON all the way, so only its size is wrong.
			long := []byte(`{"padding":"` + strings.Repeat("a", a.limit) + `"}`)
			w.fake.Misbehave(a.p, a.fault(&idptest.Answer{Body: long}))
			id, err := w.signIn(t, a.p)
			invalid(t, id, err, "the "+a.what+" answered too much")
		})
	}
}

// endless is an answer that never ends, and counts what was read of it.
type endless struct{ read atomic.Int64 }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	e.read.Add(int64(len(p)))
	return len(p), nil
}

func (e *endless) Close() error { return nil }

// No more is read of an answer than may be kept, however much there is.
func TestAnAnswerWithoutEnd(t *testing.T) {
	var body endless
	client := &idp.Client{RedirectURL: redirect, HTTP: &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &body, Request: r}, nil
	})}}
	id, err := client.Finish(context.Background(), idp.GitHub, idp.App{ClientID: "c", ClientSecret: "s"}, "code", idp.Started{Verifier: "v"})
	invalid(t, id, err, "the token endpoint answered too much")
	if got := body.read.Load(); got != idp.MaxTokenAnswer+1 {
		t.Fatalf("read %d bytes of an answer that may hold %d", got, idp.MaxTokenAnswer)
	}
}

func TestAProviderThatDoesNotAnswer(t *testing.T) {
	defer idp.SetTimeout(100 * time.Millisecond)()
	for _, a := range answers {
		t.Run(string(a.p)+" "+a.endpoint, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(a.p, a.fault(&idptest.Answer{Hang: true}))
			began := time.Now()
			id, err := w.signIn(t, a.p)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("got %v, want the time to run out", err)
			}
			unreachable(t, id, err, "no answer from the "+a.what+": context deadline exceeded")
			if took := time.Since(began); took > 5*time.Second {
				t.Fatalf("waited %v", took)
			}
		})
	}
}

func TestTheCallerGivesUp(t *testing.T) {
	w := newWorld(t)
	w.fake.Misbehave(idp.GitHub, idptest.Faults{User: &idptest.Answer{Hang: true}})
	code, started := w.begin(t, idp.GitHub)
	ctx := w.goneOnce(idp.GitHub, idptest.User)
	id, err := w.client.Finish(ctx, idp.GitHub, w.fake.App(idp.GitHub), code, started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	unreachable(t, id, err, "no answer from the user endpoint: context canceled")
}

func TestAProviderThatIsNotThere(t *testing.T) {
	w := newWorld(t)
	code, started := w.begin(t, idp.Meta)
	w.fake.Close()
	id, err := w.client.Finish(context.Background(), idp.Meta, w.fake.App(idp.Meta), code, started)
	// The error of the HTTP client names the address asked; it is not passed on.
	unreachable(t, id, err, "no answer from the token endpoint")
}

// An answer cut in the middle is no answer, whatever was read of it.
func TestAnAnswerCutShort(t *testing.T) {
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"access_token":"abc","token_type":"bearer"`))
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer cut.Close()
	client := &idp.Client{HTTP: &http.Client{}, RedirectURL: redirect, Endpoints: map[idp.Provider]idp.Endpoints{
		idp.GitHub: {Token: cut.URL},
	}}
	id, err := client.Finish(context.Background(), idp.GitHub, idp.App{ClientID: "c", ClientSecret: "s"}, "code", idp.Started{Verifier: "v"})
	unreachable(t, id, err, "no answer from the token endpoint")
}

// The requests go out through the client that was given, as it is: its
// transport (in the enclave, the host's proxy and the pinned roots) and no
// cookie from one request to the next.
func TestTheClientGivenIsUsedAsItIs(t *testing.T) {
	var cookies []string
	var mu sync.Mutex
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "of-the-provider", Path: "/"})
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"abc","token_type":"bearer"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":7,"name":null}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer provider.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	var trips atomic.Int32
	given := &http.Client{Jar: jar, Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		trips.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	client := &idp.Client{HTTP: given, RedirectURL: redirect, Endpoints: map[idp.Provider]idp.Endpoints{
		idp.GitHub: {Token: provider.URL + "/token", User: provider.URL + "/user", Emails: provider.URL + "/emails"},
	}}
	id, err := client.Finish(context.Background(), idp.GitHub, idp.App{ClientID: "c", ClientSecret: "s"}, "code", idp.Started{Verifier: "v"})
	if err != nil || id.Subject != "7" {
		t.Fatalf("got %v, %v", id, err)
	}
	if trips.Load() != 3 {
		t.Fatalf("%d requests went through the transport given, want 3", trips.Load())
	}
	if !slices.Equal(cookies, []string{"", "", ""}) {
		t.Fatalf("cookies were carried from one request to the next: %q", cookies)
	}
	u, _ := url.Parse(provider.URL)
	if given.Jar != jar || len(jar.Cookies(u)) != 0 || given.CheckRedirect != nil {
		t.Fatal("the client given was changed, or its jar was filled")
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHosts(t *testing.T) {
	want := []string{
		"api.github.com:443",
		"github.com:443",
		"graph.facebook.com:443",
		"login.microsoftonline.com:443",
		"oauth2.googleapis.com:443",
		"www.googleapis.com:443",
	}
	if got := idp.Hosts(); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Each call gives its own list.
	idp.Hosts()[0] = "changed"
	if idp.Hosts()[0] != want[0] {
		t.Fatal("the list is shared")
	}
}

// With no clock given, tokens are checked against the system's.
func TestTheClockIsTheSystemsWhenNoneIsGiven(t *testing.T) {
	fake := idptest.New()
	defer fake.Close()
	client := &idp.Client{HTTP: &http.Client{}, RedirectURL: redirect, Endpoints: fake.Endpoints()}
	signIn := func() (*idp.Identity, error) {
		s, err := client.Begin(idp.Google, fake.App(idp.Google), state)
		if err != nil {
			t.Fatal(err)
		}
		code, _, err := fake.Approve(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		return client.Finish(context.Background(), idp.Google, fake.App(idp.Google), code, *s)
	}
	if _, err := signIn(); err != nil {
		t.Fatal(err)
	}
	fake.Now = func() time.Time { return time.Now().Add(24 * time.Hour) } // a token from tomorrow
	id, err := signIn()
	invalid(t, id, err, "ID token made in the future")
}

func TestProviders(t *testing.T) {
	if !slices.Equal(idp.Providers, []idp.Provider{"google", "github", "meta", "microsoft"}) {
		t.Fatalf("got %v", idp.Providers)
	}
}

// Many people sign in at once, with every provider.
func TestManyAtOnce(t *testing.T) {
	w := newWorld(t)
	var wg sync.WaitGroup
	for i := range 24 {
		p := idp.Providers[i%len(idp.Providers)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := w.signIn(t, p)
			if err != nil || id.Subject != "1001" {
				t.Errorf("%s: %v, %v", p, id, err)
			}
		}()
	}
	wg.Wait()
	// Everyone who waited for the keys got them from one request.
	for _, p := range []idp.Provider{idp.Google, idp.Microsoft} {
		if n := w.fake.Count(p, idptest.Keys); n != 1 {
			t.Fatalf("the keys of %s were asked for %d times", p, n)
		}
	}
}
