// Package provenancetest signs release bundles with a throwaway certificate
// authority and transparency log, shaped like the ones GitHub and Sigstore
// produce. It exists for tests and for the enclave's dev mode; its bundles
// never verify against Sigstore's public trust root.
package provenancetest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
)

// Authority is a certificate authority (root and intermediate) and a log.
type Authority struct {
	Root, Intermediate *x509.Certificate
	interKey           *ecdsa.PrivateKey
	logKey             *ecdsa.PrivateKey
	// LogID and Origin identify the log.
	LogID  []byte
	Origin string
	// Before and After are how many other entries the log holds around each
	// signature, so inclusion proofs are not trivial.
	Before, After int
	index         int64
}

// Build describes the workflow run a bundle claims to come from.
type Build struct {
	Repository   string // owner/name
	RepositoryID string
	OwnerID      string
	Workflow     string
	Ref          string
	Commit       string
	// Runner defaults to github-hosted and Issuer to GitHub's token issuer.
	Runner string
	Issuer string
	// At is when the log records the signature. Defaults to now.
	At time.Time
	// IssuedAt is when the signing certificate, valid for ten minutes, was
	// issued. Defaults to a minute before At.
	IssuedAt time.Time
}

// New creates an authority and a log, valid from thirty days ago for ten years.
func New() (*Authority, error) {
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	root, err := issueCA(nil, rootKey, rootKey, "test-fulcio-root", now)
	if err != nil {
		return nil, err
	}
	interKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	inter, err := issueCA(root, rootKey, interKey, "test-fulcio-intermediate", now)
	if err != nil {
		return nil, err
	}
	logKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&logKey.PublicKey)
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256(der)
	return &Authority{Root: root, Intermediate: inter, interKey: interKey, logKey: logKey, LogID: id[:], Origin: "rekor.test", Before: 4, After: 2, index: 1000}, nil
}

func issueCA(parent *x509.Certificate, parentKey, key *ecdsa.PrivateKey, cn string, now time.Time) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.AddDate(0, 0, -30),
		NotAfter:              now.AddDate(10, 0, 0),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// RekorURL is the base URL the test log has in TrustedRootJSON.
const RekorURL = "https://rekor.test"

// TrustedRootJSON returns the authority and the log in the format of
// Sigstore's trusted_root.json.
func (a *Authority) TrustedRootJSON() ([]byte, error) {
	logKey, err := x509.MarshalPKIXPublicKey(&a.logKey.PublicKey)
	if err != nil {
		return nil, err
	}
	start := map[string]string{"start": a.Root.NotBefore.UTC().Format(time.RFC3339)}
	return json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"tlogs": []map[string]any{{
			"baseUrl":       RekorURL,
			"hashAlgorithm": "SHA2_256",
			"publicKey":     map[string]any{"rawBytes": logKey, "keyDetails": "PKIX_ECDSA_P256_SHA_256", "validFor": start},
			"logId":         map[string]any{"keyId": a.LogID},
		}},
		"certificateAuthorities": []map[string]any{{
			"subject":   map[string]string{"organization": "test", "commonName": "test-fulcio"},
			"uri":       "https://fulcio.test",
			"certChain": map[string]any{"certificates": []map[string]any{{"rawBytes": a.Intermediate.Raw}, {"rawBytes": a.Root.Raw}}},
			"validFor":  start,
		}},
	})
}

// TrustRoot returns the trust root that accepts this authority's bundles.
func (a *Authority) TrustRoot() *provenance.TrustRoot {
	doc, err := a.TrustedRootJSON()
	if err != nil {
		panic(err)
	}
	tr, err := provenance.ParseTrustRoot(doc, RekorURL)
	if err != nil {
		panic(err)
	}
	return tr
}

func utf8Ext(arc int, value string) (pkix.Extension, error) {
	der, err := asn1.MarshalWithParams(value, "utf8")
	return pkix.Extension{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, arc}, Value: der}, err
}

