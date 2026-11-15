// Package openai is the client of OpenAI's Decisions API (ADR 0028, "The
// request"): the one decide.Model the enclave builds, an adapter that is
// given a call, a key and the enclave's egress client, and reaches nothing
// else (depguard model-client-sees-no-mail-store).
//
// It makes one attempt a call, sent once: it never tries again by itself,
// and its body cannot be rewound, so the transport cannot send it again
// either. The service of internal/services/decisions reserves every
// attempt before it is sent and decides whether to try again, from the
// decide.AttemptError each failure is. It follows no redirect, keeps no
// cookie, speaks only HTTP/2 (Go's HTTP/1.1 transport logs what a server
// sends on an idle connection), logs nothing, and never passes on a word of
// the provider's: an error's text may echo the key or the request.
package openai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
)

// What the image calls, compiled in and shown on /status (ADR 0028, "What
// /status and permaily-verify show").
const (
	// Endpoint is the one address a request is sent to.
	Endpoint = "https://api.openai.com/v1/decisions"
	// Model is the model asked and the one every answer must name.
	Model = "gpt-6-luna"
	// MaxAnswer bounds the body of an answer that is read; a longer one is
	// decide.ErrInvalidAnswer.
	MaxAnswer = 64 << 10
)

// Client is the client of OpenAI's Decisions API. It implements
// decide.Model.
type Client struct {
	hc           http.Client
	key          decide.Credential
	org, project string
	// The attempt's timeouts: decide.CallTimeout and
	// decide.LongCallTimeout, shortened only by tests.
	callTimeout, longTimeout time.Duration
}

var _ decide.Model = (*Client)(nil)

// New is the client of OpenAI's Decisions API. key is called for each
// attempt and returns the key the enclave holds, or ok false when there is
// none to use (an AttemptError of ErrKeyRefused, not Sent, and no request).
// hc is the enclave's egress client, which New copies: the copy follows no
// redirect (CheckRedirect answers http.ErrUseLastResponse; a 3xx is
// ErrRequest, Sent, never retried), keeps no cookie (Jar nil) and speaks
// only HTTP/2 (http2Only), and hc is left as it is.
//
// org and project are sent as the OpenAI-Organization and OpenAI-Project
// headers, so a key of another organisation or project is refused rather
// than used (ADR 0028, "The key"); an empty one is not sent.
func New(hc *http.Client, key decide.Credential, org, project string) *Client {
	c := &Client{key: key, org: org, project: project, callTimeout: decide.CallTimeout, longTimeout: decide.LongCallTimeout}
	if hc != nil {
		c.hc = *hc
	}
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.hc.Jar = nil
	c.hc.Transport = http2Only(c.hc.Transport)
	return c
}

// http2Only is a copy of rt that speaks only HTTP/2 when rt is an
// *http.Transport (nil is http.DefaultTransport), and rt itself otherwise.
// Go's HTTP/1.1 transport reads an idle connection and logs what it finds
// there ("Unsolicited response received on idle HTTP channel"), so a
// provider's bytes, its echo of the key or of a message included, would
// reach the log; its HTTP/2 transport logs no byte of the peer's. A
// provider that offers only HTTP/1.1, or no protocol at all, is not asked
// (ErrUnavailable, not Sent). The copy keeps its connections for the next call; closing them
// instead (Request.Close, DisableKeepAlives) costs a TLS handshake a call
// and, with a body that cannot be rewound, fails some attempts made at
// once before they are sent.
func http2Only(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	tr, ok := rt.(*http.Transport)
	if !ok {
		return rt
	}
	tr = tr.Clone()
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	// The client offers only h2, but a TLS server may answer with no
	// protocol at all, and the transport then speaks HTTP/1.1: a handshake
	// that did not settle on h2 is refused before a byte of HTTP is written.
	// Clone gave the copy a TLSClientConfig of its own (it makes one when
	// the transport given has none), so the transport given keeps its own.
	verify := tr.TLSClientConfig.VerifyConnection
	tr.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
		if verify != nil {
			if err := verify(cs); err != nil {
				return err
			}
		}
		if cs.NegotiatedProtocol != "h2" {
			return errNotHTTP2
		}
		return nil
	}
	return tr
}

// errNotHTTP2 refuses a connection that did not negotiate HTTP/2.
var errNotHTTP2 = errors.New("openai: the provider did not negotiate HTTP/2")

// Name is the model as receipts and kept decisions name it.
func (c *Client) Name() string { return Model }

// Hosts are what the client reaches: "api.openai.com:443". The host's
// allowlist holds them (cmd/permaily-host, TestEgressAllowsTheModel).
func Hosts() []string { return []string{"api.openai.com:443"} }

