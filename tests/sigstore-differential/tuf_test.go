package differential_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"

	differential "github.com/permaily-org/permaily/tests/sigstore-differential"
)

type tufCase struct {
	name    string
	roots   [][]byte
	targets []byte
	trusted []byte
	at      time.Time
	accept  bool
}

// compareTUF runs provenance's Update and go-tuf on the same update. They must
// give the same answer, and the same versions when they accept.
func compareTUF(t *testing.T, anchor []byte, inUse *provenance.TrustRoot, cases []tufCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, errOurs := inUse.Update(provenance.Update{Roots: c.roots, Targets: c.targets, TrustedRoot: c.trusted}, c.at)
			rootV, targetsV, errRef := differential.FollowTUF(anchor, c.roots, c.targets, c.trusted, c.at, int64(inUse.Publication.TargetsVersion))
			if (errOurs == nil) != c.accept || (errRef == nil) != c.accept {
				t.Fatalf("want accept=%v; pkg/provenance: %v; go-tuf: %v", c.accept, errOurs, errRef)
			}
			if c.accept && (int64(got.Publication.RootVersion) != rootV || int64(got.Publication.TargetsVersion) != targetsV) {
				t.Fatalf("pkg/provenance %+v, go-tuf root %d targets %d", got.Publication, rootV, targetsV)
			}
		})
	}
}

// What Sigstore published between August 2025 and November 2026: a keyholder
// replaced, the root signed again three times, and Rekor v2 in the trust root.
func TestSigstoreHistory(t *testing.T) {
	root := func(v string) []byte { return read(t, "testdata/tuf/"+v+".root.json") }
	anchor := root("12")
	inUse, err := provenance.FromTUF(anchor, read(t, "testdata/tuf/12.targets.json"), read(t, "testdata/tuf/trusted_root.12.json"))
	if err != nil {
		t.Fatal(err)
	}
	targets, trusted := read(t, "sigstore_tuf_targets.json"), read(t, "sigstore_trusted_root.json")
	all := func() [][]byte { return [][]byte{root("13"), root("14"), root("15"), root("16")} }
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tamper := func(doc []byte, old, new string) []byte {
		if !bytes.Contains(doc, []byte(old)) {
			t.Fatalf("nothing to change: %q", old)
		}
		return bytes.Replace(doc, []byte(old), []byte(new), 1)
	}
	swapped := all()
	swapped[1], swapped[2] = swapped[2], swapped[1]
	forged := all()
	forged[3] = tamper(forged[3], `"expires": "2027-04-22T19:27:31Z"`, `"expires": "2037-04-22T19:27:31Z"`)
	compareTUF(t, anchor, inUse, []tufCase{
		{"the whole history", all(), targets, trusted, now, true},
		{"a root is missing", all()[1:], targets, trusted, now, false},
		{"the roots are out of order", swapped, targets, trusted, now, false},
		{"no roots, and the one in use expired", nil, targets, trusted, now, false},
		{"a root with its expiry changed", forged, targets, trusted, now, false},
		{"targets with a version changed", all(), tamper(targets, `"version": 14`, `"version": 99`), trusted, now, false},
		{"another trust root than the one named", all(), targets, read(t, "testdata/tuf/trusted_root.12.json"), now, false},
		{"the trust root with a byte added", all(), targets, append(bytes.Clone(trusted), '\n'), now, false},
		{"after the last root expired", all(), targets, trusted, time.Date(2027, 4, 23, 0, 0, 0, 0, time.UTC), false},
		{"after the targets expired", all(), targets, trusted, time.Date(2036, 5, 10, 0, 0, 0, 0, time.UTC), false},
		{"the day before the last root expires", all(), targets, trusted, time.Date(2027, 4, 21, 0, 0, 0, 0, time.UTC), true},
	})
}