// Attest returns a bundle saying that the workflow run b built files, the way
// GitHub's artifact attestations do: build provenance naming the SHA-256 of
// each file, signed with a ten-minute certificate and recorded in the log.
func (a *Authority) Attest(b Build, files ...[]byte) ([]byte, error) {
	if b.Runner == "" {
		b.Runner = "github-hosted"
	}
	if b.Issuer == "" {
		b.Issuer = "https://token.actions.githubusercontent.com"
	}
	if b.At.IsZero() {
		b.At = time.Now()
	}
	at := b.At.UTC().Truncate(time.Second)
	if b.IssuedAt.IsZero() {
		b.IssuedAt = at.Add(-time.Minute)
	}
	repoURL := "https://github.com/" + b.Repository

	// The signing certificate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	san, err := url.Parse(repoURL + "/" + b.Workflow + "@" + b.Ref)
	if err != nil {
		return nil, err
	}
	var exts []pkix.Extension
	for arc, v := range map[int]string{8: b.Issuer, 11: b.Runner, 12: repoURL, 13: b.Commit, 14: b.Ref, 15: b.RepositoryID, 17: b.OwnerID} {
		if v == "" {
			continue
		}
		e, err := utf8Ext(arc, v)
		if err != nil {
			return nil, err
		}
		exts = append(exts, e)
	}
	slices.SortFunc(exts, func(x, y pkix.Extension) int { return x.Id[len(x.Id)-1] - y.Id[len(y.Id)-1] })
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:    serial,
		NotBefore:       b.IssuedAt,
		NotAfter:        b.IssuedAt.Add(10 * time.Minute),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:            []*url.URL{san},
		ExtraExtensions: exts,
	}, a.Intermediate, &key.PublicKey, a.interKey)
	if err != nil {
		return nil, err
	}

	// The statement and its DSSE signature.
	type subject struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}
	var subjects []subject
	for i, f := range files {
		sum := sha256.Sum256(f)
		subjects = append(subjects, subject{Name: fmt.Sprintf("file-%d", i), Digest: map[string]string{"sha256": hex.EncodeToString(sum[:])}})
	}
	payload, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       subjects,
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	const payloadType = "application/vnd.in-toto+json"
	sig, err := sign(key, fmt.Appendf(nil, "DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
	if err != nil {
		return nil, err
	}

	// The log entry: its body, the signed entry timestamp, and an inclusion
	// proof in a small tree with a signed checkpoint.
	envelope := map[string]any{
		"payload":     payload,
		"payloadType": payloadType,
		"signatures":  []map[string]any{{"sig": sig}},
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	payloadHash, envelopeHash := sha256.Sum256(payload), sha256.Sum256(envelopeJSON)
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.1",
		"kind":       "dsse",
		"spec": map[string]any{
			"envelopeHash": map[string]string{"algorithm": "sha256", "value": hex.EncodeToString(envelopeHash[:])},
			"payloadHash":  map[string]string{"algorithm": "sha256", "value": hex.EncodeToString(payloadHash[:])},
			"signatures": []map[string]any{{
				"signature": sig,
				"verifier":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
			}},
		},
	})
	if err != nil {
		return nil, err
	}
	a.index++
	set, err := sign(a.logKey, fmt.Appendf(nil, `{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString(body), at.Unix(), hex.EncodeToString(a.LogID), a.index))
	if err != nil {
		return nil, err
	}
	var leaves [][32]byte
	for i := range a.Before + 1 + a.After {
		leaf := sha256.Sum256(fmt.Appendf([]byte{0}, "other entry %d", i))
		if i == a.Before {
			leaf = sha256.Sum256(append([]byte{0}, body...))
		}
		leaves = append(leaves, leaf)
	}
	root := treeHash(leaves)
	size := fmt.Sprint(len(leaves))
	note := fmt.Sprintf("%s - 1\n%s\n%s\n", a.Origin, size, base64.StdEncoding.EncodeToString(root[:]))
	noteSig, err := sign(a.logKey, []byte(note))
	if err != nil {
		return nil, err
	}
	checkpoint := note + "\n— " + a.Origin + " " + base64.StdEncoding.EncodeToString(append(slices.Clone(a.LogID[:4]), noteSig...)) + "\n"

	return json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{
			"certificate": map[string]any{"rawBytes": certDER},
			"tlogEntries": []map[string]any{{
				"logIndex":         fmt.Sprint(a.index),
				"logId":            map[string]any{"keyId": a.LogID},
				"kindVersion":      map[string]string{"kind": "dsse", "version": "0.0.1"},
				"integratedTime":   fmt.Sprint(at.Unix()),
				"inclusionPromise": map[string]any{"signedEntryTimestamp": set},
				"inclusionProof": map[string]any{
					"logIndex":   fmt.Sprint(a.Before),
					"rootHash":   root[:],
					"treeSize":   size,
					"hashes":     treePath(a.Before, leaves),
					"checkpoint": map[string]string{"envelope": checkpoint},
				},
				"canonicalizedBody": body,
			}},
		},
		"dsseEnvelope": envelope,
	})
}

func sign(key *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	h := sha256.Sum256(msg)
	return ecdsa.SignASN1(rand.Reader, key, h[:])
}

// split returns the largest power of two smaller than n (RFC 6962, 2.1).
func split(n int) int {
	k := 1
	for k*2 < n {
		k *= 2
	}
	return k
}

func treeHash(leaves [][32]byte) [32]byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	k := split(len(leaves))
	l, r := treeHash(leaves[:k]), treeHash(leaves[k:])
	return sha256.Sum256(slices.Concat([]byte{1}, l[:], r[:]))
}

// treePath is the audit path of leaf m (RFC 6962, 2.1.1), leaf side first.
func treePath(m int, leaves [][32]byte) [][]byte {
	if len(leaves) == 1 {
		return [][]byte{}
	}
	k := split(len(leaves))
	if m < k {
		r := treeHash(leaves[k:])
		return append(treePath(m, leaves[:k]), r[:])
	}
	l := treeHash(leaves[:k])
	return append(treePath(m-k, leaves[k:]), l[:])
}
