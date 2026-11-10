// Package provenance decides whether the measurements of an enclave image
// (its pcrs.json) were published by a repository's release workflow.
//
// A release of Permaily carries a Sigstore bundle: a statement naming the
// SHA-256 of pcrs.json, signed with a short-lived certificate that Fulcio
// issued to the GitHub Actions run that built it, and recorded in the public
// Rekor transparency log. Verify checks all of that with nothing but the
// bundle, the file and a pinned trust root, so an enclave can do it without
// trusting the network or the operator: an image nobody published cannot be
// passed off as a release (ADR 0013).
//
// Only the standard library is used. The checks are compared with sigstore-go
// in CI (tests/sigstore-differential).
package provenance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// Sentinel errors. Use errors.Is to classify a refusal.
var (
	// ErrMalformed means the bundle, the measurements or the trust root could not be read.
	ErrMalformed = errors.New("provenance: malformed input")
	// ErrUntrusted means a signature, the certificate chain or the log proof does not verify.
	ErrUntrusted = errors.New("provenance: not signed and logged under the trust root")
	// ErrNotARelease means everything verifies, but it was not made by the
	// release workflow the policy names, or it does not cover these measurements.
	ErrNotARelease = errors.New("provenance: not a release")
)

// Limits on what Verify reads. A real bundle is about 12 KiB.
const (
	maxBundle = 256 << 10
	maxPCRs   = 4 << 10
)

// Policy says which builds count as releases.
type Policy struct {
	// Repository is "owner/name" on github.com.
	Repository string
	// RepositoryID and OwnerID are GitHub's numeric ids of the repository and
	// its owner. Names can be renamed and taken by someone else; ids cannot.
	// Each is checked when set.
	RepositoryID string
	OwnerID      string
	// Workflow is the path of the release workflow in the repository.
	Workflow string
	// Ref is the only git ref whose runs may publish releases, for example
	// refs/heads/main. RefPrefix allows every ref that starts with it instead,
	// for test images. Exactly one of the two is set.
	Ref       string
	RefPrefix string
}

// Validate reports whether the policy names a repository, a workflow and
// either a ref or a ref prefix.
func (p Policy) Validate() error {
	if p.Repository == "" || p.Workflow == "" || (p.Ref == "") == (p.RefPrefix == "") {
		return fmt.Errorf("%w: a release policy needs a repository, a workflow and either a ref or a ref prefix", ErrMalformed)
	}
	return nil
}

func (p Policy) allowsRef(ref string) bool {
	if p.Ref != "" {
		return ref == p.Ref
	}
	return len(ref) > len(p.RefPrefix) && strings.HasPrefix(ref, p.RefPrefix)
}

// Release is what a verified bundle proves.
type Release struct {
	// PCRs are the measurements in pcrs.json; PCR0 is the image, in lowercase hex.
	PCRs map[uint][]byte
	PCR0 string
	// Commit is the git commit the workflow built, and Ref the ref it ran on.
	Commit string
	Ref    string
	// LoggedAt is when the public transparency log recorded the signature.
	LoggedAt time.Time
}

// Authority is one certificate authority that issues signing certificates
// (Fulcio), and the period in which its certificates count.
type Authority struct {
	Roots, Intermediates *x509.CertPool
	// NotBefore and NotAfter bound the log time of a signature. A zero
	// NotAfter means the authority is still in use.
	NotBefore, NotAfter time.Time
}

// Log is the transparency log (Rekor) whose entries count.
type Log struct {
	Key *ecdsa.PublicKey
	// ID is the SHA-256 of the log's public key, and Origin the name it signs
	// its checkpoints with.
	ID                  []byte
	Origin              string
	NotBefore, NotAfter time.Time
}

// TrustRoot pins who may issue signing certificates and which log counts.
type TrustRoot struct {
	Authorities []Authority
	Log         Log
}

const (
	githubIssuer   = "https://token.actions.githubusercontent.com"
	provenanceType = "https://slsa.dev/provenance/v1"
	statementType  = "https://in-toto.io/Statement/v1"
	payloadType    = "application/vnd.in-toto+json"
	hostedRunner   = "github-hosted"
)

// PublicGoodRekor is the base URL of Sigstore's public log that GitHub's
// artifact attestations of public repositories are recorded in.
const PublicGoodRekor = "https://rekor.sigstore.dev"

//go:embed sigstore_trusted_root.json
var publicGoodJSON []byte

// PublicGood returns Sigstore's public trust root as compiled into this
// package: Fulcio's certificates and the Rekor log key. It changes only with
// the code, so inside an enclave image it is part of the measured image.
var PublicGood = sync.OnceValues(func() (*TrustRoot, error) {
	return ParseTrustRoot(publicGoodJSON, PublicGoodRekor)
})

