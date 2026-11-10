package httpapi

import (
	"context"
	"net/http"

	"github.com/permaily-org/permaily/internal/buildinfo"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Status returns a handler for GET /status. kms reads the policy and grants of
// the pinned KMS key; nil means this image stores no mail. The response has no
// signature: a verifier trusts it because it arrives over the TLS key bound in
// the attestation document (attestation.FetchStatus).
func Status(kms func(context.Context) (attestation.KMSStatus, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := attestation.Status{Commit: buildinfo.Commit}
		if kms != nil {
			k, err := kms(r.Context())
			if err != nil {
				// The cause may name AWS resources and comes through the untrusted host; keep it out of the response.
				http.Error(w, "KMS key policy unavailable", http.StatusServiceUnavailable)
				return
			}
			st.KMS = &k
		}
		writeJSON(w, st)
	})
}
