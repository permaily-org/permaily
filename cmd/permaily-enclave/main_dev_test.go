//go:build dev

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/smtp"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func freePort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint32(ln.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits
}

// TestMailEndToEnd runs the whole service in dev mode: a message sent over
// SMTP with STARTTLS is readable through the mailbox API with the token.
func TestMailEndToEnd(t *testing.T) {
	const token = "pmt_e2e_token"
	sum := sha256.Sum256([]byte(token))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused",
		port: freePort(t), smtpPort: freePort(t),
		mailboxID: "e2e", mailboxAddress: "u-e2e@inbox.permaily.test",
		mailboxTokenSHA256: hex.EncodeToString(sum[:]), mtaSTSMode: "testing",
	}
	// Everything the service logs is kept, to check at the end that it holds no mail data.
	var logs lockedBuffer
	go func() {
		_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()

	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev mode self-signed certificate
	smtpAddr := fmt.Sprintf("127.0.0.1:%d", cfg.smtpPort)
	var c *smtp.Client
	var err error
	for range 50 {
		if c, err = smtp.Dial(smtpAddr); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartTLS(insecure); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail("alice@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("u-e2e@inbox.permaily.test"); err != nil {
		t.Fatal(err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "From: alice@example.org\r\nSubject: Booking confirmed\r\n\r\nSee you on Friday.\r\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.Quit()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: insecure,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}
	get := func(url, auth string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, _ := get("https://api.permaily.test/v1/mailbox/messages", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	code, body := get("https://api.permaily.test/v1/mailbox/messages", token)
	var page struct {
		Messages []struct {
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &page) != nil || len(page.Messages) != 1 || page.Messages[0].Subject != "Booking confirmed" {
		t.Fatalf("messages: %d %s", code, body)
	}
	// Dev mode has no root secret and no KMS key, and reports neither instead
	// of inventing a policy. It reports its TLS key, and that the key only
	// lives in memory here.
	if code, body := get("https://api.permaily.test/status", ""); code != 200 || strings.Contains(body, `"root"`) || strings.Contains(body, `"kms"`) || !strings.Contains(body, `"sealed":false`) || !strings.Contains(body, `"spki_sha256":"`) {
		t.Fatalf("status: %d %s", code, body)
	}
	if code, body := get("https://mta-sts.inbox.permaily.test/.well-known/mta-sts.txt", ""); code != 200 || !strings.Contains(body, "mx: inbox.permaily.test") {
		t.Fatalf("mta-sts: %d %s", code, body)
	}
	if code, body := get("https://api.permaily.test/attestation?nonce="+strings.Repeat("ab", 32), ""); code != 200 || !strings.Contains(body, `"document"`) {
		t.Fatalf("attestation: %d", code)
	}

	// Logs leave the enclave through the untrusted parent instance. After
	// receiving, listing and reading a message they must name no address, no
	// subject, no content and no token.
	if code, _ := get("https://api.permaily.test/v1/mailbox", token); code != 200 {
		t.Fatalf("mailbox: %d", code)
	}
	out := logs.String()
	if !strings.Contains(out, "inbox: message stored") {
		t.Fatalf("the log does not record the delivery:\n%s", out)
	}
	for _, secret := range []string{"alice", "example.org", "u-e2e", "Booking", "Friday", token, cfg.mailboxTokenSHA256} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q:\n%s", secret, out)
		}
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent log writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestAccountsEndToEnd runs the whole service in dev mode: the operator makes
// an invitation, a person signs up with the code of the message, the browser
// holds a session, and signing out ends it.
func TestAccountsEndToEnd(t *testing.T) {
	const admin = "pma_e2e_token"
	sum := sha256.Sum256([]byte(admin))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]),
	}
	var logs lockedBuffer
	go func() {
		_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}
	app := fmt.Sprintf("https://app.permaily.test:%d", cfg.port)
	call := func(method, url, auth, body string) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if strings.HasPrefix(url, app) && method != http.MethodGet {
			req.Header.Set("Origin", app)
			req.Header.Set("Content-Type", "application/json")
		}
		var res *http.Response
		var err error
		for range 50 {
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
			req.Body = io.NopCloser(strings.NewReader(body))
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, _ := call("POST", "https://api.permaily.test/v1/admin/invitations", "", ""); code != 401 {
		t.Fatalf("invitations without the operator's token: %d", code)
	}
	code, body := call("POST", "https://api.permaily.test/v1/admin/invitations", admin, `{"count":1}`)
	var made struct {
		Invitations []string `json:"invitations"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Invitations) != 1 {
		t.Fatalf("invitations: %d %s", code, body)
	}

	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me before signing in: %d", code)
	}
	code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"ana@example.org","invitation":"`+made.Invitations[0]+`"}`)
	var started struct {
		Attempt string `json:"attempt"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &started) != nil || started.Attempt == "" {
		t.Fatalf("start: %d %s", code, body)
	}
	// The message is sent after the answer: wait for it in the dev outbox.
	var signInCode string
	for range 50 {
		_, outbox := call("GET", app+"/dev/outbox", "", "")
		if g := regexp.MustCompile(`\\n    (\d{6})\\n`).FindStringSubmatch(outbox); g != nil && strings.Contains(outbox, started.Attempt) {
			signInCode = g[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if signInCode == "" {
		t.Fatal("no sign-in message in the outbox")
	}
	code, body = call("POST", app+"/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+signInCode+`"}`)
	if code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("finish: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/me", "", ""); code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("me: %d %s", code, body)
	}
	var session string
	for _, c := range jar.Cookies(mustURL(t, app)) {
		if c.Name == "__Host-permaily-session" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("the browser holds no session cookie")
	}
	// The web app is still served on the same host, and the API host knows
	// nothing of the app's own API.
	if code, _ := call("GET", app+"/api/v1/nothing", "", ""); code != 404 {
		t.Fatalf("unknown endpoint: %d", code)
	}
	if code, _ := call("GET", "https://api.permaily.test/api/v1/me", "", ""); code != 404 {
		t.Fatalf("the app's API on the API host: %d", code)
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after signing out: %d", code)
	}

	// Logs leave the enclave: after a sign-up they name no address, no
	// code, no invitation, no cookie and no account.
	out := logs.String()
	if !strings.Contains(out, "accounts: account created") {
		t.Fatalf("the log does not record the sign-up:\n%s", out)
	}
	for _, secret := range []string{"ana@", "example.org", signInCode, made.Invitations[0], started.Attempt, session, strings.Split(session, ".")[0], admin} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q:\n%s", secret, out)
		}
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
