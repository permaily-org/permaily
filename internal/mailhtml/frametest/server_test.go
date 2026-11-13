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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
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
	lookups  []string
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
	api      http.Handler
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
	if dns := os.Getenv("PERMAILY_FRAMETEST_DNS"); dns != "" {
		pc, err := net.ListenPacket("udp", dns)
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		go s.resolver(pc)
		t.Logf("frametest: resolver on %s", dns)
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
	// The app's host refuses what another site asks its API for, as the
	// enclave's does, with the session or without.
	for _, cookie := range []string{"alice", ""} {
		req, _ := http.NewRequest("GET", appOrigin+"/api/v1/me", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
		}
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 403 || !strings.Contains(string(body), `"code":"forbidden"`) {
			t.Fatalf("me from another site, cookie %q: %d %s", cookie, res.StatusCode, body)
		}
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
	// The resolver answers every name as one that does not exist, and
	// records it.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go s.resolver(pc)
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
	if addrs, err := r.LookupHost(context.Background(), "pre.evil.example"); err == nil || len(addrs) != 0 {
		t.Fatalf("resolved: %v %v", addrs, err)
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if len(s.rec.internet) != 1 || s.rec.internet[0].Host != "evil.example" || len(s.rec.connects) != 1 {
		t.Fatalf("%+v %v", s.rec.internet, s.rec.connects)
	}
	if len(s.rec.lookups) == 0 || s.rec.lookups[0] != "pre.evil.example" {
		t.Fatalf("lookups %v", s.rec.lookups)
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
	// Sample mail for the screens of docs/web.md (screenshots, by hand):
	// named, with their own subjects, after the corpus.
	for i, sm := range samples(t) {
		id := fmt.Sprintf("%016x%016x", at.Add(time.Duration(len(names)+i)*time.Second).UnixNano(), len(names)+i+1)
		meta := inbox.StoredMeta{From: []contracts.MailAddress{{Name: "Northwind Outfitters", Address: "news@northwind.example"}}, Subject: sm.subject}
		raw := "From: Northwind Outfitters <news@northwind.example>\r\nTo: Alice <alice@example.org>\r\nSubject: " + sm.subject +
			"\r\nDate: Thu, 12 Nov 2026 09:30:00 +0000\r\nMIME-Version: 1.0\r\n" + sm.body
		if err := inboxtest.Seed(context.Background(), mem, mem, b, inboxtest.Message{ID: id, ReceivedAt: at.Add(time.Duration(i) * time.Minute), RetentionDays: 30, Raw: []byte(raw), Meta: meta}); err != nil {
			t.Fatal(err)
		}
		s.messages[sm.name] = id
	}
	api := httpapi.MailboxesAPI(httpapi.MailboxesConfig{
		Service: fake, Reader: inbox.NewReader(mem), Sessions: s.sessions, AppOrigin: appOrigin,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s.api = api
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
	// The app's host as the enclave mounts it: every route under /api/
	// behind the rule that refuses another site (httpapi.RefuseCrossSite).
	s.app = httptest.NewUnstartedServer(recorded(router.New(nil, map[string]http.Handler{appHost: httpapi.RefuseCrossSite(app)})))
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

type sample struct{ name, subject, body string }

// samples are the sample messages the screens are shown with: a newsletter
// with remote images and an inline image too large to show, a message with
// a text part of its own, one too large to format and one in a charset
// Permaily does not read.
func samples(t *testing.T) []sample {
	logo := pngOf(t, 120, 40, color.RGBA{0x1f, 0x5c, 0x4a, 0xff})
	huge := pngOf(t, 8193, 1, color.RGBA{0xff, 0xff, 0xff, 0xff})
	newsletter := `<!DOCTYPE html><html><head><style>
.wrap{max-width:600px;margin:0 auto;font-family:Georgia,serif;color:#222}
h1{font-size:28px;color:#1f5c4a;margin:16px 0 8px}
.cta{background-color:#1f5c4a;padding:12px 20px}
@media (max-width:480px){h1{font-size:22px}}
</style></head><body bgcolor="#f6f3ee"><div class="wrap">
<p><img src="cid:logo@northwind" alt="Northwind Outfitters" width="120" height="40"></p>
<h1>The autumn trail guide</h1>
<p><img src="https://cdn.northwind.example/hero.jpg" alt="A trail through birch trees" width="600"></p>
<p>Six routes for the short days, a checklist for wet weather, and the boots our guides wore all season.</p>
<p><img src="cid:map@northwind" alt="The route map"></p>
<table width="100%" cellpadding="0" cellspacing="0"><tr><td class="cta"><a href="https://northwind.example/guide/autumn?utm_source=mail">Read the guide</a></td></tr></table>
<p style="font-size:13px;color:#555">You get this because you asked for trail news. <a href="https://northwind.example/unsubscribe/7c91">Unsubscribe</a>.</p>
<img src="https://t.northwind.example/open/7c91.gif" width="1" height="1" alt="">
</div></body></html>`
	related := "Content-Type: multipart/related; boundary=rel\r\n\r\n" +
		"--rel\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + newsletter + "\r\n" +
		"--rel\r\nContent-Type: image/png\r\nContent-ID: <logo@northwind>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(logo) + "\r\n" +
		"--rel\r\nContent-Type: image/png\r\nContent-ID: <map@northwind>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(huge) + "\r\n" +
		"--rel--\r\n"
	alternative := "Content-Type: multipart/alternative; boundary=alt\r\n\r\n" +
		"--alt\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYour order NW-20417 has shipped.\r\n\r\nTrail boots, size 42: 1\r\nWool socks: 2\r\n\r\nTrack it at https://northwind.example/orders/NW-20417\r\n" +
		"--alt\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p style=\"font:16px/1.5 Arial,sans-serif\">Your order <b>NW-20417</b> has shipped.</p>" +
		"<table border=\"1\" cellpadding=\"6\" style=\"border-collapse:collapse\"><tr><td>Trail boots, size 42</td><td>1</td></tr><tr><td>Wool socks</td><td>2</td></tr></table>" +
		"<p><a href=\"https://northwind.example/orders/NW-20417\">Track your parcel</a></p>\r\n--alt--\r\n"
	return []sample{
		{"sample-newsletter", "The autumn trail guide", related},
		{"sample-order", "Your order has shipped", alternative},
		{"sample-too-large", "Everything in our catalogue", "Content-Type: text/html; charset=utf-8\r\n\r\n<p>The whole catalogue.</p>" + strings.Repeat("<br>", 524_288)},
		{"sample-unreadable", "Ein Gruß aus dem Laden", "Content-Type: multipart/alternative; boundary=alt\r\n\r\n--alt\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nA greeting from the shop, in the text the sender included.\r\n--alt\r\nContent-Type: text/html; charset=utf-7\r\n\r\n+ADw-p+AD4-Hallo+ADw-/p+AD4-\r\n--alt--\r\n"},
	}
}

func pngOf(t *testing.T, w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func b64(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for len(s) > 76 {
		out.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	out.WriteString(s)
	return out.String()
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app": orEmpty(s.rec.app), "internet": orEmpty(s.rec.internet),
			"connects": orEmptyS(s.rec.connects), "lookups": orEmptyS(s.rec.lookups),
		})
	case "reset":
		s.rec.mu.Lock()
		s.rec.app, s.rec.internet, s.rec.connects, s.rec.lookups = nil, nil, nil, nil
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
		// "A sandboxed document's own requests get nothing of the account"):
		// an image, a click that navigates the frame and a new tab, each to
		// the API, and an image of a page that is not the API, which records
		// whether the browser attached the session without being refused.
		w.Header().Del("Content-Security-Policy")
		write("text/html; charset=utf-8", `<!DOCTYPE html><img src="/test/probe?from=img"><img src="/api/v1/me?from=img">`+
			`<a id="go" href="/api/v1/me?from=click">go</a><a id="tab" href="/api/v1/me?from=tab" target="_blank">tab</a>`)
	case "viewer-sandboxed":
		write("text/html; charset=utf-8", page(viewerFrame("/test/sandboxed", "Message: sandboxed")))
	case "nometa":
		// The real frame route as a browser that sends no Fetch Metadata
		// reaches it, or behind a proxy that strips it (release gate:
		// "Missing Fetch Metadata"): /test/nometa/<message> is the route
		// for that message of the suite's mailbox, every Sec-Fetch-*
		// header removed.
		for k := range r.Header {
			if strings.HasPrefix(k, "Sec-Fetch-") {
				r.Header.Del(k)
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/v1/mailboxes/" + s.box + "/messages/" + arg + "/html"
		r2.RequestURI = r2.URL.RequestURI()
		s.api.ServeHTTP(w, r2)
	case "probe":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("probe"))
	case "blank":
		write("text/html; charset=utf-8", page("<p>Another page.</p>"))
	default:
		http.NotFound(w, r)
	}
}

// resolver is the browsers' only DNS server when the suite runs isolated
// (tests/message-frame/run.sh): it records the name of every query and
// answers that it does not exist, so no name of a message resolves to
// anything, and a lookup the browser made is seen.
func (s *suite) resolver(pc net.PacketConn) {
	buf := make([]byte, 1500)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		name, end, ok := question(buf[:n])
		if !ok {
			continue
		}
		s.rec.mu.Lock()
		s.rec.lookups = append(s.rec.lookups, name)
		s.rec.mu.Unlock()
		_, _ = pc.WriteTo(nxdomain(buf[:end]), from)
	}
}

// question reads the name of a DNS query's first question (RFC 1035, 4.1),
// and where that question ends.
func question(m []byte) (string, int, bool) {
	if len(m) < 12 || m[2]&0x80 != 0 || m[4] != 0 || m[5] == 0 {
		return "", 0, false
	}
	var labels []string
	i := 12
	for {
		if i >= len(m) {
			return "", 0, false
		}
		l := int(m[i])
		i++
		if l == 0 {
			break
		}
		if l > 63 || i+l > len(m) {
			return "", 0, false
		}
		labels = append(labels, string(m[i:i+l]))
		i += l
	}
	if i+4 > len(m) {
		return "", 0, false
	}
	return strings.ToLower(strings.Join(labels, ".")), i + 4, true
}

// nxdomain is the answer to query (its header and one question): a
// response, recursion available, name error, no records.
func nxdomain(query []byte) []byte {
	out := append([]byte(nil), query...)
	out[2] = 0x80 | out[2]&0x79 // QR, the opcode and RD kept
	out[3] = 0x80 | 3           // RA, NXDOMAIN
	out[4], out[5] = 0, 1
	for k := 6; k < 12; k++ {
		out[k] = 0
	}
	return out
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
