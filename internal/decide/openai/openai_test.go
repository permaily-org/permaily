package openai

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/enclave"
	"github.com/permaily-org/permaily/internal/query"
)

const testKey = "sk-proj-test-0123456789"

func key(k string) decide.Credential {
	return func(context.Context) (string, bool) { return k, k != "" }
}

var (
	subjectInvoices = query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}
	aboutInvoices   = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "invoices"}
	mentionsHome    = query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: `a "home" address\`}

	subjectCall = decide.Call{
		Content:    decide.Content{Kind: decide.ContentSubject, Subject: "Invoice 12"},
		Statements: []query.Atom{subjectInvoices},
	}
	messageCall = decide.Call{
		Content:    decide.Content{Kind: decide.ContentMessage, Subject: "Invoice 12", Body: "Please pay \"invoice\" 12.\nThanks <b>é</b>"},
		Statements: []query.Atom{aboutInvoices, mentionsHome},
	}
)

// played is a transport that answers in place of the provider and records
// what it was asked. It says it had a connection, as a real transport does
// once it has one, unless offline.
type played struct {
	mu      sync.Mutex
	urls    []string
	headers []http.Header
	bodies  [][]byte
	rewinds []bool // a Request.GetBody, so a transport could send the body again
	offline bool
	answer  func(r *http.Request) (*http.Response, error)
}

func (p *played) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr := httptrace.ContextClientTrace(r.Context()); tr != nil && tr.GotConn != nil && !p.offline {
		tr.GotConn(httptrace.GotConnInfo{})
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	p.mu.Lock()
	p.urls = append(p.urls, r.URL.String())
	p.headers = append(p.headers, r.Header.Clone())
	p.bodies = append(p.bodies, body)
	p.rewinds = append(p.rewinds, r.GetBody != nil)
	p.mu.Unlock()
	if p.answer == nil {
		return respond(r, 200, nil, ok(len(questions(body)))), nil
	}
	return p.answer(r)
}

func (p *played) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.urls)
}

func respond(r *http.Request, status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

// ok is an answer of n statements, each 0.5.
func ok(n int) string {
	var items []string
	for i := range n {
		items = append(items, `{"type":"predicate","name":"`+name(i)+`","probability":0.5}`)
	}
	return `{"model":"gpt-6-luna","answers":[` + strings.Join(items, ",") + `],"usage":{"input_tokens":1390,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`
}

func questions(body []byte) []any {
	var r struct{ Questions []any }
	_ = json.Unmarshal(body, &r)
	return r.Questions
}

func newClient(p *played) *Client {
	return New(&http.Client{Transport: p}, key(testKey), "org-test", "proj_test")
}

// A1: the request, golden: the exact body the re-measure's client wrote,
// the headers and nothing else.
func TestA1TheRequestIsGolden(t *testing.T) {
	for _, c := range []struct {
		name   string
		call   decide.Call
		golden string
	}{
		{"subject", subjectCall, goldenSubject},
		{"message", messageCall, goldenMessage},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &played{}
			if _, err := newClient(p).Decide(context.Background(), c.call); err != nil {
				t.Fatal(err)
			}
			if p.urls[0] != Endpoint {
				t.Errorf("sent to %s", p.urls[0])
			}
			if string(p.bodies[0]) != c.golden {
				t.Errorf("body:\n%s\nwant:\n%s", p.bodies[0], c.golden)
			}
			want := http.Header{
				"Authorization":       {"Bearer " + testKey},
				"Content-Type":        {"application/json"},
				"Openai-Organization": {"org-test"},
				"Openai-Project":      {"proj_test"},
				"User-Agent":          {""},
			}
			if !equalHeaders(p.headers[0], want) {
				t.Errorf("headers %v, want %v", p.headers[0], want)
			}
			// A body no transport can send again (A6).
			if p.rewinds[0] {
				t.Error("Request.GetBody is set: a transport could send the body again")
			}
			// The four fields and no other, and nothing that names a message,
			// a mailbox, an account or a person.
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(p.bodies[0], &fields); err != nil {
				t.Fatal(err)
			}
			keys := slices.Sorted(func(yield func(string) bool) {
				for k := range fields {
					if !yield(k) {
						return
					}
				}
			})
			if !slices.Equal(keys, []string{"input", "model", "prompt_cache_options", "questions"}) {
				t.Errorf("fields %v", keys)
			}
			if string(fields["prompt_cache_options"]) != `{"mode":"explicit"}` {
				t.Errorf("prompt_cache_options %s", fields["prompt_cache_options"])
			}
		})
	}
}

func equalHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if !slices.Equal(a[k], v) {
			return false
		}
	}
	return true
}

// A1: no organisation or project header when the image names none.
func TestA1NoPinWhenNoneIsGiven(t *testing.T) {
	p := &played{}
	if _, err := New(&http.Client{Transport: p}, key(testKey), "", "").Decide(context.Background(), subjectCall); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.headers[0]["Openai-Organization"]; ok {
		t.Error("an organisation header")
	}
	if _, ok := p.headers[0]["Openai-Project"]; ok {
		t.Error("a project header")
	}
}

// A1: on the wire, through the transport enclave.EgressClient builds and
// the played provider's own client: HTTP/2, the headers the client sets
// and only those a transport must add, no User-Agent, and the connection
// kept for the next call.
func TestA1TheHeadersOnTheWire(t *testing.T) {
	for name, client := range map[string]func(pr *decidetest.Provider) *http.Client{
		"the egress transport":  func(pr *decidetest.Provider) *http.Client { return egressTo(t, pr.DialContext, pr.RootCAs(), "") },
		"the provider's client": func(pr *decidetest.Provider) *http.Client { return pr.Client() },
	} {
		t.Run(name, func(t *testing.T) {
			pr := decidetest.NewProvider(t, decidetest.NewTable())
			c := New(client(pr), key(testKey), "org-test", "proj_test")
			for range 2 {
				if _, err := c.Decide(context.Background(), messageCall); err != nil {
					t.Fatal(err)
				}
			}
			r := pr.Requests()[0]
			if r.Method != http.MethodPost || r.Host != "api.openai.com" || r.Path != "/v1/decisions" || r.Proto != "HTTP/2.0" {
				t.Errorf("%s %s%s %s", r.Method, r.Host, r.Path, r.Proto)
			}
			var names []string
			for k := range r.Header {
				names = append(names, k)
			}
			slices.Sort(names)
			if want := []string{"Accept-Encoding", "Authorization", "Content-Length", "Content-Type", "Openai-Organization", "Openai-Project"}; !slices.Equal(names, want) {
				t.Errorf("headers on the wire %v, want %v", names, want)
			}
			if pr.Dials() != 1 {
				t.Errorf("two calls, %d connections; want one", pr.Dials())
			}
		})
	}
}

// egressTo is the client enclave.EgressClient builds, its transport as it
// is (its proxy, HTTP/2, keep-alives, timeouts) but for the network: the
// host's CONNECT proxy is played in memory and joins each tunnel to
// dial's connection, and the certificates are checked against pool (as
// serverName when not empty). No external address is dialled.
func egressTo(t *testing.T, dial func(ctx context.Context, network, addr string) (net.Conn, error), pool *x509.CertPool, serverName string) *http.Client {
	t.Helper()
	hc := enclave.EgressClient(8001)
	tr := hc.Transport.(*http.Transport).Clone()
	if !tr.ForceAttemptHTTP2 || tr.Proxy == nil {
		t.Fatal("the egress transport changed: this helper proves less")
	}
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, proxy := net.Pipe()
		go connect(context.WithoutCancel(ctx), proxy, dial)
		return client, nil
	}
	tr.TLSClientConfig.RootCAs = pool
	tr.TLSClientConfig.ServerName = serverName
	hc.Transport = tr
	t.Cleanup(tr.CloseIdleConnections)
	return hc
}

// connect is the host's proxy, played (internal/hostproxy): one CONNECT to
// api.openai.com:443, its tunnel joined to dial's connection.
func connect(ctx context.Context, conn net.Conn, dial func(ctx context.Context, network, addr string) (net.Conn, error)) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect || req.Host != Hosts()[0] {
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\n\r\n")
		return
	}
	up, err := dial(ctx, "tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\n"); err != nil {
		return
	}
	go func() {
		_, _ = io.Copy(up, br)
		_ = up.Close()
	}()
	_, _ = io.Copy(conn, up)
}

// maxCase is one of the re-measure's maximum-size requests.
type maxCase struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Subject string  `json:"subject"`
	Body    string  `json:"body"`
	Atoms   [][]any `json:"atoms"`
}

func readMaxSize(t testing.TB) []maxCase {
	t.Helper()
	f, err := os.Open("../testdata/maxsize.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []maxCase
	dec := json.NewDecoder(zr)
	for dec.More() {
		var c maxCase
		if err := dec.Decode(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if len(out) != 4 {
		t.Fatalf("%d maximum-size requests", len(out))
	}
	return out
}

// maxCall is the call the enclave makes for a maximum-size request: its
// atoms are statements on the whole message.
func maxCall(c maxCase) decide.Call {
	call := decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Subject: c.Subject, Body: c.Body}}
	for i := range c.Atoms {
		call.Statements = append(call.Statements, query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "topic " + name(i)})
	}
	return call
}

// A1: the four maximum-size requests are sent whole: the text of the
// input_text part is the content the re-measure sent, byte for byte, and
// the body is the size the re-measure's client wrote, but for max02, whose
// 2,559 line separators (U+2028) encoding/json writes as \u2028 (7,677
// bytes more, the same JSON).
func TestA1TheLongestRequestsAreSentWhole(t *testing.T) {
	want := map[string]struct {
		content int
		hash    string
		body    int
	}{
		"max01": {265929, "8098b7905814fdad59943ac9cee9f1a9f29984439cdcfab145c1312cfd8e612b", 269_360},
		"max02": {364265, "d64b1fb497e40e220d1a4f6929b3eb0cbf4e9dfc5690e1d634b51f2a10876b15", 478_294},
		"max03": {265813, "929ae47cd35b3173c01c2497e1d93c44d3c094a41c9a1dd96f1dd70166ff2761", 269_128},
		"max04": {528346, "30d6bdc876e2554fcdda641f2a24639e7df7f21d2ea008c3bd464677c902344e", 1_058_112},
	}
	for _, c := range readMaxSize(t) {
		p := &played{}
		if _, err := newClient(p).Decide(context.Background(), maxCall(c)); err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		var r struct {
			Input []struct {
				Content json.RawMessage
			}
		}
		if err := json.Unmarshal(p.bodies[0], &r); err != nil {
			t.Fatal(err)
		}
		var parts []struct{ Text string }
		if err := json.Unmarshal(r.Input[1].Content, &parts); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(parts[0].Text))
		w := want[c.ID]
		if len(parts[0].Text) != w.content || hex.EncodeToString(sum[:]) != w.hash {
			t.Errorf("%s: the text sent is %d bytes, not the %d measured", c.ID, len(parts[0].Text), w.content)
		}
		if len(p.bodies[0]) != w.body {
			t.Errorf("%s: the body is %d bytes, want %d", c.ID, len(p.bodies[0]), w.body)
		}
	}
}

// A2: what comes back is checked.
func TestA2AnswersAreChecked(t *testing.T) {
	two := func(a, b string) string {
		return `{"model":"gpt-6-luna","answers":[` + a + `,` + b + `],"usage":{"input_tokens":10}}`
	}
	p0 := `{"type":"predicate","name":"s0","probability":0.25}`
	p1 := `{"type":"predicate","name":"s1","probability":0.75}`
	for _, c := range []struct {
		name    string
		body    string
		err     error
		answers []decide.Answer
	}{
		{"two answers", two(p0, p1), nil, []decide.Answer{{P: 0.25, OK: true}, {P: 0.75, OK: true}}},
		{"one answer", `{"model":"gpt-6-luna","answers":[` + p0 + `]}`, decide.ErrInvalidAnswer, nil},
		{"three answers", `{"model":"gpt-6-luna","answers":[` + p0 + `,` + p1 + `,` + p1 + `]}`, decide.ErrInvalidAnswer, nil},
		{"out of order", two(p1, p0), decide.ErrInvalidAnswer, nil},
		{"repeated", two(p0, p0), decide.ErrInvalidAnswer, nil},
		{"a name not a string", two(p0, `{"type":"predicate","name":1,"probability":0.5}`), decide.ErrInvalidAnswer, nil},
		{"an answer not an object", two(p0, `"s1"`), decide.ErrInvalidAnswer, nil},
		{"not JSON", `{"model":"gpt-6-luna","answers":[`, decide.ErrInvalidAnswer, nil},
		{"null", `null`, decide.ErrInvalidAnswer, nil},
		{"no model", `{"answers":[` + p0 + `,` + p1 + `]}`, decide.ErrInvalidAnswer, nil},
		{"an empty model", `{"model":"","answers":[` + p0 + `,` + p1 + `]}`, decide.ErrInvalidAnswer, nil},
		{"over 64 KiB", two(p0, p1) + strings.Repeat(" ", MaxAnswer), decide.ErrInvalidAnswer, nil},
		{"another model", strings.Replace(two(p0, p1), "gpt-6-luna", "gpt-6-luna-2027-01-01", 1), decide.ErrModelChanged, nil},
		{"another model, malformed", `{"model":"gpt-7","answers":[` + p0 + `]}`, decide.ErrInvalidAnswer, nil},
		{"another model, out of order", `{"model":"gpt-7","answers":[` + p1 + `,` + p0 + `]}`, decide.ErrInvalidAnswer, nil},
		{"a refusal", two(p0, `{"type":"refusal","name":"s1"}`), nil, []decide.Answer{{P: 0.25, OK: true}, {Refused: true}}},
		{"another type", two(`{"type":"boolean","name":"s0","value":true}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"no type", two(`{"name":"s0","probability":0.5}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"a type not a string", two(`{"type":1,"name":"s0","probability":0.5}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"NaN as a string", two(`{"type":"predicate","name":"s0","probability":"NaN"}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"a string", two(`{"type":"predicate","name":"s0","probability":"0.5"}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"negative", two(`{"type":"predicate","name":"s0","probability":-0.01}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"over 1", two(`{"type":"predicate","name":"s0","probability":1.01}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"out of range", two(`{"type":"predicate","name":"s0","probability":1e400}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"none", two(`{"type":"predicate","name":"s0"}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"null", two(`{"type":"predicate","name":"s0","probability":null}`, p1), nil, []decide.Answer{{}, {P: 0.75, OK: true}}},
		{"the bounds", two(`{"type":"predicate","name":"s0","probability":0}`, `{"type":"predicate","name":"s1","probability":1}`), nil, []decide.Answer{{P: 0, OK: true}, {P: 1, OK: true}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &played{answer: func(r *http.Request) (*http.Response, error) { return respond(r, 200, nil, c.body), nil }}
			res, err := newClient(p).Decide(context.Background(), messageCall)
			if c.err != nil {
				var ae *decide.AttemptError
				if res != nil || !errors.As(err, &ae) || !errors.Is(err, c.err) || !ae.Sent || ae.Retry {
					t.Fatalf("got %+v, %v; want %v, sent, no retry", res, err, c.err)
				}
				if errors.Is(c.err, decide.ErrModelChanged) && errors.Is(err, decide.ErrInvalidAnswer) {
					t.Error("another model is read as an invalid answer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Answers, c.answers) {
				t.Errorf("answers %+v, want %+v", res.Answers, c.answers)
			}
		})
	}
}

// A2: the usage is read when it says how many input tokens the call took,
// and is Reported false otherwise.
func TestA2Usage(t *testing.T) {
	for _, c := range []struct {
		usage string
		want  decide.Usage
	}{
		{`"usage":{"input_tokens":1390,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":7}}`, decide.Usage{InputTokens: 1390, CachedTokens: 5, CacheWriteTokens: 7, Reported: true}},
		{`"usage":{"input_tokens":1390}`, decide.Usage{InputTokens: 1390, Reported: true}},
		{`"usage":{"input_tokens":0}`, decide.Usage{Reported: true}},
		{`"usage":{}`, decide.Usage{}},
		{`"usage":null`, decide.Usage{}},
		{`"usage":{"input_tokens":"1390"}`, decide.Usage{}},
		{`"usage":{"input_tokens":-1}`, decide.Usage{}},
		{`"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":-1}}`, decide.Usage{}},
		{`"usage":{"input_tokens":1e15}`, decide.Usage{}},
		{`"usage":{"input_tokens":1.5}`, decide.Usage{}},
		{`"usage":"many"`, decide.Usage{}},
		{`"nothing":0`, decide.Usage{}},
	} {
		body := `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"s0","probability":0.5}],` + c.usage + `}`
		p := &played{answer: func(r *http.Request) (*http.Response, error) { return respond(r, 200, nil, body), nil }}
		res, err := newClient(p).Decide(context.Background(), subjectCall)
		if err != nil {
			t.Fatalf("%s: %v", c.usage, err)
		}
		if res.Usage != c.want {
			t.Errorf("%s: usage %+v, want %+v", c.usage, res.Usage, c.want)
		}
		if res.Model != Model || res.Took < 0 {
			t.Errorf("model %q, took %v", res.Model, res.Took)
		}
	}
}

// A3: every status and code of "Failures" is its AttemptError.
func TestA3FailuresAreAttemptErrors(t *testing.T) {
	limitBody := func(code string) string { return `{"error":{"message":"x","type":"x","code":"` + code + `"}}` }
	for _, c := range []struct {
		name       string
		status     int
		body       string
		retryAfter string
		err        error
		retry      bool
		after      time.Duration
	}{
		{"500", 500, "", "", decide.ErrUnavailable, true, 0},
		{"502", 502, "", "", decide.ErrUnavailable, true, 0},
		{"503 with Retry-After", 503, "", "2", decide.ErrUnavailable, true, 2 * time.Second},
		{"504", 504, "", "", decide.ErrUnavailable, true, 0},
		{"501", 501, "", "", decide.ErrUnavailable, false, 0},
		{"505", 505, "", "1", decide.ErrUnavailable, false, 0},
		{"429 slow_down", 429, limitBody("slow_down"), "1.5", decide.ErrUnavailable, true, 1500 * time.Millisecond},
		{"429 rate_limit_exceeded", 429, limitBody("rate_limit_exceeded"), "30", decide.ErrUnavailable, true, decide.MaxRetryAfter},
		{"429 with no code", 429, "not json", "", decide.ErrUnavailable, true, 0},
		{"429 credit_balance_exhausted", 429, limitBody("credit_balance_exhausted"), "1", decide.ErrProviderLimit, false, 0},
		{"429 insufficient_quota", 429, limitBody("insufficient_quota"), "", decide.ErrProviderLimit, false, 0},
		{"429 project_spend_limit_exceeded", 429, limitBody("project_spend_limit_exceeded"), "", decide.ErrProviderLimit, false, 0},
		{"429 organization_spend_limit_exceeded", 429, limitBody("organization_spend_limit_exceeded"), "", decide.ErrProviderLimit, false, 0},
		{"429 organization_usage_limit_exceeded", 429, limitBody("organization_usage_limit_exceeded"), "", decide.ErrProviderLimit, false, 0},
		{"401", 401, limitBody("invalid_api_key"), "", decide.ErrKeyRefused, false, 0},
		{"403", 403, "", "", decide.ErrKeyRefused, false, 0},
		{"400", 400, "", "", decide.ErrRequest, false, 0},
		{"404", 404, "", "", decide.ErrRequest, false, 0},
		{"413", 413, "", "", decide.ErrRequest, false, 0},
		{"422", 422, "", "", decide.ErrRequest, false, 0},
		{"301", 301, "", "", decide.ErrRequest, false, 0},
		{"302", 302, "", "", decide.ErrRequest, false, 0},
		{"303", 303, "", "", decide.ErrRequest, false, 0},
		{"304", 304, "", "", decide.ErrRequest, false, 0},
		{"307", 307, "", "", decide.ErrRequest, false, 0},
		{"308", 308, "", "", decide.ErrRequest, false, 0},
		{"201", 201, ok(2), "", decide.ErrInvalidAnswer, false, 0},
		{"204", 204, "", "", decide.ErrInvalidAnswer, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &played{answer: func(r *http.Request) (*http.Response, error) {
				h := http.Header{}
				if c.retryAfter != "" {
					h.Set("Retry-After", c.retryAfter)
				}
				if c.status >= 300 && c.status < 400 {
					h.Set("Location", "https://elsewhere.example/v1/decisions")
				}
				return respond(r, c.status, h, c.body), nil
			}}
			res, err := newClient(p).Decide(context.Background(), messageCall)
			expect(t, res, err, c.err, true, c.retry, c.after)
			if p.count() != 1 {
				t.Errorf("%d requests", p.count())
			}
		})
	}
}

func expect(t *testing.T, res *decide.Result, err, want error, sent, retry bool, after time.Duration) {
	t.Helper()
	var ae *decide.AttemptError
	if !errors.As(err, &ae) {
		t.Fatalf("%v is not an AttemptError", err)
	}
	if res != nil || !errors.Is(err, want) || ae.Sent != sent || ae.Retry != retry || ae.RetryAfter != after {
		t.Errorf("got %+v, want %v sent %v retry %v after %v", *ae, want, sent, retry, after)
	}
}

// A3: no key, or one that cannot be sent, is ErrKeyRefused, not Sent, and
// no request.
func TestA3NoKeyIsNoRequest(t *testing.T) {
	for _, cred := range []decide.Credential{nil, key(""), key("sk bad"), key("sk-\n"), key("sk-é"), key(strings.Repeat("k", 1025)),
		func(context.Context) (string, bool) { return testKey, false }} {
		p := &played{}
		res, err := New(&http.Client{Transport: p}, cred, "", "").Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrKeyRefused, false, false, 0)
		if p.count() != 0 {
			t.Error("a request without a key")
		}
	}
}

// A3: a call the client does not send is ErrRequest, not Sent.
func TestA3ACallNotSent(t *testing.T) {
	p := &played{}
	c := newClient(p)
	many := make([]query.Atom, decide.MaxStatementsPerCall+1)
	for i := range many {
		many[i] = aboutInvoices
	}
	for _, call := range []decide.Call{
		{Content: messageCall.Content},
		{Content: messageCall.Content, Statements: many},
		{Content: messageCall.Content, Statements: []query.Atom{subjectInvoices}},
		{Content: decide.Content{Subject: "x"}, Statements: []query.Atom{subjectInvoices}},
		{Content: messageCall.Content, Statements: []query.Atom{{Var: contracts.VarBody, Op: contracts.OpContains, Topic: "x"}}},
	} {
		res, err := c.Decide(context.Background(), call)
		expect(t, res, err, decide.ErrRequest, false, false, 0)
	}
	if p.count() != 0 {
		t.Error("sent")
	}
}

// A3: 3 s an attempt, 8 s past 32 KiB of content, never past the context's
// deadline.
func TestA3TheAttemptsTimeout(t *testing.T) {
	long := decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Body: strings.Repeat("x", decide.LongContent)}, Statements: []query.Atom{aboutInvoices}}
	short := decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Body: strings.Repeat("x", decide.LongContent-len(`{"subject":"","body":""}`))}, Statements: []query.Atom{aboutInvoices}}
	if n := len(decide.ContentJSON(short.Content)); n != decide.LongContent {
		t.Fatalf("short is %d bytes", n)
	}
	for _, c := range []struct {
		name   string
		call   decide.Call
		parent time.Duration
		want   time.Duration
	}{
		{"a subject", subjectCall, time.Minute, decide.CallTimeout},
		{"32 KiB of content", short, time.Minute, decide.CallTimeout},
		{"past 32 KiB", long, time.Minute, decide.LongCallTimeout},
		{"a sooner deadline", long, time.Second, time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			var deadline time.Time
			p := &played{answer: func(r *http.Request) (*http.Response, error) {
				deadline, _ = r.Context().Deadline()
				return respond(r, 200, nil, ok(1)), nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), c.parent)
			defer cancel()
			start := time.Now()
			if _, err := newClient(p).Decide(ctx, c.call); err != nil {
				t.Fatal(err)
			}
			if got := deadline.Sub(start); got > c.want+time.Second/10 || got < c.want-time.Second/2 {
				t.Errorf("the attempt's deadline is %v away, want %v", got, c.want)
			}
		})
	}
	if decide.CallTimeout != 3*time.Second || decide.LongCallTimeout != 8*time.Second || decide.LongContent != 32<<10 {
		t.Error("the ADR's timeouts changed")
	}
}

