package accounts

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain/mail"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/notices"
)

// update writes the golden files of the messages and the fixtures of the
// mail's preview again, from the code:
//
//	go test ./internal/services/accounts -run 'TestEveryMessageReadsAsItsGoldenText|TestThePreviewShowsWhatTheServiceSends' -update
var update = flag.Bool("update", false, "write the golden files of the messages again")

// Sample values of the messages: nobody's address, a made-up code and token.
const (
	sampleTo      = "ada@example.com"
	sampleNewTo   = "ada.lovelace@example.com"
	sampleAppURL  = "https://app.permaily.com"
	sampleApp     = "app.permaily.com"
	sampleAttempt = "attempt-example"
	sampleCode    = "428913"
	sampleToken   = "link-example"
	// A browser the list of sessions names "Firefox on Mac".
	sampleBrowser = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.5; rv:132.0) Gecko/20100101 Firefox/132.0"
)

var sampleAt = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

// sampleMail is one message the service sends, in one of its variants.
type sampleMail struct {
	name string
	mail Mail
}

// sampleMails is every message the service sends, every variant of each, with
// sample values. A message added to the service is added here: its golden
// text, the checks of its HTML and its preview come from this list.
func sampleMails() []sampleMail {
	closing := func(by string, blocked bool) *accountDoc {
		d := &accountDoc{Address: sampleTo, State: contracts.AccountClosing, ClosingBy: by, ClosesAt: sampleAt.Add(7 * 24 * time.Hour)}
		if blocked {
			d.BlockedAt = sampleAt.Add(-time.Hour)
		}
		return d
	}
	address := addressMails(sampleTo, sampleNewTo, sampleAt)
	list := []sampleMail{
		{"sign-in", signInMail(sampleTo, sampleAppURL, sampleAttempt, sampleCode, sampleToken)},
		{"sign-in-link-only", signInMail(sampleTo, sampleAppURL, sampleAttempt, "", sampleToken)},
		{"confirm", confirmMail(sampleTo, sampleApp, sampleCode)},
		{"address-code", addressCodeMail(sampleNewTo, sampleCode)},
		{"new-device-by-mail", newDeviceMail(sampleTo, sampleApp, sampleAt, wayMail, sampleBrowser)},
		{"new-device-by-passkey", newDeviceMail(sampleTo, sampleApp, sampleAt, wayPasskey, sampleBrowser)},
		{"new-device-by-recovery-code", newDeviceMail(sampleTo, sampleApp, sampleAt, wayRecovery, sampleBrowser)},
		{"new-device-by-provider", newDeviceMail(sampleTo, sampleApp, sampleAt, wayProvider(idp.Google), sampleBrowser)},
		{"new-device-not-recognised", newDeviceMail(sampleTo, sampleApp, sampleAt, wayMail, "")},
		{"address-changed-old", address[0]},
		{"address-changed-new", address[1]},
		{"deletion-asked-by-person", deletionAskedMail(closing(contracts.ClosingByPerson, false), sampleApp, sampleAt)},
		{"deletion-asked-by-operator", deletionAskedMail(closing(contracts.ClosingByOperator, false), sampleApp, sampleAt)},
		{"deletion-asked-blocked", deletionAskedMail(closing(contracts.ClosingByOperator, true), sampleApp, sampleAt)},
		{"deletion-cancelled-by-person", deletionCancelledMail(sampleTo, sampleApp, sampleAt, contracts.ClosingByPerson)},
		{"deletion-cancelled-by-operator", deletionCancelledMail(sampleTo, sampleApp, sampleAt, contracts.ClosingByOperator)},
		{"blocked", blockedMail(sampleTo, sampleApp, sampleAt, true)},
		{"unblocked", blockedMail(sampleTo, sampleApp, sampleAt, false)},
		{"admin-made", adminMail(sampleTo, sampleApp, sampleAt, true)},
		{"admin-removed", adminMail(sampleTo, sampleApp, sampleAt, false)},
		// The notices about mailboxes (ADR 0026, "Notices by mail").
		{"mailbox-added", mailboxMail(sampleTo, sampleApp, mailboxes.NoticeAdded, sampleAt)},
		{"forwarding-asked", mailboxMail(sampleTo, sampleApp, mailboxes.NoticeForwarding, sampleAt)},
		{"mailbox-disconnected", mailboxMail(sampleTo, sampleApp, mailboxes.NoticeDisconnected, sampleAt)},
		{"mailbox-paused", mailboxMail(sampleTo, sampleApp, mailboxes.NoticePaused, sampleAt)},
		{"mailbox-resumed", mailboxMail(sampleTo, sampleApp, mailboxes.NoticeResumed, sampleAt)},
	}
	for _, kind := range []string{noticePasskeyAdded, noticePasskeyRemoved, noticePasskeyOnlyOn, noticePasskeyOnlyOff, noticeRecoveryCodesNew, noticeRecoveryCodeUsed} {
		list = append(list, sampleMail{"sign-in-change-" + kind, signInChangeMail(sampleTo, sampleApp, sampleAt, signInChanges[kind])})
	}
	for _, p := range []idp.Provider{idp.Google, idp.GitHub, idp.Meta, idp.Microsoft} {
		list = append(list,
			sampleMail{"provider-linked-" + string(p), providerMail(sampleTo, sampleApp, sampleAt, p, true)},
			sampleMail{"provider-unlinked-" + string(p), providerMail(sampleTo, sampleApp, sampleAt, p, false)})
	}
	return append(list, connectionSamples()...)
}

