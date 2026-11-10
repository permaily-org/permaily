package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// MailboxService is what the mailbox API needs from the inbox.
type MailboxService interface {
	Mailbox(ctx context.Context) (inbox.MailboxView, error)
	SetRetention(ctx context.Context, days int) error
	List(ctx context.Context, limit int, before string) ([]inbox.Summary, error)
	Get(ctx context.Context, id string) (inbox.Message, error)
	Raw(ctx context.Context, id string) ([]byte, error)
	Delete(ctx context.Context, id string) error
}

// APIError is the error body of every API response that fails.
type APIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e APIError
	e.Error.Code, e.Error.Message = code, message
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// TokenAuth accepts requests whose bearer token hashes (SHA-256, hex) to
// tokenSHA256. The image carries only the hash, so publishing it reveals
// nothing; the token itself stays with the mailbox owners (F1 test mailbox).
func TokenAuth(tokenSHA256 string, next http.Handler) http.Handler {
	want, err := hex.DecodeString(tokenSHA256)
	if err != nil || len(want) != sha256.Size {
		want = nil // misconfigured: reject everything
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))
		if !ok || want == nil || subtle.ConstantTimeCompare(sum[:], want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="permaily"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MailboxAPI returns the /v1/mailbox routes.
func MailboxAPI(svc MailboxService) http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such message")
		case errors.Is(err, inbox.ErrInvalidRetention):
			writeError(w, http.StatusBadRequest, "invalid_retention", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
		}
	}
	mux.HandleFunc("GET /v1/mailbox", func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.Mailbox(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("PATCH /v1/mailbox", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RetentionDays *int `json:"retention_days"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || body.RetentionDays == nil {
			writeError(w, http.StatusBadRequest, "invalid_body", `expected {"retention_days": <days>}`)
			return
		}
		if err := svc.SetRetention(r.Context(), *body.RetentionDays); err != nil {
			fail(w, err)
			return
		}
		v, err := svc.Mailbox(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /v1/mailbox/messages", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 200 {
				writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be 1-200")
				return
			}
			limit = n
		}
		msgs, err := svc.List(r.Context(), limit, r.URL.Query().Get("before"))
		if err != nil {
			fail(w, err)
			return
		}
		res := struct {
			Messages []inbox.Summary `json:"messages"`
			Next     string          `json:"next,omitempty"`
		}{Messages: msgs}
		if len(msgs) == limit {
			res.Next = msgs[len(msgs)-1].ID
		}
		writeJSON(w, res)
	})
	mux.HandleFunc("GET /v1/mailbox/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, err := svc.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, m)
	})
	mux.HandleFunc("GET /v1/mailbox/messages/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		raw, err := svc.Raw(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("id")+`.eml"`)
		w.Header().Set("Cache-Control", "no-store")
		// Downloaded, never rendered: attachment disposition, nosniff, and a
		// sandbox CSP in case a browser displays it anyway.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		_, _ = w.Write(raw) //nolint:gosec // message/rfc822 attachment, sandboxed, not rendered as HTML
	})
	mux.HandleFunc("DELETE /v1/mailbox/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Delete(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
