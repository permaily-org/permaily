package differential_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"

	"github.com/descope/virtualwebauthn"
	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/internal/webauthn"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"

	differential "github.com/permaily-org/permaily/tests/webauthn-differential"
)

const (
	rpID   = "app.example.com"
	origin = "https://app.example.com"
)

var (
	rp        = webauthn.RelyingParty{ID: rpID, Origins: []string{origin}}
	challenge = bytes.Repeat([]byte{0x5a}, 32)
	another   = bytes.Repeat([]byte{0xa5}, 32)
	user      = []byte("account-1")

	algorithms = []struct {
		name string
		alg  int
	}{{"ES256", webauthntest.ES256}, {"EdDSA", webauthntest.EdDSA}, {"RS256", webauthntest.RS256}}
)

// verdict is what the two must answer. They must agree, unless why says why
// they do not: a difference is stated case by case and checked, never skipped,
// so one that goes away, or a new one, fails the test.
type verdict struct {
	ours, theirs bool
	why          string
}

var (
	accepted = verdict{ours: true, theirs: true}
	refused  = verdict{}
)

// Differences that show in more than one test.
const (
	jsonOfGo = "the library reads the client data with encoding/json, which takes names in any case and the last of two members; this package takes each name once, spelled exactly"
	backedUp = "a key cannot be backed up (BS) without being allowed to (BE), WebAuthn 6.1; the library does not look"
)

// stricter is a case this package refuses and the library accepts.
func stricter(why string) verdict { return verdict{theirs: true, why: why} }

// looser is a case this package accepts and the library refuses.
func looser(why string) verdict { return verdict{ours: true, why: why} }

func (v verdict) check(t *testing.T, ours, theirs error) {
	t.Helper()
	if (v.ours != v.theirs) != (v.why != "") {
		t.Fatal("a case says the two differ and not why, or why and not that they differ")
	}
	if (ours == nil) != v.ours || (theirs == nil) != v.theirs {
		t.Fatalf("this package: %v (want accepted: %v)\nthe library: %v (want accepted: %v)", ours, v.ours, theirs, v.theirs)
	}
	if v.why != "" {
		t.Logf("they differ on purpose: %s", v.why)
	}
}

// creation is an answer to navigator.credentials.create and what was asked.
type creation struct {
	challenge, id, clientData, attestation []byte
}

// use is an answer to navigator.credentials.get, what was asked and the stored key.
type use struct {
	challenge, id, key, clientData, authData, signature []byte
}

func (c creation) compare(t *testing.T, v verdict) {
	t.Helper()
	cred, ours := rp.Registered(c.challenge, c.clientData, c.attestation)
	key, theirs := differential.Registered(rpID, rp.Origins, c.challenge, c.id, c.clientData, c.attestation)
	v.check(t, ours, theirs)
	// When both accept, they took the same passkey out of the answer.
	if ours == nil && theirs == nil && (!bytes.Equal(cred.ID, c.id) || !bytes.Equal(cred.PublicKey, key)) {
		t.Fatalf("this package took id %x and key %x, the library key %x", cred.ID, cred.PublicKey, key)
	}
}

func (u use) compare(t *testing.T, v verdict) {
	t.Helper()
	_, ours := rp.Asserted(u.challenge, u.key, u.clientData, u.authData, u.signature)
	theirs := differential.Asserted(rpID, rp.Origins, u.challenge, u.id, u.key, u.clientData, u.authData, u.signature)
	v.check(t, ours, theirs)
}

// makeWith asks the repository's software authenticator for a creation.
func makeWith(a *webauthntest.Authenticator) creation {
	id, clientData, attestation := a.Create(rpID, origin, challenge, user)
	return creation{challenge, id, clientData, attestation}
}

// passkey makes a passkey both accept, and returns its authenticator and its stored key.
func passkey(t *testing.T, alg int) (*webauthntest.Authenticator, []byte, []byte) {
	t.Helper()
	a := webauthntest.New(alg)
	c := makeWith(a)
	cred, err := rp.Registered(c.challenge, c.clientData, c.attestation)
	if err != nil {
		t.Fatal(err)
	}
	return a, c.id, cred.PublicKey
}

