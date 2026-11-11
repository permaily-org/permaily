package webauthn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"math"
	"math/big"
	"slices"
)

// COSE algorithms (RFC 9053, RFC 8812).
const (
	algES256 = -7
	algEdDSA = -8
	algRS256 = -257
)

// Labels of a COSE_Key (RFC 9052 section 7, RFC 9053 section 7, RFC 8230 section 4).
const (
	labelKty = 1
	labelAlg = 3
	labelCrv = -1 // n for RSA
	labelX   = -2 // e for RSA
	labelY   = -3

	ktyOKP = 1
	ktyEC2 = 2
	ktyRSA = 3

	crvP256    = 1
	crvEd25519 = 6
)

// key is a public key that passed every check, ready to verify with.
type key struct {
	alg int
	ec  *ecdsa.PublicKey
	ed  ed25519.PublicKey
	rsa *rsa.PublicKey
}

// parseKey reads a COSE_Key. A key holds exactly the fields of its algorithm
// and no other (WebAuthn section 6.5.1), so a field this code does not know
// can never change what the key means.
func parseKey(raw []byte) (*key, error) {
	if len(raw) > maxKey {
		return nil, refuse("public key too long")
	}
	var fields map[int]any
	if major(raw) != cborMap || strict.Unmarshal(raw, &fields) != nil {
		return nil, refuse("public key is not a strict CBOR map")
	}
	kty, _ := integer(fields[labelKty])
	alg, _ := integer(fields[labelAlg])
	switch {
	case kty == ktyEC2 && alg == algES256:
		return parseES256(fields)
	case kty == ktyOKP && alg == algEdDSA:
		return parseEdDSA(fields)
	case kty == ktyRSA && alg == algRS256:
		return parseRS256(fields)
	}
	return nil, refuse("public key of a type or an algorithm not accepted")
}

func parseES256(fields map[int]any) (*key, error) {
	crv, _ := integer(fields[labelCrv])
	x, _ := fields[labelX].([]byte)
	y, _ := fields[labelY].([]byte) // a boolean here would be a compressed point: not accepted
	if len(fields) != 5 || crv != crvP256 || len(x) != 32 || len(y) != 32 {
		return nil, refuse("malformed P-256 key")
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, x, y))
	if err != nil {
		return nil, refuse("P-256 key not on the curve")
	}
	return &key{alg: algES256, ec: pub}, nil
}

func parseEdDSA(fields map[int]any) (*key, error) {
	crv, _ := integer(fields[labelCrv])
	x, _ := fields[labelX].([]byte)
	if len(fields) != 4 || crv != crvEd25519 || len(x) != ed25519.PublicKeySize {
		return nil, refuse("malformed Ed25519 key")
	}
	return &key{alg: algEdDSA, ed: ed25519.PublicKey(x)}, nil
}

func parseRS256(fields map[int]any) (*key, error) {
	n, _ := fields[labelCrv].([]byte)
	e, _ := fields[labelX].([]byte)
	// Both are written in as few bytes as they need (RFC 8230), so a key has one writing.
	if len(fields) != 4 || len(n) == 0 || n[0] == 0 || len(e) == 0 || len(e) > 4 || e[0] == 0 {
		return nil, refuse("malformed RSA key")
	}
	modulus := new(big.Int).SetBytes(n)
	if bits := modulus.BitLen(); bits < 2048 || bits > 8192 || modulus.Bit(0) == 0 {
		return nil, refuse("RSA modulus not accepted")
	}
	exponent := new(big.Int).SetBytes(e).Int64()
	if exponent < 3 || exponent&1 == 0 || exponent > math.MaxInt32 {
		return nil, refuse("RSA exponent not accepted")
	}
	return &key{alg: algRS256, rsa: &rsa.PublicKey{N: modulus, E: int(exponent)}}, nil
}

// integer is a CBOR integer: the decoder gives the positive ones as uint64.
func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case int64:
		return n, true
	case uint64:
		if n <= math.MaxInt64 {
			return int64(n), true
		}
	}
	return 0, false
}

// verify checks a signature over message with the key's one algorithm.
func (k *key) verify(message, signature []byte) bool {
	digest := sha256.Sum256(message)
	switch k.alg {
	case algES256:
		return ecdsa.VerifyASN1(k.ec, digest[:], signature)
	case algEdDSA:
		return ed25519.Verify(k.ed, message, signature)
	default:
		return rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, digest[:], signature) == nil
	}
}
