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
	// TLS describes the certificate key and the ACME account.
	TLS func() *attestation.TLSStatus
	// Sigstore says which trust root releases are checked against.
	Sigstore func() *attestation.SigstoreStatus
	// Anchors describes the records that cannot be put back to an earlier copy.
	Anchors func() *attestation.AnchorStatus
}

// Status returns a handler for GET /status. The response has no signature: a
// verifier trusts it because it arrives over the TLS key bound in the
// attestation document (attestation.FetchStatus).
func Status(src StatusSources) http.Handler {
	tls, root, sigstore, anchors := src.TLS, src.Root, src.Sigstore, src.Anchors
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := attestation.Status{Commit: buildinfo.Commit}
		if tls != nil {
			st.TLS = tls()
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
		if sigstore != nil {
			st.Sigstore = sigstore()
		}
		if anchors != nil {
			st.Anchors = anchors()
		}
		writeJSON(w, st)
	})
}
