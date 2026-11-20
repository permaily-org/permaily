package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain/mail"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

// sampleAccount is the account of noticestest.Sample's notices.
const sampleAccount = "acc_ana"

// noticeWorld is a world with the account of noticestest.Sample's notices,
// open, its terms agreed, made in its storage as a sign-up makes one, at
// noticestest.SampleAt.
func noticeWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.now = noticestest.SampleAt
	doc := &accountDoc{
		V: 1, ID: sampleAccount, State: contracts.AccountActive, Address: "ana@example.org", Language: "en",
		CreatedAt: w.now, AddressVerifiedAt: w.now, Terms: &termsDoc{Version: w.terms, At: w.now},
	}
	if err := w.svc.makeAccount(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	return w
}

// edit changes the account's record in its storage, as a change of the
// service would.
func (w *world) edit(id string, change func(*accountDoc)) {
	w.t.Helper()
	ctx := context.Background()
	doc, at, err := w.svc.readAt(ctx, id)
	if err != nil || doc == nil {
		w.t.Fatalf("%+v %v", doc, err)
	}
	change(doc)
	if err := w.svc.putAccountAt(ctx, doc, at); err != nil {
		w.t.Fatal(err)
	}
}

// failsReads makes every read of the account's record end with what fail
// answers.
func (w *world) failsReads(id string, fail func(ctx context.Context) error) {
	w.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		// The anchored store keeps each version under the name and "@".
		if step == store.StepRead && strings.HasPrefix(key, "record#"+recordName(id)) {
			return fail(ctx)
		}
		return nil
	}
}

