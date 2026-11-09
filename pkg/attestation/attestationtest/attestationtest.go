// Package attestationtest builds signed attestation documents with a
// throwaway certificate chain. It exists for tests and for the enclave's dev
// mode; documents it produces never verify against the AWS root.
package attestationtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// CA is a root -> intermediate -> leaf chain that signs documents.
type CA struct {
	Root         *x509.Certificate
	Intermediate *x509.Certificate
	Leaf         *x509.Certificate
	leafKey      *ecdsa.PrivateKey
}

// NewCA creates a fresh chain valid from now-1h to now+24h.
func NewCA() (*CA, error) {
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	root, err := issue(nil, nil, rootKey, "test-root", true, now)
	if err != nil {
		return nil, err
	}
	interKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	inter, err := issue(root, rootKey, interKey, "test-intermediate", true, now)
	if err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leaf, err := issue(inter, interKey, leafKey, "test-enclave", false, now)
	if err != nil {
		return nil, err
	}
	return &CA{Root: root, Intermediate: inter, Leaf: leaf, leafKey: leafKey}, nil
}

func issue(parent *x509.Certificate, parentKey, key *ecdsa.PrivateKey, cn string, isCA bool, now time.Time) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// Fields are the document contents the caller controls.
type Fields struct {
	ModuleID  string
	Timestamp time.Time
	PCRs      map[uint][]byte
	UserData  []byte
	Nonce     []byte
	PublicKey []byte
}

// PCRs returns PCR0..PCR2 filled with the given byte, useful as fixtures.
func PCRs(fill byte) map[uint][]byte {
	m := map[uint][]byte{}
	for _, i := range attestation.MeasuredPCRs {
		v := make([]byte, sha512.Size384)
		for j := range v {
			v[j] = fill
		}
		m[i] = v
	}
	return m
}

// Sign returns a COSE_Sign1 attestation document signed by the leaf.
func (ca *CA) Sign(f Fields) ([]byte, error) {
	if f.ModuleID == "" {
		f.ModuleID = "i-test-enc0000000000000000"
	}
	if f.Timestamp.IsZero() {
		f.Timestamp = time.Now()
	}
	payload, err := cbor.Marshal(map[string]any{
		"module_id":   f.ModuleID,
		"digest":      "SHA384",
		"timestamp":   uint64(f.Timestamp.UnixMilli()),
		"pcrs":        f.PCRs,
		"certificate": ca.Leaf.Raw,
		"cabundle":    [][]byte{ca.Root.Raw, ca.Intermediate.Raw},
		"public_key":  f.PublicKey,
		"user_data":   f.UserData,
		"nonce":       f.Nonce,
	})
	if err != nil {
		return nil, err
	}
	protected, err := cbor.Marshal(map[int]int{1: -35})
	if err != nil {
		return nil, err
	}
	tbs, err := attestation.SigStructure(protected, payload)
	if err != nil {
		return nil, err
	}
	digest := sha512.Sum384(tbs)
	r, s, err := ecdsa.Sign(rand.Reader, ca.leafKey, digest[:])
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 96)
	r.FillBytes(sig[:48])
	s.FillBytes(sig[48:])
	return cbor.Marshal([]any{protected, map[any]any{}, payload, sig})
}
