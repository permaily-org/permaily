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
	"net/http/cookiejar"
	"net/smtp"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
)

// mailAnswer is one answer of the app's host: its status, headers and body.
type mailAnswer struct {
	code   int
	header http.Header
	body   string
}

// TestMailEndToEnd (ADR 0026, R9) runs two enclaves of one deployment in dev
// mode, over one storage, and uses mailboxes as a person does: Ana signs up,
// makes two mailboxes through the API, mail with a hostile subject, sender
// and filename arrives over SMTP with STARTTLS through one enclave and is
// listed, searched, opened and downloaded, the original and an attachment,
// through the other; one message is deleted, then many; the mailbox is
// disconnected with a fresh proof and its address refuses mail. Bea, another
// account, gets not_found for everything of Ana's, answered as for what does
// not exist. Every answer has nosniff, every error is JSON with fixed words,
// and the log holds no address, subject, sender, filename, code or
// identifier.
func TestMailEndToEnd(t *testing.T) {
	t.Cleanup(func() { devAhead.Store(0) })
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	const admin = "pma_mail_e2e_token"
	sum := sha256.Sum256([]byte(admin))
	var logs lockedBuffer
	var enclaves []config
	for range 2 {
		cfg := config{
			domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing",
			adminTokenSHA256: hex.EncodeToString(sum[:]),
		}
		enclaves = append(enclaves, cfg)
		go func() {
			_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		}()
	}
	const app = "https://app.permaily.test"
	fixed := map[string]bool{"no such endpoint": true, "internal error": true}
	for _, e := range []*contracts.Error{contracts.ErrUnauthorized, contracts.ErrNotFound, contracts.ErrConfirmationNeeded, contracts.ErrInvalidQuery, contracts.ErrTooManyMessages} {
		fixed[e.Message] = true
	}
	// A browser of its own for each person, whose requests go to one enclave
	// or the other.
	type browser struct{ jar http.CookieJar }
	call := func(b browser, enclave int, method, path, body string) mailAnswer {
		t.Helper()
		client := &http.Client{Jar: b.jar, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", enclaves[enclave].port))
			},
		}}
		url := app + path
		if strings.HasPrefix(path, "https://") {
			url = path
		}
		var res *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequest(method, url, strings.NewReader(body))
			if strings.HasPrefix(url, app) && method != http.MethodGet {
				req.Header.Set("Origin", app)
				req.Header.Set("Content-Type", "application/json")
			}
			if strings.HasPrefix(url, "https://api.") {
				req.Header.Set("Authorization", "Bearer "+admin)
			}
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		a := mailAnswer{code: res.StatusCode, header: res.Header, body: string(out)}
		// What every answer of the mailbox routes keeps for the frame.
		if strings.HasPrefix(path, "/api/v1/mailboxes") {
			if a.header.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s %s: no nosniff", method, path)
			}
			if a.code >= 400 {
				var e struct {
					Error struct{ Code, Message string }
				}
				if a.header.Get("Content-Type") != "application/json" || json.Unmarshal(out, &e) != nil || !fixed[e.Error.Message] {
					t.Errorf("%s %s: error %d %q", method, path, a.code, a.body)
				}
			}
		}
		return a
	}
	newBrowser := func() browser { jar, _ := cookiejar.New(nil); return browser{jar} }
	type sent struct{ To, Subject, Text string }
	// outboxCode waits for the message to the address with the subject (and
	// whose text names attempt, when one is given), in the outbox of the
	// enclave that sent it, and returns its code.
	sixDigits := regexp.MustCompile(`\n    (\d{6})\n`)
	outboxCode := func(enclave int, to, subject, attempt string) string {
		t.Helper()
		for range 50 {
			var out []sent
			_ = json.Unmarshal([]byte(call(newBrowser(), enclave, "GET", "/dev/outbox", "").body), &out)
			for _, m := range out {
				if g := sixDigits.FindStringSubmatch(m.Text); g != nil && m.To == to && m.Subject == subject && strings.Contains(m.Text, attempt) {
					return g[1]
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no message %q", subject)
		return ""
	}
	signUp := func(address string) browser {
		t.Helper()
		b := newBrowser()
		var made struct{ Invitations []string }
		if a := call(b, 0, "POST", "https://api.permaily.test/v1/admin/invitations", `{"count":1}`); a.code != 200 || json.Unmarshal([]byte(a.body), &made) != nil {
			t.Fatalf("invitations: %d %s", a.code, a.body)
		}
		var started struct{ Attempt string }
		a := call(b, 0, "POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`)
		if a.code != 200 || json.Unmarshal([]byte(a.body), &started) != nil {
			t.Fatalf("start: %d %s", a.code, a.body)
		}
		if a := call(b, 1, "POST", "/api/v1/auth/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+outboxCode(0, address, "Sign in to Permaily", started.Attempt)+`"}`); a.code != 200 {
			t.Fatalf("finish: %d %s", a.code, a.body)
		}
		return b
	}
	decode := func(a mailAnswer, status int, v any) {
		t.Helper()
		if a.code != status || (v != nil && json.Unmarshal([]byte(a.body), v) != nil) {
			t.Fatalf("%d %s, want %d", a.code, a.body, status)
		}
	}

	ana, bea := signUp("ana@example.org"), signUp("bea@example.org")
	var work, home contracts.Mailbox
	decode(call(ana, 0, "POST", "/api/v1/mailboxes", `{"label":"Work"}`), 201, &work)
	decode(call(ana, 1, "POST", "/api/v1/mailboxes", `{"label":"Home"}`), 201, &home)
	var list contracts.MailboxList
	decode(call(ana, 1, "GET", "/api/v1/mailboxes", ""), 200, &list)
	if len(list.Mailboxes) != 2 || list.Mailboxes[0].ID != work.ID || list.Mailboxes[1].Label != "Home" || work.State != contracts.MailboxWaiting {
		t.Fatalf("%+v", list)
	}
	var beas contracts.Mailbox
	decode(call(bea, 0, "POST", "/api/v1/mailboxes", `{"label":"Bea"}`), 201, &beas)

	// Mail arrives over SMTP with STARTTLS, through the first enclave.
	const hostile = "<img src=x onerror=alert(1)> Booking"
	ticket := func(to, subject, text string) string {
		return "From: \"<b>Alice</b>\" <alice@example.org>\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
			"--b\r\nContent-Type: text/plain\r\n\r\n" + text + "\r\n" +
			"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"../<script>evil.pdf\"\r\n\r\n%PDF\r\n--b--\r\n"
	}
	send := func(enclave int, raw string, rcpts ...string) error {
		var c *smtp.Client
		var err error
		for range 50 {
			if c, err = smtp.Dial(fmt.Sprintf("127.0.0.1:%d", enclaves[enclave].smtpPort)); err == nil {
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
		for _, r := range rcpts {
			if err := c.Rcpt(r); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		_, _ = io.WriteString(w, raw)
		if err := w.Close(); err != nil {
			return err
		}
		return c.Quit()
	}
	first := ticket(work.Address, hostile, "See you on Friday at gate zebra.")
	if err := send(0, first, work.Address, home.Address); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := send(0, ticket(work.Address, fmt.Sprintf("Receipt %d", i), fmt.Sprintf("Receipt number %d.", i)), work.Address); err != nil {
			t.Fatal(err)
		}
	}

	// Read through the second enclave.
	base := "/api/v1/mailboxes/" + work.ID
	var page contracts.MessagePage
	decode(call(ana, 1, "GET", base+"/messages", ""), 200, &page)
	if len(page.Messages) != 4 || page.Messages[3].Subject != hostile || page.Messages[0].Subject != "Receipt 2" || page.Next != "" {
		t.Fatalf("%+v", page)
	}
	msg := page.Messages[3].ID
	decode(call(ana, 1, "GET", base+"/messages?limit=2", ""), 200, &page)
	if len(page.Messages) != 2 || page.Next == "" {
		t.Fatalf("%+v", page)
	}
	decode(call(ana, 1, "GET", base+"/messages?limit=2&after="+page.Next, ""), 200, &page)
	if len(page.Messages) != 2 || page.Messages[1].ID != msg {
		t.Fatalf("%+v", page)
	}
	decode(call(ana, 0, "GET", base+"/messages?q=zebra+friday", ""), 200, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != msg || page.SearchedTo.IsZero() {
		t.Fatalf("search: %+v", page)
	}
	decode(call(ana, 1, "GET", base+"/messages?q=nothing-like-this", ""), 200, &page)
	if len(page.Messages) != 0 || page.SearchedTo.IsZero() {
		t.Fatalf("search for nothing: %+v", page)
	}
	if a := call(ana, 1, "GET", base+"/messages?q=", ""); a.code != 400 {
		t.Fatalf("an empty query: %d", a.code)
	}
	var m contracts.Message
	decode(call(ana, 1, "GET", base+"/messages/"+msg, ""), 200, &m)
	if m.Subject != hostile || m.Text != "See you on Friday at gate zebra." || m.TextFrom != contracts.TextFromPlain ||
		len(m.Attachments) != 1 || m.Attachments[0].Name != "../<script>evil.pdf" || len(m.From) != 1 || m.From[0].Name != "<b>Alice</b>" {
		t.Fatalf("%+v", m)
	}
	raw := call(ana, 0, "GET", base+"/messages/"+msg+"/raw", "")
	if raw.code != 200 || raw.body != first || raw.header.Get("Content-Type") != "message/rfc822" ||
		raw.header.Get("Content-Disposition") != `attachment; filename="`+msg+`.eml"; filename*=UTF-8''`+msg+`.eml` ||
		raw.header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("the original: %d %v", raw.code, raw.header)
	}
	att := call(ana, 1, "GET", base+"/messages/"+msg+"/attachments/0", "")
	if att.code != 200 || att.body != "%PDF" || att.header.Get("Content-Type") != "application/octet-stream" ||
		att.header.Get("Content-Disposition") != `attachment; filename="scriptevil.pdf"; filename*=UTF-8''scriptevil.pdf` {
		t.Fatalf("the attachment: %d %q %v", att.code, att.body, att.header)
	}
	if a := call(ana, 1, "GET", base+"/messages/"+msg+"/attachments/1", ""); a.code != 404 {
		t.Fatalf("an attachment that is not there: %d", a.code)
	}
	decode(call(ana, 0, "GET", "/api/v1/mailboxes/"+work.ID, ""), 200, &work)
	if work.State != contracts.MailboxReceiving {
		t.Fatalf("%+v", work)
	}

	// Bea: everything of Ana's is not_found, answered as for what does not
	// exist; Ana's message through Bea's own mailbox too.
	never := "mbx_" + strings.Repeat("2", len(work.ID)-len("mbx_"))
	neverMsg := strings.Repeat("0", 31) + "1"
	for _, r := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"PATCH", "", `{"label":"Mine"}`}, {"GET", "/messages", ""}, {"GET", "/messages?q=zebra", ""},
		{"GET", "/messages/" + msg, ""}, {"GET", "/messages/" + msg + "/raw", ""}, {"GET", "/messages/" + msg + "/attachments/0", ""},
		{"POST", "/messages/delete", `{"ids":["` + msg + `"]}`}, {"DELETE", "/messages/" + msg, ""}, {"DELETE", "", ""},
	} {
		for enclave := range 2 {
			theirs := call(bea, enclave, r.method, "/api/v1/mailboxes/"+work.ID+r.path, r.body)
			none := call(bea, enclave, r.method, "/api/v1/mailboxes/"+never+r.path, r.body)
			if theirs.code != 404 || !strings.Contains(theirs.body, `"not_found"`) || theirs.body != none.body ||
				theirs.header.Get("Content-Type") != none.header.Get("Content-Type") || len(theirs.header) != len(none.header) {
				t.Errorf("Bea, %s %s: %d %s and %d %s", r.method, r.path, theirs.code, theirs.body, none.code, none.body)
			}
			if strings.Contains(r.path, msg) {
				through := call(bea, enclave, r.method, "/api/v1/mailboxes/"+beas.ID+r.path, r.body)
				absent := call(bea, enclave, r.method, "/api/v1/mailboxes/"+beas.ID+strings.Replace(r.path, msg, neverMsg, 1), r.body)
				if through.code != 404 || through.body != absent.body {
					t.Errorf("Bea, through hers, %s %s: %d %s and %d %s", r.method, r.path, through.code, through.body, absent.code, absent.body)
				}
			}
		}
	}
	decode(call(bea, 0, "GET", "/api/v1/mailboxes", ""), 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != beas.ID {
		t.Fatalf("Bea's mailboxes: %+v", list)
	}
	// Ana's mail is all still there.
	decode(call(ana, 0, "GET", base+"/messages", ""), 200, &page)
	if len(page.Messages) != 4 {
		t.Fatalf("after Bea: %+v", page)
	}

	// Deleting one, then many.
	ids := []string{page.Messages[0].ID, page.Messages[1].ID, page.Messages[2].ID}
	if a := call(ana, 0, "DELETE", base+"/messages/"+ids[0], ""); a.code != 204 {
		t.Fatalf("delete one: %d %s", a.code, a.body)
	}
	if a := call(ana, 1, "DELETE", base+"/messages/"+ids[0], ""); a.code != 404 {
		t.Fatalf("the same again: %d", a.code)
	}
	var deleted contracts.MessagesDeleted
	decode(call(ana, 1, "POST", base+"/messages/delete", `{"ids":["`+ids[0]+`","`+ids[1]+`","`+ids[2]+`","`+neverMsg+`"]}`), 200, &deleted)
	if deleted.Deleted != 2 {
		t.Fatalf("%+v", deleted)
	}
	decode(call(ana, 0, "GET", base+"/messages", ""), 200, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != msg {
		t.Fatalf("after deleting: %+v", page)
	}
	decode(call(ana, 0, "GET", base+"/messages?q=receipt", ""), 200, &page)
	if len(page.Messages) != 0 {
		t.Fatalf("deleted mail found: %+v", page)
	}

	// Disconnecting needs a fresh proof: eleven minutes after signing in, the
	// session confirms again with a code by mail.
	devAhead.Store(int64(11 * time.Minute))
	if a := call(ana, 1, "DELETE", base, ""); a.code != 403 || !strings.Contains(a.body, `"confirmation_needed"`) {
		t.Fatalf("disconnecting without a fresh proof: %d %s", a.code, a.body)
	}
	var started struct{ Attempt string }
	decode(call(ana, 0, "POST", "/api/v1/auth/confirm/email/start", ""), 200, &started)
	decode(call(ana, 1, "POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+outboxCode(0, "ana@example.org", "Confirm it is you on Permaily", "")+`"}`), 200, nil)
	if a := call(ana, 1, "DELETE", base, ""); a.code != 204 {
		t.Fatalf("disconnect: %d %s", a.code, a.body)
	}
	if a := call(ana, 0, "DELETE", base, ""); a.code != 204 {
		t.Fatalf("disconnecting again before the sweep's last pass: %d %s", a.code, a.body)
	}
	for enclave := range 2 {
		if err := send(enclave, ticket(work.Address, "After", "after"), work.Address); err == nil || !strings.Contains(err.Error(), "550") {
			t.Fatalf("the disconnected address through enclave %d: %v", enclave, err)
		}
		if a := call(ana, enclave, "GET", base+"/messages/"+msg, ""); a.code != 404 {
			t.Fatalf("its mail after the disconnect: %d", a.code)
		}
	}
	decode(call(ana, 0, "GET", "/api/v1/mailboxes", ""), 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != home.ID {
		t.Fatalf("after the disconnect: %+v", list)
	}
	// The other mailbox has its copy and still takes mail.
	if err := send(1, ticket(home.Address, "Still here", "still"), home.Address); err != nil {
		t.Fatal(err)
	}
	decode(call(ana, 1, "GET", "/api/v1/mailboxes/"+home.ID+"/messages", ""), 200, &page)
	if len(page.Messages) != 2 {
		t.Fatalf("home: %+v", page)
	}
	if a := call(newBrowser(), 0, "GET", "/api/v1/mailboxes/"+home.ID+"/messages", ""); a.code != 401 {
		t.Fatalf("without a session: %d", a.code)
	}

	out := logs.String()
	if !strings.Contains(out, "mailboxes: a message was stored") {
		t.Fatalf("the log does not record the deliveries:\n%s", out)
	}
	for _, secret := range []string{"alice", "ana@", "bea@", "example.org", work.Address, home.Address, beas.Address, work.ID, home.ID, beas.ID, msg,
		"Booking", "Friday", "zebra", "Receipt", "onerror", "evil", "script", started.Attempt} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log contains %q", secret)
		}
	}
	for _, id := range regexp.MustCompile(`acc_[a-z0-9]{26}`).FindAllString(out, -1) {
		t.Fatalf("the log contains an account %s", id)
	}
}
