package webauthn_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/internal/webauthn"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

const (
	rpID   = "app.example.com"
	origin = "https://app.example.com"
)

var (
	rp        = webauthn.RelyingParty{ID: rpID, Origins: []string{origin}}
	challenge = bytes.Repeat([]byte{0x5a}, 32)
	user      = []byte("account-1")

	algorithms = []struct {
		name string
		alg  int
	}{{"ES256", webauthntest.ES256}, {"EdDSA", webauthntest.EdDSA}, {"RS256", webauthntest.RS256}}
)

// refused fails unless err is a refusal for exactly this reason. The whole
// text is compared, so a reason that carried a piece of the input would fail.
func refused(t *testing.T, err error, why string) {
	t.Helper()
	if !errors.Is(err, webauthn.ErrInvalid) {
		t.Fatalf("got %v, want a refusal: %s", err, why)
	}
	if want := webauthn.ErrInvalid.Error() + ": " + why; err.Error() != want {
		t.Fatalf("refused with %q, want %q", err, want)
	}
}

// outcome checks an answer against why it must be refused; "" means it must be accepted.
func outcome(t *testing.T, call string, err error, why string) {
	t.Helper()
	if why != "" {
		refused(t, err, why)
	} else if err != nil {
		t.Fatalf("%s: refused a right answer: %v", call, err)
	}
}

func enc(v any) []byte {
	b, err := cbor.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// cmap writes a CBOR map of the pairs given, in the order given, so a test
// can write what an encoder would not: a key twice, keys out of order.
func cmap(pairs ...any) []byte {
	out := []byte{0xa0 | byte(len(pairs)/2)}
	for _, p := range pairs {
		out = append(out, enc(p)...)
	}
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// passkey makes a passkey with a right answer and returns the authenticator that holds it.
func passkey(t *testing.T, alg int) (*webauthntest.Authenticator, *webauthn.Credential) {
	t.Helper()
	a := webauthntest.New(alg)
	_, clientData, attestation := a.Create(rpID, origin, challenge, user)
	cred, err := rp.Registered(challenge, clientData, attestation)
	if err != nil {
		t.Fatal(err)
	}
	return a, cred
}

func TestMadeAndUsed(t *testing.T) {
	for _, c := range algorithms {
		t.Run(c.name, func(t *testing.T) {
			a := webauthntest.New(c.alg)
			id, clientData, attestation := a.Create(rpID, origin, challenge, user)
			cred, err := rp.Registered(challenge, clientData, attestation)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cred.ID, id) || cred.Algorithm != c.alg || cred.Synced {
				t.Fatalf("got id %x, algorithm %d, synced %v", cred.ID, cred.Algorithm, cred.Synced)
			}
			// The key is the authenticator's bytes, untouched: they are inside the authenticator data.
			var object struct {
				AuthData []byte `cbor:"authData"`
			}
			if err := cbor.Unmarshal(attestation, &object); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(object.AuthData, cred.PublicKey) || len(cred.PublicKey) == 0 {
				t.Fatal("the public key is not the bytes the authenticator gave")
			}

			again := bytes.Repeat([]byte{0xa5}, 32)
			clientData, authData, signature, handle := a.Get(rpID, origin, again, id)
			got, err := rp.Asserted(again, cred.PublicKey, clientData, authData, signature)
			if err != nil {
				t.Fatal(err)
			}
			if got.Synced || !bytes.Equal(handle, user) {
				t.Fatalf("got synced %v, user %q", got.Synced, handle)
			}
		})
	}
}

func TestAlgorithmsOffered(t *testing.T) {
	if !slices.Equal(webauthn.Algorithms, []int{-8, -7, -257}) {
		t.Fatalf("got %v", webauthn.Algorithms)
	}
}

func TestSecondOriginAccepted(t *testing.T) {
	two := webauthn.RelyingParty{ID: rpID, Origins: []string{"https://other.example.com", origin}}
	a := webauthntest.New(webauthntest.ES256)
	_, clientData, attestation := a.Create(rpID, origin, challenge, user)
	if _, err := two.Registered(challenge, clientData, attestation); err != nil {
		t.Fatal(err)
	}
	none := webauthn.RelyingParty{ID: rpID}
	_, err := none.Registered(challenge, clientData, attestation)
	refused(t, err, "origin not accepted")
}

// A relying party set up wrongly, with an empty origin, must not make a client
// data that names no origin a right one.
func TestNoOriginIsNoMatch(t *testing.T) {
	empty := webauthn.RelyingParty{ID: rpID, Origins: []string{""}}
	for name, edit := range map[string]func([]byte) []byte{
		"no origin": member("origin", nil),
		"null":      func(d []byte) []byte { return before(`,"origin":null`)(member("origin", nil)(d)) },
	} {
		t.Run(name, func(t *testing.T) {
			a := webauthntest.New(webauthntest.ES256)
			a.EditClientData = edit
			_, clientData, attestation := a.Create(rpID, origin, challenge, user)
			_, err := empty.Registered(challenge, clientData, attestation)
			refused(t, err, "origin not accepted")
		})
	}
}

func TestSyncedIsReported(t *testing.T) {
	for _, c := range []struct {
		name             string
		eligible, synced bool
	}{{"neither", false, false}, {"may be backed up", true, false}, {"backed up", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			a := webauthntest.New(webauthntest.EdDSA)
			a.BackupEligible, a.Synced = c.eligible, c.synced
			id, clientData, attestation := a.Create(rpID, origin, challenge, user)
			cred, err := rp.Registered(challenge, clientData, attestation)
			if err != nil {
				t.Fatal(err)
			}
			clientData, authData, signature, _ := a.Get(rpID, origin, challenge, id)
			got, err := rp.Asserted(challenge, cred.PublicKey, clientData, authData, signature)
			if err != nil {
				t.Fatal(err)
			}
			if cred.Synced != c.synced || got.Synced != c.synced {
				t.Fatalf("made synced %v, used synced %v, want %v", cred.Synced, got.Synced, c.synced)
			}
		})
	}
}

