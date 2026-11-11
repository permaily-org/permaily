package differential

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

const (
	redirect = "https://app.permaily.com/api/v1/auth/provider/callback"
	work     = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	object   = "00000000-0000-0000-66f3-3332eca7ea81"
)

// start is the time of every case: the fake dates its tokens with it, and
// both checks are told it is now.
var start = time.Unix(1_800_000_000, 0)

var signers = []idp.Provider{idp.Google, idp.Microsoft}

type faults = idptest.Faults

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func set(name string, value any) func(map[string]any) {
	return func(m map[string]any) { m[name] = value }
}

func drop(name string) func(map[string]any) {
	return func(m map[string]any) { delete(m, name) }
}

func both(changes ...func(map[string]any)) func(map[string]any) {
	return func(m map[string]any) {
		for _, change := range changes {
			change(m)
		}
	}
}

// rawNumber is written into JSON as it is.
type rawNumber string

func (n rawNumber) MarshalJSON() ([]byte, error) { return []byte(n), nil }

// part changes one of the three parts of a finished token.
func part(i int, change func(string) string) func(string) string {
	return func(token string) string {
		parts := strings.Split(token, ".")
		parts[i] = change(parts[i])
		return strings.Join(parts, ".")
	}
}

// key changes the one key a provider publishes.
func key(change func(map[string]any)) func([]map[string]any) []map[string]any {
	return func(k []map[string]any) []map[string]any {
		change(k[0])
		return k
	}
}

func generate(bits int) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		panic(err)
	}
	return k
}

var (
	otherKey = generate(2048) // a key no provider publishes
	smallKey = generate(1024) // a key shorter than any provider's
)

func signWith(k *rsa.PrivateKey) func(string) []byte {
	return func(signed string) []byte {
		hash := sha256.Sum256([]byte(signed))
		signature, err := rsa.SignPKCS1v15(nil, k, crypto.SHA256, hash[:])
		if err != nil {
			panic(err)
		}
		return signature
	}
}

// asked is one token given to both checks.
type asked struct {
	ours    *idp.Identity
	oursErr error
	theirs  *Answer
	lib     error
}

// ask makes the token a provider gives for a person, with the faults of a
// case, and gives it to internal/idp (through a whole sign-in, as the
// service uses it) and to go-oidc. Both read the provider's keys from the
// fake, with the same faults.
func ask(t *testing.T, p idp.Provider, who idptest.Person, fault func(fake *idptest.Server) faults) asked {
	t.Helper()
	fake := idptest.New()
	t.Cleanup(fake.Close)
	fake.Now = func() time.Time { return start }
	fake.SignInAs(p, who)
	client := &idp.Client{HTTP: &http.Client{}, RedirectURL: redirect, Now: fake.Now, Endpoints: fake.Endpoints()}
	app := fake.App(p)
	started, err := client.Begin(p, app, "state")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := fake.Approve(started.URL)
	if err != nil {
		t.Fatal(err)
	}
	var f faults
	if fault != nil {
		f = fault(fake)
	}
	fake.Misbehave(p, f)
	token := fake.IDToken(p, started.Nonce)
	// From here the provider gives exactly this token, and the keys of the case.
	fake.Misbehave(p, faults{Keys: f.Keys, EditKeys: f.EditKeys, EditIDToken: func(string) string { return token }})

	var a asked
	a.ours, a.oursErr = client.Finish(context.Background(), p, app, code, *started)
	e := fake.Endpoints()[p]
	a.theirs, a.lib = Reference(context.Background(), e.Keys, e.Issuer, app.ClientID, started.Nonce, token, start)
	return a
}

