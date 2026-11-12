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

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
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
// operator's tool looking at each step. Then the providers, played by a local
// server: a sign-up with Google, its answer refused in another browser,
// GitHub linked and unlinked from Settings, a Meta account whose address is
// confirmed by mail, and the operator turning a provider off.
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
	code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"ana@example.org","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`)
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
		code, body = call("POST", app+"/api/v1/auth/email/start", "", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`)
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

	// The providers. The operator sees which have an application, and never
	// a secret; the app is told which are on.
	const operatorProviders = "https://api.permaily.test/v1/admin/providers"
	if code, _ := call("GET", operatorProviders, "", ""); code != 401 {
		t.Fatalf("the providers' applications without the operator's token: %d", code)
	}
	if code, body := call("GET", operatorProviders, admin, ""); code != 200 || strings.Count(body, `"client_id":"client-of-`) != 4 || strings.Contains(body, "secret") {
		t.Fatalf("the providers' applications: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/auth/providers", "", ""); code != 200 || !strings.Contains(body, `{"providers":["google","github","meta","microsoft"]}`) {
		t.Fatalf("the providers that are on: %d %s", code, body)
	}
	providers := devProviders.Load()
	// trip starts a trip the way the page does, has the person agree at the
	// provider, and returns the address the provider sends the browser to.
	trip := func(path, body string) (string, string) {
		t.Helper()
		code, answer := call("POST", app+path, "", body)
		var started struct {
			URL string `json:"url"`
		}
		if code != 200 || json.Unmarshal([]byte(answer), &started) != nil || !strings.HasPrefix(started.URL, providers.URL+"/") {
			t.Fatalf("starting with a provider: %d %s", code, answer)
		}
		grant, state, err := providers.Approve(started.URL)
		if err != nil || grant == "" {
			t.Fatalf("the provider: %v", err)
		}
		return app + "/api/v1/auth/provider/callback?code=" + url.QueryEscape(grant) + "&state=" + url.QueryEscape(state), state
	}
	// arrive is the browser going where the provider sent it, with the
	// cookies of a jar, and returns where the callback sends it next.
	arrive := func(jar http.CookieJar, address string) string {
		t.Helper()
		browser := &http.Client{Jar: jar, Transport: client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := browser.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("the callback answered %d", res.StatusCode)
		}
		return res.Header.Get("Location")
	}
	invite := func() string {
		t.Helper()
		code, body := call("POST", "https://api.permaily.test/v1/admin/invitations", admin, "")
		var made struct {
			Invitations []string `json:"invitations"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Invitations) != 1 {
			t.Fatalf("invitations: %d %s", code, body)
		}
		return made.Invitations[0]
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}

	// Pat signs up with Google, which vouches for the address.
	providers.SignInAs(idp.Google, idptest.Person{Subject: "g-e2e-700100200300", Address: "pat@example.org", Verified: true, Name: "Pat E2E"})
	callback, patState := trip("/api/v1/auth/provider/start", `{"provider":"google","invitation":"`+invite()+`","return_to":"/connections","terms":"`+domain.TermsVersion+`"}`)
	// The answer of the provider in a browser that did not start the trip
	// signs nobody in, and ends the trip.
	stranger, _ := cookiejar.New(nil)
	if to := arrive(stranger, callback); to != "/sign-in#provider_error=provider_failed" || len(stranger.Cookies(mustURL(t, app))) != 0 {
		t.Fatalf("the provider's answer in another browser: %q, cookies %v", to, stranger.Cookies(mustURL(t, app)))
	}
	if to := arrive(jar, callback); to != "/sign-in#provider_error=provider_failed" {
		t.Fatalf("a trip whose answer was tried elsewhere: %q", to)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after a trip that failed: %d", code)
	}
	// Again, in the browser that starts it.
	callback, _ = trip("/api/v1/auth/provider/start", `{"provider":"google","invitation":"`+invite()+`","return_to":"/connections","terms":"`+domain.TermsVersion+`"}`)
	if to := arrive(jar, callback); to != "/connections" {
		t.Fatalf("signing up with Google: %q", to)
	}
	code, body = call("GET", app+"/api/v1/me", "", "")
	var pat struct {
		Account struct{ ID, Address string } `json:"account"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &pat) != nil || pat.Account.Address != "pat@example.org" {
		t.Fatalf("me after signing up with Google: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/providers", "", ""); code != 200 || !strings.Contains(body, `{"providers":[{"provider":"google","linked_at":"`) {
		t.Fatalf("the providers of the account: %d %s", code, body)
	}

	// GitHub is linked from Settings, signs in, and is unlinked.
	providers.SignInAs(idp.GitHub, idptest.Person{Subject: "700100200301"})
	callback, _ = trip("/api/v1/providers/start", `{"provider":"github"}`)
	if to := arrive(jar, callback); to != "/settings" {
		t.Fatalf("linking GitHub: %q", to)
	}
	if code, body := call("GET", app+"/api/v1/providers", "", ""); code != 200 || !strings.Contains(body, `{"provider":"github","linked_at":"`) || strings.Contains(body, "700100200301") {
		t.Fatalf("the providers after linking: %d %s", code, body)
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}
	callback, _ = trip("/api/v1/auth/provider/start", `{"provider":"github"}`)
	if to := arrive(jar, callback); to != "/" {
		t.Fatalf("signing in with GitHub: %q", to)
	}
	if code, body := call("GET", app+"/api/v1/me", "", ""); code != 200 || !strings.Contains(body, pat.Account.ID) {
		t.Fatalf("me after signing in with GitHub: %d %s", code, body)
	}
	if code, _ := call("DELETE", app+"/api/v1/providers/github", "", ""); code != 204 {
		t.Fatalf("unlinking GitHub: %d", code)
	}
	if code, _ := call("POST", app+"/api/v1/auth/sign-out", "", ""); code != 204 {
		t.Fatalf("sign-out: %d", code)
	}

	// Meta never vouches for an address. Mallory's account there shows Pat's:
	// nothing is linked until the code of a message is typed, and that
	// message goes to Pat.
	providers.SignInAs(idp.Meta, idptest.Person{Subject: "700100200302", Address: "pat@example.org", Verified: true})
	callback, _ = trip("/api/v1/auth/provider/start", `{"provider":"meta"}`)
	if to := arrive(jar, callback); to != "/sign-in#provider_confirm" {
		t.Fatalf("a Meta account that is not linked: %q", to)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("signed in by an address that Meta gave: %d", code)
	}
	if code, body := call("GET", app+"/api/v1/auth/provider/pending", "", ""); code != 200 || !strings.Contains(body, `"provider":"meta","address":"pat@example.org","return_to":"/"`) {
		t.Fatalf("what waits: %d %s", code, body)
	}
	code, body = call("POST", app+"/api/v1/auth/provider/confirm/start", "", `{"address":"pat@example.org"}`)
	var confirming struct {
		Attempt string `json:"attempt"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &confirming) != nil || confirming.Attempt == "" {
		t.Fatalf("asking for the message: %d %s", code, body)
	}
	confirmCode := ""
	for range 50 {
		_, outbox := call("GET", app+"/dev/outbox", "", "")
		var sent []struct{ To, Text string }
		if err := json.Unmarshal([]byte(outbox), &sent); err != nil {
			t.Fatalf("the outbox: %v", err)
		}
		for _, m := range sent {
			if g := regexp.MustCompile(`\n    (\d{6})\n`).FindStringSubmatch(m.Text); g != nil && m.To == "pat@example.org" && strings.Contains(m.Text, confirming.Attempt) {
				confirmCode = g[1]
			}
		}
		if confirmCode != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	wrongCode := "000000"
	if confirmCode == wrongCode {
		wrongCode = "000001"
	}
	if code, body := call("POST", app+"/api/v1/auth/provider/confirm/finish", "", `{"attempt":"`+confirming.Attempt+`","code":"`+wrongCode+`"}`); code != 400 || !strings.Contains(body, `"invalid_code"`) {
		t.Fatalf("a wrong code: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/auth/provider/confirm/finish", "", `{"attempt":"`+confirming.Attempt+`","code":"`+confirmCode+`"}`); code != 200 || !strings.Contains(body, pat.Account.ID) {
		t.Fatalf("confirming the address: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/providers", "", ""); code != 200 || !strings.Contains(body, `"provider":"google"`) || !strings.Contains(body, `"provider":"meta"`) || strings.Contains(body, `"github"`) {
		t.Fatalf("the providers after confirming: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/auth/provider/pending", "", ""); code != 400 || !strings.Contains(body, `"provider_failed"`) {
		t.Fatalf("what waited, after it was confirmed: %d %s", code, body)
	}

	// The operator takes Microsoft's application away: it is off.
	if code, _ := call("DELETE", operatorProviders+"/microsoft", admin, ""); code != 204 {
		t.Fatalf("removing an application: %d", code)
	}
	if code, body := call("GET", app+"/api/v1/auth/providers", "", ""); code != 200 || !strings.Contains(body, `{"providers":["google","github","meta"]}`) {
		t.Fatalf("the providers that are on: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/auth/provider/start", "", `{"provider":"microsoft"}`); code != 409 || !strings.Contains(body, `"provider_off"`) {
		t.Fatalf("starting with a provider that is off: %d %s", code, body)
	}
	if code, body := call("PUT", operatorProviders+"/microsoft", admin, `{"client_id":"client-of-microsoft","client_secret":"secret-of-microsoft"}`); code != 200 || strings.Contains(body, "secret") {
		t.Fatalf("setting an application: %d %s", code, body)
	}
	providers.SignInAs(idp.Microsoft, idptest.Person{Subject: "ms-e2e-700100200303", Address: "pat@example.org", Verified: true})
	callback, _ = trip("/api/v1/providers/start", `{"provider":"microsoft"}`)
	if to := arrive(jar, callback); to != "/settings" {
		t.Fatalf("linking Microsoft after it was set again: %q", to)
	}

	// Logs leave the enclave: after all of it they name no address, no
	// code, no invitation, no cookie, no account, no passkey, no ceremony
	// and no name; and no provider account, no state, no application's
	// secret and nothing a provider answered.
	out := logs.String()
	for _, line := range []string{"accounts: provider linked", "accounts: provider unlinked", "accounts: a provider's application was set", "accounts: a provider's application was removed"} {
		if !strings.Contains(out, line) {
			t.Fatalf("the log does not record %q:\n%s", line, out)
		}
	}
	for _, secret := range []string{
		"pat@", "Pat E2E", pat.Account.ID, "700100200300", "700100200301", "700100200302", "700100200303", patState, confirming.Attempt, confirmCode,
		"secret-of-", "client-of-", providers.URL, "code=", "state=",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q:\n%s", secret, out)
		}
	}
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

// The operator's side of accounts, and what a person is told, on the whole
// service in dev mode: an account is blocked and its sign-in refused with the
// reason, unblocked, its contact address changed with a code, an invitation
// listed and revoked; the notices are in the dev outbox, /status counts what
// the operator did, and the logs name nobody.
func TestOperatorEndToEnd(t *testing.T) {
	const admin = "pma_e2e_operator_token"
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
	app, api := fmt.Sprintf("https://app.permaily.test:%d", cfg.port), "https://api.permaily.test"
	call := func(method, url, auth, body string) (int, string) {
		t.Helper()
		var res *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequest(method, url, strings.NewReader(body))
			if auth != "" {
				req.Header.Set("Authorization", "Bearer "+auth)
			}
			if strings.HasPrefix(url, app) && method != http.MethodGet {
				req.Header.Set("Origin", app)
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) E2E/1.0")
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	type mail struct{ To, Subject, Text string }
	// outbox waits for a message to an address with a subject, and returns it.
	outbox := func(to, subject string) mail {
		t.Helper()
		var sent []mail
		for range 50 {
			_, body := call("GET", app+"/dev/outbox", "", "")
			if err := json.Unmarshal([]byte(body), &sent); err != nil {
				t.Fatalf("the outbox: %v", err)
			}
			for i := len(sent) - 1; i >= 0; i-- {
				if sent[i].To == to && sent[i].Subject == subject {
					return sent[i]
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no message %q to %s in the outbox: %+v", subject, to, sent)
		return mail{}
	}
	sixDigits := regexp.MustCompile(`\n    (\d{6})\n`)
	// signIn signs the browser in by mail and returns the answer.
	signIn := func(address, invitation string) (int, string) {
		t.Helper()
		terms := ""
		if invitation != "" {
			terms = `,"terms":"` + domain.TermsVersion + `"`
		}
		code, body := call("POST", app+"/api/v1/auth/email/start", "", `{"address":"`+address+`","invitation":"`+invitation+`"`+terms+`}`)
		var started struct {
			Attempt string `json:"attempt"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &started) != nil {
			t.Fatalf("start: %d %s", code, body)
		}
		var m mail
		for range 50 {
			if m = outbox(address, "Sign in to Permaily"); strings.Contains(m.Text, started.Attempt) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return call("POST", app+"/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+sixDigits.FindStringSubmatch(m.Text)[1]+`"}`)
	}

	// Two invitations: each with an identifier that is not its code.
	code, body := call("POST", api+"/v1/admin/invitations", admin, `{"count":2}`)
	var made struct {
		Invitations, IDs []string
	}
	if code != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Invitations) != 2 || len(made.IDs) != 2 {
		t.Fatalf("invitations: %d %s", code, body)
	}
	if code, body := call("GET", api+"/v1/admin/invitations", admin, ""); code != 200 || !strings.Contains(body, made.IDs[0]) || !strings.Contains(body, made.IDs[1]) || strings.Contains(body, made.Invitations[0]) {
		t.Fatalf("the list of invitations: %d %s", code, body)
	}
	if code, _ := call("DELETE", api+"/v1/admin/invitations/"+made.IDs[1], admin, ""); code != 204 {
		t.Fatalf("revoking an invitation: %d", code)
	}
	if code, body := call("POST", app+"/api/v1/auth/email/start", "", `{"address":"x@example.org","invitation":"`+made.Invitations[1]+`"}`); code != 400 || !strings.Contains(body, "invalid_invitation") {
		t.Fatalf("a revoked invitation: %d %s", code, body)
	}
	if code, body := call("GET", api+"/v1/admin/invitations", "", ""); code != 401 || strings.Contains(body, "inv_") {
		t.Fatalf("the list without the token: %d %s", code, body)
	}

	code, body = signIn("olga@example.org", made.Invitations[0])
	var me struct {
		Account struct{ ID string }
	}
	if code != 200 || json.Unmarshal([]byte(body), &me) != nil || me.Account.ID == "" {
		t.Fatalf("signing up: %d %s", code, body)
	}
	var device string
	for _, c := range jar.Cookies(mustURL(t, app)) {
		if c.Name == "__Host-permaily-device" {
			device = c.Value
		}
	}
	if len(device) != 43 {
		t.Fatalf("the browser holds no device cookie: %q", device)
	}
	one := api + "/v1/admin/accounts/" + me.Account.ID

	// The operator finds the account by its address, and blocks it.
	if code, body := call("GET", api+"/v1/admin/accounts?address=Olga%40example.org", admin, ""); code != 200 || !strings.Contains(body, me.Account.ID) || !strings.Contains(body, `"providers":[]`) {
		t.Fatalf("finding the account: %d %s", code, body)
	}
	if code, body := call("POST", one+"/block", "", ""); code != 401 {
		t.Fatalf("blocking without the token: %d %s", code, body)
	}
	if code, body := call("POST", one+"/block", admin, ""); code != 200 || !strings.Contains(body, `"blocked_at":"`) || !strings.Contains(body, `"sessions":0`) {
		t.Fatalf("blocking: %d %s", code, body)
	}
	told := outbox("olga@example.org", "Your Permaily account was blocked")
	if !strings.Contains(told.Text, "Permaily's operator blocked your Permaily account") || !strings.Contains(told.Text, "support@permaily.com") {
		t.Fatalf("the notice: %s", told.Text)
	}
	// The session is nothing, and signing in is refused with the reason,
	// once the code proved who it is.
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me with the session of a blocked account: %d", code)
	}
	if code, body := signIn("olga@example.org", ""); code != 403 || !strings.Contains(body, `"account_blocked"`) || !strings.Contains(body, "this account is blocked; write to support@permaily.com") {
		t.Fatalf("signing in to a blocked account: %d %s", code, body)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after a refused sign-in: %d", code)
	}
	// Unblocked, the same browser signs in, and is not a new device.
	if code, body := call("POST", one+"/unblock", admin, ""); code != 200 || strings.Contains(body, "blocked_at") {
		t.Fatalf("unblocking: %d %s", code, body)
	}
	outbox("olga@example.org", "Your Permaily account was unblocked")
	if code, body := signIn("olga@example.org", ""); code != 200 {
		t.Fatalf("signing in after unblocking: %d %s", code, body)
	}

	// The contact address changes with a code sent to the new one.
	code, body = call("POST", app+"/api/v1/account/address/start", "", `{"address":"olga.new@example.org"}`)
	var started struct {
		Attempt   string `json:"attempt"`
		ExpiresIn int    `json:"expires_in"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &started) != nil || started.Attempt == "" || started.ExpiresIn != 600 {
		t.Fatalf("asking for a new address: %d %s", code, body)
	}
	proof := outbox("olga.new@example.org", "Confirm your new address for Permaily")
	newCode := sixDigits.FindStringSubmatch(proof.Text)[1]
	if strings.Contains(proof.Text, "://") {
		t.Fatalf("the message that proves an address has a link: %s", proof.Text)
	}
	// The attempt signs nobody in.
	if code, body := call("POST", app+"/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+newCode+`"}`); code != 400 || !strings.Contains(body, "invalid_code") {
		t.Fatalf("an address attempt as a sign-in: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/account/address/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+newCode+`"}`); code != 200 || !strings.Contains(body, `"address":"olga.new@example.org"`) {
		t.Fatalf("changing the address: %d %s", code, body)
	}
	const changed = "The contact address of your Permaily account changed"
	if old := outbox("olga@example.org", changed); !strings.Contains(old.Text, "This address is no longer the account's") || strings.Contains(old.Text, "olga.new") {
		t.Fatalf("the notice to the old address: %s", old.Text)
	}
	outbox("olga.new@example.org", changed)
	if code, body := call("GET", api+"/v1/admin/accounts?address=olga.new%40example.org", admin, ""); code != 200 || !strings.Contains(body, me.Account.ID) {
		t.Fatalf("finding the account by its new address: %d %s", code, body)
	}
	if code, body := call("GET", api+"/v1/admin/accounts?address=olga%40example.org", admin, ""); code != 200 || !strings.Contains(body, `"accounts":[]`) {
		t.Fatalf("finding the account by the address it left: %d %s", code, body)
	}
	// Another browser signs in: its person is told, with how and from what,
	// named from fixed words and not as the browser said it.
	other, _ := cookiejar.New(nil)
	client.Jar = other
	if code, body := signIn("olga.new@example.org", ""); code != 200 {
		t.Fatalf("signing in from another browser: %d %s", code, body)
	}
	fresh := outbox("olga.new@example.org", "A new device signed in to your Permaily account")
	if !strings.Contains(fresh.Text, "How: with a code or a link sent by email") || !strings.Contains(fresh.Text, "    Browser: Linux\n") || strings.Contains(fresh.Text, "E2E") {
		t.Fatalf("the notice of a new device: %s", fresh.Text)
	}
	client.Jar = jar

	// The operator asks to delete, the person sees who asked and cancels;
	// the operator signs every device out.
	if code, body := call("POST", one+"/delete", admin, ""); code != 200 || !strings.Contains(body, `"closing_by":"operator"`) {
		t.Fatalf("the operator asks to delete: %d %s", code, body)
	}
	if code, body := call("GET", app+"/api/v1/me", "", ""); code != 200 || !strings.Contains(body, `"closing_by":"operator"`) || !strings.Contains(body, `"state":"closing"`) {
		t.Fatalf("what the person sees: %d %s", code, body)
	}
	if code, body := call("POST", app+"/api/v1/account/delete/cancel", "", ""); code != 200 || !strings.Contains(body, `"state":"active"`) {
		t.Fatalf("the person cancels: %d %s", code, body)
	}
	if code, body := call("POST", one+"/sessions/close", admin, ""); code != 200 || !strings.Contains(body, `"sessions":0`) {
		t.Fatalf("closing the sessions: %d %s", code, body)
	}
	if code, _ := call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("me after the operator closed the sessions: %d", code)
	}
	// Nothing on the operator's side changes an address or a way in.
	for _, c := range [][2]string{{"PATCH", one}, {"POST", one + "/address"}, {"POST", one + "/passkey-only"}, {"DELETE", one + "/passkeys/x"}, {"POST", one + "/sign-in"}} {
		if code, _ := call(c[0], c[1], admin, `{"address":"eve@example.org","on":false}`); code != 404 && code != 405 {
			t.Fatalf("%s %s: %d", c[0], c[1], code)
		}
	}

	// What the operator did is on /status, for anyone: kinds and counts.
	code, body = call("GET", app+"/status", "", "")
	var status struct {
		Admin struct {
			Since   time.Time
			Actions map[string]int
		}
	}
	if code != 200 || json.Unmarshal([]byte(body), &status) != nil || status.Admin.Since.IsZero() {
		t.Fatalf("status: %d %s", code, body)
	}
	for kind, n := range map[string]int{
		"block": 1, "unblock": 1, "delete": 1, "delete_cancel": 0, "sessions_close": 1, "invitations_made": 2, "invitation_revoked": 1,
		// Dev mode sets the four applications as an operator would.
		"provider_app_set": 4, "provider_app_removed": 0,
	} {
		if got, counted := status.Admin.Actions[kind]; !counted || got != n {
			t.Errorf("status counts %d of %s, want %d", got, kind, n)
		}
	}
	for _, secret := range []string{"olga", "example.org", me.Account.ID, "acc_", "inv_"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the status holds %q: %s", secret, body)
		}
	}

	// Logs leave the enclave: they say what kind of thing the operator did,
	// and name no address, account, invitation, attempt, code or cookie.
	out := logs.String()
	for _, line := range []string{`"msg":"accounts: the operator acted","kind":"block"`, `"kind":"unblock"`, `"kind":"sessions_close"`, `"kind":"invitation_revoked"`, "accounts: contact address changed"} {
		if !strings.Contains(out, line) {
			t.Fatalf("the log does not record %s:\n%s", line, out)
		}
	}
	for _, secret := range []string{"olga", "example.org", me.Account.ID, made.IDs[0], made.IDs[1], made.Invitations[0], made.Invitations[1], started.Attempt, newCode, device, admin, "E2E/1.0"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q:\n%s", secret, out)
		}
	}
}