// both runs a creation and a use with an authenticator set to answer wrongly.
// The wrong answers are signed like right ones, so only the check that is
// named can refuse them.
func both(t *testing.T, set func(a *webauthntest.Authenticator), asked, given []byte, why string) {
	t.Helper()
	a, cred := passkey(t, webauthntest.ES256)
	set(a)
	_, clientData, attestation := a.Create(rpID, origin, asked, user)
	_, err := rp.Registered(given, clientData, attestation)
	outcome(t, "create", err, why)

	clientData, authData, signature, _ := a.Get(rpID, origin, asked, cred.ID)
	_, err = rp.Asserted(given, cred.PublicKey, clientData, authData, signature)
	outcome(t, "get", err, why)
}

// member sets one member of the client data; a nil value takes it out.
func member(name string, value any) func([]byte) []byte {
	return func(data []byte) []byte {
		var members map[string]any
		if err := json.Unmarshal(data, &members); err != nil {
			panic(err)
		}
		if value == nil {
			delete(members, name)
		} else {
			members[name] = value
		}
		out, err := json.Marshal(members)
		if err != nil {
			panic(err)
		}
		return out
	}
}

// before puts text in front of the closing brace of the client data.
func before(text string) func([]byte) []byte {
	return func(data []byte) []byte {
		return append(slices.Clone(data[:len(data)-1]), text+"}"...)
	}
}

