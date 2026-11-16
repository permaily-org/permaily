package mcpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
)

// V48. Stateless: the 2026-07-28 calls of Claude Code 2.1.325 (server/discover,
// subscriptions/listen, tools/list, tools/call: no initialize, no session)
// and Codex CLI 0.169.0's 2025-06-18 initialize and calls, shaped as the
// spike's log recorded them, each request through the other enclave than the
// one before (two handlers over one service): every one is answered, and no
// answer names a session.
func TestV48StatelessClaudeCodeAndCodexThroughEitherEnclave(t *testing.T) {
	w := newWorld()
	enclaves := []http.Handler{w.handler(), w.handler()}
	n := 0
	next := func() http.Handler { n++; return enclaves[n%2] }

	t.Run("claude code, 2026-07-28", func(t *testing.T) {
		discover := decode(t, mustNoSession(t, do(next(), claudeCall(1, "server/discover", ""))))
		var d struct {
			SupportedVersions []string `json:"supportedVersions"`
			Instructions      string   `json:"instructions"`
			Capabilities      struct {
				Tools *struct{} `json:"tools"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(discover.Result, &d); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(d.SupportedVersions, protocolClaudeCode) || !slices.Contains(d.SupportedVersions, protocolCodex) {
			t.Fatalf("versions %v", d.SupportedVersions)
		}
		if d.Instructions != mcpapi.Instructions || d.Capabilities.Tools == nil {
			t.Fatalf("discover %s", discover.Result)
		}

		// subscriptions/listen: nothing to subscribe to; the stream it opens
		// ends with the request, and names no session either.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		listen := httptest.NewRecorder()
		next().ServeHTTP(listen, claudeCall(2, "subscriptions/listen", "").http().WithContext(ctx))
		if listen.Code >= 500 || listen.Header().Get("Mcp-Session-Id") != "" {
			t.Fatalf("listen %d %v", listen.Code, listen.Header())
		}

		list := decode(t, mustNoSession(t, do(next(), claudeCall(3, "tools/list", ""))))
		if got := toolNamesOf(t, list.Result); !slices.Equal(got, []string{"connection_info", "get_attachment", "read_message", "search_mail"}) {
			t.Fatalf("tools %v", got)
		}
		for i, call := range []string{
			`"name":"connection_info","arguments":{}`,
			`"name":"search_mail","arguments":{}`,
			`"name":"read_message","arguments":{"id":"mbx_A.msg_1"}`,
		} {
			res := decode(t, mustNoSession(t, do(next(), claudeCall(4+i, "tools/call", call))))
			var r toolResult
			if err := json.Unmarshal(res.Result, &r); err != nil || r.IsError || len(r.StructuredContent) == 0 {
				t.Fatalf("call %d: %v %s", i, err, res.Result)
			}
		}
	})

	t.Run("codex, 2025-06-18", func(t *testing.T) {
		init := request{token: tokenMCP, body: `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{}},"clientInfo":{"name":"codex-mcp-client","title":"Codex","version":"0.169.0"}}}`}
		res := decode(t, mustNoSession(t, do(next(), init)))
		var i struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
		}
		if err := json.Unmarshal(res.Result, &i); err != nil || i.ProtocolVersion != protocolCodex || i.Instructions != mcpapi.Instructions {
			t.Fatalf("initialize %v %s", err, res.Result)
		}
		initialized := mustNoSession(t, do(next(), request{token: tokenMCP, protocol: protocolCodex, body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`}))
		if initialized.Code != http.StatusAccepted {
			t.Fatalf("initialized %d", initialized.Code)
		}
		decode(t, mustNoSession(t, do(next(), codexCall(1, "tools/list", ""))))
		for id, q := range []request{
			toolCall(2, "connection_info", `{}`),
			toolCall(3, "search_mail", `{"limit":5}`),
			toolCall(4, "read_message", `{"id":"mbx_A.msg_1"}`),
		} {
			r := decode(t, mustNoSession(t, do(next(), q)))
			var tr toolResult
			if err := json.Unmarshal(r.Result, &tr); err != nil || tr.IsError {
				t.Fatalf("call %d: %v %s", id, err, r.Result)
			}
		}
	})

	t.Run("a session a client sends is not needed nor kept", func(t *testing.T) {
		q := toolCall(9, "read_message", `{"id":"mbx_A.msg_1"}`)
		r := q.http()
		r.Header.Set("Mcp-Session-Id", "a-session-from-elsewhere")
		rec := httptest.NewRecorder()
		next().ServeHTTP(rec, r)
		decode(t, mustNoSession(t, rec))
	})

	t.Run("only POST: GET and DELETE are 405 after the token", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodDelete} {
			r := httptest.NewRequest(m, "https://mcp.permaily.test/mcp", nil)
			rec := httptest.NewRecorder()
			next().ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s without a token: %d", m, rec.Code)
			}
			r.Header.Set("Authorization", "Bearer "+tokenMCP)
			rec = httptest.NewRecorder()
			next().ServeHTTP(rec, r)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s: %d", m, rec.Code)
			}
		}
	})
}

