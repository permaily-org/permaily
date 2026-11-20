package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/webhooks"
)

// The routes of connections' webhooks (ADR 0039, pull request 4), adapters
// that decide nothing: the person's route that turns a connection's webhook
// off, on the app's host with the person's other routes of connections
// (ConnectionsAPI), and the key set notices are signed with, published on
// the api host (WebhookKeys). Nothing of a webhook's URL, its subscription,
// a time zone or a key is logged.

// webhookOff is the body of POST /api/v1/connections/{id}/webhook/off: the
// version of the connection the page showed, which the service writes at
// (contracts.ErrConsentChanged when it changed since).
type webhookOff struct {
	Version uint64 `json:"version"`
}

// webhookOff turns the connection's webhook off: no fresh proof (it only
// protects) and no rule of standing of the route's own, so it works under
// unagreed terms as revoking does (ADR 0039, "Retries and giving up",
// choice 11).
func (a *consentAPI) webhookOff(w http.ResponseWriter, r *http.Request, p connections.Person) {
	var in webhookOff
	if !decodeWhole(w, r, &in) {
		return
	}
	out, err := a.cfg.Webhooks.WebhookOff(r.Context(), p, r.PathValue("id"), in.Version)
	a.answer(w, r, out, err)
}

// WebhookKeys serves the JWK set every notice's signature verifies with
// (ADR 0039, "Signatures"), at webhook.KeysPath on the api host, the
// issuer's, over the enclave's own certificate: GET or HEAD, any origin,
// public for an hour, as the server's metadata is. /status reports the same
// keys over the attested TLS key, and permaily-verify compares the two. A
// set the service cannot give now is 503, no-store, logged by its kind
// alone; never a set from before.
func WebhookKeys(keys webhooks.KeySet, log *slog.Logger) http.Handler {
	return logRequests(log, "webhooks: request", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRoute(r, "webhook_keys")
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodOptions:
			h.Set("Access-Control-Allow-Methods", "GET, HEAD")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			h.Set("Allow", "GET, HEAD, OPTIONS")
			h.Set("Cache-Control", "no-store")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, err := keys.KeySet(r.Context())
		if err != nil || len(b) == 0 {
			if clientLeft(r, err) {
				setOutcome(r, outcomeClientLeft)
			} else {
				log.Error("webhooks: key set", "kind", "unavailable")
			}
			h.Set("Cache-Control", "no-store")
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", metadataCache)
		h.Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	}))
}