func TestClientData(t *testing.T) {
	short := bytes.Repeat([]byte{1}, 15)
	for _, c := range []struct {
		name         string
		edit         func([]byte) []byte
		asked, given []byte
		why          string
	}{
		{name: "as a browser writes it"},
		{name: "no crossOrigin", edit: member("crossOrigin", nil)},
		{name: "members it does not know", edit: before(`,"topOrigin":"https://app.example.com","tokenBinding":{"status":"supported"},"other_keys_can_be_added_here":"do not compare clientDataJSON against a template"`)},
		{name: "spaces", edit: func(d []byte) []byte { return append([]byte(" \n"), append(d, ' ')...) }},

		{name: "the other call", edit: func(d []byte) []byte {
			if bytes.Contains(d, []byte("webauthn.create")) {
				return bytes.Replace(d, []byte("webauthn.create"), []byte("webauthn.get"), 1)
			}
			return bytes.Replace(d, []byte("webauthn.get"), []byte("webauthn.create"), 1)
		}, why: "client data of another call"},
		{name: "no type", edit: member("type", nil), why: "client data of another call"},
		{name: "type that is not text", edit: func(d []byte) []byte {
			return before(`,"type":null`)(member("type", nil)(d))
		}, why: "client data of another call"},
		{name: "type under another spelling", edit: func(d []byte) []byte {
			return bytes.Replace(d, []byte(`"type"`), []byte(`"Type"`), 1)
		}, why: "client data of another call"},

		{name: "another challenge", given: bytes.Repeat([]byte{0x5b}, 32), why: "wrong challenge"},
		{name: "a challenge that only starts the same", given: append(slices.Clone(challenge), 0), why: "wrong challenge"},
		{name: "challenge with padding", edit: member("challenge", base64.URLEncoding.EncodeToString(challenge)), why: "wrong challenge"},
		{name: "challenge in the other alphabet", asked: bytes.Repeat([]byte{0xfb}, 32), given: bytes.Repeat([]byte{0xfb}, 32), edit: func(d []byte) []byte {
			return bytes.ReplaceAll(d, []byte("-"), []byte("+"))
		}, why: "wrong challenge"},
		{name: "no challenge", edit: member("challenge", nil), why: "wrong challenge"},
		{name: "challenge that is a number", edit: member("challenge", 7), why: "wrong challenge"},
		{name: "short challenge", asked: short, given: short, why: "challenge too short"},
		{name: "no challenge on either side", asked: []byte{}, given: []byte{}, why: "challenge too short"},

		{name: "plain http", edit: member("origin", "http://app.example.com"), why: "origin not accepted"},
		{name: "another host", edit: member("origin", "https://evil.example.com"), why: "origin not accepted"},
		{name: "a host that ends the same", edit: member("origin", "https://evilapp.example.com"), why: "origin not accepted"},
		{name: "origin with a path", edit: member("origin", origin+"/"), why: "origin not accepted"},
		{name: "origin with a port", edit: member("origin", origin+":443"), why: "origin not accepted"},
		{name: "origin in capitals", edit: member("origin", "https://APP.example.com"), why: "origin not accepted"},
		{name: "no origin", edit: member("origin", nil), why: "origin not accepted"},
		{name: "origin that is a list", edit: member("origin", []string{origin}), why: "origin not accepted"},

		{name: "inside another site", edit: member("crossOrigin", true), why: "asked from inside another site"},
		{name: "crossOrigin that is text", edit: member("crossOrigin", "false"), why: "asked from inside another site"},
		{name: "crossOrigin that is null", edit: func(d []byte) []byte {
			return before(`,"crossOrigin":null`)(member("crossOrigin", nil)(d))
		}, why: "asked from inside another site"},

		{name: "not JSON", edit: func([]byte) []byte { return []byte("nope") }, why: "client data is not one JSON object"},
		{name: "empty", edit: func([]byte) []byte { return nil }, why: "client data is not one JSON object"},
		{name: "a list", edit: func(d []byte) []byte { return append(append([]byte("["), d...), ']') }, why: "client data is not one JSON object"},
		{name: "a list of names and values", edit: func(d []byte) []byte {
			// Read one token at a time, a list of pairs looks like an object's members.
			var members map[string]any
			if err := json.Unmarshal(d, &members); err != nil {
				panic(err)
			}
			out, err := json.Marshal([]any{"type", members["type"], "challenge", members["challenge"], "origin", members["origin"]})
			if err != nil {
				panic(err)
			}
			return out
		}, why: "client data is not one JSON object"},
		{name: "a name that is not text", edit: func([]byte) []byte { return []byte(`{1:2}`) }, why: "client data is not one JSON object"},
		{name: "a member without a value", edit: before(`,"x":`), why: "client data is not one JSON object"},
		{name: "cut short", edit: func(d []byte) []byte { return d[:len(d)-1] }, why: "client data is not one JSON object"},
		{name: "a member twice", edit: func(d []byte) []byte {
			// The same value twice: only the rule against a name twice can refuse it.
			var members map[string]json.RawMessage
			if err := json.Unmarshal(d, &members); err != nil {
				panic(err)
			}
			return before(`,"type":` + string(members["type"]))(d)
		}, why: "client data is not one JSON object"},
		{name: "two objects", edit: func(d []byte) []byte { return append(d, "{}"...) }, why: "client data is not one JSON object"},
		{name: "text after the object", edit: func(d []byte) []byte { return append(d, " x"...) }, why: "client data is not one JSON object"},
		{name: "bytes that are not UTF-8", edit: before(",\"x\":\"\xff\""), why: "client data is not one JSON object"},
		{name: "too long", edit: before(`,"x":"` + strings.Repeat("a", 4096) + `"`), why: "client data too long"},
	} {
		t.Run(c.name, func(t *testing.T) {
			asked, given := c.asked, c.given
			if asked == nil {
				asked = challenge
			}
			if given == nil {
				given = asked
			}
			both(t, func(a *webauthntest.Authenticator) { a.EditClientData = c.edit }, asked, given, c.why)
		})
	}
}

