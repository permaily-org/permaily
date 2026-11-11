package idp_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

type faults = idptest.Faults

func body(s string) *idptest.Answer { return &idptest.Answer{Body: []byte(s)} }

// What a token endpoint may answer wrongly, for every provider.
func TestTokenAnswerRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		fault faults
		why   string
	}{
		{"not JSON", faults{Token: body(`access_token=abc&token_type=bearer`)}, "token answer is not one JSON object"},
		{"a list", faults{Token: body(`[{"access_token":"abc","token_type":"bearer"}]`)}, "token answer is not one JSON object"},
		{"nothing", faults{Token: body(``)}, "token answer is not one JSON object"},
		{"null", faults{Token: body(`null`)}, "token answer is not one JSON object"},
		{"cut short", faults{Token: body(`{"access_token":"abc","token_type":"bearer"`)}, "token answer is not one JSON object"},
		{"something after it", faults{Token: body(`{"access_token":"abc","token_type":"bearer"}{}`)}, "token answer is not one JSON object"},
		{"not UTF-8", faults{Token: body("{\"access_token\":\"abc\",\"token_type\":\"bearer\",\"x\":\"\xff\"}")}, "token answer is not one JSON object"},
		{"a member without a value", faults{Token: body(`{"access_token":}`)}, "token answer is not one JSON object"},
		{"a member that is not a name", faults{Token: body(`{1:2}`)}, "token answer is not one JSON object"},
		// The first would be for one account and the second for another.
		{"the access token twice", faults{Token: body(`{"access_token":"abc","access_token":"def","token_type":"bearer"}`)}, "token answer is not one JSON object"},
		{"the ID token twice", faults{Token: body(`{"id_token":"a.b.c","id_token":"d.e.f","token_type":"bearer"}`)}, "token answer is not one JSON object"},
		{"no kind of token", faults{EditTokenAnswer: drop("token_type")}, "token answer of another kind"},
		{"another kind of token", faults{EditTokenAnswer: set("token_type", "mac")}, "token answer of another kind"},
		{"a kind that is not text", faults{EditTokenAnswer: set("token_type", 1)}, "token answer of another kind"},
		{"an access token that is not text", faults{EditTokenAnswer: set("access_token", 12345)}, "malformed access token"},
		{"a null access token", faults{EditTokenAnswer: set("access_token", nil)}, "malformed access token"},
		{"an empty access token", faults{EditTokenAnswer: set("access_token", "")}, "malformed access token"},
		// It is written into a header.
		{"an access token with a line break", faults{EditTokenAnswer: set("access_token", "abc\r\nX-Injected: 1")}, "malformed access token"},
		{"an access token with a space", faults{EditTokenAnswer: set("access_token", "abc def")}, "malformed access token"},
		{"an access token not in ASCII", faults{EditTokenAnswer: set("access_token", "abcé")}, "malformed access token"},
		{"an access token too long", faults{EditTokenAnswer: set("access_token", strings.Repeat("a", 4097))}, "malformed access token"},
	} {
		for _, p := range idp.Providers {
			t.Run(string(p)+"/"+c.name, func(t *testing.T) {
				w := newWorld(t)
				w.fake.Misbehave(p, c.fault)
				id, err := w.signIn(t, p)
				invalid(t, id, err, c.why)
				// Nobody was asked who it is with a token like that.
				if n := w.fake.Count(p, idptest.User) + w.fake.Count(p, idptest.Keys); n != 0 {
					t.Fatalf("the provider was asked %d more times", n)
				}
			})
		}
	}

	// What each provider must give.
	for _, c := range []struct {
		name  string
		p     idp.Provider
		fault faults
		why   string
	}{
		{"github, no access token", idp.GitHub, faults{EditTokenAnswer: drop("access_token")}, "token answer without an access token"},
		{"meta, no access token", idp.Meta, faults{EditTokenAnswer: drop("access_token")}, "token answer without an access token"},
		{"github, an ID token and no access token", idp.GitHub, faults{EditTokenAnswer: both(drop("access_token"), set("id_token", "a.b.c"))}, "token answer without an access token"},
		{"google, no ID token", idp.Google, faults{EditTokenAnswer: drop("id_token")}, "token answer without an ID token"},
		{"microsoft, no ID token", idp.Microsoft, faults{EditTokenAnswer: drop("id_token")}, "token answer without an ID token"},
		{"google, an ID token that is not text", idp.Google, faults{EditTokenAnswer: set("id_token", map[string]any{"sub": "ada"})}, "malformed ID token"},
		{"google, an empty ID token", idp.Google, faults{EditTokenAnswer: set("id_token", "")}, "malformed ID token"},
		{"microsoft, a null ID token", idp.Microsoft, faults{EditTokenAnswer: set("id_token", nil)}, "malformed ID token"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(c.p, c.fault)
			id, err := w.signIn(t, c.p)
			invalid(t, id, err, c.why)
		})
	}
}

