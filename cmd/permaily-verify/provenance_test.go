package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
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
