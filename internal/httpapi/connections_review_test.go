package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
)

// The review of pull request 6 (#131): its reproductions, as tests.

// cutAtHeader cuts the lease when the header is written: between the header
// and the first byte of the body.
type cutAtHeader struct {
	*httptest.ResponseRecorder
	lease *countedLease
}

func (w *cutAtHeader) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
	w.lease.Cut()
}

// V45 (review 1): a lease cut between the header and the first byte of the
// body writes no byte of the body, and flushes nothing.
func TestV45ALeaseCutAtTheHeaderWritesNoBody(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	lease := newLease()
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: []byte("mail after lease cut")}, lease, nil
	}
	w := &cutAtHeader{httptest.NewRecorder(), lease}
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/messages/mbx_1.1/attachments/0", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	if !abortedBy(a.h, w, r) {
		t.Error("the response was not aborted")
	}
	lease.finished(t, false)
	if w.Body.Len() != 0 || w.Flushed {
		t.Fatalf("wrote %d bytes of the body after the cut, flushed %v", w.Body.Len(), w.Flushed)
	}
}

// cutAfterWrite cuts the lease once a write has been buffered by the
// server, before it is flushed (when cut is set).
type cutAfterWrite struct {
	http.ResponseWriter
	lease *countedLease
	cut   bool
}

func (w *cutAfterWrite) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if w.cut {
		w.lease.Cut()
	}
	return n, err
}

func (w *cutAfterWrite) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// wire records every byte the server writes to its connections.
type wire struct {
	net.Listener
	mu  sync.Mutex
	out bytes.Buffer
}

func (l *wire) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &wireConn{c, l}, nil
}

func (l *wire) sent() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.String()
}

type wireConn struct {
	net.Conn
	l *wire
}

func (c *wireConn) Write(b []byte) (int, error) {
	c.l.mu.Lock()
	c.l.out.Write(b)
	c.l.mu.Unlock()
	return c.Conn.Write(b)
}

// V45 (review 1): a chunk the server buffered while the lease lasted is not
// flushed after the lease was cut: none of it reaches the connection, over
// HTTP/1.1, by a real server to a real client. The answer uncut reaches it
// whole. One P makes the schedule deterministic: the deadline's AfterFunc
// is runnable, and the handler goes on to flush unless it reads the lease
// first. Over HTTP/2 (without TLS, so the wire can be read) the answer is
// refused before its lease is used (second review): none of it, cut or not.
func TestV45ALeaseCutAfterABufferedWriteIsNotFlushed(t *testing.T) {
	const secret = "mail buffered before the cut, flushed after it"
	for _, h2 := range []bool{false, true} {
		for _, cut := range []bool{false, true} {
			name := map[bool]string{false: "http1", true: "http2"}[h2] + map[bool]string{false: " uncut", true: " cut"}[cut]
			t.Run(name, func(t *testing.T) {
				prev := runtime.GOMAXPROCS(1)
				defer runtime.GOMAXPROCS(prev)
				a := newConnApp(t)
				grants(a.fake, apiGrant)
				lease := newLease()
				a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
					return &inbox.Attachment{Data: []byte(secret)}, lease, nil
				}
				done := make(chan struct{})
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(done)
					r.Host = apiHost
					a.h.ServeHTTP(&cutAfterWrite{w, lease, cut}, r)
				}))
				l := &wire{Listener: srv.Listener}
				srv.Listener = l
				srv.Config.ErrorLog = log.New(io.Discard, "", 0)
				srv.Config.ConnContext = httpapi.ConnContext // over HTTP/2, a cut closes the connection
				client := &http.Client{Transport: &http.Transport{}}
				if h2 {
					srv.Config.Protocols = new(http.Protocols)
					srv.Config.Protocols.SetUnencryptedHTTP2(true)
					client.Transport.(*http.Transport).Protocols = new(http.Protocols)
					client.Transport.(*http.Transport).Protocols.SetUnencryptedHTTP2(true)
				}
				srv.Start()
				defer srv.Close()
				req, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
				req.Header.Set("Authorization", "Bearer "+apiToken)
				resp, err := client.Do(req)
				if err == nil {
					if h2 != (resp.ProtoMajor == 2) {
						t.Errorf("protocol %s", resp.Proto)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				<-done
				srv.Close()
				if h2 {
					if started, finished, _, _ := lease.State(); started || finished || strings.Contains(l.sent(), secret) {
						t.Fatalf("over HTTP/2: started %v, finished %v, on the wire %v", started, finished, strings.Contains(l.sent(), secret))
					}
					return
				}
				lease.finished(t, !cut)
				if on := strings.Contains(l.sent(), secret); on == cut {
					t.Fatalf("the answer on the wire: %v, the lease cut before the flush: %v", on, cut)
				}
			})
		}
	}
}

