package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

const fixtures = "../../pkg/provenance/testdata/"

func provenanceOpts(t *testing.T, args ...string) *provenanceOptions {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o := provenanceFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestProvenanceAcceptsARelease(t *testing.T) {
	var out bytes.Buffer
	o := provenanceOpts(t, "-provenance", fixtures+"v0.5.0-provenance.bundle.json")
	if !checkProvenance(&out, o, fixtures+"v0.5.0-pcrs.json", false) {
		t.Fatalf("a real release was refused:\n%s", out.String())
	}
	for _, want := range []string{"PASS  release provenance", "on refs/heads/main by permaily-org/permaily/.github/workflows/release.yml", "accepts it as a release"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if !checkProvenance(&out, o, fixtures+"v0.5.0-pcrs.json", true) {
		t.Fatalf("refused in JSON mode:\n%s", out.String())
	}
	var res provenanceResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || !res.OK || len(res.PCR0) != 96 || len(res.Commit) != 40 || res.Ref != releaseRef || res.LoggedAt.IsZero() {
		t.Errorf("JSON result = %+v (%v)", res, err)
	}
}

func TestProvenanceRefuses(t *testing.T) {
	other := t.TempDir() + "/pcrs.json"
	if err := os.WriteFile(other, []byte(`{"PCR0":"`+strings.Repeat("ab", 48)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	real := []string{"-provenance", fixtures + "v0.5.0-provenance.bundle.json"}
	cases := []struct {
		name string
		args []string
		pcrs string
		want string
	}{
		{"other measurements", real, other, "does not cover these measurements"},
		{"a bundle that is not build provenance", []string{"-provenance", fixtures + "v0.5.0-github-release.bundle.json"}, fixtures + "v0.5.0-pcrs.json", "provenance:"},
		{"another workflow", append([]string{"-release-workflow", ".github/workflows/staging-image.yml"}, real...), fixtures + "v0.5.0-pcrs.json", "not a release"},
		{"another ref", append([]string{"-release-ref", "refs/heads/develop"}, real...), fixtures + "v0.5.0-pcrs.json", "not a release"},
		{"a ref prefix that main is not under", append([]string{"-release-ref-prefix", "refs/heads/staging/"}, real...), fixtures + "v0.5.0-pcrs.json", "not a release"},
		{"another repository id", append([]string{"-release-repository-id", "1"}, real...), fixtures + "v0.5.0-pcrs.json", "another one with the same name"},
		{"no measurements", real, "", "needs -pcrs"},
		{"a missing bundle", []string{"-provenance", fixtures + "missing.json"}, fixtures + "v0.5.0-pcrs.json", "no such file"},
		{"a missing measurements file", real, fixtures + "missing.json", "no such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if checkProvenance(&out, provenanceOpts(t, c.args...), c.pcrs, false) {
				t.Fatalf("accepted:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "FAIL  release provenance") || !strings.Contains(out.String(), c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out.String())
			}
			out.Reset()
			var res provenanceResult
			if checkProvenance(&out, provenanceOpts(t, c.args...), c.pcrs, true) || json.Unmarshal(out.Bytes(), &res) != nil || res.OK || res.Error == "" {
				t.Errorf("JSON result = %+v", res)
			}
		})
	}
}

// The verifier's defaults are the policy the production image is built with.
func TestReleasePolicyMatchesTheImage(t *testing.T) {
	data, err := os.ReadFile("../../deploy/enclave.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"release_repository":    releaseRepository,
		"release_repository_id": releaseRepositoryID,
		"release_owner_id":      releaseOwnerID,
		"release_workflow":      releaseWorkflow,
		"release_ref":           releaseRef,
		"release_ref_prefix":    "",
	} {
		if got, _ := cfg[key].(string); got != want {
			t.Errorf("deploy/enclave.json %s = %q, the verifier assumes %q", key, got, want)
		}
	}
}

func TestCheckSigstore(t *testing.T) {
	compiled, err := provenance.PublicGood()
	if err != nil {
		t.Fatal(err)
	}
	own := compiled.Publication
	at := time.Date(2026, 11, 11, 7, 0, 0, 0, time.UTC)
	same := attestation.SigstoreStatus{TrustedRootSHA256: own.SHA256, TUFRootVersion: own.RootVersion, TUFTargetsVersion: own.TargetsVersion}
	newer := attestation.SigstoreStatus{TrustedRootSHA256: strings.Repeat("ab", 32), TUFRootVersion: own.RootVersion + 1, TUFTargetsVersion: own.TargetsVersion + 1, UpdatedAt: at}
	with := func(st attestation.SigstoreStatus, change func(*attestation.SigstoreStatus)) *attestation.SigstoreStatus {
		change(&st)
		return &st
	}
	for name, st := range map[string]*attestation.SigstoreStatus{
		"the one compiled in":                 &same,
		"a later one":                         &newer,
		"a later one under the same TUF root": with(newer, func(s *attestation.SigstoreStatus) { s.TUFRootVersion = own.RootVersion }),
	} {
		if err := checkSigstore(st, compiled); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		st   *attestation.SigstoreStatus
		want string
	}{
		"compiled in, with another hash":    {with(same, func(s *attestation.SigstoreStatus) { s.TrustedRootSHA256 = strings.Repeat("ab", 32) }), "compiled into its image"},
		"compiled in, of another version":   {with(same, func(s *attestation.SigstoreStatus) { s.TUFTargetsVersion-- }), "compiled into its image"},
		"compiled in, under another root":   {with(same, func(s *attestation.SigstoreStatus) { s.TUFRootVersion++ }), "compiled into its image"},
		"updated to the same version":       {with(newer, func(s *attestation.SigstoreStatus) { s.TUFTargetsVersion = own.TargetsVersion }), "does not follow"},
		"updated to an older TUF root":      {with(newer, func(s *attestation.SigstoreStatus) { s.TUFRootVersion = own.RootVersion - 1 }), "does not follow"},
		"updated to the one compiled in":    {with(newer, func(s *attestation.SigstoreStatus) { s.TrustedRootSHA256 = own.SHA256 }), "does not follow"},
		"updated to something without hash": {with(newer, func(s *attestation.SigstoreStatus) { s.TrustedRootSHA256 = "ab" }), "does not follow"},
	} {
		if err := checkSigstore(c.st, compiled); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	var out bytes.Buffer
	printSigstore(&out, nil)
	if out.Len() != 0 {
		t.Errorf("printed %q for nothing", out.String())
	}
	printSigstore(&out, &same)
	if !strings.Contains(out.String(), "compiled into the image") || !strings.Contains(out.String(), own.SHA256) {
		t.Errorf("compiled in: %s", out.String())
	}
	out.Reset()
	printSigstore(&out, &newer)
	for _, want := range []string{"given to the enclave at 2026-11-11T07:00:00Z", "signatures of Sigstore's keyholders", "stays as compiled into the image",
		"https://tuf-repo-cdn.sigstore.dev/targets/" + strings.Repeat("ab", 32) + ".trusted_root.json"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("updated: output lacks %q:\n%s", want, out.String())
		}
	}
}

// The directory scripts/sigstore-trust-root.sh fetches, checked the way an
// enclave decides.
func TestTrustUpdate(t *testing.T) {
	write := func(dir, name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(dir+"/"+name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	compiled, err := provenance.PublicGood()
	if err != nil {
		t.Fatal(err)
	}

	// What Sigstore serves today is what is compiled in: nothing newer.
	current := t.TempDir()
	write(current, "trusted_root.json", read("../../pkg/provenance/sigstore_trusted_root.json"))
	write(current, "targets.json", read("../../pkg/provenance/sigstore_tuf_targets.json"))
	var out bytes.Buffer
	if !checkTrustUpdate(&out, compiled, current, now, false) || !strings.Contains(out.String(), "there is nothing newer") {
		t.Fatalf("the trust root compiled in:\n%s", out.String())
	}

	// From the trust root of 2025, today's is an update: four TUF roots later,
	// one of them with a keyholder replaced, and Rekor v2 among the logs.
	old, err := provenance.FromTUF(read(fixtures+"tuf/12.root.json"), read(fixtures+"tuf/12.targets.json"), read(fixtures+"tuf/trusted_root.12.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"13", "14", "15", "16"} {
		write(current, v+".root.json", read(fixtures+"tuf/"+v+".root.json"))
	}
	out.Reset()
	if !checkTrustUpdate(&out, old, current, now, false) {
		t.Fatalf("from 2025 to today:\n%s", out.String())
	}
	for _, want := range []string{"PASS  Sigstore trust root", "(TUF root 16, targets 14), newer than the one compiled into this binary (TUF root 12, targets 12)", "log2025-1.rekor.sigstore.dev", "timestamp authorities: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	var res trustUpdateResult
	if !checkTrustUpdate(&out, old, current, now, true) || json.Unmarshal(out.Bytes(), &res) != nil || !res.OK || !res.Newer || res.TUFRootVersion != 16 || res.TUFTargetsVersion != 14 || len(res.Logs) != 2 {
		t.Fatalf("JSON result %+v:\n%s", res, out.String())
	}

	// Refused: a root missing from the chain, the trust root of 2025 offered
	// to today's image, and a directory without the files.
	gap := t.TempDir()
	for _, name := range []string{"trusted_root.json", "targets.json", "13.root.json", "15.root.json", "16.root.json"} {
		write(gap, name, read(current+"/"+name))
	}
	back := t.TempDir()
	write(back, "trusted_root.json", read(fixtures+"tuf/trusted_root.12.json"))
	write(back, "targets.json", read(fixtures+"tuf/12.targets.json"))
	noTargets := t.TempDir()
	write(noTargets, "trusted_root.json", read(current+"/trusted_root.json"))
	for name, c := range map[string]struct {
		from *provenance.TrustRoot
		dir  string
		want string
	}{
		"a TUF root missing":  {old, gap, "expired"},
		"an older trust root": {compiled, back, "not newer"},
		"no trust root":       {compiled, t.TempDir(), "no such file"},
		"no targets metadata": {compiled, noTargets, "no such file"},
		"a TUF root that is a folder": {old, func() string {
			d := t.TempDir()
			write(d, "trusted_root.json", nil)
			write(d, "targets.json", nil)
			_ = os.Mkdir(d+"/13.root.json", 0o700)
			return d
		}(), "is a directory"},
	} {
		out.Reset()
		if checkTrustUpdate(&out, c.from, c.dir, now, false) || !strings.Contains(out.String(), "FAIL  Sigstore trust root") || !strings.Contains(out.String(), c.want) {
			t.Errorf("%s:\n%s", name, out.String())
		}
		out.Reset()
		if checkTrustUpdate(&out, c.from, c.dir, now, true) || json.Unmarshal(out.Bytes(), &res) != nil || res.OK || res.Error == "" {
			t.Errorf("%s, JSON: %s", name, out.String())
		}
	}
}