// Metadata from the test authority: how many keyholders signed, and what a
// next root may say.
func TestTUFSignatures(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	anchor := a.TUFRoot()
	inUse := a.TrustRoot()
	_, trusted, err := a.Publish()
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	targets := func(change func(doc map[string]any), signers ...int) []byte {
		t.Helper()
		doc := a.TargetsDoc(trusted)
		if change != nil {
			change(doc)
		}
		out, err := a.SignTUF(doc, signers...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	nextRoot := func(from *provenancetest.Authority, change func(doc map[string]any), signers ...int) []byte {
		t.Helper()
		doc := from.RootDoc()
		doc["version"] = 2
		if change != nil {
			change(doc)
		}
		out, err := from.SignTUF(doc, signers...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	strangerTargets, err := stranger.SignTUF(stranger.TargetsDoc(trusted))
	if err != nil {
		t.Fatal(err)
	}
	good := targets(nil)
	cases := []tufCase{
		{"three keyholders", nil, good, trusted, now, true},
		{"all five", nil, targets(nil, 0, 1, 2, 3, 4), trusted, now, true},
		{"another three", nil, targets(nil, 4, 2, 3), trusted, now, true},
		{"two keyholders", nil, targets(nil, 0, 1), trusted, now, false},
		{"one keyholder three times", nil, targets(nil, 0, 0, 0), trusted, now, false},
		{"nobody", nil, targets(nil, []int{}...), trusted, now, false},
		{"other keyholders", nil, strangerTargets, trusted, now, false},
		{"signed, then changed", nil, bytes.Replace(good, []byte(`"another.file"`), []byte(`"another.fil3"`), 1), trusted, now, false},
		{"metadata that expired", nil, targets(func(doc map[string]any) { doc["expires"] = now.Add(-time.Minute).UTC().Format(time.RFC3339) }), trusted, now, false},
		{"metadata of another kind", nil, targets(func(doc map[string]any) { doc["_type"] = "snapshot" }), trusted, now, false},
		{"targets that do not name the trust root", nil, targets(func(doc map[string]any) { delete(doc["targets"].(map[string]any), "trusted_root.json") }), trusted, now, false},
		{"a trust root of another length", nil, targets(func(doc map[string]any) {
			doc["targets"].(map[string]any)["trusted_root.json"].(map[string]any)["length"] = 3
		}), trusted, now, false},
		{"fields nobody reads, with quotes and line breaks", nil, targets(func(doc map[string]any) {
			doc["x-custom"] = map[string]any{"true": true, "list": []any{1, "two", []any{}}, "quote \" and \\": "line\nbreak"}
		}), trusted, now, true},
		{"a root signed again, as is", [][]byte{nextRoot(a, nil)}, good, trusted, now, true},
		{"a next root that the keyholders in use did not sign", [][]byte{nextRoot(stranger, nil, 0, 1, 2, 3, 4)}, good, trusted, now, false},
		{"a next root that its own keyholders did not sign", [][]byte{nextRoot(a, func(doc map[string]any) {
			doc["keys"], doc["roles"] = stranger.RootDoc()["keys"], stranger.RootDoc()["roles"]
		})}, good, trusted, now, false},
		{"a next root without a targets role", [][]byte{nextRoot(a, func(doc map[string]any) { delete(doc["roles"].(map[string]any), "targets") })}, good, trusted, now, false},
	}
	compareTUF(t, anchor, inUse, cases)

	// A keyholder is replaced: the next root is signed by the five it names,
	// four of whom the root in use knows.
	rotated, err := a.Rotate(4)
	if err != nil {
		t.Fatal(err)
	}
	after, trustedAfter, err := a.Publish()
	if err != nil {
		t.Fatal(err)
	}
	compareTUF(t, anchor, inUse, []tufCase{
		{"after a keyholder was replaced", [][]byte{rotated}, after, trustedAfter, now, true},
		{"the new keyholders' targets without the root that names them", nil, func() []byte {
			out, err := a.SignTUF(a.TargetsDoc(trustedAfter), 2, 3, 4)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}(), trustedAfter, now, false},
	})
}
