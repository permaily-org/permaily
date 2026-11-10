package minimal_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	handoff "github.com/permaily-org/permaily/spikes/release-handoff"
	"github.com/permaily-org/permaily/spikes/release-handoff/minimal"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var production = minimal.Policy{Repository: "permaily-org/permaily", Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main"}

// edit returns the bundle with one field changed, addressed by its JSON path.
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

// flip changes one bit of a base64 value.
func flip(s any) string {
	raw, _ := base64.StdEncoding.DecodeString(s.(string))
	raw[len(raw)/2] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

// The minimal verifier must agree with sigstore-go on every case: accept the
// real release and refuse everything else. Where both refuse, the reasons may
// differ; where one accepts and the other does not, the spike has failed.
func TestAgreesWithSigstoreGo(t *testing.T) {
	trustedJSON := read(t, "sigstore-public-good.trusted_root.json")
	tr, err := minimal.ParseTrustRoot(trustedJSON, "https://rekor.sigstore.dev")
	if err != nil {
		t.Fatal(err)
	}
	provenance := read(t, "v0.5.0-provenance.bundle.json")
	pcrs := read(t, "v0.5.0-pcrs.json")

	cases := []struct {
		name   string
		policy minimal.Policy
		bundle []byte
		pcrs   []byte
		accept bool
	}{
		{"the release", production, provenance, pcrs, true},
		{"measurements of another image", production, provenance, bytes.Replace(pcrs, []byte("597b93ef"), []byte("00000000"), 1), false},
		{"another repository", minimal.Policy{Repository: "someone/permaily", Workflow: production.Workflow, Ref: production.Ref}, provenance, pcrs, false},
		{"another branch", minimal.Policy{Repository: production.Repository, Workflow: production.Workflow, Ref: "refs/heads/develop"}, provenance, pcrs, false},
		{"another workflow", minimal.Policy{Repository: production.Repository, Workflow: ".github/workflows/ci.yml", Ref: production.Ref}, provenance, pcrs, false},
		{"GitHub's release attestation (not in the public log)", production, read(t, "v0.5.0-github-release.bundle.json"), pcrs, false},
		{"not a bundle", production, []byte(`{}`), pcrs, false},
		{"statement signature changed", production, edit(t, provenance, func(d map[string]any) {
			sig := d["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
			sig["sig"] = flip(sig["sig"])
		}), pcrs, false},
		{"statement changed", production, edit(t, provenance, func(d map[string]any) {
			env := d["dsseEnvelope"].(map[string]any)
			raw, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
			env["payload"] = base64.StdEncoding.EncodeToString(bytes.Replace(raw, []byte("refs/heads/main"), []byte("refs/heads/evil"), 1))
		}), pcrs, false},
		{"log timestamp signature changed", production, edit(t, provenance, func(d map[string]any) {
			p := entry(d)["inclusionPromise"].(map[string]any)
			p["signedEntryTimestamp"] = flip(p["signedEntryTimestamp"])
		}), pcrs, false},
		{"log time moved", production, edit(t, provenance, func(d map[string]any) { entry(d)["integratedTime"] = "1794290095" }), pcrs, false},
		{"log entry body changed", production, edit(t, provenance, func(d map[string]any) { entry(d)["canonicalizedBody"] = flip(entry(d)["canonicalizedBody"]) }), pcrs, false},
		{"entry from another log", production, edit(t, provenance, func(d map[string]any) {
			id := entry(d)["logId"].(map[string]any)
			id["keyId"] = flip(id["keyId"])
		}), pcrs, false},
		{"no log entry", production, edit(t, provenance, func(d map[string]any) { d["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{} }), pcrs, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, errMin := minimal.VerifyRelease(c.policy, tr, c.bundle, c.pcrs)
			ref, errRef := handoff.VerifyRelease(handoff.Policy(c.policy), trustedJSON, c.bundle, c.pcrs)
			if (errMin == nil) != c.accept || (errRef == nil) != c.accept {
				t.Fatalf("want accept=%v; minimal: %v; sigstore-go: %v", c.accept, errMin, errRef)
			}
			if c.accept && (got.PCR0 != ref.PCR0 || got.Commit != ref.Commit || !got.LoggedAt.Equal(ref.LoggedAt)) {
				t.Fatalf("minimal %+v, sigstore-go %+v", got, ref)
			}
			if errMin != nil {
				t.Logf("minimal: %.110s", errMin)
			}
		})
	}
}

// What the minimal verifier checks beyond what sigstore-go was asked for here:
// the Merkle inclusion proof and the checkpoint the log signed.
func TestInclusionProofAndCheckpoint(t *testing.T) {
	tr, err := minimal.ParseTrustRoot(read(t, "sigstore-public-good.trusted_root.json"), "https://rekor.sigstore.dev")
	if err != nil {
		t.Fatal(err)
	}
	provenance, pcrs := read(t, "v0.5.0-provenance.bundle.json"), read(t, "v0.5.0-pcrs.json")
	proof := func(d map[string]any) map[string]any { return entry(d)["inclusionProof"].(map[string]any) }
	for name, change := range map[string]func(d map[string]any){
		"a proof hash changed":  func(d map[string]any) { h := proof(d)["hashes"].([]any); h[3] = flip(h[3]) },
		"a proof hash removed":  func(d map[string]any) { proof(d)["hashes"] = proof(d)["hashes"].([]any)[1:] },
		"index changed":         func(d map[string]any) { proof(d)["logIndex"] = "3657550835" },
		"root changed":          func(d map[string]any) { proof(d)["rootHash"] = flip(proof(d)["rootHash"]) },
		"tree size changed":     func(d map[string]any) { proof(d)["treeSize"] = "3657550862" },
		"index beyond the tree": func(d map[string]any) { proof(d)["logIndex"] = "9999999999" },
		"checkpoint signature changed": func(d map[string]any) {
			cp := proof(d)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.Replace(cp["envelope"].(string), "wNI9ajBGAiEA", "wNI9ajBGAiEB", 1)
		},
		"checkpoint from another log": func(d map[string]any) {
			cp := proof(d)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.ReplaceAll(cp["envelope"].(string), "rekor.sigstore.dev", "rekor.example.org")
		},
		"no checkpoint": func(d map[string]any) { proof(d)["checkpoint"] = map[string]any{"envelope": ""} },
	} {
		t.Run(name, func(t *testing.T) {
			if rel, err := minimal.VerifyRelease(production, tr, edit(t, provenance, change), pcrs); err == nil {
				t.Fatalf("accepted: %+v", rel)
			}
		})
	}
}

func TestParseTrustRoot(t *testing.T) {
	good := read(t, "sigstore-public-good.trusted_root.json")
	for name, c := range map[string]struct {
		doc []byte
		url string
	}{
		"not JSON":                  {[]byte("nope"), "https://rekor.sigstore.dev"},
		"empty":                     {[]byte("{}"), "https://rekor.sigstore.dev"},
		"unknown log":               {good, "https://rekor.example.org"},
		"a log key of another type": {good, "https://log2025-1.rekor.sigstore.dev"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := minimal.ParseTrustRoot(c.doc, c.url); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
