// Package webauthn checks what a browser answers when a passkey is made
// (navigator.credentials.create) and when it is used
// (navigator.credentials.get), as WebAuthn Level 3 sections 7.1 and 7.2 say.
//
// It is this repository's own code because it runs inside the enclave: a
// library would bring attestation formats, JWT and metadata code into the
// measured image. No attestation is trusted, so which device holds a key is
// never decided here. The answers are compared with a known library in
// tests/webauthn-differential.
//
// testdata/vectors holds answers that real authenticators and browsers gave
// (security keys, Apple, Android and Windows platform authenticators, password
// managers) and the specification's own examples, each with where it comes
// from. Every one must pass, or say in its file which rule of the service it
// does not meet, or why it is wrong: a check added here that a real device
// does not meet shows there before it shows in production.
package webauthn

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

// RelyingParty is who the passkeys are for.
type RelyingParty struct {
	ID      string   // the host, e.g. "app.permaily.com"
	Origins []string // the origins accepted, exactly, e.g. "https://app.permaily.com"
}

// Credential is a passkey as it was made.
type Credential struct {
	ID        []byte // the credential's identifier
	PublicKey []byte // its public key, the COSE_Key bytes exactly as the authenticator gave them
	Algorithm int    // COSE algorithm: -7 (ES256), -8 (EdDSA), -257 (RS256)
	Synced    bool   // the authenticator says the key is backed up (BS flag)
}

// Assertion is what a use of a passkey proved.
type Assertion struct {
	Synced bool
}

// ErrInvalid is every refusal. It is wrapped with the check that failed, in
// words that hold nothing of the input: they may be logged, and logs are public.
var ErrInvalid = errors.New("webauthn: answer refused")

// Algorithms are the COSE algorithms accepted, in the order to offer them.
var Algorithms = []int{algEdDSA, algES256, algRS256}

// Flags of the authenticator data.
const (
	flagUP = 1 << 0 // the person was there
	flagUV = 1 << 2 // the person was verified (PIN, fingerprint)
	flagBE = 1 << 3 // the key may be backed up
	flagBS = 1 << 4 // the key is backed up
	flagAT = 1 << 6 // a credential follows
	flagED = 1 << 7 // extensions follow
)

// Bounds, so nothing large is parsed. Each is well above what an honest
// browser sends: a client data of a few hundred bytes, an RSA key of 8192 bits.
const (
	minChallenge         = 16
	maxClientData        = 4096
	maxAttestationObject = 16 << 10
	maxCredentialID      = 1023
	maxKey               = 1100
	maxExtensions        = 1024
	maxSignature         = 1024

	fixedAuthData        = 32 + 1 + 4 // hash of the relying party, flags, counter
	attestedHeader       = 16 + 2     // the authenticator's model and the length of the identifier
	maxAuthenticatorData = fixedAuthData + attestedHeader + maxCredentialID + maxKey + maxExtensions
)

// strict reads CBOR as CTAP2 authenticators write it: nothing of indefinite
// length, no tags, no key twice, and small. Field names match exactly, so two
// spellings of one name cannot both be taken.
var strict = func() cbor.DecMode {
	mode, err := cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		TagsMd:            cbor.TagsForbidden,
		FieldNameMatching: cbor.FieldNameMatchingCaseSensitive,
		MaxNestedLevels:   8,
		MaxMapPairs:       16,
		MaxArrayElements:  16,
	}.DecMode()
	if err != nil {
		panic("webauthn: CBOR options: " + err.Error())
	}
	return mode
}()

func refuse(why string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, why)
}

// Registered checks the answer to navigator.credentials.create.
//
// The attestation format and statement are read and not checked: the service
// asks for none, and a statement would only say which device holds the key.
func (rp RelyingParty) Registered(challenge, clientDataJSON, attestationObject []byte) (*Credential, error) {
	return rp.registered(service, challenge, clientDataJSON, attestationObject)
}

// asked is what an answer must meet that is the relying party's own choice and
// not a rule of the format: that the person was verified, and how long a
// challenge is.
type asked struct {
	verified  bool
	challenge int // the fewest bytes of a challenge
}

// service is what Registered and Asserted ask, always. Only the tests ask
// less, of answers captured from real authenticators at other relying parties:
// many were made without a PIN or a finger, or for a short challenge, and a
// test must tell one refused for that alone from one refused for more.
var service = asked{verified: true, challenge: minChallenge}

func (rp RelyingParty) registered(ask asked, challenge, clientDataJSON, attestationObject []byte) (*Credential, error) {
	if err := rp.clientData(ask, clientDataJSON, challenge, "webauthn.create"); err != nil {
		return nil, err
	}
	if len(attestationObject) > maxAttestationObject {
		return nil, refuse("attestation object too long")
	}
	var object struct {
		Format    *string         `cbor:"fmt"`
		Statement cbor.RawMessage `cbor:"attStmt"`
		AuthData  []byte          `cbor:"authData"`
	}
	if major(attestationObject) != cborMap || strict.Unmarshal(attestationObject, &object) != nil {
		return nil, refuse("malformed attestation object")
	}
	if object.Format == nil || major(object.Statement) != cborMap {
		return nil, refuse("attestation object without a format or a statement")
	}
	flags, rest, err := rp.authenticatorData(ask, object.AuthData)
	if err != nil {
		return nil, err
	}
	if flags&flagAT == 0 {
		return nil, refuse("no credential in the authenticator data")
	}
	if len(rest) < attestedHeader {
		return nil, refuse("credential data too short")
	}
	n := int(binary.BigEndian.Uint16(rest[16:attestedHeader]))
	rest = rest[attestedHeader:]
	if n < 1 || n > maxCredentialID || n > len(rest) {
		return nil, refuse("credential identifier of a wrong length")
	}
	id, rest := rest[:n], rest[n:]

	// The key is one CBOR item; where it ends is where the extensions begin.
	var item cbor.RawMessage
	after, err := strict.UnmarshalFirst(rest, &item)
	if err != nil {
		return nil, refuse("public key is not strict CBOR")
	}
	raw := rest[:len(rest)-len(after)]
	key, err := parseKey(raw)
	if err != nil {
		return nil, err
	}
	if err := extensions(flags, after); err != nil {
		return nil, err
	}
	return &Credential{
		ID:        bytes.Clone(id),
		PublicKey: bytes.Clone(raw),
		Algorithm: key.alg,
		Synced:    flags&flagBS != 0,
	}, nil
}

