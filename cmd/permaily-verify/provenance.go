package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
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
	// trustUpdate is the directory of -trust-update.
	trustUpdate string
}

func provenanceFlags(fs *flag.FlagSet) *provenanceOptions {
	o := &provenanceOptions{}
	fs.StringVar(&o.bundle, "provenance", "", "instead of checking a deployment: check, offline, that this Sigstore bundle makes the -pcrs file a release, the way a running enclave decides before it hands the root secret over")
	fs.StringVar(&o.trustUpdate, "trust-update", "", "instead of checking a deployment: check, offline, that this directory holds a trust root that Sigstore published after the one compiled in (trusted_root.json, targets.json and every later N.root.json), the way a running enclave decides before it takes one")
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

// tufRepository is where Sigstore publishes its trust root.
const tufRepository = "https://tuf-repo-cdn.sigstore.dev"

// checkSigstore checks what an enclave says about the trust root it checks
// releases against. While it uses the one compiled into its image, that is
// the one compiled into this verifier, which is built from the same source.
// After an update it must be a later one; the enclave took it only with the
// signatures of Sigstore's keyholders, which this verifier trusts the image
// to have checked, like everything else the measured code does.
func checkSigstore(st *attestation.SigstoreStatus, compiled *provenance.TrustRoot) error {
	own := compiled.Publication
	if st.UpdatedAt.IsZero() {
		if st.TrustedRootSHA256 != own.SHA256 || st.TUFRootVersion != own.RootVersion || st.TUFTargetsVersion != own.TargetsVersion {
			return fmt.Errorf("the enclave says it uses the trust root compiled into its image, sha256 %s (TUF targets %d), and the one of this release is %s (TUF targets %d)",
				st.TrustedRootSHA256, st.TUFTargetsVersion, own.SHA256, own.TargetsVersion)
		}
		return nil
	}
	if st.TUFTargetsVersion <= own.TargetsVersion || st.TUFRootVersion < own.RootVersion || st.TrustedRootSHA256 == own.SHA256 || len(st.TrustedRootSHA256) != 64 {
		return fmt.Errorf("the enclave says it was given a newer trust root, and reports sha256 %s (TUF root %d, targets %d), which does not follow the one of this release (TUF root %d, targets %d)",
			st.TrustedRootSHA256, st.TUFRootVersion, st.TUFTargetsVersion, own.RootVersion, own.TargetsVersion)
	}
	return nil
}

func printSigstore(w io.Writer, st *attestation.SigstoreStatus) {
	if st == nil {
		return
	}
	if st.UpdatedAt.IsZero() {
		fmt.Fprintf(w, "      releases are checked against Sigstore's trust root sha256 %s (TUF targets %d), compiled into the image\n", st.TrustedRootSHA256, st.TUFTargetsVersion)
		return
	}
	fmt.Fprintf(w, "      releases are checked against Sigstore's trust root sha256 %s (TUF root %d, targets %d), given to the enclave at %s; it took it only with the signatures of Sigstore's keyholders, and who issues signing certificates stays as compiled into the image\n",
		st.TrustedRootSHA256, st.TUFRootVersion, st.TUFTargetsVersion, st.UpdatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "      that trust root as Sigstore publishes it: %s/targets/%s.trusted_root.json\n", tufRepository, st.TrustedRootSHA256)
}

type trustUpdateResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Newer is false when the files are the trust root compiled into this binary.
	Newer             bool   `json:"newer"`
	TrustedRootSHA256 string `json:"trusted_root_sha256,omitempty"`
	TUFRootVersion    int    `json:"tuf_root_version,omitempty"`
	TUFTargetsVersion int    `json:"tuf_targets_version,omitempty"`
	// Logs and TimestampAuthorities are what the new trust root names.
	Logs                 []string `json:"logs,omitempty"`
	TimestampAuthorities int      `json:"timestamp_authorities,omitempty"`
}

// checkTrustUpdate decides, offline, whether an enclave built from this code
// would take the trust root in dir: trusted_root.json, targets.json and every
// N.root.json after the TUF root compiled in, as Sigstore's TUF repository
// serves them (scripts/sigstore-trust-root.sh fetches them).
func checkTrustUpdate(w io.Writer, compiled *provenance.TrustRoot, dir string, now time.Time, asJSON bool) bool {
	res := trustUpdateResult{}
	next, err := trustUpdate(compiled, dir, now)
	if err != nil {
		res.Error = err.Error()
	} else {
		p := next.Publication
		res = trustUpdateResult{OK: true, Newer: *p != *compiled.Publication, TrustedRootSHA256: p.SHA256, TUFRootVersion: p.RootVersion, TUFTargetsVersion: p.TargetsVersion, TimestampAuthorities: len(next.Timestampers)}
		for _, l := range next.Logs {
			res.Logs = append(res.Logs, l.Origin)
		}
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return res.OK
	}
	switch {
	case !res.OK:
		fmt.Fprintf(w, "FAIL  Sigstore trust root\n      %s\n      an enclave built from this code would refuse it\n", res.Error)
	case !res.Newer:
		fmt.Fprintf(w, "PASS  Sigstore trust root\n      sha256 %s (TUF root %d, targets %d): the one compiled into this binary; there is nothing newer in %s\n", res.TrustedRootSHA256, res.TUFRootVersion, res.TUFTargetsVersion, dir)
	default:
		fmt.Fprintf(w, "PASS  Sigstore trust root\n      sha256 %s (TUF root %d, targets %d), newer than the one compiled into this binary (TUF root %d, targets %d)\n",
			res.TrustedRootSHA256, res.TUFRootVersion, res.TUFTargetsVersion, compiled.Publication.RootVersion, compiled.Publication.TargetsVersion)
		fmt.Fprintf(w, "      signed by Sigstore's keyholders, each TUF root by those of the one before\n      logs: %s; timestamp authorities: %d\n", strings.Join(res.Logs, ", "), res.TimestampAuthorities)
		fmt.Fprintln(w, "      an enclave built from this code takes it, for its logs and timestamp authorities; who issues signing certificates stays as compiled in")
	}
	return res.OK
}

func trustUpdate(compiled *provenance.TrustRoot, dir string, now time.Time) (next *provenance.TrustRoot, err error) {
	var u provenance.Update
	if u.TrustedRoot, err = os.ReadFile(filepath.Join(dir, "trusted_root.json")); err != nil {
		return nil, err
	}
	if u.Targets, err = os.ReadFile(filepath.Join(dir, "targets.json")); err != nil {
		return nil, err
	}
	// Every TUF root after the one compiled in, in order, until one is missing.
	for v := compiled.Publication.RootVersion + 1; ; v++ {
		root, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.root.json", v)))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return nil, err
		}
		u.Roots = append(u.Roots, root)
	}
	return compiled.Update(u, now)
}
