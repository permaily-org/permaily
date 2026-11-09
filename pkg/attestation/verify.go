// Package attestation verifies AWS Nitro Enclaves attestation documents.
//
// An attestation document is a COSE_Sign1 structure (RFC 9052) signed with
// ECDSA P-384 by a certificate that chains up to the AWS Nitro Enclaves root
// certificate. Verify checks the signature, the certificate chain, freshness,
// the nonce, the user data, and the platform configuration registers (PCRs).
package attestation

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha512"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Sentinel errors. Use errors.Is to classify a verification failure.
var (
	ErrMalformed   = errors.New("attestation: malformed document")
	ErrSignature   = errors.New("attestation: invalid signature")
	ErrChain       = errors.New("attestation: invalid certificate chain")
	ErrStale       = errors.New("attestation: document timestamp outside the allowed window")
	ErrNonce       = errors.New("attestation: nonce mismatch")
	ErrUserData    = errors.New("attestation: user data mismatch")
	ErrDebugMode   = errors.New("attestation: PCRs are zero (enclave runs in debug mode)")
	ErrPCRMismatch = errors.New("attestation: PCR mismatch")
)

// coseAlgES384 is the COSE algorithm identifier for ECDSA with SHA-384.
const coseAlgES384 = -35

// DefaultMaxAge is how old (or how far in the future) a document may be.
const DefaultMaxAge = 5 * time.Minute

// MeasuredPCRs are the PCRs that identify the enclave image:
// 0 = whole image, 1 = kernel and bootstrap, 2 = application.
var MeasuredPCRs = []uint{0, 1, 2}

//go:embed aws_nitro_enclaves_root_g1.pem
var awsRootPEM []byte

// AWSRootFingerprint is the SHA-256 fingerprint of the AWS Nitro Enclaves
// root certificate (G1), as published by AWS.
const AWSRootFingerprint = "641a0321a3e244efe456463195d606317ed7cdcc3c1756e09893f3c68f79bb5b"

