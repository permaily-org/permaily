package provenance_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"
)

func rekor2Build() provenancetest.Build {
	b := testBuild()
	b.Rekor2 = true
	return b
}

func stamps(doc map[string]any) []any {
	return doc["verificationMaterial"].(map[string]any)["timestampVerificationData"].(map[string]any)["rfc3161Timestamps"].([]any)
}

func setStamps(doc map[string]any, der ...[]byte) {
	list := []any{}
	for _, d := range der {
		list = append(list, map[string]any{"signedTimestamp": base64.StdEncoding.EncodeToString(d)})
	}
	doc["verificationMaterial"].(map[string]any)["timestampVerificationData"] = map[string]any{"rfc3161Timestamps": list}
}

func signature(t testing.TB, bundle []byte) []byte {
	t.Helper()
	var sig []byte
	edit(t, bundle, func(doc map[string]any) {
		s := doc["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)["sig"].(string)
		sig, _ = base64.StdEncoding.DecodeString(s)
	})
	return sig
}

// A release recorded in a Rekor v2 log: the entry is rebuilt from the
// signature, the log signs its checkpoint with Ed25519, and the time comes
// from the timestamp authority.
func TestVerifyRekor2(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := a.TrustRoot()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	b := rekor2Build()
	b.At = at
	bundle, err := a.Attest(b, pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := provenance.Verify(production, tr, bundle, pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if rel.PCR0 != strings.Repeat("aa", 48) || rel.Commit != b.Commit || rel.Ref != b.Ref || !rel.LoggedAt.Equal(at) {
		t.Fatalf("release %+v, want the time of the timestamp %s", rel, at)
	}

	// The same statement in trees of other shapes and positions.
	for _, shape := range [][2]int{{0, 0}, {0, 5}, {7, 0}, {3, 9}} {
		a.Before, a.After = shape[0], shape[1]
		other, err := a.Attest(b, pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, tr, other, pcrsA); err != nil {
			t.Fatalf("tree with %d entries before and %d after: %v", shape[0], shape[1], err)
		}
	}
	a.Before, a.After = 4, 2

	second, err := a.Attest(b, pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	strangerStamp, err := stranger.Timestamp(signature(t, bundle), at)
	if err != nil {
		t.Fatal(err)
	}
	later, err := a.Timestamp(signature(t, bundle), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var own []byte
	edit(t, bundle, func(doc map[string]any) {
		own, _ = base64.StdEncoding.DecodeString(stamps(doc)[0].(map[string]any)["signedTimestamp"].(string))
	})

	t.Run("timestamps that do not count are ignored", func(t *testing.T) {
		// One from an authority the trust root does not name, and a second one
		// from the same authority: the first that verifies is the time.
		for name, list := range map[string][][]byte{"a stranger's first": {strangerStamp, own}, "two of one authority": {own, later}, "garbage first": {[]byte("nope"), own}} {
			got, err := provenance.Verify(production, tr, edit(t, bundle, func(doc map[string]any) { setStamps(doc, list...) }), pcrsA)
			if err != nil || !got.LoggedAt.Equal(at) {
				t.Errorf("%s: %+v, %v", name, got, err)
			}
		}
	})

	refused := []struct {
		name   string
		change func(doc map[string]any)
		want   error
		reason string
	}{
		{"no timestamp", func(doc map[string]any) { setStamps(doc) }, provenance.ErrUntrusted, "carries none"},
		{"no timestamp data at all", func(doc map[string]any) {
			delete(doc["verificationMaterial"].(map[string]any), "timestampVerificationData")
		}, provenance.ErrUntrusted, "carries none"},
		{"only a stranger's timestamp", func(doc map[string]any) { setStamps(doc, strangerStamp) }, provenance.ErrUntrusted, "signed by another certificate"},
		{"a timestamp for another signature", func(doc map[string]any) {
			var theirs []byte
			edit(t, second, func(d map[string]any) {
				theirs, _ = base64.StdEncoding.DecodeString(stamps(d)[0].(map[string]any)["signedTimestamp"].(string))
			})
			setStamps(doc, theirs)
		}, provenance.ErrUntrusted, "another message"},
		{"a timestamp with a bit changed", func(doc map[string]any) {
			s := stamps(doc)[0].(map[string]any)
			s["signedTimestamp"] = flip(s["signedTimestamp"])
		}, provenance.ErrUntrusted, "timestamp"},
		{"too many timestamps", func(doc map[string]any) {
			setStamps(doc, own, own, own, own, own, own, own, own, own)
		}, provenance.ErrMalformed, "too many"},
		{"another statement", func(doc map[string]any) {
			env := doc["dsseEnvelope"].(map[string]any)
			env["payload"] = base64.StdEncoding.EncodeToString([]byte(`{"_type":"https://in-toto.io/Statement/v1"}`))
		}, provenance.ErrUntrusted, "not in the tree"},
		{"another certificate", func(doc map[string]any) {
			edit(t, second, func(d map[string]any) {
				doc["verificationMaterial"].(map[string]any)["certificate"] = d["verificationMaterial"].(map[string]any)["certificate"]
			})
		}, provenance.ErrUntrusted, "not in the tree"},
		{"another position in the tree", func(doc map[string]any) { entry(doc)["logIndex"] = "3" }, provenance.ErrUntrusted, "not in the tree"},
		{"a position outside the tree", func(doc map[string]any) { entry(doc)["logIndex"] = "70" }, provenance.ErrUntrusted, "bad index"},
		{"a position that is not a number", func(doc map[string]any) { entry(doc)["logIndex"] = "-1" }, provenance.ErrMalformed, "bad index"},
		{"a proof with a hash changed", func(doc map[string]any) {
			h := proof(doc)["hashes"].([]any)
			h[0] = flip(h[0])
		}, provenance.ErrUntrusted, "not in the tree"},
		{"a checkpoint with another root", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			lines := strings.Split(cp["envelope"].(string), "\n")
			lines[2] = flip(lines[2])
			cp["envelope"] = strings.Join(lines, "\n")
		}, provenance.ErrUntrusted, "signature does not verify"},
		{"a checkpoint with another size", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.Replace(cp["envelope"].(string), "\n7\n", "\n8\n", 1)
		}, provenance.ErrUntrusted, "signature does not verify"},
		{"a checkpoint with a size that is not canonical", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.Replace(cp["envelope"].(string), "\n7\n", "\n07\n", 1)
		}, provenance.ErrUntrusted, "bad size or root"},
		{"a checkpoint of another log", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.ReplaceAll(cp["envelope"].(string), a.Origin2, "log2098-1.rekor.test")
		}, provenance.ErrUntrusted, "not for this log"},
		{"a checkpoint shaped like the first Rekor's", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			cp["envelope"] = strings.Replace(cp["envelope"].(string), a.Origin2+"\n", a.Origin2+" - 1\n", 1)
		}, provenance.ErrUntrusted, "not for this log"},
		{"a checkpoint signed with another key id", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			text, sig, _ := strings.Cut(cp["envelope"].(string), "\n\n— "+a.Origin2+" ")
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
			raw[0] ^= 1
			cp["envelope"] = text + "\n\n— " + a.Origin2 + " " + base64.StdEncoding.EncodeToString(raw) + "\n"
		}, provenance.ErrUntrusted, "signature does not verify"},
		{"a checkpoint without signatures", func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			text, _, _ := strings.Cut(cp["envelope"].(string), "\n\n")
			cp["envelope"] = text + "\n\n"
		}, provenance.ErrUntrusted, "signature does not verify"},
		{"no checkpoint", func(doc map[string]any) { delete(proof(doc), "checkpoint") }, provenance.ErrUntrusted, "not for this log"},
		{"the entry named as the first Rekor's log", func(doc map[string]any) {
			entry(doc)["logId"] = map[string]any{"keyId": base64.StdEncoding.EncodeToString(a.LogID)}
		}, provenance.ErrUntrusted, "not for this log"},
		{"an unknown log", func(doc map[string]any) {
			entry(doc)["logId"] = map[string]any{"keyId": base64.StdEncoding.EncodeToString([]byte("another log, 32 bytes long......"))}
		}, provenance.ErrUntrusted, "another log"},
		{"no log id", func(doc map[string]any) { delete(entry(doc), "logId") }, provenance.ErrUntrusted, "another log"},
		{"a kind that does not match the entry", func(doc map[string]any) {
			entry(doc)["kindVersion"] = map[string]any{"kind": "dsse", "version": "0.0.1"}
		}, provenance.ErrUntrusted, "does not read"},
		{"an entry of a kind nobody reads", func(doc map[string]any) {
			entry(doc)["canonicalizedBody"] = base64.StdEncoding.EncodeToString([]byte(`{"apiVersion":"0.0.3","kind":"hashedrekord"}`))
			entry(doc)["kindVersion"] = map[string]any{"kind": "hashedrekord", "version": "0.0.3"}
		}, provenance.ErrUntrusted, "does not read"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			_, err := provenance.Verify(production, tr, edit(t, bundle, c.change), pcrsA)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("got %v, want %v with %q", err, c.want, c.reason)
			}
		})
	}

	t.Run("a certificate that was not valid at the time of the timestamp", func(t *testing.T) {
		for _, issued := range []time.Duration{-time.Hour, time.Hour} {
			b := rekor2Build()
			b.At = at
			b.IssuedAt = at.Add(issued)
			bundle, err := a.Attest(b, pcrsA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "signing certificate") {
				t.Fatalf("issued %s from the timestamp: %v", issued, err)
			}
		}
	})
	t.Run("a log or a timestamp authority outside its period", func(t *testing.T) {
		for name, change := range map[string]func(tr *provenance.TrustRoot){
			"the log stopped before":       func(tr *provenance.TrustRoot) { tr.Logs[1].NotAfter = at.Add(-time.Minute) },
			"the log started after":        func(tr *provenance.TrustRoot) { tr.Logs[1].NotBefore = at.Add(time.Minute) },
			"the authority stopped before": func(tr *provenance.TrustRoot) { tr.Timestampers[0].NotAfter = at.Add(-time.Minute) },
			"the authority started after":  func(tr *provenance.TrustRoot) { tr.Timestampers[0].NotBefore = at.Add(time.Minute) },
			"no authority":                 func(tr *provenance.TrustRoot) { tr.Timestampers = nil },
		} {
			other := a.TrustRoot()
			change(other)
			if _, err := provenance.Verify(production, other, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("the first Rekor's entries still verify, and do not need a timestamp", func(t *testing.T) {
		first, err := a.Attest(testBuild(), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, tr, first, pcrsA); err != nil {
			t.Fatal(err)
		}
		// A first-generation entry cannot borrow the second log's name.
		moved := edit(t, first, func(doc map[string]any) {
			entry(doc)["logId"] = map[string]any{"keyId": base64.StdEncoding.EncodeToString(a.Log2ID)}
		})
		if _, err := provenance.Verify(production, tr, moved, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
}

// Timestamps a verifier must take, and ones it must not, built field by field.
func TestTimestamps(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := a.TrustRoot()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	b := rekor2Build()
	b.At = at
	bundle, err := a.Attest(b, pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	sig := signature(t, bundle)
	with := func(s provenancetest.Stamp) []byte {
		t.Helper()
		if s.Message == nil {
			s.Message = sig
		}
		if s.At.IsZero() {
			s.At = at
		}
		stamp, err := a.Stamp(s)
		if err != nil {
			t.Fatal(err)
		}
		return edit(t, bundle, func(doc map[string]any) { setStamps(doc, stamp) })
	}
	only := func(keep asn1.ObjectIdentifier) func([]provenancetest.Attribute) []provenancetest.Attribute {
		return func(attrs []provenancetest.Attribute) []provenancetest.Attribute {
			return slices.DeleteFunc(attrs, func(a provenancetest.Attribute) bool { return !a.Type.Equal(keep) })
		}
	}
	twice := func(which asn1.ObjectIdentifier) func([]provenancetest.Attribute) []provenancetest.Attribute {
		return func(attrs []provenancetest.Attribute) []provenancetest.Attribute {
			for _, a := range attrs {
				if a.Type.Equal(which) {
					return append(attrs, a)
				}
			}
			return attrs
		}
	}

	accepted := map[string]provenancetest.Stamp{
		"granted with modifications":            {Status: 1},
		"a time with a fraction of a second":    {Fraction: true, At: at.Add(250 * time.Millisecond)},
		"the signer named by its key id":        {ByKeyID: true},
		"SHA-384 of the message":                {Imprint: provenancetest.SHA384},
		"SHA-512 of the message":                {Imprint: provenancetest.SHA512},
		"a signature over SHA-256":              {Digest: provenancetest.SHA256},
		"a signature over SHA-512":              {Digest: provenancetest.SHA512},
		"an attribute this verifier ignores":    {Attributes: func(attrs []provenancetest.Attribute) []provenancetest.Attribute { return attrs }},
		"only the two attributes that are read": {Attributes: func(attrs []provenancetest.Attribute) []provenancetest.Attribute { return attrs[:2] }},
	}
	for name, s := range accepted {
		t.Run(name, func(t *testing.T) {
			want := at
			if !s.At.IsZero() {
				want = s.At
			}
			rel, err := provenance.Verify(production, tr, with(s), pcrsA)
			if err != nil || !rel.LoggedAt.Equal(want) {
				t.Fatalf("got %+v, %v; want the time %s", rel, err, want)
			}
		})
	}

	sha1 := asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	refused := map[string]struct {
		stamp  provenancetest.Stamp
		reason string
	}{
		"the authority refused":                  {provenancetest.Stamp{Status: 2}, "refused the request"},
		"a token that is not signed data":        {provenancetest.Stamp{Outer: provenancetest.TSTInfo}, "no signed data"},
		"signed content that is not a timestamp": {provenancetest.Stamp{Content: provenancetest.SignedData}, "not one signature over a timestamp"},
		"two signatures":                         {provenancetest.Stamp{Signers: 2}, "not one signature over a timestamp"},
		"no signature":                           {provenancetest.Stamp{Signers: -1}, "not one signature over a timestamp"},
		"a signature over SHA-1":                 {provenancetest.Stamp{Digest: sha1}, "digest algorithm"},
		"SHA-1 of the message":                   {provenancetest.Stamp{Imprint: sha1}, "another message"},
		"another message":                        {provenancetest.Stamp{Message: []byte("another signature")}, "another message"},
		"no signed attributes":                   {provenancetest.Stamp{Attributes: func([]provenancetest.Attribute) []provenancetest.Attribute { return nil }}, "no signed attributes"},
		"no content type among the attributes":   {provenancetest.Stamp{Attributes: only(provenancetest.MessageDigest)}, "not over this timestamp"},
		"no message digest among the attributes": {provenancetest.Stamp{Attributes: only(provenancetest.ContentType)}, "not over this timestamp"},
		"two content types":                      {provenancetest.Stamp{Attributes: twice(provenancetest.ContentType)}, "two content types"},
		"two message digests":                    {provenancetest.Stamp{Attributes: twice(provenancetest.MessageDigest)}, "two message digests"},
		"an attribute with two values": {provenancetest.Stamp{Attributes: func(attrs []provenancetest.Attribute) []provenancetest.Attribute {
			attrs[0].Values = append(attrs[0].Values, attrs[0].Values[0])
			return attrs
		}}, "an attribute with 2 values"},
		"a content type that is not one": {provenancetest.Stamp{Attributes: func(attrs []provenancetest.Attribute) []provenancetest.Attribute {
			attrs[0].Values = provenancetest.Value(7)
			return attrs
		}}, "malformed signed attribute"},
		"the digest of other content": {provenancetest.Stamp{Attributes: func(attrs []provenancetest.Attribute) []provenancetest.Attribute {
			attrs[1].Values = provenancetest.Value(make([]byte, 48))
			return attrs
		}}, "not over this timestamp"},
		"a timestamp of version 2":                   {provenancetest.Stamp{Version: 2}, "version 2"},
		"content that is not a timestamp":            {provenancetest.Stamp{Info: []byte{0x04, 0x01, 0x00}}, "malformed timestamp"},
		"a timestamp cut short":                      {provenancetest.Stamp{Info: []byte{0x30, 0x03, 0x02, 0x01, 0x01}}, "malformed timestamp"},
		"signed data that is not it":                 {provenancetest.Stamp{Signed: []byte{0x04, 0x01, 0x00}}, "malformed signed data"},
		"a time that is not one":                     {provenancetest.Stamp{Time: "tomorrow"}, "bad time"},
		"a time in another zone":                     {provenancetest.Stamp{Time: at.Format("20060102150405") + "+0100"}, "bad time"},
		"before the authority's certificate existed": {provenancetest.Stamp{At: a.TSALeaf.NotBefore.Add(-time.Hour)}, "authority"},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := provenance.Verify(production, tr, with(c.stamp), pcrsA)
			if !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("got %v, want %q", err, c.reason)
			}
		})
	}

	t.Run("responses that are not ASN.1", func(t *testing.T) {
		good, err := a.Timestamp(sig, at)
		if err != nil {
			t.Fatal(err)
		}
		for name, stamp := range map[string][]byte{"nothing": {}, "text": []byte("nope"), "with bytes after it": append(bytes.Clone(good), 0)} {
			_, err := provenance.Verify(production, tr, edit(t, bundle, func(doc map[string]any) { setStamps(doc, stamp) }), pcrsA)
			if !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "not a timestamp response") {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("an authority whose certificate is not for timestamping only", func(t *testing.T) {
		other := a.TrustRoot()
		leaf := *other.Timestampers[0].Leaf
		leaf.ExtKeyUsage = append(slices.Clone(leaf.ExtKeyUsage), x509.ExtKeyUsageCodeSigning)
		other.Timestampers[0].Leaf = &leaf
		if _, err := provenance.Verify(production, other, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "timestamping only") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an authority whose certificate does not chain to its root", func(t *testing.T) {
		stranger, err := provenancetest.New()
		if err != nil {
			t.Fatal(err)
		}
		other := a.TrustRoot()
		other.Timestampers[0].Roots = stranger.TrustRoot().Timestampers[0].Roots
		if _, err := provenance.Verify(production, other, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "authority's certificate at") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a time that is not a GeneralizedTime", func(t *testing.T) {
		type imprint struct {
			Alg  pkix.AlgorithmIdentifier
			Hash []byte
		}
		sum := sha256.Sum256(sig)
		info, err := asn1.Marshal(struct {
			Version int
			Policy  asn1.ObjectIdentifier
			Imprint imprint
			Serial  int
			Time    time.Time `asn1:"utc"`
		}{1, asn1.ObjectIdentifier{1, 2, 3}, imprint{pkix.AlgorithmIdentifier{Algorithm: provenancetest.SHA256}, sum[:]}, 7, at})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provenance.Verify(production, tr, with(provenancetest.Stamp{Info: info}), pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "no time") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a signature that is not the authority's", func(t *testing.T) {
		other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		edPub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		// The same certificate by name, with another key in it.
		for name, key := range map[string]any{"another key": &other.PublicKey, "a key of another kind": edPub} {
			forged := a.TrustRoot()
			leaf := *forged.Timestampers[0].Leaf
			leaf.PublicKey = key
			forged.Timestampers[0].Leaf = &leaf
			if _, err := provenance.Verify(production, forged, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "authority's signature does not verify") {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("an authority named by a key id it does not have", func(t *testing.T) {
		other := a.TrustRoot()
		leaf := *other.Timestampers[0].Leaf
		leaf.SubjectKeyId = nil
		other.Timestampers[0].Leaf = &leaf
		if _, err := provenance.Verify(production, other, with(provenancetest.Stamp{ByKeyID: true}), pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "signed by another certificate") {
			t.Fatalf("got %v", err)
		}
	})
}

// A Rekor v2 log may sign its checkpoints with ECDSA instead of Ed25519; the
// key id of its signature lines is then the hash of the key alone.
func TestRekor2LogWithECDSA(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := a.Attest(rekor2Build(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(der)
	resign := func(keyID []byte) []byte {
		return edit(t, bundle, func(doc map[string]any) {
			cp := proof(doc)["checkpoint"].(map[string]any)
			text, _, _ := strings.Cut(cp["envelope"].(string), "\n\n")
			digest := sha256.Sum256([]byte(text + "\n"))
			sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			cp["envelope"] = text + "\n\n— " + a.Origin2 + " " + base64.StdEncoding.EncodeToString(append(bytes.Clone(keyID), sig...)) + "\n"
		})
	}
	tr := a.TrustRoot()
	tr.Logs[1].Key = &key.PublicKey
	if _, err := provenance.Verify(production, tr, resign(id[:4]), pcrsA); err != nil {
		t.Fatal(err)
	}
	// The Ed25519 key id is not this key's, and the Ed25519 signature is not
	// an ECDSA one.
	for name, b := range map[string][]byte{"the other key id": resign(a.Log2ID[:4]), "the Ed25519 signature": bundle} {
		if _, err := provenance.Verify(production, tr, b, pcrsA); !errors.Is(err, provenance.ErrUntrusted) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A trust root made by hand with a kind of key nobody checks.
	tr.Logs[1].Key = "not a key"
	if _, err := provenance.Verify(production, tr, bundle, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "does not sign entries of this kind") {
		t.Fatalf("got %v", err)
	}
	tr = a.TrustRoot()
	tr.Logs[0].Key = "not a key"
	first, err := a.Attest(testBuild(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.Verify(production, tr, first, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "does not sign entries of this kind") {
		t.Fatalf("got %v", err)
	}
}

// Bundles that are not what they should be before any signature is checked.
func TestMalformedBundles(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := a.TrustRoot()
	first, err := a.Attest(testBuild(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for name, c := range map[string]struct {
		change func(doc map[string]any)
		want   error
	}{
		"a certificate that is not one": {func(doc map[string]any) {
			doc["verificationMaterial"].(map[string]any)["certificate"] = map[string]any{"rawBytes": "AAAA"}
		}, provenance.ErrMalformed},
		"an entry that is not JSON": {func(doc map[string]any) { entry(doc)["canonicalizedBody"] = b64("nope") }, provenance.ErrMalformed},
		"an entry whose spec is not an object": {func(doc map[string]any) {
			entry(doc)["canonicalizedBody"] = b64(`{"apiVersion":"0.0.1","kind":"dsse","spec":"nope"}`)
		}, provenance.ErrMalformed},
		"a proof whose position is not a number": {func(doc map[string]any) { proof(doc)["logIndex"] = "x" }, provenance.ErrUntrusted},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := provenance.Verify(production, tr, edit(t, first, c.change), pcrsA); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}

	// A Rekor v2 entry signed with a certificate that holds another curve.
	t.Run("a signing key that is not P-256", func(t *testing.T) {
		second, err := a.Attest(rekor2Build(), pcrsA)
		if err != nil {
			t.Fatal(err)
		}
		swapped := edit(t, second, func(doc map[string]any) {
			doc["verificationMaterial"].(map[string]any)["certificate"] = map[string]any{"rawBytes": base64.StdEncoding.EncodeToString(a.TSALeaf.Raw)}
		})
		if _, err := provenance.Verify(production, tr, swapped, pcrsA); !errors.Is(err, provenance.ErrUntrusted) || !strings.Contains(err.Error(), "P-256") {
			t.Fatalf("got %v", err)
		}
	})
}

// A trust root with what Sigstore may add: an authority with an intermediate
// certificate, and a log with a kind of key this package does not check.
func TestTrustRootWithMore(t *testing.T) {
	a, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := a.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	more := edit(t, doc, func(d map[string]any) {
		d["tlogs"] = append(d["tlogs"].([]any), map[string]any{
			"baseUrl": "https://log2100-1.rekor.test", "hashAlgorithm": "SHA2_256",
			"publicKey": map[string]any{"rawBytes": base64.StdEncoding.EncodeToString(rsaDER)}, "logId": map[string]any{"keyId": "AAAA"},
		})
		chain := d["timestampAuthorities"].([]any)[0].(map[string]any)["certChain"].(map[string]any)
		certs := chain["certificates"].([]any)
		chain["certificates"] = []any{certs[0], map[string]any{"rawBytes": base64.StdEncoding.EncodeToString(a.Intermediate.Raw)}, certs[1]}
	})
	tr, err := provenance.ParseTrustRoot(more)
	if err != nil || len(tr.Logs) != 2 || len(tr.Timestampers) != 1 {
		t.Fatalf("got %+v, %v", tr, err)
	}
	bundle, err := a.Attest(rekor2Build(), pcrsA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.Verify(production, tr, bundle, pcrsA); err != nil {
		t.Fatal(err)
	}
}

// The fuzzer changes a Rekor v2 bundle and its measurements. It has no signing
// key, so nothing but the bundle it was given may be accepted.
func FuzzVerifyRekor2(f *testing.F) {
	a, err := provenancetest.New()
	if err != nil {
		f.Fatal(err)
	}
	tr := a.TrustRoot()
	build := rekor2Build()
	bundle, err := a.Attest(build, pcrsA)
	if err != nil {
		f.Fatal(err)
	}
	first, err := a.Attest(testBuild(), pcrsA)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(bundle, pcrsA)
	f.Add(first, pcrsA)
	f.Add([]byte(`{}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, bundle, pcrs []byte) {
		rel, err := provenance.Verify(production, tr, bundle, pcrs)
		if err != nil {
			return
		}
		if rel.Commit != build.Commit || !bytes.Equal(pcrs, pcrsA) {
			t.Fatalf("accepted %+v for %q", rel, pcrs)
		}
	})
}

// The fuzzer changes the timestamp of a good Rekor v2 bundle: the parser of
// timestamps must not fail on any input, and no input may move the time.
func FuzzTimestamp(f *testing.F) {
	a, err := provenancetest.New()
	if err != nil {
		f.Fatal(err)
	}
	tr := a.TrustRoot()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	build := rekor2Build()
	build.At = at
	bundle, err := a.Attest(build, pcrsA)
	if err != nil {
		f.Fatal(err)
	}
	sig := signature(f, bundle)
	for _, s := range []provenancetest.Stamp{{}, {Fraction: true}, {ByKeyID: true}, {Status: 2}, {Imprint: provenancetest.SHA512}, {At: at.Add(time.Minute)}, {Signers: 2}} {
		s.Message = sig
		if s.At.IsZero() {
			s.At = at
		}
		stamp, err := a.Stamp(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(stamp)
	}
	var doc map[string]any
	if err := json.Unmarshal(bundle, &doc); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, stamp []byte) {
		setStamps(doc, stamp)
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := provenance.Verify(production, tr, edited, pcrsA)
		if err != nil {
			return
		}
		// The authority signed two times for this signature, both among the seeds.
		if !rel.LoggedAt.Equal(at) && !rel.LoggedAt.Equal(at.Add(time.Minute)) {
			t.Fatalf("accepted the time %s", rel.LoggedAt)
		}
	})
}
