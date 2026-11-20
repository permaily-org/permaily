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
	// Mail describes the mail the enclave sends and whether SES would keep a
	// copy of it; nil when the image sends no mail.
	Mail func() *attestation.MailStatus
	// Notices counts what this enclave did with the queue of notices by
	// mail (ADR 0041, "What /status and permaily-verify show"), reported as
	// mail.notices with Mail's answer; nil when the image queues none.
	Notices func() *attestation.NoticesStatus
	// Admin counts what the operator did to accounts through this enclave;
	// nil when the image has no accounts.
	Admin func() *attestation.AdminStatus
	// Model describes the model the enclave asks (ADR 0028, "What /status
	// and permaily-verify show"): what is compiled into the image and this
	// enclave's state, never a key, an amount spent or anything of a
	// person; nil when the image asks no model.
	Model func(context.Context) *attestation.ModelStatus
	// Connections describes the authorization server, the default rule and
	// the record of what left (ADR 0030, "What /status and permaily-verify
	// show"): what the image compiles in and this enclave's refusals by
	// fixed reason, never a token, a client that is not verified, an
	// account, a connection or a count of them; nil when the image has no
	// connections.
	Connections func() *attestation.ConnectionsStatus
}

// Status returns a handler for GET /status. The response has no signature: a
// verifier trusts it because it arrives over the TLS key bound in the
// attestation document (attestation.FetchStatus).
func Status(src StatusSources) http.Handler {
	tls, root, sigstore, anchors, mail, admin, model, conns := src.TLS, src.Root, src.Sigstore, src.Anchors, src.Mail, src.Admin, src.Model, src.Connections
	notices := src.Notices
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
		if mail != nil {
			st.Mail = mail()
		}
		if st.Mail != nil && notices != nil {
			// Mail's answer is a copy of its own: the counts go in it.
			st.Mail.Notices = notices()
		}
		if admin != nil {
			st.Admin = admin()
		}
		if model != nil {
			st.Model = model(r.Context())
		}
		if conns != nil {
			st.Connections = conns()
		}
		writeJSON(w, st)
	})
}
