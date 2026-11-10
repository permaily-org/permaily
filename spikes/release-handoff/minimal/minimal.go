// Package minimal does the same verification as the parent package with the
// standard library only, to see what the enclave would really need: about 300
// lines instead of a dependency tree with gRPC and OpenTelemetry in it.
//
// Spike code (ADR 0013). Not imported by the product, never deployed.
package minimal

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
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
	"time"
)

// Policy says which builds count as releases.
type Policy struct {
	Repository string // owner/name on github.com
	Workflow   string // path of the release workflow
	Ref        string // the only ref whose runs may publish releases
}

// Release is what a verified bundle proves.
type Release struct {
	PCR0     string
	Commit   string
	LoggedAt time.Time
}

// TrustRoot pins who may issue signing certificates (Fulcio) and which
// transparency log counts (Rekor). An enclave image would carry these.
type TrustRoot struct {
	FulcioRoots, FulcioIntermediates *x509.CertPool
	RekorKey                         *ecdsa.PublicKey
	RekorLogID                       []byte
	RekorOrigin                      string
}

const (
	githubIssuer   = "https://token.actions.githubusercontent.com"
	provenanceType = "https://slsa.dev/provenance/v1"
)

// Fulcio certificate extensions (https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md).
var (
	oidIssuer    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidRunnerEnv = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 11}
	oidSourceURI = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	oidSourceSHA = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 13}
	oidSourceRef = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 14}
)

// ParseTrustRoot reads Sigstore's trusted_root.json and keeps Fulcio's
// certificates and the Rekor log whose base URL is rekorURL.
func ParseTrustRoot(trustedRootJSON []byte, rekorURL string) (*TrustRoot, error) {
	var doc struct {
		CertificateAuthorities []struct {
			CertChain struct {
				Certificates []struct{ RawBytes []byte }
			}
		}
		Tlogs []struct {
			BaseURL   string `json:"baseUrl"`
			PublicKey struct{ RawBytes []byte }
			LogID     struct{ KeyID []byte } `json:"logId"`
		}
	}
	if err := json.Unmarshal(trustedRootJSON, &doc); err != nil {
		return nil, fmt.Errorf("trusted root: %w", err)
	}
	tr := &TrustRoot{FulcioRoots: x509.NewCertPool(), FulcioIntermediates: x509.NewCertPool()}
	n := 0
	for _, ca := range doc.CertificateAuthorities {
		for _, c := range ca.CertChain.Certificates {
			cert, err := x509.ParseCertificate(c.RawBytes)
			if err != nil {
				return nil, fmt.Errorf("trusted root: %w", err)
			}
			if bytes.Equal(cert.RawIssuer, cert.RawSubject) {
				tr.FulcioRoots.AddCert(cert)
				n++
			} else {
				tr.FulcioIntermediates.AddCert(cert)
			}
		}
	}
	for _, l := range doc.Tlogs {
		if l.BaseURL != rekorURL {
			continue
		}
		pub, err := x509.ParsePKIXPublicKey(l.PublicKey.RawBytes)
		if err != nil {
			return nil, fmt.Errorf("trusted root: %w", err)
		}
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, errors.New("trusted root: the log key is not ECDSA")
		}
		tr.RekorKey, tr.RekorLogID, tr.RekorOrigin = key, l.LogID.KeyID, strings.TrimPrefix(rekorURL, "https://")
	}
	if n == 0 || tr.RekorKey == nil {
		return nil, errors.New("trusted root: no certificate authority or no log")
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

// VerifyRelease checks that pcrs is the pcrs.json of a release, like the
// parent package's function of the same name. It needs no network.
func VerifyRelease(p Policy, tr *TrustRoot, bundleJSON, pcrs []byte) (*Release, error) {
	var b bundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	env := b.DSSEEnvelope
	if len(b.VerificationMaterial.TlogEntries) != 1 || len(env.Signatures) != 1 {
		return nil, errors.New("bundle: want exactly one signature and one log entry")
	}
	entry := b.VerificationMaterial.TlogEntries[0]
	cert, err := x509.ParseCertificate(b.VerificationMaterial.Certificate.RawBytes)
	if err != nil {
		return nil, fmt.Errorf("bundle certificate: %w", err)
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
		return nil, fmt.Errorf("log entry: %w", err)
	}
	payloadHash := sha256.Sum256(env.Payload)
	block, _ := pem.Decode(firstVerifier(body.Spec.Signatures))
	if body.Kind != "dsse" || body.Spec.PayloadHash.Algorithm != "sha256" || body.Spec.PayloadHash.Value != hex.EncodeToString(payloadHash[:]) ||
		len(body.Spec.Signatures) != 1 || !bytes.Equal(body.Spec.Signatures[0].Signature, env.Signatures[0].Sig) ||
		block == nil || !bytes.Equal(block.Bytes, cert.Raw) {
		return nil, errors.New("the log entry is not for this signature")
	}

	// 2. The log signed a promise to include the entry, with the time (SET)...
	logIndex, err1 := strconv.ParseInt(entry.LogIndex, 10, 64)
	integrated, err2 := strconv.ParseInt(entry.IntegratedTime, 10, 64)
	if err1 != nil || err2 != nil {
		return nil, errors.New("log entry: bad index or time")
	}
	if !bytes.Equal(entry.LogID.KeyID, tr.RekorLogID) {
		return nil, errors.New("the entry is from another log")
	}
	set := fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString(entry.CanonicalizedBody), integrated, hex.EncodeToString(entry.LogID.KeyID), logIndex)
	if !verify(tr.RekorKey, []byte(set), entry.InclusionPromise.SignedEntryTimestamp) {
		return nil, errors.New("the log's signed entry timestamp does not verify")
	}
	// ...and the entry is in a tree whose head the log signed.
	if err := checkInclusion(tr, entry.CanonicalizedBody, entry.InclusionProof.LogIndex, entry.InclusionProof.TreeSize,
		entry.InclusionProof.Hashes, entry.InclusionProof.RootHash, entry.InclusionProof.Checkpoint.Envelope); err != nil {
		return nil, err
	}
	loggedAt := time.Unix(integrated, 0).UTC()

	// 3. Fulcio issued the certificate, and it was valid when the log saw it.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots: tr.FulcioRoots, Intermediates: tr.FulcioIntermediates, CurrentTime: loggedAt,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return nil, fmt.Errorf("signing certificate: %w", err)
	}

	// 4. It was issued to the release workflow, on the allowed ref.
	san := "https://github.com/" + p.Repository + "/" + p.Workflow + "@" + p.Ref
	if len(cert.URIs) != 1 || cert.URIs[0].String() != san || len(cert.EmailAddresses)+len(cert.DNSNames)+len(cert.IPAddresses) != 0 {
		return nil, fmt.Errorf("not a release of %s: the certificate is not for %s", p.Repository, san)
	}
	if ext(cert, oidIssuer) != githubIssuer || ext(cert, oidSourceURI) != "https://github.com/"+p.Repository ||
		ext(cert, oidSourceRef) != p.Ref || ext(cert, oidRunnerEnv) != "github-hosted" {
		return nil, fmt.Errorf("not a release of %s: issued for %s at %s by %s", p.Repository, ext(cert, oidSourceURI), ext(cert, oidSourceRef), ext(cert, oidIssuer))
	}

	// 5. The certificate's key signed the statement (DSSE)...
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(env.PayloadType), env.PayloadType, len(env.Payload), env.Payload)
	if !ok || env.PayloadType != "application/vnd.in-toto+json" || !verify(pub, []byte(pae), env.Signatures[0].Sig) {
		return nil, errors.New("the statement's signature does not verify")
	}
	// ...and the statement is build provenance that names this pcrs.json.
	var st struct {
		Type          string `json:"_type"`
		PredicateType string
		Subject       []struct{ Digest struct{ SHA256 string } }
	}
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		return nil, fmt.Errorf("statement: %w", err)
	}
	digest := sha256.Sum256(pcrs)
	want := hex.EncodeToString(digest[:])
	if st.Type != "https://in-toto.io/Statement/v1" || st.PredicateType != provenanceType ||
		!slices.ContainsFunc(st.Subject, func(s struct{ Digest struct{ SHA256 string } }) bool { return s.Digest.SHA256 == want }) {
		return nil, fmt.Errorf("not a release of %s: the statement does not cover these measurements", p.Repository)
	}

	var file struct{ PCR0 string }
	if err := json.Unmarshal(pcrs, &file); err != nil {
		return nil, fmt.Errorf("pcrs.json: %w", err)
	}
	if raw, err := hex.DecodeString(file.PCR0); err != nil || len(raw) != 48 {
		return nil, errors.New("pcrs.json: PCR0 is not a SHA-384 value")
	}
	return &Release{PCR0: file.PCR0, Commit: ext(cert, oidSourceSHA), LoggedAt: loggedAt}, nil
}

