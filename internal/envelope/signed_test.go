package envelope

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSigned(t *testing.T) {
	key, other := bytes.Repeat([]byte{1}, KeySize), bytes.Repeat([]byte{2}, KeySize)
	doc := []byte(`{"address":"ana@example.org","state":"active"}`)
	aad := AAD("account", "acc_1")
	value, err := Sign(key, doc, aad)
	if err != nil {
		t.Fatal(err)
	}
	// Whoever runs the storage reads the document as it is.
	if !bytes.Contains(value, doc) {
		t.Fatalf("the document is not readable in %s", value)
	}
	got, err := Verified(key, value, aad)
	if err != nil || !bytes.Equal(got, doc) {
		t.Fatalf("round trip: %s %v", got, err)
	}

	// And cannot change it, move it to another context, or make one.
	forged := []byte(strings.Replace(string(value), "ana@example.org", "eve@example.org", 1))
	cases := map[string]struct {
		key, value, aad []byte
	}{
		"changed document": {key, forged, aad},
		"another context":  {key, value, AAD("account", "acc_2")},
		"another key":      {other, value, aad},
		"no code":          {key, []byte(`{"doc":{"state":"active"}}`), aad},
		"no document":      {key, []byte(`{"mac":"AAAA"}`), aad},
		"not JSON":         {key, []byte("x"), aad},
		"short key":        {key[:16], value, aad},
	}
	for name, c := range cases {
		if _, err := Verified(c.key, c.value, c.aad); !errors.Is(err, ErrSigned) {
			t.Errorf("%s: %v, want ErrSigned", name, err)
		}
	}

	if _, err := Sign(key[:16], doc, aad); err == nil {
		t.Error("signed with a short key")
	}
	if _, err := Sign(key, []byte("not json"), aad); err == nil {
		t.Error("signed something that is not JSON")
	}
}

// A context and a document must not be confused with another pair that
// concatenates to the same bytes.
func TestSignedContextIsBound(t *testing.T) {
	key := bytes.Repeat([]byte{1}, KeySize)
	a, _ := Sign(key, []byte(`1`), []byte("ab"))
	if _, err := Verified(key, a, []byte("a")); !errors.Is(err, ErrSigned) {
		t.Fatal("a shorter context verified")
	}
}