// Fulcio certificate extensions (https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md).
var (
	oidIssuer    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidRunnerEnv = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 11}
	oidSourceURI = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	oidSourceSHA = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 13}
	oidSourceRef = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 14}
	oidSourceID  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 15}
	oidOwnerID   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 17}
)

type validFor struct {
	Start, End time.Time
}

// ParseTrustRoot reads Sigstore's trusted_root.json and keeps Fulcio's
// certificates and the Rekor log whose base URL is rekorURL.
func ParseTrustRoot(trustedRootJSON []byte, rekorURL string) (*TrustRoot, error) {
	var doc struct {
		CertificateAuthorities []struct {
			CertChain struct {
				Certificates []struct{ RawBytes []byte }
			}
			ValidFor validFor
		}
		Tlogs []struct {
			BaseURL   string `json:"baseUrl"`
			PublicKey struct {
				RawBytes []byte
				ValidFor validFor
			}
			LogID struct{ KeyID []byte } `json:"logId"`
		}
	}
	if err := json.Unmarshal(trustedRootJSON, &doc); err != nil {
		return nil, fmt.Errorf("%w: trusted root: %w", ErrMalformed, err)
	}
	tr := &TrustRoot{}
	for _, ca := range doc.CertificateAuthorities {
		a := Authority{Roots: x509.NewCertPool(), Intermediates: x509.NewCertPool(), NotBefore: ca.ValidFor.Start, NotAfter: ca.ValidFor.End}
		roots := 0
		for _, c := range ca.CertChain.Certificates {
			cert, err := x509.ParseCertificate(c.RawBytes)
			if err != nil {
				return nil, fmt.Errorf("%w: trusted root: %w", ErrMalformed, err)
			}
			if bytes.Equal(cert.RawIssuer, cert.RawSubject) {
				a.Roots.AddCert(cert)
				roots++
			} else {
				a.Intermediates.AddCert(cert)
			}
		}
		if roots == 0 {
			return nil, fmt.Errorf("%w: trusted root: a certificate authority without a root", ErrMalformed)
		}
		tr.Authorities = append(tr.Authorities, a)
	}
	for _, l := range doc.Tlogs {
		if l.BaseURL != rekorURL {
			continue
		}
		pub, err := x509.ParsePKIXPublicKey(l.PublicKey.RawBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: trusted root: %w", ErrMalformed, err)
		}
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: trusted root: the log key is not ECDSA", ErrMalformed)
		}
		tr.Log = Log{Key: key, ID: l.LogID.KeyID, Origin: strings.TrimPrefix(rekorURL, "https://"), NotBefore: l.PublicKey.ValidFor.Start, NotAfter: l.PublicKey.ValidFor.End}
	}
	if len(tr.Authorities) == 0 || tr.Log.Key == nil || len(tr.Log.ID) == 0 {
		return nil, fmt.Errorf("%w: trusted root: no certificate authority or no log", ErrMalformed)
	}
	return tr, nil
}

type bundle struct {
	VerificationMaterial struct {
		Certificate struct{ RawBytes []byte }
		TlogEntries []struct {
			LogIndex         string
			LogID            struct{ KeyID []byte } `json:"logId"`
			IntegratedTime   string
			InclusionPromise struct{ SignedEntryTimestamp []byte }
			InclusionProof   struct {
				LogIndex   string
				RootHash   []byte
				TreeSize   string
				Hashes     [][]byte
				Checkpoint struct{ Envelope string }
			}
			CanonicalizedBody []byte
		}
	}
	DSSEEnvelope struct {
		Payload     []byte
		PayloadType string
		Signatures  []struct{ Sig []byte }
	} `json:"dsseEnvelope"`
}

type digest struct {
	Digest struct{ SHA256 string }
}

