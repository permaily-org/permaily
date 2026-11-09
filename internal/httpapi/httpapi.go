// Package httpapi implements the enclave's HTTP endpoints that are common to
// every host: /attestation and /version.
package httpapi

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/permaily-org/permaily/internal/buildinfo"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// AttestationResponse is the JSON body of GET /attestation.
type AttestationResponse struct {
	// Document is the base64 (standard encoding) COSE_Sign1 attestation document.
	Document string `json:"document"`
	// UserDataScheme says how user_data binds the TLS key.
	UserDataScheme string `json:"user_data_scheme"`
	// Commit is informational; the verifier relies on PCRs, not on this field.
	Commit string `json:"commit"`
}

// VersionResponse is the JSON body of GET /version.
type VersionResponse struct {
	Commit string `json:"commit"`
}

// Nonce limits: at least 16 bytes so replays are impractical, at most the NSM limit.
const (
	minNonce = 16
	maxNonce = 512
)

// Attestation returns a handler for GET /attestation?nonce=<hex>.
// userData returns the SHA-256 of the TLS certificate's SPKI.
func Attestation(att nsm.Attester, userData func() []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		nonce, err := hex.DecodeString(r.URL.Query().Get("nonce"))
		if err != nil || len(nonce) < minNonce || len(nonce) > maxNonce {
			http.Error(w, "nonce must be 16-512 bytes, hex encoded", http.StatusBadRequest)
			return
		}
		doc, err := att.Attest(nonce, userData())
		if err != nil {
			http.Error(w, "attestation failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, AttestationResponse{
			Document:       base64.StdEncoding.EncodeToString(doc),
			UserDataScheme: attestation.UserDataScheme,
			Commit:         buildinfo.Commit,
		})
	})
}

// Version returns a handler for GET /version.
func Version() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, VersionResponse{Commit: buildinfo.Commit})
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
