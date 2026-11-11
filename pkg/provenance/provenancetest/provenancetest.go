// Package provenancetest signs release bundles with a throwaway certificate
// authority and transparency log, shaped like the ones GitHub and Sigstore
// produce. It exists for tests and for the enclave's dev mode; its bundles
// never verify against Sigstore's public trust root.
package provenancetest

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
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

// Authority is a certificate authority (root and intermediate), a log of each
// generation of Rekor and a timestamp authority.
type Authority struct {
	Root, Intermediate *x509.Certificate
	interKey           *ecdsa.PrivateKey
	logKey             *ecdsa.PrivateKey
	// LogID and Origin identify the log shaped like the first Rekor.
	LogID  []byte
	Origin string
	// Log2ID and Origin2 identify the log shaped like Rekor v2, which signs
	// its checkpoints with Ed25519 and no time.
	Log2ID  []byte
	Origin2 string
	log2Key ed25519.PrivateKey
	// TSARoot and TSALeaf are the timestamp authority's certificates.
	TSARoot, TSALeaf *x509.Certificate
	tsaKey           *ecdsa.PrivateKey
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
	// Rekor2 records the signature in the log shaped like Rekor v2, with a
	// timestamp from the timestamp authority, instead of in the first one.
	Rekor2 bool
	// At is when the log records the signature, or for Rekor2 the time of the
	// timestamp. Defaults to now.
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
	a := &Authority{Root: root, Intermediate: inter, interKey: interKey, logKey: logKey, LogID: id[:], Origin: "rekor.test", Before: 4, After: 2, index: 1000}

	// The second log. Its id is the key id of its checkpoint signatures
	// (c2sp.org/signed-note), not truncated.
	a.Origin2 = "log2099-1.rekor.test"
	log2Pub, log2Key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	id2 := sha256.Sum256(slices.Concat([]byte(a.Origin2+"\n"), []byte{1}, log2Pub))
	a.log2Key, a.Log2ID = log2Key, id2[:]

	// The timestamp authority: a root and the certificate it signs with.
	tsaRootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	if a.TSARoot, err = issue(nil, tsaRootKey, &tsaRootKey.PublicKey, &x509.Certificate{
		Subject: pkix.Name{CommonName: "test-tsa-root"}, BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}, now); err != nil {
		return nil, err
	}
	if a.tsaKey, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
		return nil, err
	}
	// RFC 3161: the extended key usage is timestamping, alone and critical.
	eku, err := asn1.Marshal([]asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 7, 3, 8}})
	if err != nil {
		return nil, err
	}
	if a.TSALeaf, err = issue(a.TSARoot, tsaRootKey, &a.tsaKey.PublicKey, &x509.Certificate{
		Subject: pkix.Name{CommonName: "test-tsa"}, KeyUsage: x509.KeyUsageDigitalSignature, SubjectKeyId: []byte("test-tsa key id"),
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 37}, Critical: true, Value: eku}},
	}, now); err != nil {
		return nil, err
	}
	return a, nil
}

// issue signs a certificate valid from thirty days ago for ten years; a nil
// parent makes it self-signed.
func issue(parent *x509.Certificate, parentKey *ecdsa.PrivateKey, pub *ecdsa.PublicKey, tmpl *x509.Certificate, now time.Time) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	tmpl.SerialNumber, tmpl.NotBefore, tmpl.NotAfter = serial, now.AddDate(0, 0, -30), now.AddDate(10, 0, 0)
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
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

