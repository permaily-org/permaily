// Package router dispatches requests by Host so one enclave binary serves
// app., api., mcp. and inbox. on the same certificate.
package router

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/permaily-org/permaily/internal/buildinfo"
)

// Hosts names the virtual hosts the enclave serves.
type Hosts struct {
	App   string
	API   string
	MCP   string
	Inbox string
}

// All returns every host, App first.
func (h Hosts) All() []string { return []string{h.App, h.API, h.MCP, h.Inbox} }

// HostsForDomain returns the standard subdomains of domain.
func HostsForDomain(domain string) Hosts {
	return Hosts{App: "app." + domain, API: "api." + domain, MCP: "mcp." + domain, Inbox: "inbox." + domain}
}

// New returns the top-level handler. common is mounted on every host
// (/attestation, /version); app serves the SPA on the App host.
func New(h Hosts, common map[string]http.Handler, app http.Handler) http.Handler {
	hello := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "hello from permaily %s %s\n", name, buildinfo.Commit)
		})
	}
	byHost := map[string]http.Handler{
		h.App:   app,
		h.API:   hello("api"),
		h.MCP:   hello("mcp"),
		h.Inbox: hello("inbox"),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if c, ok := common[r.URL.Path]; ok {
			c.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if next, ok := byHost[strings.ToLower(host)]; ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
	})
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
}
