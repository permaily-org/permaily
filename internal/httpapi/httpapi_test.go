package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestAttestation(t *testing.T) {
	att, err := fakensm.New()
	if err != nil {
		t.Fatal(err)
	}
	userData := bytes.Repeat([]byte{7}, 32)
	h := httpapi.Attestation(att, func() []byte { return userData })

	call := func(method, nonce string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/attestation?nonce="+nonce, nil))
		return rec
	}

	nonce := bytes.Repeat([]byte{1}, 32)
	rec := call(http.MethodGet, hex.EncodeToString(nonce))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, cache-control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body httpapi.AttestationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(body.Document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attestation.Verify(raw, attestation.Options{Root: att.CA.Root, Nonce: nonce, UserData: userData}); err != nil {
		t.Fatalf("document does not carry the nonce and user data: %v", err)
	}

	for name, tc := range map[string]struct {
		method, nonce string
		status        int
	}{
		"not hex":    {http.MethodGet, "zz", http.StatusBadRequest},
		"too short":  {http.MethodGet, strings.Repeat("ab", 15), http.StatusBadRequest},
		"too long":   {http.MethodGet, strings.Repeat("ab", 513), http.StatusBadRequest},
		"min length": {http.MethodGet, strings.Repeat("ab", 16), http.StatusOK},
		"max length": {http.MethodGet, strings.Repeat("ab", 512), http.StatusOK},
		"POST":       {http.MethodPost, strings.Repeat("ab", 32), http.StatusMethodNotAllowed},
	} {
		if got := call(tc.method, tc.nonce).Code; got != tc.status {
			t.Errorf("%s: status %d, want %d", name, got, tc.status)
		}
	}
}
