// Package differential compares pkg/provenance, the verifier of release
// bundles that runs inside the enclave, with sigstore-go, the reference
// implementation. pkg/provenance uses only the standard library because
// sigstore-go would bring gRPC, OpenTelemetry and about two hundred more
// packages into the enclave image (ADR 0013); this module is where the two are
// held to the same answers. It is its own Go module so that none of its
// dependencies can reach the product.
package differential

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

const githubIssuer = "https://token.actions.githubusercontent.com"

// Reference is what sigstore-go says about a bundle.
type Reference struct {
	PCR0     string
	Commit   string
	Ref      string
	LoggedAt time.Time
}

// Verify answers the same question as provenance.Verify with sigstore-go.
func Verify(p provenance.Policy, trustedRootJSON, bundleJSON, pcrs []byte) (*Reference, error) {
	trusted, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("trusted root: %w", err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	// One entry in the public log. For the first Rekor, with the time the log
	// signed: a signature that only carries a timestamp from an authority is
	// refused. An entry of Rekor v2 has no such time, so there the time is the
	// timestamp authority's.
	when := verify.WithIntegratedTimestamps(1)
	var kind struct {
		VerificationMaterial struct {
			TlogEntries []struct {
				KindVersion struct{ Kind, Version string }
			}
		}
	}
	if err := json.Unmarshal(bundleJSON, &kind); err == nil && len(kind.VerificationMaterial.TlogEntries) == 1 &&
		kind.VerificationMaterial.TlogEntries[0].KindVersion.Kind == "hashedrekord" && kind.VerificationMaterial.TlogEntries[0].KindVersion.Version == "0.0.2" {
		when = verify.WithSignedTimestamps(1)
	}
	v, err := verify.NewVerifier(trusted, verify.WithTransparencyLog(1), when)
	if err != nil {
		return nil, err
	}
	san := "^https://github\\.com/" + escape(p.Repository) + "/" + escape(p.Workflow) + "@"
	if p.Ref != "" {
		san += escape(p.Ref) + "$"
	} else {
		san += escape(p.RefPrefix) + ".+$"
	}
	identity, err := verify.NewShortCertificateIdentity(githubIssuer, "", "", san)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(pcrs)
	res, err := v.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest[:]), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return nil, fmt.Errorf("not a release of %s: %w", p.Repository, err)
	}

	// The certificate also says where the workflow ran and what it built from.
	cert := res.Signature.Certificate
	if cert == nil {
		return nil, errors.New("bundle without a signing certificate")
	}
	ref := cert.SourceRepositoryRef
	if cert.SourceRepositoryURI != "https://github.com/"+p.Repository || cert.RunnerEnvironment != "github-hosted" ||
		cert.SubjectAlternativeName != "https://github.com/"+p.Repository+"/"+p.Workflow+"@"+ref {
		return nil, fmt.Errorf("built from %s at %s on a %s runner", cert.SourceRepositoryURI, ref, cert.RunnerEnvironment)
	}
	if (p.RepositoryID != "" && cert.SourceRepositoryIdentifier != p.RepositoryID) || (p.OwnerID != "" && cert.SourceRepositoryOwnerIdentifier != p.OwnerID) {
		return nil, errors.New("another repository or owner with the same name")
	}
	if len(res.VerifiedTimestamps) == 0 {
		return nil, errors.New("no verified log timestamp")
	}
	measured, err := attestation.ParsePCRFile(pcrs)
	if err != nil {
		return nil, err
	}
	return &Reference{PCR0: fmt.Sprintf("%x", measured[0]), Commit: cert.SourceRepositoryDigest, Ref: ref, LoggedAt: res.VerifiedTimestamps[0].Timestamp}, nil
}

// escape quotes s for use in a regular expression.
func escape(s string) string {
	return strings.NewReplacer(".", `\.`, "+", `\+`, "*", `\*`, "?", `\?`, "(", `\(`, ")", `\)`, "[", `\[`, "]", `\]`, "{", `\{`, "}", `\}`, "^", `\^`, "$", `\$`, "|", `\|`, `\`, `\\`).Replace(s)
}
