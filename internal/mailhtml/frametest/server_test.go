// Package frametest is the server the browser suite of ADR 0026 runs
// against (tests/message-frame, "Tests" 26 to 31): tests only, never in the
// image. Browsers reach it through a proxy it runs, which is the only way
// out they have: the app's host is served over TLS by the real frame and
// link routes (httpapi.MailboxesAPI over mailboxestest.Fake and the real
// reader on store.Memory), the built web app and a few test pages; every
// other host is "the internet", a listener that records whatever reaches
// it, so a test can assert that nothing did.
package frametest

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
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/mailhtml"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

const (
	appHost   = "app.permaily.test"
	appOrigin = "https://" + appHost
	alice     = "acc_aliceaaaaaaaaaaaaaaaaaaaaa"
)

// viewerFrame is the viewer's <iframe>, exactly as web/src/pages/inbox/
// MessageFrame.tsx writes it (its attributes are pinned there by test 33).
func viewerFrame(src, title string) string {
	return `<iframe src="` + html.EscapeString(src) + `" sandbox="allow-popups allow-popups-to-escape-sandbox" referrerpolicy="no-referrer" title="` +
		html.EscapeString(title) + `"></iframe>`
}

// A request that reached a host, as the suite reads it back.
type seen struct {
	Host    string `json:"host"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Session bool   `json:"session"`
	Device  bool   `json:"device"`
	Referer string `json:"referer,omitempty"`
	Dest    string `json:"dest,omitempty"`
	Site    string `json:"site,omitempty"`
	Status  int    `json:"status,omitempty"`
}

type recorder struct {
	mu       sync.Mutex
	app      []seen
	internet []seen
	connects []string
}

func (r *recorder) add(list *[]seen, s seen) {
	r.mu.Lock()
	*list = append(*list, s)
	r.mu.Unlock()
}

// sessions are the suite's sessions: a cookie's value stands for an
// account until /test/sign-out.
type sessions struct {
	mu  sync.Mutex
	who map[string]*accounts.Authenticated
}

func (s *sessions) Authenticate(_ context.Context, cookie string) (*accounts.Authenticated, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if who, ok := s.who[cookie]; ok {
		return who, nil
	}
	return nil, contracts.ErrUnauthorized
}

type suite struct {
	t        *testing.T
	rec      *recorder
	sessions *sessions
	box      string
	cases    map[string][]byte // corpus name -> raw HTML
	messages map[string]string // corpus name -> message id
	app      *httptest.Server
	internet *httptest.Server
	web      string
}

// TestServe runs the suite's server until it is stopped, when
// PERMAILY_FRAMETEST_ADDR names where its proxy listens (the browsers'
// proxy, and GET /health); PERMAILY_FRAMETEST_WEB is the built web app.
// Skipped otherwise.
func TestServe(t *testing.T) {
	addr := os.Getenv("PERMAILY_FRAMETEST_ADDR")
	if addr == "" {
		t.Skip("PERMAILY_FRAMETEST_ADDR is not set: the browser suite's server is not asked for")
	}
	web := os.Getenv("PERMAILY_FRAMETEST_WEB")
	if web != "" && !filepath.IsAbs(web) {
		web = filepath.Join("..", "..", "..", web) // relative to the repository, and go test runs here
	}
	s := newSuite(t, web)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.proxy), ReadHeaderTimeout: 10 * time.Second}
	t.Logf("frametest: proxy on %s, app at %s, internet at %s", addr, s.app.Listener.Addr(), s.internet.Listener.Addr())
	if err := srv.Serve(ln); err != nil {
		t.Fatal(err)
	}
}

// TestTheSuiteServerAnswers checks the server itself, without a browser.
func TestTheSuiteServerAnswers(t *testing.T) {
	s := newSuite(t, "")
	c := s.client()
	get := func(path string, cookie string) *http.Response {
		req, _ := http.NewRequest("GET", appOrigin+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
		}
		req.Header.Set("Sec-Fetch-Dest", "iframe")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	var list struct {
		Cases []struct{ Name, Message string }
	}
	res := get("/test/cases", "")
	_ = json.NewDecoder(res.Body).Decode(&list)
	cases := list.Cases
	if len(cases) < 15 {
		t.Fatalf("%d cases", len(cases))
	}
	frame := get("/api/v1/mailboxes/"+s.box+"/messages/"+cases[0].Message+"/html", "alice")
	if frame.StatusCode != 200 || !strings.HasPrefix(frame.Header.Get("Content-Security-Policy"), "sandbox allow-popups") {
		t.Fatalf("%d %v", frame.StatusCode, frame.Header)
	}
	if res := get("/test/plain/"+cases[0].Name, ""); res.StatusCode != 200 || res.Header.Get("Content-Security-Policy") != "" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	if res := get("/test/raw/"+cases[0].Name, ""); res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	// Through the proxy, a host that is not the app's reaches the internet
	// listener, and is recorded.
	pr := httptest.NewServer(http.HandlerFunc(s.proxy))
	defer pr.Close()
	tr := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(pr.URL) }, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the suite's own certificate
	res, err := (&http.Client{Transport: tr}).Get("https://evil.example/x")
	if err != nil || res.StatusCode != 200 {
		t.Fatal(err)
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if len(s.rec.internet) != 1 || s.rec.internet[0].Host != "evil.example" || len(s.rec.connects) != 1 {
		t.Fatalf("%+v %v", s.rec.internet, s.rec.connects)
	}
}

func newSuite(t *testing.T, web string) *suite {
	cert := certificate(t)
	s := &suite{t: t, rec: &recorder{}, web: web, cases: map[string][]byte{}, messages: map[string]string{}}
	s.sessions = &sessions{who: map[string]*accounts.Authenticated{
		"alice": {AccountID: alice, Session: contracts.Session{ID: "ses_alice", ConfirmedUntil: time.Now().Add(time.Hour)}},
	}}
	fake := mailboxestest.New()
	mem := store.NewMemory()
	box := fake.Add(alice, "Mine")
	s.box = box.ID
	b := inbox.Box{MailboxID: box.ID, Key: mailboxestest.Key(box.ID)}
	files, err := filepath.Glob(filepath.Join("..", "testdata", "hostile", "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the corpus: %v", err)
	}
	extra := map[string]string{
		"links": `<p>Pay at <a href="https://evil.example/pay?id=7">https://bank.example/pay</a>, or read <a href="http://news.example:8080/a b">the news</a>.</p>` +
			`<p style="height:400px">Space</p><p><a href="https://evil.example/second">Second link</a></p>`,
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		extra[strings.TrimSuffix(filepath.Base(f), ".html")] = string(raw)
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	at := time.Now().Add(-time.Hour)
	for i, name := range names {
		s.cases[name] = []byte(extra[name])
		id := fmt.Sprintf("%016x%016x", at.Add(time.Duration(i)*time.Second).UnixNano(), i+1)
		raw := "From: Sender <sender@example.org>\r\nSubject: " + name + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + extra[name]
		meta := inbox.StoredMeta{From: []contracts.MailAddress{{Name: "Sender", Address: "sender@example.org"}}, Subject: name}
		if err := inboxtest.Seed(context.Background(), mem, mem, b, inboxtest.Message{ID: id, ReceivedAt: at, RetentionDays: 30, Raw: []byte(raw), Meta: meta}); err != nil {
			t.Fatal(err)
		}
		s.messages[name] = id
	}
	api := httpapi.MailboxesAPI(httpapi.MailboxesConfig{
		Service: fake, Reader: inbox.NewReader(mem), Sessions: s.sessions, AppOrigin: appOrigin,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	app := http.NewServeMux()
	app.Handle("/api/v1/mailboxes", api)
	app.Handle("/api/v1/mailboxes/", api)
	app.HandleFunc("/api/v1/me", s.me)
	app.HandleFunc("/api/v1/auth/providers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[]}`))
	})
	app.HandleFunc("/test/", s.test)
	app.HandleFunc("/", s.spa)
	recorded := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(sw, r)
			if !strings.HasPrefix(r.URL.Path, "/test/requests") && !strings.HasPrefix(r.URL.Path, "/test/reset") {
				_, sess := r.Cookie(httpapi.SessionCookie)
				_, dev := r.Cookie(httpapi.DeviceCookie)
				s.rec.add(&s.rec.app, seen{Host: appHost, Method: r.Method, Path: r.URL.RequestURI(), Session: sess == nil, Device: dev == nil,
					Referer: r.Referer(), Dest: r.Header.Get("Sec-Fetch-Dest"), Site: r.Header.Get("Sec-Fetch-Site"), Status: sw.status})
			}
		})
	}
	s.app = httptest.NewUnstartedServer(recorded(router.New(nil, map[string]http.Handler{appHost: app})))
	s.app.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.app.StartTLS()
	s.internet = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.rec.add(&s.rec.internet, seen{Host: r.Host, Method: r.Method, Path: r.URL.RequestURI(), Referer: r.Referer()})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!DOCTYPE html><title>The internet</title><p>The internet listener.</p>"))
	}))
	s.internet.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.internet.StartTLS()
	t.Cleanup(func() { s.app.Close(); s.internet.Close() })
	return s
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// proxy is the browsers' only way out: CONNECT to the app's host reaches
// the app; to any other host, the internet listener, recorded; a plain
// http request through it is recorded and answered there.
func (s *suite) proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		if r.URL.IsAbs() {
			s.rec.add(&s.rec.internet, seen{Host: r.URL.Host, Method: r.Method, Path: r.URL.RequestURI(), Referer: r.Referer()})
			_, _ = w.Write([]byte("recorded"))
			return
		}
		_, _ = w.Write([]byte("ok"))
		return
	}
	host, _, _ := net.SplitHostPort(r.Host)
	target := s.internet.Listener.Addr().String()
	if host == appHost {
		target = s.app.Listener.Addr().String()
	} else {
		s.rec.mu.Lock()
		s.rec.connects = append(s.rec.connects, host)
		s.rec.mu.Unlock()
	}
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(upstream, buf, int64(buf.Reader.Buffered()))
		}
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}()
}

