// Package openai is the client of OpenAI's Responses API for a person's
// words (ADR 0040, "Plain words"): the one rulewords.Model the enclave
// builds, an adapter that is given the words, a key and the enclave's egress
// client, and reaches nothing else.
//
// As internal/decide/openai does for decisions, it makes one attempt a
// call, sent once, never tried again by itself or by its transport; follows
// no redirect, keeps no cookie, speaks only HTTP/2, logs nothing, and never
// passes on a word of the provider's. The service of
// internal/services/decisions reserves the attempt before it is sent, from
// the decide.AttemptError each failure is.
package openai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/rulewords"
)

const (
	// Endpoint is the one address a request is sent to.
	Endpoint = "https://api.openai.com/v1/responses"
	// MaxAnswer bounds the body of an answer that is read; a longer one is
	// decide.ErrInvalidAnswer.
	MaxAnswer = 64 << 10
	// CallTimeout is how long one attempt waits for its answer: a person is
	// waiting for it on the rule's screen.
	CallTimeout = 30 * time.Second
)

// Client is the client of OpenAI's Responses API for words. It implements
// rulewords.Model.
type Client struct {
	hc           http.Client
	key          decide.Credential
	org, project string
	timeout      time.Duration
}

var _ rulewords.Model = (*Client)(nil)

// New is the client. key is called for each attempt and returns the key the
// enclave holds, or ok false when there is none to use (an AttemptError of
// ErrKeyRefused, not Sent, and no request). hc is the enclave's egress
// client, which New copies: the copy follows no redirect, keeps no cookie
// and speaks only HTTP/2, and hc is left as it is. org and project are sent
// as the OpenAI-Organization and OpenAI-Project headers; an empty one is
// not sent.
func New(hc *http.Client, key decide.Credential, org, project string) *Client {
	c := &Client{key: key, org: org, project: project, timeout: CallTimeout}
	if hc != nil {
		c.hc = *hc
	}
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.hc.Jar = nil
	c.hc.Transport = http2Only(c.hc.Transport)
	return c
}

// Name is the model asked.
func (c *Client) Name() string { return rulewords.ModelName }

// Hosts are what the client reaches: the same as the Decisions API's.
func Hosts() []string { return []string{"api.openai.com:443"} }

// http2Only is a copy of rt that speaks only HTTP/2 when rt is an
// *http.Transport (nil is http.DefaultTransport), and rt itself otherwise,
// for the reason internal/decide/openai's is: Go's HTTP/1.1 transport logs
// what a server sends on an idle connection.
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

var errNotHTTP2 = errors.New("openai: the provider did not negotiate HTTP/2")

// validKey says whether key can be sent in a header: printable ASCII, no
// space, at most 1 KiB.
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

