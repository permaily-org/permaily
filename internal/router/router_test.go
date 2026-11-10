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
	h := New(common, map[string]http.Handler{
		hosts.App:    app,
		hosts.API:    Hello("api"),
		hosts.MCP:    Hello("mcp"),
		hosts.Inbox:  Hello("inbox"),
		hosts.MTASTS: MTASTSPolicy(hosts.Inbox, "testing", 604800),
	})

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
		{"mta-sts.inbox.permaily.test", "/.well-known/mta-sts.txt", 200, "version: STSv1\nmode: testing\nmx: inbox.permaily.test\nmax_age: 604800\n"},
		{"mta-sts.inbox.permaily.test", "/", 404, ""},
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
	if got := hosts.All(); len(got) != 5 || got[4] != "mta-sts.inbox.permaily.test" {
		t.Fatalf("hosts: %v", got)
	}
}
