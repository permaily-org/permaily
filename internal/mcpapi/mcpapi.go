// Package mcpapi serves the remote MCP server, POST https://mcp.<domain>/mcp
// (ADR 0030, "MCP for agents"): the official MCP Go SDK speaks the protocol,
// stateless (no Mcp-Session-Id, nothing kept between requests, so either
// enclave answers any call), behind this package's own checks, which run
// first:
//
//  1. The bearer token, checked by the connections service on every request
//     (connections.API.Check, for contracts.ResourceMCP): none is 401 with
//     the challenge, one not valid or for the API is 401 invalid_token, and
//     a check that could not read the storage is 503 with Retry-After, never
//     401 (Codex drops its tools on a 401).
//  2. The JSON-RPC body, at most BodyMax, read to know which tool is called:
//     a tool that needs a part the grant does not have (get_attachment
//     without mail.attachments) is answered HTTP 403 insufficient_scope,
//     naming every scope the client then needs, before the SDK runs. A batch
//     is refused: one HTTP answer carries at most one tool's mail.
//  3. The SDK, given only what was verified: auth.RequireBearerToken hands
//     the tool the grant (req.Extra.TokenInfo), never the token.
//  4. The answer of a tool call, made by the SDK into memory (at most
//     AnswerMax) and sent only under the lease the service returned with the
//     mail (connections.Release, N2 and the owner's decision 14): Start
//     immediately before the first byte (refused: 503 and nothing of it),
//     every byte under its context, read before each write and each flush,
//     the response aborted, its connection closed, when it ends during the
//     transfer, and Finish once. The connection comes from ConnContext,
//     which the enclosing server must set: over HTTP/2 the stream's own
//     deadline does not stop a frame already handed to the connection, so
//     without it a leased answer over HTTP/2 is refused (503).
//
// Every tool's work with the service keeps the HTTP request's deadline and
// ends when the request does (call.bind), for both protocols.
//
// The tools only read (tools.go). Nothing here logs a token, a formula, an
// identifier or anything of the mail: a request is logged by its method and
// tool from fixed sets, and its outcome by a fixed kind. depguard's
// mcp-sdk-only-in-mcpapi keeps the SDK in this package.
package mcpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
)

const (
	// BodyMax bounds a request's body (ADR 0030, "Errors and challenges"):
	// read whole before the SDK runs, to know which tool is called.
	BodyMax = 1 << 20
	// AnswerMax bounds the answer of one tool call as the SDK makes it: an
	// attachment of contracts.VendorAttachmentMax, once in base64 with its
	// metadata twice, fits (ADR 0030, "MCP for agents"; V50).
	AnswerMax = 14 << 20
	// retryAfter is the Retry-After of a 503, in seconds: the service's
	// freshness, after which the authority is read again.
	retryAfter = "5"
	// sendChunk is how much of an answer is written at a time, so that a
	// lease that ends during the transfer stops it within one chunk.
	sendChunk = 32 << 10
)

// Option configures the handler.
type Option func(*handler)

// WithLog sets the logger that receives one line of fixed kinds per request.
// Without it nothing is logged.
func WithLog(l *slog.Logger) Option {
	return func(h *handler) {
		if l != nil {
			h.log = l
		}
	}
}

// handler is the MCP resource.
type handler struct {
	svc      connections.API
	cfg      oauth.Config
	metadata string // the resource's metadata URL (RFC 9728)
	log      *slog.Logger
	server   *mcp.Server
	sdk      http.Handler
	now      func() time.Time
}

// New returns the handler of oauth.PathMCP on the MCP host, served by svc
// (ADR 0030's pull request 3; connectionstest.Fake in tests) for the
// resource c.MCPResource. The router mounts it on the MCP host; any other
// path is 404.
func New(svc connections.API, c oauth.Config, opts ...Option) http.Handler {
	h := &handler{
		svc:      svc,
		cfg:      c,
		metadata: resourceMetadataURL(c.MCPResource),
		log:      slog.New(slog.DiscardHandler),
		now:      time.Now,
	}
	for _, o := range opts {
		o(h)
	}
	h.server = h.newServer()
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return h.server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The SDK's protection of servers on localhost does not apply: the
		// enclave's listener is vsock and the router dispatches by host, and
		// every request here carries a token checked before the SDK runs
		// (ADR 0030). In dev mode the listener is TCP on localhost with the
		// deployment's host names, which the protection would refuse.
		DisableLocalhostProtection: true,
	})
	h.sdk = auth.RequireBearerToken(verified, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: h.metadata,
		// The expiry was checked before the SDK runs, against the grant's
		// hard end; the SDK's own check only repeats it.
		AllowMissingExpiration: true,
	})(sdk)
	return h
}

