package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouter(t *testing.T) {
	hosts := HostsForDomain("permaily.test")
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "spa") })
	common := map[string]http.Handler{
		"/version": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "v") }),
	}
	h := New(hosts, common, app)

	cases := []struct {
		host, path string
		status     int
		body       string
	}{
		{"app.permaily.test", "/", 200, "spa"},
		{"APP.permaily.test:443", "/rules", 200, "spa"},
		{"api.permaily.test", "/", 200, "hello from permaily api"},
		{"mcp.permaily.test", "/", 200, "hello from permaily mcp"},
		{"inbox.permaily.test", "/", 200, "hello from permaily inbox"},
		{"api.permaily.test", "/missing", 404, ""},
		{"inbox.permaily.test", "/version", 200, "v"},
		{"evil.example", "/", http.StatusMisdirectedRequest, "unknown host"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "https://"+c.host+c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s%s: got %d %q, want %d containing %q", c.host, c.path, rec.Code, rec.Body.String(), c.status, c.body)
		}
		for _, header := range []string{"Strict-Transport-Security", "Content-Security-Policy", "X-Content-Type-Options"} {
			if rec.Header().Get(header) == "" {
				t.Errorf("%s%s: missing %s", c.host, c.path, header)
			}
		}
	}
}
