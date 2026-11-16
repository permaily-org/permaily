// Package server is the app as the enclave serves it, for the web app's
// real-browser tests (web/tests/e2e, V63 of ADR 0030): tests only, never in
// the image. It composes what cmd/permaily-enclave composes in front of the
// web app, with nothing else: the router's security headers, the app's host
// with its one decision about a path (httpapi.AppHost) and the SPA's
// handler (webui.Handler, whose dist the build copied in). The API is not
// here: the tests play it in the browser's context.
package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/webui"
)

// app is the handler: the app's host is localhost, as the tests name it.
func app() http.Handler {
	mux := httpapi.NewAppMux()
	mux.Handle("/", webui.Handler())
	return router.New(nil, map[string]http.Handler{"localhost": httpapi.AppHost(mux, nil)})
}

// TestServe serves the app until it is stopped, when PERMAILY_E2E_ADDR
// names where (web/tests/e2e/serve.sh). Skipped otherwise.
func TestServe(t *testing.T) {
	addr := os.Getenv("PERMAILY_E2E_ADDR")
	if addr == "" {
		t.Skip("PERMAILY_E2E_ADDR is not set: the real-browser tests' server is not asked for")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: app(), ReadHeaderTimeout: 10 * time.Second}
	t.Logf("e2e: the app on %s", addr)
	if err := srv.Serve(ln); err != nil {
		t.Fatal(err)
	}
}

// TestTheServerIsTheEnclaves checks the composition without a browser: the
// router's headers on every answer, and the app's host refusing a path
// that is not canonical before the SPA sees it.
func TestTheServerIsTheEnclaves(t *testing.T) {
	h := app()
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost:5175"+path, nil))
		return w
	}
	w := get("/oauth/authorize")
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy is %q", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != router.AppCSP {
		t.Errorf("Content-Security-Policy is %q", got)
	}
	if w := get("/oauth//authorize"); w.Code == http.StatusOK {
		t.Errorf("a path that is not canonical is answered %d", w.Code)
	}
}