// A provider that says no inside a success, as GitHub does for a wrong code.
func TestTokenAnswerWithAnError(t *testing.T) {
	for _, p := range idp.Providers {
		for name, answer := range map[string]string{
			"as GitHub writes it": `{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired.","error_uri":"https://docs.github.com/"}`,
			"with a token beside": `{"error":"slow_down","access_token":"abc","id_token":"a.b.c","token_type":"bearer"}`,
			"as Meta writes it":   `{"error":{"message":"This authorization code has been used.","type":"OAuthException","code":100}}`,
			"a null error":        `{"error":null,"access_token":"abc","token_type":"bearer"}`,
		} {
			t.Run(string(p)+"/"+name, func(t *testing.T) {
				w := newWorld(t)
				w.fake.Misbehave(p, faults{Token: body(answer)})
				id, err := w.signIn(t, p)
				unreachable(t, id, err, "the token endpoint answered with an error")
			})
		}
	}
}

// An answer may hold more than is read: a provider adds members over time.
func TestTokenAnswerWithMore(t *testing.T) {
	for _, p := range idp.Providers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(p, faults{EditTokenAnswer: both(
				set("refresh_token", "refresh"), set("expires_in", "3600"), set("ext_expires_in", 3600),
				set("token_type", "BEARER"), set("scope", []string{"openid"}), set("refresh_token_expires_in", nil),
			)})
			if _, err := w.signIn(t, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubUserRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		fault faults
		why   string
	}{
		{"not JSON", faults{User: body(`<html>`)}, "user is not one JSON object"},
		{"a list", faults{User: body(`[{"id":7}]`)}, "user is not one JSON object"},
		{"the identifier twice", faults{User: body(`{"id":7,"id":8}`)}, "user is not one JSON object"},
		{"not UTF-8", faults{User: body("{\"id\":7,\"name\":\"\xff\"}")}, "user is not one JSON object"},
		{"no identifier", faults{EditUser: drop("id")}, "user without a numeric identifier"},
		{"a null identifier", faults{EditUser: set("id", nil)}, "user without a numeric identifier"},
		// GitHub writes a number; text would be another account's login.
		{"an identifier as text", faults{EditUser: set("id", "583231")}, "user without a numeric identifier"},
		{"a login as the identifier", faults{EditUser: set("id", "octocat")}, "user without a numeric identifier"},
		{"an identifier of zero", faults{EditUser: set("id", 0)}, "user without a numeric identifier"},
		{"a negative identifier", faults{EditUser: set("id", -7)}, "user without a numeric identifier"},
		{"an identifier with a fraction", faults{EditUser: set("id", 7.5)}, "user without a numeric identifier"},
		{"an identifier with an exponent", faults{EditUser: set("id", rawNumber("7e3"))}, "user without a numeric identifier"},
		{"an identifier beyond what is exact", faults{EditUser: set("id", rawNumber("9007199254740993000"))}, "user without a numeric identifier"},
		{"an identifier in a list", faults{EditUser: set("id", []int{7})}, "user without a numeric identifier"},
		{"a name that is not text", faults{EditUser: set("name", 7)}, "user with a name that is not text"},
		{"a name in a list", faults{EditUser: set("name", []string{"Ada"})}, "user with a name that is not text"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(idp.GitHub, c.fault)
			id, err := w.signIn(t, idp.GitHub)
			invalid(t, id, err, c.why)
			// Whose addresses would they be?
			if n := w.fake.Count(idp.GitHub, idptest.Emails); n != 0 {
				t.Fatal("the addresses were asked for")
			}
		})
	}
}