func mustNoSession(t *testing.T, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Header().Get("Mcp-Session-Id") != "" {
		t.Fatal("a stateless answer named a session")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	return rec
}

func toolNamesOf(t *testing.T, result json.RawMessage) []string {
	t.Helper()
	var l struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &l); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range l.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// The token is checked before anything (ADR 0030, "Checking a token" and
// "Errors and challenges"): without one, or with one that is not a bearer
// in the header, 401 with the challenge; one the service refuses, or one of
// the API (V51), 401 invalid_token; a check that could not read the storage
// 503 with Retry-After and never 401; and the SDK, the tools and the
// service's reads never run.
func TestTheTokenIsCheckedBeforeAnything(t *testing.T) {
	const challenge = `Bearer resource_metadata="` + metadataURL + `", scope="mail.sender mail.subject mail.body"`
	const refused = `Bearer error="invalid_token", resource_metadata="` + metadataURL + `", scope="mail.sender mail.subject mail.body"`
	expired := grant(scopesDefault...)
	expired.TokenExpiresAt = time.Now().Add(-time.Second)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`
	for _, c := range []struct {
		name      string
		header    string
		url       string
		status    int
		challenge string
	}{
		{"no token", "", "", http.StatusUnauthorized, challenge},
		{"another scheme", "Basic " + tokenMCP, "", http.StatusUnauthorized, challenge},
		{"a scheme alone", "Bearer", "", http.StatusUnauthorized, challenge},
		{"an empty token", "Bearer ", "", http.StatusUnauthorized, challenge},
		{"two tokens", "Bearer " + tokenMCP + " " + tokenMCP, "", http.StatusUnauthorized, challenge},
		{"a token in the query", "", "?access_token=" + tokenMCP, http.StatusUnauthorized, challenge},
		{"a token refused", "Bearer " + tokenBad, "", http.StatusUnauthorized, refused},
		{"the API's token", "Bearer " + tokenAPI, "", http.StatusUnauthorized, refused},
		{"an expired grant", "Bearer expired", "", http.StatusUnauthorized, refused},
		{"the storage down", "Bearer " + tokenDown, "", http.StatusServiceUnavailable, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			check := w.fake.CheckFunc
			var checked []contracts.Resource
			w.fake.CheckFunc = func(ctx context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error) {
				checked = append(checked, r)
				if bearer == "expired" {
					return expired, nil
				}
				return check(ctx, r, bearer)
			}
			r := httptest.NewRequest(http.MethodPost, "https://mcp.permaily.test/mcp"+c.url, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			w.handler().ServeHTTP(rec, r)
			if rec.Code != c.status || rec.Header().Get("WWW-Authenticate") != c.challenge {
				t.Fatalf("%d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
			}
			if c.status == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") != "5" {
				t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
			}
			for _, r := range checked {
				if r != contracts.ResourceMCP {
					t.Fatalf("checked for %q", r)
				}
			}
			if calls := w.fake.Calls(); slices.Contains(calls, "Read") {
				t.Fatalf("the service read for a refused token: %v", calls)
			}
			var e struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error.Code == "" || strings.Contains(rec.Body.String(), "mbx_A") {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}

func TestOnlyTheMCPPathIsServed(t *testing.T) {
	w := newWorld()
	for _, path := range []string{"/", "/mcp/", "/mcpx", "/v1/connection", "/.well-known/oauth-protected-resource/mcp"} {
		r := httptest.NewRequest(http.MethodPost, "https://mcp.permaily.test"+path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+tokenMCP)
		rec := httptest.NewRecorder()
		w.handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	if calls := w.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the service was called: %v", calls)
	}
}

// The resource's metadata URL in every challenge is RFC 9728's for the
// image's MCP resource, whatever its domain.
func TestTheChallengeNamesTheResourcesMetadata(t *testing.T) {
	w := newWorld()
	c := cfg
	c.MCPResource = "https://mcp.example.org:8443/mcp"
	rec := httptest.NewRecorder()
	mcpapi.New(w.fake, c).ServeHTTP(rec, request{body: "{}"}.http())
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="https://mcp.example.org:8443/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatal(got)
	}
	c.MCPResource = "::not a url"
	rec = httptest.NewRecorder()
	mcpapi.New(w.fake, c).ServeHTTP(rec, request{body: "{}"}.http())
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="::not a url"`) {
		t.Fatal(got)
	}
}

// V34's MCP half: the log holds the method and the tool from fixed sets and
// the outcome by kind; no token, identifier, formula, address, subject or
// text, with hostile values in every field.
func TestTheLogHoldsNoTokenNorMail(t *testing.T) {
	var l logs
	w := newWorld()
	h := w.handler(mcpapi.WithLog(l.logger()), mcpapi.WithLog(nil))
	hostile := `{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"secret-formula-value"}]}]},"cursor":"pmk_secret-cursor"}`
	for _, q := range []request{
		toolCall(1, "connection_info", `{}`),
		toolCall(2, "search_mail", hostile),
		toolCall(3, "read_message", `{"id":"mbx_A.msg_1"}`),
		toolCall(4, "read_message", `{"id":"mbx_secret.id"}`),
		toolCall(5, "get_attachment", `{"id":"mbx_A.msg_1","index":0}`),
		toolCall(6, "a-tool-named-by-the-client", `{}`),
		codexCall(7, "a/method/named/by/the/client", ""),
		{token: tokenBad, body: "{}"},
		{body: "{}"},
	} {
		do(h, q)
	}
	got := l.String()
	for _, secret := range []string{tokenMCP, tokenBad, "secret-formula-value", "pmk_secret", "mbx_A", "mbx_secret", "billing@example.com", subject, "IGNORE", "invoice.pdf", "a-tool-named", "a/method/named", "con_", "acc_test", "claude.ai"} {
		if strings.Contains(got, secret) {
			t.Fatalf("the log holds %q:\n%s", secret, got)
		}
	}
	for _, kind := range []string{`"tool":"search_mail"`, `"tool":"other"`, `"method":"other"`, `"outcome":"step_up"`, `"outcome":"not_found"`, `"outcome":"no_token"`, `"outcome":"invalid_token"`, `"outcome":"answered"`} {
		if !strings.Contains(got, kind) {
			t.Fatalf("the log lacks %s:\n%s", kind, got)
		}
	}
}
