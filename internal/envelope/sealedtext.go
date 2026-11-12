package envelope

import (
	"encoding/json"
	"errors"
)

// Some stored values must be text and must not be read by whoever runs the
// storage: what a sign-in that was started keeps until it ends (the hash of
// a six-digit code, which anyone who reads it can try a million times offline,
// a link's token, a challenge, a PKCE verifier). Those are sealed as Seal
// does, and kept as a JSON document that holds nothing but the sealed bytes.
//
// Format: {"sealed":"<base64>"}.

type sealedText struct {
	Sealed []byte `json:"sealed"`
}

// SealText seals plaintext under key, bound to aad, as a JSON document.
func SealText(key, plaintext, aad []byte) ([]byte, error) {
	sealed, err := Seal(key, plaintext, aad)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedText{Sealed: sealed})
}

// OpenText opens a value made by SealText with the same key and aad. Every
// failure is ErrOpen.
func OpenText(key, value, aad []byte) ([]byte, error) {
	var s sealedText
	if json.Unmarshal(value, &s) != nil || len(s.Sealed) == 0 {
		return nil, ErrOpen
	}
	plain, err := Open(key, s.Sealed, aad)
	if err != nil {
		if errors.Is(err, ErrOpen) {
			return nil, ErrOpen
		}
		return nil, err
	}
	return plain, nil
}