func TestGitHubEmailsRefused(t *testing.T) {
	entry := func(email string, primary, verified any) map[string]any {
		return map[string]any{"email": email, "primary": primary, "verified": verified, "visibility": "private"}
	}
	only := func(entries ...map[string]any) faults {
		return faults{EditEmails: func([]map[string]any) []map[string]any { return entries }}
	}
	many := make([]map[string]any, idp.GitHubPage+1)
	for i := range many {
		many[i] = entry("ada+"+strconv.Itoa(i)+"@example.com", i == 0, true)
	}
	for _, c := range []struct {
		name  string
		fault faults
		why   string
	}{
		{"not JSON", faults{Emails: body(`Not Found`)}, "addresses are not one JSON array of a page"},
		{"an object", faults{Emails: body(`{"email":"ada@example.com","primary":true,"verified":true}`)}, "addresses are not one JSON array of a page"},
		{"a refusal inside a success", faults{Emails: body(`{"message":"Not Found"}`)}, "addresses are not one JSON array of a page"},
		{"cut short", faults{Emails: body(`[{"email":"ada@example.com","primary":true,"verified":true}`)}, "addresses are not one JSON array of a page"},
		{"something after it", faults{Emails: body(`[] []`)}, "addresses are not one JSON array of a page"},
		{"a bracket too many", faults{Emails: body(`[]]`)}, "addresses are not one JSON array of a page"},
		{"not UTF-8", faults{Emails: body("[\"\xff\"]")}, "addresses are not one JSON array of a page"},
		{"an item that is not JSON", faults{Emails: body(`[{"email":}]`)}, "addresses are not one JSON array of a page"},
		{"more than a page", only(many...), "addresses are not one JSON array of a page"},
		{"an address that is text alone", faults{Emails: body(`["ada@example.com"]`)}, "an address is not one JSON object"},
		{"a member twice", faults{Emails: body(`[{"email":"ada@example.com","primary":false,"primary":true,"verified":true}]`)}, "an address is not one JSON object"},
		{"no address", only(map[string]any{"primary": true, "verified": true}), "an address without its three members"},
		{"an address that is not text", only(entry("", true, true), map[string]any{"email": 7, "primary": false, "verified": true}), "an address without its three members"},
		{"a null address", only(map[string]any{"email": nil, "primary": true, "verified": true}), "an address without its three members"},
		{"no primary", only(map[string]any{"email": "ada@example.com", "verified": true}), "an address without its three members"},
		{"no verified", only(map[string]any{"email": "ada@example.com", "primary": true}), "an address without its three members"},
		// Text that says true is not GitHub's true.
		{"primary as text", only(entry("ada@example.com", "true", true)), "an address without its three members"},
		{"verified as text", only(entry("ada@example.com", true, "true")), "an address without its three members"},
		{"verified as a number", only(entry("ada@example.com", true, 1)), "an address without its three members"},
		{"verified as null", only(entry("ada@example.com", true, nil)), "an address without its three members"},
		// Which of the two would be a matter of their order.
		{"two primary addresses", only(entry("ada@example.com", true, true), entry("eve@example.com", true, true)), "more than one primary address"},
		{"two primary addresses, one not verified", only(entry("eve@example.com", true, false), entry("ada@example.com", true, true)), "more than one primary address"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(idp.GitHub, c.fault)
			id, err := w.signIn(t, idp.GitHub)
			invalid(t, id, err, c.why)
		})
	}
}

