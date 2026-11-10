package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
	"github.com/permaily-org/permaily/pkg/provenance"
)

// maxHandoffBody bounds the request: two bundles of about 12 KiB, in base64.
const maxHandoffBody = 1 << 20

// HandoffRequest is the body of POST /v1/admin/handoff: the measurements and
// the Sigstore bundle of the release that will run next, and of the one that
// is running. Each is the file as published, in base64.
type HandoffRequest struct {
	ToPCRs     []byte `json:"to_pcrs"`
	ToBundle   []byte `json:"to_bundle"`
	FromPCRs   []byte `json:"from_pcrs"`
	FromBundle []byte `json:"from_bundle"`
}

// Handoff returns the handler for POST /v1/admin/handoff, which asks the
// running enclave to give the root secret to the next release. Put it behind
// TokenAuth: the token limits who can make the enclave work, not who gets the
// secret. The enclave decides that from the request alone, and refuses an
// image that is not a public release whoever asks (keys.Root.HandOff).
func Handoff(hand func(context.Context, keys.Handoff) (*keys.HandoffResult, error), log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHandoffBody))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the request is too large")
			return
		}
		var req HandoffRequest
		if err := json.Unmarshal(body, &req); err != nil || len(req.ToPCRs) == 0 || len(req.ToBundle) == 0 || len(req.FromPCRs) == 0 || len(req.FromBundle) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "to_pcrs, to_bundle, from_pcrs and from_bundle are required, each in base64")
			return
		}
		res, err := hand(r.Context(), keys.Handoff{ToPCRs: req.ToPCRs, ToBundle: req.ToBundle, FromPCRs: req.FromPCRs, FromBundle: req.FromBundle})
		switch {
		case err == nil:
			writeJSON(w, res)
		case errors.Is(err, provenance.ErrNotARelease), errors.Is(err, provenance.ErrUntrusted), errors.Is(err, provenance.ErrMalformed),
			errors.Is(err, keys.ErrDowngrade), errors.Is(err, keys.ErrNotThisImage), errors.Is(err, kmspolicy.ErrNotSealed), errors.Is(err, keys.ErrKeyAccount):
			// A refusal: its reason names only public things (measurements, key ARNs, workflow identities).
			writeError(w, http.StatusUnprocessableEntity, "refused", err.Error())
		case errors.Is(err, keys.ErrDebugMode), errors.Is(err, keys.ErrAwaitingHandoff), errors.Is(err, keys.ErrRootRecord):
			writeError(w, http.StatusServiceUnavailable, "no_root_secret", "this enclave holds no root secret to hand over")
		default:
			// A failure of KMS or the store, reached through the host: keep the cause in the log.
			log.Error("handoff failed", "err", err)
			writeError(w, http.StatusBadGateway, "failed", "the handoff failed; see the enclave log")
		}
	})
}
