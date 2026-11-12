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

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The operator's routes of mailboxes are mounted on the API host, behind the
// admin token (ADR 0026): without it every one is 401; with it an address
// that leads to no mailbox is an empty list, an identifier that is no
// account or no mailbox is not_found, and every answer is JSON with nosniff.
func TestTheOperatorsMailboxRoutesAreMounted(t *testing.T) {
	const token = "pma_e2e_mailboxes_operator_token"
	sum := sha256.Sum256([]byte(token))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]), superAdmin: defaultSuperAdmin,
	}
	var logs lockedBuffer
	go func() { _ = run(cfg, slog.New(slog.NewJSONHandler(&logs, nil))) }()
	b := &devBrowser{t: t, app: fmt.Sprintf("https://app.permaily.test:%d", cfg.port), client: &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}}
	const api = "https://api.permaily.test"
	none := "/v1/admin/mailboxes/mbx_" + strings.Repeat("a", 26)
	routes := [][2]string{
		{"GET", "/v1/admin/mailboxes?address=nobody%40inbox.permaily.test"}, {"POST", none + "/pause"}, {"POST", none + "/resume"},
		{"GET", "/v1/admin/accounts/acc_" + strings.Repeat("a", 26) + "/mailboxes"},
	}
	for _, r := range routes {
		if code, body := b.call(r[0], api+r[1], "", ""); code != 401 {
			t.Errorf("%s %s without the token: %d %s", r[0], r[1], code, body)
		}
	}
	if code, body := b.call("GET", api+routes[0][1], token, ""); code != 200 || body != `{"mailboxes":[]}`+"\n" {
		t.Fatalf("an address of no mailbox: %d %s", code, body)
	}
	for _, r := range routes[1:] {
		if code, body := b.call(r[0], api+r[1], token, ""); code != 404 || !strings.Contains(body, `"not_found"`) {
			t.Errorf("%s %s: %d %s", r[0], r[1], code, body)
		}
	}
	if strings.Contains(logs.String(), "nobody@inbox") || strings.Contains(logs.String(), "mbx_") {
		t.Fatalf("the log holds what was asked:\n%s", logs.String())
	}
}

// O2 to O5 against the real service, end to end: a mailbox made through a
// second mailboxes service over the same storage is found by its address,
// paused and resumed through the operator's routes. While it is paused SMTP
// refuses mail to it at RCPT TO, and the second service sees it paused;
// /status counts each change; no answer, /status or log line holds its
// address, its label or its identifier.
func TestTheOperatorPausesARealMailboxEndToEnd(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	const token = "pma_e2e_mailboxes_pause_token"
	sum := sha256.Sum256([]byte(token))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]), superAdmin: defaultSuperAdmin,
	}
	var logs lockedBuffer
	go func() {
		_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	other, err := mailboxes.New(mailboxes.Config{
		Records: &store.Anchored{Records: shared.mem, Commits: shared.commits, OnlyNewest: true, Strict: true, Now: devClock},
		Storage: shared.mem, Keys: shared.keys, Accounts: mailboxestest.NewAccounts(), Domain: "inbox.permaily.test", Now: devClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	me := mailboxes.Caller{Account: "acc_kpq2yvnbsthk7o5xdq3icxfaxe", ConfirmedUntil: devClock().Add(time.Hour)}
	m, err := other.Create(ctx, me, contracts.MailboxCreate{Label: "Secret Label"})
	if err != nil {
		t.Fatal(err)
	}
	b := &devBrowser{t: t, client: &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}}
	const api, app = "https://api.permaily.test", "https://app.permaily.test"
	secrets := []string{m.Address, strings.Split(m.Address, "@")[0], "Secret Label"}
	clean := func(what, body string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(strings.ToLower(body), strings.ToLower(s)) {
				t.Fatalf("%s holds %q: %s", what, s, body)
			}
		}
	}
	var answered []string
	call := func(method, path string) contracts.MailboxRecord {
		t.Helper()
		code, body := b.call(method, api+path, token, "")
		if code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, code, body)
		}
		clean(path, body)
		answered = append(answered, body)
		var rec contracts.MailboxRecord
		var list contracts.MailboxRecordList
		if json.Unmarshal([]byte(body), &list) == nil && len(list.Mailboxes) == 1 {
			return list.Mailboxes[0]
		}
		if err := json.Unmarshal([]byte(body), &rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	statusCount := func(kind string) int {
		t.Helper()
		code, body := b.call("GET", app+"/status", "", "")
		var status struct {
			Admin struct{ Actions map[string]int }
		}
		if code != 200 || json.Unmarshal([]byte(body), &status) != nil {
			t.Fatalf("status: %d %s", code, body)
		}
		clean("/status", body)
		if strings.Contains(body, m.ID) {
			t.Fatalf("/status names the mailbox: %s", body)
		}
		return status.Admin.Actions[kind]
	}

	if err := smtpTo(cfg.smtpPort, m.Address); err != nil {
		t.Fatalf("an active mailbox: %v", err)
	}
	found := call("GET", "/v1/admin/mailboxes?address="+strings.ToUpper(m.Address))
	if found.ID != m.ID || found.Account != me.Account || found.State != contracts.MailboxReceiving {
		t.Fatalf("found %+v", found)
	}
	if rec := call("POST", "/v1/admin/mailboxes/"+m.ID+"/pause"); rec.State != contracts.MailboxPaused || rec.PausedAt.IsZero() {
		t.Fatalf("paused %+v", rec)
	}
	if err := smtpTo(cfg.smtpPort, m.Address); err == nil || !strings.Contains(err.Error(), "550") || !strings.Contains(err.Error(), "5.1.1 No such mailbox") {
		t.Fatalf("a paused mailbox at RCPT TO: %v", err)
	}
	if got, err := other.Get(ctx, me, m.ID); err != nil || got.State != contracts.MailboxPaused {
		t.Fatalf("the other service sees %+v %v", got, err)
	}
	if n := statusCount("mailbox_paused"); n != 1 {
		t.Fatalf("/status counts %d pauses", n)
	}
	if rec := call("POST", "/v1/admin/mailboxes/"+m.ID+"/resume"); rec.State != contracts.MailboxReceiving {
		t.Fatalf("resumed %+v", rec)
	}
	if err := smtpTo(cfg.smtpPort, m.Address); err != nil {
		t.Fatalf("a resumed mailbox: %v", err)
	}
	if n := statusCount("mailbox_resumed"); n != 1 {
		t.Fatalf("/status counts %d resumes", n)
	}
	// What the enclave logged names none of it, nor which mailbox.
	out := logs.String()
	clean("the log", out)
	for _, s := range []string{m.ID, me.Account} {
		if strings.Contains(out, s) {
			t.Fatalf("the log holds %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, `"kind":"mailbox_paused"`) || !strings.Contains(out, `"kind":"mailbox_resumed"`) {
		t.Fatalf("the changes are not logged by kind:\n%s", out)
	}
}

// smtpTo sends one short message to address through the dev enclave's SMTP
// port, over STARTTLS: an error says what the server answered.
func smtpTo(port uint32, address string) error {
	var c *smtp.Client
	var err error
	for range 50 {
		if c, err = smtp.Dial(fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil { //nolint:gosec // dev mode self-signed certificate
		return err
	}
	if err := c.Mail("alice@example.org"); err != nil {
		return err
	}
	if err := c.Rcpt(address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, _ = io.WriteString(w, "From: alice@example.org\r\nTo: "+address+"\r\nSubject: hello\r\n\r\nHello.\r\n")
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
