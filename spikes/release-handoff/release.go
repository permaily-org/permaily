// Package handoff is a spike for ADR 0013: can an enclave decide, with nothing
// but data it is handed and a trust root compiled into its image, that a PCR0
// belongs to a public release of this repository?
//
// It is not imported by the product and is never deployed.
package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// Policy says which builds count as releases.
type Policy struct {
	// Repository is "owner/name" on github.com.
	Repository string
	// Workflow is the path of the release workflow, and Ref the only ref whose
	// runs may publish releases.
	Workflow string
	Ref      string
}

// Production is the policy an enclave image would carry.
var Production = Policy{Repository: "permaily-org/permaily", Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main"}

// Release is what a verified bundle proves.
type Release struct {
	PCR0 string
	// Commit is the git commit the release workflow built.
	Commit string
	// LoggedAt is when the public transparency log recorded the signature.
	LoggedAt time.Time
}

const githubIssuer = "https://token.actions.githubusercontent.com"

// VerifyRelease checks that pcrs is the pcrs.json of a release: bundleJSON must
// be a Sigstore bundle whose signature covers the SHA-256 of pcrs, made with a
// certificate that Fulcio issued to the release workflow of the repository,
// running on the allowed ref, and recorded in the Rekor transparency log.
// trustedRootJSON pins Fulcio and Rekor. It needs no network.
func VerifyRelease(p Policy, trustedRootJSON, bundleJSON, pcrs []byte) (*Release, error) {
	trusted, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("trusted root: %w", err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	// One entry in the public log, with its signed inclusion time. A signature
	// that only carries a timestamp from a private authority is refused.
	v, err := verify.NewVerifier(trusted, verify.WithTransparencyLog(1), verify.WithIntegratedTimestamps(1))
	if err != nil {
		return nil, err
	}
	san := "https://github.com/" + p.Repository + "/" + p.Workflow + "@" + p.Ref
	identity, err := verify.NewShortCertificateIdentity(githubIssuer, "", san, "")
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
	if cert.SourceRepositoryURI != "https://github.com/"+p.Repository || cert.SourceRepositoryRef != p.Ref || cert.RunnerEnvironment != "github-hosted" {
		return nil, fmt.Errorf("built from %s at %s on a %s runner", cert.SourceRepositoryURI, cert.SourceRepositoryRef, cert.RunnerEnvironment)
	}
	if len(res.VerifiedTimestamps) == 0 {
		return nil, errors.New("no verified log timestamp")
	}

	var file struct{ PCR0 string }
	if err := json.Unmarshal(pcrs, &file); err != nil {
		return nil, fmt.Errorf("pcrs.json: %w", err)
	}
	if raw, err := hex.DecodeString(file.PCR0); err != nil || len(raw) != 48 {
		return nil, errors.New("pcrs.json: PCR0 is not a SHA-384 value")
	}
	return &Release{PCR0: file.PCR0, Commit: cert.SourceRepositoryDigest, LoggedAt: res.VerifiedTimestamps[0].Timestamp}, nil
}
