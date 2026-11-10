package provenance_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"
)

func read(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The policy the production image carries, with GitHub's ids of the repository and its owner.
var production = provenance.Policy{
	Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
	Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main",
}

// edit returns the bundle with something changed.
func edit(t testing.TB, bundle []byte, change func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(bundle, &doc); err != nil {
		t.Fatal(err)
	}
	change(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func entry(doc map[string]any) map[string]any {
	return doc["verificationMaterial"].(map[string]any)["tlogEntries"].([]any)[0].(map[string]any)
}

func proof(doc map[string]any) map[string]any {
	return entry(doc)["inclusionProof"].(map[string]any)
}

// flip changes one bit of a base64 value.
func flip(s any) string {
	raw, _ := base64.StdEncoding.DecodeString(s.(string))
	raw[len(raw)/2] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

// Fixtures: the pcrs.json of release v0.5.0 and the two attestations GitHub
// holds for it, as published, against the trust root compiled into the package.
func TestVerifyARealRelease(t *testing.T) {
	tr, err := provenance.PublicGood()
	if err != nil {
		t.Fatal(err)
	}
	bundle, pcrs := read(t, "v0.5.0-provenance.bundle.json"), read(t, "v0.5.0-pcrs.json")

	rel, err := provenance.Verify(production, tr, bundle, pcrs)
	if err != nil {
		t.Fatal(err)
	}
	const pcr0 = "597b93ef695e9ad30125df03d095b3fb21264474ebb1e4799a7ba7275fa149b5ee37e915f0e2a4c03f1e0bdbfb17330b"
	if rel.PCR0 != pcr0 || hex.EncodeToString(rel.PCRs[0]) != pcr0 || len(rel.PCRs[1]) != 48 || len(rel.PCRs[2]) != 48 {
		t.Fatalf("measurements %+v", rel)
	}
	if rel.Commit != "e18a6ed955b694126098bcaaf2b0b2e77bc20d8b" || rel.Ref != "refs/heads/main" || rel.LoggedAt.Format(time.RFC3339) != "2026-11-10T05:54:54Z" {
		t.Fatalf("release %+v", rel)
	}

	with := func(change func(p *provenance.Policy)) provenance.Policy {
		p := production
		change(&p)
		return p
	}
	// Every one of these is how an operator would try to pass a private image.
	cases := []struct {
		name   string
		policy provenance.Policy
		bundle []byte
		pcrs   []byte
		want   error
	}{
		{"measurements of another image", production, bundle, bytes.Replace(pcrs, []byte("597b93ef"), []byte("00000000"), 1), provenance.ErrNotARelease},
		{"another repository", with(func(p *provenance.Policy) { p.Repository = "someone/permaily" }), bundle, pcrs, provenance.ErrNotARelease},
		{"a repository with the same name and another id", with(func(p *provenance.Policy) { p.RepositoryID = "1" }), bundle, pcrs, provenance.ErrNotARelease},
		{"an owner with the same name and another id", with(func(p *provenance.Policy) { p.OwnerID = "1" }), bundle, pcrs, provenance.ErrNotARelease},
		{"another branch", with(func(p *provenance.Policy) { p.Ref = "refs/heads/develop" }), bundle, pcrs, provenance.ErrNotARelease},
		{"another workflow", with(func(p *provenance.Policy) { p.Workflow = ".github/workflows/ci.yml" }), bundle, pcrs, provenance.ErrNotARelease},
		{"a policy without a ref", with(func(p *provenance.Policy) { p.Ref = "" }), bundle, pcrs, provenance.ErrMalformed},
		{"a policy with a ref and a prefix", with(func(p *provenance.Policy) { p.RefPrefix = "refs/heads/" }), bundle, pcrs, provenance.ErrMalformed},
		{"GitHub's release attestation, which is not in the public log", production, read(t, "v0.5.0-github-release.bundle.json"), pcrs, nil},
		{"not a bundle", production, []byte(`{}`), pcrs, provenance.ErrMalformed},
		{"not JSON", production, []byte(`nope`), pcrs, provenance.ErrMalformed},
		{"a bundle too large to be one", production, bytes.Repeat([]byte(" "), 300<<10), pcrs, provenance.ErrMalformed},
		{"statement signature changed", production, edit(t, bundle, func(d map[string]any) {
			sig := d["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
			sig["sig"] = flip(sig["sig"])
		}), pcrs, provenance.ErrUntrusted},
		{"statement changed", production, edit(t, bundle, func(d map[string]any) {
			env := d["dsseEnvelope"].(map[string]any)
			raw, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
			env["payload"] = base64.StdEncoding.EncodeToString(bytes.Replace(raw, []byte("refs/heads/main"), []byte("refs/heads/evil"), 1))
		}), pcrs, provenance.ErrUntrusted},
		{"certificate changed", production, edit(t, bundle, func(d map[string]any) {
			c := d["verificationMaterial"].(map[string]any)["certificate"].(map[string]any)
			c["rawBytes"] = flip(c["rawBytes"])
		}), pcrs, nil},
		{"log timestamp signature changed", production, edit(t, bundle, func(d map[string]any) {
			p := entry(d)["inclusionPromise"].(map[string]any)
			p["signedEntryTimestamp"] = flip(p["signedEntryTimestamp"])
		}), pcrs, provenance.ErrUntrusted},
		{"log time moved", production, edit(t, bundle, func(d map[string]any) { entry(d)["integratedTime"] = "1794290095" }), pcrs, provenance.ErrUntrusted},
		{"log time that is not a number", production, edit(t, bundle, func(d map[string]any) { entry(d)["integratedTime"] = "soon" }), pcrs, provenance.ErrMalformed},
		{"log entry body changed", production, edit(t, bundle, func(d map[string]any) { entry(d)["canonicalizedBody"] = flip(entry(d)["canonicalizedBody"]) }), pcrs, nil},
		{"entry from another log", production, edit(t, bundle, func(d map[string]any) {
			id := entry(d)["logId"].(map[string]any)
			id["keyId"] = flip(id["keyId"])
		}), pcrs, provenance.ErrUntrusted},
		{"no log entry", production, edit(t, bundle, func(d map[string]any) { d["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{} }), pcrs, provenance.ErrMalformed},
		{"two log entries", production, edit(t, bundle, func(d map[string]any) {
			m := d["verificationMaterial"].(map[string]any)
			m["tlogEntries"] = append(m["tlogEntries"].([]any), entry(d))
		}), pcrs, provenance.ErrMalformed},

		// The Merkle inclusion proof and the checkpoint the log signed.
		{"a proof hash changed", production, edit(t, bundle, func(d map[string]any) { h := proof(d)["hashes"].([]any); h[3] = flip(h[3]) }), pcrs, provenance.ErrUntrusted},
		{"a proof hash removed", production, edit(t, bundle, func(d map[string]any) { proof(d)["hashes"] = proof(d)["hashes"].([]any)[1:] }), pcrs, provenance.ErrUntrusted},
		{"a proof hash of another size", production, edit(t, bundle, func(d map[string]any) { h := proof(d)["hashes"].([]any); h[0] = "AAAA" }), pcrs, provenance.ErrUntrusted},
		{"index changed", production, edit(t, bundle, func(d map[string]any) { proof(d)["logIndex"] = "3657550835" }), pcrs, provenance.ErrUntrusted},
		{"root changed", production, edit(t, bundle, func(d map[string]any) { proof(d)["rootHash"] = flip(proof(d)["rootHash"]) }), pcrs, provenance.ErrUntrusted},
		{"tree size changed", production, edit(t, bundle, func(d map[string]any) { proof(d)["treeSize"] = "3657550862" }), pcrs, provenance.ErrUntrusted},
		{"index beyond the tree", production, edit(t, bundle, func(d map[string]any) { proof(d)["logIndex"] = "9999999999" }), pcrs, provenance.ErrUntrusted},
		{"checkpoint signature changed", production, edit(t, bundle, func(d map[string]any) {
			cp := proof(d)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.Replace(cp["envelope"].(string), "wNI9ajBGAiEA", "wNI9ajBGAiEB", 1)
		}), pcrs, provenance.ErrUntrusted},
		{"checkpoint from another log", production, edit(t, bundle, func(d map[string]any) {
			cp := proof(d)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.ReplaceAll(cp["envelope"].(string), "rekor.sigstore.dev", "rekor.example.org")
		}), pcrs, provenance.ErrUntrusted},
		{"no checkpoint", production, edit(t, bundle, func(d map[string]any) { proof(d)["checkpoint"] = map[string]any{"envelope": ""} }), pcrs, provenance.ErrUntrusted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rel, err := provenance.Verify(c.policy, tr, c.bundle, c.pcrs)
			if err == nil {
				t.Fatalf("accepted: %+v", rel)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestParseTrustRoot(t *testing.T) {
	good, err := os.ReadFile("sigstore_trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		doc []byte
		url string
	}{
		"not JSON":                      {[]byte("nope"), provenance.PublicGoodRekor},
		"empty":                         {[]byte("{}"), provenance.PublicGoodRekor},
		"unknown log":                   {good, "https://rekor.example.org"},
		"a log key of another type":     {good, "https://log2025-1.rekor.sigstore.dev"},
		"a certificate that is not one": {[]byte(`{"certificateAuthorities":[{"certChain":{"certificates":[{"rawBytes":"AAAA"}]}}]}`), provenance.PublicGoodRekor},
		"an authority without a root":   {[]byte(`{"certificateAuthorities":[{"certChain":{"certificates":[]}}]}`), provenance.PublicGoodRekor},
		"a log key that is not one":     {[]byte(`{"tlogs":[{"baseUrl":"https://rekor.sigstore.dev","publicKey":{"rawBytes":"AAAA"}}]}`), provenance.PublicGoodRekor},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := provenance.ParseTrustRoot(c.doc, c.url); !errors.Is(err, provenance.ErrMalformed) {
				t.Fatalf("got %v", err)
			}
		})
	}
	tr, err := provenance.ParseTrustRoot(good, provenance.PublicGoodRekor)
	if err != nil {
		t.Fatal(err)
	}
	// Fulcio's first authority stopped issuing at the end of 2022.
	if len(tr.Authorities) != 2 || tr.Authorities[0].NotAfter.Year() != 2022 || !tr.Authorities[1].NotAfter.IsZero() || tr.Log.Origin != "rekor.sigstore.dev" {
		t.Fatalf("trust root %+v", tr)
	}
}

var pcrsA = []byte(`{"PCR0":"` + strings.Repeat("aa", 48) + `","PCR1":"` + strings.Repeat("bb", 48) + `","PCR2":"` + strings.Repeat("cc", 48) + `"}`)

func testBuild() provenancetest.Build {
	return provenancetest.Build{
		Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
		Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main", Commit: strings.Repeat("12", 20),
	}
}

// Bundles made by a test authority, to try what the real fixture cannot show:
// certificates issued to other identities, and authorities out of their period.
func TestVerifyIdentity(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := a.TrustRoot()
	staging := provenance.Policy{Repository: "permaily-org/permaily", Workflow: ".github/workflows/staging-image.yml", RefPrefix: "refs/heads/staging/"}

	cases := []struct {
		name   string
		policy provenance.Policy
		build  func(b *provenancetest.Build)
		want   error
	}{
		{"the release workflow on main", production, func(*provenancetest.Build) {}, nil},
		{"a self-hosted runner", production, func(b *provenancetest.Build) { b.Runner = "self-hosted" }, provenance.ErrNotARelease},
		{"another token issuer", production, func(b *provenancetest.Build) { b.Issuer = "https://token.example.org" }, provenance.ErrNotARelease},
		{"a fork", production, func(b *provenancetest.Build) { b.Repository = "someone/permaily" }, provenance.ErrNotARelease},
		{"a repository that took the name", production, func(b *provenancetest.Build) { b.RepositoryID = "999" }, provenance.ErrNotARelease},
		{"an owner that took the name", production, func(b *provenancetest.Build) { b.OwnerID = "999" }, provenance.ErrNotARelease},
		{"a certificate without ids", production, func(b *provenancetest.Build) { b.RepositoryID, b.OwnerID = "", "" }, provenance.ErrNotARelease},
		{"a pull request", production, func(b *provenancetest.Build) { b.Ref = "refs/pull/7/merge" }, provenance.ErrNotARelease},
		{"a tag named like the branch", production, func(b *provenancetest.Build) { b.Ref = "refs/tags/main" }, provenance.ErrNotARelease},
		{"a staging build under the production policy", production, func(b *provenancetest.Build) {
			b.Workflow, b.Ref = staging.Workflow, "refs/heads/staging/a"
		}, provenance.ErrNotARelease},
		{"a staging build under the staging policy", staging, func(b *provenancetest.Build) {
			b.Workflow, b.Ref = staging.Workflow, "refs/heads/staging/a"
		}, nil},
		{"the prefix alone is not a ref", staging, func(b *provenancetest.Build) { b.Workflow, b.Ref = staging.Workflow, "refs/heads/staging/" }, provenance.ErrNotARelease},
		{"another branch under the staging policy", staging, func(b *provenancetest.Build) { b.Workflow, b.Ref = staging.Workflow, "refs/heads/main" }, provenance.ErrNotARelease},
		{"a release under the staging policy", staging, func(*provenancetest.Build) {}, provenance.ErrNotARelease},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := testBuild()
			c.build(&b)
			bundle, err := a.Attest(b, []byte("another file"), pcrsA)
			if err != nil {
				t.Fatal(err)
			}
			rel, err := provenance.Verify(c.policy, tr, bundle, pcrsA)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %+v, %v; want %v", rel, err, c.want)
			}
			if err == nil && (rel.PCR0 != strings.Repeat("aa", 48) || rel.Commit != b.Commit || rel.Ref != b.Ref) {
				t.Fatalf("release %+v", rel)
			}
		})
	}

	t.Run("measurements the statement does not name", func(t *testing.T) {
		bundle, err := a.Attest(testBuild(), []byte("another file"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrNotARelease) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a signed file that is not measurements", func(t *testing.T) {
		for _, file := range [][]byte{[]byte("nope"), []byte(`{"PCR0":"aa"}`), bytes.Repeat([]byte(" "), 5<<10)} {
			bundle, err := a.Attest(testBuild(), file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provenance.Verify(production, tr, bundle, file); !errors.Is(err, provenance.ErrMalformed) {
				t.Fatalf("got %v", err)
			}
		}
	})
	t.Run("another authority and another log", func(t *testing.T) {
		other, err := provenancetest.New()
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := other.Attest(testBuild(), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("got %v", err)
		}
		// The right log with a certificate from the wrong authority.
		mixed := other.TrustRoot()
		mixed.Authorities = tr.Authorities
		if _, err := provenance.Verify(production, mixed, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an authority or a log outside its period", func(t *testing.T) {
		bundle, err := a.Attest(testBuild(), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		retired := a.TrustRoot()
		retired.Authorities[0].NotAfter = time.Now().Add(-time.Minute)
		if _, err := provenance.Verify(production, retired, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("retired authority: %v", err)
		}
		future := a.TrustRoot()
		future.Authorities[0].NotBefore = time.Now().Add(time.Hour)
		if _, err := provenance.Verify(production, future, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("future authority: %v", err)
		}
		oldLog := a.TrustRoot()
		oldLog.Log.NotAfter = time.Now().Add(-time.Minute)
		if _, err := provenance.Verify(production, oldLog, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("retired log: %v", err)
		}
	})
	t.Run("a certificate that was not valid when it was logged", func(t *testing.T) {
		for _, issued := range []time.Duration{-time.Hour, time.Hour} {
			b := testBuild()
			b.IssuedAt = time.Now().Add(issued)
			bundle, err := a.Attest(b, pcrsA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
				t.Fatalf("issued %s from the log time: %v", issued, err)
			}
		}
	})
	t.Run("no trust root", func(t *testing.T) {
		bundle, err := a.Attest(testBuild(), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range []*provenance.TrustRoot{nil, {}} {
			if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrMalformed) {
				t.Fatalf("got %v", err)
			}
		}
	})
}

// Inclusion proofs of every leaf in trees of every shape up to 40 entries.
func TestInclusionProofShapes(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := a.TrustRoot()
	for size := 1; size <= 40; size++ {
		for index := 0; index < size; index += 1 + size/8 {
			a.Before, a.After = index, size-index-1
			bundle, err := a.Attest(testBuild(), pcrsA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provenance.Verify(production, tr, bundle, pcrsA); err != nil {
				t.Fatalf("leaf %d of %d: %v", index, size, err)
			}
			if size > 1 {
				wrong := edit(t, bundle, func(d map[string]any) { proof(d)["logIndex"] = fmt.Sprint((index + 1) % size) })
				if _, err := provenance.Verify(production, tr, wrong, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
					t.Fatalf("leaf %d of %d accepted as another leaf: %v", index, size, err)
				}
			}
		}
	}
}

func FuzzVerify(f *testing.F) {
	tr, err := provenance.PublicGood()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(read(f, "v0.5.0-provenance.bundle.json"), read(f, "v0.5.0-pcrs.json"))
	f.Add(read(f, "v0.5.0-github-release.bundle.json"), read(f, "v0.5.0-pcrs.json"))
	f.Add([]byte(`{}`), []byte(`{}`))
	real := read(f, "v0.5.0-provenance.bundle.json")
	f.Fuzz(func(t *testing.T, bundle, pcrs []byte) {
		rel, err := provenance.Verify(production, tr, bundle, pcrs)
		if err != nil {
			return
		}
		// Nothing but the published release can be accepted: the fuzzer has no signing key.
		if rel.Commit != "e18a6ed955b694126098bcaaf2b0b2e77bc20d8b" || !bytes.Contains(pcrs, []byte("597b93ef695e9ad3")) || len(bundle) < len(real)/2 {
			t.Fatalf("accepted %+v", rel)
		}
	})
}
