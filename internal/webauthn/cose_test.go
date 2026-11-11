package webauthn_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"math"
	"math/big"
	"slices"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

func edKey() []byte {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return webauthntest.COSEKey(public)
}

// point is a point of P-256, as the two coordinates of a COSE_Key.
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

func rsaKey(n, e []byte) []byte {
	return cmap(1, 3, 3, -257, -1, n, -2, e)
}

// modulus is an odd number of exactly this many bits. It is no product of two
// primes: reading a key does not ask for that, and could not.
func modulus(bits int) []byte {
	n := bytes.Repeat([]byte{0xff}, (bits+7)/8)
	n[0] = 0xff >> ((8 - bits%8) % 8)
	return n
}

func TestPublicKey(t *testing.T) {
	x, y := point()
	ed := make([]byte, 32)
	if _, err := rand.Read(ed); err != nil {
		t.Fatal(err)
	}
	e := []byte{1, 0, 1}
	n := modulus(2048)
	wrongY := slices.Clone(y)
	wrongY[31] ^= 1
	many := []any{}
	for i := range 17 {
		many = append(many, 100+i, 1)
	}

	const (
		p256    = "malformed P-256 key"
		ed25519 = "malformed Ed25519 key"
		rsaKind = "malformed RSA key"
		rsaN    = "RSA modulus not accepted"
		rsaE    = "RSA exponent not accepted"
		other   = "public key of a type or an algorithm not accepted"
		notMap  = "public key is not a strict CBOR map"
		notCBOR = "public key is not strict CBOR"
	)
	for _, c := range []struct {
		name string
		key  []byte
		alg  int
		why  string
	}{
		{name: "ES256", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y), alg: -7},
		{name: "ES256 with fields in another order", key: cmap(-3, y, -2, x, -1, 1, 3, -7, 1, 2), alg: -7},
		{name: "EdDSA", key: cmap(1, 1, 3, -8, -1, 6, -2, ed), alg: -8},
		{name: "RS256", key: rsaKey(n, e), alg: -257},
		{name: "RS256 with an exponent of 3", key: rsaKey(n, []byte{3}), alg: -257},
		{name: "RS256 with the largest exponent", key: rsaKey(n, []byte{0x7f, 0xff, 0xff, 0xff}), alg: -257},
		{name: "RS256 of 8192 bits", key: rsaKey(modulus(8192), e), alg: -257},

		{name: "ES256 on P-384", key: cmap(1, 2, 3, -7, -1, 2, -2, x, -3, y), why: p256},
		{name: "ES256 with no curve", key: cmap(1, 2, 3, -7, -2, x, -3, y), why: p256},
		{name: "ES256 with the curve under another label", key: cmap(1, 2, 3, -7, -5, 1, -2, x, -3, y), why: p256},
		{name: "ES256 with a curve that is text", key: cmap(1, 2, 3, -7, -1, "P-256", -2, x, -3, y), why: p256},
		{name: "ES256 with x of 31 bytes", key: cmap(1, 2, 3, -7, -1, 1, -2, x[:31], -3, y), why: p256},
		{name: "ES256 with x of 33 bytes", key: cmap(1, 2, 3, -7, -1, 1, -2, append([]byte{0}, x...), -3, y), why: p256},
		{name: "ES256 with y of 31 bytes", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y[:31]), why: p256},
		{name: "ES256 with y of 33 bytes", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, append([]byte{0}, y...)), why: p256},
		{name: "ES256 with a compressed point", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, true), why: p256},
		{name: "ES256 with no y", key: cmap(1, 2, 3, -7, -1, 1, -2, x), why: p256},
		{name: "ES256 with x that is text", key: cmap(1, 2, 3, -7, -1, 1, -2, string(bytes.Repeat([]byte{'a'}, 32)), -3, y), why: p256},
		{name: "ES256 with a field it does not know", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y, 2, []byte("kid")), why: p256},
		{name: "ES256 with the private key", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y, -4, x), why: p256},
		{name: "ES256 off the curve", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, wrongY), why: "P-256 key not on the curve"},
		{name: "ES256 at zero", key: cmap(1, 2, 3, -7, -1, 1, -2, make([]byte, 32), -3, make([]byte, 32)), why: "P-256 key not on the curve"},

		{name: "EdDSA on Ed448", key: cmap(1, 1, 3, -8, -1, 7, -2, ed), why: ed25519},
		{name: "EdDSA on X25519", key: cmap(1, 1, 3, -8, -1, 4, -2, ed), why: ed25519},
		{name: "EdDSA on P-256", key: cmap(1, 1, 3, -8, -1, 1, -2, ed), why: ed25519},
		{name: "EdDSA with no curve", key: cmap(1, 1, 3, -8, -2, ed), why: ed25519},
		{name: "EdDSA with x of 31 bytes", key: cmap(1, 1, 3, -8, -1, 6, -2, ed[:31]), why: ed25519},
		{name: "EdDSA with x of 33 bytes", key: cmap(1, 1, 3, -8, -1, 6, -2, append([]byte{0}, ed...)), why: ed25519},
		{name: "EdDSA with no x", key: cmap(1, 1, 3, -8, -1, 6), why: ed25519},
		{name: "EdDSA with a y", key: cmap(1, 1, 3, -8, -1, 6, -2, ed, -3, ed), why: ed25519},

		{name: "RS256 with no modulus", key: cmap(1, 3, 3, -257, -2, e), why: rsaKind},
		{name: "RS256 with no exponent", key: cmap(1, 3, 3, -257, -1, n), why: rsaKind},
		{name: "RS256 with an empty modulus", key: rsaKey([]byte{}, e), why: rsaKind},
		{name: "RS256 with an empty exponent", key: rsaKey(n, []byte{}), why: rsaKind},
		{name: "RS256 with a modulus that starts with zero", key: rsaKey(append([]byte{0}, n...), e), why: rsaKind},
		{name: "RS256 with an exponent that starts with zero", key: rsaKey(n, []byte{0, 1, 0, 1}), why: rsaKind},
		{name: "RS256 with an exponent of five bytes", key: rsaKey(n, []byte{1, 0, 0, 0, 1}), why: rsaKind},
		{name: "RS256 with a modulus that is a number", key: cmap(1, 3, 3, -257, -1, 65537, -2, e), why: rsaKind},
		{name: "RS256 with a field it does not know", key: cmap(1, 3, 3, -257, -1, n, -2, e, -3, n), why: rsaKind},
		{name: "RS256 of 2047 bits", key: rsaKey(modulus(2047), e), why: rsaN},
		{name: "RS256 of 1024 bits", key: rsaKey(modulus(1024), e), why: rsaN},
		{name: "RS256 of 8193 bits", key: rsaKey(modulus(8193), e), why: rsaN},
		{name: "RS256 with an even modulus", key: rsaKey(append(slices.Clone(n[:255]), 0xfe), e), why: rsaN},
		{name: "RS256 with an exponent of 1", key: rsaKey(n, []byte{1}), why: rsaE},
		{name: "RS256 with an exponent of 2", key: rsaKey(n, []byte{2}), why: rsaE},
		{name: "RS256 with an even exponent", key: rsaKey(n, []byte{1, 0, 0}), why: rsaE},
		{name: "RS256 with an exponent too large", key: rsaKey(n, []byte{0x80, 0, 0, 1}), why: rsaE},

		{name: "a P-256 key that says EdDSA", key: cmap(1, 2, 3, -8, -1, 1, -2, x, -3, y), why: other},
		{name: "a P-256 key that says RS256", key: cmap(1, 2, 3, -257, -1, 1, -2, x, -3, y), why: other},
		{name: "an Ed25519 key that says ES256", key: cmap(1, 1, 3, -7, -1, 6, -2, ed), why: other},
		{name: "an RSA key that says ES256", key: cmap(1, 3, 3, -7, -1, n, -2, e), why: other},
		{name: "an RSA key that says EdDSA", key: cmap(1, 3, 3, -8, -1, n, -2, e), why: other},
		{name: "ES384", key: cmap(1, 2, 3, -35, -1, 2, -2, make([]byte, 48), -3, make([]byte, 48)), why: other},
		{name: "ES256K", key: cmap(1, 2, 3, -47, -1, 8, -2, x, -3, y), why: other},
		{name: "PS256", key: cmap(1, 3, 3, -37, -1, n, -2, e), why: other},
		{name: "RS1", key: cmap(1, 3, 3, -65535, -1, n, -2, e), why: other},
		{name: "a symmetric key", key: cmap(1, 4, 3, 5, -1, ed), why: other},
		{name: "no type", key: cmap(3, -7, -1, 1, -2, x, -3, y), why: other},
		{name: "no algorithm", key: cmap(1, 2, -1, 1, -2, x, -3, y), why: other},
		{name: "a type that is text", key: cmap(1, "EC2", 3, -7, -1, 1, -2, x, -3, y), why: other},
		{name: "an algorithm that is text", key: cmap(1, 2, 3, "ES256", -1, 1, -2, x, -3, y), why: other},
		{name: "a type too large for a number", key: cmap(1, uint64(math.MaxUint64), 3, -7, -1, 1, -2, x, -3, y), why: other},
		{name: "an algorithm too small for a number", key: cmap(1, 2, 3, cbor.RawMessage{0x3b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, -1, 1, -2, x, -3, y), why: other},
		{name: "an empty map", key: []byte{0xa0}, why: other},

		{name: "a label twice", key: cmap(1, 2, 3, -7, -1, 1, -2, x, -3, y, -3, y), why: notMap},
		{name: "a label twice, written two ways", key: cmap(1, 2, cbor.RawMessage{0x18, 0x01}, 2, 3, -7, -1, 1, -2, x, -3, y), why: notMap},
		{name: "a label that is text", key: cmap("kty", 2, 3, -7, -1, 1, -2, x, -3, y), why: notMap},
		{name: "a label too large for a number", key: cmap(uint64(math.MaxUint64), 2, 3, -7, -1, 1, -2, x, -3, y), why: notMap},
		{name: "a list", key: enc([]any{2, -7, 1, x, y}), why: notMap},
		{name: "null", key: []byte{0xf6}, why: notMap},
		{name: "a number", key: []byte{0x02}, why: notMap},
		{name: "more fields than a key has", key: cmap(many...), why: notCBOR},
		{name: "too long", key: rsaKey(make([]byte, 1100), e), why: "public key too long"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cred, err := made(attestationOf(authData(made0, attested(-1, []byte("an identifier"), c.key))))
			outcome(t, "create", err, c.why)
			if c.why == "" && (cred.Algorithm != c.alg || !bytes.Equal(cred.PublicKey, c.key)) {
				t.Fatalf("got algorithm %d and key %x", cred.Algorithm, cred.PublicKey)
			}
		})
	}
}