// resourceMetadataURL is RFC 9728's URL of a resource's metadata: its
// origin, oauth.PathResourceMetadata, then its path.
func resourceMetadataURL(resource string) string {
	u, err := url.Parse(resource)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return resource
	}
	return u.Scheme + "://" + u.Host + oauth.PathResourceMetadata + u.Path
}

// call is what one request's middleware and its tool share: the grant
// checked, whether the answer is buffered for a lease, and what the tool
// asked of the answer (a release, a step-up, an HTTP status in place of the
// SDK's answer).
type call struct {
	grant    *oauth.Grant
	buffered bool
	// ctx is the HTTP request's context: the SDK hands a tool a context of
	// its own, without the request's deadline or its end.
	ctx context.Context

	mu      sync.Mutex
	release connections.Release
	stepUp  []contracts.Part
	status  int
	outcome string
}

// hold keeps the lease of the tool's answer. A second one, or one for an
// answer that is not buffered, is ended at once and refused: its mail is
// never sent.
func (c *call) hold(r connections.Release) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.buffered || c.release != nil {
		r.Finish(false)
		return errNotBuffered
	}
	c.release = r
	return nil
}

// taken is the lease, the step-up, the status and the outcome asked.
func (c *call) taken() (connections.Release, []contracts.Part, int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release, c.stepUp, c.status, c.outcome
}

// bind is the context of a tool's work with the service: the SDK's ctx,
// with the HTTP request's deadline, ended when either ends.
func (c *call) bind(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.ctx == nil {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(c.ctx, func() { cancel(context.Cause(c.ctx)) })
	end := func() {}
	if d, ok := c.ctx.Deadline(); ok {
		ctx, end = context.WithDeadline(ctx, d)
	}
	return ctx, func() {
		end()
		stop()
		cancel(context.Canceled)
	}
}

// errNotBuffered refuses mail for an answer that would not be sent under
// its lease (the SDK read the body otherwise than the middleware did).
var errNotBuffered = errors.New("mcpapi: the answer is not held for a lease")

type callKey struct{}

// verified is the SDK's token verifier: it only hands the tool what the
// middleware verified, the grant and the call, never the token.
func verified(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
	c, _ := ctx.Value(callKey{}).(*call)
	if c == nil || c.grant == nil {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		Scopes:     slices.Clone(c.grant.Scopes),
		Expiration: c.grant.Expires(),
		Extra:      map[string]any{extraCall: c},
	}, nil
}

const extraCall = "permaily.call"

// rpc is what the middleware reads of a JSON-RPC message.
type rpc struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != oauth.PathMCP {
		writeError(w, http.StatusNotFound, contracts.ErrNotFound.Code, contracts.ErrNotFound.Message)
		return
	}
	method, tool := "", ""
	outcome := "answered"
	defer func() { h.logRequest(method, tool, outcome) }()

	token, ok := bearer(r)
	if !ok {
		outcome = "no_token"
		h.challenge(w, http.StatusUnauthorized, "", oauth.DefaultScopes)
		return
	}
	g, err := h.svc.Check(r.Context(), contracts.ResourceMCP, token)
	switch {
	case err == nil && g != nil:
		if e := g.Expires(); !e.IsZero() && !h.now().Before(e) {
			outcome = "invalid_token"
			h.challenge(w, http.StatusUnauthorized, oauth.ErrInvalidToken, oauth.DefaultScopes)
			return
		}
	case errors.Is(err, connections.ErrInvalidToken), errors.Is(err, connections.ErrWrongResource):
		outcome = "invalid_token"
		h.challenge(w, http.StatusUnauthorized, oauth.ErrInvalidToken, oauth.DefaultScopes)
		return
	default:
		outcome = "unavailable"
		unavailable(w)
		return
	}

	c := &call{grant: g, ctx: r.Context()}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, BodyMax+1))
		switch {
		case err != nil:
			outcome = "body"
			writeError(w, http.StatusBadRequest, "invalid_body", "the request's body could not be read")
			return
		case len(body) > BodyMax:
			outcome = "too_large"
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "a request is at most 1 MiB")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			outcome = "batch"
			writeError(w, http.StatusBadRequest, "invalid_body", "send one JSON-RPC message per request")
			return
		}
		var m rpc
		if json.Unmarshal(body, &m) == nil {
			method = kindOf(m.Method, methods)
			if m.Method == "tools/call" {
				tool = kindOf(m.Params.Name, toolNames)
				c.buffered = true
				if missing := missingParts(g, m.Params.Name); len(missing) > 0 {
					outcome = "step_up"
					h.stepUp(w, g, missing)
					return
				}
			}
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), callKey{}, c))
	if !c.buffered {
		h.sdk.ServeHTTP(&noStore{ResponseWriter: w}, r)
		return
	}
	rec := newRecorder(AnswerMax)
	h.sdk.ServeHTTP(rec, r)
	var abort bool
	if outcome, abort = h.answer(w, r, c, rec); abort {
		panic(http.ErrAbortHandler) // after outcome is set: logged as it was
	}
}

