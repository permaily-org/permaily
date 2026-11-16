package decidetest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
)

// Host is the name the played provider's certificate is for: the real
// client's compiled address.
const Host = "api.openai.com"

// Provider is a played OpenAI Decisions API: an HTTPS server, with a
// certificate for Host, that checks each request as the wire format of ADR
// 0028 ("What is sent") says and answers from a Table. Every connection of
// its Client goes to it, whatever address is asked, so the real client
// talks to it at its compiled address.
type Provider struct {
	tab    *Table
	srv    *httptest.Server
	client *http.Client
	pool   *x509.CertPool
	addr   string

	mu       sync.Mutex
	requests []Request
	dials    int
	resets   []Failure // the HTTP/2 failures waiting for their stream's reset
}

// Request is one request the played provider received, as it came, its
// body read whole.
type Request struct {
	Method, Host, Path string
	// Proto is "HTTP/1.1" or "HTTP/2.0".
	Proto  string
	Header http.Header
	Body   []byte
}

// NewProvider starts a played provider answering from tab, stopped when the
// test ends. It speaks HTTP/2 and HTTP/1.1, as the client's TLS
// negotiates (ALPN "h2" or "http/1.1"), and on RefusedStream, ResetStream
// and GoAway it ends an HTTP/2 stream with the frame each names once the
// request's body was read whole; those three are for one attempt at a
// time, as the frame goes to the next stream the server aborts.
func NewProvider(tb testing.TB, tab *Table) *Provider {
	tb.Helper()
	cert, pool := certificate(tb)
	p := &Provider{tab: tab, pool: pool}
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	// TLS is the listener's, so the server reads plain HTTP/1.1 or HTTP/2
	// (with prior knowledge) and its frames pass through an h2Conn.
	p.srv.Config.Protocols = new(http.Protocols)
	p.srv.Config.Protocols.SetHTTP1(true)
	p.srv.Config.Protocols.SetUnencryptedHTTP2(true)
	p.srv.Listener = &h2Listener{
		Listener: p.srv.Listener,
		tls:      &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}},
		p:        p,
	}
	p.srv.Start()
	tb.Cleanup(p.srv.Close)
	p.addr = p.srv.Listener.Addr().String()
	tr := &http.Transport{
		DialContext:       p.DialContext,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true, // as the enclave's egress transport
	}
	tb.Cleanup(tr.CloseIdleConnections)
	p.client = &http.Client{Transport: tr}
	return p
}

// Client is an HTTP client whose every connection goes to the played
// provider, over TLS checked against its certificate for Host: what the
// enclave's egress client is to the real one.
func (p *Provider) Client() *http.Client { return p.client }

// DialContext opens a connection to the played provider, whatever address
// is asked, and counts it in Dials: for a transport of the test's own,
// such as a copy of the one enclave.EgressClient builds.
func (p *Provider) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	p.mu.Lock()
	p.dials++
	p.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, p.addr)
}

// RootCAs is a pool that trusts the played provider's certificate for
// Host.
func (p *Provider) RootCAs() *x509.CertPool { return p.pool }

// Requests are the requests received, in the order they came.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

// Dials is how many connections the client opened.
func (p *Provider) Dials() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dials
}

