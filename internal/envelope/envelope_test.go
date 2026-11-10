package envelope

import (
	"bytes"
	"errors"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func TestSealOpen(t *testing.T) {
	aad := AAD("mailbox", "m1", "message", "x1")
	sealed, err := Seal(key(1), []byte("hello"), aad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(key(1), sealed, aad)
	if err != nil || string(got) != "hello" {
		t.Fatalf("open: %q, %v", got, err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	for name, tc := range map[string]struct {
		key, sealed, aad []byte
	}{
		"wrong key":        {key(2), sealed, aad},
		"wrong context":    {key(1), sealed, AAD("mailbox", "m2", "message", "x1")},
		"tampered":         {key(1), tampered, aad},
		"truncated":        {key(1), sealed[:10], aad},
		"commitment check": {key(2), append([]byte{sealed[0]}, append(commitment(key(2)), sealed[1+commitSize:]...)...), aad},
	} {
		if _, err := Open(tc.key, tc.sealed, tc.aad); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: err = %v, want ErrOpen", name, err)
		}
	}
}

func TestAADIsUnambiguous(t *testing.T) {
	if bytes.Equal(AAD("ab", "c"), AAD("a", "bc")) {
		t.Fatal("different parts produced the same AAD")
	}
}

func TestKeySize(t *testing.T) {
	if _, err := Seal([]byte("short"), nil, nil); err == nil {
		t.Fatal("accepted a short key")
	}
}
