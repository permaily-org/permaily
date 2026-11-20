package decidetest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/rulewords"
)

// Writer is a played OpenAI Responses API for a person's words (ADR 0040,
// U11 to U13): an HTTPS server speaking HTTP/2 (and HTTP/1.1 unless
// OnlyHTTP1), with a certificate for Host, that records every request as
// it came and answers each with what Reply says. Every connection of its
// Client goes to it, whatever address is asked.
type Writer struct {
	srv    *httptest.Server
	client *http.Client

	mu       sync.Mutex
	requests []Request
	// reply answers one request's words; nil answers a hold on mail from
	// bank.example.
	reply func(words string) WriterReply
}

// WriterReply is how the played Responses API answers one request.
type WriterReply struct {
	// Status is the HTTP status (200 when 0); Code the error's code for
	// another status.
	Status int
	Code   string
	// Text is the assistant message's output_text (hold on bank.example
	// when empty and nothing else is set).
	Text string
	// Refusal answers a refusal; Incomplete an answer cut at
	// max_output_tokens; Model names another model than
	// rulewords.ModelName; Body, when set, is the whole body as it is.
	Refusal    bool
	Incomplete bool
	Model      string
	Body       []byte
	// Usage is what the answer reports; NoUsage reports none.
	InputTokens, CachedTokens, OutputTokens int64
	NoUsage                                 bool
	// Delay holds the answer, until the client gives up when longer than
	// its wait.
	Delay time.Duration
	// Redirect answers 307 to another address.
	Redirect bool
}

// HoldText is the answer a played writer gives by default: a hold on mail
// from bank.example, within the schema.
const HoldText = `{"possible":true,"name":"Bank mail","effect":"hold","criteria":[{"all":[{"any":[{"var":"from_domain","op":"is","value":"bank.example","not":false}]}]}],"removal":null}`

// NewWriter starts a played Responses API, stopped when the test ends.
// onlyHTTP1 makes it offer HTTP/1.1 alone, which the real client refuses.
func NewWriter(tb testing.TB, onlyHTTP1 bool, reply func(words string) WriterReply) *Writer {
	tb.Helper()
	cert, pool := certificate(tb)
	w := &Writer{reply: reply}
	w.srv = httptest.NewUnstartedServer(http.HandlerFunc(w.serve))
	w.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	w.srv.EnableHTTP2 = !onlyHTTP1
	w.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	w.srv.StartTLS()
	tb.Cleanup(w.srv.Close)
	addr := w.srv.Listener.Addr().String()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	tb.Cleanup(tr.CloseIdleConnections)
	w.client = &http.Client{Transport: tr}
	return w
}

// Client is an HTTP client whose every connection goes to the played
// Responses API, over TLS checked against its certificate for Host.
func (w *Writer) Client() *http.Client { return w.client }

// Requests are the requests received, in the order they came.
func (w *Writer) Requests() []Request {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Request(nil), w.requests...)
}

// Words reads the words of a request the real client sent: the body must
// be rulewords.Request of them, byte for byte, or ok is false.
func Words(body []byte) (string, bool) {
	var r struct {
		Input []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Input) != 2 {
		return "", false
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(r.Input[1].Content, &parts) != nil || len(parts) != 1 {
		return "", false
	}
	if !bytes.Equal(body, rulewords.Request(parts[0].Text)) {
		return "", false
	}
	return parts[0].Text, true
}

func (w *Writer) serve(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	w.mu.Lock()
	w.requests = append(w.requests, Request{Method: r.Method, Host: r.Host, Path: r.URL.Path, Proto: r.Proto, Header: r.Header.Clone(), Body: body})
	reply := w.reply
	w.mu.Unlock()
	words, ok := Words(body)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Content-Type") != "application/json" || !ok {
		writeError(rw, http.StatusBadRequest, "invalid_request")
		return
	}
	var o WriterReply
	if reply != nil {
		o = reply(words)
	}
	if o.Delay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(o.Delay):
		}
	}
	switch {
	case o.Redirect:
		http.Redirect(rw, r, "https://elsewhere.example/v1/responses", http.StatusTemporaryRedirect)
		return
	case o.Status != 0 && o.Status != http.StatusOK:
		writeError(rw, o.Status, o.Code)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	if o.Body != nil {
		_, _ = rw.Write(o.Body)
		return
	}
	model := o.Model
	if model == "" {
		model = rulewords.ModelName
	}
	text := o.Text
	if text == "" {
		text = HoldText
	}
	content := []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}}
	if o.Refusal {
		content = []map[string]any{{"type": "refusal", "refusal": "played refusal"}}
	}
	out := map[string]any{
		"object": "response", "model": model, "status": "completed", "store": false,
		"output": []any{
			map[string]any{"type": "reasoning", "summary": []any{}},
			map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": content},
		},
	}
	if o.Incomplete {
		out["status"] = "incomplete"
		out["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		out["output"] = []any{map[string]any{"type": "reasoning", "summary": []any{}}}
	}
	if !o.NoUsage {
		out["usage"] = map[string]any{
			"input_tokens":          o.InputTokens,
			"input_tokens_details":  map[string]int64{"cached_tokens": o.CachedTokens},
			"output_tokens":         o.OutputTokens,
			"output_tokens_details": map[string]int64{"reasoning_tokens": 0},
			"total_tokens":          o.InputTokens + o.OutputTokens,
		}
	}
	_ = json.NewEncoder(rw).Encode(out)
}