// connectionSamples are the notices about connections and the record of what
// left (ADR 0041, "Templates and previews"), in every variant: a vendor
// verified and not, a connection new and replacing the client's, through the
// API and MCP, each set of parts, and the operator and an admin.
func connectionSamples() []sampleMail {
	var list []sampleMail
	add := func(name string, n notices.Notice) {
		m, err := connectionNoticeMail(sampleTo, sampleApp, n)
		if err != nil {
			panic(name + ": " + err.Error())
		}
		list = append(list, sampleMail{name, m})
	}
	vendors := []struct{ name, domain string }{{"verified", "claude.ai"}, {"unverified", "permaily-security.example"}}
	for _, v := range vendors {
		for _, replaced := range []bool{false, true} {
			for _, r := range []contracts.Resource{contracts.ResourceAPI, contracts.ResourceMCP} {
				n := sampleNotice(notices.KindConnectionApproved, v.domain)
				n.Facts.Replaced, n.Facts.Resource = replaced, r
				how := "new"
				if replaced {
					how = "replaced"
				}
				add("connection-approved-"+v.name+"-"+how+"-"+string(r), n)
			}
		}
	}
	// Every set of parts a connection reads: the sender always, and any of
	// the other three.
	others := []contracts.Part{contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}
	for set := range 1 << len(others) {
		parts := []contracts.Part{contracts.PartSender}
		for i, p := range others {
			if set&(1<<i) != 0 {
				parts = append(parts, p)
			}
		}
		if len(parts) == 4 {
			continue // the variants above read every part
		}
		n := sampleNotice(notices.KindConnectionApproved, "claude.ai")
		n.Facts.Parts = parts
		name := "connection-approved-reads"
		for _, p := range parts {
			name += "-" + string(p)
		}
		add(name, n)
	}
	for _, kind := range []notices.Kind{notices.KindDeviceAdded, notices.KindConnectionRenewed, notices.KindConnectionEnding, notices.KindDeviceEndedReuse} {
		for _, v := range vendors {
			add(strings.ReplaceAll(string(kind), "_", "-")+"-"+v.name, sampleNotice(kind, v.domain))
		}
	}
	add("receipts-incident", sampleNotice(notices.KindReceiptsIncident, ""))
	for _, by := range []string{notices.ByOperator, notices.ByAdmin} {
		n := sampleNotice(notices.KindDevicesSignedOut, "")
		n.Facts.By = by
		add("devices-signed-out-by-"+by, n)
	}
	return list
}

// TestEveryMessageReadsAsItsGoldenText holds the words of every message: the
// plain text of each, as it is sent, is the golden file's, byte for byte.
func TestEveryMessageReadsAsItsGoldenText(t *testing.T) {
	samples := sampleMails()
	if len(samples) != 65 {
		t.Fatalf("%d samples, want 65: a message added or removed changes this count and the golden files together", len(samples))
	}
	names := map[string]bool{}
	for _, s := range samples {
		if names[s.name] {
			t.Fatalf("two samples named %s", s.name)
		}
		names[s.name] = true
		got := "To: " + s.mail.To + "\nSubject: " + s.mail.Subject + "\n\n" + s.mail.Text
		path := filepath.Join("testdata", "mail", s.name+".txt")
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path) //nolint:gosec // G304: a file of this package's testdata
		if err != nil {
			t.Fatalf("%s: %v (write it with -update)", s.name, err)
		}
		if got != string(want) {
			t.Errorf("%s reads differently from its golden file:\n--- got\n%s\n--- want\n%s", s.name, got, want)
		}
	}
	files, err := filepath.Glob(filepath.Join("testdata", "mail", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(samples) {
		t.Errorf("%d golden files for %d messages: remove the ones no message has", len(files), len(samples))
	}
}

// fixtures is where the preview of the mail (packages/mail) and the checks of
// its HTML (internal/domain/mail) read every message the service sends.
var fixtures = filepath.Join("..", "..", "..", "packages", "mail", "fixtures")

// fixture is a message as the preview reads it.
type fixture struct {
	Name    string       `json:"name"`
	To      string       `json:"to"`
	Subject string       `json:"subject"`
	Blocks  []mail.Block `json:"blocks"`
}

// TestThePreviewShowsWhatTheServiceSends: the fixtures of the preview, and
// of the checks of the HTML, are every message of sampleMails as the service
// writes it, and nothing else.
func TestThePreviewShowsWhatTheServiceSends(t *testing.T) {
	samples := sampleMails()
	for _, s := range samples {
		got, err := json.MarshalIndent(fixture{s.name, s.mail.To, s.mail.Subject, s.mail.Blocks}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		path := filepath.Join(fixtures, s.name+".json")
		if *update {
			if err := os.WriteFile(path, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path) //nolint:gosec // G304: a fixture of this repository
		if err != nil {
			t.Fatalf("%s: %v (write it with -update)", s.name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the fixture is not what the service writes; run the test with -update", s.name)
		}
	}
	files, err := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(samples) {
		t.Errorf("%d fixtures for %d messages: remove the ones no message has", len(files), len(samples))
	}
}

// TestEveryMessageIsTextAndHTMLFromItsBlocks: the words of a message are its
// blocks, and both parts are written from them.
func TestEveryMessageIsTextAndHTMLFromItsBlocks(t *testing.T) {
	for _, s := range sampleMails() {
		if len(s.mail.Blocks) == 0 || s.mail.Text != mail.Text(s.mail.Blocks) {
			t.Errorf("%s: the text is not its blocks'", s.name)
		}
		if s.mail.HTML == "" || s.mail.HTML != mail.HTML(s.mail.Subject, s.mail.Blocks) {
			t.Errorf("%s: no HTML, or not its blocks'", s.name)
		}
	}
}