// flag sets and clears flags of the authenticator data and adds bytes at its end.
func flag(set, clear byte, tail ...byte) func([]byte) []byte {
	return func(data []byte) []byte {
		data = slices.Clone(data)
		data[32] = data[32]&^clear | set
		return append(data, tail...)
	}
}

func TestAuthenticatorData(t *testing.T) {
	bigExtension := cmap("x", make([]byte, 3300))
	for _, c := range []struct {
		name string
		set  func(a *webauthntest.Authenticator)
		why  string
	}{
		{name: "a counter", set: func(a *webauthntest.Authenticator) { a.Counter = 7 }},
		{name: "flags kept for later", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x22, 0) }},
		{name: "extensions", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, cmap("credProtect", 2, "hmac-secret", true)...)
		}},
		{name: "no extensions in the map", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0xa0) }},

		{name: "cut short", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = func(d []byte) []byte { return d[:36] }
		}, why: "authenticator data too short"},
		{name: "empty", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = func([]byte) []byte { return nil }
		}, why: "authenticator data too short"},
		{name: "too long", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, bigExtension...)
		}, why: "authenticator data too long"},
		{name: "another relying party", set: func(a *webauthntest.Authenticator) { a.RPID = "evil.example.com" }, why: "made for another relying party"},
		{name: "the origin in place of the host", set: func(a *webauthntest.Authenticator) { a.RPID = origin }, why: "made for another relying party"},
		{name: "nobody there", set: func(a *webauthntest.Authenticator) { a.NoPresence = true }, why: "the person was not there"},
		{name: "nobody verified", set: func(a *webauthntest.Authenticator) { a.NoVerification = true }, why: "the person was not verified"},
		{name: "backed up without being allowed to", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x10, 0x08)
		}, why: "backed up without being allowed to"},

		{name: "bytes after the end", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0, 0, 0xa0) }, why: "data after the authenticator data"},
		{name: "extensions flag and no extensions", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0) }, why: "extensions are not one strict CBOR map"},
		{name: "extensions that are not a map", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0x01) }, why: "extensions are not one strict CBOR map"},
		{name: "extensions that are null", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0xf6) }, why: "extensions are not one strict CBOR map"},
		{name: "bytes after the extensions", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0xa0, 0x00) }, why: "extensions are not one strict CBOR map"},
		{name: "an extension twice", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, cmap("a", 1, "a", 1)...)
		}, why: "extensions are not one strict CBOR map"},
		{name: "extensions of indefinite length", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, 0xbf, 0x61, 'a', 0x01, 0xff)
		}, why: "extensions are not one strict CBOR map"},
		{name: "an extension inside a tag", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, cmap("a", cbor.Tag{Number: 100, Content: 1})...)
		}, why: "extensions are not one strict CBOR map"},
		{name: "an extension named by a number", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, cmap(1, 1)...)
		}, why: "extensions are not one strict CBOR map"},
	} {
		t.Run(c.name, func(t *testing.T) {
			both(t, c.set, challenge, challenge, c.why)
		})
	}
}