// hang is a transport that has a connection, sends the request, and never
// answers; offline, it never has a connection.
func hang(offline bool) *played {
	return &played{offline: offline, answer: func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
}

// A3: a timeout after the request was written is Sent; tried again only
// while the caller's context is alive; a context that ended before the
// request is not Sent.
func TestA3TimeoutsAndSent(t *testing.T) {
	t.Run("the attempt's own timeout", func(t *testing.T) {
		p := hang(false)
		c := newClient(p)
		c.callTimeout = 50 * time.Millisecond
		res, err := c.Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrUnavailable, true, true, 0)
	})
	t.Run("the caller's deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		res, err := newClient(hang(false)).Decide(ctx, subjectCall)
		expect(t, res, err, decide.ErrUnavailable, true, false, 0)
	})
	t.Run("no connection before the timeout", func(t *testing.T) {
		c := newClient(hang(true))
		c.callTimeout = 50 * time.Millisecond
		res, err := c.Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrUnavailable, false, true, 0)
	})
	t.Run("a context ended before the request", func(t *testing.T) {
		p := &played{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := newClient(p).Decide(ctx, subjectCall)
		expect(t, res, err, decide.ErrUnavailable, false, false, 0)
		if p.count() != 0 {
			t.Error("sent")
		}
	})
	t.Run("a transport error after the connection", func(t *testing.T) {
		p := &played{answer: func(*http.Request) (*http.Response, error) { return nil, errors.New("reset by " + testKey) }}
		res, err := newClient(p).Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrUnavailable, true, true, 0)
		if strings.Contains(err.Error(), testKey) {
			t.Error("the transport's words passed on")
		}
	})
	t.Run("an answer cut short", func(t *testing.T) {
		p := &played{answer: func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"model":`), errReader{})), Request: r}, nil
		}}
		res, err := newClient(p).Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrUnavailable, true, true, 0)
	})
	t.Run("an error cut short is still what its status says", func(t *testing.T) {
		p := &played{answer: func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(errReader{}), Request: r}, nil
		}}
		res, err := newClient(p).Decide(context.Background(), subjectCall)
		expect(t, res, err, decide.ErrKeyRefused, true, false, 0)
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for h, want := range map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"-1":                            0,
		"1":                             time.Second,
		" 2 ":                           2 * time.Second,
		"0.25":                          250 * time.Millisecond,
		"5":                             5 * time.Second,
		"3600":                          decide.MaxRetryAfter,
		"Inf":                           decide.MaxRetryAfter,
		"NaN":                           0,
		"soon":                          0,
		"Sun, 15 Nov 2026 12:00:03 GMT": 3 * time.Second,
		"Sun, 15 Nov 2026 13:00:00 GMT": decide.MaxRetryAfter,
		"Sun, 15 Nov 2026 11:00:00 GMT": 0,
	} {
		if got := retryAfter(h, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", h, got, want)
		}
	}
}

// A4: no words of the provider: an error body that echoes the key, a
// statement or the content is in no returned error, and nothing is logged.
func TestA4NoWordsOfTheProvider(t *testing.T) {
	var logged bytes.Buffer
	saved, savedSlog := log.Writer(), slog.Default()
	log.SetOutput(&logged)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { log.SetOutput(saved); slog.SetDefault(savedSlog) })

	statement, _ := decide.Instructions(mentionsHome)
	echo := `{"error":{"message":"Incorrect API key provided: ` + testKey + ` for ` + statement + ` in ` + messageCall.Content.Body + `","type":"invalid_request_error","code":"` + testKey + `"}}`
	secrets := []string{testKey, "sk-proj", "home", "invoice", "Thanks", "Incorrect"}
	for _, status := range []int{200, 307, 400, 401, 403, 404, 429, 500, 503} {
		body := echo
		if status == 200 {
			body = `{"model":"` + testKey + `","answers":[]}`
		}
		p := &played{answer: func(r *http.Request) (*http.Response, error) {
			return respond(r, status, http.Header{"Location": {"https://" + testKey + ".example/"}}, body), nil
		}}
		_, err := newClient(p).Decide(context.Background(), messageCall)
		if err == nil {
			t.Fatalf("%d: no error", status)
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%d: the error %q holds %q", status, err, s)
			}
		}
	}
	if logged.Len() != 0 {
		t.Errorf("logged: %s", logged.String())
	}
}

// A4: no words of the provider in a log of the transport's either. Go's
// HTTP/1.1 transport logs the bytes a server sends on an idle connection
// ("Unsolicited response received on idle HTTP channel"), so the client
// speaks only HTTP/2, whose transport logs no byte of the provider's: a
// provider that offers only HTTP/1.1 and would send a whole answer and
// then the key is never asked, and nothing is logged. The review's
// TestReview115TransportDoesNotLogProviderText, turned around.
func TestA4TheTransportLogsNoProviderText(t *testing.T) {
	t.Run("a provider that offers HTTP/1.1", func(t *testing.T) { logsNoProviderText(t, false) })
	// A TLS server may answer with no protocol at all (no ALPN): Go's
	// transport then speaks HTTP/1.1, whatever the client offered. The
	// second review's TestReview115SecondNoALPN, turned around.
	t.Run("a provider that offers no protocol", func(t *testing.T) { logsNoProviderText(t, true) })
}

func logsNoProviderText(t *testing.T, noALPN bool) {
	const secret = "provider-echo-sk-proj-secret-and-private-mail"
	var logged syncBuffer
	saved, savedSlog := log.Writer(), slog.Default()
	log.SetOutput(&logged)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { log.SetOutput(saved); slog.SetDefault(savedSlog) })

	var reached atomic.Bool
	done := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		answer := ok(2)
		_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s%s", len(answer), answer, secret)
		_ = buf.Flush()
		// Until the client closes the connection, or a second has passed
		// in which an idle connection would have read the bytes.
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = conn.Read(make([]byte, 1))
		done <- struct{}{}
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if noALPN {
		// StartTLS fills srv.TLS (its certificate, and http/1.1 as the one
		// protocol); the config each handshake gets instead names none.
		srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12}, nil
		}}
	}
	srv.StartTLS() // HTTP/1.1 only
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	hc := egressTo(t, func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}, pool, srv.Certificate().DNSNames[0])
	res, err := New(hc, key(testKey), "", "").Decide(context.Background(), messageCall)
	if reached.Load() {
		<-done
	}
	if s := logged.String(); strings.Contains(s, secret) || strings.Contains(s, "Unsolicited") {
		t.Errorf("the transport logged the provider's bytes: %s", s)
	}
	expect(t, res, err, decide.ErrUnavailable, false, true, 0)
	if reached.Load() {
		t.Error("a request went over HTTP/1.1")
	}
}

// A4: the client's copy of an *http.Transport (nil is
// http.DefaultTransport) speaks only HTTP/2, and the transport given is
// left as it is; another RoundTripper is used as it is given.
func TestA4OnlyHTTP2(t *testing.T) {
	protocols := func(rt http.RoundTripper) string {
		if tr, ok := rt.(*http.Transport); ok && tr.Protocols != nil {
			return tr.Protocols.String()
		}
		return "none set"
	}
	egress := enclave.EgressClient(8001)
	given := egress.Transport
	before, defaults := protocols(given), protocols(http.DefaultTransport)
	for _, c := range []*Client{New(egress, key(testKey), "", ""), New(nil, key(testKey), "", ""), New(&http.Client{}, key(testKey), "", "")} {
		if rt := c.hc.Transport; rt == given || rt == http.DefaultTransport || protocols(rt) != "{HTTP2}" {
			t.Errorf("the client's transport %T speaks %s", rt, protocols(rt))
		}
	}
	if egress.Transport != given || protocols(given) != before || protocols(http.DefaultTransport) != defaults {
		t.Error("New changed the transport it was given")
	}
	p := &played{}
	if c := newClient(p); c.hc.Transport != p {
		t.Error("another RoundTripper is not used as given")
	}
}

// syncBuffer is a bytes.Buffer the transport's goroutines can write at
// once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// A5: the client reaches one host, its endpoint's.
func TestA5Hosts(t *testing.T) {
	u, err := url.Parse(Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if hosts := Hosts(); !slices.Equal(hosts, []string{u.Hostname() + ":443"}) || u.Scheme != "https" || hosts[0] != "api.openai.com:443" {
		t.Errorf("Hosts() = %v for %s", hosts, Endpoint)
	}
}

// A6: one attempt a call: the played provider sees exactly one request per
// Decide, whatever it answers.
func TestA6OneAttemptACall(t *testing.T) {
	for _, f := range []decidetest.Failure{decidetest.Late, decidetest.Unavailable, decidetest.SlowDown, decidetest.Drop, decidetest.RefusedStream, decidetest.ResetStream, decidetest.GoAway, decidetest.Redirect, decidetest.KeyRefused, decidetest.ProviderLimit, 0} {
		tab := decidetest.NewTable()
		tab.SetRetryAfter(time.Second)
		tab.Fail(f)
		pr := decidetest.NewProvider(t, tab)
		c := New(pr.Client(), key(testKey), "", "")
		c.callTimeout = 200 * time.Millisecond
		_, _ = c.Decide(context.Background(), messageCall)
		if n := len(pr.Requests()); n != 1 || tab.Attempts() != 1 || pr.Dials() != 1 {
			t.Errorf("failure %d: %d requests, %d attempts, %d connections; want 1", f, n, tab.Attempts(), pr.Dials())
		}
	}
}

// A6: one transmission a call over HTTP/2 too, through the transport
// enclave.EgressClient builds. Go's HTTP/2 transport sends a request again
// by itself when the provider refuses its stream (REFUSED_STREAM), resets
// it (PROTOCOL_ERROR) or sends a GOAWAY that leaves it out, if it can
// rewind the body: here each comes once the body was read whole, and each
// is one request and ErrUnavailable, sent, tried again only by the service
// (which reserves that attempt first). The review's
// TestReview115NoHTTP2Replay, turned around.
func TestA6OneTransmissionOverHTTP2(t *testing.T) {
	whole, _ := body(messageCall)
	for _, f := range []decidetest.Failure{decidetest.RefusedStream, decidetest.ResetStream, decidetest.GoAway, decidetest.Drop, decidetest.Unavailable, decidetest.Late, 0} {
		t.Run(fmt.Sprintf("failure %d", f), func(t *testing.T) {
			tab := decidetest.NewTable()
			tab.Fail(f)
			pr := decidetest.NewProvider(t, tab)
			c := New(egressTo(t, pr.DialContext, pr.RootCAs(), ""), key(testKey), "", "")
			c.callTimeout = 200 * time.Millisecond
			res, err := c.Decide(context.Background(), messageCall)
			reqs := pr.Requests()
			if len(reqs) != 1 || tab.Attempts() != 1 {
				t.Errorf("one Decide: %d whole requests, %d attempts; want 1", len(reqs), tab.Attempts())
			}
			for _, r := range reqs {
				if r.Proto != "HTTP/2.0" || !bytes.Equal(r.Body, whole) {
					t.Errorf("%s, %d bytes of %d", r.Proto, len(r.Body), len(whole))
				}
			}
			if f == 0 {
				if err != nil || len(res.Answers) != 2 {
					t.Fatalf("%+v, %v", res, err)
				}
			} else {
				expect(t, res, err, decide.ErrUnavailable, true, true, 0)
			}
		})
	}
}

// A6: no redirect is followed, over the client enclave.EgressClient
// returns (which would follow one) with a played transport: a 307 and a
// 308 to the same address, to another host and to the same address over
// http are exactly one request, to the first; the client given to New keeps
// its own CheckRedirect and Jar. The second design review's
// TestReview112EgressClientFollowsModelRedirect, turned around.
func TestA6NoRedirectIsFollowed(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound, http.StatusSeeOther, http.StatusMovedPermanently} {
		for _, to := range []string{Endpoint, "https://elsewhere.example/v1/decisions", "http://api.openai.com/v1/decisions"} {
			egress := enclave.EgressClient(8001)
			jar, _ := cookiejar.New(nil)
			egress.Jar = jar
			p := &played{answer: func(r *http.Request) (*http.Response, error) {
				return respond(r, status, http.Header{"Location": {to}, "Set-Cookie": {"a=b"}}, ""), nil
			}}
			egress.Transport = p
			c := New(egress, key(testKey), "", "")
			res, err := c.Decide(context.Background(), messageCall)
			expect(t, res, err, decide.ErrRequest, true, false, 0)
			if p.count() != 1 || p.urls[0] != Endpoint {
				t.Errorf("%d to %s: requests %v", status, to, p.urls)
			}
			if egress.CheckRedirect != nil || egress.Jar != jar {
				t.Error("New changed the client it was given")
			}
			if c.hc.Jar != nil || c.hc.CheckRedirect == nil {
				t.Error("the client's copy keeps cookies or follows redirects")
			}
			// The client given still follows a redirect itself: the copy is
			// what refuses.
			req, _ := http.NewRequest(http.MethodPost, Endpoint, strings.NewReader("{}"))
			if resp, err := egress.Do(req); err == nil {
				resp.Body.Close()
			}
			if p.count() < 3 {
				t.Errorf("%d to %s: the egress client did not follow, so this proves nothing", status, to)
			}
		}
	}
}

// A6: New with no client still refuses redirects.
func TestA6NewWithoutAClient(t *testing.T) {
	c := New(nil, key(testKey), "", "")
	if c.hc.CheckRedirect == nil || c.hc.Jar != nil || c.Name() != Model {
		t.Error("New(nil)")
	}
}

// A7: the client over the played provider keeps the contract the fake
// keeps.
func TestA7TheClientKeepsTheContract(t *testing.T) {
	decidetest.Contract(t, func(t *testing.T, tab *decidetest.Table, key decide.Credential) decide.Model {
		return New(decidetest.NewProvider(t, tab).Client(), key, "org-test", "proj_test")
	})
}

// A7: and through the transport enclave.EgressClient builds.
func TestA7TheClientKeepsTheContractOverTheEgressTransport(t *testing.T) {
	decidetest.Contract(t, func(t *testing.T, tab *decidetest.Table, key decide.Credential) decide.Model {
		pr := decidetest.NewProvider(t, tab)
		return New(egressTo(t, pr.DialContext, pr.RootCAs(), ""), key, "org-test", "proj_test")
	})
}

// A7: the fake and the client over the played provider return the same
// for the same table, a value that is not a probability beside a valid
// one included. The review's
// TestReview115FakeAndProviderAgreeOnInvalidProbabilities, turned around.
func TestA7TheFakeAndTheClientAgree(t *testing.T) {
	for name, set := range map[string]func(*decidetest.Table){
		"below 0":           func(tab *decidetest.Table) { tab.Set(aboutInvoices, -0.01) },
		"above 1":           func(tab *decidetest.Table) { tab.Set(aboutInvoices, 1.01) },
		"NaN":               func(tab *decidetest.Table) { tab.Set(aboutInvoices, math.NaN()) },
		"infinite":          func(tab *decidetest.Table) { tab.Set(aboutInvoices, math.Inf(1)) },
		"negative infinite": func(tab *decidetest.Table) { tab.Set(aboutInvoices, math.Inf(-1)) },
		"a probability":     func(tab *decidetest.Table) { tab.Set(aboutInvoices, 0.25) },
		"refused":           func(tab *decidetest.Table) { tab.Refuse(aboutInvoices) },
		"no probability":    func(tab *decidetest.Table) { tab.Unknown(aboutInvoices) },
	} {
		t.Run(name, func(t *testing.T) {
			tab := decidetest.NewTable()
			set(tab)
			tab.Set(mentionsHome, 0.9)
			fake, fakeErr := decidetest.NewFake(tab, key(testKey)).Decide(context.Background(), messageCall)
			real, realErr := New(decidetest.NewProvider(t, tab).Client(), key(testKey), "", "").Decide(context.Background(), messageCall)
			if fakeErr != nil || realErr != nil {
				t.Fatalf("fake: %v; client: %v", fakeErr, realErr)
			}
			if !reflect.DeepEqual(fake.Answers, real.Answers) || fake.Model != real.Model || fake.Usage != real.Usage {
				t.Errorf("fake %+v, client %+v", fake, real)
			}
			if fake.Answers[1] != (decide.Answer{P: 0.9, OK: true}) {
				t.Errorf("the valid sibling: %+v", fake.Answers[1])
			}
		})
	}
}

// A4: the refusal of a handshake without h2 keeps the check the given
// transport already made, and leaves that transport's own as it was.
func TestA4OnlyH2KeepsTheGivenCheck(t *testing.T) {
	refused := errors.New("refused by the given check")
	var seen int
	given := &http.Transport{TLSClientConfig: &tls.Config{VerifyConnection: func(cs tls.ConnectionState) error {
		seen++
		if cs.ServerName == "refuse" {
			return refused
		}
		return nil
	}}}
	own := given.TLSClientConfig.VerifyConnection
	verify := http2Only(given).(*http.Transport).TLSClientConfig.VerifyConnection
	for _, c := range []struct {
		cs   tls.ConnectionState
		want error
	}{
		{tls.ConnectionState{ServerName: "refuse", NegotiatedProtocol: "h2"}, refused},
		{tls.ConnectionState{NegotiatedProtocol: "h2"}, nil},
		{tls.ConnectionState{NegotiatedProtocol: "http/1.1"}, errNotHTTP2},
		{tls.ConnectionState{}, errNotHTTP2},
	} {
		if err := verify(c.cs); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.cs, err, c.want)
		}
	}
	if seen != 4 {
		t.Errorf("the given check ran %d times, want 4", seen)
	}
	// A transport with no TLS config of its own: Clone makes one, which
	// gets the check.
	if tr := http2Only(&http.Transport{}).(*http.Transport); tr.TLSClientConfig.VerifyConnection(tls.ConnectionState{}) == nil {
		t.Error("a transport with no TLS config is not refused a handshake without h2")
	}
	if err := given.TLSClientConfig.VerifyConnection(tls.ConnectionState{}); err != nil || reflect.ValueOf(given.TLSClientConfig.VerifyConnection).Pointer() != reflect.ValueOf(own).Pointer() {
		t.Error("http2Only changed the given transport's check")
	}
}