// noStore is the SDK's writer for an answer that holds no mail (anything
// but a tool call): it is sent as the SDK writes it, with Cache-Control:
// no-store in place of the SDK's.
type noStore struct {
	http.ResponseWriter
	set bool
}

func (n *noStore) header() {
	if !n.set {
		n.set = true
		n.Header().Set("Cache-Control", "no-store")
	}
}

func (n *noStore) WriteHeader(status int) {
	n.header()
	n.ResponseWriter.WriteHeader(status)
}

func (n *noStore) Write(p []byte) (int, error) {
	n.header()
	return n.ResponseWriter.Write(p)
}

// Flush lets the SDK send a stream as it writes it.
func (n *noStore) Flush() {
	n.header()
	_ = http.NewResponseController(n.ResponseWriter).Flush()
}

// Unwrap gives http.ResponseController the connection's writer.
func (n *noStore) Unwrap() http.ResponseWriter { return n.ResponseWriter }

// bearer is the token of an Authorization header "Bearer <token>" (RFC
// 6750: the scheme in any case), and only there: never a query or a body.
func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// missingParts is what the tool needs that the grant's effective parts do
// not give.
func missingParts(g *oauth.Grant, tool string) []contracts.Part {
	var missing []contracts.Part
	for _, p := range toolParts[tool] {
		if !slices.Contains(g.Parts, p) {
			missing = append(missing, p)
		}
	}
	return missing
}

// challenge answers 401 with the resource's challenge: the metadata and the
// scopes a client asks for, and code when the token was refused (ADR 0030,
// "Errors and challenges").
func (h *handler) challenge(w http.ResponseWriter, status int, code oauth.ErrorCode, scopes []string) {
	params := []string{fmt.Sprintf("resource_metadata=%q", h.metadata), fmt.Sprintf("scope=%q", strings.Join(scopes, " "))}
	if code != "" {
		params = append([]string{fmt.Sprintf("error=%q", string(code))}, params...)
	}
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	message := contracts.ErrUnauthorized.Message
	if code != "" {
		message = "that token can't be used; sign in again"
	}
	writeError(w, status, string(oauthCode(code)), message)
}

func oauthCode(code oauth.ErrorCode) oauth.ErrorCode {
	if code == "" {
		return "unauthorized"
	}
	return code
}

// stepUp answers 403 insufficient_scope naming, in one challenge, every
// scope the client then needs: the token's own and the missing parts'.
func (h *handler) stepUp(w http.ResponseWriter, g *oauth.Grant, missing []contracts.Part) {
	var scopes []string
	for _, sp := range oauth.ScopeParts {
		if sp.Part == contracts.PartSender || slices.Contains(g.Scopes, sp.Scope) || slices.Contains(missing, sp.Part) {
			scopes = append(scopes, sp.Scope)
		}
	}
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer error=%q, scope=%q, resource_metadata=%q",
		string(oauth.ErrInsufficientScope), strings.Join(scopes, " "), h.metadata))
	writeError(w, http.StatusForbidden, string(oauth.ErrInsufficientScope), "this tool needs a permission the connection does not have; connect again to ask for it")
}

// unavailable answers 503 with Retry-After: the storage could not be read,
// or the answer could no longer be released. Never 401, so a client keeps
// its tokens.
func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfter)
	writeError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Permaily cannot answer now; try again shortly")
}

// errorBody is the HTTP error answered before or instead of the SDK: the
// API's shape (ADR 0030, "The API for apps").
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e errorBody
	e.Error.Code, e.Error.Message = code, message
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// methods are the JSON-RPC methods logged by name; any other is "other".
var methods = []string{
	"initialize", "notifications/initialized", "ping", "server/discover",
	"subscriptions/listen", "tools/list", "tools/call",
}

// kindOf is s when it is one of known, else "other": nothing a client
// writes reaches a log.
func kindOf(s string, known []string) string {
	if slices.Contains(known, s) {
		return s
	}
	return "other"
}

func (h *handler) logRequest(method, tool, outcome string) {
	attrs := []slog.Attr{slog.String("outcome", outcome)}
	if method != "" {
		attrs = append(attrs, slog.String("method", method))
	}
	if tool != "" {
		attrs = append(attrs, slog.String("tool", tool))
	}
	h.log.LogAttrs(context.Background(), slog.LevelInfo, "mcp request", attrs...)
}
