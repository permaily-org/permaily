package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// V11. Secrets: the prefix and 43 characters; kept only as a hash
// separated by kind; a family's pair the same in every enclave and another
// for another kind, family, generation or key.
func TestV11SecretsAndTheirHashes(t *testing.T) {
	seen := map[string]bool{}
	for _, prefix := range []string{PrefixAccess, PrefixRefresh, PrefixCode, PrefixRequest, PrefixPushed} {
		s, err := NewSecret(prefix, func(b []byte) error { _, err := rand.Read(b); return err })
		if err != nil {
			t.Fatal(err)
		}
		rest, ok := strings.CutPrefix(s, prefix)
		if !ok || len(rest) != 43 || !secretValue(rest) || seen[rest] {
			t.Fatalf("%q", s)
		}
		seen[rest] = true
	}
	broken := errors.New("no randomness")
	if s, err := NewSecret(PrefixCode, func([]byte) error { return broken }); !errors.Is(err, broken) || s != "" {
		t.Fatal(s, err)
	}
	zero, _ := NewSecret(PrefixCode, func([]byte) error { return nil })
	if zero != PrefixCode+strings.Repeat("A", 43) {
		t.Fatal(zero)
	}

	// The hash is the hex SHA-256 of the kind's domain and the secret.
	sum := sha256.Sum256([]byte("permaily oauth code v1\x00" + zero))
	if Hash(KindCode, zero) != hex.EncodeToString(sum[:]) {
		t.Fatal("the hash")
	}
	hashes := map[string]Kind{}
	for _, k := range []Kind{KindAccess, KindRefresh, KindCode, KindRequest, KindPushed, KindJTI} {
		h := Hash(k, zero)
		if other, twice := hashes[h]; twice || len(h) != 64 || strings.Contains(h, zero) {
			t.Fatalf("%s and %s", k, other)
		}
		hashes[h] = k
	}
	if Hash(KindAccess, "a") == Hash(KindAccess, "b") {
		t.Fatal("two secrets, one hash")
	}

	key := []byte("a key derived from the root secret, 32 bytes")
	access := FamilySecret(key, KindAccess, "fam_A", 7)
	refresh := FamilySecret(key, KindRefresh, "fam_A", 7)
	if !strings.HasPrefix(access, PrefixAccess) || !strings.HasPrefix(refresh, PrefixRefresh) || len(access) != len(PrefixAccess)+43 || len(refresh) != len(PrefixRefresh)+43 {
		t.Fatal(access, refresh)
	}
	if access != FamilySecret(key, KindAccess, "fam_A", 7) {
		t.Fatal("another enclave makes another token")
	}
	values := map[string]string{access: "access", refresh[len(PrefixRefresh):]: "refresh", access[len(PrefixAccess):]: "access's value"}
	for name, v := range map[string]string{
		"another family":     FamilySecret(key, KindAccess, "fam_B", 7),
		"another generation": FamilySecret(key, KindAccess, "fam_A", 8),
		"another key":        FamilySecret([]byte("another key"), KindAccess, "fam_A", 7),
		"a family and generation that run together": FamilySecret(key, KindAccess, "fam_A\x00", 7),
	} {
		if _, twice := values[v]; twice || values[v[len(PrefixAccess):]] != "" {
			t.Errorf("%s: the same value", name)
		}
		values[v] = name
	}
	if s := FamilySecret(key, KindCode, "fam_A", 7); strings.HasPrefix(s, PrefixCode) || len(s) != 43 {
		t.Fatalf("a code is never a family's: %q", s)
	}
}

// A family is ended once: a second end changes nothing (family.go, which
// the service calls when a device is revoked or reused).
func TestAFamilyEndsOnce(t *testing.T) {
	f := FamilyState{Status: FamilyActive}
	at := vectorNow
	if !f.End("revoked", at) || f.Status != FamilyEnded || !f.EndedAt.Equal(at) || f.EndReason != "revoked" {
		t.Fatalf("%+v", f)
	}
	if f.End("reuse", at.Add(time.Hour)) || f.EndReason != "revoked" || !f.EndedAt.Equal(at) {
		t.Fatalf("%+v", f)
	}
}