var people = map[string]idptest.Person{
	"verified":            {Subject: "subject-of-ada", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace", Object: object},
	"not verified":        {Subject: "subject-of-ada", Address: "ada@example.com", Name: "Ada Lovelace", Object: object},
	"no address":          {Subject: "subject-of-ada", Name: "Ada Lovelace", Object: object},
	"no name":             {Subject: "10769150350006150715113082367", Address: "ada@example.com", Verified: true},
	"work, verified":      {Subject: "subject-of-ada", Address: "ada@contoso.com", Verified: true, Tenant: work, Object: object},
	"work, not verified":  {Subject: "subject-of-ada", Address: "ada@contoso.com", Tenant: work, Object: object},
	"no object":           {Subject: "subject-of-ada", Address: "ada@example.com", Verified: true},
	"work, no object":     {Subject: "subject-of-ada", Address: "ada@contoso.com", Verified: true, Tenant: work},
	"an address in upper": {Subject: "subject-of-ada", Address: "ADA@EXAMPLE.COM", Verified: true, Object: object},
}

// Tokens that are right: both accept, and say the same person.
func TestBothAccept(t *testing.T) {
	for _, p := range signers {
		for name, who := range people {
			t.Run(string(p)+"/"+name, func(t *testing.T) {
				a := ask(t, p, who, nil)
				if a.oursErr != nil || a.lib != nil {
					t.Fatalf("internal/idp: %v, go-oidc: %v", a.oursErr, a.lib)
				}
				if a.ours.Subject != a.theirs.Subject || a.ours.Address != strings.ToLower(a.theirs.Address) {
					t.Fatalf("internal/idp says %+v, go-oidc %+v", *a.ours, *a.theirs)
				}
			})
		}
	}
}

// ada is a person both checks let in when nothing is wrong with the token.
var ada = people["verified"]

// Tokens that are wrong, each in one way. Both must refuse, unless the case
// says why internal/idp refuses what go-oidc accepts: then exactly that must
// happen, so a reason that stops being true fails here. There is no case
// the other way round: nothing go-oidc refuses is accepted in the enclave.
func TestBroken(t *testing.T) {
	plain := func(f faults) func(*idptest.Server) faults {
		return func(*idptest.Server) faults { return f }
	}
	const (
		oneWriting  = "one token has one writing here: encoding/base64, which go-jose reads with, skips line breaks and takes any spare bits in the last character"
		strictJSON  = "what the provider signed is read with a member twice refused, so no two readers can take different values; go-oidc reads the claims with encoding/json, which takes the last"
		ownKeyRules = "go-oidc takes any key of the set whose identifier matches, whatever the set says it is for"
		wholeTimes  = "a time is a whole number of seconds written plainly, as both providers write it; go-oidc takes any JSON number"
	)
	cases := []struct {
		name  string
		fault func(fake *idptest.Server) faults
		only  idp.Provider // empty for both
		// differ is why internal/idp refuses and go-oidc accepts; empty when both refuse.
		differ string
	}{
		// How it says it is signed.
		{"no algorithm", plain(faults{EditHeader: drop("alg")}), "", ""},
		{"algorithm none, no signature", plain(faults{EditHeader: set("alg", "none"), Sign: func(string) []byte { return nil }}), "", ""},
		{"algorithm none, signed all the same", plain(faults{EditHeader: set("alg", "none")}), "", ""},
		{"HS256 with the public key as the secret", func(fake *idptest.Server) faults {
			_, k := fake.SigningKey()
			secret, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
			return faults{EditHeader: set("alg", "HS256"), Sign: func(signed string) []byte {
				mac := hmac.New(sha256.New, secret)
				mac.Write([]byte(signed))
				return mac.Sum(nil)
			}}
		}, "", ""},
		{"RS384, signed so", func(fake *idptest.Server) faults {
			_, k := fake.SigningKey()
			return faults{EditHeader: set("alg", "RS384"), Sign: func(signed string) []byte {
				hash := sha512.Sum384([]byte(signed))
				signature, _ := rsa.SignPKCS1v15(nil, k, crypto.SHA384, hash[:])
				return signature
			}}
		}, "", ""},
		{"PS256, signed so", func(fake *idptest.Server) faults {
			_, k := fake.SigningKey()
			return faults{EditHeader: set("alg", "PS256"), Sign: func(signed string) []byte {
				hash := sha256.Sum256([]byte(signed))
				signature, _ := rsa.SignPSS(rand.Reader, k, crypto.SHA256, hash[:], nil)
				return signature
			}}
		}, "", ""},
		{"algorithm rs256", plain(faults{EditHeader: set("alg", "rs256")}), "", ""},
		{"algorithm twice, none first", func(fake *idptest.Server) faults {
			kid, _ := fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"none","alg":"RS256","kid":"` + kid + `"}`)}
		}, "", ""},
		{"key identifier twice", func(fake *idptest.Server) faults {
			kid, _ := fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"RS256","kid":"another-key","kid":"` + kid + `"}`)}
		}, "", ""},
		{"header is not JSON", plain(faults{RawHeader: []byte(`{"alg":"RS256"`)}), "", ""},
		{"an access token by its type", plain(faults{EditHeader: set("typ", "at+jwt")}), "",
			"a token that says it is of another type is not an ID token; go-oidc does not read the type"},
		{"a header that must be understood", plain(faults{EditHeader: set("crit", []string{"exp"})}), "", ""},
		{"no key identifier", plain(faults{EditHeader: drop("kid")}), "",
			"a token names the key that signed it, as both providers' do; without a name go-oidc tries every key of the set"},
		{"a key nobody publishes", plain(faults{EditHeader: set("kid", "another-key")}), "", ""},

		// Its shape.
		{"two parts", plain(faults{EditIDToken: func(token string) string { return token[:strings.LastIndex(token, ".")] }}), "", ""},
		{"four parts", plain(faults{EditIDToken: func(token string) string { return token + ".AAAA" }}), "", ""},
		{"five parts", plain(faults{EditIDToken: func(token string) string { return token + ".AAAA.AAAA" }}), "", ""},
		{"not a token", plain(faults{EditIDToken: func(string) string { return "token" }}), "", ""},
		{"longer than any provider's", plain(faults{EditClaims: set("padding", strings.Repeat("a", 16<<10))}), "",
			"a token is at most sixteen thousand bytes here, several times the longest a provider sends; go-oidc sets no limit"},
		{"header not base64url", plain(faults{EditIDToken: part(0, func(s string) string { return "+" + s[1:] })}), "", ""},
		{"signature with padding", plain(faults{EditIDToken: part(2, func(s string) string { return s + "==" })}), "", ""},
		{"a line break in the header", plain(faults{EditIDToken: part(0, func(s string) string { return s[:8] + "\n" + s[8:] })}), "", oneWriting},
		{"a line break in the claims", plain(faults{EditIDToken: part(1, func(s string) string { return s[:8] + "\n" + s[8:] })}), "", oneWriting},
		{"a line break in the signature", plain(faults{EditIDToken: part(2, func(s string) string { return s[:8] + "\r\n" + s[8:] })}), "", oneWriting},
		{"signature with spare bits set", plain(faults{EditIDToken: part(2, func(s string) string {
			const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
			last := strings.IndexByte(alphabet, s[len(s)-1])
			return s[:len(s)-1] + string(alphabet[last|1])
		})}), "", oneWriting},

		// Who signed it.
		{"signed with another key under the provider's identifier", plain(faults{Sign: signWith(otherKey)}), "", ""},
		{"no signature", plain(faults{Sign: func(string) []byte { return nil }}), "", ""},
		{"signature with a byte changed", func(fake *idptest.Server) faults {
			_, k := fake.SigningKey()
			return faults{Sign: func(signed string) []byte {
				signature := signWith(k)(signed)
				signature[100] ^= 1
				return signature
			}}
		}, "", ""},
		{"claims changed after it was signed", plain(faults{EditIDToken: part(1, func(s string) string {
			raw, _ := base64.RawURLEncoding.DecodeString(s)
			return b64([]byte(strings.Replace(string(raw), "ada@example.com", "eve@example.com", 1)))
		})}), "", ""},

		// The keys.
		{"a key for RS512", plain(faults{EditKeys: key(set("alg", "RS512"))}), "", ownKeyRules},
		{"a key for PS256", plain(faults{EditKeys: key(set("alg", "PS256"))}), "", ownKeyRules},
		{"a key for encryption", plain(faults{EditKeys: key(set("use", "enc"))}), "", ownKeyRules},
		{"a key of another kind", plain(faults{EditKeys: key(set("kty", "EC"))}), "", ""},
		{"a key without a modulus", plain(faults{EditKeys: key(drop("n"))}), "", ""},
		{"a key with an even exponent", plain(faults{EditKeys: key(set("e", "AQAA"))}), "", ""},
		{"a key of 1024 bits, which signed it", plain(faults{EditKeys: key(set("n", b64(smallKey.N.Bytes()))), Sign: func(signed string) []byte {
			hash := sha256.Sum256([]byte(signed))
			signature, _ := rsa.SignPKCS1v15(nil, smallKey, crypto.SHA256, hash[:])
			return signature
		}}), "", "a provider's key is of 2048 bits at least; go-oidc takes whatever crypto/rsa does"},
		{"a key of 8192 bits", plain(faults{EditKeys: key(set("n", b64(new(big.Int).Lsh(big.NewInt(1), 8191).Bytes())))}), "", ""},
		{"two keys with one identifier, the provider's second", plain(faults{EditKeys: func(k []map[string]any) []map[string]any {
			return []map[string]any{{"kty": "RSA", "kid": k[0]["kid"], "n": b64(otherKey.N.Bytes()), "e": "AQAB"}, k[0]}
		}}), "", "which of two keys with one identifier signs would be a matter of their order, so the set is refused; go-oidc tries each"},
		{"no keys", plain(faults{EditKeys: func([]map[string]any) []map[string]any { return []map[string]any{} }}), "", ""},
		{"keys that are not JSON", plain(faults{Keys: &idptest.Answer{Body: []byte(`<html>`)}}), "", ""},
		{"keys that cannot be read", plain(faults{Keys: &idptest.Answer{Status: 503}}), "", ""},
		{"a key of Microsoft's personal accounts on a token of an organisation", plain(faults{
			EditKeys:   key(set("issuer", "https://login.microsoftonline.com/"+idptest.PersonalTenant+"/v2.0")),
			EditClaims: both(set("tid", work), set("iss", "https://login.microsoftonline.com/"+work+"/v2.0")),
		}), idp.Microsoft, "Microsoft says in its key set which keys are for the personal accounts alone, and tells applications to hold tokens to it; a key set is only keys to go-oidc"},

		// Who made it.
		{"no issuer", plain(faults{EditClaims: drop("iss")}), "", ""},
		{"another issuer", plain(faults{EditClaims: set("iss", "https://accounts.example.com")}), "", ""},
		{"Google's issuer over http", plain(faults{EditClaims: set("iss", "http://accounts.google.com")}), idp.Google, ""},
		{"the issuer of one tenant and the tenant of another", plain(faults{EditClaims: set("iss", "https://login.microsoftonline.com/"+work+"/v2.0")}), idp.Microsoft, ""},
		{"no tenant", plain(faults{EditClaims: drop("tid")}), idp.Microsoft, ""},
		{"tenant in upper case", plain(faults{EditClaims: both(
			set("tid", strings.ToUpper(idptest.PersonalTenant)),
			set("iss", "https://login.microsoftonline.com/"+strings.ToUpper(idptest.PersonalTenant)+"/v2.0"),
		)}), idp.Microsoft, "a tenant is an identifier in lower case, as Microsoft writes it, so one account has one subject; the application's own check with go-oidc compares text"},
		{"tenant by name", plain(faults{EditClaims: both(set("tid", "common"), set("iss", "https://login.microsoftonline.com/common/v2.0"))}), idp.Microsoft,
			"a tenant is an identifier, never a name; the application's own check with go-oidc compares text"},
		{"object that is no identifier", plain(faults{EditClaims: set("oid", "ada")}), idp.Microsoft,
			"an object is an identifier in lower case, since it is half of the subject; go-oidc knows nothing of it"},

		// Who it is for.
		{"no audience", plain(faults{EditClaims: drop("aud")}), "", ""},
		{"another application", plain(faults{EditClaims: both(set("aud", "another-client"), drop("azp"))}), "", ""},
		{"a list with our application alone", plain(faults{EditClaims: func(m map[string]any) { m["aud"] = []any{m["aud"]} }}), "",
			"the audience is exactly the application, as text, which is what both providers write; go-oidc takes a list that holds it"},
		{"a list with our application and another", plain(faults{EditClaims: func(m map[string]any) { m["aud"] = []any{m["aud"], "another-client"} }}), "",
			"a token made for another application too is refused; go-oidc takes a list that holds ours"},
		{"given to another application", plain(faults{EditClaims: set("azp", "another-client")}), "",
			"a token says which application it was given to, and that must be ours; go-oidc does not read it"},

		// When.
		{"no expiry", plain(faults{EditClaims: drop("exp")}), "", ""},
		{"expired a second ago", plain(faults{EditClaims: set("exp", start.Unix()-1)}), "", ""},
		{"expires this second", plain(faults{EditClaims: set("exp", start.Unix())}), "",
			"a token is good while its expiry is in the future; go-oidc still takes it in its last second"},
		{"expiry as text", plain(faults{EditClaims: set("exp", "1900000000")}), "", wholeTimes + ", and text that holds one"},
		{"expiry with a fraction", plain(faults{EditClaims: set("exp", float64(start.Add(time.Hour).Unix())+0.5)}), "", wholeTimes},
		{"expiry with an exponent", plain(faults{EditClaims: set("exp", rawNumber("1.9e9"))}), "", wholeTimes},
		{"no time of making", plain(faults{EditClaims: drop("iat")}), "",
			"a token says when it was made, as OpenID Connect requires; go-oidc does not ask for it"},
		{"made a minute and a second from now", plain(faults{EditClaims: set("iat", start.Unix()+61)}), "",
			"a token made more than a minute in the future is refused; go-oidc does not compare when a token was made"},
		{"not valid for another minute and a second", plain(faults{EditClaims: set("nbf", start.Unix()+61)}), "",
			"the provider's clock may be a minute ahead here; go-oidc gives five"},
		{"not valid for another ten minutes", plain(faults{EditClaims: set("nbf", start.Unix()+600)}), "", ""},

		// For which sign-in.
		{"no nonce", plain(faults{EditClaims: drop("nonce")}), "", ""},
		{"another nonce", plain(faults{EditClaims: set("nonce", "nonce-of-another-sign-in")}), "", ""},
		{"nonce in a list", plain(faults{EditClaims: func(m map[string]any) { m["nonce"] = []any{m["nonce"]} }}), "", ""},

		// Who it is about.
		{"no subject", plain(faults{EditClaims: drop("sub")}), "",
			"a token without a subject is about nobody; go-oidc gives an empty one"},
		{"subject as a number", plain(faults{EditClaims: set("sub", 1001)}), "", ""},
		{"subject of 256 characters", plain(faults{EditClaims: set("sub", strings.Repeat("s", 256))}), "",
			"a subject is at most 255 characters of ASCII, as OpenID Connect says; go-oidc takes any text"},
		{"subject with a space", plain(faults{EditClaims: set("sub", "subject of ada")}), "",
			"a subject is at most 255 characters of ASCII that show, with no space; go-oidc takes any text"},
		{"address not text", plain(faults{EditClaims: set("email", []string{"ada@example.com"})}), "", ""},
		{"name not text", plain(faults{EditClaims: set("name", 7)}), "", ""},

		// What is signed is read strictly too.
		{"subject twice", func(fake *idptest.Server) faults {
			return faults{EditIDToken: func(token string) string {
				return resign(fake, token, func(claims string) string { return strings.Replace(claims, `"sub":`, `"sub":"eve","sub":`, 1) })
			}}
		}, "", strictJSON},
		{"audience twice, another application first", func(fake *idptest.Server) faults {
			return faults{EditIDToken: func(token string) string {
				return resign(fake, token, func(claims string) string {
					return strings.Replace(claims, `"aud":`, `"aud":"another-client","aud":`, 1)
				})
			}}
		}, "", strictJSON},
		{"claims that are a list", plain(faults{RawClaims: []byte(`[]`)}), "", ""},
		{"claims that are not JSON", plain(faults{RawClaims: []byte(`sub=ada`)}), "", ""},
		{"claims with something after them", func(fake *idptest.Server) faults {
			return faults{EditIDToken: func(token string) string {
				return resign(fake, token, func(claims string) string { return claims + ` {}` })
			}}
		}, "", ""},
		{"claims that are not UTF-8", func(fake *idptest.Server) faults {
			return faults{EditIDToken: func(token string) string {
				return resign(fake, token, func(claims string) string { return strings.Replace(claims, "Ada Lovelace", "Ada Lovelace \xff", 1) })
			}}
		}, "", "what the provider signed is UTF-8 or refused; encoding/json, which go-oidc reads the claims with, puts a replacement character in its place"},
	}
	for _, p := range signers {
		for _, c := range cases {
			if c.only != "" && c.only != p {
				continue
			}
			t.Run(string(p)+"/"+c.name, func(t *testing.T) {
				a := ask(t, p, ada, c.fault)
				if a.oursErr == nil {
					t.Fatalf("internal/idp accepts it: %+v (go-oidc: %v)", *a.ours, a.lib)
				}
				if !idpRefused(a.oursErr) {
					t.Fatalf("internal/idp refuses with %v", a.oursErr)
				}
				switch {
				case c.differ == "" && a.lib == nil:
					t.Fatalf("go-oidc accepts what internal/idp refuses (%v), and the case names no reason: %+v", a.oursErr, *a.theirs)
				case c.differ != "" && a.lib != nil:
					t.Fatalf("go-oidc refuses it too (%v): the reason to differ no longer holds: %s", a.lib, c.differ)
				}
			})
		}
	}
}