// AWSRoot returns the embedded AWS Nitro Enclaves root certificate.
func AWSRoot() *x509.Certificate {
	block, _ := pem.Decode(awsRootPEM)
	if block == nil {
		panic("attestation: embedded AWS root is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic("attestation: embedded AWS root: " + err.Error())
	}
	return cert
}

// Document is a verified attestation document.
type Document struct {
	ModuleID    string
	Timestamp   time.Time
	PCRs        map[uint][]byte
	Certificate *x509.Certificate
	CABundle    []*x509.Certificate
	PublicKey   []byte
	UserData    []byte
	Nonce       []byte
}

// Options control verification. Nonce, UserData and ExpectedPCRs are only
// checked when set; a verifier that wants end-to-end guarantees sets all three.
type Options struct {
	// Root is the trusted root certificate. Defaults to AWSRoot().
	Root *x509.Certificate
	// Now is the current time. Defaults to time.Now().
	Now time.Time
	// MaxAge bounds |Now - document timestamp|. Defaults to DefaultMaxAge.
	MaxAge time.Duration
	// Nonce must equal the document nonce when non-nil.
	Nonce []byte
	// UserData must equal the document user data when non-nil.
	UserData []byte
	// ExpectedPCRs must each equal the document PCR with the same index.
	ExpectedPCRs map[uint][]byte
}

type coseSign1 struct {
	_           struct{} `cbor:",toarray"`
	Protected   []byte
	Unprotected cbor.RawMessage
	Payload     []byte
	Signature   []byte
}

type payload struct {
	ModuleID    string          `cbor:"module_id"`
	Digest      string          `cbor:"digest"`
	Timestamp   uint64          `cbor:"timestamp"`
	PCRs        map[uint][]byte `cbor:"pcrs"`
	Certificate []byte          `cbor:"certificate"`
	CABundle    [][]byte        `cbor:"cabundle"`
	PublicKey   []byte          `cbor:"public_key"`
	UserData    []byte          `cbor:"user_data"`
	Nonce       []byte          `cbor:"nonce"`
}

// Verify parses and verifies a raw attestation document.
func Verify(raw []byte, opts Options) (*Document, error) {
	if opts.Root == nil {
		opts.Root = AWSRoot()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}

	var msg coseSign1
	if err := cbor.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("%w: COSE_Sign1: %w", ErrMalformed, err)
	}
	var protected map[int]int
	if err := cbor.Unmarshal(msg.Protected, &protected); err != nil {
		return nil, fmt.Errorf("%w: protected header: %w", ErrMalformed, err)
	}
	if protected[1] != coseAlgES384 {
		return nil, fmt.Errorf("%w: unsupported algorithm %d", ErrMalformed, protected[1])
	}
	var p payload
	if err := cbor.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("%w: payload: %w", ErrMalformed, err)
	}
	if p.Digest != "SHA384" || p.ModuleID == "" || p.Timestamp == 0 || len(p.Certificate) == 0 || len(p.CABundle) == 0 {
		return nil, fmt.Errorf("%w: missing mandatory fields", ErrMalformed)
	}

	leaf, err := x509.ParseCertificate(p.Certificate)
	if err != nil {
		return nil, fmt.Errorf("%w: certificate: %w", ErrMalformed, err)
	}
	bundle := make([]*x509.Certificate, len(p.CABundle))
	for i, der := range p.CABundle {
		if bundle[i], err = x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("%w: cabundle[%d]: %w", ErrMalformed, i, err)
		}
	}

	if err := verifySignature(msg, leaf); err != nil {
		return nil, err
	}

	if p.Timestamp > math.MaxInt64 {
		return nil, fmt.Errorf("%w: timestamp out of range", ErrMalformed)
	}
	ts := time.UnixMilli(int64(p.Timestamp))
	if err := verifyChain(leaf, bundle, opts.Root, ts); err != nil {
		return nil, err
	}
	if d := opts.Now.Sub(ts); d > opts.MaxAge || d < -opts.MaxAge {
		return nil, fmt.Errorf("%w: timestamp %s, now %s", ErrStale, ts.UTC().Format(time.RFC3339), opts.Now.UTC().Format(time.RFC3339))
	}
	if opts.Nonce != nil && !bytes.Equal(opts.Nonce, p.Nonce) {
		return nil, ErrNonce
	}
	if opts.UserData != nil && !bytes.Equal(opts.UserData, p.UserData) {
		return nil, fmt.Errorf("%w: got %x, want %x", ErrUserData, p.UserData, opts.UserData)
	}
	for _, i := range MeasuredPCRs {
		v, ok := p.PCRs[i]
		if !ok || len(v) != sha512.Size384 {
			return nil, fmt.Errorf("%w: PCR%d missing or wrong length", ErrMalformed, i)
		}
		if isZero(v) {
			return nil, ErrDebugMode
		}
	}
	for i, want := range opts.ExpectedPCRs {
		if got := p.PCRs[i]; !bytes.Equal(got, want) {
			return nil, fmt.Errorf("%w: PCR%d is %s, expected %s", ErrPCRMismatch, i, hex.EncodeToString(got), hex.EncodeToString(want))
		}
	}

	return &Document{
		ModuleID:    p.ModuleID,
		Timestamp:   ts,
		PCRs:        p.PCRs,
		Certificate: leaf,
		CABundle:    bundle,
		PublicKey:   p.PublicKey,
		UserData:    p.UserData,
		Nonce:       p.Nonce,
	}, nil
}

func verifySignature(msg coseSign1, leaf *x509.Certificate) error {
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P384() {
		return fmt.Errorf("%w: signing key is not ECDSA P-384", ErrSignature)
	}
	if len(msg.Signature) != 96 {
		return fmt.Errorf("%w: signature length %d", ErrSignature, len(msg.Signature))
	}
	toBeSigned, err := SigStructure(msg.Protected, msg.Payload)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	digest := sha512.Sum384(toBeSigned)
	r := new(big.Int).SetBytes(msg.Signature[:48])
	s := new(big.Int).SetBytes(msg.Signature[48:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return ErrSignature
	}
	return nil
}

// SigStructure returns the COSE Sig_structure for a COSE_Sign1 message with
// an empty external AAD (RFC 9052, section 4.4).
func SigStructure(protected, payload []byte) ([]byte, error) {
	return cbor.Marshal([]any{"Signature1", protected, []byte{}, payload})
}

func verifyChain(leaf *x509.Certificate, bundle []*x509.Certificate, root *x509.Certificate, at time.Time) error {
	// cabundle[0] is the root; the rest are intermediates ordered towards the leaf.
	if !bytes.Equal(bundle[0].Raw, root.Raw) {
		return fmt.Errorf("%w: cabundle does not start with the trusted root", ErrChain)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inter := x509.NewCertPool()
	for _, c := range bundle[1:] {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrChain, err)
	}
	return nil
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