func useWith(a *webauthntest.Authenticator, id, key []byte) use {
	clientData, authData, signature, _ := a.Get(rpID, origin, challenge, id)
	return use{challenge, id, key, clientData, authData, signature}
}

func enc(v any) []byte {
	b, err := cbor.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// cmap writes a CBOR map of the pairs given, in the order given, so a case can
// write what an encoder would not, such as a key twice.
func cmap(pairs ...any) []byte {
	out := []byte{0xa0 | byte(len(pairs)/2)}
	for _, p := range pairs {
		out = append(out, enc(p)...)
	}
	return out
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

// flag sets and clears flags of the authenticator data and adds bytes at its end.
func flag(set, clear byte, tail ...byte) func([]byte) []byte {
	return func(data []byte) []byte {
		data = slices.Clone(data)
		data[32] = data[32]&^clear | set
		return append(data, tail...)
	}
}

// keyAt is where the public key starts in the authenticator data the software authenticator writes.
const keyAt = 37 + 16 + 2 + 32

// withKey puts another public key in a creation's authenticator data.
func withKey(key []byte) func([]byte) []byte {
	return func(data []byte) []byte { return append(slices.Clone(data[:keyAt]), key...) }
}

// reattest wraps the authenticator data of a creation in another attestation object.
func reattest(c *creation, object func(authData []byte) []byte) {
	var parsed struct {
		AuthData []byte `cbor:"authData"`
	}
	if err := cbor.Unmarshal(c.attestation, &parsed); err != nil {
		panic(err)
	}
	c.attestation = object(parsed.AuthData)
}

func point() (x, y []byte) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	raw, err := private.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	return raw[1:33], raw[33:]
}

func TestRightAnswers(t *testing.T) {
	for _, c := range algorithms {
		t.Run(c.name, func(t *testing.T) {
			a := webauthntest.New(c.alg)
			made := makeWith(a)
			made.compare(t, accepted)
			cred, err := rp.Registered(made.challenge, made.clientData, made.attestation)
			if err != nil {
				t.Fatal(err)
			}
			useWith(a, made.id, cred.PublicKey).compare(t, accepted)

			a.Synced, a.Counter = true, 9
			makeWith(a).compare(t, accepted)
			useWith(a, made.id, cred.PublicKey).compare(t, accepted)
		})
	}
}

// virtual is an answer of the virtual authenticator, which is somebody else's
// code: it writes the JSON a browser sends, with a packed self attestation.
type virtual struct {
	ID       string `json:"id"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
	} `json:"response"`
}

func parseVirtual(t *testing.T, answer string) (id, clientData, attestation, authData, signature []byte) {
	t.Helper()
	var v virtual
	if err := json.Unmarshal([]byte(answer), &v); err != nil {
		t.Fatal(err)
	}
	fields := make([][]byte, 5)
	for i, s := range []string{v.ID, v.Response.ClientDataJSON, v.Response.AttestationObject, v.Response.AuthenticatorData, v.Response.Signature} {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		fields[i] = b
	}
	return fields[0], fields[1], fields[2], fields[3], fields[4]
}

// The answers of an authenticator this repository did not write: the software
// authenticator and the checks could be wrong in the same way.
func TestVirtualAuthenticator(t *testing.T) {
	party := virtualwebauthn.RelyingParty{ID: rpID, Name: "Example", Origin: origin}
	for _, kind := range []virtualwebauthn.KeyType{virtualwebauthn.KeyTypeEC2, virtualwebauthn.KeyTypeRSA} {
		for _, synced := range []bool{false, true} {
			name := string(kind)
			if synced {
				name += " synced"
			}
			t.Run(name, func(t *testing.T) {
				auth := virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
					UserHandle: user, BackupEligible: synced, BackupState: synced,
				})
				cred := virtualwebauthn.NewCredential(kind)
				id, clientData, attestation, _, _ := parseVirtual(t, virtualwebauthn.CreateAttestationResponse(party, auth, cred,
					virtualwebauthn.AttestationOptions{Challenge: challenge, RelyingPartyID: rpID}))
				made := creation{challenge, id, clientData, attestation}
				made.compare(t, accepted)
				ours, err := rp.Registered(made.challenge, made.clientData, made.attestation)
				if err != nil {
					t.Fatal(err)
				}
				if ours.Synced != synced {
					t.Fatalf("got synced %v", ours.Synced)
				}

				_, clientData, _, authData, signature := parseVirtual(t, virtualwebauthn.CreateAssertionResponse(party, auth, cred,
					virtualwebauthn.AssertionOptions{Challenge: another, RelyingPartyID: rpID}))
				used := use{another, id, ours.PublicKey, clientData, authData, signature}
				used.compare(t, accepted)

				// Its packed statement signs the answer. A statement that signs
				// something else proves nothing, and nothing is asked of it here.
				reattest(&made, func(authData []byte) []byte {
					return cmap("fmt", "packed", "attStmt", map[string]any{"alg": -7, "sig": []byte("not a signature")}, "authData", authData)
				})
				made.compare(t, looser("the attestation statement is not trusted here, so it is not checked (ADR 0017); the library verifies a packed one"))

				used.signature = slices.Clone(signature)
				used.signature[len(signature)/2] ^= 1
				used.compare(t, refused)
				used.signature, used.challenge = signature, challenge
				used.compare(t, refused)
			})
		}
	}
}

func TestCreations(t *testing.T) {
	x, y := point()
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ed := make([]byte, 32)

	const (
		readsOnlyAlgorithm = "when a passkey is made with no attestation the library reads only the key's algorithm; this package reads the whole key, so one that could never be used is not stored"
		statementUnchecked = "the attestation statement is not trusted here, so it is not checked (ADR 0017); the library checks it against its format"
		originAsWritten    = "a browser writes an origin one way (lower case, no path, no default port) and this package compares it byte for byte; the library compares origins as URLs"
	)
	for _, c := range []struct {
		name string
		set  func(a *webauthntest.Authenticator)
		edit func(c *creation)
		want verdict
	}{
		{name: "extensions", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x80, 0, cmap("credProtect", 2)...)
		}, want: accepted},
		{name: "members of the client data it does not know", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = before(`,"other_keys_can_be_added_here":"do not compare clientDataJSON against a template"`)
		}, want: accepted},

		{name: "another challenge", edit: func(c *creation) { c.challenge = another }, want: refused},
		{name: "challenge with padding", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("challenge", base64.URLEncoding.EncodeToString(challenge))
		}, want: refused},
		{name: "no challenge", set: func(a *webauthntest.Authenticator) { a.EditClientData = member("challenge", nil) }, want: refused},
		{name: "plain http", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", "http://app.example.com")
		}, want: refused},
		{name: "another host", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", "https://evil.example.com")
		}, want: refused},
		{name: "another port", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", origin+":8443")
		}, want: refused},
		{name: "no origin", set: func(a *webauthntest.Authenticator) { a.EditClientData = member("origin", nil) }, want: refused},
		{name: "origin in capitals", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", "https://APP.example.com")
		}, want: stricter(originAsWritten)},
		{name: "origin with a path", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", origin+"/")
		}, want: stricter(originAsWritten)},
		{name: "origin with its default port", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("origin", origin+":443")
		}, want: stricter(originAsWritten)},
		{name: "another relying party", set: func(a *webauthntest.Authenticator) { a.RPID = "evil.example.com" }, want: refused},
		{name: "the type of a use", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = member("type", "webauthn.get")
		}, want: refused},
		{name: "no type", set: func(a *webauthntest.Authenticator) { a.EditClientData = member("type", nil) }, want: refused},
		{name: "inside another site", set: func(a *webauthntest.Authenticator) { a.CrossOrigin = true }, want: refused},
		{name: "nobody there", set: func(a *webauthntest.Authenticator) { a.NoPresence = true }, want: refused},
		{name: "nobody verified", set: func(a *webauthntest.Authenticator) { a.NoVerification = true }, want: refused},
		{name: "backed up without being allowed to", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = flag(0x10, 0x08)
		}, want: stricter(backedUp)},

		{name: "client data that is not JSON", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = func([]byte) []byte { return []byte("nope") }
		}, want: refused},
		{name: "client data cut short", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = func(d []byte) []byte { return d[:len(d)-1] }
		}, want: refused},
		{name: "text after the client data", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = func(d []byte) []byte { return append(d, "{}"...) }
		}, want: refused},
		{name: "a member of the client data twice", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = before(`,"type":"webauthn.create"`)
		}, want: stricter(jsonOfGo)},
		{name: "the type under another spelling", set: func(a *webauthntest.Authenticator) {
			a.EditClientData = func(d []byte) []byte { return bytes.Replace(d, []byte(`"type"`), []byte(`"TYPE"`), 1) }
		}, want: stricter(jsonOfGo)},

		{name: "no credential flag", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0, 0x40) }, want: refused},
		{name: "authenticator data cut inside the fixed part", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = func(d []byte) []byte { return d[:36] }
		}, want: refused},
		{name: "authenticator data cut inside the key", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = func(d []byte) []byte { return d[:len(d)-1] }
		}, want: refused},
		{name: "authenticator data with a byte more", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0, 0, 0x00) }, want: refused},
		{name: "extensions flag and no extensions", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0) }, want: refused},
		{name: "extensions that are not a map", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0x01) }, want: refused},
		{name: "extensions and a byte more", set: func(a *webauthntest.Authenticator) { a.EditAuthData = flag(0x80, 0, 0xa0, 0x00) }, want: refused},
		{name: "identifier of 1024 bytes", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = func(d []byte) []byte {
				return slices.Concat(d[:53], []byte{0x04, 0x00}, make([]byte, 1024), d[keyAt:])
			}
		}, edit: func(c *creation) { c.id = make([]byte, 1024) }, want: refused},

		{name: "a key of an algorithm not offered", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -35, -1, 2, -2, make([]byte, 48), -3, make([]byte, 48)))
		}, want: refused},
		{name: "a key with no algorithm", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, -1, 1, -2, x, -3, y))
		}, want: refused},
		{name: "a key that is not a map", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(enc([]any{2, -7, 1, x, y}))
		}, want: refused},
		{name: "a key off the curve", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -7, -1, 1, -2, x, -3, x))
		}, want: stricter(readsOnlyAlgorithm)},
		{name: "a key with a coordinate cut short", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -7, -1, 1, -2, x[:31], -3, y))
		}, want: stricter(readsOnlyAlgorithm)},
		{name: "a P-256 key that says EdDSA", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -8, -1, 1, -2, x, -3, y))
		}, want: stricter(readsOnlyAlgorithm)},
		{name: "an Ed25519 key that says ES256", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 1, 3, -7, -1, 6, -2, ed))
		}, want: stricter(readsOnlyAlgorithm)},
		{name: "an RSA key of 1024 bits", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(webauthntest.COSEKey(&weak.PublicKey))
		}, want: stricter(readsOnlyAlgorithm)},
		{name: "a key with a field it should not have", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y, 2, []byte("kid")))
		}, want: stricter("a passkey's key holds the fields of its algorithm and no other (WebAuthn 6.5.1); the library ignores the ones it does not know")},
		{name: "a key with a field twice", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y, -3, y))
		}, want: refused},
		{name: "a key of indefinite length", set: func(a *webauthntest.Authenticator) {
			a.EditAuthData = withKey(slices.Concat([]byte{0xbf}, cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y)[1:], []byte{0xff}))
		}, want: refused},

		{name: "attestation object that is not CBOR", edit: func(c *creation) { c.attestation = []byte("nope") }, want: refused},
		{name: "attestation object and a byte more", edit: func(c *creation) { c.attestation = append(c.attestation, 0x00) }, want: refused},
		{name: "attestation object cut short", edit: func(c *creation) { c.attestation = c.attestation[:len(c.attestation)-1] }, want: refused},
		{name: "attestation object with no authenticator data", edit: func(c *creation) {
			c.attestation = cmap("fmt", "none", "attStmt", map[string]any{})
		}, want: refused},
		{name: "attestation object with the authenticator data twice", edit: func(c *creation) {
			reattest(c, func(d []byte) []byte {
				return cmap("fmt", "none", "attStmt", map[string]any{}, "authData", d, "authData", d)
			})
		}, want: refused},
		{name: "attestation object of indefinite length", edit: func(c *creation) {
			c.attestation = slices.Concat([]byte{0xbf}, c.attestation[1:], []byte{0xff})
		}, want: refused},
		{name: "a statement with the format none", edit: func(c *creation) {
			reattest(c, func(d []byte) []byte { return cmap("fmt", "none", "attStmt", map[string]any{"x": 1}, "authData", d) })
		}, want: looser(statementUnchecked)},
		{name: "a format nobody knows", edit: func(c *creation) {
			reattest(c, func(d []byte) []byte { return cmap("fmt", "of-tomorrow", "attStmt", map[string]any{}, "authData", d) })
		}, want: looser(statementUnchecked)},
		{name: "no statement", edit: func(c *creation) {
			reattest(c, func(d []byte) []byte { return cmap("fmt", "none", "authData", d) })
		}, want: stricter("the attestation object has three members (WebAuthn 6.5.4); the library takes a missing statement for an empty one")},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := webauthntest.New(webauthntest.ES256)
			if c.set != nil {
				c.set(a)
			}
			made := makeWith(a)
			if c.edit != nil {
				c.edit(&made)
			}
			made.compare(t, c.want)
		})
	}
}

func TestUses(t *testing.T) {
	for _, alg := range algorithms {
		t.Run(alg.name, func(t *testing.T) {
			a, id, key := passkey(t, alg.alg)
			_, _, anotherKey := passkey(t, alg.alg)
			var foreign [][]byte
			for _, o := range algorithms {
				if o.alg != alg.alg {
					_, _, k := passkey(t, o.alg)
					foreign = append(foreign, k)
				}
			}
			otherClientData, otherAuthData, otherSignature, _ := a.Get(rpID, origin, another, id)

			for _, c := range []struct {
				name string
				set  func(a *webauthntest.Authenticator)
				edit func(u *use)
				want verdict
			}{
				{name: "a counter", set: func(a *webauthntest.Authenticator) { a.Counter = 41 }, want: accepted},
				{name: "extensions", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = flag(0x80, 0, cmap("appid", true)...)
				}, want: accepted},

				{name: "another challenge", edit: func(u *use) { u.challenge = another }, want: refused},
				{name: "another origin", set: func(a *webauthntest.Authenticator) {
					a.EditClientData = member("origin", "https://evil.example.com")
				}, want: refused},
				{name: "another relying party", set: func(a *webauthntest.Authenticator) { a.RPID = "evil.example.com" }, want: refused},
				{name: "the type of a creation", set: func(a *webauthntest.Authenticator) {
					a.EditClientData = member("type", "webauthn.create")
				}, want: refused},
				{name: "inside another site", set: func(a *webauthntest.Authenticator) { a.CrossOrigin = true }, want: refused},
				{name: "nobody there", set: func(a *webauthntest.Authenticator) { a.NoPresence = true }, want: refused},
				{name: "nobody verified", set: func(a *webauthntest.Authenticator) { a.NoVerification = true }, want: refused},
				{name: "backed up without being allowed to", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = flag(0x10, 0x08)
				}, want: stricter(backedUp)},
				{name: "a member of the client data twice", set: func(a *webauthntest.Authenticator) {
					a.EditClientData = before(`,"type":"webauthn.get"`)
				}, want: stricter(jsonOfGo)},

				{name: "the signature of another answer", edit: func(u *use) { u.signature = otherSignature }, want: refused},
				{name: "the signature of another answer, with its authenticator data", edit: func(u *use) {
					u.authData, u.signature = otherAuthData, otherSignature
				}, want: refused},
				{name: "another answer whole, for this challenge", edit: func(u *use) {
					u.clientData, u.authData, u.signature = otherClientData, otherAuthData, otherSignature
				}, want: refused},
				{name: "a signature changed", edit: func(u *use) { u.signature[len(u.signature)/2] ^= 1 }, want: refused},
				{name: "a signature cut short", edit: func(u *use) { u.signature = u.signature[:len(u.signature)-1] }, want: refused},
				{name: "no signature", edit: func(u *use) { u.signature = nil }, want: refused},
				{name: "the client data changed after signing", edit: func(u *use) { u.clientData = append(u.clientData, ' ') }, want: refused},
				{name: "the counter changed after signing", edit: func(u *use) { u.authData[36] ^= 1 }, want: refused},
				{name: "a flag changed after signing", edit: func(u *use) { u.authData[32] |= 0x18 }, want: refused},

				{name: "authenticator data cut short, signed", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = func(d []byte) []byte { return d[:36] }
				}, want: refused},
				{name: "authenticator data cut short after signing", edit: func(u *use) { u.authData = u.authData[:36] }, want: refused},
				{name: "authenticator data with a byte more, signed", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = flag(0, 0, 0x00)
				}, want: refused},
				{name: "authenticator data with a byte more after signing", edit: func(u *use) { u.authData = append(u.authData, 0x00) }, want: refused},
				{name: "extensions flag and no extensions, signed", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = flag(0x80, 0)
				}, want: refused},
				{name: "a credential in the authenticator data, signed", set: func(a *webauthntest.Authenticator) {
					a.EditAuthData = func(d []byte) []byte {
						return slices.Concat(flag(0x40, 0)(d), make([]byte, 16), []byte{0, byte(len(id))}, id, key)
					}
				}, want: stricter("a use carries no credential (WebAuthn 6.1: the flag is for a creation); the library reads one when the flag says so")},

				{name: "the key of another passkey", edit: func(u *use) { u.key = anotherKey }, want: refused},
				{name: "a key of another algorithm", edit: func(u *use) { u.key = foreign[0] }, want: refused},
				{name: "a key of the third algorithm", edit: func(u *use) { u.key = foreign[1] }, want: refused},
				{name: "no key", edit: func(u *use) { u.key = nil }, want: refused},
				{name: "a key and a byte more", edit: func(u *use) { u.key = append(slices.Clone(u.key), 0x00) }, want: refused},
				{name: "a key cut short", edit: func(u *use) { u.key = u.key[:len(u.key)-1] }, want: refused},
			} {
				t.Run(c.name, func(t *testing.T) {
					a.NoPresence, a.NoVerification, a.CrossOrigin, a.RPID, a.Counter = false, false, false, "", 0
					a.EditAuthData, a.EditClientData = nil, nil
					if c.set != nil {
						c.set(a)
					}
					used := useWith(a, id, key)
					if c.edit != nil {
						c.edit(&used)
					}
					used.compare(t, c.want)
				})
			}
		})
	}
}

// An ECDSA signature in BER that is not DER: the same two numbers, the length
// written the long way.
func TestSignatureEncoding(t *testing.T) {
	a, id, key := passkey(t, webauthntest.ES256)
	used := useWith(a, id, key)
	used.compare(t, accepted)
	if used.signature[0] != 0x30 || used.signature[1] >= 0x80 {
		t.Fatalf("not the DER expected: %x", used.signature)
	}
	used.signature = slices.Concat([]byte{0x30, 0x81}, used.signature[1:])
	used.compare(t, refused)

	// The digest the signature is over, checked here a third way: with the standard library alone.
	var cose struct {
		X []byte `cbor:"-2,keyasint"`
		Y []byte `cbor:"-3,keyasint"`
	}
	if err := cbor.Unmarshal(key, &cose); err != nil {
		t.Fatal(err)
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, cose.X, cose.Y))
	if err != nil {
		t.Fatal(err)
	}
	right := useWith(a, id, key)
	hash := sha256.Sum256(right.clientData)
	digest := sha256.Sum256(slices.Concat(right.authData, hash[:]))
	if !ecdsa.VerifyASN1(public, digest[:], right.signature) {
		t.Fatal("the signature is not over the authenticator data and the hash of the client data")
	}
}