// V44, V45 (review 4): a path of the api or the mcp host that is not in
// canonical form ("//", "/./", "/../", an escape) is answered there, fixed,
// no-store: never a redirect, never what the request carried. Mounted as the
// composition mounts them, and each handler on its own.
func TestV44NoAliasOfARouteRedirectsOrReflects(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	check := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != 404 || rec.Header().Get("Location") != "" || rec.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(rec.Body.String(), "marker") {
			t.Errorf("status=%d location=%q cache=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	aliases := map[string][]string{
		apiHost: {
			"/oauth//token?code=pmc_private_marker", "/oauth/./revoke?token=pmr_private_marker",
			"/oauth/par/../token?client_id=https://private-marker.invalid/c", "/oauth/%74oken?code=pmc_marker",
			"/v1//connection?x=marker", "/v1/messages/./search?x=marker", "/v1/messages/mbx_1.1/attachments/../0?x=marker",
			"/.well-known//oauth-authorization-server?x=marker", "/.well-known/oauth-protected-resource//v1?x=marker",
		},
		mcpHost: {"/.well-known//oauth-protected-resource/mcp?x=marker", "/.well-known/./oauth-protected-resource?x=marker"},
	}
	for host, paths := range aliases {
		for _, path := range paths {
			for _, method := range []string{"GET", "POST"} {
				t.Run(method+" "+host+path, func(t *testing.T) {
					r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader("token=pma_marker"))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("Authorization", "Bearer "+apiToken)
					check(t, a.serve(r))
				})
			}
		}
	}
	if calls := a.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the service was asked %v", calls)
	}
	cfg := httpapi.ConnectionsConfig{
		Service: a.fake, OAuth: testOAuth, Metadata: testMetadata, ResourceMetadata: testResourceMetadata,
		Challenge: testChallenge, Log: slog.New(slog.DiscardHandler),
	}
	for name, h := range map[string]http.Handler{"server": httpapi.AuthorizationServer(cfg), "api": httpapi.Hooked(httpapi.VendorAPI(cfg))} {
		for _, path := range aliases[apiHost] {
			t.Run(name+" "+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("POST", "https://"+apiHost+path, nil))
				check(t, rec)
			})
		}
	}
}

// logEvents are the log's lines whose message is msg, each as its fields.
func logEvents(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(logs.String()) {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if e["msg"] == msg {
			delete(e, "time")
			delete(e, "level")
			delete(e, "msg")
			out = append(out, e)
		}
	}
	return out
}

// event is the fields of one request's line.
func event(route, outcome string, status int) map[string]any {
	return map[string]any{"route": route, "outcome": outcome, "status": float64(status)}
}

