// Package envelope encrypts stored data with AES-256-GCM, binding every
// ciphertext to its context (additional authenticated data) and to its key.
//
// AES-GCM is not key-committing: a crafted ciphertext can decrypt under two
// different keys. Each sealed value therefore carries a commitment to the key
// (an HMAC of a fixed label), checked before decryption, so a value sealed
// under one mailbox key can never be opened as if it belonged to another.
//
// Format: version (1 byte) | key commitment (32) | nonce (12) | ciphertext+tag.
//
// Values that must stay readable outside the enclave are signed instead of
// encrypted: see Sign.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	version    = 1
	commitSize = sha256.Size
	nonceSize  = 12
	// KeySize is the data key length (AES-256).
	KeySize = 32
)

// ErrOpen is returned for any ciphertext that does not authenticate: wrong
// key, wrong context, or tampered bytes. Callers cannot tell which.
var ErrOpen = errors.New("envelope: cannot open")

// AAD joins context parts unambiguously (each part is length-prefixed).
func AAD(parts ...string) []byte {
	var out []byte
	for _, p := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(p))) //nolint:gosec // parts are short identifiers
		out = append(out, p...)
	}
	return out
}

func commitment(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("permaily envelope key commitment v1"))
	return m.Sum(nil)
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("envelope: key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under key, bound to aad.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+commitSize+nonceSize+len(plaintext)+g.Overhead())
	out = append(out, version)
	out = append(out, commitment(key)...)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a value produced by Seal with the same key and aad.
func Open(key, sealed, aad []byte) ([]byte, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1+commitSize+nonceSize+g.Overhead() || sealed[0] != version {
		return nil, ErrOpen
	}
	if !hmac.Equal(sealed[1:1+commitSize], commitment(key)) {
		return nil, ErrOpen
	}
	nonce := sealed[1+commitSize : 1+commitSize+nonceSize]
	plain, err := g.Open(nil, nonce, sealed[1+commitSize+nonceSize:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
