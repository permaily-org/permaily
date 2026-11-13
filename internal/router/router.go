// Package router dispatches requests by Host so one enclave binary serves
// app., api., mcp., inbox. and mta-sts.inbox. on the same certificate.
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
	App    string
	API    string
	MCP    string
	Inbox  string
	MTASTS string // MTA-STS policy host for the inbox domain
}

// All returns every host, App first. They all go on the TLS certificate.
func (h Hosts) All() []string { return []string{h.App, h.API, h.MCP, h.Inbox, h.MTASTS} }

// HostsForDomain returns the standard subdomains of domain.
func HostsForDomain(domain string) Hosts {
	return Hosts{
		App:    "app." + domain,
		API:    "api." + domain,
		MCP:    "mcp." + domain,
		Inbox:  "inbox." + domain,
		MTASTS: "mta-sts.inbox." + domain,
	}
}

// Hello answers "/" with a plain greeting naming the host (placeholder for
// hosts without a product yet).
func Hello(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "hello from permaily %s %s\n", name, buildinfo.Commit)
	})
}

// MTASTSPolicy serves the MTA-STS policy (RFC 8461) for the inbox MX.
// mode is "testing" or "enforce".
func MTASTSPolicy(mx, mode string, maxAge int) http.Handler {
	policy := fmt.Sprintf("version: STSv1\nmode: %s\nmx: %s\nmax_age: %d\n", mode, mx, maxAge)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/mta-sts.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(policy))
	})
}

// New returns the top-level handler. common is mounted on every host
// (/attestation, /version); byHost maps each host name to its handler.
func New(common map[string]http.Handler, byHost map[string]http.Handler) http.Handler {
	hosts := make(map[string]http.Handler, len(byHost))
	for k, v := range byHost {
		hosts[strings.ToLower(k)] = v
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		// A route of every host answers its path as written, never an
		// encoding of it: a request for /%61ttestation goes to its host,
		// whose own rules answer it (the app's refuses it).
		if c, ok := common[r.URL.Path]; ok && r.URL.RawPath == "" {
			c.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if next, ok := hosts[strings.ToLower(host)]; ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
	})
}

// AppCSP is the Content-Security-Policy of every answer of the enclave, but
// the frame's route, which sets its own.
const AppCSP = "default-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// frame-src is written out (ADR 0026, "The frame"): the app frames only
	// its own documents, a message's HTML, whatever default-src becomes.
	h.Set("Content-Security-Policy", AppCSP)
}
