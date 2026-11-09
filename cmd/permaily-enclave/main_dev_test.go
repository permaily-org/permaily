//go:build dev

package main

import (
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
	"net/smtp"
	"strings"
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
	go func() { _ = run(cfg, slog.New(slog.DiscardHandler)) }()

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
	if code, body := get("https://mta-sts.inbox.permaily.test/.well-known/mta-sts.txt", ""); code != 200 || !strings.Contains(body, "mx: inbox.permaily.test") {
		t.Fatalf("mta-sts: %d %s", code, body)
	}
	if code, body := get("https://api.permaily.test/attestation?nonce="+strings.Repeat("ab", 32), ""); code != 200 || !strings.Contains(body, `"document"`) {
		t.Fatalf("attestation: %d", code)
	}
}