// Answers written by hand, for a creation: no key is needed to make them.

func createClientData() []byte {
	return []byte(`{"type":"webauthn.create","challenge":"` + b64(challenge) + `","origin":"` + origin + `","crossOrigin":false}`)
}

// authData is the fixed part, for this relying party and with a counter of zero, and what follows it.
func authData(flags byte, tail ...[]byte) []byte {
	hash := sha256.Sum256([]byte(rpID))
	return slices.Concat(hash[:], []byte{flags, 0, 0, 0, 0}, slices.Concat(tail...))
}

// attested is the credential data, with n in place of the identifier's length when it is not negative.
func attested(n int, id, key []byte) []byte {
	if n < 0 {
		n = len(id)
	}
	return slices.Concat(make([]byte, 16), binary.BigEndian.AppendUint16(nil, uint16(n)), id, key)
}

func attestationOf(authData []byte) []byte {
	return cmap("fmt", "none", "attStmt", map[string]any{}, "authData", authData)
}

func made(attestation []byte) (*webauthn.Credential, error) {
	return rp.Registered(challenge, createClientData(), attestation)
}

const made0 = 0x45 // UP, UV and AT

func TestCredentialData(t *testing.T) {
	key := edKey()
	id := []byte("an identifier")
	for _, c := range []struct {
		name     string
		authData []byte
		why      string
	}{
		{name: "as an authenticator writes it", authData: authData(made0, attested(-1, id, key))},
		{name: "identifier of one byte", authData: authData(made0, attested(-1, []byte{1}, key))},
		{name: "identifier of 1023 bytes", authData: authData(made0, attested(-1, make([]byte, 1023), key))},
		{name: "extensions after the key", authData: authData(made0|0x80, attested(-1, id, key), cmap("credProtect", 2))},

		{name: "flag of a credential not set", authData: authData(made0&^0x40, attested(-1, id, key)), why: "no credential in the authenticator data"},
		{name: "nothing after the fixed part", authData: authData(made0), why: "credential data too short"},
		{name: "credential data cut short", authData: authData(made0, make([]byte, 17)), why: "credential data too short"},
		{name: "identifier of no bytes", authData: authData(made0, attested(-1, nil, key)), why: "credential identifier of a wrong length"},
		{name: "identifier of 1024 bytes", authData: authData(made0, attested(-1, make([]byte, 1024), key)), why: "credential identifier of a wrong length"},
		{name: "identifier longer than what is there", authData: authData(made0, attested(100, id, nil)), why: "credential identifier of a wrong length"},
		{name: "no key", authData: authData(made0, attested(-1, id, nil)), why: "public key is not strict CBOR"},
		{name: "key cut short", authData: authData(made0, attested(-1, id, key[:len(key)-1])), why: "public key is not strict CBOR"},
		{name: "key of indefinite length", authData: authData(made0, attested(-1, id, slices.Concat([]byte{0xbf}, key[1:], []byte{0xff}))), why: "public key is not strict CBOR"},
		{name: "key inside a tag", authData: authData(made0, attested(-1, id, slices.Concat([]byte{0xd8, 0x64}, key))), why: "public key is not strict CBOR"},
		{name: "bytes after the key", authData: authData(made0, attested(-1, id, key), []byte{0x00}), why: "data after the authenticator data"},
		{name: "extensions without their flag", authData: authData(made0, attested(-1, id, key), cmap("credProtect", 2)), why: "data after the authenticator data"},
		{name: "extensions flag and no extensions", authData: authData(made0|0x80, attested(-1, id, key)), why: "extensions are not one strict CBOR map"},
		{name: "bytes after the extensions", authData: authData(made0|0x80, attested(-1, id, key), []byte{0xa0, 0xa0}), why: "extensions are not one strict CBOR map"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cred, err := made(attestationOf(c.authData))
			outcome(t, "create", err, c.why)
			if c.why == "" && (len(cred.ID) == 0 || !bytes.Equal(cred.PublicKey, key) || cred.Algorithm != webauthntest.EdDSA) {
				t.Fatalf("got id %x, key %x, algorithm %d", cred.ID, cred.PublicKey, cred.Algorithm)
			}
		})
	}
}