// Verify checks that pcrs is the pcrs.json of a release under policy p:
// bundleJSON must be a Sigstore bundle whose statement names the SHA-256 of
// pcrs, signed with a certificate that an authority in tr issued to the
// workflow and ref of p, on a GitHub-hosted runner, and recorded in the log
// of tr. It needs no network and no clock: the time is the one the log signed.
func Verify(p Policy, tr *TrustRoot, bundleJSON, pcrs []byte) (*Release, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if tr == nil || tr.Log.Key == nil {
		return nil, fmt.Errorf("%w: incomplete trust root", ErrMalformed)
	}
	if len(bundleJSON) > maxBundle || len(pcrs) > maxPCRs {
		return nil, fmt.Errorf("%w: input too large", ErrMalformed)
	}
	var b bundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return nil, fmt.Errorf("%w: bundle: %w", ErrMalformed, err)
	}
	env := b.DSSEEnvelope
	if len(b.VerificationMaterial.TlogEntries) != 1 || len(env.Signatures) != 1 {
		return nil, fmt.Errorf("%w: bundle: want exactly one signature and one log entry", ErrMalformed)
	}
	entry := b.VerificationMaterial.TlogEntries[0]
	cert, err := x509.ParseCertificate(b.VerificationMaterial.Certificate.RawBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: bundle certificate: %w", ErrMalformed, err)
	}

	// 1. The log entry is for exactly this payload, signature and certificate.
	var body struct {
		Kind string
		Spec struct {
			PayloadHash struct{ Algorithm, Value string }
			Signatures  []struct {
				Signature []byte
				Verifier  []byte
			}
		}
	}
	if err := json.Unmarshal(entry.CanonicalizedBody, &body); err != nil {
		return nil, fmt.Errorf("%w: log entry: %w", ErrMalformed, err)
	}
	payloadHash := sha256.Sum256(env.Payload)
	if body.Kind != "dsse" || body.Spec.PayloadHash.Algorithm != "sha256" || body.Spec.PayloadHash.Value != hex.EncodeToString(payloadHash[:]) || len(body.Spec.Signatures) != 1 {
		return nil, fmt.Errorf("%w: the log entry is not for this statement", ErrUntrusted)
	}
	block, _ := pem.Decode(body.Spec.Signatures[0].Verifier)
	if block == nil || !bytes.Equal(block.Bytes, cert.Raw) || !bytes.Equal(body.Spec.Signatures[0].Signature, env.Signatures[0].Sig) {
		return nil, fmt.Errorf("%w: the log entry is not for this signature", ErrUntrusted)
	}

	// 2. The log signed a promise to include the entry, with the time (SET)...
	logIndex, err1 := strconv.ParseInt(entry.LogIndex, 10, 64)
	integrated, err2 := strconv.ParseInt(entry.IntegratedTime, 10, 64)
	if err1 != nil || err2 != nil || logIndex < 0 || integrated <= 0 {
		return nil, fmt.Errorf("%w: log entry: bad index or time", ErrMalformed)
	}
	if !bytes.Equal(entry.LogID.KeyID, tr.Log.ID) {
		return nil, fmt.Errorf("%w: the entry is from another log", ErrUntrusted)
	}
	set := fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString(entry.CanonicalizedBody), integrated, hex.EncodeToString(entry.LogID.KeyID), logIndex)
	if !verify(tr.Log.Key, []byte(set), entry.InclusionPromise.SignedEntryTimestamp) {
		return nil, fmt.Errorf("%w: the log's signed entry timestamp does not verify", ErrUntrusted)
	}
	// ...and the entry is in a tree whose head the log signed.
	proof := entry.InclusionProof
	if err := checkInclusion(tr.Log, entry.CanonicalizedBody, proof.LogIndex, proof.TreeSize, proof.Hashes, proof.RootHash, proof.Checkpoint.Envelope); err != nil {
		return nil, err
	}
	loggedAt := time.Unix(integrated, 0).UTC()
	if !within(loggedAt, tr.Log.NotBefore, tr.Log.NotAfter) {
		return nil, fmt.Errorf("%w: the log was not in use at %s", ErrUntrusted, loggedAt.Format(time.RFC3339))
	}

	// 3. An authority in use at that time issued the certificate, and the
	// certificate was valid then.
	if err := checkChain(tr, cert, loggedAt); err != nil {
		return nil, err
	}

	// 4. The certificate's key signed the statement (DSSE)...
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(env.PayloadType), env.PayloadType, len(env.Payload), env.Payload)
	if !ok || env.PayloadType != payloadType || !verify(pub, []byte(pae), env.Signatures[0].Sig) {
		return nil, fmt.Errorf("%w: the statement's signature does not verify", ErrUntrusted)
	}

	// 5. It was issued to the release workflow, on an allowed ref.
	ref := ext(cert, oidSourceRef)
	repoURL := "https://github.com/" + p.Repository
	san := repoURL + "/" + p.Workflow + "@" + ref
	if !p.allowsRef(ref) || len(cert.URIs) != 1 || cert.URIs[0].String() != san || len(cert.EmailAddresses)+len(cert.DNSNames)+len(cert.IPAddresses) != 0 {
		return nil, fmt.Errorf("%w of %s: the certificate is for %v at %q", ErrNotARelease, p.Repository, cert.URIs, ref)
	}
	if ext(cert, oidIssuer) != githubIssuer || ext(cert, oidSourceURI) != repoURL || ext(cert, oidRunnerEnv) != hostedRunner {
		return nil, fmt.Errorf("%w of %s: issued for %q by %q on a %q runner", ErrNotARelease, p.Repository, ext(cert, oidSourceURI), ext(cert, oidIssuer), ext(cert, oidRunnerEnv))
	}
	if (p.RepositoryID != "" && ext(cert, oidSourceID) != p.RepositoryID) || (p.OwnerID != "" && ext(cert, oidOwnerID) != p.OwnerID) {
		return nil, fmt.Errorf("%w of %s: the repository or its owner is another one with the same name", ErrNotARelease, p.Repository)
	}

	// 6. The statement is build provenance that names this pcrs.json.
	var st struct {
		Type          string `json:"_type"`
		PredicateType string
		Subject       []digest
	}
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		return nil, fmt.Errorf("%w: statement: %w", ErrMalformed, err)
	}
	sum := sha256.Sum256(pcrs)
	want := hex.EncodeToString(sum[:])
	if st.Type != statementType || st.PredicateType != provenanceType || !slices.ContainsFunc(st.Subject, func(s digest) bool { return s.Digest.SHA256 == want }) {
		return nil, fmt.Errorf("%w of %s: the statement does not cover these measurements", ErrNotARelease, p.Repository)
	}

	measured, err := attestation.ParsePCRFile(pcrs)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return &Release{PCRs: measured, PCR0: hex.EncodeToString(measured[0]), Commit: ext(cert, oidSourceSHA), Ref: ref, LoggedAt: loggedAt}, nil
}