// play puts the account of Sample's notices in standing s, in accounts' own
// storage.
func (w *world) play(s noticestest.Standing) {
	w.t.Helper()
	ctx := context.Background()
	name := recordName(sampleAccount)
	switch s {
	case noticestest.StandingOpen:
	case noticestest.StandingNotAgreed:
		w.edit(sampleAccount, func(d *accountDoc) { d.Terms = nil })
	case noticestest.StandingGone:
		if err := w.anchored.RemoveRecord(ctx, name); err != nil {
			w.t.Fatal(err)
		}
	case noticestest.StandingClosing:
		w.edit(sampleAccount, func(d *accountDoc) {
			d.State, d.ClosingBy, d.ClosesAt = contracts.AccountClosing, contracts.ClosingByPerson, w.now.Add(7*24*time.Hour)
		})
	case noticestest.StandingBlocked:
		w.edit(sampleAccount, func(d *accountDoc) { d.BlockedAt = w.now })
	case noticestest.StandingUnreadable:
		w.failsReads(sampleAccount, func(context.Context) error { return errors.New("the storage did not answer") })
	case noticestest.StandingTimeout:
		w.svc.deliverFor = 50 * time.Millisecond
		w.failsReads(sampleAccount, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	case noticestest.StandingRefused:
		w.failsReads(sampleAccount, func(context.Context) error {
			return errors.New("the storage refused the request: its authentication failed")
		})
	case noticestest.StandingLost:
		for _, stored := range w.mem.RecordNames() {
			if strings.HasPrefix(stored, name+"@") {
				if err := w.mem.DeleteRecord(ctx, stored); err != nil {
					w.t.Fatal(err)
				}
			}
		}
	case noticestest.StandingForked:
		head, _, err := w.commits.Head(ctx, name)
		if err != nil {
			w.t.Fatal(err)
		}
		w.commits.Fork(name, head.Version, [32]byte{1})
	case noticestest.StandingMalformed:
		// Signed by the service and committed, but not an account's.
		_, at, err := w.svc.readAt(ctx, sampleAccount)
		if err != nil {
			w.t.Fatal(err)
		}
		key, err := w.svc.signingKey(ctx)
		if err != nil {
			w.t.Fatal(err)
		}
		value, err := envelope.Sign(key, []byte(`{"id":"acc_other","state":"active","address":"eve@example.org"}`), recordAAD(name))
		if err != nil {
			w.t.Fatal(err)
		}
		if err := w.anchored.PutRecordAt(ctx, &store.Record{Name: name, Value: value, Plain: true, UpdatedAt: w.now}, at); err != nil {
			w.t.Fatal(err)
		}
	case noticestest.StandingUnknown:
		w.edit(sampleAccount, func(d *accountDoc) { d.State = "suspended" })
	default:
		w.t.Fatalf("standing %d is not played", s)
	}
}

// E25 (accounts' half): accounts' Deliver keeps the contract every
// Deliverer keeps, each standing played in accounts' own storage.
func TestE25DeliverKeepsTheDelivererContract(t *testing.T) {
	noticestest.DelivererContract(t, func(t *testing.T) noticestest.DelivererSubject {
		w := noticeWorld(t)
		return noticestest.DelivererSubject{
			Deliverer: w.svc,
			Set: func(s noticestest.Standing) {
				w.mem.Interrupt = nil
				if s == noticestest.StandingOpen {
					return
				}
				w.play(s)
			},
			Sent: func() int { return len(w.out.Sent()) },
		}
	})
}

// E25: the address is read when the notice is sent, from the account's
// record, never from the queue: an address changed after it was queued gets
// it, and the queue holds none.
func TestE25TheAddressIsReadWhenSending(t *testing.T) {
	w, ctx := noticeWorld(t), context.Background()
	q := &noticestest.Fake{Deliverer: w.svc, Now: func() time.Time { return w.now }}
	n := noticestest.Sample("rcp_1")
	if err := q.Enqueue(ctx, n); err != nil {
		t.Fatal(err)
	}
	queued, err := json.Marshal(q.Queued())
	if err != nil || strings.Contains(string(queued), "@") {
		t.Fatalf("the queue holds an address: %s %v", queued, err)
	}
	w.edit(sampleAccount, func(d *accountDoc) { d.Address = "ana.new@example.org" })
	if err := q.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	sent := w.out.Sent()
	if len(sent) != 1 || sent[0].To != "ana.new@example.org" || sent[0].Subject != "A connection to your mail was approved" {
		t.Fatalf("sent %+v", sent)
	}
}

// E25: a notice is said in the words of its kind, and when the account is
// due to be removed, it is not told: ErrSkip.
func TestE25AnAccountDueToBeRemovedIsNotTold(t *testing.T) {
	w := noticeWorld(t)
	w.edit(sampleAccount, func(d *accountDoc) {
		d.State, d.ClosingBy, d.ClosesAt = contracts.AccountClosing, contracts.ClosingByPerson, w.now.Add(time.Hour)
	})
	w.now = w.now.Add(time.Hour)
	n := noticestest.Sample("rcp_1")
	n.At = w.now
	if err := w.svc.Deliver(context.Background(), n); !errors.Is(err, notices.ErrSkip) || len(w.out.Sent()) != 0 {
		t.Fatalf("Deliver = %v, sent %d", err, len(w.out.Sent()))
	}
}

// E25: an account without a contact address is a standing Deliver does not
// know, and a mail system that fails is tried again; neither error holds
// the address.
func TestE25DeliverTriesAgainWhatItCannotSend(t *testing.T) {
	ctx := context.Background()
	w := noticeWorld(t)
	w.edit(sampleAccount, func(d *accountDoc) { d.Address = "" })
	err := w.svc.Deliver(ctx, noticestest.Sample("rcp_1"))
	if err == nil || errors.Is(err, notices.ErrSkip) || errors.Is(err, notices.ErrInvalid) {
		t.Fatalf("Deliver to no address = %v", err)
	}
	w = noticeWorld(t)
	w.svc.cfg.Mailer = failingMailer{}
	err = w.svc.Deliver(ctx, noticestest.Sample("rcp_1"))
	if err == nil || errors.Is(err, notices.ErrSkip) || strings.Contains(err.Error(), "@") {
		t.Fatalf("Deliver through a failing mail system = %v", err)
	}
	w = noticeWorld(t)
	w.svc.cfg.Records = noHistory{w.svc.cfg.Records}
	if err := w.svc.Deliver(ctx, noticestest.Sample("rcp_1")); err == nil || errors.Is(err, notices.ErrSkip) {
		t.Fatalf("Deliver over a store without histories = %v", err)
	}
}

// failingMailer is a mail system that refuses every message, saying the
// address, as SES's errors do.
type failingMailer struct{}

func (failingMailer) Send(_ context.Context, m Mail) error {
	return errors.New("the mail system refused " + m.To)
}

// E25: the words know every kind of notices.Kinds, and nothing else: a kind,
// part, resource or signer they do not know is ErrInvalid, never worded,
// even when Check would let it through.
func TestE25TheWordsKnowOnlyTheirKinds(t *testing.T) {
	for _, kind := range notices.Kinds() {
		if _, _, err := connectionNotice(sampleApp, sampleNotice(kind, "claude.ai")); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	for name, change := range map[string]func(*notices.Notice){
		"kind":     func(n *notices.Notice) { n.Kind = "webhook_failing" },
		"part":     func(n *notices.Notice) { n.Facts.Parts = []contracts.Part{contracts.PartSender, "everything"} },
		"twice":    func(n *notices.Notice) { n.Facts.Parts = []contracts.Part{contracts.PartSender, contracts.PartSender} },
		"no parts": func(n *notices.Notice) { n.Facts.Parts = nil },
		"resource": func(n *notices.Notice) { n.Facts.Resource = "smtp" },
	} {
		n := sampleNotice(notices.KindConnectionApproved, "claude.ai")
		change(&n)
		if _, _, err := connectionNotice(sampleApp, n); !errors.Is(err, notices.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	added := sampleNotice(notices.KindDeviceAdded, "claude.ai")
	added.Facts.Parts = []contracts.Part{"everything"}
	if _, _, err := connectionNotice(sampleApp, added); !errors.Is(err, notices.ErrInvalid) {
		t.Errorf("a device added reading an unknown part: %v", err)
	}
	signedOut := sampleNotice(notices.KindDevicesSignedOut, "")
	signedOut.Facts.By = "<a href=x>"
	if _, _, err := connectionNotice(sampleApp, signedOut); !errors.Is(err, notices.ErrInvalid) {
		t.Errorf("an unknown signer: %v", err)
	}
}

// sampleNotice is a valid notice of kind for the sample account, about
// vendor when its kind names one, with every part and through MCP when it
// has them.
func sampleNotice(kind notices.Kind, vendor string) notices.Notice {
	n := notices.Notice{Account: sampleAccount, Kind: kind, Key: notices.Key(sampleAccount, kind, "sample"), At: sampleAt}
	all := []contracts.Part{contracts.PartAttachments, contracts.PartBody, contracts.PartSubject, contracts.PartSender}
	ends := sampleAt.AddDate(0, 0, 90)
	switch kind {
	case notices.KindConnectionApproved:
		n.Facts = notices.Facts{Vendor: vendor, Resource: contracts.ResourceMCP, Parts: all, EndsAt: ends}
	case notices.KindDeviceAdded:
		n.Facts = notices.Facts{Vendor: vendor, Parts: all, EndsAt: ends}
	case notices.KindConnectionRenewed:
		n.Facts = notices.Facts{Vendor: vendor, EndsAt: ends}
	case notices.KindConnectionEnding:
		n.Facts = notices.Facts{Vendor: vendor, EndsAt: sampleAt.AddDate(0, 0, 7)}
	case notices.KindDeviceEndedReuse:
		n.Facts = notices.Facts{Vendor: vendor}
	case notices.KindDevicesSignedOut:
		n.Facts = notices.Facts{By: notices.ByOperator}
	}
	return n
}

// E35: the operator and an admin signing every device out of an account
// queue devices_signed_out, once per count of sign-outs, saying who; a
// block, which signs every device out too but has its own notice, queues
// nothing.
func TestE35SigningEveryDeviceOutIsTold(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	q := &noticestest.Fake{Deliverer: w.svc, Now: func() time.Time { return w.now }}
	w.svc.cfg.Notices = q
	boss := w.account(superAdmin, true)
	ana, _ := w.signUp("ana@example.org")
	id := ana.Account.ID
	before := len(w.out.Sent())

	w.operator(w.svc.CloseAccountSessions, id)
	if _, err := w.as(boss).CloseAccountSessions(ctx, id); err != nil {
		t.Fatal(err)
	}
	w.operator(w.svc.BlockAccount, id)
	w.operator(w.svc.UnblockAccount, id)

	queued := q.Queued()
	if len(queued) != 2 {
		t.Fatalf("queued %+v, want the two sign-outs", queued)
	}
	for i, by := range []string{notices.ByOperator, notices.ByAdmin} {
		n := queued[i]
		if n.Kind != notices.KindDevicesSignedOut || n.Account != id || n.Facts.By != by || !n.At.Equal(w.now) ||
			n.Key != notices.Key(id, notices.KindDevicesSignedOut, string(rune('1'+i))) {
			t.Errorf("notice %d: %+v", i, n)
		}
	}
	// Queued again by the same count is the same notice.
	if err := q.Enqueue(ctx, queued[0]); err != nil || len(q.Queued()) != 2 {
		t.Fatalf("%v %d", err, len(q.Queued()))
	}
	if err := q.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var told []Mail
	for _, m := range w.out.Sent()[before:] {
		if m.Subject == "Every device was signed out of your Permaily account" {
			told = append(told, m)
		}
	}
	if len(told) != 2 || !strings.HasPrefix(told[0].Text, "Permaily's operator signed every device out") ||
		!strings.HasPrefix(told[1].Text, "An admin of Permaily signed every device out") || told[0].To != "ana@example.org" {
		t.Fatalf("told %+v", told)
	}
}

// E35, E36: a queue that cannot be written fails no sign-out, holds it no
// longer than notices.EnqueueMax, and is logged by the notice's kind alone.
func TestE35AQueueThatFailsFailsNoSignOut(t *testing.T) {
	w := newWorld(t)
	w.svc.cfg.Notices = &noticestest.Fake{Fail: errors.New("the storage refused acc_secret"), Now: func() time.Time { return w.now }}
	ana, _ := w.signUp("ana@example.org")
	if rec := w.operator(w.svc.CloseAccountSessions, ana.Account.ID); rec.Sessions != 0 {
		t.Fatalf("%+v", rec)
	}
	logs := w.logs.String()
	if !strings.Contains(logs, "a notice could not be queued") || !strings.Contains(logs, "kind=devices_signed_out") ||
		strings.Contains(logs, ana.Account.ID) || strings.Contains(logs, "secret") || strings.Contains(logs, "@") {
		t.Fatalf("logs:\n%s", logs)
	}
}

// E38: no notice about a connection holds a link, a code, a token, an
// identifier, a client name, an unverified domain, a query or a sentence,
// whatever a hostile vendor's domain says; a verified vendor is named by its
// owner and its domain alone.
func TestE38NoticesAboutConnectionsHoldNoCodeLinkOrToken(t *testing.T) {
	ctx := context.Background()
	hostile := []string{"permaily-security.example", "xn--pymal-ksa.com", "login.permaily.com.evil.example", "a1234567.example"}
	verified := map[string]string{"claude.ai": "Anthropic", "chatgpt.com": "OpenAI"}
	var (
		link   = regexp.MustCompile(`(?i)://|https?:|www\.|[#?&=]|token|attempt`)
		digits = regexp.MustCompile(`\d{5,}`)
		ident  = regexp.MustCompile(`\b(acc|con|fam|rcp|mbx|ses|dev)_`)
		long   = regexp.MustCompile(`[A-Za-z0-9_-]{25,}`)
		names  = regexp.MustCompile(`[a-z0-9-]+(\.[a-z0-9-]+)*\.[a-z]{2,}`)
	)
	for _, kind := range notices.Kinds() {
		for _, vendor := range append(append([]string{}, hostile...), "claude.ai", "chatgpt.com") {
			w := noticeWorld(t)
			n := sampleNotice(kind, vendor)
			n.At = w.now
			if !n.Facts.EndsAt.IsZero() {
				n.Facts.EndsAt = w.now.Add(n.Facts.EndsAt.Sub(sampleAt))
			}
			if kind == notices.KindReceiptsIncident || kind == notices.KindDevicesSignedOut {
				n.Facts.Vendor = ""
			}
			if err := w.svc.Deliver(ctx, n); err != nil {
				t.Fatalf("%s %s: %v", kind, vendor, err)
			}
			sent := w.out.Sent()
			if len(sent) != 1 {
				t.Fatalf("%s: sent %d", kind, len(sent))
			}
			m := sent[0]
			text := m.Subject + "\n" + m.Text
			if strings.ContainsAny(m.Subject, "0123456789@:/#.") {
				t.Errorf("the subject %q", m.Subject)
			}
			for what, re := range map[string]*regexp.Regexp{"a link": link, "a code": digits, "an identifier": ident, "a token": long} {
				if found := re.FindString(text); found != "" {
					t.Errorf("%s holds %s (%q):\n%s", kind, what, found, text)
				}
			}
			for _, name := range names.FindAllString(text, -1) {
				if _, ok := verified[name]; name != "app.permaily.test" && name != "permaily.com" && !(ok && name == n.Facts.Vendor) {
					t.Errorf("%s names %q:\n%s", kind, name, text)
				}
			}
			if owner, ok := verified[n.Facts.Vendor]; ok && !strings.Contains(text, "App: An app by "+owner+" ("+n.Facts.Vendor+")") {
				t.Errorf("%s does not name the verified vendor:\n%s", kind, text)
			}
			if _, ok := verified[n.Facts.Vendor]; !ok && n.Facts.Vendor != "" && !strings.Contains(text, "App: An app Permaily hasn't verified") {
				t.Errorf("%s does not say the app is not verified:\n%s", kind, text)
			}
			for _, secret := range []string{n.Key, sampleAccount, "evil", "xn--", "a1234567", "permaily-security"} {
				if strings.Contains(text, secret) {
					t.Errorf("%s holds %q:\n%s", kind, secret, text)
				}
			}
			if strings.Count(text, "@") != strings.Count(text, "support@permaily.com") {
				t.Errorf("%s names an address:\n%s", kind, text)
			}
			if strings.ContainsAny(text, "<>\r\t") || !strings.HasSuffix(m.Text, "\n") || strings.Contains(m.Text, "\n\n\n") {
				t.Errorf("%s is not plain text:\n%q", kind, m.Text)
			}
			if strings.Contains(m.HTML, "href") {
				t.Errorf("%s links", kind)
			}
		}
	}
}

// E37: every kind of notices.Kinds is in sampleMails, so its golden text, its
// fixture in packages/mail and internal/domain/mail's checks of its HTML
// cover it (TestEveryMessageReadsAsItsGoldenText,
// TestThePreviewShowsWhatTheServiceSends), each sample as Deliver writes it.
func TestE37EveryNoticeAboutConnectionsIsASample(t *testing.T) {
	subjects := map[string]bool{}
	for _, s := range connectionSamples() {
		subjects[s.mail.Subject] = true
		if s.mail.To != sampleTo || s.mail.Text != mail.Text(s.mail.Blocks) {
			t.Errorf("%s is not written from its blocks", s.name)
		}
	}
	for _, kind := range notices.Kinds() {
		subject, _, err := connectionNotice(sampleApp, sampleNotice(kind, "claude.ai"))
		if err != nil || !subjects[subject] {
			t.Errorf("%s has no sample (%v)", kind, err)
		}
	}
	if n := len(connectionSamples()); n != 26 {
		t.Errorf("%d samples of the notices about connections, want 26", n)
	}
}