// Which address GitHub's answers give, when they are not refused.
func TestGitHubAddress(t *testing.T) {
	entry := func(email string, primary, verified bool) map[string]any {
		return map[string]any{"email": email, "primary": primary, "verified": verified, "visibility": nil}
	}
	only := func(entries ...map[string]any) faults {
		return faults{EditEmails: func([]map[string]any) []map[string]any { return entries }}
	}
	page := make([]map[string]any, idp.GitHubPage)
	for i := range page {
		page[i] = entry("ada+"+strconv.Itoa(i)+"@example.com", i == idp.GitHubPage-1, true)
	}
	for _, c := range []struct {
		name  string
		fault faults
		want  string
	}{
		{"primary and verified", only(entry("ada@example.com", true, true)), "ada@example.com"},
		{"primary and verified, last of many", only(entry("a@example.com", false, true), entry("b@example.com", false, false), entry("ada@example.com", true, true)), "ada@example.com"},
		{"primary and verified, last of a page", only(page...), "ada+99@example.com"},
		{"in upper case", only(entry("Ada@Example.COM", true, true)), "ada@example.com"},
		{"primary and not verified", only(entry("ada@example.com", true, false)), ""},
		// A verified address that is not the primary one is not where GitHub writes to the person.
		{"verified and not primary", only(entry("ada@example.com", false, true)), ""},
		{"primary not verified, another verified", only(entry("ceo@victim.example", true, false), entry("ada@example.com", false, true)), ""},
		{"no addresses", faults{EditEmails: func([]map[string]any) []map[string]any { return []map[string]any{} }}, ""},
		{"a primary address that is not one", only(entry("Ada <ada@example.com>", true, true)), ""},
		{"an empty primary address", only(entry("", true, true)), ""},
		{"a primary address not in ASCII", only(entry("adá@example.com", true, true)), ""},
		// The address on the profile is whatever the person typed there.
		{"an address on the profile and none verified", faults{
			EditUser: set("email", "ceo@victim.example"),
			EditEmails: func([]map[string]any) []map[string]any {
				return []map[string]any{entry("ceo@victim.example", false, false)}
			},
		}, ""},
		{"members that are not read", faults{EditEmails: func(e []map[string]any) []map[string]any {
			return []map[string]any{{"email": "ada@example.com", "primary": true, "verified": true, "visibility": 7, "more": []int{1}}}
		}}, "ada@example.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.SignInAs(idp.GitHub, idptest.Person{Subject: "583231", Address: "unused@example.com", Verified: true, Name: "Ada"})
			w.fake.Misbehave(idp.GitHub, c.fault)
			got, err := w.signIn(t, idp.GitHub)
			if err != nil {
				t.Fatal(err)
			}
			want := idp.Identity{Provider: idp.GitHub, Subject: "583231", Address: c.want, Verified: c.want != "", Name: "Ada"}
			if *got != want {
				t.Fatalf("got %+v, want %+v", *got, want)
			}
		})
	}
}

func TestGitHubName(t *testing.T) {
	for name, c := range map[string]struct {
		given any
		want  string
	}{
		"null":             {nil, ""},
		"empty":            {"", ""},
		"spaces around it": {"  Ada Lovelace\t", "Ada Lovelace"},
		"not in ASCII":     {"Ada Lovelace 艾达", "Ada Lovelace 艾达"},
		"two lines":        {"Ada\nLovelace", ""},
		"a control":        {"Ada\x1b[31m", ""},
		"as long as kept":  {strings.Repeat("a", idp.MaxName), strings.Repeat("a", idp.MaxName)},
		"too long":         {strings.Repeat("a", idp.MaxName+1), ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(idp.GitHub, faults{EditUser: set("name", c.given)})
			got, err := w.signIn(t, idp.GitHub)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != c.want {
				t.Fatalf("got %q, want %q", got.Name, c.want)
			}
		})
	}
}

