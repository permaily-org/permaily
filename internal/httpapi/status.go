package httpapi

import (
	"context"
	"net/http"

	"github.com/permaily-org/permaily/internal/buildinfo"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// StatusSources is what GET /status reports. Every field may be nil.
type StatusSources struct {
	// Root describes the key that holds the root secret for this image.
	Root func(context.Context) (*attestation.RootStatus, error)
	// KMS reads the policy and grants of the KMS key that older images
	// wrapped data keys with; nil when the image has no such key.
	KMS func(context.Context) (attestation.KMSStatus, error)
	// TLS describes the certificate key and the ACME account.
	TLS func() *attestation.TLSStatus
}

// Status returns a handler for GET /status. The response has no signature: a
// verifier trusts it because it arrives over the TLS key bound in the
// attestation document (attestation.FetchStatus).
func Status(src StatusSources) http.Handler {
	kms, tls, root := src.KMS, src.TLS, src.Root
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := attestation.Status{Commit: buildinfo.Commit}
		if tls != nil {
			st.TLS = tls()
		}
		if kms != nil {
			k, err := kms(r.Context())
			if err != nil {
				// The cause may name AWS resources and comes through the untrusted host; keep it out of the response.
				http.Error(w, "KMS key policy unavailable", http.StatusServiceUnavailable)
				return
			}
			st.KMS = &k
		}
		if root != nil {
			rs, err := root(r.Context())
			if err != nil {
				// Without its root secret the enclave stores and serves nothing.
				http.Error(w, "root secret unavailable", http.StatusServiceUnavailable)
				return
			}
			st.Root = rs
		}
		writeJSON(w, st)
	})
}
