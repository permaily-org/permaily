package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
)

// Who publishes the releases of Permaily. The production image is told the
// same in deploy/enclave.json, and a test keeps the two equal.
const (
	releaseRepository   = "permaily-org/permaily"
	releaseRepositoryID = "1448574502"
	releaseOwnerID      = "353453526"
	releaseWorkflow     = ".github/workflows/release.yml"
	releaseRef          = "refs/heads/main"
)

// provenanceOptions is the offline check of -provenance: the same decision a
// running enclave makes before it hands the root secret to a release.
type provenanceOptions struct {
	bundle string
	policy provenance.Policy
}

func provenanceFlags(fs *flag.FlagSet) *provenanceOptions {
	o := &provenanceOptions{}
	fs.StringVar(&o.bundle, "provenance", "", "instead of checking a deployment: check, offline, that this Sigstore bundle makes the -pcrs file a release, the way a running enclave decides before it hands the root secret over")
	fs.StringVar(&o.policy.Repository, "release-repository", releaseRepository, "with -provenance: owner/name of the repository that publishes releases")
	fs.StringVar(&o.policy.RepositoryID, "release-repository-id", releaseRepositoryID, "with -provenance: GitHub's numeric id of that repository")
	fs.StringVar(&o.policy.OwnerID, "release-owner-id", releaseOwnerID, "with -provenance: GitHub's numeric id of the repository's owner")
	fs.StringVar(&o.policy.Workflow, "release-workflow", releaseWorkflow, "with -provenance: path of the workflow that publishes releases")
	fs.StringVar(&o.policy.Ref, "release-ref", "", "with -provenance: the git ref whose runs of that workflow are releases (default "+releaseRef+")")
	fs.StringVar(&o.policy.RefPrefix, "release-ref-prefix", "", "with -provenance, instead of -release-ref: every ref that starts with this (test images)")
	return o
}

type provenanceResult struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	PCR0     string    `json:"pcr0,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	Ref      string    `json:"ref,omitempty"`
	LoggedAt time.Time `json:"logged_at,omitzero"`
}

// checkProvenance verifies the bundle against the Sigstore trust root compiled
// into this binary, which is the one an enclave of the same release carries.
// It reports whether the measurements are a release.
func checkProvenance(w io.Writer, o *provenanceOptions, pcrsFile string, asJSON bool) bool {
	p := o.policy
	if p.Ref == "" && p.RefPrefix == "" {
		p.Ref = releaseRef
	}
	res := provenanceResult{}
	rel, err := verifyProvenance(p, o.bundle, pcrsFile)
	if err != nil {
		res.Error = err.Error()
	} else {
		res = provenanceResult{OK: true, PCR0: rel.PCR0, Commit: rel.Commit, Ref: rel.Ref, LoggedAt: rel.LoggedAt}
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return res.OK
	}
	if !res.OK {
		fmt.Fprintf(w, "FAIL  release provenance\n      %s\n      an enclave built from this code would refuse to hand the root secret to this image\n", res.Error)
		return false
	}
	fmt.Fprintf(w, "PASS  release provenance\n      image %s\n      built from commit %s on %s by %s/%s\n      recorded in the public transparency log at %s\n      an enclave built from this code accepts it as a release\n",
		res.PCR0, res.Commit, res.Ref, p.Repository, p.Workflow, res.LoggedAt.Format(time.RFC3339))
	return true
}

func verifyProvenance(p provenance.Policy, bundleFile, pcrsFile string) (*provenance.Release, error) {
	if pcrsFile == "" {
		return nil, fmt.Errorf("-provenance needs -pcrs FILE, the measurements the bundle is about")
	}
	bundle, err := os.ReadFile(bundleFile) //nolint:gosec // path given by the user on purpose
	if err != nil {
		return nil, err
	}
	pcrs, err := os.ReadFile(pcrsFile) //nolint:gosec // path given by the user on purpose
	if err != nil {
		return nil, err
	}
	trust, err := provenance.PublicGood()
	if err != nil {
		return nil, err
	}
	return provenance.Verify(p, trust, bundle, pcrs)
}