func TestGitHubWithoutItsScope(t *testing.T) {
	// The fake, like GitHub, gives no addresses to a token without user:email:
	// a client that asked for less would not get this far.
	w := newWorld(t)
	s, err := w.client.Begin(idp.GitHub, w.fake.App(idp.GitHub), state)
	if err != nil {
		t.Fatal(err)
	}
	less := strings.Replace(s.URL, "user%3Aemail", "user%3Afollow", 1)
	if less == s.URL {
		t.Fatalf("no user:email in %s", s.URL)
	}
	code, _, err := w.fake.Approve(less)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.client.Finish(t.Context(), idp.GitHub, w.fake.App(idp.GitHub), code, *s)
	unreachable(t, id, err, "the addresses endpoint answered HTTP 404")
}

func TestMetaUserRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		fault faults
		why   string
	}{
		{"not JSON", faults{User: body(`<html>`)}, "user is not one JSON object"},
		{"a list", faults{User: body(`[{"id":"7"}]`)}, "user is not one JSON object"},
		{"the identifier twice", faults{User: body(`{"id":"7","id":"8"}`)}, "user is not one JSON object"},
		{"the address twice", faults{User: body(`{"id":"7","email":"ada@example.com","email":"eve@example.com"}`)}, "user is not one JSON object"},
		{"no identifier", faults{EditUser: drop("id")}, "user without a numeric identifier"},
		// Meta writes text; a number that large would not be exact.
		{"an identifier as a number", faults{EditUser: set("id", 10158123456789012)}, "user without a numeric identifier"},
		{"an empty identifier", faults{EditUser: set("id", "")}, "user without a numeric identifier"},
		{"an identifier that is not digits", faults{EditUser: set("id", "ada.lovelace")}, "user without a numeric identifier"},
		{"an identifier with a sign", faults{EditUser: set("id", "-7")}, "user without a numeric identifier"},
		{"an identifier with a space", faults{EditUser: set("id", "7 ")}, "user without a numeric identifier"},
		// Two writings of one number would be two accounts.
		{"an identifier with a zero in front", faults{EditUser: set("id", "07")}, "user without a numeric identifier"},
		{"an identifier too long", faults{EditUser: set("id", strings.Repeat("7", 33))}, "user without a numeric identifier"},
		{"an identifier in other digits", faults{EditUser: set("id", "٧٧٧")}, "user without a numeric identifier"},
		{"a null identifier", faults{EditUser: set("id", nil)}, "user without a numeric identifier"},
		{"a name that is not text", faults{EditUser: set("name", 7)}, "user with a name that is not text"},
		{"an address that is not text", faults{EditUser: set("email", []string{"ada@example.com"})}, "user with an address that is not text"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.Misbehave(idp.Meta, c.fault)
			id, err := w.signIn(t, idp.Meta)
			invalid(t, id, err, c.why)
		})
	}

	t.Run("an error inside a success", func(t *testing.T) {
		w := newWorld(t)
		w.fake.Misbehave(idp.Meta, faults{User: body(`{"error":{"message":"Invalid OAuth access token.","type":"OAuthException","code":190},"id":"7"}`)})
		id, err := w.signIn(t, idp.Meta)
		unreachable(t, id, err, "the user endpoint answered with an error")
	})
}

