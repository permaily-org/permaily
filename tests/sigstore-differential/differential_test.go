package differential_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"

	differential "github.com/permaily-org/permaily/tests/sigstore-differential"
)

const productDir = "../../pkg/provenance/"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(productDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var production = provenance.Policy{
	Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
	Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main",
}

var staging = provenance.Policy{Repository: "permaily-org/permaily", Workflow: ".github/workflows/staging-image.yml", RefPrefix: "refs/heads/staging/"}

func edit(t *testing.T, bundle []byte, change func(doc map[string]any)) []byte {
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

func flip(s any) string {
	raw, _ := base64.StdEncoding.DecodeString(s.(string))
	raw[len(raw)/2] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

type testCase struct {
	name   string
	policy provenance.Policy
	bundle []byte
	pcrs   []byte
	accept bool
}

// compare runs both verifiers. They must give the same answer; where both
// refuse, the reasons may differ.
func compare(t *testing.T, trustedJSON []byte, cases []testCase) {
	t.Helper()
	tr, err := provenance.ParseTrustRoot(trustedJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, errOurs := provenance.Verify(c.policy, tr, c.bundle, c.pcrs)
			ref, errRef := differential.Verify(c.policy, trustedJSON, c.bundle, c.pcrs)
			if (errOurs == nil) != c.accept || (errRef == nil) != c.accept {
				t.Fatalf("want accept=%v; pkg/provenance: %v; sigstore-go: %v", c.accept, errOurs, errRef)
			}
			if c.accept && (got.PCR0 != ref.PCR0 || got.Commit != ref.Commit || got.Ref != ref.Ref || !got.LoggedAt.Equal(ref.LoggedAt)) {
				t.Fatalf("pkg/provenance %+v, sigstore-go %+v", got, ref)
			}
		})
	}
}

// The published release v0.5.0, and what an operator would try with it.
func TestRealRelease(t *testing.T) {
	bundle, pcrs := read(t, "testdata/v0.5.0-provenance.bundle.json"), read(t, "testdata/v0.5.0-pcrs.json")
	with := func(change func(p *provenance.Policy)) provenance.Policy {
		p := production
		change(&p)
		return p
	}
	compare(t, read(t, "sigstore_trusted_root.json"), []testCase{
		{"the release", production, bundle, pcrs, true},
		{"measurements of another image", production, bundle, bytes.Replace(pcrs, []byte("597b93ef"), []byte("00000000"), 1), false},
		{"another repository", with(func(p *provenance.Policy) { p.Repository = "someone/permaily" }), bundle, pcrs, false},
		{"another repository id", with(func(p *provenance.Policy) { p.RepositoryID = "1" }), bundle, pcrs, false},
		{"another owner id", with(func(p *provenance.Policy) { p.OwnerID = "1" }), bundle, pcrs, false},
		{"another branch", with(func(p *provenance.Policy) { p.Ref = "refs/heads/develop" }), bundle, pcrs, false},
		{"another workflow", with(func(p *provenance.Policy) { p.Workflow = ".github/workflows/ci.yml" }), bundle, pcrs, false},
		{"the staging policy", staging, bundle, pcrs, false},
		{"GitHub's release attestation (not in the public log)", production, read(t, "testdata/v0.5.0-github-release.bundle.json"), pcrs, false},
		{"not a bundle", production, []byte(`{}`), pcrs, false},
		{"statement signature changed", production, edit(t, bundle, func(d map[string]any) {
			sig := d["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
			sig["sig"] = flip(sig["sig"])
		}), pcrs, false},
		{"statement changed", production, edit(t, bundle, func(d map[string]any) {
			env := d["dsseEnvelope"].(map[string]any)
			raw, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
			env["payload"] = base64.StdEncoding.EncodeToString(bytes.Replace(raw, []byte("refs/heads/main"), []byte("refs/heads/evil"), 1))
		}), pcrs, false},
		{"log timestamp signature changed", production, edit(t, bundle, func(d map[string]any) {
			p := entry(d)["inclusionPromise"].(map[string]any)
			p["signedEntryTimestamp"] = flip(p["signedEntryTimestamp"])
		}), pcrs, false},
		{"log time moved", production, edit(t, bundle, func(d map[string]any) { entry(d)["integratedTime"] = "1794290095" }), pcrs, false},
		{"log entry body changed", production, edit(t, bundle, func(d map[string]any) { entry(d)["canonicalizedBody"] = flip(entry(d)["canonicalizedBody"]) }), pcrs, false},
		{"entry from another log", production, edit(t, bundle, func(d map[string]any) {
			id := entry(d)["logId"].(map[string]any)
			id["keyId"] = flip(id["keyId"])
		}), pcrs, false},
		{"no log entry", production, edit(t, bundle, func(d map[string]any) { d["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{} }), pcrs, false},
	})
}

// A signature recorded in Sigstore's public Rekor v2 log by the canary of the
// workflow sigstore-watch, with Sigstore's timestamp and witnessed checkpoint.
func TestRealRekor2Entry(t *testing.T) {
	bundle, pcrs := read(t, "testdata/rekor2-canary.bundle.json"), read(t, "testdata/rekor2-canary-pcrs.json")
	canary := provenance.Policy{
		Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
		Workflow: ".github/workflows/sigstore-watch.yml", Ref: "refs/heads/feat/rekor-v2",
	}
	checkpoint := func(change func(text, sigs string) string) []byte {
		return edit(t, bundle, func(d map[string]any) {
			cp := entry(d)["inclusionProof"].(map[string]any)["checkpoint"].(map[string]any)
			text, sigs, _ := strings.Cut(cp["envelope"].(string), "\n\n")
			cp["envelope"] = change(text, sigs)
		})
	}
	compare(t, read(t, "sigstore_trusted_root.json"), []testCase{
		{"the canary", canary, bundle, pcrs, true},
		{"under the policy of releases", production, bundle, pcrs, false},
		{"measurements of another image", canary, bundle, bytes.Replace(pcrs, []byte(`"PCR0": "`), []byte(`"PCR0": "00`), 1), false},
		{"without its timestamp", canary, edit(t, bundle, func(d map[string]any) {
			delete(d["verificationMaterial"].(map[string]any), "timestampVerificationData")
		}), pcrs, false},
		{"only the witnesses signed the checkpoint", canary, checkpoint(func(text, sigs string) string {
			return text + "\n\n" + strings.Join(strings.Split(sigs, "\n")[1:], "\n")
		}), pcrs, false},
		{"only the log signed the checkpoint", canary, checkpoint(func(text, sigs string) string {
			return text + "\n\n" + strings.Split(sigs, "\n")[0] + "\n"
		}), pcrs, true},
		{"at another position of the log", canary, edit(t, bundle, func(d map[string]any) { entry(d)["logIndex"] = "193611424" }), pcrs, false},
		{"statement signature changed", canary, edit(t, bundle, func(d map[string]any) {
			sig := d["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
			sig["sig"] = flip(sig["sig"])
		}), pcrs, false},
	})
}

var pcrsA = []byte(`{"PCR0":"` + strings.Repeat("aa", 48) + `","PCR1":"` + strings.Repeat("bb", 48) + `","PCR2":"` + strings.Repeat("cc", 48) + `"}`)

// Bundles from the test authority that the product's tests use. That
// sigstore-go accepts them shows they have the shape of real ones, and the
// two verifiers must agree on every identity.
func TestTestAuthority(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	trustedJSON, err := a.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	build := func(change func(b *provenancetest.Build)) []byte {
		b := provenancetest.Build{
			Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
			Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main", Commit: strings.Repeat("12", 20),
		}
		change(&b)
		bundle, err := a.Attest(b, []byte("another file"), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	stagingBuild := func(ref string) func(b *provenancetest.Build) {
		return func(b *provenancetest.Build) { b.Workflow, b.Ref = staging.Workflow, ref }
	}
	cases := []testCase{
		{"the release workflow on main", production, build(func(*provenancetest.Build) {}), pcrsA, true},
		{"a self-hosted runner", production, build(func(b *provenancetest.Build) { b.Runner = "self-hosted" }), pcrsA, false},
		{"another token issuer", production, build(func(b *provenancetest.Build) { b.Issuer = "https://token.example.org" }), pcrsA, false},
		{"a fork", production, build(func(b *provenancetest.Build) { b.Repository = "someone/permaily" }), pcrsA, false},
		{"a repository that took the name", production, build(func(b *provenancetest.Build) { b.RepositoryID = "999" }), pcrsA, false},
		{"an owner that took the name", production, build(func(b *provenancetest.Build) { b.OwnerID = "999" }), pcrsA, false},
		{"a pull request", production, build(func(b *provenancetest.Build) { b.Ref = "refs/pull/7/merge" }), pcrsA, false},
		{"a staging build under the production policy", production, build(stagingBuild("refs/heads/staging/a")), pcrsA, false},
		{"a staging build under the staging policy", staging, build(stagingBuild("refs/heads/staging/a")), pcrsA, true},
		{"the prefix alone is not a ref", staging, build(stagingBuild("refs/heads/staging/")), pcrsA, false},
		{"another branch under the staging policy", staging, build(stagingBuild("refs/heads/main")), pcrsA, false},
		{"a release under the staging policy", staging, build(func(*provenancetest.Build) {}), pcrsA, false},
		{"a certificate issued an hour before the log time", production, build(func(b *provenancetest.Build) { b.IssuedAt = time.Now().Add(-time.Hour) }), pcrsA, false},
		{"a certificate issued an hour after the log time", production, build(func(b *provenancetest.Build) { b.IssuedAt = time.Now().Add(time.Hour) }), pcrsA, false},
		{"measurements the statement does not name", production, build(func(*provenancetest.Build) {}), bytes.Replace(pcrsA, []byte("aa"), []byte("ab"), 1), false},
	}
	// Inclusion proofs in trees of several shapes.
	for _, shape := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {3, 4}, {7, 0}, {8, 0}, {0, 8}, {13, 20}} {
		a.Before, a.After = shape[0], shape[1]
		cases = append(cases, testCase{fmt.Sprintf("entry %d of %d", shape[0], shape[0]+1+shape[1]), production, build(func(*provenancetest.Build) {}), pcrsA, true})
	}
	compare(t, trustedJSON, cases)
}

func proof(doc map[string]any) map[string]any {
	return entry(doc)["inclusionProof"].(map[string]any)
}

func setStamps(doc map[string]any, der ...[]byte) {
	list := []any{}
	for _, d := range der {
		list = append(list, map[string]any{"signedTimestamp": base64.StdEncoding.EncodeToString(d)})
	}
	doc["verificationMaterial"].(map[string]any)["timestampVerificationData"] = map[string]any{"rfc3161Timestamps": list}
}

func firstStamp(t *testing.T, bundle []byte) (stamp, sig []byte) {
	t.Helper()
	edit(t, bundle, func(doc map[string]any) {
		list := doc["verificationMaterial"].(map[string]any)["timestampVerificationData"].(map[string]any)["rfc3161Timestamps"].([]any)
		stamp, _ = base64.StdEncoding.DecodeString(list[0].(map[string]any)["signedTimestamp"].(string))
		sig, _ = base64.StdEncoding.DecodeString(doc["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)["sig"].(string))
	})
	return stamp, sig
}

// Bundles recorded the way Rekor v2 records them: the entry rebuilt from the
// signature, a checkpoint signed with Ed25519 and the time from a timestamp
// authority. The two verifiers must agree on what counts.
func TestRekor2(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	trustedJSON, err := a.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	build := func(change func(b *provenancetest.Build)) []byte {
		b := provenancetest.Build{
			Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
			Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main", Commit: strings.Repeat("12", 20),
			Rekor2: true, At: at,
		}
		change(&b)
		bundle, err := a.Attest(b, []byte("another file"), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	bundle := build(func(*provenancetest.Build) {})
	second := build(func(*provenancetest.Build) {})
	own, sig := firstStamp(t, bundle)
	theirs, _ := firstStamp(t, second)
	stranger, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	strangerStamp, err := stranger.Timestamp(sig, at)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := func(change func(text string) string) func(d map[string]any) {
		return func(d map[string]any) {
			cp := proof(d)["checkpoint"].(map[string]any)
			cp["envelope"] = change(cp["envelope"].(string))
		}
	}

	cases := []testCase{
		{"the release workflow on main", production, bundle, pcrsA, true},
		{"a stranger's timestamp before the authority's", production, edit(t, bundle, func(d map[string]any) { setStamps(d, strangerStamp, own) }), pcrsA, true},
		{"a self-hosted runner", production, build(func(b *provenancetest.Build) { b.Runner = "self-hosted" }), pcrsA, false},
		{"a pull request", production, build(func(b *provenancetest.Build) { b.Ref = "refs/pull/7/merge" }), pcrsA, false},
		{"a staging build under the production policy", production, build(func(b *provenancetest.Build) { b.Workflow, b.Ref = staging.Workflow, "refs/heads/staging/a" }), pcrsA, false},
		{"a staging build under the staging policy", staging, build(func(b *provenancetest.Build) { b.Workflow, b.Ref = staging.Workflow, "refs/heads/staging/a" }), pcrsA, true},
		{"measurements the statement does not name", production, bundle, bytes.Replace(pcrsA, []byte("aa"), []byte("ab"), 1), false},
		{"a certificate issued an hour before the timestamp", production, build(func(b *provenancetest.Build) { b.IssuedAt = at.Add(-time.Hour) }), pcrsA, false},
		{"a certificate issued an hour after the timestamp", production, build(func(b *provenancetest.Build) { b.IssuedAt = at.Add(time.Hour) }), pcrsA, false},
		{"no timestamp", production, edit(t, bundle, func(d map[string]any) { setStamps(d) }), pcrsA, false},
		{"only a stranger's timestamp", production, edit(t, bundle, func(d map[string]any) { setStamps(d, strangerStamp) }), pcrsA, false},
		{"a timestamp for another signature", production, edit(t, bundle, func(d map[string]any) { setStamps(d, theirs) }), pcrsA, false},
		{"a timestamp with a bit changed", production, edit(t, bundle, func(d map[string]any) {
			raw := bytes.Clone(own)
			raw[len(raw)-20] ^= 1
			setStamps(d, raw)
		}), pcrsA, false},
		{"statement changed", production, edit(t, bundle, func(d map[string]any) {
			env := d["dsseEnvelope"].(map[string]any)
			raw, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
			env["payload"] = base64.StdEncoding.EncodeToString(bytes.Replace(raw, []byte("file-0"), []byte("file-9"), 1))
		}), pcrsA, false},
		{"statement signature changed", production, edit(t, bundle, func(d map[string]any) {
			s := d["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
			s["sig"] = flip(s["sig"])
		}), pcrsA, false},
		{"another certificate", production, edit(t, bundle, func(d map[string]any) {
			edit(t, second, func(o map[string]any) {
				d["verificationMaterial"].(map[string]any)["certificate"] = o["verificationMaterial"].(map[string]any)["certificate"]
			})
		}), pcrsA, false},
		{"another position in the tree", production, edit(t, bundle, func(d map[string]any) { entry(d)["logIndex"] = "3" }), pcrsA, false},
		{"a proof with a hash changed", production, edit(t, bundle, func(d map[string]any) {
			h := proof(d)["hashes"].([]any)
			h[0] = flip(h[0])
		}), pcrsA, false},
		{"a checkpoint with another size", production, edit(t, bundle, checkpoint(func(s string) string { return strings.Replace(s, "\n7\n", "\n8\n", 1) })), pcrsA, false},
		{"a checkpoint of another log", production, edit(t, bundle, checkpoint(func(s string) string { return strings.ReplaceAll(s, a.Origin2, "log2098-1.rekor.test") })), pcrsA, false},
		{"a checkpoint shaped like the first Rekor's", production, edit(t, bundle, checkpoint(func(s string) string { return strings.Replace(s, a.Origin2+"\n", a.Origin2+" - 1\n", 1) })), pcrsA, false},
		{"a checkpoint without signatures", production, edit(t, bundle, checkpoint(func(s string) string {
			text, _, _ := strings.Cut(s, "\n\n")
			return text + "\n\n"
		})), pcrsA, false},
		{"the entry named as the first Rekor's log", production, edit(t, bundle, func(d map[string]any) {
			entry(d)["logId"] = map[string]any{"keyId": base64.StdEncoding.EncodeToString(a.LogID)}
		}), pcrsA, false},
		{"an unknown log", production, edit(t, bundle, func(d map[string]any) {
			id := entry(d)["logId"].(map[string]any)
			id["keyId"] = flip(id["keyId"])
		}), pcrsA, false},
		{"the first Rekor's entry named as the second log's", production, edit(t, func() []byte {
			b := provenancetest.Build{
				Repository: "permaily-org/permaily", RepositoryID: "1448574502", OwnerID: "353453526",
				Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main", Commit: strings.Repeat("12", 20),
			}
			first, err := a.Attest(b, pcrsA)
			if err != nil {
				t.Fatal(err)
			}
			return first
		}(), func(d map[string]any) {
			entry(d)["logId"] = map[string]any{"keyId": base64.StdEncoding.EncodeToString(a.Log2ID)}
		}), pcrsA, false},
	}
	for _, shape := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {3, 4}, {7, 0}, {8, 0}, {0, 8}, {13, 20}} {
		a.Before, a.After = shape[0], shape[1]
		cases = append(cases, testCase{fmt.Sprintf("entry %d of %d", shape[0], shape[0]+1+shape[1]), production, build(func(*provenancetest.Build) {}), pcrsA, true})
	}
	compare(t, trustedJSON, cases)
}