// V45 (review 6): every request of the API for apps writes one line, its
// route and outcome in fixed words and its status, and nothing it carried:
// a success, a refusal before the service and after, an answer not released,
// and a transfer cut after it started.
func TestV45EveryAPIRequestLogsItsRouteAndOutcome(t *testing.T) {
	const marker = "pmq_private_marker"
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		return &contracts.VendorConnection{}, nil
	}
	a.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		return nil, nil, contracts.ErrNotFound
	}
	var lease connections.Release
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: make([]byte, 100<<10)}, lease, nil
	}
	refused := newLease()
	refused.TooLate = true
	cut := newLease()
	for _, c := range []struct {
		token, method, path, body string
		lease                     connections.Release
		want                      map[string]any
	}{
		{apiToken, "GET", "/v1/connection?x=" + marker, "", nil, event("connection", "ok", 200)},
		{"", "GET", "/v1/connection", "", nil, event("connection", "unauthorized", 401)},
		{"pma_" + marker, "GET", "/v1/connection", "", nil, event("connection", "unauthorized", 401)},
		{apiToken, "POST", "/v1/messages/search", `{"query":{"v":2,"all":[{"any":[{"var":"` + marker + `"}]}]}}`, nil, event("search", "refused", 400)},
		{apiToken, "GET", "/v1/messages/mbx_" + marker, "", nil, event("read", "refused", 404)},
		{apiToken, "DELETE", "/v1/messages/mbx_" + marker, "", nil, event("read", "refused", 405)},
		{apiToken, "GET", "/v1/messages/mbx_1.1/attachments/0", "", refused, event("attachment", "not_released", 503)},
		{apiToken, "GET", "/v1/messages/mbx_1.1/attachments/0", "", cut, event("attachment", "cut", 200)},
		{apiToken, "GET", "/v1/messages/mbx_1.1/" + marker, "", nil, event("other", "refused", 404)},
		{"", "OPTIONS", "/v1/connection", "", nil, event("other", "ok", 204)},
	} {
		a.logs.Reset()
		lease = c.lease
		r := httptest.NewRequest(c.method, "https://"+apiHost+c.path, strings.NewReader(c.body))
		if c.token != "" {
			r.Header.Set("Authorization", "Bearer "+c.token)
		}
		var w http.ResponseWriter = httptest.NewRecorder()
		if c.lease == cut {
			w = &cutOnWrite{ResponseRecorder: httptest.NewRecorder(), lease: cut}
			if !abortedBy(a.h, w, r) {
				t.Fatalf("%s %s: not aborted", c.method, c.path)
			}
		} else {
			a.h.ServeHTTP(w, r)
		}
		got := logEvents(t, a.logs, "api: request")
		if len(got) != 1 || !mapsEqual(got[0], c.want) {
			t.Errorf("%s %s: logged %v, want %v", c.method, c.path, got, c.want)
		}
		if strings.Contains(a.logs.String(), marker) || strings.Contains(a.logs.String(), "mbx_1.1") || strings.Contains(a.logs.String(), apiToken) {
			t.Errorf("%s %s: the log holds what was sent: %s", c.method, c.path, a.logs)
		}
	}
}

