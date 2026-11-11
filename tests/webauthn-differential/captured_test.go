package differential_test

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/internal/webauthn"

	differential "github.com/permaily-org/permaily/tests/webauthn-differential"
)

// captured is what this module reads of a file of internal/webauthn/testdata/vectors:
// an answer a real authenticator gave somewhere else, and what the package's own tests hold it to.
type captured struct {
	Name         string `json:"name"`
	RelyingParty string `json:"relyingParty"`
	Origin       string `json:"origin"`
	Creation     *struct {
		Challenge         string `json:"challenge"`
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
		Expect            string `json:"expect"`
	} `json:"creation"`
	Use *struct {
		PublicKey         string `json:"publicKey"`
		Challenge         string `json:"challenge"`
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		Expect            string `json:"expect"`
	} `json:"use"`
}

// Where the two differ on a captured answer, by the fixture's name. Anything not here must agree.
const (
	shortChallenge   = "the relying party of the capture asked with a challenge of 9 bytes; this package takes none under 16, the library has no least length"
	credentialInUse  = "the authenticator data of a use carries a credential (AT), which WebAuthn 6.1 forbids; the library does not look"
	tokenBindingText = "the client data has tokenBinding as a string, as a browser of 2018 wrote; WebAuthn Level 3 reserves the member and no longer has it checked, so this package does not read it, and the library refuses what is not an object there"
)

var (
	// The exported functions against the library asked the same.
	capturedDiffer = map[string]verdict{
		"yubico-yubikey-5-nano creation":                       stricter(shortChallenge),
		"yubico-yubikey-5-nano use":                            stricter(shortChallenge),
		"go-webauthn-chrome-on-mac-2018-credential-in-use use": stricter(credentialInUse),
	}
	// The fixture's own verdict (is anything wrong beyond the service's own
	// rules) against the library asked without user verification.
	fixtureDiffers = map[string]string{
		"go-webauthn-chrome-on-mac-2018-credential-in-use use": credentialInUse,
		"py-webauthn-yubikey-4-u2f-hashalgorithm creation":     tokenBindingText,
	}
)

func un(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// attestedIn reads the authenticator data, the identifier and the key out of an attestation object by hand.
func attestedIn(t *testing.T, attestation []byte) (authData, id, key []byte) {
	t.Helper()
	var object struct {
		AuthData []byte `cbor:"authData"`
	}
	if err := cbor.Unmarshal(attestation, &object); err != nil || len(object.AuthData) < 55 {
		t.Fatalf("attestation object: %v", err)
	}
	n := int(binary.BigEndian.Uint16(object.AuthData[53:55]))
	if len(object.AuthData) < 55+n {
		t.Fatal("credential identifier longer than the authenticator data")
	}
	rest := object.AuthData[55+n:]
	var item cbor.RawMessage
	after, err := cbor.UnmarshalFirst(rest, &item)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return object.AuthData, object.AuthData[55 : 55+n], rest[:len(rest)-len(after)]
}

// TestCapturedAnswers asks the library about every answer captured from a real
// authenticator that the package's own tests run.
//
// The package never reads an attestation statement and the library checks it,
// certificates and their dates included, so a creation the package takes is
// given to the library with the statement taken out (format "none", the same
// authenticator data): the comparison is then of what both do, and does not
// change the day a certificate of 2019 runs out.
//
// Two things are held. The exported functions and the library, asked the same,
// agree. And the library asked without user verification accepts exactly the
// answers the fixtures say have nothing wrong beyond the service's own rules,
// which is what the package's tests hold the package to: so those verdicts are
// not the package's word alone.
func TestCapturedAnswers(t *testing.T) {
	files, err := filepath.Glob("../../internal/webauthn/testdata/vectors/*.json")
	if err != nil || len(files) < 50 {
		t.Fatalf("the captured answers are not where they were: %d files, %v", len(files), err)
	}
	seen := map[string]bool{}
	hold := func(t *testing.T, name, expect string, ours, theirs, unverified error) {
		t.Helper()
		seen[name] = true
		v := accepted
		if ours != nil {
			v = refused
		}
		if differs, ok := capturedDiffer[name]; ok {
			v = differs
		}
		v.check(t, ours, theirs)
		nothingWrong := expect != "refused"
		if why, differs := fixtureDiffers[name]; differs {
			nothingWrong = !nothingWrong
			t.Logf("the library and the fixture differ on purpose: %s", why)
		}
		if (unverified == nil) != nothingWrong {
			t.Fatalf("the fixture expects %q; the library without user verification: %v", expect, unverified)
		}
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var v captured
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		t.Run(v.Name, func(t *testing.T) {
			rp := webauthn.RelyingParty{ID: v.RelyingParty, Origins: []string{v.Origin}}
			id, key := []byte("not-given-by-the-source"), []byte(nil)
			if c := v.Creation; c != nil {
				challenge, clientData, attestation := un(t, c.Challenge), un(t, c.ClientDataJSON), un(t, c.AttestationObject)
				var authData []byte
				authData, id, key = attestedIn(t, attestation)
				forLibrary := attestation
				if c.Expect != "refused" {
					forLibrary = enc(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
				}
				cred, ours := rp.Registered(challenge, clientData, attestation)
				theirKey, theirs := differential.Registered(v.RelyingParty, rp.Origins, challenge, id, clientData, forLibrary)
				_, unverified := differential.RegisteredAsking(false, v.RelyingParty, rp.Origins, challenge, id, clientData, forLibrary)
				hold(t, v.Name+" creation", c.Expect, ours, theirs, unverified)
				if ours == nil && theirs == nil && (string(cred.ID) != string(id) || string(cred.PublicKey) != string(theirKey) || string(key) != string(theirKey)) {
					t.Fatal("the two took different passkeys out of one answer")
				}
			}
			if u := v.Use; u != nil {
				if u.PublicKey != "" {
					key = un(t, u.PublicKey)
				}
				challenge, clientData, data, signature := un(t, u.Challenge), un(t, u.ClientDataJSON), un(t, u.AuthenticatorData), un(t, u.Signature)
				_, ours := rp.Asserted(challenge, key, clientData, data, signature)
				theirs := differential.Asserted(v.RelyingParty, rp.Origins, challenge, id, key, clientData, data, signature)
				unverified := differential.AssertedAsking(false, v.RelyingParty, rp.Origins, challenge, id, key, clientData, data, signature)
				hold(t, v.Name+" use", u.Expect, ours, theirs, unverified)
			}
		})
	}
	for name := range capturedDiffer {
		if !seen[name] {
			t.Errorf("a difference is stated for %q, which is no longer there", name)
		}
	}
	for name := range fixtureDiffers {
		if !seen[name] {
			t.Errorf("a difference is stated for %q, which is no longer there", name)
		}
	}
}