// TrustedRootJSON returns the authority, the logs and the timestamp authority
// in the format of Sigstore's trusted_root.json.
func (a *Authority) TrustedRootJSON() ([]byte, error) {
	logKey, err := x509.MarshalPKIXPublicKey(&a.logKey.PublicKey)
	if err != nil {
		return nil, err
	}
	log2Key, err := x509.MarshalPKIXPublicKey(a.log2Key.Public())
	if err != nil {
		return nil, err
	}
	start := map[string]string{"start": a.Root.NotBefore.UTC().Format(time.RFC3339)}
	return json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"tlogs": []map[string]any{{
			"baseUrl":       "https://" + a.Origin,
			"hashAlgorithm": "SHA2_256",
			"publicKey":     map[string]any{"rawBytes": logKey, "keyDetails": "PKIX_ECDSA_P256_SHA_256", "validFor": start},
			"logId":         map[string]any{"keyId": a.LogID},
		}, {
			"baseUrl":         "https://" + a.Origin2,
			"hashAlgorithm":   "SHA2_256",
			"publicKey":       map[string]any{"rawBytes": log2Key, "keyDetails": "PKIX_ED25519", "validFor": start},
			"logId":           map[string]any{"keyId": a.Log2ID},
			"checkpointKeyId": map[string]any{"keyId": a.Log2ID},
		}},
		"timestampAuthorities": []map[string]any{{
			"subject":   map[string]string{"organization": "test", "commonName": "test-tsa"},
			"uri":       "https://tsa.test/api/v1/timestamp",
			"certChain": map[string]any{"certificates": []map[string]any{{"rawBytes": a.TSALeaf.Raw}, {"rawBytes": a.TSARoot.Raw}}},
			"validFor":  start,
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
	tr, err := provenance.ParseTrustRoot(doc)
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
	if b.Rekor2 {
		return a.attest2(envelope, payloadType, payload, sig, certDER, at)
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

// attest2 records a signed statement the way Rekor v2 does: as the hash of
// what was signed with the signature and the certificate, in a tree whose
// checkpoint the log signs with Ed25519, and with a timestamp for the
// signature instead of a time signed by the log.
func (a *Authority) attest2(envelope map[string]any, payloadType string, payload, sig, certDER []byte, at time.Time) ([]byte, error) {
	signed := sha256.Sum256(fmt.Appendf(nil, "DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
	// Keys in the order of canonical JSON (RFC 8785), which json.Marshal gives
	// for maps.
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.2",
		"kind":       "hashedrekord",
		"spec": map[string]any{"hashedRekordV002": map[string]any{
			"data": map[string]any{"algorithm": "SHA2_256", "digest": signed[:]},
			"signature": map[string]any{
				"content":  sig,
				"verifier": map[string]any{"keyDetails": "PKIX_ECDSA_P256_SHA_256", "x509Certificate": map[string]any{"rawBytes": certDER}},
			},
		}},
	})
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
	note := fmt.Sprintf("%s\n%s\n%s\n", a.Origin2, size, base64.StdEncoding.EncodeToString(root[:]))
	checkpoint := note + "\n— " + a.Origin2 + " " + base64.StdEncoding.EncodeToString(append(slices.Clone(a.Log2ID[:4]), ed25519.Sign(a.log2Key, []byte(note))...)) + "\n"
	stamp, err := a.Timestamp(sig, at)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{
			"certificate": map[string]any{"rawBytes": certDER},
			"tlogEntries": []map[string]any{{
				"logIndex":       fmt.Sprint(a.Before),
				"logId":          map[string]any{"keyId": a.Log2ID},
				"kindVersion":    map[string]string{"kind": "hashedrekord", "version": "0.0.2"},
				"integratedTime": "0",
				"inclusionProof": map[string]any{
					"logIndex":   fmt.Sprint(a.Before),
					"rootHash":   root[:],
					"treeSize":   size,
					"hashes":     treePath(a.Before, leaves),
					"checkpoint": map[string]string{"envelope": checkpoint},
				},
				"canonicalizedBody": body,
			}},
			"timestampVerificationData": map[string]any{"rfc3161Timestamps": []map[string]any{{"signedTimestamp": stamp}}},
		},
		"dsseEnvelope": envelope,
	})
}

// Object identifiers of the hashes a timestamp may use.
var (
	SHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	SHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	SHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
)

// Object identifiers of CMS (RFC 5652) and of timestamps (RFC 3161).
var (
	SignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	TSTInfo       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	ContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	MessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
)

var (
	oidECDSAWithSHA384 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidSigningCertV2   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}
)

// Attribute is a signed attribute of a timestamp's signature.
type Attribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

// Value returns v in DER as the single value of an attribute.
func Value(v any) []asn1.RawValue {
	der, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return []asn1.RawValue{{FullBytes: der}}
}

// Stamp describes an RFC 3161 timestamp response. With only Message and At it
// is what Sigstore's authority answers: SHA-256 of the message, signed with
// ECDSA P-384 and SHA-384 over three attributes, no certificate inside. The
// other fields make responses a verifier must refuse, or rarer valid ones.
type Stamp struct {
	// Message is what existed at the time At: the signature of the statement.
	Message []byte
	At      time.Time
	// Status is the PKIStatus: 0, granted, unless set.
	Status int
	// Fraction writes the time with a fraction of a second.
	Fraction bool
	// Time replaces the time with these bytes.
	Time string
	// ByKeyID names the signing certificate by its key identifier instead of
	// its issuer and serial number.
	ByKeyID bool
	// Imprint and Digest are the hash of the message and the hash the
	// signature covers. They default to SHA256 and SHA384.
	Imprint, Digest asn1.ObjectIdentifier
	// Version of the timestamp: 1 unless set.
	Version int
	// Content and Outer are the types of the signed content and of the token:
	// TSTInfo and SignedData unless set.
	Content, Outer asn1.ObjectIdentifier
	// Attributes changes the signed attributes before they are signed. Nil
	// attributes leave the signature without any.
	Attributes func([]Attribute) []Attribute
	// Signers is how many times the authority signs: once unless set; -1 is none.
	Signers int
	// Info replaces the timestamp inside the signed content, and Signed the
	// signed data inside the token.
	Info, Signed []byte
}

func hashFor(oid asn1.ObjectIdentifier) func([]byte) []byte {
	switch {
	case oid.Equal(SHA384):
		return func(b []byte) []byte { h := sha512.Sum384(b); return h[:] }
	case oid.Equal(SHA512):
		return func(b []byte) []byte { h := sha512.Sum512(b); return h[:] }
	}
	return func(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
}

// wrap puts der inside a context-specific, constructed tag.
func wrap(tag int, der []byte) (asn1.RawValue, error) {
	out, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: tag, IsCompound: true, Bytes: der})
	return asn1.RawValue{FullBytes: out}, err
}