// V44 (review 6): every request of the issuer's endpoints writes one line,
// its endpoint and outcome in fixed words and its status, and nothing of its
// form.
func TestV44EveryIssuerRequestLogsItsEndpointAndOutcome(t *testing.T) {
	const marker = "pmc_private_marker"
	a := newConnApp(t)
	a.fake.TokenFunc = func(_ context.Context, f url.Values) (*connections.TokenAnswer, error) {
		if f.Get("code") != "" {
			return nil, &connections.OAuthError{Code: oauth.ErrInvalidGrant}
		}
		return &connections.TokenAnswer{AccessToken: "pma_a", TokenType: "Bearer"}, nil
	}
	a.fake.RevokeTokenFunc = func(context.Context, url.Values) error { return connections.ErrUnavailable }
	a.fake.PushFunc = func(context.Context, url.Values) (string, error) { return oauth.RequestURIPrefix + "pmp_p", nil }
	for _, c := range []struct {
		method, path, body string
		want               map[string]any
	}{
		{"POST", oauth.PathToken, "grant_type=refresh_token&refresh_token=pmr_r", event("token", "ok", 200)},
		{"POST", oauth.PathToken, "grant_type=authorization_code&code=" + marker, event("token", "refused", 400)},
		{"POST", oauth.PathToken, "grant_type=" + marker, event("token", "refused", 400)},
		{"GET", oauth.PathToken + "?code=" + marker, "", event("token", "refused", 405)},
		{"POST", oauth.PathRevoke, "token=" + marker, event("revoke", "unavailable", 503)},
		{"POST", oauth.PathPushed, "client_id=https://" + marker + ".example/c", event("par", "ok", 201)},
		{"GET", oauth.PathServerMetadata, "", event("metadata", "ok", 200)},
		{"GET", "/oauth/" + marker, "", event("other", "refused", 404)},
	} {
		a.logs.Reset()
		r := httptest.NewRequest(c.method, "https://"+apiHost+c.path, strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		a.serve(r)
		got := logEvents(t, a.logs, "oauth: request")
		if len(got) != 1 || !mapsEqual(got[0], c.want) {
			t.Errorf("%s %s: logged %v, want %v", c.method, c.path, got, c.want)
		}
		if strings.Contains(a.logs.String(), "private_marker") || strings.Contains(a.logs.String(), "pmr_r") {
			t.Errorf("%s %s: the log holds what was sent: %s", c.method, c.path, a.logs)
		}
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// V43 (review 7): a body of the person's routes is one JSON object and
// nothing after it but white space, all of it within the 16 KiB bound;
// anything else is invalid_body before the service is asked.
func TestV43TheConsentBodiesAreOneBoundedObject(t *testing.T) {
	routes := slices.DeleteFunc(slices.Clone(consentRoutes), func(c consentRoute) bool { return c.body == "" })
	if len(routes) != 4 {
		t.Fatalf("%d routes with a body", len(routes))
	}
	for _, c := range routes {
		for _, suffix := range []string{` {"attacker":"trailing"}`, ` x`, `}`, `[]`, strings.Repeat(" ", 100<<10)} {
			a := newConnApp(t)
			answerEverything(a.fake)
			want(t, a.app("fresh", c.method, c.path, c.body+suffix), 400, "invalid_body")
			if calls := a.fake.Calls(); len(calls) != 0 {
				t.Fatalf("%s with %.20q after its body: the service was asked %v", c.path, suffix, calls)
			}
		}
		a := newConnApp(t)
		answerEverything(a.fake)
		if rec := a.app("fresh", c.method, c.path, c.body+" \n\t "); rec.Code != 200 {
			t.Fatalf("%s with white space after its body: %d %s", c.path, rec.Code, rec.Body)
		}
	}
}

// noFlush is a ResponseWriter that cannot flush.
type noFlush struct{ rec *httptest.ResponseRecorder }

func (w noFlush) Header() http.Header         { return w.rec.Header() }
func (w noFlush) Write(b []byte) (int, error) { return w.rec.Write(b) }
func (w noFlush) WriteHeader(code int)        { w.rec.WriteHeader(code) }

// V45: an answer whose chunk cannot be flushed is aborted, its lease
// finished once, unsent; a failure of the service's own is logged by its
// kind and its request's line says failed, and a request its client left
// says client_left, neither with the error's text.
func TestV45AFailedTransferAndAFailedServiceAreLoggedByTheirKind(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	lease := newLease()
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: []byte("x")}, lease, nil
	}
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/messages/mbx_1.1/attachments/0", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	if !abortedBy(a.h, noFlush{httptest.NewRecorder()}, r) {
		t.Fatal("not aborted")
	}
	lease.finished(t, false)
	if got := logEvents(t, a.logs, "api: request"); len(got) != 1 || !mapsEqual(got[0], event("attachment", "aborted", 200)) {
		t.Fatalf("logged %v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var fail error
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		if errors.Is(fail, context.Canceled) {
			cancel()
		}
		return nil, fail
	}
	for _, c := range []struct {
		err  error
		want map[string]any
	}{
		{errors.New("pmr_leaked at https://vendor.example/c"), event("connection", "failed", 500)},
		{context.Canceled, event("connection", "client_left", 500)},
	} {
		a.logs.Reset()
		fail = c.err
		r := httptest.NewRequestWithContext(ctx, "GET", "https://"+apiHost+"/v1/connection", nil)
		r.Header.Set("Authorization", "Bearer "+apiToken)
		a.serve(r)
		if got := logEvents(t, a.logs, "api: request"); len(got) != 1 || !mapsEqual(got[0], c.want) {
			t.Errorf("%v: logged %v", c.err, got)
		}
		if strings.Contains(a.logs.String(), "leaked") || strings.Contains(a.logs.String(), "vendor.example") {
			t.Errorf("the log holds the error's text: %s", a.logs)
		}
	}
}