func (s *suite) me(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(httpapi.SessionCookie)
	if err == nil {
		if who, err := s.sessions.Authenticate(r.Context(), c.Value); err == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"account": map[string]any{"id": who.AccountID, "state": "active", "address": "alice@example.org", "name": "Alice", "language": "en", "created_at": "2026-11-01T10:00:00Z"},
				"session": map[string]any{"id": who.Session.ID, "created_at": "2026-11-01T10:00:00Z", "last_used_at": "2026-11-12T10:00:00Z", "expires_at": "2026-12-12T10:00:00Z", "device": "test", "current": true},
			})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"sign in"}}`))
}

// spa serves the built web app as the enclave does (internal/webui): a file
// that exists, or index.html for a route of the app.
func (s *suite) spa(w http.ResponseWriter, r *http.Request) {
	if s.web == "" {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if st, err := os.Stat(filepath.Join(s.web, p)); err != nil || st.IsDir() {
		p = "index.html"
	}
	if p == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, filepath.Join(s.web, p))
}

func page(body string) string {
	return `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Viewer</title>` +
		`<style>iframe{display:block;width:900px;height:700px;border:1px solid #ccc}</style></head>` +
		`<body data-state="clean">` + body + `</body></html>`
}

// test answers the suite's own pages and controls.
func (s *suite) test(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/test/")
	name, arg, _ := strings.Cut(rest, "/")
	write := func(contentType, body string) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}
	switch name {
	case "health":
		write("text/plain", "ok")
	case "cases":
		type c struct{ Name, Message string }
		var out []c
		for n, id := range s.messages {
			out = append(out, c{n, id})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"box": s.box, "cases": out})
	case "requests":
		s.rec.mu.Lock()
		defer s.rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"app": orEmpty(s.rec.app), "internet": orEmpty(s.rec.internet), "connects": orEmptyS(s.rec.connects)})
	case "reset":
		s.rec.mu.Lock()
		s.rec.app, s.rec.internet, s.rec.connects = nil, nil, nil
		s.rec.mu.Unlock()
		write("text/plain", "reset")
	case "session":
		// The cookies the app sets: the session (Strict) and the device's
		// (Lax), both HttpOnly; and one a page script can read, as a marker.
		s.sessions.mu.Lock()
		s.sessions.who["alice"] = &accounts.Authenticated{AccountID: alice, Session: contracts.Session{ID: "ses_alice", ConfirmedUntil: time.Now().Add(time.Hour)}}
		s.sessions.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: httpapi.SessionCookie, Value: "alice", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.SetCookie(w, &http.Cookie{Name: httpapi.DeviceCookie, Value: "device", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.SetCookie(w, &http.Cookie{Name: "marker", Value: "app", Path: "/", Secure: true, SameSite: http.SameSiteStrictMode})
		write("text/plain", "signed in")
	case "sign-out":
		s.sessions.mu.Lock()
		delete(s.sessions.who, "alice")
		s.sessions.mu.Unlock()
		write("text/plain", "signed out")
	case "viewer":
		// The viewer's frame on the real route (test 28, and the release
		// gate's session cookie on the frame's first navigation).
		id := s.messages[arg]
		write("text/html; charset=utf-8", page(viewerFrame("/api/v1/mailboxes/"+s.box+"/messages/"+id+"/html", "Message: "+arg)))
	case "viewer-raw":
		// Run A: the raw corpus file in the viewer's frame, with the
		// frame's headers and no sanitiser.
		write("text/html; charset=utf-8", page(viewerFrame("/test/raw/"+arg, "Message: "+arg)))
	case "raw":
		body, ok := s.cases[arg]
		if !ok {
			http.NotFound(w, r)
			return
		}
		httpapi.SetFrameHeaders(w.Header(), appOrigin)
		_, _ = w.Write(body) //nolint:gosec // Run A: hostile HTML on purpose, behind the frame's headers alone
	case "plain":
		// Run B: the sanitiser's output as a plain page of the origin, with
		// no CSP, no sandbox and no frame.
		body, ok := s.cases[arg]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var out bytes.Buffer
		in := mailhtml.Input{HTML: body, Mailbox: s.box, Message: s.messages[arg], LinkKey: inbox.LinkKey(mailboxestest.Key(s.box))}
		if _, err := mailhtml.Render(r.Context(), in, &out); err != nil {
			http.Error(w, "render", http.StatusInternalServerError)
			return
		}
		w.Header().Del("Content-Security-Policy")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out.Bytes()) //nolint:gosec // Run B: the sanitiser's output without the frame, on purpose
	case "sandboxed":
		// A document that asks the app for its API from inside the frame,
		// with the sandbox attribute and no CSP of its own (release gate:
		// "A sandboxed document's own requests carry no session").
		w.Header().Del("Content-Security-Policy")
		write("text/html; charset=utf-8", `<!DOCTYPE html><img src="/test/probe?from=img"><img src="/api/v1/me?from=img">`+
			`<a id="go" href="/test/probe?from=click">go</a><a id="tab" href="/test/probe?from=tab" target="_blank">tab</a>`)
	case "viewer-sandboxed":
		write("text/html; charset=utf-8", page(viewerFrame("/test/sandboxed", "Message: sandboxed")))
	case "probe":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("probe"))
	case "blank":
		write("text/html; charset=utf-8", page("<p>Another page.</p>"))
	default:
		http.NotFound(w, r)
	}
}

func orEmpty(s []seen) []seen {
	if s == nil {
		return []seen{}
	}
	return s
}

func orEmptyS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// certificate is a self-signed certificate for every name the suite
// serves; the browsers ignore its errors.
func certificate(t *testing.T) tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: appHost},
		DNSNames:  []string{appHost, "*.example", "evil.example", "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (s *suite) client() *http.Client {
	app := s.app.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, app)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the suite's own certificate
	}}
}
