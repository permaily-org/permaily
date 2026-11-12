package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

// The handoff and the trust root's routes answer in an image with no
// mailboxes at all (ADR 0026, "The test mailbox: retired"), and every route
// left on the API host answers JSON with nosniff.
func TestKeyRoutesStandOnTheirOwn(t *testing.T) {
	const token = "pma_routes"
	sum := sha256.Sum256([]byte(token))
	cfg := config{adminTokenSHA256: hex.EncodeToString(sum[:])}
	p := &platform{
		handoff: func(context.Context, keys.Handoff) (*keys.HandoffResult, error) {
			return nil, errors.New("no next release here")
		},
		updateTrust: func(provenance.Update) (*attestation.SigstoreStatus, error) {
			return nil, errors.New("no newer trust root")
		},
	}
	api := http.NewServeMux()
	keyRoutes(api, cfg, p, slog.New(slog.DiscardHandler))
	api.Handle("/status", httpapi.Status(httpapi.StatusSources{}))
	hosts := router.HostsForDomain("permaily.test")
	h := router.New(nil, map[string]http.Handler{hosts.API: api})
	call := func(method, path, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://"+hosts.API+path, strings.NewReader(body))
		req.Host = hosts.API
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/v1/admin/handoff", "/v1/admin/trust-root"} {
		rec := call(http.MethodPost, path, "", "{}")
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", path, rec.Code, rec.Header())
		}
		if rec := call(http.MethodPost, path, token, "{}"); rec.Code == http.StatusNotFound || rec.Code == http.StatusUnauthorized || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s with the token: %d", path, rec.Code)
		}
	}
	if rec := call(http.MethodGet, "/status", "", ""); rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status: %d %v", rec.Code, rec.Header())
	}
	// /v1/mailbox is gone: a path under it is unknown.
	if rec := call(http.MethodGet, "/v1/mailbox/messages", token, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("/v1/mailbox: %d", rec.Code)
	}
	// Without a root secret neither is mounted.
	bare := http.NewServeMux()
	keyRoutes(bare, cfg, &platform{}, slog.New(slog.DiscardHandler))
	if _, pattern := bare.Handler(httptest.NewRequest(http.MethodPost, "/v1/admin/handoff", nil)); pattern != "" {
		t.Fatal("mounted without a root secret")
	}
}