// Asserted checks the answer to navigator.credentials.get against a stored public key (COSE_Key bytes).
//
// The signature counter is not looked at: synced passkeys always say zero (ADR 0017).
func (rp RelyingParty) Asserted(challenge, publicKey, clientDataJSON, authenticatorData, signature []byte) (*Assertion, error) {
	return rp.asserted(service, challenge, publicKey, clientDataJSON, authenticatorData, signature)
}

func (rp RelyingParty) asserted(ask asked, challenge, publicKey, clientDataJSON, authenticatorData, signature []byte) (*Assertion, error) {
	if err := rp.clientData(ask, clientDataJSON, challenge, "webauthn.get"); err != nil {
		return nil, err
	}
	flags, rest, err := rp.authenticatorData(ask, authenticatorData)
	if err != nil {
		return nil, err
	}
	// A use carries no credential. Refusing the flag keeps one reading of what follows.
	if flags&flagAT != 0 {
		return nil, refuse("a credential in the authenticator data of a use")
	}
	if err := extensions(flags, rest); err != nil {
		return nil, err
	}
	key, err := parseKey(publicKey)
	if err != nil {
		return nil, err
	}
	if len(signature) > maxSignature {
		return nil, refuse("signature too long")
	}
	hash := sha256.Sum256(clientDataJSON)
	if !key.verify(slices.Concat(authenticatorData, hash[:]), signature) {
		return nil, refuse("wrong signature")
	}
	return &Assertion{Synced: flags&flagBS != 0}, nil
}

// clientData checks what the browser says it was asked: which call, with which
// challenge, from which page.
func (rp RelyingParty) clientData(ask asked, raw, challenge []byte, call string) error {
	// A missing challenge must never match a client data that has none either.
	if len(challenge) < ask.challenge {
		return refuse("challenge too short")
	}
	if len(raw) > maxClientData {
		return refuse("client data too long")
	}
	members, ok := object(raw)
	if !ok {
		return refuse("client data is not one JSON object")
	}
	if got, ok := text(members["type"]); !ok || got != call {
		return refuse("client data of another call")
	}
	want := base64.RawURLEncoding.EncodeToString(challenge)
	if got, ok := text(members["challenge"]); !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return refuse("wrong challenge")
	}
	if got, ok := text(members["origin"]); !ok || !slices.Contains(rp.Origins, got) {
		return refuse("origin not accepted")
	}
	// The page that asked was inside another site's frame. Anything but a plain
	// false is refused too: a browser writes only true or false.
	if v, ok := members["crossOrigin"]; ok && string(v) != "false" {
		return refuse("asked from inside another site")
	}
	return nil
}

// object reads the members of one JSON object. encoding/json alone would take
// the last of two members with one name, and names in any case; here a name
// twice is refused and names are matched exactly by the caller.
func object(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := token.(string) // inside an object the decoder gives only names here
		if _, twice := members[name]; twice {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // and nothing after it
		return nil, false
	}
	return members, true
}

// text is a member that is a JSON string. A null would otherwise read as "".
func text(value json.RawMessage) (string, bool) {
	var s string
	if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &s) != nil {
		return "", false
	}
	return s, true
}

// authenticatorData checks the part every answer has, and returns the flags
// and what follows it.
func (rp RelyingParty) authenticatorData(ask asked, raw []byte) (flags byte, rest []byte, err error) {
	if len(raw) < fixedAuthData {
		return 0, nil, refuse("authenticator data too short")
	}
	if len(raw) > maxAuthenticatorData {
		return 0, nil, refuse("authenticator data too long")
	}
	hash := sha256.Sum256([]byte(rp.ID))
	if !bytes.Equal(raw[:sha256.Size], hash[:]) {
		return 0, nil, refuse("made for another relying party")
	}
	flags = raw[sha256.Size]
	if flags&flagUP == 0 {
		return 0, nil, refuse("the person was not there")
	}
	if ask.verified && flags&flagUV == 0 {
		return 0, nil, refuse("the person was not verified")
	}
	// The specification says a key cannot be backed up without being allowed to.
	if flags&flagBS != 0 && flags&flagBE == 0 {
		return 0, nil, refuse("backed up without being allowed to")
	}
	return flags, raw[fixedAuthData:], nil
}

// extensions checks the end of the authenticator data: one CBOR map when the
// flag says so, nothing otherwise. What the extensions say is not used.
func extensions(flags byte, rest []byte) error {
	if flags&flagED == 0 {
		if len(rest) != 0 {
			return refuse("data after the authenticator data")
		}
		return nil
	}
	var entries map[string]cbor.RawMessage
	if major(rest) != cborMap || strict.Unmarshal(rest, &entries) != nil {
		return refuse("extensions are not one strict CBOR map")
	}
	return nil
}

// CBOR major types (RFC 8949, section 3.1) that are told apart by hand: the
// decoder reads a null into any Go value without an error.
const (
	cborMap  = 5
	cborNone = 0xff
)

func major(item []byte) byte {
	if len(item) == 0 {
		return cborNone
	}
	return item[0] >> 5
}