// The request's body, read strictly: the four fields and no other.
type wireRequest struct {
	Model string `json:"model"`
	Input []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"input"`
	Questions []struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
	} `json:"questions"`
	PromptCacheOptions *struct {
		Mode string `json:"mode"`
	} `json:"prompt_cache_options"`
}

type wirePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// call reads a request as the Decisions API would, or says it is not one
// the real client sends.
func call(body []byte) (decide.Call, bool) {
	var r wireRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.More() || r.Model != Name || len(r.Input) != 2 || r.PromptCacheOptions == nil ||
		r.PromptCacheOptions.Mode != "explicit" || len(r.Questions) == 0 {
		return decide.Call{}, false
	}
	var developer string
	if r.Input[0].Role != "developer" || json.Unmarshal(r.Input[0].Content, &developer) != nil || developer != decide.Developer {
		return decide.Call{}, false
	}
	var parts []wirePart
	pd := json.NewDecoder(bytes.NewReader(r.Input[1].Content))
	pd.DisallowUnknownFields()
	if r.Input[1].Role != "user" || pd.Decode(&parts) != nil || len(parts) != 1 || parts[0].Type != "input_text" {
		return decide.Call{}, false
	}
	var content struct {
		Subject *string `json:"subject"`
		Body    *string `json:"body"`
	}
	cd := json.NewDecoder(bytes.NewReader([]byte(parts[0].Text)))
	cd.DisallowUnknownFields()
	if cd.Decode(&content) != nil || content.Subject == nil {
		return decide.Call{}, false
	}
	c := decide.Call{Content: decide.Content{Kind: decide.ContentSubject, Subject: *content.Subject}}
	if content.Body != nil {
		c.Content.Kind, c.Content.Body = decide.ContentMessage, *content.Body
	}
	if string(decide.ContentJSON(c.Content)) != parts[0].Text {
		return decide.Call{}, false // not the bytes decide writes
	}
	for i, q := range r.Questions {
		a, ok := AtomOf(q.Instructions)
		if q.Type != "predicate" || q.Name != "s"+strconv.Itoa(i) || !ok {
			return decide.Call{}, false
		}
		c.Statements = append(c.Statements, a)
	}
	return c, sendable(c)
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	p.mu.Lock()
	p.requests = append(p.requests, Request{Method: r.Method, Host: r.Host, Path: r.URL.Path, Proto: r.Proto, Header: r.Header.Clone(), Body: body})
	p.mu.Unlock()

	const bearer = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(bearer) || auth[:len(bearer)] != bearer {
		writeError(w, http.StatusUnauthorized, "missing_api_key")
		return
	}
	c, ok := call(body)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/decisions" || r.Header.Get("Content-Type") != "application/json" || !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	o := p.tab.attempt(c, auth[len(bearer):])
	if o.failure == Late {
		select { // answer once the client has gone
		case <-r.Context().Done():
		case <-time.After(time.Minute):
		}
	} else if o.delay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(o.delay):
		}
	}
	retry := func() {
		if o.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.FormatFloat(o.retryAfter.Seconds(), 'f', -1, 64))
		}
	}
	switch o.failure {
	case Unavailable:
		retry()
		writeError(w, http.StatusServiceUnavailable, "server_error")
	case SlowDown:
		retry()
		writeError(w, http.StatusTooManyRequests, "slow_down")
	case KeyRefused:
		writeError(w, http.StatusUnauthorized, "invalid_api_key")
	case ProviderLimit:
		writeError(w, http.StatusTooManyRequests, "project_spend_limit_exceeded")
	case BadRequest:
		writeError(w, http.StatusBadRequest, "invalid_request")
	case Redirect:
		http.Redirect(w, r, "https://elsewhere.example/v1/decisions", http.StatusTemporaryRedirect)
	case RefusedStream, ResetStream, GoAway:
		if r.ProtoMajor == 2 {
			p.mu.Lock()
			p.resets = append(p.resets, o.failure)
			p.mu.Unlock()
		}
		panic(http.ErrAbortHandler) // HTTP/2: the reset h2Conn turns into this failure; HTTP/1.1: as Drop
	case Drop:
		panic(http.ErrAbortHandler) // the connection closed, with no answer (HTTP/2: RST_STREAM INTERNAL_ERROR)
	case Invalid:
		writeAnswer(w, Name, o, true)
	case OtherModel:
		writeAnswer(w, AnotherModel, o, false)
	default:
		writeAnswer(w, Name, o, false)
	}
}

// writeAnswer writes a 200 as the Decisions API does; extra, with one
// answer more than the call's statements. Every answer is valid JSON: a
// value that is not a probability is the number when it is finite, null
// for NaN and a string for an infinity, each of which the real client
// reads as no answer for that statement alone.
func writeAnswer(w http.ResponseWriter, model string, o outcome, extra bool) {
	type item struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Probability json.RawMessage `json:"probability,omitempty"`
	}
	answers := o.answers
	if extra {
		answers = append(answers, reply{})
	}
	items := make([]item, len(answers))
	for i, a := range answers {
		items[i] = item{Type: "predicate", Name: "s" + strconv.Itoa(i)}
		switch {
		case a.refused:
			items[i].Type = "refusal"
		case a.missing:
		default:
			items[i].Probability = probability(a.p)
		}
	}
	out := map[string]any{"model": model, "answers": items}
	if o.usage.Reported {
		out["usage"] = map[string]any{
			"input_tokens":         o.usage.InputTokens,
			"input_tokens_details": map[string]int64{"cached_tokens": 0, "cache_write_tokens": 0},
			"output_tokens":        0,
			"total_tokens":         o.usage.InputTokens,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// probability is p as JSON: the number, or for a value JSON has no number
// for, null (NaN) or a string (an infinity).
func probability(p float64) json.RawMessage {
	switch {
	case math.IsNaN(p):
		return json.RawMessage("null")
	case math.IsInf(p, 1):
		return json.RawMessage(`"Infinity"`)
	case math.IsInf(p, -1):
		return json.RawMessage(`"-Infinity"`)
	}
	b, _ := json.Marshal(p) // finite: it cannot fail
	return b
}

// writeError writes an error as the Decisions API does, with words that
// echo nothing: the client must not read them anyway.
func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "played provider: " + code, "type": "error", "code": code}})
}

// certificate is a self-signed certificate for Host, and a pool that
// trusts it.
func certificate(tb testing.TB) (tls.Certificate, *x509.CertPool) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: Host},
		DNSNames:     []string{Host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,

		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// h2Listener accepts the played provider's TLS connections itself, so the
// server reads and writes plain HTTP through an h2Conn.
type h2Listener struct {
	net.Listener
	tls *tls.Config
	p   *Provider
}

func (l *h2Listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &h2Conn{Conn: tls.Server(c, l.tls), p: l.p}, nil
}

// The HTTP/2 frames h2Conn reads or writes (RFC 9113, section 6).
const (
	frameHeader      = 9
	frameRSTStream   = 0x3
	frameGoAway      = 0x7
	codeNo           = 0x0
	codeProtocol     = 0x1
	codeInternal     = 0x2
	codeRefused      = 0x7
	rstStreamPayload = 4
)

// h2Conn is the server's side of a connection. Over HTTP/1.1 it is the TLS
// connection as it is. Over HTTP/2 every frame the server writes passes as
// it is, but for the reset of an aborted answer (RST_STREAM
// INTERNAL_ERROR) when the provider has a failure waiting, which becomes
// that failure's frame.
type h2Conn struct {
	net.Conn
	p *Provider

	mu     sync.Mutex
	out    []byte // what was written and is not yet a whole frame
	closed bool
}

func (c *h2Conn) Write(b []byte) (int, error) {
	if tc, ok := c.Conn.(*tls.Conn); ok && tc.ConnectionState().NegotiatedProtocol != "h2" {
		return c.Conn.Write(b)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.out = append(c.out, b...) // c.out is the conn's own: b is never changed
	for len(c.out) >= frameHeader {
		n := frameHeader + (int(c.out[0])<<16 | int(c.out[1])<<8 | int(c.out[2]))
		if len(c.out) < n {
			break
		}
		frame := c.out[:n:n]
		c.out = c.out[n:]
		if frame[3] == frameRSTStream && n == frameHeader+rstStreamPayload && binary.BigEndian.Uint32(frame[frameHeader:]) == codeInternal {
			switch f, _ := c.p.nextReset(); f {
			case GoAway:
				return len(b), c.goAway(frame)
			case RefusedStream:
				binary.BigEndian.PutUint32(frame[frameHeader:], codeRefused)
			case ResetStream:
				binary.BigEndian.PutUint32(frame[frameHeader:], codeProtocol)
			}
		}
		if _, err := c.Conn.Write(frame); err != nil {
			return 0, err
		}
	}
	c.out = append([]byte(nil), c.out...)
	return len(b), nil
}

// goAway writes, in place of the reset frame, a GOAWAY naming no stream
// from the reset one on as processed, and closes the connection.
func (c *h2Conn) goAway(reset []byte) error {
	stream := binary.BigEndian.Uint32(reset[5:frameHeader]) & 0x7fffffff
	frame := []byte{0, 0, 8, frameGoAway, 0, 0, 0, 0, 0}
	frame = binary.BigEndian.AppendUint32(frame, stream-1)
	frame = binary.BigEndian.AppendUint32(frame, codeNo)
	_, err := c.Conn.Write(frame)
	c.closed = true
	_ = c.Close()
	return err
}

// nextReset is the failure waiting for the next aborted stream, if any.
func (p *Provider) nextReset() (Failure, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.resets) == 0 {
		return 0, false
	}
	f := p.resets[0]
	p.resets = p.resets[1:]
	return f, true
}