func TestAttestationObject(t *testing.T) {
	data := authData(made0, attested(-1, []byte("an identifier"), edKey()))
	right := attestationOf(data)
	for _, c := range []struct {
		name   string
		object []byte
		why    string
	}{
		{name: "none", object: right},
		// The statement is not trusted, so one that proves nothing is as good as any.
		{name: "a format and a statement that are not checked", object: cmap("fmt", "packed", "attStmt", map[string]any{"alg": -7, "sig": []byte("not a signature"), "x5c": [][]byte{[]byte("not a certificate")}}, "authData", data)},
		{name: "a format nobody knows", object: cmap("fmt", "of-tomorrow", "attStmt", map[string]any{}, "authData", data)},
		{name: "a member it does not know", object: cmap("fmt", "none", "attStmt", map[string]any{}, "authData", data, "epAtt", true)},
		{name: "a member named by a number", object: cmap("fmt", "none", "attStmt", map[string]any{}, "authData", data, 1, 1)},
		{name: "members in another order", object: cmap("authData", data, "fmt", "none", "attStmt", map[string]any{})},

		{name: "too long", object: cmap("fmt", "packed", "attStmt", map[string]any{"x5c": [][]byte{make([]byte, 16<<10)}}, "authData", data), why: "attestation object too long"},
		{name: "empty", object: nil, why: "malformed attestation object"},
		{name: "not CBOR", object: []byte("nope"), why: "malformed attestation object"},
		{name: "a list", object: enc([]any{"none", map[string]any{}, data}), why: "malformed attestation object"},
		{name: "null", object: []byte{0xf6}, why: "malformed attestation object"},
		{name: "cut short", object: right[:len(right)-1], why: "malformed attestation object"},
		{name: "bytes after it", object: append(slices.Clone(right), 0x00), why: "malformed attestation object"},
		{name: "of indefinite length", object: slices.Concat([]byte{0xbf}, right[1:], []byte{0xff}), why: "malformed attestation object"},
		{name: "inside a tag", object: slices.Concat([]byte{0xd9, 0xd9, 0xf7}, right), why: "malformed attestation object"},
		{name: "authenticator data twice", object: cmap("fmt", "none", "attStmt", map[string]any{}, "authData", data, "authData", data), why: "malformed attestation object"},
		{name: "format twice", object: cmap("fmt", "none", "fmt", "none", "attStmt", map[string]any{}, "authData", data), why: "malformed attestation object"},
		{name: "format that is not text", object: cmap("fmt", 1, "attStmt", map[string]any{}, "authData", data), why: "malformed attestation object"},
		{name: "authenticator data that is text", object: cmap("fmt", "none", "attStmt", map[string]any{}, "authData", strings.Repeat("a", 60)), why: "malformed attestation object"},
		{name: "statement nested too deep", object: cmap("fmt", "none", "attStmt", nested(9), "authData", data), why: "malformed attestation object"},

		{name: "no format", object: cmap("attStmt", map[string]any{}, "authData", data), why: "attestation object without a format or a statement"},
		{name: "format that is null", object: cmap("fmt", nil, "attStmt", map[string]any{}, "authData", data), why: "attestation object without a format or a statement"},
		{name: "no statement", object: cmap("fmt", "none", "authData", data), why: "attestation object without a format or a statement"},
		{name: "statement that is a list", object: cmap("fmt", "none", "attStmt", []any{}, "authData", data), why: "attestation object without a format or a statement"},
		{name: "statement that is null", object: cmap("fmt", "none", "attStmt", nil, "authData", data), why: "attestation object without a format or a statement"},

		{name: "no authenticator data", object: cmap("fmt", "none", "attStmt", map[string]any{}), why: "authenticator data too short"},
		{name: "authenticator data under another spelling", object: cmap("fmt", "none", "attStmt", map[string]any{}, "AuthData", data), why: "authenticator data too short"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := made(c.object)
			outcome(t, "create", err, c.why)
		})
	}
}

