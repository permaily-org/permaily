//go:build dev

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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

	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
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
// holds a session, and signing out ends it. Then a passkey: made, used to
// sign in, made the only way in, with mail sending nothing and a recovery
// code working once. Then another person: a name, asking to delete the
// account and cancelling, asking again and, seven days later, an account the
// enclave removed by itself and an address that signs up again; with the
// operator's tool looking at each step.
func TestAccountsEndToEnd(t *testing.T) {
	t.Cleanup(func() { devAhead.Store(0) })
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

	// A passkey is made with a software authenticator, which answers what
	// the browser would for the page at the app's origin.
	b64 := base64.RawURLEncoding
	unb64 := func(s string) []byte {
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatalf("%q is not base64url without padding", s)
		}
		return b
	}
	auth := webauthntest.New(webauthntest.ES256)
	code, body = call("POST", app+"/api/v1/passkeys/start", "", "")
	var creation struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			RP        struct{ ID string } `json:"rp"`
			User      struct{ ID string } `json:"user"`
			Challenge string              `json:"challenge"`
		} `json:"options"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &creation) != nil || creation.Options.RP.ID != "app.permaily.test" {
		t.Fatalf("starting a passkey: %d %s", code, body)
	}
	credential, clientData, attestation := auth.Create(creation.Options.RP.ID, app, unb64(creation.Options.Challenge), unb64(creation.Options.User.ID))
	passkey := b64.EncodeToString(credential)
	code, body = call("POST", app+"/api/v1/passkeys/finish", "", fmt.Sprintf(
		`{"ceremony":%q,"name":"E2E key","credential":{"id":%q,"rawId":%q,"type":"public-key","response":{"clientDataJSON":%q,"attestationObject":%q,"transports":["internal"]},"clientExtensionResults":{}}}`,
		creation.Ceremony, passkey, passkey, b64.EncodeToString(clientData), b64.EncodeToString(attestation)))
	if code != 200 || !strings.Contains(body, `"id":"`+passkey+`"`) || !strings.Contains(body, `"name":"E2E key"`) {
		t.Fatalf("finishing a passkey: %d %s", code, body)
	}

	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after signing out: %d", code)
	}

	// The passkey signs in: no address is asked.
	code, body = call("POST", app+"/api/v1/auth/passkey/start", "", "")
	var request struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
		} `json:"options"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &request) != nil || request.Options.RPID != "app.permaily.test" {
		t.Fatalf("starting a sign-in with a passkey: %d %s", code, body)
	}
	clientData, authData, signature, handle := auth.Get(request.Options.RPID, app, unb64(request.Options.Challenge), credential)
	assertion := fmt.Sprintf(
		`{"ceremony":%q,"credential":{"id":%q,"rawId":%q,"type":"public-key","response":{"clientDataJSON":%q,"authenticatorData":%q,"signature":%q,"userHandle":%q},"clientExtensionResults":{}}}`,
		request.Ceremony, passkey, passkey, b64.EncodeToString(clientData), b64.EncodeToString(authData), b64.EncodeToString(signature), b64.EncodeToString(handle))
	code, body = call("POST", app+"/api/v1/auth/passkey/finish", "", assertion)
	if code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("signing in with a passkey: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/me", "", ""); code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("me after signing in with a passkey: %d %s", code, body)
	}
	// The answer worked once.
	if code, body := call("POST", app+"/api/v1/auth/passkey/finish", "", assertion); code != 400 || !strings.Contains(body, `"invalid_passkey"`) {
		t.Fatalf("the same answer again: %d %s", code, body)
	}

	// "Passkey only": from here a sign-in by mail answers as always and
	// sends nothing.
	code, body = call("POST", app+"/api/v1/account/passkey-only", "", `{"on":true}`)
	var only struct {
		PasskeyOnly   bool     `json:"passkey_only"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &only) != nil || !only.PasskeyOnly || len(only.RecoveryCodes) != 10 {
		t.Fatalf("passkey only: %d %s", code, body)
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}
	code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"ana@example.org"}`)
	var silent struct {
		Attempt   string `json:"attempt"`
		ExpiresIn int    `json:"expires_in"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &silent) != nil || silent.Attempt == "" || silent.ExpiresIn != 600 {
		t.Fatalf("starting a sign-in by mail with passkey only: %d %s", code, body)
	}
	// A message would be sent after the answer: give it the time one takes.
	for range 10 {
		time.Sleep(50 * time.Millisecond)
		if _, outbox := call("GET", app+"/dev/outbox", "", ""); strings.Contains(outbox, silent.Attempt) {
			t.Fatal("a sign-in message was sent to an account that signs in with passkeys only")
		}
	}

	// A recovery code and the address sign in, once.
	recovery := `{"address":"ana@example.org","code":"` + only.RecoveryCodes[0] + `"}`
	if code, body := call("POST", app+"/api/v1/auth/recovery", "", recovery); code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("recovery: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/passkeys", "", ""); code != 200 || !strings.Contains(body, `"passkey_only":true,"recovery_codes_left":9`) || !strings.Contains(body, `"name":"E2E key"`) {
		t.Fatalf("passkeys after a recovery: %d %s", code, body)
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}
	if code, body := call("POST", app+"/api/v1/auth/recovery", "", recovery); code != 400 || !strings.Contains(body, `"invalid_code"`) {
		t.Fatalf("the same recovery code again: %d %s", code, body)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after a recovery that was refused: %d", code)
	}

	// Another person signs up, reading the message that was sent for her.
	const operator = "https://api.permaily.test/v1/admin/accounts"
	signUp := func(address string) (id string) {
		t.Helper()
		code, body := call("POST", "https://api.permaily.test/v1/admin/invitations", admin, "")
		var made struct {
			Invitations []string `json:"invitations"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Invitations) != 1 {
			t.Fatalf("invitations: %d %s", code, body)
		}
		code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`"}`)
		var started struct {
			Attempt string `json:"attempt"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &started) != nil || started.Attempt == "" {
			t.Fatalf("start: %d %s", code, body)
		}
		signInCode := ""
		for range 50 {
			_, outbox := call("GET", app+"/dev/outbox", "", "")
			var sent []struct{ To, Text string }
			if err := json.Unmarshal([]byte(outbox), &sent); err != nil {
				t.Fatalf("the outbox: %v", err)
			}
			for _, m := range sent {
				if g := regexp.MustCompile(`\n    (\d{6})\n`).FindStringSubmatch(m.Text); g != nil && m.To == address && strings.Contains(m.Text, started.Attempt) {
					signInCode = g[1]
				}
			}
			if signInCode != "" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		code, body = call("POST", app+"/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+signInCode+`"}`)
		var me struct {
			Account struct{ ID, State, Name string } `json:"account"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &me) != nil || me.Account.ID == "" || me.Account.State != "active" || me.Account.Name != "" {
			t.Fatalf("finish for %s: %d %s", address, code, body)
		}
		return me.Account.ID
	}
	eve := signUp("eve@example.org")
	ana := strings.Split(session, ".")[0]

	// Her name, and what her account refuses while it is being deleted.
	if code, body := call("PATCH", app+"/api/v1/account", "", `{"name":"Eve E2E"}`); code != 200 || !strings.Contains(body, `"name":"Eve E2E"`) || !strings.Contains(body, `"state":"active"`) {
		t.Fatalf("naming the account: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/account/delete", "", ""); code != 200 || !strings.Contains(body, `"state":"closing"`) || !strings.Contains(body, `"closes_at":"`) {
		t.Fatalf("asking to delete the account: %d %s", code, body)
	}
	if code, body := call("PATCH", app+"/api/v1/account", "", `{"name":"Eve"}`); code != 409 || !strings.Contains(body, `"account_closing"`) {
		t.Fatalf("a change while the account is closing: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/me", "", ""); code != 200 || !strings.Contains(body, `"state":"closing"`) || !strings.Contains(body, `"name":"Eve E2E"`) {
		t.Fatalf("me while closing: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/account/delete/cancel", "", ""); code != 200 || !strings.Contains(body, `"state":"active"`) || strings.Contains(body, "closes_at") {
		t.Fatalf("cancelling: %d %s", code, body)
	}
	if code, body := call("PATCH", app+"/api/v1/account", "", `{"name":"Eve E2E again"}`); code != 200 || !strings.Contains(body, `"name":"Eve E2E again"`) {
		t.Fatalf("a change after cancelling: %d %s", code, body)
	}

	// The operator's tool looks, with the operator's token only.
	type listed struct {
		Accounts []struct {
			ID, State, Address, Name string
			Passkeys                 int
			PasskeyOnly              bool `json:"passkey_only"`
		} `json:"accounts"`
		Unreadable int `json:"unreadable"`
	}
	accounts := func() listed {
		t.Helper()
		code, body := call("GET", operator, admin, "")
		var list listed
		if code != 200 || json.Unmarshal([]byte(body), &list) != nil || list.Unreadable != 0 {
			t.Fatalf("the operator's list: %d %s", code, body)
		}
		return list
	}
	for _, url := range []string{operator, operator + "/" + eve} {
		if code, body := call("GET", url, "", ""); code != 401 || strings.Contains(body, "example.org") {
			t.Fatalf("the operator's tool without the token: %d %s", code, body)
		}
		if code, _ := call("DELETE", url, admin, ""); code != 405 {
			t.Fatalf("the operator's tool changing something: %d", code)
		}
	}
	if code, _ := call("GET", app+"/v1/admin/accounts", admin, ""); code == 200 {
		t.Fatal("the operator's tool answers on the app's host")
	}
	list := accounts()
	if len(list.Accounts) != 2 || list.Accounts[0].ID != ana || list.Accounts[0].Address != "ana@example.org" || list.Accounts[0].Passkeys != 1 || !list.Accounts[0].PasskeyOnly ||
		list.Accounts[1].ID != eve || list.Accounts[1].Name != "Eve E2E again" || list.Accounts[1].State != "active" || list.Accounts[1].PasskeyOnly {
		t.Fatalf("the operator's list: %+v", list)
	}
	if code, body := call("GET", operator+"/"+eve, admin, ""); code != 200 || !strings.Contains(body, `"address":"eve@example.org"`) || !strings.Contains(body, `"sessions":1`) {
		t.Fatalf("the operator's tool on one account: %d %s", code, body)
	}

	// She asks again and does not cancel. Seven days later the enclave has
	// removed the account by itself: nobody used it.
	if code, body := call("POST", app+"/api/v1/account/delete", "", ""); code != 200 || !strings.Contains(body, `"state":"closing"`) {
		t.Fatalf("asking again to delete the account: %d %s", code, body)
	}
	if list := accounts(); len(list.Accounts) != 2 || list.Accounts[1].State != "closing" {
		t.Fatalf("the operator's list with a closing account: %+v", list)
	}
	devAhead.Store(int64(7*24*time.Hour - time.Minute))
	time.Sleep(2 * devRemoveEvery)
	if code, body := call("GET", operator+"/"+eve, admin, ""); code != 200 || !strings.Contains(body, `"state":"closing"`) {
		t.Fatalf("the account a minute before its day: %d %s", code, body)
	}
	devAhead.Store(int64(7*24*time.Hour + time.Minute))
	removed := false
	for range 100 {
		if code, _ := call("GET", operator+"/"+eve, admin, ""); code == 404 {
			removed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !removed {
		t.Fatal("the enclave did not remove the account when its day came")
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after the account was removed: %d", code)
	}
	if list := accounts(); len(list.Accounts) != 1 || list.Accounts[0].ID != ana {
		t.Fatalf("the operator's list after the removal: %+v", list)
	}
	// Her address has no account: a sign-in sends nothing, and with an
	// invitation it signs up again, to a new one.
	code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"eve@example.org"}`)
	var nothing struct {
		Attempt string `json:"attempt"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &nothing) != nil || nothing.Attempt == "" {
		t.Fatalf("starting a sign-in for the address of a removed account: %d %s", code, body)
	}
	for range 10 {
		time.Sleep(50 * time.Millisecond)
		if _, outbox := call("GET", app+"/dev/outbox", "", ""); strings.Contains(outbox, nothing.Attempt) {
			t.Fatal("a sign-in message was sent to the address of a removed account")
		}
	}
	again := signUp("eve@example.org")
	if again == eve {
		t.Fatal("the address signed in to the account that was removed")
	}
	if list := accounts(); len(list.Accounts) != 2 || list.Accounts[1].ID != again || list.Accounts[1].Name != "" || list.Accounts[1].State != "active" {
		t.Fatalf("the operator's list after signing up again: %+v", list)
	}
	if code, _ := call("GET", operator+"/"+eve, admin, ""); code != 404 {
		t.Fatalf("the removed account after its address signed up again: %d", code)
	}

	// Logs leave the enclave: after all of it they name no address, no
	// code, no invitation, no cookie, no account, no passkey, no ceremony
	// and no name.
	out := logs.String()
	for _, line := range []string{
		"accounts: account created", "accounts: passkey made", "accounts: recovery code used",
		"accounts: account closing", "accounts: account kept", "accounts: account removed",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("the log does not record %q:\n%s", line, out)
		}
	}
	secrets := []string{
		"ana@", "example.org", signInCode, made.Invitations[0], started.Attempt, session, strings.Split(session, ".")[0], admin,
		passkey, hex.EncodeToString(credential), "E2E key", creation.Ceremony, request.Ceremony, silent.Attempt,
		"eve@", "Eve E2E", eve, again, nothing.Attempt,
	}
	for _, c := range only.RecoveryCodes {
		secrets = append(secrets, c, strings.ReplaceAll(c, "-", ""), strings.ToLower(strings.ReplaceAll(c, "-", "")))
	}
	for _, secret := range secrets {
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
