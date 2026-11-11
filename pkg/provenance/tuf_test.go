package provenance_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"
)

// What is compiled into the package is what Sigstore's keyholders signed: the
// trust root is the one the targets metadata names, and the keys of the TUF
// root signed that metadata. It is never older than the fixtures of the
// history below.
func TestCompiledInTrustRoot(t *testing.T) {
	tr, err := provenance.PublicGood()
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := os.ReadFile("sigstore_trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(trusted)
	if p := tr.Publication; p == nil || p.SHA256 != hex.EncodeToString(sum[:]) || p.RootVersion < 16 || p.TargetsVersion < 14 {
		t.Fatalf("publication %+v", tr.Publication)
	}
	if len(tr.Authorities) < 2 || len(tr.Logs) < 2 || len(tr.Timestampers) < 1 {
		t.Fatalf("%d authorities, %d logs, %d timestamp authorities", len(tr.Authorities), len(tr.Logs), len(tr.Timestampers))
	}
}

// What Sigstore really published. In August 2025 its TUF root was version 12
// and the trust root named one log. Since then a keyholder was replaced (root
// 13), the root was signed again three times, and the trust root gained the
// first instance of Rekor v2 (targets 13 and 14). An image built then must be
// able to follow all of that, and nothing else.
func TestSigstoreHistory(t *testing.T) {
	root := func(v string) []byte { return read(t, "tuf/"+v+".root.json") }
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	old, err := provenance.FromTUF(root("12"), read(t, "tuf/12.targets.json"), read(t, "tuf/trusted_root.12.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p := old.Publication; p.RootVersion != 12 || p.TargetsVersion != 12 || !strings.HasPrefix(p.SHA256, "6a0c056d") || len(old.Logs) != 1 {
		t.Fatalf("the trust root of 2025: %+v, %d logs", old.Publication, len(old.Logs))
	}
	// It knows nothing of Rekor v2, so the canary recorded there is refused.
	canary, canaryPCRs := read(t, "rekor2-canary.bundle.json"), read(t, "rekor2-canary-pcrs.json")
	if _, err := provenance.Verify(rekor2Canary, old, canary, canaryPCRs); !errors.Is(err, provenance.ErrUntrusted) {
		t.Fatalf("with the trust root of 2025: %v", err)
	}

	targets, trusted := read(t, "tuf/14.targets.json"), read(t, "tuf/trusted_root.14.json")
	update := provenance.Update{Roots: [][]byte{root("13"), root("14"), root("15"), root("16")}, Targets: targets, TrustedRoot: trusted}
	updated, err := old.Update(update, now)
	if err != nil {
		t.Fatal(err)
	}
	if p := updated.Publication; p.RootVersion != 16 || p.TargetsVersion != 14 || !strings.HasPrefix(p.SHA256, "6494e21e") || len(updated.Logs) != 2 {
		t.Fatalf("updated to %+v with %d logs", p, len(updated.Logs))
	}
	if _, err := provenance.Verify(rekor2Canary, updated, canary, canaryPCRs); err != nil {
		t.Fatalf("after the update: %v", err)
	}
	if _, err := provenance.Verify(production, updated, read(t, "v0.5.0-provenance.bundle.json"), read(t, "v0.5.0-pcrs.json")); err != nil {
		t.Fatalf("a release in the first log, after the update: %v", err)
	}
	// Sending the same again changes nothing, and is not an error.
	if again, err := updated.Update(provenance.Update{Targets: targets, TrustedRoot: trusted}, now); err != nil || *again.Publication != *updated.Publication {
		t.Fatalf("the same update again: %+v, %v", again, err)
	}

	with := func(change func(u *provenance.Update)) provenance.Update {
		u := provenance.Update{Roots: [][]byte{root("13"), root("14"), root("15"), root("16")}, Targets: targets, TrustedRoot: trusted}
		change(&u)
		return u
	}
	tamper := func(doc []byte, old, new string) []byte {
		if !bytes.Contains(doc, []byte(old)) {
			t.Fatalf("nothing to change: %q", old)
		}
		return bytes.Replace(doc, []byte(old), []byte(new), 1)
	}
	for name, c := range map[string]struct {
		from   *provenance.TrustRoot
		update provenance.Update
		at     time.Time
		want   error
		reason string
	}{
		"a root is missing from the chain":     {old, with(func(u *provenance.Update) { u.Roots = u.Roots[1:] }), now, provenance.ErrUntrusted, "does not follow"},
		"the roots are out of order":           {old, with(func(u *provenance.Update) { u.Roots[1], u.Roots[2] = u.Roots[2], u.Roots[1] }), now, provenance.ErrUntrusted, "does not follow"},
		"no roots, and the one in use expired": {old, with(func(u *provenance.Update) { u.Roots = nil }), now, provenance.ErrUntrusted, "expired"},
		"a root with its expiry changed": {old, with(func(u *provenance.Update) {
			u.Roots[3] = tamper(u.Roots[3], `"expires": "2027-04-22T19:27:31Z"`, `"expires": "2037-04-22T19:27:31Z"`)
		}), now, provenance.ErrUntrusted, "signatures"},
		"a root that is not JSON":               {old, with(func(u *provenance.Update) { u.Roots[0] = []byte("nope") }), now, provenance.ErrMalformed, "TUF metadata"},
		"targets metadata as a root":            {old, with(func(u *provenance.Update) { u.Roots[0] = targets }), now, provenance.ErrMalformed, "not TUF root metadata"},
		"a root as the targets metadata":        {old, with(func(u *provenance.Update) { u.Targets = root("16") }), now, provenance.ErrMalformed, "not TUF targets metadata"},
		"targets with a version changed":        {old, with(func(u *provenance.Update) { u.Targets = tamper(u.Targets, `"version": 14`, `"version": 99`) }), now, provenance.ErrUntrusted, "signatures"},
		"another trust root than the one named": {old, with(func(u *provenance.Update) { u.TrustedRoot = read(t, "tuf/trusted_root.12.json") }), now, provenance.ErrUntrusted, "is not the trusted_root.json"},
		"the trust root with a byte added":      {old, with(func(u *provenance.Update) { u.TrustedRoot = append(bytes.Clone(trusted), '\n') }), now, provenance.ErrUntrusted, "is not the trusted_root.json"},
		"after the last root expired":           {old, update, time.Date(2027, 4, 23, 0, 0, 0, 0, time.UTC), provenance.ErrUntrusted, "expired"},
		"after the targets expired":             {old, update, time.Date(2036, 5, 10, 0, 0, 0, 0, time.UTC), provenance.ErrUntrusted, "expired"},
		"no time to check against":              {old, update, time.Time{}, provenance.ErrMalformed, "no time"},
		"back to the trust root of 2025":        {updated, provenance.Update{Targets: read(t, "tuf/12.targets.json"), TrustedRoot: read(t, "tuf/trusted_root.12.json")}, now, provenance.ErrUntrusted, "not newer"},
		"too many roots": {old, with(func(u *provenance.Update) {
			for range 70 {
				u.Roots = append(u.Roots, u.Roots[0])
			}
		}), now, provenance.ErrMalformed, "too large"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.from.Update(c.update, c.at)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("got %v, want %v with %q", err, c.want, c.reason)
			}
		})
	}

	// A trust root that did not come from TUF metadata cannot be updated.
	plain, err := provenance.ParseTrustRoot(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Update(update, now); !errors.Is(err, provenance.ErrMalformed) {
		t.Fatalf("got %v", err)
	}
	if _, err := (*provenance.TrustRoot)(nil).Update(update, now); !errors.Is(err, provenance.ErrMalformed) {
		t.Fatalf("got %v", err)
	}
}

// Sigstore replaces the instance of Rekor v2: releases recorded in the new one
// are refused until the trust root that names it arrives, with the signatures
// of the keyholders.
func TestUpdateFollowsANewLog(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	inUse := a.TrustRoot()
	before, err := a.Attest(rekor2Build(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.NextLog(); err != nil {
		t.Fatal(err)
	}
	after, err := a.Attest(rekor2Build(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.Verify(production, inUse, after, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "another log") {
		t.Fatalf("before the update: %v", err)
	}
	// A keyholder is replaced too, so the update carries a root.
	root2, err := a.Rotate(4)
	if err != nil {
		t.Fatal(err)
	}
	targets, trusted, err := a.Publish()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := inUse.Update(provenance.Update{Roots: [][]byte{root2}, Targets: targets, TrustedRoot: trusted}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p := updated.Publication; p.RootVersion != 2 || p.TargetsVersion != inUse.Publication.TargetsVersion+1 || p.SHA256 == inUse.Publication.SHA256 || len(updated.Logs) != 3 {
		t.Fatalf("updated to %+v with %d logs", p, len(updated.Logs))
	}
	for name, bundle := range map[string][]byte{"recorded in the new log": after, "recorded in the old log before it stopped": before} {
		if _, err := provenance.Verify(production, updated, bundle, pcrsA); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The old log stopped taking entries: one that claims a later time is refused.
	if len(updated.Logs) == 3 && updated.Logs[1].NotAfter.IsZero() {
		t.Errorf("the old log has no end: %+v", updated.Logs[1])
	}
	// The trust root in use is not changed by making the next one.
	if len(inUse.Logs) != 2 || inUse.Publication.RootVersion != 1 {
		t.Fatalf("the trust root in use changed: %+v", inUse.Publication)
	}
}

// The update moves logs and timestamp authorities. It does not say who issues
// signing certificates: that stays as the image has it.
func TestUpdateKeepsTheAuthorities(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	inUse := a.TrustRoot()
	_, trusted, err := a.Publish()
	if err != nil {
		t.Fatal(err)
	}
	rogue, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	rogueJSON, err := rogue.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	var rogueDoc map[string]any
	if err := json.Unmarshal(rogueJSON, &rogueDoc); err != nil {
		t.Fatal(err)
	}
	publish := func(change func(doc map[string]any)) *provenance.TrustRoot {
		t.Helper()
		edited := edit(t, trusted, change)
		targets, err := a.SignTUF(a.TargetsDoc(edited))
		if err != nil {
			t.Fatal(err)
		}
		tr, err := inUse.Update(provenance.Update{Targets: targets, TrustedRoot: edited}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}

	// The keyholders name another issuer of certificates, with its own log and
	// timestamp authority: a release it "signs" is still refused.
	added := publish(func(doc map[string]any) {
		doc["certificateAuthorities"] = append(doc["certificateAuthorities"].([]any), rogueDoc["certificateAuthorities"].([]any)...)
		doc["tlogs"] = append(doc["tlogs"].([]any), rogueDoc["tlogs"].([]any)...)
		doc["timestampAuthorities"] = append(doc["timestampAuthorities"].([]any), rogueDoc["timestampAuthorities"].([]any)...)
	})
	if len(added.Authorities) != 1 || len(added.Logs) != 4 || len(added.Timestampers) != 2 {
		t.Fatalf("%d authorities, %d logs, %d timestamp authorities", len(added.Authorities), len(added.Logs), len(added.Timestampers))
	}
	for _, b := range []provenancetest.Build{testBuild(), rekor2Build()} {
		forged, err := rogue.Attest(b, pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, added, forged, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "signing certificate") {
			t.Fatalf("a release signed by the added issuer: %v", err)
		}
	}

	// Replacing the issuer changes nothing either: the one of the image stays.
	replaced := publish(func(doc map[string]any) { doc["certificateAuthorities"] = rogueDoc["certificateAuthorities"] })
	good, err := a.Attest(testBuild(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.Verify(production, replaced, good, pcrsA); err != nil {
		t.Fatalf("after the issuer was replaced in the update: %v", err)
	}

	// Ending the period of the image's issuer does take effect, and only
	// earlier: it takes trust away.
	ended := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	end := func(at time.Time) func(doc map[string]any) {
		return func(doc map[string]any) {
			ca := doc["certificateAuthorities"].([]any)[0].(map[string]any)
			ca["validFor"].(map[string]any)["end"] = at.Format(time.RFC3339)
		}
	}
	retired := publish(end(ended))
	if !retired.Authorities[0].NotAfter.Equal(ended) {
		t.Fatalf("the issuer ends at %s, want %s", retired.Authorities[0].NotAfter, ended)
	}
	if _, err := provenance.Verify(production, retired, good, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
		t.Fatalf("a release signed after the issuer was retired: %v", err)
	}
	inUse = retired
	if later := publish(end(ended.Add(48 * time.Hour))); !later.Authorities[0].NotAfter.Equal(ended) {
		t.Fatalf("a later end was taken: %s", later.Authorities[0].NotAfter)
	}
}

// What the keyholders' signatures must be for metadata to count.
func TestTUFSignatures(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	inUse := a.TrustRoot()
	_, trusted, err := a.Publish()
	if err != nil {
		t.Fatal(err)
	}
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
	none := func(map[string]any) {}
	nextRoot := func(change func(doc map[string]any), signers ...int) []byte {
		t.Helper()
		doc := a.RootDoc()
		doc["version"] = 2
		change(doc)
		out, err := a.SignTUF(doc, signers...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	good := targets(nil)

	accepted := map[string]provenance.Update{
		"three keyholders": {Targets: good},
		"all five":         {Targets: targets(nil, 0, 1, 2, 3, 4)},
		"another three":    {Targets: targets(nil, 4, 2, 3)},
		"with fields this package does not read": {Targets: targets(func(doc map[string]any) {
			doc["x-custom"] = map[string]any{"null": nil, "true": true, "list": []any{1, "two", []any{}}, "quote \" and \\": "line\nbreak"}
		})},
		"a root signed again, as is": {Roots: [][]byte{nextRoot(none)}, Targets: targets(nil)},
	}
	for name, u := range accepted {
		u.TrustedRoot = trusted
		if _, err := inUse.Update(u, time.Now()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	stranger, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	strangerTargets, err := stranger.SignTUF(stranger.TargetsDoc(trusted))
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]struct {
		update provenance.Update
		want   error
		reason string
	}{
		"two keyholders":                          {provenance.Update{Targets: targets(nil, 0, 1)}, provenance.ErrUntrusted, "2 of the 3 signatures"},
		"one keyholder three times":               {provenance.Update{Targets: targets(nil, 0, 0, 0)}, provenance.ErrUntrusted, "1 of the 3 signatures"},
		"nobody":                                  {provenance.Update{Targets: targets(nil, []int{}...)}, provenance.ErrUntrusted, "signatures"},
		"other keyholders":                        {provenance.Update{Targets: strangerTargets}, provenance.ErrUntrusted, "0 of the 3 signatures"},
		"signed, then changed":                    {provenance.Update{Targets: bytes.Replace(good, []byte(`"another.file"`), []byte(`"another.fil3"`), 1)}, provenance.ErrUntrusted, "0 of the 3 signatures"},
		"metadata that expired":                   {provenance.Update{Targets: targets(func(doc map[string]any) { doc["expires"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) })}, provenance.ErrUntrusted, "expired"},
		"metadata of another kind":                {provenance.Update{Targets: targets(func(doc map[string]any) { doc["_type"] = "snapshot" })}, provenance.ErrMalformed, "not TUF targets metadata"},
		"metadata of a version of TUF to come":    {provenance.Update{Targets: targets(func(doc map[string]any) { doc["spec_version"] = "2.0" })}, provenance.ErrMalformed, "not TUF targets metadata"},
		"a number that is not an integer":         {provenance.Update{Targets: targets(func(doc map[string]any) { doc["x"] = 1.5 })}, provenance.ErrMalformed, "not an integer"},
		"targets that do not name the trust root": {provenance.Update{Targets: targets(func(doc map[string]any) { delete(doc["targets"].(map[string]any), "trusted_root.json") })}, provenance.ErrUntrusted, "is not the trusted_root.json"},
		"a trust root of another length": {provenance.Update{Targets: targets(func(doc map[string]any) {
			doc["targets"].(map[string]any)["trusted_root.json"].(map[string]any)["length"] = 3
		})}, provenance.ErrUntrusted, "is not the trusted_root.json"},
		"a next root that the keyholders in use did not sign": {provenance.Update{Roots: [][]byte{func() []byte {
			doc := stranger.RootDoc()
			doc["version"] = 2
			out, err := stranger.SignTUF(doc, 0, 1, 2, 3, 4)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}()}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 1"},
		"a next root that its own keyholders did not sign": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			doc["keys"], doc["roles"] = stranger.RootDoc()["keys"], stranger.RootDoc()["roles"]
		})}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 2"},
		"a next root that asks for no signature": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			doc["roles"].(map[string]any)["root"].(map[string]any)["threshold"] = 0
		})}, Targets: good}, provenance.ErrUntrusted, "names no root role"},
		"a next root without a targets role": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			delete(doc["roles"].(map[string]any), "targets")
		})}, Targets: good}, provenance.ErrUntrusted, "names no targets role"},
		"a next root whose keys are of another kind": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			for _, k := range doc["keys"].(map[string]any) {
				k.(map[string]any)["scheme"] = "ed25519"
			}
		})}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 2"},
		"a next root whose keys are not keys": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			for _, k := range doc["keys"].(map[string]any) {
				k.(map[string]any)["keyval"] = map[string]any{"public": "nope"}
			}
		})}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 2"},
		"a next root whose keys are not keys inside": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			for _, k := range doc["keys"].(map[string]any) {
				k.(map[string]any)["keyval"] = map[string]any{"public": "-----BEGIN PUBLIC KEY-----\nbm9wZQ==\n-----END PUBLIC KEY-----\n"}
			}
		})}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 2"},
		"a next root whose keys are on another curve": {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) {
			der, err := x509.MarshalPKIXPublicKey(a.TSALeaf.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range doc["keys"].(map[string]any) {
				k.(map[string]any)["keyval"] = map[string]any{"public": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
			}
		})}, Targets: good}, provenance.ErrUntrusted, "role of TUF root 2"},
		"a version that is not a number":                 {provenance.Update{Targets: targets(func(doc map[string]any) { doc["version"] = "fourteen" })}, provenance.ErrMalformed, "TUF metadata"},
		"a number that is not an integer, inside a list": {provenance.Update{Targets: targets(func(doc map[string]any) { doc["x"] = map[string]any{"y": []any{1, 1.5}} })}, provenance.ErrMalformed, "not an integer"},
		"metadata nested without end": {provenance.Update{Targets: targets(func(doc map[string]any) {
			var deep any = "x"
			for range 40 {
				deep = []any{deep}
			}
			doc["x"] = deep
		})}, provenance.ErrMalformed, "nested too deep"},
		"nothing signed":                         {provenance.Update{Targets: []byte(`{"signatures":[]}`)}, provenance.ErrMalformed, "TUF metadata"},
		"a next root of version 0":               {provenance.Update{Roots: [][]byte{nextRoot(func(doc map[string]any) { doc["version"] = 0 })}, Targets: good}, provenance.ErrMalformed, "not TUF root metadata"},
		"metadata too large":                     {provenance.Update{Targets: append(bytes.Clone(good), bytes.Repeat([]byte(" "), 300<<10)...)}, provenance.ErrMalformed, "too large"},
		"metadata whose signed part was emptied": {provenance.Update{Targets: bytes.Replace(good, []byte(`"signed":{`), []byte(`"signed":{} ,"signed2":{`), 1)}, provenance.ErrMalformed, "not TUF targets metadata"},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			c.update.TrustedRoot = trusted
			_, err := inUse.Update(c.update, time.Now())
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("got %v, want %v with %q", err, c.want, c.reason)
			}
		})
	}

	// The anchor itself must be root metadata, and a trust root the anchor's
	// keyholders did not sign for is not one.
	if _, err := provenance.FromTUF(good, good, trusted); !errors.Is(err, provenance.ErrMalformed) {
		t.Errorf("targets metadata as the anchor: %v", err)
	}
	if _, err := provenance.FromTUF(stranger.TUFRoot(), good, trusted); !errors.Is(err, provenance.ErrUntrusted) {
		t.Errorf("another anchor: %v", err)
	}
	if _, err := provenance.FromTUF(a.TUFRoot(), good, []byte(`{}`)); !errors.Is(err, provenance.ErrUntrusted) {
		t.Errorf("another trust root: %v", err)
	}
	// The keyholders signed for a file that is not a trust root.
	empty, err := a.SignTUF(a.TargetsDoc([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.FromTUF(a.TUFRoot(), empty, []byte(`{}`)); !errors.Is(err, provenance.ErrMalformed) {
		t.Errorf("a signed file that is not a trust root: %v", err)
	}
}

// The fuzzer changes an update. It has no keyholder's key, so the only trust
// roots it can get accepted are the ones the keyholders signed for.
func FuzzTrustUpdate(f *testing.F) {
	a, err := provenancetest.New()
	if err != nil {
		f.Fatal(err)
	}
	inUse := a.TrustRoot()
	sameTargets, same, err := a.Publish()
	if err != nil {
		f.Fatal(err)
	}
	if err := a.NextLog(); err != nil {
		f.Fatal(err)
	}
	root, err := a.Rotate(2)
	if err != nil {
		f.Fatal(err)
	}
	targets, trusted, err := a.Publish()
	if err != nil {
		f.Fatal(err)
	}
	signed := bytes.Clone(trusted)
	f.Add(root, targets, trusted)
	f.Add([]byte{}, sameTargets, same)
	f.Add(read(f, "tuf/16.root.json"), read(f, "tuf/14.targets.json"), read(f, "tuf/trusted_root.14.json"))
	f.Add([]byte(`{}`), []byte(`{"signatures":[],"signed":{}}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, root, targets, trusted []byte) {
		u := provenance.Update{Targets: targets, TrustedRoot: trusted}
		if len(root) > 0 {
			u.Roots = [][]byte{root}
		}
		next, err := inUse.Update(u, time.Now())
		if err != nil {
			return
		}
		if !bytes.Equal(trusted, same) && !bytes.Equal(trusted, signed) {
			t.Fatalf("accepted %+v for a trust root nobody signed for", next.Publication)
		}
	})
}
