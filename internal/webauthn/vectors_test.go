package webauthn_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/internal/webauthn"
)

// vector is one file of testdata/vectors: an answer that a real authenticator
// and a real browser gave somewhere else (or that the specification, or a tool
// of the FIDO Alliance, gives as an example), with what was asked and what
// this package must say of it. The data is copied; where it comes from and
// under which licence is in the file.
type vector struct {
	Name   string `json:"name"`
	Source struct {
		Project   string `json:"project"`
		URL       string `json:"url"`
		License   string `json:"license"`
		Copyright string `json:"copyright"`
		Notice    string `json:"notice"` // the licence's text, under testdata
	} `json:"source"`
	Made          string    `json:"made"` // captured, specification or tool
	Authenticator string    `json:"authenticator"`
	AAGUID        string    `json:"aaguid"`
	Client        string    `json:"client"`
	Algorithm     string    `json:"algorithm"`
	RelyingParty  string    `json:"relyingParty"`
	Origin        string    `json:"origin"`
	Note          string    `json:"note"`
	Creation      *creation `json:"creation"`
	Use           *use      `json:"use"`
}

// verdict is what the package must answer.
//
// Expect is "accepted", or "refused" with the refusal and why the package is
// right to refuse, or the rules of the service that the answer does not meet
// through no fault of the authenticator: "no user verification", "short
// challenge", or both. Such an answer must be refused for each of those rules
// alone and accepted once they are excused, so nothing else is wrong with it.
type verdict struct {
	Expect  string `json:"expect"`
	Refusal string `json:"refusal"`
	Why     string `json:"why"`
	Synced  bool   `json:"synced"`
}

type creation struct {
	Format            string `json:"format"`
	Challenge         string `json:"challenge"`
	ClientDataJSON    string `json:"clientDataJSON"`
	AttestationObject string `json:"attestationObject"`
	verdict
}

type use struct {
	PublicKey         string `json:"publicKey"` // only when the source gives no creation
	Challenge         string `json:"challenge"`
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
	Client            string `json:"client"`
	verdict
}

// The rules an answer captured elsewhere may not meet, and what the service says of each.
var excuses = []struct {
	name, refusal string
	excused       webauthn.Excused
}{
	{"short challenge", "challenge too short", webauthn.Excused{ShortChallenge: true}},
	{"no user verification", "the person was not verified", webauthn.Excused{Unverified: true}},
}

var everything = webauthn.Excused{ShortChallenge: true, Unverified: true}

var algorithmNames = map[string]int{"EdDSA": -8, "ES256": -7, "RS256": -257}

func vectors(t *testing.T) []vector {
	t.Helper()
	files, err := filepath.Glob("testdata/vectors/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	var all []vector
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if filepath.Base(file) != v.Name+".json" {
			t.Fatalf("%s is named %q inside", file, v.Name)
		}
		all = append(all, v)
	}
	return all
}

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("not base64url: %v", err)
	}
	return b
}

// check holds an answer to its verdict. run gives the package's answer with
// some rules excused; with none it is the exported function, what the service runs.
func (v verdict) check(t *testing.T, run func(webauthn.Excused) error) {
	t.Helper()
	strict := run(webauthn.Excused{})
	switch v.Expect {
	case "accepted":
		if strict != nil {
			t.Fatalf("refused a right answer: %v", strict)
		}
		return
	case "refused":
		// Refused for this reason even when everything of the service's own is excused.
		if v.Why == "" {
			t.Fatal("a refusal with no account of why the package is right")
		}
		refused(t, run(everything), v.Refusal)
		if strict == nil {
			t.Fatal("refused only when excused")
		}
		return
	}
	if v.Refusal != "" || v.Why != "" {
		t.Fatal("a refusal stated for an answer that is not expected refused")
	}
	// Each rule named is a reason by itself, and together they are the only ones.
	named := strings.Split(v.Expect, ", ")
	var granted webauthn.Excused
	for _, e := range excuses {
		if slices.Contains(named, e.name) {
			granted.ShortChallenge = granted.ShortChallenge || e.excused.ShortChallenge
			granted.Unverified = granted.Unverified || e.excused.Unverified
		}
	}
	known := 0
	for _, e := range excuses {
		if !slices.Contains(named, e.name) {
			continue
		}
		known++
		others := granted
		if e.excused.ShortChallenge {
			others.ShortChallenge = false
		}
		if e.excused.Unverified {
			others.Unverified = false
		}
		refused(t, run(others), e.refusal)
	}
	if known != len(named) {
		t.Fatalf("expectation %q not understood", v.Expect)
	}
	if strict == nil {
		t.Fatal("the service would accept an answer that does not meet its rules")
	}
	if err := run(granted); err != nil {
		t.Fatalf("refused for more than %q: %v", v.Expect, err)
	}
}

