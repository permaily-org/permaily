package envelope

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
)

// Some stored values are meant to be read by whoever runs the storage, and
// must still be made only by an enclave: an account's contact address, which
// support needs to see, and which would be a way into the account if it could
// be written by hand. Those are signed instead of sealed: the value stays in
// plain text, next to a code (HMAC-SHA-256) over the value and its context.
//
// Format: {"doc":<the value, a JSON document>,"mac":"<base64>"}.

// ErrSigned is returned for a signed value that does not verify: another key,
// another context, or changed bytes. Callers cannot tell which.
var ErrSigned = errors.New("envelope: the signature of a readable value does not verify")

type signed struct {
	Doc json.RawMessage `json:"doc"`
	MAC []byte          `json:"mac"`
}

func signature(key, doc, aad []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("permaily signed value v1"))
	// The context goes first with its length, so that no two pairs of
	// context and document give the same input.
	m.Write(AAD(string(aad)))
	m.Write(doc)
	return m.Sum(nil)
}

// Sign returns doc, a JSON document, with a code that binds it to key and
// aad. The result is JSON too, and doc can be read in it as it is.
func Sign(key, doc, aad []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, errors.New("envelope: a signing key must be 32 bytes")
	}
	if !json.Valid(doc) {
		return nil, errors.New("envelope: only a JSON document can be signed")
	}
	return json.Marshal(signed{Doc: doc, MAC: signature(key, doc, aad)})
}

// Verified returns the document inside a value made by Sign with the same
// key and aad.
func Verified(key, value, aad []byte) ([]byte, error) {
	var s signed
	if len(key) != KeySize || json.Unmarshal(value, &s) != nil || len(s.Doc) == 0 {
		return nil, ErrSigned
	}
	if !hmac.Equal(s.MAC, signature(key, s.Doc, aad)) {
		return nil, ErrSigned
	}
	return s.Doc, nil
}
