package handoff

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Fixtures: the pcrs.json of release v0.5.0, the two attestations GitHub holds
// for it, and Sigstore's public trust root, all as published.
func TestVerifyRelease(t *testing.T) {
	trusted := read(t, "sigstore-public-good.trusted_root.json")
	provenance := read(t, "v0.5.0-provenance.bundle.json")
	pcrs := read(t, "v0.5.0-pcrs.json")

	rel, err := VerifyRelease(Production, trusted, provenance, pcrs)
	if err != nil {
		t.Fatal(err)
	}
	if rel.PCR0 != "597b93ef695e9ad30125df03d095b3fb21264474ebb1e4799a7ba7275fa149b5ee37e915f0e2a4c03f1e0bdbfb17330b" {
		t.Fatalf("PCR0 %s", rel.PCR0)
	}
	if rel.Commit != "e18a6ed955b694126098bcaaf2b0b2e77bc20d8b" || rel.LoggedAt.Format("2006-01-02") != "2026-11-10" {
		t.Fatalf("release %+v", rel)
	}

	// Every one of these is how an operator would try to pass a private image.
	otherPCRs := bytes.Replace(pcrs, []byte("597b93ef"), []byte("00000000"), 1)
	cases := []struct {
		name    string
		policy  Policy
		bundle  []byte
		pcrs    []byte
		wantErr string
	}{
		{"measurements of another image", Production, provenance, otherPCRs, "not a release"},
		{"signature edited", Production, bytes.Replace(provenance, []byte(`"sig":"M`), []byte(`"sig":"N`), 1), pcrs, ""},
		{"another repository's release workflow", Policy{"someone/permaily", ".github/workflows/release.yml", "refs/heads/main"}, provenance, pcrs, "not a release"},
		{"a workflow run on another branch", Policy{"permaily-org/permaily", ".github/workflows/release.yml", "refs/heads/develop"}, provenance, pcrs, "not a release"},
		{"another workflow of the repository", Policy{"permaily-org/permaily", ".github/workflows/ci.yml", "refs/heads/main"}, provenance, pcrs, "not a release"},
		{"an attestation that is not in the public log", Production, read(t, "v0.5.0-github-release.bundle.json"), pcrs, ""},
		{"not a bundle", Production, []byte(`{}`), pcrs, ""},
		{"empty trust root", Production, provenance, pcrs, "trusted root"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := trusted
			if c.wantErr == "trusted root" {
				root = []byte(`{}`)
			}
			rel, err := VerifyRelease(c.policy, root, c.bundle, c.pcrs)
			if err == nil {
				t.Fatalf("accepted: %+v", rel)
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q lacks %q", err, c.wantErr)
			}
			t.Logf("refused: %.160s", err)
		})
	}
}
