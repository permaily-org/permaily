package idp_test

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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

// signers are the two providers that sign who the person is.
var signers = []idp.Provider{idp.Google, idp.Microsoft}

// ada is a person every check below would let in if the token were right.
var ada = idptest.Person{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace", Object: object}

// otherKey is a key no provider publishes.
var otherKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

func signWith(key *rsa.PrivateKey) func(string) []byte {
	return func(signed string) []byte {
		hash := sha256.Sum256([]byte(signed))
		signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, hash[:])
		if err != nil {
			panic(err)
		}
		return signature
	}
}

func set(name string, value any) func(map[string]any) {
	return func(m map[string]any) { m[name] = value }
}

func drop(name string) func(map[string]any) {
	return func(m map[string]any) { delete(m, name) }
}

// part replaces one of the three parts of a finished token.
func part(i int, change func(string) string) func(string) string {
	return func(token string) string {
		parts := strings.Split(token, ".")
		parts[i] = change(parts[i])
		return strings.Join(parts, ".")
	}
}

// Every way a token can be wrong, on a token that is right in every other
// way: signed by the provider's key unless the case is about the signature.
func TestIDTokenRefused(t *testing.T) {
	in := time.Hour // the fake makes tokens that last an hour
	cases := []struct {
		name  string
		fault func(w *world) faults
		why   string
		only  idp.Provider // empty for both
	}{
		// How it says it is signed.
		{"no algorithm", func(*world) faults { return faults{EditHeader: drop("alg")} }, "ID token signed with another algorithm", ""},
		{"algorithm none, no signature", func(*world) faults {
			return faults{EditHeader: set("alg", "none"), Sign: func(string) []byte { return nil }}
		}, "ID token signed with another algorithm", ""},
		{"algorithm none, signed all the same", func(*world) faults { return faults{EditHeader: set("alg", "none")} }, "ID token signed with another algorithm", ""},
		{"algorithm None", func(*world) faults { return faults{EditHeader: set("alg", "None")} }, "ID token signed with another algorithm", ""},
		{"algorithm rs256", func(*world) faults { return faults{EditHeader: set("alg", "rs256")} }, "ID token signed with another algorithm", ""},
		{"algorithm not text", func(*world) faults { return faults{EditHeader: set("alg", 256)} }, "ID token signed with another algorithm", ""},
		{"algorithm in a list", func(*world) faults { return faults{EditHeader: set("alg", []string{"RS256"})} }, "ID token signed with another algorithm", ""},
		// The provider's public key is public: with HS256 anyone could "sign" with it.
		{"HS256 with the public key as the secret", func(w *world) faults {
			_, key := w.fake.SigningKey()
			secret, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
			return faults{EditHeader: set("alg", "HS256"), Sign: func(signed string) []byte {
				mac := hmac.New(sha256.New, secret)
				mac.Write([]byte(signed))
				return mac.Sum(nil)
			}}
		}, "ID token signed with another algorithm", ""},
		{"RS384, signed so", func(w *world) faults {
			_, key := w.fake.SigningKey()
			return faults{EditHeader: set("alg", "RS384"), Sign: func(signed string) []byte {
				hash := sha512.Sum384([]byte(signed))
				signature, _ := rsa.SignPKCS1v15(nil, key, crypto.SHA384, hash[:])
				return signature
			}}
		}, "ID token signed with another algorithm", ""},
		{"PS256, signed so", func(w *world) faults {
			_, key := w.fake.SigningKey()
			return faults{EditHeader: set("alg", "PS256"), Sign: func(signed string) []byte {
				hash := sha256.Sum256([]byte(signed))
				signature, _ := rsa.SignPSS(rand.Reader, key, crypto.SHA256, hash[:], nil)
				return signature
			}}
		}, "ID token signed with another algorithm", ""},
		{"ES256", func(*world) faults { return faults{EditHeader: set("alg", "ES256")} }, "ID token signed with another algorithm", ""},
		{"an access token", func(*world) faults { return faults{EditHeader: set("typ", "at+jwt")} }, "a token of another type", ""},
		{"type not text", func(*world) faults { return faults{EditHeader: set("typ", 1)} }, "a token of another type", ""},
		{"a header that must be understood", func(*world) faults { return faults{EditHeader: set("crit", []string{"exp"})} }, "ID token with a header that must be understood", ""},
		{"no key identifier", func(*world) faults { return faults{EditHeader: drop("kid")} }, "ID token without a key identifier", ""},
		{"empty key identifier", func(*world) faults { return faults{EditHeader: set("kid", "")} }, "ID token without a key identifier", ""},
		{"key identifier not text", func(*world) faults { return faults{EditHeader: set("kid", 7)} }, "ID token without a key identifier", ""},
		{"key identifier with a space", func(*world) faults { return faults{EditHeader: set("kid", "idptest key")} }, "ID token without a key identifier", ""},
		{"key identifier too long", func(*world) faults { return faults{EditHeader: set("kid", strings.Repeat("k", 257))} }, "ID token without a key identifier", ""},
		{"a key nobody publishes", func(*world) faults { return faults{EditHeader: set("kid", "another-key")} }, "ID token signed with a key the provider does not publish", ""},
		// The first algorithm would be the one a lax reader takes, or the last.
		{"algorithm twice", func(w *world) faults {
			kid, _ := w.fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"none","alg":"RS256","kid":"` + kid + `"}`)}
		}, "ID token header is not one JSON object", ""},
		{"key identifier twice", func(w *world) faults {
			kid, _ := w.fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"RS256","kid":"another-key","kid":"` + kid + `"}`)}
		}, "ID token header is not one JSON object", ""},
		{"header is a list", func(*world) faults { return faults{RawHeader: []byte(`["RS256"]`)} }, "ID token header is not one JSON object", ""},
		{"header is not JSON", func(*world) faults { return faults{RawHeader: []byte(`{"alg":"RS256"`)} }, "ID token header is not one JSON object", ""},
		{"header with something after it", func(w *world) faults {
			kid, _ := w.fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"RS256","kid":"` + kid + `"}{}`)}
		}, "ID token header is not one JSON object", ""},
		{"header is not UTF-8", func(w *world) faults {
			kid, _ := w.fake.SigningKey()
			return faults{RawHeader: []byte(`{"alg":"RS256","kid":"` + kid + `","x":"` + "\xff" + `"}`)}
		}, "ID token header is not one JSON object", ""},

		// Its shape.
		{"two parts", func(*world) faults {
			return faults{EditIDToken: func(token string) string { return token[:strings.LastIndex(token, ".")] }}
		}, "ID token is not three parts", ""},
		{"four parts", func(*world) faults {
			return faults{EditIDToken: func(token string) string { return token + ".AAAA" }}
		}, "ID token is not three parts", ""},
		{"five parts, as an encrypted token has", func(*world) faults {
			return faults{EditIDToken: func(token string) string { return token + ".AAAA.AAAA" }}
		}, "ID token is not three parts", ""},
		{"not a token", func(*world) faults {
			return faults{EditIDToken: func(string) string { return "token" }}
		}, "ID token is not three parts", ""},
		{"too long", func(*world) faults {
			return faults{EditClaims: set("padding", strings.Repeat("a", idp.MaxIDToken))}
		}, "ID token too long", ""},
		{"header not base64url", func(*world) faults {
			return faults{EditIDToken: part(0, func(s string) string { return "+" + s[1:] })}
		}, "ID token header is not base64url", ""},
		{"header with padding", func(*world) faults {
			return faults{EditIDToken: part(0, func(s string) string { return s + "=" })}
		}, "ID token header is not base64url", ""},
		{"claims not base64url", func(*world) faults {
			return faults{EditIDToken: part(1, func(s string) string { return s[:5] + "/" + s[6:] })}
		}, "ID token claims are not base64url", ""},
		// encoding/base64 skips line breaks: the same token could be written in many ways.
		{"a line break in the claims", func(*world) faults {
			return faults{EditIDToken: part(1, func(s string) string { return s[:8] + "\n" + s[8:] })}
		}, "ID token claims are not base64url", ""},
		{"a line break in the signature", func(*world) faults {
			return faults{EditIDToken: part(2, func(s string) string { return s[:8] + "\r\n" + s[8:] })}
		}, "ID token signature is not base64url", ""},
		{"signature with padding", func(*world) faults {
			return faults{EditIDToken: part(2, func(s string) string { return s + "==" })}
		}, "ID token signature is not base64url", ""},
		// A signature of 256 bytes ends in a character of which only two bits
		// count; with the others set it would be another writing of the same token.
		{"signature with spare bits set", func(*world) faults {
			return faults{EditIDToken: part(2, func(s string) string {
				const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
				last := strings.IndexByte(alphabet, s[len(s)-1])
				return s[:len(s)-1] + string(alphabet[last|1])
			})}
		}, "ID token signature is not base64url", ""},

		// Who signed it.
		{"signed with another key under the provider's identifier", func(*world) faults {
			return faults{Sign: signWith(otherKey)}
		}, "wrong signature", ""},
		{"no signature", func(*world) faults {
			return faults{Sign: func(string) []byte { return nil }}
		}, "wrong signature", ""},
		{"signature cut short", func(w *world) faults {
			_, key := w.fake.SigningKey()
			return faults{Sign: func(signed string) []byte { return signWith(key)(signed)[:255] }}
		}, "wrong signature", ""},
		{"signature with a byte changed", func(w *world) faults {
			_, key := w.fake.SigningKey()
			return faults{Sign: func(signed string) []byte {
				signature := signWith(key)(signed)
				signature[100] ^= 1
				return signature
			}}
		}, "wrong signature", ""},
		{"signature of another token", func(w *world) faults {
			_, key := w.fake.SigningKey()
			return faults{Sign: func(string) []byte { return signWith(key)("another.token") }}
		}, "wrong signature", ""},
		{"claims changed after it was signed", func(*world) faults {
			return faults{EditIDToken: part(1, func(s string) string {
				raw, _ := base64.RawURLEncoding.DecodeString(s)
				return b64([]byte(strings.Replace(string(raw), "ada@example.com", "eve@example.com", 1)))
			})}
		}, "wrong signature", ""},
		{"header changed after it was signed", func(*world) faults {
			return faults{EditIDToken: part(0, func(s string) string {
				raw, _ := base64.RawURLEncoding.DecodeString(s)
				return b64([]byte(strings.Replace(string(raw), `{`, `{"x":1,`, 1)))
			})}
		}, "wrong signature", ""},

		// The header and the key must agree.
		{"a key for RS512", func(*world) faults { return faults{EditKeys: keys(set("alg", "RS512"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key for PS256", func(*world) faults { return faults{EditKeys: keys(set("alg", "PS256"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key whose algorithm is not text", func(*world) faults { return faults{EditKeys: keys(set("alg", 256))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key for encryption", func(*world) faults { return faults{EditKeys: keys(set("use", "enc"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key whose use is not text", func(*world) faults { return faults{EditKeys: keys(set("use", true))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key of another kind", func(*world) faults { return faults{EditKeys: keys(set("kty", "EC"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key of no kind", func(*world) faults { return faults{EditKeys: keys(drop("kty"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key without a modulus", func(*world) faults { return faults{EditKeys: keys(drop("n"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key whose modulus is not text", func(*world) faults { return faults{EditKeys: keys(set("n", 7))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key whose modulus is not base64url", func(*world) faults { return faults{EditKeys: keys(set("n", "+/+/"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key with an empty modulus", func(*world) faults { return faults{EditKeys: keys(set("n", ""))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key of 8192 bits", func(*world) faults {
			return faults{EditKeys: keys(set("n", b64(new(big.Int).Lsh(big.NewInt(1), 8191).Bytes())))}
		}, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key of 4104 bits", func(*world) faults {
			return faults{EditKeys: keys(set("n", b64(new(big.Int).Lsh(big.NewInt(1), 4103).Bytes())))}
		}, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key of 1024 bits, which signed it", func(*world) faults {
			return faults{EditKeys: keys(set("n", b64(smallKey.N.Bytes()))), Sign: func(signed string) []byte {
				hash := sha256.Sum256([]byte(signed))
				signature, _ := rsa.SignPKCS1v15(nil, smallKey, crypto.SHA256, hash[:])
				return signature
			}}
		}, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key without an exponent", func(*world) faults { return faults{EditKeys: keys(drop("e"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key with the exponent one", func(*world) faults { return faults{EditKeys: keys(set("e", "AQ"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key with an even exponent", func(*world) faults { return faults{EditKeys: keys(set("e", "AQAA"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key with an exponent too large", func(*world) faults { return faults{EditKeys: keys(set("e", "gAAAAQ"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key with an exponent of five bytes", func(*world) faults { return faults{EditKeys: keys(set("e", "AQAAAAAB"))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key whose issuer is not text", func(*world) faults { return faults{EditKeys: keys(set("issuer", 7))} }, "ID token signed with a key that is not for RS256 signatures", ""},
		{"a key without an identifier", func(*world) faults { return faults{EditKeys: keys(drop("kid"))} }, "ID token signed with a key the provider does not publish", ""},
		{"a key whose identifier is not text", func(*world) faults { return faults{EditKeys: keys(set("kid", 7))} }, "ID token signed with a key the provider does not publish", ""},
		{"no keys", func(*world) faults {
			return faults{EditKeys: func([]map[string]any) []map[string]any { return []map[string]any{} }}
		}, "ID token signed with a key the provider does not publish", ""},
		// Which of the two signs would be a matter of their order.
		{"two keys with one identifier", func(*world) faults {
			return faults{EditKeys: func(k []map[string]any) []map[string]any {
				return []map[string]any{{"kty": "RSA", "kid": k[0]["kid"], "n": b64(otherKey.N.Bytes()), "e": "AQAB"}, k[0]}
			}}
		}, "two keys with one identifier", ""},
		{"too many keys", func(*world) faults {
			return faults{EditKeys: func(k []map[string]any) []map[string]any {
				for i := range idp.MaxKeys {
					k = append(k, map[string]any{"kty": "EC", "kid": "key-" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
				}
				return k
			}}
		}, "keys without a list, or with one too long", ""},
		{"keys that are not JSON", func(*world) faults { return faults{Keys: &idptest.Answer{Body: []byte(`<html>`)}} }, "keys are not one JSON object", ""},
		{"keys that are a list", func(*world) faults { return faults{Keys: &idptest.Answer{Body: []byte(`[]`)}} }, "keys are not one JSON object", ""},
		{"keys twice", func(*world) faults {
			return faults{Keys: &idptest.Answer{Body: []byte(`{"keys":[],"keys":[]}`)}}
		}, "keys are not one JSON object", ""},
		{"keys without a list", func(*world) faults { return faults{Keys: &idptest.Answer{Body: []byte(`{}`)}} }, "keys without a list, or with one too long", ""},
		{"keys that are an object", func(*world) faults { return faults{Keys: &idptest.Answer{Body: []byte(`{"keys":{}}`)}} }, "keys without a list, or with one too long", ""},
		{"keys with something after the list", func(*world) faults {
			return faults{Keys: &idptest.Answer{Body: []byte(`{"keys":[] ]}`)}}
		}, "keys are not one JSON object", ""},
		{"a key that is not an object", func(*world) faults { return faults{Keys: &idptest.Answer{Body: []byte(`{"keys":["RSA"]}`)}} }, "a key is not one JSON object", ""},
		{"a key with a member twice", func(*world) faults {
			return faults{Keys: &idptest.Answer{Body: []byte(`{"keys":[{"kid":"a","kid":"b"}]}`)}}
		}, "a key is not one JSON object", ""},
		{"a key of Microsoft's personal accounts on a token of an organisation", func(*world) faults {
			return faults{EditKeys: keys(set("issuer", "https://login.microsoftonline.com/"+idptest.PersonalTenant+"/v2.0")),
				EditClaims: both(set("tid", work), set("iss", "https://login.microsoftonline.com/"+work+"/v2.0"))}
		}, "ID token signed with a key of another issuer", idp.Microsoft},
		{"a key of another cloud", func(*world) faults {
			return faults{EditKeys: keys(set("issuer", "https://login.microsoftonline.us/{tenantid}/v2.0"))}
		}, "ID token signed with a key of another issuer", idp.Microsoft},

		// Who made it.
		{"no issuer", func(*world) faults { return faults{EditClaims: drop("iss")} }, "ID token without an issuer", ""},
		{"empty issuer", func(*world) faults { return faults{EditClaims: set("iss", "")} }, "ID token without an issuer", ""},
		{"issuer not text", func(*world) faults { return faults{EditClaims: set("iss", []string{idptest.GoogleIssuer})} }, "ID token without an issuer", ""},
		{"another issuer", func(*world) faults { return faults{EditClaims: set("iss", "https://accounts.example.com")} }, "ID token of another issuer", ""},
		{"the other provider's issuer", func(*world) faults { return faults{EditClaims: set("iss", idptest.GoogleIssuer)} }, "ID token of another issuer", idp.Microsoft},
		{"the other provider's issuer, with a tenant", func(*world) faults {
			return faults{EditClaims: both(set("iss", "https://login.microsoftonline.com/"+work+"/v2.0"), set("tid", work))}
		}, "ID token of another issuer", idp.Google},
		{"Google's issuer over http", func(*world) faults { return faults{EditClaims: set("iss", "http://accounts.google.com")} }, "ID token of another issuer", idp.Google},
		{"Google's issuer with a slash", func(*world) faults { return faults{EditClaims: set("iss", "https://accounts.google.com/")} }, "ID token of another issuer", idp.Google},
		{"Microsoft's issuer without https", func(*world) faults {
			return faults{EditClaims: set("iss", "login.microsoftonline.com/"+idptest.PersonalTenant+"/v2.0")}
		}, "ID token of another issuer", idp.Microsoft},
		{"Microsoft's issuer of version 1", func(*world) faults {
			return faults{EditClaims: set("iss", "https://sts.windows.net/"+idptest.PersonalTenant+"/")}
		}, "ID token of another issuer", idp.Microsoft},
		// A token of an organisation that says it is of personal accounts would be believed about its address.
		{"the issuer of one tenant and the tenant of another", func(*world) faults {
			return faults{EditClaims: set("iss", "https://login.microsoftonline.com/"+work+"/v2.0")}
		}, "ID token of another issuer", idp.Microsoft},
		{"the placeholder as the issuer", func(*world) faults { return faults{EditClaims: set("iss", idptest.MicrosoftIssuer)} }, "ID token of another issuer", idp.Microsoft},
		{"no tenant", func(*world) faults { return faults{EditClaims: drop("tid")} }, "ID token without a tenant", idp.Microsoft},
		{"tenant not text", func(*world) faults { return faults{EditClaims: set("tid", 9188040)} }, "ID token without a tenant", idp.Microsoft},
		{"tenant by name", func(*world) faults {
			return faults{EditClaims: both(set("tid", "common"), set("iss", "https://login.microsoftonline.com/common/v2.0"))}
		}, "ID token without a tenant", idp.Microsoft},
		{"tenant in upper case", func(*world) faults {
			upper := strings.ToUpper(idptest.PersonalTenant)
			return faults{EditClaims: both(set("tid", upper), set("iss", "https://login.microsoftonline.com/"+upper+"/v2.0"))}
		}, "ID token without a tenant", idp.Microsoft},
		{"tenant with a brace", func(*world) faults {
			return faults{EditClaims: both(set("tid", "{tenantid}"), set("iss", idptest.MicrosoftIssuer))}
		}, "ID token without a tenant", idp.Microsoft},
		{"tenant without its dashes", func(*world) faults {
			return faults{EditClaims: set("tid", "9188040d06c6704c5b0b112036a304b66dad")}
		}, "ID token without a tenant", idp.Microsoft},

		// Who it is for.
		{"no audience", func(*world) faults { return faults{EditClaims: drop("aud")} }, "ID token for another application", ""},
		{"another application", func(*world) faults { return faults{EditClaims: both(set("aud", "another-client"), drop("azp"))} }, "ID token for another application", ""},
		{"an audience that begins like ours", func(*world) faults {
			return faults{EditClaims: both(set("aud", "client-of-google-2"), drop("azp"))}
		}, "ID token for another application", ""},
		{"a list with our application alone", func(*world) faults {
			return faults{EditClaims: func(m map[string]any) { m["aud"] = []any{m["aud"]} }}
		}, "ID token for another application", ""},
		{"a list with our application and another", func(*world) faults {
			return faults{EditClaims: func(m map[string]any) { m["aud"] = []any{m["aud"], "another-client"} }}
		}, "ID token for another application", ""},
		{"given to another application", func(*world) faults { return faults{EditClaims: set("azp", "another-client")} }, "ID token given to another application", ""},
		{"given to something that is not text", func(*world) faults { return faults{EditClaims: set("azp", 7)} }, "ID token given to another application", ""},

		// When.
		{"no expiry", func(*world) faults { return faults{EditClaims: drop("exp")} }, "ID token expired", ""},
		{"expired a second ago", func(*world) faults { return faults{EditClaims: set("exp", start.Unix()-1)} }, "ID token expired", ""},
		{"expires this second", func(*world) faults { return faults{EditClaims: set("exp", start.Unix())} }, "ID token expired", ""},
		{"expired long ago", func(*world) faults { return faults{EditClaims: set("exp", 1)} }, "ID token expired", ""},
		{"expiry of zero", func(*world) faults { return faults{EditClaims: set("exp", 0)} }, "ID token expired", ""},
		{"expiry as text", func(*world) faults { return faults{EditClaims: set("exp", "1900000000")} }, "ID token expired", ""},
		{"expiry with a fraction", func(*world) faults { return faults{EditClaims: set("exp", float64(start.Add(in).Unix())+0.5)} }, "ID token expired", ""},
		{"negative expiry", func(*world) faults { return faults{EditClaims: set("exp", -1)} }, "ID token expired", ""},
		{"expiry beyond what is exact", func(*world) faults { return faults{EditClaims: set("exp", int64(1e15))} }, "ID token expired", ""},
		{"expiry with an exponent", func(*world) faults { return faults{EditClaims: set("exp", rawNumber("1.9e9"))} }, "ID token expired", ""},
		{"expiry with a sign", func(*world) faults { return faults{EditClaims: set("exp", rawNumber("-0"))} }, "ID token expired", ""},
		{"no time of making", func(*world) faults { return faults{EditClaims: drop("iat")} }, "ID token made in the future", ""},
		{"made before 1970", func(*world) faults { return faults{EditClaims: set("iat", -1)} }, "ID token made in the future", ""},
		{"made at a time with a fraction", func(*world) faults { return faults{EditClaims: set("iat", float64(start.Unix())-0.5)} }, "ID token made in the future", ""},
		{"valid from before 1970", func(*world) faults { return faults{EditClaims: set("nbf", -1)} }, "ID token not valid yet", ""},
		{"made as text", func(*world) faults { return faults{EditClaims: set("iat", "now")} }, "ID token made in the future", ""},
		{"made a minute and a second from now", func(*world) faults { return faults{EditClaims: set("iat", start.Unix()+61)} }, "ID token made in the future", ""},
		{"made tomorrow", func(*world) faults {
			return faults{EditClaims: both(set("iat", start.Unix()+86400), set("exp", start.Unix()+90000))}
		}, "ID token made in the future", ""},
		{"not valid for another minute and a second", func(*world) faults { return faults{EditClaims: set("nbf", start.Unix()+61)} }, "ID token not valid yet", ""},
		{"not valid before something that is not a time", func(*world) faults { return faults{EditClaims: set("nbf", "soon")} }, "ID token not valid yet", ""},

		// For which sign-in.
		{"no nonce", func(*world) faults { return faults{EditClaims: drop("nonce")} }, "ID token of another sign-in", ""},
		{"another nonce", func(*world) faults { return faults{EditClaims: set("nonce", "nonce-of-another-sign-in")} }, "ID token of another sign-in", ""},
		{"empty nonce", func(*world) faults { return faults{EditClaims: set("nonce", "")} }, "ID token of another sign-in", ""},
		{"nonce cut short", func(*world) faults {
			return faults{EditClaims: func(m map[string]any) { m["nonce"] = m["nonce"].(string)[:42] }}
		}, "ID token of another sign-in", ""},
		{"nonce with more after it", func(*world) faults {
			return faults{EditClaims: func(m map[string]any) { m["nonce"] = m["nonce"].(string) + "A" }}
		}, "ID token of another sign-in", ""},
		{"nonce in a list", func(*world) faults {
			return faults{EditClaims: func(m map[string]any) { m["nonce"] = []any{m["nonce"]} }}
		}, "ID token of another sign-in", ""},

		// Who it is about.
		{"no subject", func(*world) faults { return faults{EditClaims: drop("sub")} }, "ID token without a subject", ""},
		{"empty subject", func(*world) faults { return faults{EditClaims: set("sub", "")} }, "ID token without a subject", ""},
		{"subject as a number", func(*world) faults { return faults{EditClaims: set("sub", 1001)} }, "ID token without a subject", ""},
		{"null subject", func(*world) faults { return faults{EditClaims: set("sub", nil)} }, "ID token without a subject", ""},
		{"subject of 256 characters", func(*world) faults { return faults{EditClaims: set("sub", strings.Repeat("s", 256))} }, "ID token without a subject", ""},
		{"subject with a space", func(*world) faults { return faults{EditClaims: set("sub", "subject of ada")} }, "ID token without a subject", ""},
		{"subject with a line break", func(*world) faults { return faults{EditClaims: set("sub", "ada\n")} }, "ID token without a subject", ""},
		{"subject not in ASCII", func(*world) faults { return faults{EditClaims: set("sub", "adá")} }, "ID token without a subject", ""},
		{"address not text", func(*world) faults { return faults{EditClaims: set("email", []string{"ada@example.com"})} }, "ID token with an address that is not text", ""},
		{"address as a number", func(*world) faults { return faults{EditClaims: set("email", 7)} }, "ID token with an address that is not text", ""},
		{"name not text", func(*world) faults { return faults{EditClaims: set("name", map[string]any{"first": "Ada"})} }, "ID token with a name that is not text", ""},
		{"object not text", func(*world) faults { return faults{EditClaims: set("oid", 7)} }, "ID token with an object that is not an identifier", idp.Microsoft},
		{"object that is no identifier", func(*world) faults { return faults{EditClaims: set("oid", "ada")} }, "ID token with an object that is not an identifier", idp.Microsoft},
		{"object in upper case", func(*world) faults { return faults{EditClaims: set("oid", strings.ToUpper(work))} }, "ID token with an object that is not an identifier", idp.Microsoft},
		{"null object", func(*world) faults { return faults{EditClaims: set("oid", nil)} }, "ID token with an object that is not an identifier", idp.Microsoft},

		// What is signed is read strictly too.
		{"subject twice", func(w *world) faults {
			return faults{EditTokenAnswer: func(m map[string]any) {
				m["id_token"] = resign(w, m["id_token"].(string), func(claims string) string {
					return strings.Replace(claims, `"sub":`, `"sub":"eve","sub":`, 1)
				})
			}}
		}, "ID token claims are not one JSON object", ""},
		{"address twice", func(w *world) faults {
			return faults{EditTokenAnswer: func(m map[string]any) {
				m["id_token"] = resign(w, m["id_token"].(string), func(claims string) string {
					return strings.Replace(claims, `"email":`, `"email":"eve@example.com","email":`, 1)
				})
			}}
		}, "ID token claims are not one JSON object", ""},
		{"claims that are a list", func(*world) faults { return faults{RawClaims: []byte(`[]`)} }, "ID token claims are not one JSON object", ""},
		{"claims that are not JSON", func(*world) faults { return faults{RawClaims: []byte(`sub=ada`)} }, "ID token claims are not one JSON object", ""},
		{"claims cut short", func(*world) faults { return faults{RawClaims: []byte(`{"sub":"ada"`)} }, "ID token claims are not one JSON object", ""},
		{"claims with something after them", func(*world) faults { return faults{RawClaims: []byte(`{} {}`)} }, "ID token claims are not one JSON object", ""},
		{"claims that are not UTF-8", func(*world) faults { return faults{RawClaims: []byte("{\"sub\":\"ad\xe1\"}")} }, "ID token claims are not one JSON object", ""},
		{"empty claims", func(*world) faults { return faults{RawClaims: []byte{}} }, "ID token claims are not one JSON object", ""},
	}
	for _, p := range signers {
		for _, c := range cases {
			if c.only != "" && c.only != p {
				continue
			}
			t.Run(string(p)+"/"+c.name, func(t *testing.T) {
				w := newWorld(t)
				w.fake.SignInAs(p, ada)
				// The same sign-in is let in when nothing is wrong: what
				// follows is refused for the fault and for nothing else.
				if _, err := w.signIn(t, p); err != nil {
					t.Fatalf("refused with nothing wrong: %v", err)
				}
				w.forget()
				w.fake.Misbehave(p, c.fault(w))
				id, err := w.signIn(t, p)
				invalid(t, id, err, c.why)
			})
		}
	}
}

// smallKey is a key shorter than any provider's.
var smallKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	return key
}()

// rawNumber is written into JSON as it is.
type rawNumber string

func (n rawNumber) MarshalJSON() ([]byte, error) { return []byte(n), nil }

func both(changes ...func(map[string]any)) func(map[string]any) {
	return func(m map[string]any) {
		for _, change := range changes {
			change(m)
		}
	}
}

// keys changes the one key a provider publishes.
func keys(change func(map[string]any)) func([]map[string]any) []map[string]any {
	return func(k []map[string]any) []map[string]any {
		change(k[0])
		return k
	}
}

// resign changes the claims of a token as text and signs it again with the
// provider's key, for what no map makes: a member twice.
func resign(w *world, token string, change func(claims string) string) string {
	parts := strings.Split(token, ".")
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	return w.fake.SignToken(header, []byte(change(string(claims))))
}

// What a token may say without being refused, and what is made of it.
func TestIDTokenAccepted(t *testing.T) {
	personal := "https://login.microsoftonline.com/" + idptest.PersonalTenant + "/v2.0"
	verified := idp.Identity{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}
	unverified := verified
	unverified.Verified = false
	nobody := idp.Identity{Subject: "subject-of-ada", Name: "Ada Lovelace"}
	for _, c := range []struct {
		name  string
		p     idp.Provider
		who   func(p *idptest.Person)
		fault faults
		want  idp.Identity
	}{
		{"google, as it comes", idp.Google, nil, faults{}, verified},
		{"google, issuer without https", idp.Google, nil, faults{EditClaims: set("iss", "accounts.google.com")}, verified},
		{"google, verified as text", idp.Google, nil, faults{EditClaims: set("email_verified", "true")}, verified},
		{"google, not verified", idp.Google, nil, faults{EditClaims: set("email_verified", false)}, unverified},
		{"google, not verified as text", idp.Google, nil, faults{EditClaims: set("email_verified", "false")}, unverified},
		{"google, verified says nothing", idp.Google, nil, faults{EditClaims: drop("email_verified")}, unverified},
		{"google, verified as a number", idp.Google, nil, faults{EditClaims: set("email_verified", 1)}, unverified},
		{"google, verified as other text", idp.Google, nil, faults{EditClaims: set("email_verified", "TRUE")}, unverified},
		{"google, verified in a list", idp.Google, nil, faults{EditClaims: set("email_verified", []bool{true})}, unverified},
		{"google, verified as null", idp.Google, nil, faults{EditClaims: set("email_verified", nil)}, unverified},
		// What is not plainly one address is no address, verified or not.
		{"google, an address with a name", idp.Google, nil, faults{EditClaims: set("email", "Ada <ada@example.com>")}, nobody},
		{"google, two addresses", idp.Google, nil, faults{EditClaims: set("email", "ada@example.com,eve@example.com")}, nobody},
		{"google, an address not in ASCII", idp.Google, nil, faults{EditClaims: set("email", "adá@example.com")}, nobody},
		{"google, an empty address", idp.Google, nil, faults{EditClaims: set("email", "")}, nobody},
		{"google, a null address", idp.Google, nil, faults{EditClaims: set("email", nil)}, nobody},
		{"google, no address but verified", idp.Google, nil, faults{EditClaims: drop("email")}, nobody},
		{"google, a null name", idp.Google, nil, faults{EditClaims: set("name", nil)},
			idp.Identity{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true}},
		{"google, a name of two lines", idp.Google, nil, faults{EditClaims: set("name", "Ada\nLovelace")},
			idp.Identity{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true}},
		{"google, a name too long", idp.Google, nil, faults{EditClaims: set("name", strings.Repeat("a", idp.MaxName+1))},
			idp.Identity{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true}},
		{"google, a name not in ASCII", idp.Google, nil, faults{EditClaims: set("name", "Ada Lovelace 艾达")},
			idp.Identity{Subject: "subject-of-ada", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace 艾达"}},
		{"google, no type in the header", idp.Google, nil, faults{EditHeader: drop("typ")}, verified},
		{"google, the type in lower case", idp.Google, nil, faults{EditHeader: set("typ", "jwt")}, verified},
		{"google, not given to anyone", idp.Google, nil, faults{EditClaims: drop("azp")}, verified},
		{"google, a subject of 255 characters", idp.Google, nil, faults{EditClaims: set("sub", strings.Repeat("9", 255))},
			idp.Identity{Subject: strings.Repeat("9", 255), Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"google, made a minute from now", idp.Google, nil, faults{EditClaims: set("iat", start.Unix()+60)}, verified},
		{"google, valid from a minute from now", idp.Google, nil, faults{EditClaims: set("nbf", start.Unix()+60)}, verified},
		{"google, expires in a second", idp.Google, nil, faults{EditClaims: set("exp", start.Unix()+1)}, verified},
		{"google, made long ago", idp.Google, nil, faults{EditClaims: set("iat", 1)}, verified},
		// A key the token carries is not the provider's: it is not looked at.
		{"google, a key inside the header", idp.Google, nil, faults{EditHeader: set("jwk", map[string]any{"kty": "RSA", "n": b64(otherKey.N.Bytes()), "e": "AQAB"})}, verified},
		// Microsoft's tenant claim means nothing in a token of Google.
		{"google, with a tenant", idp.Google, nil, faults{EditClaims: both(set("tid", idptest.PersonalTenant), set("xms_edov", true), set("email_verified", false))}, unverified},
		{"google, with an object", idp.Google, nil, faults{EditClaims: set("oid", object)}, verified},
		{"google, a key with other members", idp.Google, nil, faults{EditKeys: keys(both(set("x5c", []string{"MIIC"}), set("issuer", nil), set("key_ops", []string{"verify"})))}, verified},
		{"google, a key that names no use and no algorithm", idp.Google, nil, faults{EditKeys: keys(both(drop("use"), drop("alg")))}, verified},
		{"google, other keys beside it", idp.Google, nil, faults{EditKeys: func(k []map[string]any) []map[string]any {
			return append([]map[string]any{
				{"kty": "EC", "kid": "an-elliptic-key", "crv": "P-256", "x": "AA", "y": "AA"},
				{"kty": "RSA", "n": b64(otherKey.N.Bytes()), "e": "AQAB"},
				{"kty": "RSA", "kid": "another-key", "n": b64(otherKey.N.Bytes()), "e": "AQAB"},
			}, k...)
		}}, verified},
		{"google, a modulus with a zero in front", idp.Google, nil, faults{EditKeys: func(k []map[string]any) []map[string]any {
			raw, _ := base64.RawURLEncoding.DecodeString(k[0]["n"].(string))
			k[0]["n"] = b64(append([]byte{0}, raw...))
			return k
		}}, verified},

		{"microsoft, personal account", idp.Microsoft, nil, faults{},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, personal account that says its domain was not verified", idp.Microsoft, nil, faults{EditClaims: set("xms_edov", false)},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, personal account, a key of personal accounts", idp.Microsoft, nil, faults{EditKeys: keys(set("issuer", personal))},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, a key that names no issuer", idp.Microsoft, nil, faults{EditKeys: keys(drop("issuer"))},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, personal account without an address", idp.Microsoft, func(p *idptest.Person) { p.Address = "" }, faults{},
			idp.Identity{Subject: idptest.PersonalTenant + "." + object, Name: "Ada Lovelace"}},
		{"microsoft, work account, domain verified", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, work account, domain verified as text", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: set("xms_edov", "true")},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Verified: true, Name: "Ada Lovelace"}},
		{"microsoft, work account, domain not verified", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: set("xms_edov", false)},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Name: "Ada Lovelace"}},
		{"microsoft, work account that says nothing of its domain", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: drop("xms_edov")},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Name: "Ada Lovelace"}},
		{"microsoft, work account, domain verified as a number", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: set("xms_edov", 1)},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Name: "Ada Lovelace"}},
		// Google's claim means nothing in a token of Microsoft.
		{"microsoft, work account with Google's claim", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: both(drop("xms_edov"), set("email_verified", true))},
			idp.Identity{Subject: work + "." + object, Address: "ada@example.com", Name: "Ada Lovelace"}},
		// The name an account shows is not its address.
		{"microsoft, work account, an address only as its user name", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: both(drop("email"), set("preferred_username", "ceo@victim.example"))},
			idp.Identity{Subject: work + "." + object, Name: "Ada Lovelace"}},
		{"microsoft, work account, domain verified and an address that is not one", idp.Microsoft, func(p *idptest.Person) { p.Tenant = work }, faults{EditClaims: set("email", "ada")},
			idp.Identity{Subject: work + "." + object, Name: "Ada Lovelace"}},
		{"microsoft, no object", idp.Microsoft, func(p *idptest.Person) { p.Object = "" }, faults{}, verified},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			who := ada
			if c.who != nil {
				c.who(&who)
			}
			w.fake.SignInAs(c.p, who)
			w.fake.Misbehave(c.p, c.fault)
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

// A token is good for the application it was made for and no other, even
// one of the same provider that the same person signed into.
func TestIDTokenOfAnotherSignIn(t *testing.T) {
	for _, p := range signers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			w.fake.SignInAs(p, ada)
			// A token the provider made for an earlier sign-in, given for this one.
			earlier := w.fake.IDToken(p, "nonce-of-an-earlier-sign-in")
			w.fake.Misbehave(p, idptest.Faults{EditIDToken: func(string) string { return earlier }})
			id, err := w.signIn(t, p)
			invalid(t, id, err, "ID token of another sign-in")

			// The clock moves past the token's hour.
			w.fake.Misbehave(p, idptest.Faults{})
			code, started := w.begin(t, p)
			token := w.fake.IDToken(p, started.Nonce)
			w.fake.Misbehave(p, idptest.Faults{EditIDToken: func(string) string { return token }})
			w.advance(time.Hour)
			id, err = w.client.Finish(context.Background(), p, w.fake.App(p), code, started)
			invalid(t, id, err, "ID token expired")
		})
	}
}

func TestKeysAreKeptAndAskedForAgain(t *testing.T) {
	for _, p := range signers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			asked := func(want int, when string) {
				t.Helper()
				if got := w.fake.Count(p, idptest.Keys); got != want {
					t.Fatalf("%s: the keys were asked for %d times, want %d", when, got, want)
				}
			}
			ok := func(when string) {
				t.Helper()
				if _, err := w.signIn(t, p); err != nil {
					t.Fatalf("%s: %v", when, err)
				}
			}
			ok("first")
			ok("second")
			asked(1, "kept for the second sign-in")

			// Kept for a bounded time.
			w.advance(idp.KeysLife - time.Second)
			ok("just before they are old")
			asked(1, "just before they are old")
			w.advance(time.Second)
			ok("old")
			asked(2, "old")

			// The provider changes its key: asked again, once.
			w.advance(idp.KeysWait)
			w.fake.Rotate()
			ok("after the provider changed its key")
			asked(3, "after the provider changed its key")
			ok("with the new key")
			asked(3, "with the new key")

			// A key nobody publishes makes it ask once, and not again for a minute.
			w.advance(idp.KeysWait)
			w.fake.Misbehave(p, idptest.Faults{EditHeader: set("kid", "a-key-nobody-publishes")})
			for range 5 {
				id, err := w.signIn(t, p)
				invalid(t, id, err, "ID token signed with a key the provider does not publish")
			}
			asked(4, "five tokens with an unknown key")
			w.advance(idp.KeysWait - time.Second)
			id, err := w.signIn(t, p)
			invalid(t, id, err, "ID token signed with a key the provider does not publish")
			asked(4, "a second before the minute")
			w.advance(time.Second)
			id, err = w.signIn(t, p)
			invalid(t, id, err, "ID token signed with a key the provider does not publish")
			asked(5, "after the minute")
			// Meanwhile a right token is let in with the keys that are kept.
			w.fake.Misbehave(p, idptest.Faults{})
			ok("a right token")
			asked(5, "a right token")
		})
	}
}

// The provider changed its key less than a minute after the keys were read:
// the sign-ins with the new key wait for the minute, and then go through.
func TestKeysChangedRightAfterTheyWereRead(t *testing.T) {
	w := newWorld(t)
	if _, err := w.signIn(t, idp.Google); err != nil {
		t.Fatal(err)
	}
	w.fake.Rotate()
	id, err := w.signIn(t, idp.Google)
	invalid(t, id, err, "ID token signed with a key the provider does not publish")
	w.advance(idp.KeysWait)
	if _, err := w.signIn(t, idp.Google); err != nil {
		t.Fatal(err)
	}
	// The key that was taken away signs nothing any more.
	old := idptest.Faults{EditHeader: set("kid", "idptest-key-0"), Sign: signWith(otherKey)}
	w.fake.Misbehave(idp.Google, old)
	id, err = w.signIn(t, idp.Google)
	invalid(t, id, err, "ID token signed with a key the provider does not publish")
}

func TestKeysThatCannotBeRead(t *testing.T) {
	for _, p := range signers {
		t.Run(string(p), func(t *testing.T) {
			w := newWorld(t)
			down := idptest.Faults{Keys: &idptest.Answer{Status: 503}}
			w.fake.Misbehave(p, down)
			id, err := w.signIn(t, p)
			unreachable(t, id, err, "the keys endpoint answered HTTP 503")
			// Not asked again at once, even though there are none.
			id, err = w.signIn(t, p)
			unreachable(t, id, err, "the keys were asked for a moment ago and not read")
			if n := w.fake.Count(p, idptest.Keys); n != 1 {
				t.Fatalf("asked %d times", n)
			}
			w.advance(idp.KeysWait)
			w.fake.Misbehave(p, idptest.Faults{})
			if _, err := w.signIn(t, p); err != nil {
				t.Fatal(err)
			}

			// Keys that were read are not believed past their time because the
			// provider cannot be asked.
			w.advance(idp.KeysLife)
			w.fake.Misbehave(p, down)
			id, err = w.signIn(t, p)
			unreachable(t, id, err, "the keys endpoint answered HTTP 503")
			id, err = w.signIn(t, p)
			unreachable(t, id, err, "the keys were asked for a moment ago and not read")

			// Keys that cannot be parsed replace nothing.
			w.advance(idp.KeysWait)
			w.fake.Misbehave(p, idptest.Faults{Keys: &idptest.Answer{Body: []byte(`{"keys":[`)}})
			id, err = w.signIn(t, p)
			invalid(t, id, err, "keys are not one JSON object")
			w.advance(idp.KeysWait)
			w.fake.Misbehave(p, idptest.Faults{})
			if _, err := w.signIn(t, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A person who goes away while the keys are being read must not make the
// next one wait a minute.
func TestKeysAskedByAPersonWhoWentAway(t *testing.T) {
	w := newWorld(t)
	w.fake.Misbehave(idp.Google, idptest.Faults{Keys: &idptest.Answer{Hang: true}})
	code, started := w.begin(t, idp.Google)
	ctx := w.goneOnce(idp.Google, idptest.Keys)
	id, err := w.client.Finish(ctx, idp.Google, w.fake.App(idp.Google), code, started)
	unreachable(t, id, err, "no answer from the keys endpoint: context canceled")
	w.fake.Misbehave(idp.Google, idptest.Faults{})
	if _, err := w.signIn(t, idp.Google); err != nil {
		t.Fatalf("the next sign-in: %v", err)
	}
}

// Dev mode can set the clock back: the keys are then asked for again, never
// kept for ever or never asked.
func TestKeysWhenTheClockGoesBack(t *testing.T) {
	w := newWorld(t)
	if _, err := w.signIn(t, idp.Microsoft); err != nil {
		t.Fatal(err)
	}
	w.advance(-2 * time.Hour)
	if _, err := w.signIn(t, idp.Microsoft); err != nil {
		t.Fatal(err)
	}
	if n := w.fake.Count(idp.Microsoft, idptest.Keys); n != 2 {
		t.Fatalf("the keys were asked for %d times, want 2", n)
	}
}

// The keys of one provider sign nothing of the other.
func TestKeysOfOneProviderAreNotTheOthers(t *testing.T) {
	w := newWorld(t)
	for _, p := range signers {
		if _, err := w.signIn(t, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range signers {
		if n := w.fake.Count(p, idptest.Keys); n != 1 {
			t.Fatalf("the keys of %s were asked for %d times", p, n)
		}
	}
}

// An application with no identifier, or a sign-in with no nonce, must never
// match a token that has none either.
func TestNothingNeverMatchesNothing(t *testing.T) {
	claims := func(aud, nonce string) []byte {
		return []byte(`{"iss":"https://accounts.google.com","aud":"` + aud + `","sub":"ada","nonce":"` + nonce +
			`","iat":` + strconv.FormatInt(start.Unix(), 10) + `,"exp":` + strconv.FormatInt(start.Unix()+3600, 10) + `}`)
	}
	if _, err := idp.Claims(idp.Google, idptest.GoogleIssuer, "the-client", "the-nonce", claims("the-client", "the-nonce"), start); err != nil {
		t.Fatalf("refused with nothing wrong: %v", err)
	}
	id, err := idp.Claims(idp.Google, idptest.GoogleIssuer, "", "the-nonce", claims("", "the-nonce"), start)
	invalid(t, id, err, "ID token for another application")
	id, err = idp.Claims(idp.Google, idptest.GoogleIssuer, "the-client", "", claims("the-client", ""), start)
	invalid(t, id, err, "ID token of another sign-in")
}
