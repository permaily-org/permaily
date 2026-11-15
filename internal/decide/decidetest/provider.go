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
	"encoding/json"
	"io"
	"log"
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

	mu       sync.Mutex
	requests []Request
	dials    int
}

// Request is one request the played provider received, as it came.
type Request struct {
	Method, Host, Path string
	Header             http.Header
	Body               []byte
}

// NewProvider starts a played provider answering from tab, stopped when the
// test ends.
func NewProvider(tb testing.TB, tab *Table) *Provider {
	tb.Helper()
	cert, pool := certificate(tb)
	p := &Provider{tab: tab}
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	p.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	p.srv.StartTLS()
	tb.Cleanup(p.srv.Close)
	addr := p.srv.Listener.Addr().String()
	p.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			p.mu.Lock()
			p.dials++
			p.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	return p
}

// Client is an HTTP client whose every connection goes to the played
// provider, over TLS checked against its certificate for Host: what the
// enclave's egress client is to the real one.
func (p *Provider) Client() *http.Client { return p.client }

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
	p.requests = append(p.requests, Request{Method: r.Method, Host: r.Host, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
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
	case Drop:
		panic(http.ErrAbortHandler) // the connection closed, with no answer
	case Invalid:
		writeAnswer(w, Name, o, true)
	case OtherModel:
		writeAnswer(w, AnotherModel, o, false)
	default:
		writeAnswer(w, Name, o, false)
	}
}

// writeAnswer writes a 200 as the Decisions API does; extra, with one
// answer more than the call's statements.
func writeAnswer(w http.ResponseWriter, model string, o outcome, extra bool) {
	type item struct {
		Type        string   `json:"type"`
		Name        string   `json:"name"`
		Probability *float64 `json:"probability,omitempty"`
	}
	answers := o.answers
	if extra {
		answers = append(answers, decide.Answer{OK: true})
	}
	items := make([]item, len(answers))
	for i, a := range answers {
		items[i] = item{Type: "predicate", Name: "s" + strconv.Itoa(i)}
		if a.Refused {
			items[i].Type = "refusal"
		} else {
			items[i].Probability = &a.P
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
