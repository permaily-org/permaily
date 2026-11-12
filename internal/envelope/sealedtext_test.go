package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestSealedText(t *testing.T) {
	key, other := bytes.Repeat([]byte{1}, KeySize), bytes.Repeat([]byte{2}, KeySize)
	doc := []byte(`{"address":"ana@example.org","code":"123456"}`)
	aad := AAD("entry", "pending#attempt#att_1", "pending")
	value, err := SealText(key, doc, aad)
	if err != nil {
		t.Fatal(err)
	}
	// Text, and nothing of what it holds can be read in it.
	if !json.Valid(value) || bytes.Contains(value, []byte("ana@example.org")) || bytes.Contains(value, []byte("123456")) {
		t.Fatalf("what is stored: %s", value)
	}
	got, err := OpenText(key, value, aad)
	if err != nil || !bytes.Equal(got, doc) {
		t.Fatalf("round trip: %s %v", got, err)
	}

	var changed sealedText
	_ = json.Unmarshal(value, &changed)
	changed.Sealed[len(changed.Sealed)-1] ^= 1
	tampered, _ := json.Marshal(changed)
	cases := map[string]struct {
		key, value, aad []byte
	}{
		"another key":       {other, value, aad},
		"another partition": {key, value, AAD("entry", "pending#attempt#att_2", "pending")},
		"another key name":  {key, value, AAD("entry", "pending#attempt#att_1", "other")},
		"changed bytes":     {key, tampered, aad},
		"not JSON":          {key, []byte("sealed"), aad},
		"nothing sealed":    {key, []byte(`{"sealed":""}`), aad},
		"a signed value":    {key, mustSign(t, key, doc, aad), aad},
	}
	for name, c := range cases {
		if _, err := OpenText(c.key, c.value, c.aad); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A key that is not one is an error, not a value that does not open.
	if _, err := SealText([]byte("short"), doc, aad); err == nil {
		t.Error("sealed with a short key")
	}
	if _, err := OpenText([]byte("short"), value, aad); err == nil || errors.Is(err, ErrOpen) {
		t.Errorf("opened with a short key: %v", err)
	}
}

func mustSign(t *testing.T, key, doc, aad []byte) []byte {
	t.Helper()
	v, err := Sign(key, doc, aad)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