// Timestamp returns the timestamp authority's response saying that message
// existed at the time at.
func (a *Authority) Timestamp(message []byte, at time.Time) ([]byte, error) {
	return a.Stamp(Stamp{Message: message, At: at})
}

// Stamp returns the timestamp response s describes, signed by the timestamp
// authority.
func (a *Authority) Stamp(s Stamp) ([]byte, error) {
	def := func(oid *asn1.ObjectIdentifier, d asn1.ObjectIdentifier) {
		if *oid == nil {
			*oid = d
		}
	}
	def(&s.Imprint, SHA256)
	def(&s.Digest, SHA384)
	def(&s.Content, TSTInfo)
	def(&s.Outer, SignedData)
	if s.Version == 0 {
		s.Version = 1
	}
	if s.Signers == 0 {
		s.Signers = 1
	}
	null := asn1.NullRawValue
	digest := hashFor(s.Digest)

	// The timestamp itself (TSTInfo).
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	genTime := s.At.UTC().Format("20060102150405Z")
	if s.Fraction {
		genTime = s.At.UTC().Format("20060102150405.000Z")
	}
	if s.Time != "" {
		genTime = s.Time
	}
	type messageImprint struct {
		Alg  pkix.AlgorithmIdentifier
		Hash []byte
	}
	info, err := asn1.Marshal(struct {
		Version  int
		Policy   asn1.ObjectIdentifier
		Imprint  messageImprint
		Serial   *big.Int
		GenTime  asn1.RawValue
		Accuracy struct{ Seconds int }
		Nonce    *big.Int
	}{s.Version, asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 2}, messageImprint{pkix.AlgorithmIdentifier{Algorithm: s.Imprint, Parameters: null}, hashFor(s.Imprint)(s.Message)},
		serial, asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagGeneralizedTime, Bytes: []byte(genTime)}, struct{ Seconds int }{1}, big.NewInt(42)})
	if err != nil {
		return nil, err
	}
	if s.Info != nil {
		info = s.Info
	}

	// The signed attributes: what the content is, its digest, and which
	// certificate signs (RFC 5816).
	certHash := sha256.Sum256(a.TSALeaf.Raw)
	type essCertID struct{ Hash []byte }
	attributes := []Attribute{
		{ContentType, Value(s.Content)},
		{MessageDigest, Value(digest(info))},
		{oidSigningCertV2, Value(struct{ Certs []essCertID }{[]essCertID{{certHash[:]}}})},
	}
	if s.Attributes != nil {
		attributes = s.Attributes(attributes)
	}
	attrs, err := asn1.MarshalWithParams(attributes, "set")
	if err != nil {
		return nil, err
	}
	signature, err := ecdsa.SignASN1(rand.Reader, a.tsaKey, digest(attrs))
	if err != nil {
		return nil, err
	}
	// Inside the signer the attributes carry an implicit tag instead of SET.
	var tagged asn1.RawValue
	if attributes != nil {
		tagged.FullBytes = slices.Clone(attrs)
		tagged.FullBytes[0] = 0xa0
	}
	id, err := asn1.Marshal(struct {
		Issuer asn1.RawValue
		Serial *big.Int
	}{asn1.RawValue{FullBytes: a.TSALeaf.RawIssuer}, a.TSALeaf.SerialNumber})
	if err != nil {
		return nil, err
	}
	if s.ByKeyID {
		if id, err = asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: a.TSALeaf.SubjectKeyId}); err != nil {
			return nil, err
		}
	}

	type signerInfo struct {
		Version     int
		ID          asn1.RawValue
		DigestAlg   pkix.AlgorithmIdentifier
		SignedAttrs asn1.RawValue `asn1:"optional"`
		SigAlg      pkix.AlgorithmIdentifier
		Signature   []byte
	}
	type content struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}
	signers := []signerInfo{}
	for range s.Signers {
		signers = append(signers, signerInfo{1, asn1.RawValue{FullBytes: id}, pkix.AlgorithmIdentifier{Algorithm: s.Digest, Parameters: null}, tagged,
			pkix.AlgorithmIdentifier{Algorithm: oidECDSAWithSHA384}, signature})
	}
	signedData, err := asn1.Marshal(struct {
		Version    int
		DigestAlgs []pkix.AlgorithmIdentifier `asn1:"set"`
		Content    content
		Signers    []signerInfo `asn1:"set"`
	}{3, []pkix.AlgorithmIdentifier{{Algorithm: s.Digest, Parameters: null}}, content{s.Content, info}, signers})
	if err != nil {
		return nil, err
	}
	if s.Signed != nil {
		signedData = s.Signed
	}
	inner, err := wrap(0, signedData)
	if err != nil {
		return nil, err
	}
	type token struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue
	}
	if s.Status > 1 {
		return asn1.Marshal(struct{ Status struct{ Status int } }{struct{ Status int }{s.Status}})
	}
	return asn1.Marshal(struct {
		Status struct{ Status int }
		Token  token
	}{struct{ Status int }{s.Status}, token{s.Outer, inner}})
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