func firstVerifier(sigs []struct{ Signature, Verifier []byte }) []byte {
	if len(sigs) == 0 {
		return nil
	}
	return sigs[0].Verifier
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
func checkInclusion(tr *TrustRoot, leaf []byte, index, size string, hashes [][]byte, root []byte, checkpoint string) error {
	fn, err1 := strconv.ParseUint(index, 10, 64)
	n, err2 := strconv.ParseUint(size, 10, 64)
	if err1 != nil || err2 != nil || fn >= n {
		return errors.New("inclusion proof: bad index or tree size")
	}
	sn := n - 1
	r := sha256.Sum256(append([]byte{0}, leaf...))
	node := func(l, rgt []byte) [32]byte { return sha256.Sum256(slices.Concat([]byte{1}, l, rgt)) }
	for _, h := range hashes {
		if sn == 0 || len(h) != sha256.Size {
			return errors.New("inclusion proof: malformed")
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
		return errors.New("inclusion proof: the entry is not in the tree")
	}

	// Checkpoint (signed note): origin, size and root, a blank line, signatures.
	text, sigs, ok := strings.Cut(checkpoint, "\n\n")
	lines := strings.Split(text, "\n")
	if !ok || len(lines) < 3 || !strings.HasPrefix(lines[0], tr.RekorOrigin+" - ") || lines[1] != size || lines[2] != base64.StdEncoding.EncodeToString(root) {
		return errors.New("checkpoint: not for this log, size and root")
	}
	for _, line := range strings.Split(sigs, "\n") {
		name, sigB64, ok := strings.Cut(strings.TrimPrefix(line, "— "), " ")
		if !ok || name != tr.RekorOrigin {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(sigB64)
		if err == nil && len(raw) > 4 && verify(tr.RekorKey, []byte(text+"\n"), raw[4:]) {
			return nil
		}
	}
	return errors.New("checkpoint: the log's signature does not verify")
}
