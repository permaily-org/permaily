//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
)

// spyMatcher is the enclave's matcher, counting who asks it.
type spyMatcher struct {
	inbox.Matcher
	mu    sync.Mutex
	asked int
}

func (s *spyMatcher) Match(ctx context.Context, b inbox.Box, fs []*query.Canonical, before string, limit int) (*contracts.MatchPage, error) {
	s.mu.Lock()
	s.asked++
	s.mu.Unlock()
	return s.Matcher.Match(ctx, b, fs, before, limit)
}

func (s *spyMatcher) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

// Q45 (ADR 0027, "The routes"), extended by V46 (ADR 0030): a page of
// matches is answered by POST /api/v1/mailboxes/{id}/matches alone, in a
// session, and no route of a vendor (the authorization server, the API,
// MCP) answers a MatchPage. Every route the
// enclave registers, sent to every host it serves (the app's, api., mcp.,
// inbox. and mta-sts.), by GET and by POST, with a session of the mailbox's
// owner (its cookie from the app's own page, and the operator's token) and
// without one: the matcher is asked by that one request and by no other.
func TestQ45OnlyTheMatchesRouteAsksTheMatcher(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	const admin = "pma_q45_token"
	sum := sha256.Sum256([]byte(admin))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hosts := router.HostsForDomain(cfg.domain)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	p, err := newPlatform(ctx, cfg, hosts, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.listener.Close(); _ = p.smtpListener.Close() })
	spy := &spyMatcher{Matcher: p.matcher}
	p.matcher = spy
	h := handler(cfg, hosts, p, log)

	const app = "https://app.permaily.test"
	var cookie *http.Cookie
	// call asks h as the app's own page does when signed is set (the
	// session's cookie, same origin, the operator's token too); without it,
	// as anybody.
	call := func(host, method, path, body string, signed bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "https://"+host+path, strings.NewReader(body))
		if signed {
			r.Header.Set("Origin", app)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+admin)
			if cookie != nil {
				r.AddCookie(cookie)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, status int, v any) {
		t.Helper()
		if rec.Code != status || (v != nil && json.Unmarshal(rec.Body.Bytes(), v) != nil) {
			t.Fatalf("%d %s, want %d", rec.Code, rec.Body, status)
		}
	}

	// Signing up as the app does, and making a mailbox.
	var made struct{ Invitations []string }
	decode(call(hosts.API, "POST", "/v1/admin/invitations", `{"count":1}`, true), 200, &made)
	const address = "q45@example.org"
	var started struct{ Attempt string }
	decode(call(hosts.App, "POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`, true), 200, &started)
	code := ""
	for i := 0; code == ""; i++ { // the message is kept once it is sent, after the answer
		if i == 50 {
			t.Fatal("no sign-in message")
		}
		time.Sleep(20 * time.Millisecond)
		var sent []struct{ To, Subject, Text string }
		decode(call(hosts.App, "GET", "/dev/outbox", "", false), 200, &sent)
		for _, m := range sent {
			if g := regexp.MustCompile(`\n    (\d{6})\n`).FindStringSubmatch(m.Text); g != nil && m.To == address && strings.Contains(m.Text, started.Attempt) {
				code = g[1]
			}
		}
	}
	finished := call(hosts.App, "POST", "/api/v1/auth/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+code+`"}`, true)
	decode(finished, 200, nil)
	for _, c := range finished.Result().Cookies() {
		if c.Name == httpapi.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session")
	}
	var box contracts.Mailbox
	decode(call(hosts.App, "POST", "/api/v1/mailboxes", `{"label":"Mine"}`, true), 201, &box)
	matchesPath := "/api/v1/mailboxes/" + box.ID + "/matches"

	// Every route registered on the app's host, with the mailbox's identifier
	// where a route takes one; every route of the API host; the routes of
	// every host.
	samples := map[string]string{"id": box.ID, "mailboxId": box.ID, "message": "18e7000000000000aaaaaaaaaaaaaaaa", "index": "0", "link": "0.Mhw80zyWKGnmqy03"}
	wildcard := regexp.MustCompile(`\{([A-Za-z]+)(\.\.\.)?\}`)
	var paths []string
	for _, pattern := range httpapi.RegisteredPatterns() {
		if i := strings.IndexByte(pattern, ' '); i >= 0 {
			pattern = pattern[i+1:]
		}
		paths = append(paths, wildcard.ReplaceAllStringFunc(pattern, func(w string) string {
			if v, ok := samples[strings.Trim(w, "{}.")]; ok {
				return v
			}
			return "x_" + strings.ToLower(strings.Trim(w, "{}.")) + "-0"
		}))
	}
	if !slices.Contains(paths, matchesPath) || len(paths) < 30 {
		t.Fatalf("the routes of the app's host: %v", paths)
	}
	account := "acc_" + strings.Repeat("a", 26)
	paths = append(paths, "/", "/attestation", "/status", "/version", "/dev/outbox",
		"/v1/admin/handoff", "/v1/admin/trust-root", "/v1/admin/invitations", "/v1/admin/invitations/x",
		"/v1/admin/accounts", "/v1/admin/accounts/"+account, "/v1/admin/accounts/"+account+"/mailboxes",
		"/v1/admin/providers", "/v1/admin/providers/google", "/v1/admin/mailboxes", "/v1/admin/mailboxes/"+box.ID+"/pause", "/v1/admin/mailboxes/"+box.ID+"/resume",
		// The vendors' routes (V46): the authorization server, the API and
		// MCP, on whatever host they are asked.
		"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/v1",
		"/.well-known/oauth-protected-resource/mcp", "/oauth/token", "/oauth/revoke", "/oauth/par", "/v1/connection", "/v1/messages/search",
		"/v1/messages/"+box.ID+".18e7000000000000aaaaaaaaaaaaaaaa", "/v1/messages/"+box.ID+".18e7000000000000aaaaaaaaaaaaaaaa/attachments/0", "/mcp")
	slices.Sort(paths)
	paths = slices.Compact(paths)

	body := `{"formula":{"v":1,"all":[]}}`
	type ask struct {
		host, method, path string
		signed             bool
	}
	var reached []ask
	asks := 0
	askAll := func(path string) {
		for _, host := range hosts.All() {
			for _, method := range []string{"GET", "POST"} {
				for _, signed := range []bool{false, true} {
					before := spy.count()
					rec := call(host, method, path, body, signed)
					asks++
					if spy.count() != before {
						reached = append(reached, ask{host, method, path, signed})
					}
					// A MatchPage's own members, in any answer but the
					// person's page of matches.
					if path != matchesPath && (strings.Contains(rec.Body.String(), `"needs_model"`) || strings.Contains(rec.Body.String(), `"searched_to"`)) {
						t.Errorf("%s %s%s answered a MatchPage", method, host, path)
					}
				}
			}
		}
	}
	const signOut = "/api/v1/auth/sign-out"
	for _, path := range paths {
		if path != signOut { // asked last: it ends the session the others are asked in
			askAll(path)
		}
	}
	// The session lasted to the end: the one route still answers.
	decode(call(hosts.App, "POST", matchesPath, body, true), 200, nil)
	askAll(signOut)
	want := []ask{{hosts.App, "POST", matchesPath, true}}
	if !slices.Equal(reached, want) || spy.count() != 2 {
		t.Fatalf("the matcher was asked by %+v (%d times), want %+v", reached, spy.count(), want)
	}
	if rec := call(hosts.App, "POST", matchesPath, body, true); rec.Code != http.StatusUnauthorized || !slices.Contains(paths, signOut) {
		t.Fatalf("signed out: %d", rec.Code)
	}
	t.Logf("%d requests over %d paths and %d hosts", asks, len(paths), len(hosts.All()))
}
