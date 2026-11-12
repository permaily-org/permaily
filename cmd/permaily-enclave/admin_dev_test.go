//go:build dev

package main

import (
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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// devBrowser is one person's browser against a dev enclave: its own cookies.
type devBrowser struct {
	t      *testing.T
	client *http.Client
	app    string
}

func (b *devBrowser) call(method, url, auth, body string) (int, string) {
	b.t.Helper()
	var res *http.Response
	var err error
	for range 50 {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if strings.HasPrefix(url, b.app) && method != http.MethodGet {
			req.Header.Set("Origin", b.app)
			req.Header.Set("Content-Type", "application/json")
		}
		if res, err = b.client.Do(req); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

// The admin panel in the whole service: the super admin is the account with
// the address of the image (-super-admin), is refused until "passkey only",
// makes an admin, who blocks an account after confirming with a passkey; the
// log, the notices and /status say so, and the enclave's log names nobody.
func TestAdminPanelEndToEnd(t *testing.T) {
	t.Cleanup(func() { devAhead.Store(0) })
	const token = "pma_e2e_admin_panel_token"
	sum := sha256.Sum256([]byte(token))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]), superAdmin: defaultSuperAdmin,
	}
	var logs lockedBuffer
	go func() {
		_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	app, api := fmt.Sprintf("https://app.permaily.test:%d", cfg.port), "https://api.permaily.test"
	browser := func() *devBrowser {
		jar, _ := cookiejar.New(nil)
		return &devBrowser{t: t, app: app, client: &http.Client{Jar: jar, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
			},
		}}}
	}
	operator := browser()
	type mail struct{ To, Subject, Text string }
	outbox := func(to, subject string) mail {
		t.Helper()
		var sent []mail
		for range 50 {
			_, body := operator.call("GET", app+"/dev/outbox", "", "")
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
		t.Fatalf("no message %q to %s: %+v", subject, to, sent)
		return mail{}
	}
	sixDigits := regexp.MustCompile(`\n    (\d{6})\n`)
	signUp := func(b *devBrowser, address string) string {
		t.Helper()
		_, body := operator.call("POST", api+"/v1/admin/invitations", token, `{"count":1}`)
		var made struct{ Invitations []string }
		_ = json.Unmarshal([]byte(body), &made)
		_, body = b.call("POST", app+"/api/v1/auth/email/start", "", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`"}`)
		var started struct{ Attempt string }
		_ = json.Unmarshal([]byte(body), &started)
		var m mail
		for range 50 {
			if m = outbox(address, "Sign in to Permaily"); strings.Contains(m.Text, started.Attempt) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		code, body := b.call("POST", app+"/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+sixDigits.FindStringSubmatch(m.Text)[1]+`"}`)
		var me struct{ Account struct{ ID, Admin string } }
		if code != 200 || json.Unmarshal([]byte(body), &me) != nil {
			t.Fatalf("signing %s up: %d %s", address, code, body)
		}
		return me.Account.ID
	}
	b64 := base64.RawURLEncoding
	unb64 := func(s string) []byte { out, _ := b64.DecodeString(s); return out }
	// passkeyOnly gives the account a passkey and turns "passkey only" on.
	passkeyOnly := func(b *devBrowser) (*webauthntest.Authenticator, string) {
		t.Helper()
		auth := webauthntest.New(webauthntest.ES256)
		_, body := b.call("POST", app+"/api/v1/passkeys/start", "", "")
		var c struct {
			Ceremony string
			Options  struct {
				RP        struct{ ID string }
				User      struct{ ID string }
				Challenge string
			}
		}
		_ = json.Unmarshal([]byte(body), &c)
		id, clientData, attestation := auth.Create(c.Options.RP.ID, app, unb64(c.Options.Challenge), unb64(c.Options.User.ID))
		key := b64.EncodeToString(id)
		if code, body := b.call("POST", app+"/api/v1/passkeys/finish", "", fmt.Sprintf(`{"ceremony":%q,"name":"key","credential":{"rawId":%q,"response":{"clientDataJSON":%q,"attestationObject":%q}}}`,
			c.Ceremony, key, b64.EncodeToString(clientData), b64.EncodeToString(attestation))); code != 200 {
			t.Fatalf("a passkey: %d %s", code, body)
		}
		if code, body := b.call("POST", app+"/api/v1/account/passkey-only", "", `{"on":true}`); code != 200 {
			t.Fatalf("passkey only: %d %s", code, body)
		}
		return auth, key
	}
	confirm := func(b *devBrowser, auth *webauthntest.Authenticator, key string) {
		t.Helper()
		_, body := b.call("POST", app+"/api/v1/auth/confirm/passkey/start", "", "")
		var r struct {
			Ceremony string
			Options  struct {
				Challenge string
				RPID      string `json:"rpId"`
			}
		}
		_ = json.Unmarshal([]byte(body), &r)
		clientData, authData, signature, handle := auth.Get(r.Options.RPID, app, unb64(r.Options.Challenge), unb64(key))
		if code, body := b.call("POST", app+"/api/v1/auth/confirm/passkey/finish", "", fmt.Sprintf(`{"ceremony":%q,"credential":{"rawId":%q,"response":{"clientDataJSON":%q,"authenticatorData":%q,"signature":%q,"userHandle":%q}}}`,
			r.Ceremony, key, b64.EncodeToString(clientData), b64.EncodeToString(authData), b64.EncodeToString(signature), b64.EncodeToString(handle))); code != 200 || !strings.Contains(body, `"confirmed_until":"`) {
			t.Fatalf("confirming: %d %s", code, body)
		}
	}

	boss, ana, carl := browser(), browser(), browser()
	bossID := signUp(boss, defaultSuperAdmin)
	if _, body := boss.call("GET", app+"/api/v1/me", "", ""); !strings.Contains(body, `"admin":"super"`) || !strings.Contains(body, `"confirmed_until":"`) {
		t.Fatalf("the super admin's me: %s", body)
	}
	if code, body := boss.call("GET", app+"/api/v1/admin/stats", "", ""); code != 403 || !strings.Contains(body, `"passkey_only_needed"`) {
		t.Fatalf("before passkey only: %d %s", code, body)
	}
	bossAuth, bossKey := passkeyOnly(boss)
	anaID := signUp(ana, "ana@example.org")
	anaAuth, anaKey := passkeyOnly(ana)
	carlID := signUp(carl, "carl@example.org")
	if code, body := ana.call("GET", app+"/api/v1/admin/stats", "", ""); code != 404 || !strings.Contains(body, `"not_found"`) {
		t.Fatalf("ana before: %d %s", code, body)
	}
	if code, body := boss.call("PUT", app+"/api/v1/admin/admins/"+anaID, "", ""); code != 200 || !strings.Contains(body, anaID) {
		t.Fatalf("making ana an admin: %d %s", code, body)
	}
	outbox("ana@example.org", "Your Permaily account is now an admin")
	// Ten minutes later, ana confirms with her passkey and blocks carl.
	devAhead.Store(int64(11 * time.Minute))
	one := app + "/api/v1/admin/accounts/" + carlID
	if code, body := ana.call("POST", one+"/block", "", ""); code != 403 || !strings.Contains(body, `"confirmation_needed"`) {
		t.Fatalf("blocking without a fresh proof: %d %s", code, body)
	}
	confirm(ana, anaAuth, anaKey)
	if code, body := ana.call("POST", one+"/block", "", ""); code != 200 || !strings.Contains(body, `"blocked_at":"`) {
		t.Fatalf("ana blocks carl: %d %s", code, body)
	}
	if code, _ := carl.call("GET", app+"/api/v1/me", "", ""); code != 401 {
		t.Fatalf("carl after: %d", code)
	}
	if code, body := ana.call("PUT", app+"/api/v1/admin/admins/"+carlID, "", ""); code != 403 || !strings.Contains(body, `"super_admin_only"`) {
		t.Fatalf("ana makes an admin: %d %s", code, body)
	}
	code, body := ana.call("GET", app+"/api/v1/admin/log", "", "")
	if code != 200 || !strings.Contains(body, `"kind":"block"`) || !strings.Contains(body, `"kind":"admin_made"`) || !strings.Contains(body, `"address":"carl@example.org"`) {
		t.Fatalf("the log: %d %s", code, body)
	}
	if code, body := ana.call("GET", app+"/api/v1/admin/stats", "", ""); code != 200 || !strings.Contains(body, `"blocked":1`) || !strings.Contains(body, `"total":3`) {
		t.Fatalf("the counts: %d %s", code, body)
	}
	// The token's routes make nobody an admin.
	if code, _ := operator.call("PUT", api+"/v1/admin/admins/"+carlID, token, ""); code != 404 {
		t.Fatalf("the token makes an admin: %d", code)
	}
	confirm(boss, bossAuth, bossKey)
	if code, body := boss.call("DELETE", app+"/api/v1/admin/admins/"+anaID, "", ""); code != 200 || strings.Contains(body, anaID) {
		t.Fatalf("removing ana: %d %s", code, body)
	}
	outbox("ana@example.org", "Your Permaily account is no longer an admin")

	_, body = operator.call("GET", app+"/status", "", "")
	var status struct {
		Admin struct{ Actions map[string]int }
	}
	if json.Unmarshal([]byte(body), &status) != nil || status.Admin.Actions["admin_made"] != 1 || status.Admin.Actions["admin_removed"] != 1 || status.Admin.Actions["block"] != 1 {
		t.Fatalf("status: %s", body)
	}
	out := logs.String()
	for _, secret := range []string{"example.org", defaultSuperAdmin, bossID, anaID, carlID, token} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q", secret)
		}
	}
	if !strings.Contains(out, `"kind":"admin_made"`) || !strings.Contains(out, `"msg":"accounts: a session was confirmed","way":"passkey"`) {
		t.Fatalf("the log does not say what was done:\n%s", out)
	}
}