// nested is a map inside a map, depth times.
func nested(depth int) any {
	if depth == 0 {
		return 1
	}
	return map[string]any{"a": nested(depth - 1)}
}

func TestUse(t *testing.T) {
	type answer struct{ challenge, key, clientData, authData, signature []byte }
	for _, alg := range algorithms {
		t.Run(alg.name, func(t *testing.T) {
			a, cred := passkey(t, alg.alg)
			_, another := passkey(t, alg.alg)
			// A key of each other algorithm: the answer was not signed with it.
			var foreign [][]byte
			for _, o := range algorithms {
				if o.alg != alg.alg {
					_, c := passkey(t, o.alg)
					foreign = append(foreign, c.PublicKey)
				}
			}
			otherChallenge := bytes.Repeat([]byte{0x77}, 32)
			_, _, otherSignature, _ := a.Get(rpID, origin, otherChallenge, cred.ID)

			for _, c := range []struct {
				name string
				set  func(a *webauthntest.Authenticator)
				edit func(r *answer)
				why  string
			}{
				{name: "a right answer"},
				{name: "a credential flag", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x40, 0) }, why: "a credential in the authenticator data of a use"},
				{name: "a credential", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = flag(0x40, 0, attested(-1, cred.ID, cred.PublicKey)...)
				}, why: "a credential in the authenticator data of a use"},

				{name: "the key of another passkey", edit: func(r *answer) { r.key = another.PublicKey }, why: "wrong signature"},
				{name: "a key of another algorithm", edit: func(r *answer) { r.key = foreign[0] }, why: "wrong signature"},
				{name: "a key of the third algorithm", edit: func(r *answer) { r.key = foreign[1] }, why: "wrong signature"},
				{name: "no key", edit: func(r *answer) { r.key = nil }, why: "public key is not a strict CBOR map"},
				{name: "bytes after the key", edit: func(r *answer) { r.key = append(slices.Clone(r.key), 0x00) }, why: "public key is not a strict CBOR map"},
				{name: "a key too long", edit: func(r *answer) { r.key = rsaKey(make([]byte, 1100), []byte{1, 0, 1}) }, why: "public key too long"},

				{name: "a signature over another answer", edit: func(r *answer) { r.signature = otherSignature }, why: "wrong signature"},
				{name: "a signature changed", edit: func(r *answer) { r.signature[len(r.signature)/2] ^= 1 }, why: "wrong signature"},
				{name: "a signature cut short", edit: func(r *answer) { r.signature = r.signature[:len(r.signature)-1] }, why: "wrong signature"},
				{name: "no signature", edit: func(r *answer) { r.signature = nil }, why: "wrong signature"},
				{name: "a signature too long", edit: func(r *answer) { r.signature = make([]byte, 1025) }, why: "signature too long"},
				{name: "the counter changed after signing", edit: func(r *answer) { r.authData[36] ^= 1 }, why: "wrong signature"},
				{name: "a flag changed after signing", edit: func(r *answer) { r.authData[32] |= 0x18 }, why: "wrong signature"},
				{name: "the client data changed after signing", edit: func(r *answer) { r.clientData = append(r.clientData, ' ') }, why: "wrong signature"},
			} {
				t.Run(c.name, func(t *testing.T) {
					a.EditAuthData = nil
					if c.set != nil {
						c.set(a)
					}
					clientData, authData, signature, _ := a.Get(rpID, origin, challenge, cred.ID)
					r := answer{challenge, cred.PublicKey, clientData, authData, signature}
					if c.edit != nil {
						c.edit(&r)
					}
					_, err := rp.Asserted(r.challenge, r.key, r.clientData, r.authData, r.signature)
					outcome(t, "get", err, c.why)
				})
			}
		})
	}
}

func TestUnknownCredential(t *testing.T) {
	a := webauthntest.New(webauthntest.ES256)
	if clientData, authData, signature, handle := a.Get(rpID, origin, challenge, []byte("never made")); clientData != nil || authData != nil || signature != nil || handle != nil {
		t.Fatal("answered for a credential it never made")
	}
}