// The request's body (ADR 0028, "What is sent"): these four fields and no
// other.
type request struct {
	Model              string       `json:"model"`
	Input              []message    `json:"input"`
	Questions          []question   `json:"questions"`
	PromptCacheOptions cacheOptions `json:"prompt_cache_options"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // a string for the developer, parts for the user
}

type part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type question struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

// cacheOptions turns the prompt cache off: explicit, with no breakpoint.
type cacheOptions struct {
	Mode string `json:"mode"`
}

// body is the request for call, or false for a call that must not be sent:
// no statement, more than decide.MaxStatementsPerCall, a statement a model
// does not decide, or one on another content than the call's (which would
// send a field the statement does not need).
func body(call decide.Call) ([]byte, bool) {
	n := len(call.Statements)
	if n == 0 || n > decide.MaxStatementsPerCall {
		return nil, false
	}
	if call.Content.Kind != decide.ContentSubject && call.Content.Kind != decide.ContentMessage {
		return nil, false
	}
	qs := make([]question, n)
	for i, a := range call.Statements {
		text, ok := decide.Instructions(a)
		if !ok || decide.KindOf(a) != call.Content.Kind {
			return nil, false
		}
		qs[i] = question{Type: "predicate", Name: name(i), Instructions: text}
	}
	r := request{
		Model: Model,
		Input: []message{
			{Role: "developer", Content: decide.Developer},
			{Role: "user", Content: []part{{Type: "input_text", Text: string(decide.ContentJSON(call.Content))}}},
		},
		Questions:          qs,
		PromptCacheOptions: cacheOptions{Mode: "explicit"},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r) // every field is a string or a list of them: it cannot fail
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), true
}

// name is a statement's name: its position, never its topic.
func name(i int) string { return "s" + strconv.Itoa(i) }

// validKey says whether key can be sent in a header: printable ASCII, no
// space, at most 1 KiB. Another one is no key.
func validKey(key string) bool {
	if key == "" || len(key) > 1<<10 {
		return false
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] > '~' {
			return false
		}
	}
	return true
}

func fail(err error, sent bool) *decide.AttemptError {
	return &decide.AttemptError{Err: err, Sent: sent}
}

// Decide makes one attempt, with CallTimeout or LongCallTimeout, or what
// is left of ctx's deadline when that is sooner; it never retries. Its
// errors are *decide.AttemptError: an answer naming another model is
// ErrModelChanged, never ErrInvalidAnswer.
func (c *Client) Decide(ctx context.Context, call decide.Call) (*decide.Result, error) {
	payload, ok := body(call)
	if !ok {
		return nil, fail(decide.ErrRequest, false)
	}
	if ctx.Err() != nil {
		return nil, fail(decide.ErrUnavailable, false)
	}
	key, ok := "", false
	if c.key != nil {
		key, ok = c.key(ctx)
	}
	if !ok || !validKey(key) {
		return nil, fail(decide.ErrKeyRefused, false)
	}
	timeout := c.callTimeout
	if len(decide.ContentJSON(call.Content)) > decide.LongContent {
		timeout = c.longTimeout
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Sent: a connection to the provider was had, so the request may have
	// left; before that, only the host's proxy was asked for one.
	var connected atomic.Bool
	actx = httptrace.WithClientTrace(actx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
	// The method and the address are constants that parse.
	req, _ := http.NewRequestWithContext(actx, http.MethodPost, Endpoint, bytes.NewReader(payload))
	// Sent once: with no GetBody the transport cannot send the body again
	// by itself, as Go's HTTP/2 transport would after a REFUSED_STREAM, a
	// PROTOCOL_ERROR or a GOAWAY. Such a failure is ErrUnavailable, Sent,
	// and the service reserves any next attempt.
	req.GetBody = nil
	req.Header = http.Header{
		"Authorization": {"Bearer " + key},
		"Content-Type":  {"application/json"},
		"User-Agent":    {""}, // none: Go would name itself
	}
	if c.org != "" {
		req.Header.Set("OpenAI-Organization", c.org)
	}
	if c.project != "" {
		req.Header.Set("OpenAI-Project", c.project)
	}

	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		// No error of the HTTP client is passed on: it names the address.
		return nil, unavailable(ctx, connected.Load(), 0)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswer+1))
	if resp.StatusCode != http.StatusOK {
		return nil, failure(ctx, resp, answer) // what its status says, whole or not
	}
	if err != nil {
		return nil, unavailable(ctx, true, 0)
	}
	if len(answer) > MaxAnswer {
		return nil, fail(decide.ErrInvalidAnswer, true)
	}
	res, err := read(answer, len(call.Statements))
	if err != nil {
		return nil, fail(err, true)
	}
	res.Took = time.Since(start)
	return res, nil
}

// unavailable is an attempt with no answer: tried again only while the
// caller's context is alive (the attempt's own timeout, a transport error),
// never once the caller has given up.
func unavailable(ctx context.Context, sent bool, after time.Duration) *decide.AttemptError {
	e := &decide.AttemptError{Err: decide.ErrUnavailable, Sent: sent}
	if ctx.Err() == nil {
		e.Retry, e.RetryAfter = true, after
	}
	return e
}

// failure is what a status other than 200 means (ADR 0028, "Failures").
// The body is read only for the code of a 429, never for its words.
func failure(ctx context.Context, resp *http.Response, body []byte) *decide.AttemptError {
	s := resp.StatusCode
	switch {
	case s >= 300 && s < 400:
		return fail(decide.ErrRequest, true) // a redirect is never followed
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return fail(decide.ErrKeyRefused, true)
	case s == http.StatusTooManyRequests:
		if limit(code(body)) {
			return fail(decide.ErrProviderLimit, true)
		}
		return unavailable(ctx, true, retryAfter(resp.Header.Get("Retry-After"), time.Now()))
	case s == http.StatusInternalServerError || s == http.StatusBadGateway || s == http.StatusServiceUnavailable || s == http.StatusGatewayTimeout:
		return unavailable(ctx, true, retryAfter(resp.Header.Get("Retry-After"), time.Now()))
	case s >= 500:
		return fail(decide.ErrUnavailable, true) // another 5xx: no answer, and trying again would not help
	case s >= 400:
		return fail(decide.ErrRequest, true)
	}
	return fail(decide.ErrInvalidAnswer, true) // another 1xx or 2xx
}

// code is an error's code, from {"error": {"code": …}}; empty otherwise.
func code(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Code
}

// limit says whether a 429's code is the provider's own limit, which no
// retry helps (ADR 0028, "Failures"), as opposed to slow_down,
// rate_limit_exceeded or a code not known, which are tried again.
func limit(code string) bool {
	switch code {
	case "credit_balance_exhausted", "insufficient_quota":
		return true
	}
	return strings.HasSuffix(code, "_spend_limit_exceeded") || strings.HasSuffix(code, "_usage_limit_exceeded")
}

// retryAfter is a Retry-After header, in seconds or as a date, bounded by
// decide.MaxRetryAfter; 0 when there is none or it does not read.
func retryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	var d time.Duration
	if s, err := strconv.ParseFloat(h, 64); err == nil {
		if math.IsNaN(s) || s <= 0 {
			return 0
		}
		d = time.Duration(min(s, decide.MaxRetryAfter.Seconds()) * float64(time.Second))
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), decide.MaxRetryAfter)
}

// The answer's body: only what is read of it.
type answerBody struct {
	Model   *string           `json:"model"`
	Answers []json.RawMessage `json:"answers"`
	Usage   json.RawMessage   `json:"usage"`
}

type answerItem struct {
	Type        json.RawMessage `json:"type"`
	Name        string          `json:"name"`
	Probability json.RawMessage `json:"probability"`
}

// read checks an answer of 200 (ADR 0028, "What comes back, and how it is
// checked"): JSON, as many answers as statements, named s0, s1, … in their
// order, and naming a model, or decide.ErrInvalidAnswer; then naming
// Model, or decide.ErrModelChanged. Each answer is then a probability or
// no answer for that statement alone.
func read(body []byte, n int) (*decide.Result, error) {
	var b answerBody
	if json.Unmarshal(body, &b) != nil || len(b.Answers) != n || b.Model == nil || *b.Model == "" {
		return nil, decide.ErrInvalidAnswer
	}
	items := make([]answerItem, n)
	for i, raw := range b.Answers {
		if json.Unmarshal(raw, &items[i]) != nil || items[i].Name != name(i) {
			return nil, decide.ErrInvalidAnswer
		}
	}
	if *b.Model != Model {
		return nil, decide.ErrModelChanged
	}
	res := &decide.Result{Answers: make([]decide.Answer, n), Model: *b.Model, Usage: usage(b.Usage)}
	for i, it := range items {
		var kind string
		_ = json.Unmarshal(it.Type, &kind) // another type, or none, is no answer
		switch kind {
		case "refusal":
			res.Answers[i] = decide.Answer{Refused: true}
		case "predicate":
			var p *float64
			if json.Unmarshal(it.Probability, &p) == nil && p != nil && *p >= 0 && *p <= 1 {
				res.Answers[i] = decide.Answer{P: *p, OK: true}
			}
		}
	}
	return res, nil
}

// usage is the answer's usage, Reported only when it holds the input
// tokens, and every count a whole number from 0 to 2^40.
func usage(raw json.RawMessage) decide.Usage {
	var u struct {
		InputTokens *int64 `json:"input_tokens"`
		Details     *struct {
			Cached  int64 `json:"cached_tokens"`
			Written int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &u) != nil || u.InputTokens == nil {
		return decide.Usage{}
	}
	out := decide.Usage{InputTokens: *u.InputTokens, Reported: true}
	if u.Details != nil {
		out.CachedTokens, out.CacheWriteTokens = u.Details.Cached, u.Details.Written
	}
	for _, v := range []int64{out.InputTokens, out.CachedTokens, out.CacheWriteTokens} {
		if v < 0 || v > 1<<40 {
			return decide.Usage{}
		}
	}
	return out
}
