package webauthn_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/permaily-org/permaily/internal/webauthn"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// asked fails unless an accepted client data says what was asked. The package
// refuses a member twice, in any case too, so reading it into a map here
// (names exactly) or into a struct (names in any case, as encoding/json reads
// them, the last of two taken) chooses nothing: both say the same.
func asked(t *testing.T, clientData []byte, call string) {
	t.Helper()
	var members map[string]any
	if err := json.Unmarshal(clientData, &members); err != nil {
		t.Fatalf("accepted a client data that is not JSON: %q", clientData)
	}
	crossOrigin, has := members["crossOrigin"]
	if members["type"] != call || members["challenge"] != b64(challenge) || members["origin"] != origin || (has && crossOrigin != false) {
		t.Fatalf("accepted a client data that does not say what was asked: %q", clientData)
	}
	var anyCase struct {
		Type        any `json:"type"`
		Challenge   any `json:"challenge"`
		Origin      any `json:"origin"`
		CrossOrigin any `json:"crossOrigin"`
	}
	if err := json.Unmarshal(clientData, &anyCase); err != nil {
		t.Fatalf("accepted a client data that is not JSON: %q", clientData)
	}
	if anyCase.Type != call || anyCase.Challenge != b64(challenge) || anyCase.Origin != origin || (has && anyCase.CrossOrigin != false) {
		t.Fatalf("accepted a client data that says another thing to encoding/json: %q", clientData)
	}
}

// inAnotherCase adds to the client data a member that is one already in
// another case, or in other letters encoding/json takes as one (the Kelvin
// sign for a k, the long s for an s), with another value.
func inAnotherCase(member string) func([]byte) []byte {
	return func(d []byte) []byte {
		return append(slices.Clone(d[:len(d)-1]), ","+member+"}"...)
	}
}

// FuzzRegistered feeds arbitrary bytes as the answer to a creation. It must
// never panic, every refusal must be ErrInvalid, and what it accepts must hold
// the rules: the seeds are right answers, so the fuzzer does get in.
func FuzzRegistered(f *testing.F) {
	for _, c := range algorithms {
		a := webauthntest.New(c.alg)
		_, clientData, attestation := a.Create(rpID, origin, challenge, user)
		f.Add(clientData, attestation)
		a.Synced = true
		a.EditAuthData = flag(0x80, 0, cmap("credProtect", 2)...)
		_, clientData, attestation = a.Create(rpID, origin, challenge, user)
		f.Add(clientData, attestation)
	}
	for _, m := range []string{`"TYPE":"webauthn.get"`, `"Origin":"https://evil.example.com"`, `"cro\u017fsOrigin":true`} {
		a := webauthntest.New(algorithms[0].alg)
		a.EditClientData = inAnotherCase(m)
		_, clientData, attestation := a.Create(rpID, origin, challenge, user)
		f.Add(clientData, attestation)
	}
	f.Add([]byte("{}"), []byte{0xa0})
	f.Add([]byte{}, []byte{})

	hash := sha256.Sum256([]byte(rpID))
	get := []byte(`{"type":"webauthn.get","challenge":"` + b64(challenge) + `","origin":"` + origin + `"}`)
	f.Fuzz(func(t *testing.T, clientData, attestation []byte) {
		cred, err := rp.Registered(challenge, clientData, attestation)
		if err != nil {
			if !errors.Is(err, webauthn.ErrInvalid) || cred != nil {
				t.Fatalf("refused with %v and credential %v", err, cred)
			}
			return
		}
		asked(t, clientData, "webauthn.create")
		if len(cred.ID) < 1 || len(cred.ID) > 1023 || !slices.Contains(webauthn.Algorithms, cred.Algorithm) {
			t.Fatalf("accepted an identifier of %d bytes with algorithm %d", len(cred.ID), cred.Algorithm)
		}
		if !bytes.Contains(attestation, cred.PublicKey) || !bytes.Contains(attestation, cred.ID) || !bytes.Contains(attestation, hash[:]) {
			t.Fatal("the credential is not made of the answer's bytes")
		}
		// The key it gives is one a use can be checked with: with no signature,
		// the signature is the one thing wrong.
		_, err = rp.Asserted(challenge, cred.PublicKey, get, authData(0x05), nil)
		if err == nil || err.Error() != webauthn.ErrInvalid.Error()+": wrong signature" {
			t.Fatalf("a key that was accepted cannot be used: %v", err)
		}
	})
}

// FuzzAsserted feeds arbitrary bytes as a stored key and the answer to a use.
// It must never panic, every refusal must be ErrInvalid, and what it accepts
// must hold the rules.
func FuzzAsserted(f *testing.F) {
	for _, c := range algorithms {
		a := webauthntest.New(c.alg)
		id, clientData, attestation := a.Create(rpID, origin, challenge, user)
		cred, err := rp.Registered(challenge, clientData, attestation)
		if err != nil {
			f.Fatal(err)
		}
		clientData, data, signature, _ := a.Get(rpID, origin, challenge, id)
		f.Add(cred.PublicKey, clientData, data, signature)
		a.Synced = true
		a.EditAuthData = flag(0x80, 0, cmap("appid", true)...)
		clientData, data, signature, _ = a.Get(rpID, origin, challenge, id)
		f.Add(cred.PublicKey, clientData, data, signature)
	}
	for _, m := range []string{`"TYPE":"webauthn.create"`, `"Origin":"https://evil.example.com"`, `"cro\u017fsOrigin":true`} {
		a := webauthntest.New(algorithms[0].alg)
		id, clientData, attestation := a.Create(rpID, origin, challenge, user)
		cred, err := rp.Registered(challenge, clientData, attestation)
		if err != nil {
			f.Fatal(err)
		}
		a.EditClientData = inAnotherCase(m)
		clientData, data, signature, _ := a.Get(rpID, origin, challenge, id)
		f.Add(cred.PublicKey, clientData, data, signature)
	}
	f.Add([]byte{0xa0}, []byte("{}"), []byte{}, []byte{})

	hash := sha256.Sum256([]byte(rpID))
	f.Fuzz(func(t *testing.T, key, clientData, data, signature []byte) {
		got, err := rp.Asserted(challenge, key, clientData, data, signature)
		if err != nil {
			if !errors.Is(err, webauthn.ErrInvalid) || got != nil {
				t.Fatalf("refused with %v and assertion %v", err, got)
			}
			return
		}
		asked(t, clientData, "webauthn.get")
		if len(data) < 37 || !bytes.Equal(data[:32], hash[:]) {
			t.Fatalf("accepted authenticator data of another relying party: %x", data)
		}
		flags := data[32]
		if flags&0x05 != 0x05 || flags&0x40 != 0 || (flags&0x10 != 0) != got.Synced || (got.Synced && flags&0x08 == 0) {
			t.Fatalf("accepted flags %08b with synced %v", flags, got.Synced)
		}
		if (flags&0x80 == 0) != (len(data) == 37) {
			t.Fatalf("accepted %d bytes of authenticator data with flags %08b", len(data), flags)
		}
		if len(signature) == 0 || len(signature) > 1024 || len(key) > 1100 {
			t.Fatalf("accepted a signature of %d bytes with a key of %d", len(signature), len(key))
		}
	})
}