func within(t, notBefore, notAfter time.Time) bool {
	return !t.Before(notBefore) && (notAfter.IsZero() || !t.After(notAfter))
}

func checkChain(tr *TrustRoot, cert *x509.Certificate, at time.Time) error {
	err := errors.New("no certificate authority was in use then")
	for _, a := range tr.Authorities {
		if !within(at, a.NotBefore, a.NotAfter) {
			continue
		}
		if _, err = cert.Verify(x509.VerifyOptions{
			Roots: a.Roots, Intermediates: a.Intermediates, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		}); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: signing certificate at %s: %w", ErrUntrusted, at.Format(time.RFC3339), err)
}

func verify(key *ecdsa.PublicKey, msg, sig []byte) bool {
	h := sha256.Sum256(msg)
	return key != nil && ecdsa.VerifyASN1(key, h[:], sig)
}

// ext returns a Fulcio extension holding a DER UTF8String, or "".
func ext(cert *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, e := range cert.Extensions {
		if e.Id.Equal(oid) {
			var s string
			if rest, err := asn1.UnmarshalWithParams(e.Value, &s, "utf8"); err == nil && len(rest) == 0 {
				return s
			}
		}
	}
	return ""
}

// checkInclusion verifies an RFC 6962 inclusion proof up to a root hash, and
// the log's signature on the checkpoint that carries that root and tree size.
func checkInclusion(log Log, leaf []byte, index, size string, hashes [][]byte, root []byte, checkpoint string) error {
	fn, err1 := strconv.ParseUint(index, 10, 63)
	n, err2 := strconv.ParseUint(size, 10, 63)
	if err1 != nil || err2 != nil || fn >= n {
		return fmt.Errorf("%w: inclusion proof: bad index or tree size", ErrUntrusted)
	}
	sn := n - 1
	r := sha256.Sum256(append([]byte{0}, leaf...))
	node := func(l, rgt []byte) [32]byte { return sha256.Sum256(slices.Concat([]byte{1}, l, rgt)) }
	for _, h := range hashes {
		if sn == 0 || len(h) != sha256.Size {
			return fmt.Errorf("%w: inclusion proof: malformed", ErrUntrusted)
		}
		if fn&1 == 1 || fn == sn {
			r = node(h, r[:])
			for fn&1 == 0 && fn != 0 {
				fn, sn = fn>>1, sn>>1
			}
		} else {
			r = node(r[:], h)
		}
		fn, sn = fn>>1, sn>>1
	}
	if sn != 0 || !bytes.Equal(r[:], root) {
		return fmt.Errorf("%w: inclusion proof: the entry is not in the tree", ErrUntrusted)
	}

	// Checkpoint (signed note): origin, size and root, a blank line, signatures.
	text, sigs, ok := strings.Cut(checkpoint, "\n\n")
	lines := strings.Split(text, "\n")
	if !ok || len(lines) < 3 || !strings.HasPrefix(lines[0], log.Origin+" - ") || lines[1] != size || lines[2] != base64.StdEncoding.EncodeToString(root) {
		return fmt.Errorf("%w: checkpoint: not for this log, size and root", ErrUntrusted)
	}
	for line := range strings.SplitSeq(sigs, "\n") {
		name, sigB64, ok := strings.Cut(strings.TrimPrefix(line, "— "), " ")
		if !ok || name != log.Origin {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(sigB64)
		if err == nil && len(raw) > 4 && verify(log.Key, []byte(text+"\n"), raw[4:]) {
			return nil
		}
	}
	return fmt.Errorf("%w: checkpoint: the log's signature does not verify", ErrUntrusted)
}