// An RSA key with a public exponent other than 65537 signs and is accepted:
// the exponent is the key's own, not a constant of this code.
func TestRSAWithAnExponentOfThree(t *testing.T) {
	three := big.NewInt(3)
	prime := func() *big.Int {
		for {
			p, err := rand.Prime(rand.Reader, 1024)
			if err != nil {
				t.Fatal(err)
			}
			// 3 must have an inverse modulo p-1.
			if new(big.Int).Mod(p, three).Int64() == 2 {
				return p
			}
		}
	}
	p, q := prime(), prime()
	one := big.NewInt(1)
	phi := new(big.Int).Mul(new(big.Int).Sub(p, one), new(big.Int).Sub(q, one))
	private := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: new(big.Int).Mul(p, q), E: 3},
		D:         new(big.Int).ModInverse(three, phi),
		Primes:    []*big.Int{p, q},
	}
	private.Precompute()

	a, cred := passkey(t, webauthntest.ES256)
	clientData, authData, _, _ := a.Get(rpID, origin, challenge, cred.ID)
	hash := sha256.Sum256(clientData)
	digest := sha256.Sum256(slices.Concat(authData, hash[:]))
	signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	key := webauthntest.COSEKey(&private.PublicKey)
	if _, err := rp.Asserted(challenge, key, clientData, authData, signature); err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	_, err = rp.Asserted(challenge, key, clientData, authData, signature)
	refused(t, err, "wrong signature")
}