// TestCapturedAnswers runs every answer of testdata/vectors: the answers of
// real authenticators and browsers must pass, and where one does not, the
// fixture says which rule of the service it does not meet, or why it is wrong.
func TestCapturedAnswers(t *testing.T) {
	for _, v := range vectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			if v.Source.Project == "" || !strings.HasPrefix(v.Source.URL, "https://") || v.Source.License == "" || v.Source.Copyright == "" {
				t.Fatal("a vector must say where it comes from and under which licence")
			}
			if _, err := os.Stat(filepath.Join("testdata", v.Source.Notice)); err != nil {
				t.Fatalf("the licence's text is not kept: %v", err)
			}
			if !slices.Contains([]string{"captured", "specification", "tool"}, v.Made) || v.Authenticator == "" || v.Client == "" {
				t.Fatal("a vector must say what made it")
			}
			if v.Creation == nil && v.Use == nil {
				t.Fatal("a vector with no answer")
			}
			rp := webauthn.RelyingParty{ID: v.RelyingParty, Origins: []string{v.Origin}}

			var key []byte
			if c := v.Creation; c != nil {
				challenge, clientData, attestation := unb64(t, c.Challenge), unb64(t, c.ClientDataJSON), unb64(t, c.AttestationObject)
				var made *webauthn.Credential
				c.check(t, func(e webauthn.Excused) error {
					cred, err := rp.RegisteredExcusing(e, challenge, clientData, attestation)
					if e == (webauthn.Excused{}) {
						// With nothing excused it is the exported function, exactly.
						same, sameErr := rp.Registered(challenge, clientData, attestation)
						if (err == nil) != (sameErr == nil) || (err != nil && err.Error() != sameErr.Error()) || (cred != nil && !bytes.Equal(cred.PublicKey, same.PublicKey)) {
							t.Fatalf("Registered answers %v, the same checks answer %v", sameErr, err)
						}
					}
					if err == nil {
						made = cred
					}
					return err
				})
				if c.Expect != "refused" {
					var object struct {
						Format   string `cbor:"fmt"`
						AuthData []byte `cbor:"authData"`
					}
					if err := cbor.Unmarshal(attestation, &object); err != nil || object.Format != c.Format {
						t.Fatalf("the fixture says format %q, the answer %q (%v)", c.Format, object.Format, err)
					}
					if made.Algorithm != algorithmNames[v.Algorithm] || made.Synced != c.Synced {
						t.Fatalf("got algorithm %d and synced %v, the fixture says %s and %v", made.Algorithm, made.Synced, v.Algorithm, c.Synced)
					}
					if !bytes.Contains(object.AuthData, made.PublicKey) || !bytes.Contains(object.AuthData, made.ID) {
						t.Fatal("the credential is not the answer's own bytes")
					}
					key = made.PublicKey
				}
			}

			u := v.Use
			if u == nil {
				return
			}
			if u.PublicKey != "" {
				if key != nil {
					t.Fatal("a use with a key of its own after a creation that gave one")
				}
				key = unb64(t, u.PublicKey)
			}
			if key == nil {
				t.Fatal("a use with no key to check it against")
			}
			challenge, clientData, data, signature := unb64(t, u.Challenge), unb64(t, u.ClientDataJSON), unb64(t, u.AuthenticatorData), unb64(t, u.Signature)
			var got *webauthn.Assertion
			u.check(t, func(e webauthn.Excused) error {
				assertion, err := rp.AssertedExcusing(e, challenge, key, clientData, data, signature)
				if e == (webauthn.Excused{}) {
					_, sameErr := rp.Asserted(challenge, key, clientData, data, signature)
					if (err == nil) != (sameErr == nil) || (err != nil && err.Error() != sameErr.Error()) {
						t.Fatalf("Asserted answers %v, the same checks answer %v", sameErr, err)
					}
				}
				if err == nil {
					got = assertion
				}
				return err
			})
			if u.Expect == "refused" {
				return
			}
			if got.Synced != u.Synced {
				t.Fatalf("got synced %v, the fixture says %v", got.Synced, u.Synced)
			}
			// The signature is what ties the answer to the key: with one bit of it
			// turned, the same answer is refused, and for that.
			signature[len(signature)-1] ^= 1
			_, err := rp.AssertedExcusing(everything, challenge, key, clientData, data, signature)
			refused(t, err, "wrong signature")
		})
	}
}

// TestCapturedAnswersCoverTheKinds keeps the fixtures from thinning out
// unnoticed: every algorithm the service offers and every attestation format a
// browser hands over must be there in an answer the package takes, from a real
// device, and so must a synced passkey and a use of each algorithm.
func TestCapturedAnswersCoverTheKinds(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range vectors(t) {
		if v.Made != "captured" {
			continue
		}
		if c := v.Creation; c != nil && c.Expect != "refused" {
			seen["creation "+v.Algorithm] = true
			seen["format "+c.Format] = true
			if c.Synced {
				seen["synced"] = true
			}
		}
		if u := v.Use; u != nil && u.Expect != "refused" {
			seen["use "+v.Algorithm] = true
		}
	}
	for _, want := range []string{
		"creation ES256", "creation EdDSA", "creation RS256", "use ES256", "use EdDSA", "use RS256", "synced",
		"format none", "format packed", "format fido-u2f", "format tpm", "format apple", "format android-key", "format android-safetynet",
	} {
		if !seen[want] {
			t.Errorf("no captured answer left for: %s", want)
		}
	}
}