// idpRefused says whether an error is one of the two refusals of internal/idp.
func idpRefused(err error) bool {
	return strings.HasPrefix(err.Error(), idp.ErrInvalid.Error()+": ") || strings.HasPrefix(err.Error(), idp.ErrUnreachable.Error()+": ")
}

// resign changes the claims of a token as text and signs it again with the
// provider's key, for what no map makes: a member twice.
func resign(fake *idptest.Server, token string, change func(claims string) string) string {
	parts := strings.Split(token, ".")
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	return fake.SignToken(header, []byte(change(string(claims))))
}

// What a token may say that is unusual and right: both accept.
func TestBothAcceptTheUnusual(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     idp.Provider
		fault faults
	}{
		{"Google's issuer without https", idp.Google, faults{EditClaims: set("iss", "accounts.google.com")}},
		{"no type in the header", idp.Google, faults{EditHeader: drop("typ")}},
		{"no type in the header", idp.Microsoft, faults{EditHeader: drop("typ")}},
		{"a key inside the header", idp.Google, faults{EditHeader: set("jwk", map[string]any{"kty": "RSA", "n": b64(otherKey.N.Bytes()), "e": "AQAB"})}},
		{"not given to anyone", idp.Google, faults{EditClaims: drop("azp")}},
		{"made a minute from now", idp.Google, faults{EditClaims: set("iat", start.Unix()+60)}},
		{"made a minute from now", idp.Microsoft, faults{EditClaims: both(set("iat", start.Unix()+60), set("nbf", start.Unix()+60))}},
		{"expires in a second", idp.Microsoft, faults{EditClaims: set("exp", start.Unix()+1)}},
		{"a key that names no use and no algorithm", idp.Google, faults{EditKeys: key(both(drop("use"), drop("alg")))}},
		{"other keys beside it", idp.Microsoft, faults{EditKeys: func(k []map[string]any) []map[string]any {
			return append([]map[string]any{{"kty": "RSA", "kid": "another-key", "use": "sig", "n": b64(otherKey.N.Bytes()), "e": "AQAB"}}, k...)
		}}},
		{"claims nobody reads", idp.Microsoft, faults{EditClaims: both(set("aio", "Df2UVXL1ix!lMCWMSOJBcFatzcGfvFGhjKv8q5g0x732"), set("uti", "abc"), set("groups", []string{"a", "b"}))}},
	} {
		t.Run(string(c.p)+"/"+c.name, func(t *testing.T) {
			a := ask(t, c.p, ada, func(*idptest.Server) faults { return c.fault })
			if a.oursErr != nil || a.lib != nil {
				t.Fatalf("internal/idp: %v, go-oidc: %v", a.oursErr, a.lib)
			}
			if a.ours.Subject != a.theirs.Subject || a.ours.Address != a.theirs.Address {
				t.Fatalf("internal/idp says %+v, go-oidc %+v", *a.ours, *a.theirs)
			}
		})
	}
}