// Meta says nothing of whether an address is verified: whatever its answer
// holds, the address is never taken as verified.
func TestMetaNeverVerifies(t *testing.T) {
	for name, c := range map[string]struct {
		fault faults
		want  idp.Identity
	}{
		"as it comes": {faults{}, idp.Identity{Subject: "10158123456789012", Address: "ada@example.com", Name: "Ada Lovelace"}},
		"it says verified": {faults{EditUser: both(set("verified", true), set("email_verified", true), set("is_verified", true))},
			idp.Identity{Subject: "10158123456789012", Address: "ada@example.com", Name: "Ada Lovelace"}},
		"a null address":        {faults{EditUser: set("email", nil)}, idp.Identity{Subject: "10158123456789012", Name: "Ada Lovelace"}},
		"an address in escapes": {faults{User: body(`{"id":"10158123456789012","name":"Ada Lovelace","email":"ada@example.com"}`)}, idp.Identity{Subject: "10158123456789012", Address: "ada@example.com", Name: "Ada Lovelace"}},
		"an address that is not one": {faults{EditUser: set("email", "ada@example.com, eve@example.com")},
			idp.Identity{Subject: "10158123456789012", Name: "Ada Lovelace"}},
		"a null name":     {faults{EditUser: set("name", nil)}, idp.Identity{Subject: "10158123456789012", Address: "ada@example.com"}},
		"more than asked": {faults{EditUser: set("picture", map[string]any{"data": map[string]any{"url": "https://example.com/a.jpg"}})}, idp.Identity{Subject: "10158123456789012", Address: "ada@example.com", Name: "Ada Lovelace"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.SignInAs(idp.Meta, idptest.Person{Subject: "10158123456789012", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"})
			w.fake.Misbehave(idp.Meta, c.fault)
			got, err := w.signIn(t, idp.Meta)
			if err != nil {
				t.Fatal(err)
			}
			c.want.Provider = idp.Meta
			if *got != c.want {
				t.Fatalf("got %+v, want %+v", *got, c.want)
			}
		})
	}
}

func TestMetaWithoutItsScope(t *testing.T) {
	// Without the email permission Meta gives no address, as the fake does.
	w := newWorld(t)
	s, err := w.client.Begin(idp.Meta, w.fake.App(idp.Meta), state)
	if err != nil {
		t.Fatal(err)
	}
	less := strings.Replace(s.URL, "scope=email+public_profile", "scope=public_profile", 1)
	if less == s.URL {
		t.Fatalf("no email permission in %s", s.URL)
	}
	code, _, err := w.fake.Approve(less)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.client.Finish(t.Context(), idp.Meta, w.fake.App(idp.Meta), code, *s)
	if err != nil || id.Address != "" || id.Subject != "1001" {
		t.Fatalf("got %+v, %v", id, err)
	}
}

// The parsers themselves, on what no fake needs to send.
func TestParsers(t *testing.T) {
	if access, id, err := idp.ParseTokenAnswer([]byte(`{"token_type":"Bearer","access_token":"abc","id_token":"a.b.c"}`)); err != nil || access != "abc" || id != "a.b.c" {
		t.Fatalf("got %q, %q, %v", access, id, err)
	}
	if access, id, err := idp.ParseTokenAnswer([]byte(`{"token_type":"bearer"}`)); err != nil || access != "" || id != "" {
		t.Fatalf("got %q, %q, %v", access, id, err)
	}
	if id, err := idp.ParseGitHubUser([]byte(`{"id":999999999999999}`)); err != nil || id.Subject != "999999999999999" {
		t.Fatalf("got %+v, %v", id, err)
	}
	if id, err := idp.ParseGitHubUser([]byte(`{"id":1000000000000000}`)); err == nil {
		t.Fatalf("an identifier of sixteen digits was read: %+v", id)
	}
	if id, err := idp.ParseGitHubUser([]byte(`{"id":007}`)); err == nil {
		t.Fatalf("an identifier with zeros in front was read: %+v", id)
	}
	if id, err := idp.ParseMetaUser([]byte(`{"id":"` + strings.Repeat("7", 32) + `"}`)); err != nil || id.Subject != strings.Repeat("7", 32) || id.Verified {
		t.Fatalf("got %+v, %v", id, err)
	}
	if got, err := idp.ParseGitHubEmails([]byte(` [ ] `)); err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	usable, kept, err := idp.UsableKeys([]byte(`{"keys":[{"kty":"EC","kid":"a"},{"kty":"RSA"}]}`))
	if err != nil || len(usable) != 0 || kept != 1 {
		t.Fatalf("got %v, %d, %v", usable, kept, err)
	}
}
