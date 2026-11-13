//go:build dev

package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
)

// ADR 0026, "The app's host: one decision about a path": no route of this
// application needs an encoded character in its path. This walks every
// pattern registered on the app's host by the enclave as it runs (its own
// mux, the accounts API, the mailboxes API, the dev routes), the web app's
// routes, its built assets, the provider's callback with its query and the
// sign-in link from mail, and asserts that each one's own path is canonical
// and passes the gate to the handler behind it.
func TestEveryRouteOfTheAppHostIsCanonical(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	cfg := config{domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing"}
	go func() { _ = run(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
	frame := "GET /api/v1/mailboxes/{id}/messages/{message}/html"
	for i := 0; !slices.Contains(httpapi.RegisteredPatterns(), frame); i++ {
		if i > 300 {
			t.Fatal("the enclave registered no route of the app's host")
		}
		time.Sleep(20 * time.Millisecond)
	}

	samples := map[string]string{
		"id": "mbx_01jbz3k4q5r6s7t8v9w0x1y2z3", "message": "18e7000000000000aaaaaaaaaaaaaaaa", "index": "0",
		"link": "0.Mhw80zyWKGnmqy03", "mailboxId": "mbx_01jbz3k4q5r6s7t8v9w0x1y2z3", "messageId": "18e7000000000000aaaaaaaaaaaaaaaa",
		"vendorId": "google", "connectionId": "con_01jbz3k4q5",
	}
	wildcard := regexp.MustCompile(`\{([A-Za-z]+)(\.\.\.)?\}|:([A-Za-z]+)`)
	concrete := func(pattern string) string {
		if i := strings.IndexByte(pattern, ' '); i >= 0 {
			pattern = pattern[i+1:]
		}
		return wildcard.ReplaceAllStringFunc(pattern, func(w string) string {
			name := strings.Trim(w, "{}.:")
			if v, ok := samples[name]; ok {
				return v
			}
			return "x_" + strings.ToLower(name) + "-0"
		})
	}
	var paths []string
	for _, p := range httpapi.RegisteredPatterns() {
		paths = append(paths, concrete(p))
	}
	if len(paths) < 30 {
		t.Fatalf("%d patterns registered: %v", len(paths), paths)
	}
	// The web app's own routes, from its router.
	app, err := os.ReadFile("../../web/src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`path="([^"*]+)"`).FindAllStringSubmatch(string(app), -1) {
		paths = append(paths, concrete("/"+m[1]))
	}
	// Its assets as Vite names them, when built.
	_ = filepath.WalkDir("../../web/dist", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, "/"+filepath.ToSlash(strings.TrimPrefix(p, "../../web/dist/")))
		}
		return nil
	})
	paths = append(paths, httpapi.ProviderCallback+"?state=a%2Fb%3D&code=4%2F0Ab_c-d", "/sign-in", "/favicon.ico")

	reached := 0
	gate := httpapi.RequireCanonicalPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	for _, p := range paths {
		escaped, _, _ := strings.Cut(p, "?")
		if !httpapi.CanonicalPath(escaped) {
			t.Errorf("%s: not canonical", p)
			continue
		}
		before := reached
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "https://app.permaily.test"+p, nil))
		if reached != before+1 {
			t.Errorf("%s: refused by the gate", p)
		}
	}
	t.Logf("%d paths of the app's host checked", len(paths))
}