// Write makes one attempt with CallTimeout, or what is left of ctx's
// deadline when sooner. Words that rulewords.ValidWords refuses are never
// sent (ErrRequest, not Sent). Its errors are *decide.AttemptError; none is
// tried again by the service (ADR 0040: one attempt a call).
func (c *Client) Write(ctx context.Context, words string) (*rulewords.Answer, error) {
	if !rulewords.ValidWords(words) {
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
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var connected atomic.Bool
	actx = httptrace.WithClientTrace(actx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
	req, _ := http.NewRequestWithContext(actx, http.MethodPost, Endpoint, bytes.NewReader(rulewords.Request(words)))
	req.GetBody = nil // sent once
	req.Header = http.Header{
		"Authorization": {"Bearer " + key},
		"Content-Type":  {"application/json"},
		"User-Agent":    {""},
	}
	if c.org != "" {
		req.Header.Set("OpenAI-Organization", c.org)
	}
	if c.project != "" {
		req.Header.Set("OpenAI-Project", c.project)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fail(decide.ErrUnavailable, connected.Load())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswer+1))
	if resp.StatusCode != http.StatusOK {
		return nil, failure(resp.StatusCode, body)
	}
	if err != nil {
		return nil, fail(decide.ErrUnavailable, true)
	}
	if len(body) > MaxAnswer {
		return nil, fail(decide.ErrInvalidAnswer, true)
	}
	a, err := read(body)
	if err != nil {
		return nil, fail(err, true)
	}
	return a, nil
}

// failure is what a status other than 200 means, as for the Decisions API
// (ADR 0028, "Failures"). The body is read only for the code of a 429.
func failure(s int, body []byte) *decide.AttemptError {
	switch {
	case s >= 300 && s < 400:
		return fail(decide.ErrRequest, true)
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return fail(decide.ErrKeyRefused, true)
	case s == http.StatusTooManyRequests:
		if limit(code(body)) {
			return fail(decide.ErrProviderLimit, true)
		}
		return fail(decide.ErrUnavailable, true)
	case s >= 500:
		return fail(decide.ErrUnavailable, true)
	case s >= 400:
		return fail(decide.ErrRequest, true)
	}
	return fail(decide.ErrInvalidAnswer, true)
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

// limit says whether a 429's code is the provider's own limit.
func limit(code string) bool {
	switch code {
	case "credit_balance_exhausted", "insufficient_quota":
		return true
	}
	return strings.HasSuffix(code, "_spend_limit_exceeded") || strings.HasSuffix(code, "_usage_limit_exceeded")
}

// The answer's body: only what is read of it.
type answerBody struct {
	Model  *string `json:"model"`
	Status string  `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage json.RawMessage `json:"usage"`
}

// read checks an answer of 200: JSON naming a model, or
// decide.ErrInvalidAnswer; then naming rulewords.ModelName, or
// decide.ErrModelChanged. A completed answer is the text of its assistant
// message's output_text, or Refused when it holds a refusal; an incomplete
// one (cut at max_output_tokens) is Incomplete; any other status, or a
// completed answer with no text, is ErrInvalidAnswer.
func read(body []byte) (*rulewords.Answer, error) {
	var b answerBody
	if json.Unmarshal(body, &b) != nil || b.Model == nil || *b.Model == "" {
		return nil, decide.ErrInvalidAnswer
	}
	if *b.Model != rulewords.ModelName {
		return nil, decide.ErrModelChanged
	}
	a := &rulewords.Answer{Model: *b.Model, Usage: usage(b.Usage)}
	switch b.Status {
	case "incomplete":
		a.Incomplete = true
		return a, nil
	case "completed":
	default:
		return nil, decide.ErrInvalidAnswer
	}
	texts := 0
	for _, o := range b.Output {
		if o.Type != "message" {
			continue // reasoning
		}
		if o.Role != "assistant" {
			return nil, decide.ErrInvalidAnswer
		}
		for _, c := range o.Content {
			switch c.Type {
			case "output_text":
				a.Text += c.Text
				texts++
			case "refusal":
				a.Refused = true
			}
		}
	}
	if a.Refused {
		a.Text = ""
		return a, nil
	}
	if texts != 1 {
		return nil, decide.ErrInvalidAnswer
	}
	return a, nil
}

// usage is the answer's usage, Reported only when it holds the input and
// the output tokens, every count a whole number from 0 to 2^40.
func usage(raw json.RawMessage) rulewords.Usage {
	var u struct {
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		Details      *struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &u) != nil || u.InputTokens == nil || u.OutputTokens == nil {
		return rulewords.Usage{}
	}
	out := rulewords.Usage{InputTokens: *u.InputTokens, OutputTokens: *u.OutputTokens, Reported: true}
	if u.Details != nil {
		out.CachedTokens = u.Details.Cached
	}
	for _, v := range []int64{out.InputTokens, out.CachedTokens, out.OutputTokens} {
		if v < 0 || v > 1<<40 {
			return rulewords.Usage{}
		}
	}
	return out
}
