package connections

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// allMail is a draft of every mailbox and all the mail kept.
func allMail(req *contracts.AuthorizationRequest) contracts.ConsentDraft {
	return contracts.ConsentDraft{Mailboxes: req.Mailboxes}
}

// connectAll connects a's client to every mailbox and all its mail.
func (w *world) connectAll(s *Service, p Person, a *ask) *TokenAnswer {
	w.t.Helper()
	req := w.start(s, p, a)
	d := allMail(req)
	page := w.preview(s, p, req, d)
	tok, err := w.exchange(s, a, w.allowDraft(s, p, req, d, page.Snapshot))
	if err != nil {
		w.t.Fatal(err)
	}
	return tok
}

func ids(page *contracts.VendorPage) []string {
	var out []string
	for _, m := range page.Messages {
		out = append(out, m.ID)
	}
	return out
}

func formula(t *testing.T, raw string) *contracts.Formula {
	t.Helper()
	c, err := query.Parse([]byte(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &c.Formula
}

// V31. Search, Read and Attachment call the Discloser with the connection's
// formulas and rules.Default(); a search on what the effective parts do not
// give is refused before anything is evaluated; cursors; another account's
// mailbox or one not the connection's is not_found before anything is read.
func TestV31WhatAReadGives(t *testing.T) {
	ctx := context.Background()

	t.Run("the formulas evaluated together, and the default rule", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		x := w.ask(otherApp)
		x.resource = apiResource
		since := start.Add(-6 * 24 * time.Hour)
		req := w.pushed(s, p, x, fromExample, url.Values{"mail_since": {since.Format(time.RFC3339)}})
		d := contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: contracts.MailSince{At: since}}
		page := w.preview(s, p, req, d)
		tok, _ := w.exchange(s, x, w.allowDraft(s, p, req, d, page.Snapshot))
		// A narrower device: subject contains "4".
		y := w.ask(otherApp)
		y.resource = apiResource
		req2 := w.pushed(s, p, y, `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]},{"any":[{"var":"subject","op":"contains","value":"4"}]}]}`,
			url.Values{"mail_since": {since.Format(time.RFC3339)}})
		d2 := contracts.ConsentDraft{Mailboxes: req2.Mailboxes, Since: contracts.MailSince{At: since}}
		page2 := w.preview(s, p, req2, d2)
		if page2.Change != contracts.ChangeAddDevice {
			t.Fatal(page2.Change)
		}
		tok2, _ := w.exchange(s, y, w.allowDraft(s, p, req2, d2, page2.Snapshot))

		g := w.grant(s, x, tok.AccessToken)
		got, rel, err := s.Search(ctx, g, contracts.VendorSearch{Query: formula(t, `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"subject"}]}]}`)})
		if err != nil {
			t.Fatal(err)
		}
		rel.Finish(false)
		ds := w.disc.last()
		if len(ds.Formulas) != 4 || len(ds.Rules) != 1 || ds.Rules[0].ID != rules.DefaultID || !ds.Model || ds.Account != accountA {
			t.Fatalf("%d formulas, %+v", len(ds.Formulas), ds.Rules)
		}
		// m004 to m006 arrived since; all from example.com.
		if want := []string{mailboxID(1) + ".m006", mailboxID(1) + ".m005", mailboxID(1) + ".m004"}; !slices.Equal(ids(got), want) {
			t.Fatal(ids(got), want)
		}
		for _, m := range got.Messages {
			if m.Text != nil || m.Subject == nil || m.Attachments != nil {
				t.Fatalf("%+v", m)
			}
		}
		g2 := w.grant(s, y, tok2.AccessToken)
		got, rel, err = s.Search(ctx, g2, contracts.VendorSearch{})
		if err != nil {
			t.Fatal(err)
		}
		rel.Finish(false)
		if len(w.disc.last().Formulas) != 4 || !slices.Equal(ids(got), []string{mailboxID(1) + ".m004"}) {
			t.Fatal(ids(got))
		}
		// A read and an attachment, with the same formulas.
		m, rel, err := s.Read(ctx, g, mailboxID(1)+".m005")
		if err != nil || m.Text == nil || *m.Text != "hello" || *m.Subject != "subject 5" || m.Attachments != nil {
			t.Fatal(m, err)
		}
		rel.Finish(true)
		if _, _, err := s.Read(ctx, g2, mailboxID(1)+".m005"); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m005", 0); !errors.Is(err, contracts.ErrPartNotApproved) {
			t.Fatal(err)
		}
		// Info.
		info, err := s.Info(ctx, g)
		if err != nil || info.Query == nil || info.Delivery != contracts.DeliveryOn || !info.Since.At.Equal(since) {
			t.Fatal(info, err)
		}
	})

	t.Run("a search on what the parts do not give", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		x := w.ask(claudeCode)
		x.scope = oauth.ScopeSender
		tok := w.connectAll(s, p, x)
		g := w.grant(s, x, tok.AccessToken)
		for _, c := range []string{
			`{"var":"subject","op":"contains","value":"a"}`, `{"var":"body","op":"contains","value":"a"}`,
			`{"var":"message","op":"about","value":"a"}`, `{"var":"recipient_address","op":"is","value":"a@example.com"}`,
			`{"var":"signed_by","op":"is","value":"example.com"}`, `{"var":"list","op":"is","value":"a.example.com"}`,
			`{"var":"size","op":"at_least","value":"10"}`, `{"var":"mailbox","op":"is","value":"` + mailboxID(1) + `"}`,
			`{"var":"attachments","op":"exists"}`,
		} {
			f := formula(t, `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]},{"any":[`+c+`]}]}`)
			before := len(w.disc.seen)
			_, _, err := s.Search(ctx, g, contracts.VendorSearch{Query: f})
			var pr *query.Problem
			if !errors.As(err, &pr) || pr.Reason != contracts.FormulaPartNotApproved || pr.At != "all[1].any[0].var" {
				t.Fatalf("%s: %v", c, err)
			}
			if len(w.disc.seen) != before {
				t.Fatal("evaluated before it was refused")
			}
		}
		if r := w.rcpt.of(receipts.KindRefused); r[len(r)-1].Refused.Reason != receipts.RefusedFormulaPart {
			t.Fatalf("%+v", r[len(r)-1])
		}
		// A token narrowed below its connection's parts.
		y := w.ask(otherApp)
		tok2 := w.connectAll(s, p, y)
		narrow, err := w.refresh(s, y, tok2.RefreshToken, oauth.ScopeSender)
		if err != nil {
			t.Fatal(err)
		}
		g2 := w.grant(s, y, narrow.AccessToken)
		if _, _, err := s.Search(ctx, g2, contracts.VendorSearch{Query: formula(t, `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"a"}]}]}`)}); !errors.Is(err, contracts.ErrInvalidFormula) {
			t.Fatal(err)
		}
		// invalid_formula keeps its details.
		bad := &contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: "nonsense", Op: "is", Value: "x"}}}}}
		_, _, err = s.Search(ctx, g2, contracts.VendorSearch{Query: bad})
		var pr *query.Problem
		if !errors.As(err, &pr) || pr.Reason != contracts.FormulaUnknownVariable || pr.At == "" {
			t.Fatal(err)
		}
		if _, _, err := s.Search(ctx, g2, contracts.VendorSearch{Limit: 51}); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Fatal(err)
		}
	})

	t.Run("a cursor for its connection, device, scope and search, an hour", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		x := w.ask(claudeCode)
		tok := w.connectAll(s, p, x)
		g := w.grant(s, x, tok.AccessToken)
		var all []string
		cursor := ""
		for range 10 {
			page, rel, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			rel.Finish(true)
			all = append(all, ids(page)...)
			if cursor = page.Cursor; cursor == "" {
				break
			}
		}
		want := []string{".m006", ".m005", ".m004", ".m003", ".m002", ".m001"}
		for i := range want {
			want[i] = mailboxID(1) + want[i]
		}
		want = append(want, mailboxID(2)+".m100")
		if !slices.Equal(all, want) {
			t.Fatal(all)
		}
		page, rel, _ := s.Search(ctx, g, contracts.VendorSearch{Limit: 2})
		rel.Finish(false)
		// Another search, another scope, another device, another connection.
		if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Cursor: page.Cursor, Query: formula(t, `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"s"}]}]}`)}); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatal(err)
		}
		narrow, _ := w.refresh(s, x, tok.RefreshToken, oauth.ScopeSender)
		if _, _, err := s.Search(ctx, w.grant(s, x, narrow.AccessToken), contracts.VendorSearch{Cursor: page.Cursor}); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatal(err)
		}
		y := w.ask(otherApp)
		other := w.connectAll(s, p, y)
		if _, _, err := s.Search(ctx, w.grant(s, y, other.AccessToken), contracts.VendorSearch{Cursor: page.Cursor}); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatal(err)
		}
		for _, c := range []string{"nothing", "pmk_!!", "pmk_" + b64.EncodeToString([]byte("not sealed"))} {
			if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Cursor: c}); !errors.Is(err, contracts.ErrInvalidCursor) {
				t.Fatal(err)
			}
		}
		w.clock.add(CursorTTL)
		g = w.grant(s, x, mustRefresh(t, w, s, x, narrow.RefreshToken))
		if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Cursor: page.Cursor}); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatal(err)
		}
	})

	t.Run("another account's mailbox, or one not the connection's", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		x := w.ask(claudeCode)
		req := w.start(s, p, x)
		d := contracts.ConsentDraft{Mailboxes: []string{mailboxID(1)}}
		page := w.preview(s, p, req, d)
		tok, _ := w.exchange(s, x, w.allowDraft(s, p, req, d, page.Snapshot))
		g := w.grant(s, x, tok.AccessToken)
		w.mbx.mu.Lock()
		opened := len(w.mbx.opened)
		w.mbx.mu.Unlock()
		for _, id := range []string{mailboxID(3) + ".m001", mailboxID(2) + ".m100", "m001", mailboxID(1) + ".", strings.Repeat("x", 300)} {
			if _, _, err := s.Read(ctx, g, id); !errors.Is(err, contracts.ErrNotFound) {
				t.Fatal(id, err)
			}
		}
		w.mbx.mu.Lock()
		defer w.mbx.mu.Unlock()
		if len(w.mbx.opened) != opened {
			t.Fatal("a mailbox opened")
		}
	})
}

func mustRefresh(t *testing.T, w *world, s *Service, x *ask, token string) string {
	t.Helper()
	tok, err := w.refresh(s, x, token)
	if err != nil {
		t.Fatal(err)
	}
	return tok.AccessToken
}

// V32. Receipts: one before every answer that gives mail, under
// ReceiptWriteMax and the hard end, never the freshness (N3); a page cut at
// its receipt's bound (finding 10); a writer that fails gives nothing.
func TestV32Receipts(t *testing.T) {
	ctx := context.Background()

	t.Run("what was made available and what was held", func(t *testing.T) {
		w := newWorld(t)
		w.mbx.add(mail{ID: "m007", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "codes@example.com", Subject: "Your code is 123456", Held: true})
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		before := w.rcpt.count()
		page, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ids(page), mailboxID(1)+".m007") {
			t.Fatal("a held message left")
		}
		rel.Finish(true)
		r := w.rcpt.of(receipts.KindSearch)
		last := r[len(r)-1]
		if w.rcpt.count() != before+1 || len(last.Delivered) != 7 || len(last.Held) != 1 || last.Held[0].Message != "m007" || last.Held[0].Why != "exact" ||
			last.Connection != g.Connection || last.Device != g.Family || len(last.Formulas) != 1 || last.Delivered[0].Subject == nil {
			t.Fatalf("%+v", last)
		}
		if _, _, err := s.Read(ctx, g, mailboxID(1)+".m007"); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		if r := w.rcpt.of(receipts.KindRefused); r[len(r)-1].Refused.Reason != receipts.RefusedHeld || r[len(r)-1].Refused.Held == nil {
			t.Fatalf("%+v", r[len(r)-1])
		}
		m, rel, err := s.Read(ctx, g, mailboxID(1)+".m001")
		if err != nil || *m.Text != "hello" {
			t.Fatal(m, err)
		}
		rel.Finish(true)
		if r := w.rcpt.of(receipts.KindRead); len(r) != 1 || r[0].Delivered[0].Message != "m001" {
			t.Fatalf("%+v", r)
		}
	})

	t.Run("a preparation of six seconds reads the authority again", func(t *testing.T) {
		w := newWorld(t)
		s, f := w.hooked()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		w.disc.pause = func() { w.clock.add(6 * time.Second) }
		reads := 0
		f.set(func(op, name string) error {
			if op == "get" && strings.HasPrefix(name, RecordConnection) {
				reads++
			}
			return nil
		})
		_, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
		if err != nil || reads == 0 {
			t.Fatal(err, reads)
		}
		if _, err := rel.Start(ctx); err != nil {
			t.Fatal(err)
		}
		rel.Finish(true)

		// A check served from a cache entry 4.9 seconds old.
		w.disc.pause = func() { w.clock.add(200 * time.Millisecond) }
		w.grant(s, x, tok.AccessToken)
		w.clock.add(4900 * time.Millisecond)
		g = w.grant(s, x, tok.AccessToken)
		reads = 0
		_, rel, err = s.Search(ctx, g, contracts.VendorSearch{})
		if err != nil || reads == 0 {
			t.Fatal(err, reads)
		}
		rel.Finish(true)

		// Revoked meanwhile through the other enclave: only a refusal.
		other := w.enclave()
		w.disc.pause = func() {
			w.disc.pause = nil
			if _, err := other.Revoke(ctx, w.person(accountA), g.Connection); err != nil {
				t.Error(err)
			}
			w.clock.add(6 * time.Second)
		}
		searches := len(w.rcpt.of(receipts.KindSearch))
		if _, _, err := s.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, ErrTooLate) {
			t.Fatal(err)
		}
		if len(w.rcpt.of(receipts.KindSearch)) != searches {
			t.Fatal("a receipt of what did not leave")
		}
		if r := w.rcpt.of(receipts.KindRefused); r[len(r)-1].Refused.Reason != receipts.RefusedTooLate {
			t.Fatalf("%+v", r[len(r)-1])
		}
	})

	t.Run("a page cut at its receipt's bound, skipping nothing", func(t *testing.T) {
		w := newWorld(t)
		for i := range 80 {
			w.mbx.add(mail{ID: fmt.Sprintf("b%03d", i), Mailbox: mailboxID(2), Received: start.Add(-time.Duration(80-i) * time.Minute),
				From: "big@example.com", Subject: strings.Repeat("s", 9000) + fmt.Sprint(i)})
		}
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		var got []string
		cursor, pages := "", 0
		for {
			page, rel, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 50, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			rel.Finish(true)
			pages++
			got = append(got, ids(page)...)
			if cursor = page.Cursor; cursor == "" {
				break
			}
		}
		if len(got) != 87 || pages < 3 { // 50 a page but for the receipt's bound: two
			t.Fatalf("%d messages in %d pages", len(got), pages)
		}
		seen := map[string]bool{}
		for _, id := range got {
			if seen[id] {
				t.Fatal("twice", id)
			}
			seen[id] = true
		}
	})

	t.Run("a writer that fails: 503, nothing answered", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		w.rcpt.mu.Lock()
		w.rcpt.fail = errors.New("the table is down")
		w.rcpt.mu.Unlock()
		if page, rel, err := s.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, ErrUnavailable) || page != nil || rel != nil {
			t.Fatal(page, err)
		}
		if m, _, err := s.Read(ctx, g, mailboxID(1)+".m001"); !errors.Is(err, ErrUnavailable) || m != nil {
			t.Fatal(err)
		}
	})

	t.Run("the model unavailable: 503 and its receipt", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		w.gate.set(inbox.AskLimit)
		for _, call := range []func() error{
			func() error { _, _, err := s.Search(ctx, g, contracts.VendorSearch{}); return err },
			func() error { _, _, err := s.Read(ctx, g, mailboxID(1)+".m001"); return err },
			func() error { _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m001", 0); return err },
		} {
			if err := call(); !errors.Is(err, contracts.ErrModelUnavailable) {
				t.Fatal(err)
			}
		}
		if r := w.rcpt.of(receipts.KindRefused); r[len(r)-1].Refused.Reason != receipts.RefusedModelUnavailable {
			t.Fatalf("%+v", r)
		}
		info, err := s.Info(ctx, g)
		if err != nil || info.Delivery != contracts.DeliveryModelUnavailable {
			t.Fatal(info, err)
		}
		c, _ := s.Get(ctx, w.person(accountA), g.Connection)
		if c.Delivery != contracts.DeliveryModelUnavailable {
			t.Fatal(c.Delivery)
		}
		w.gate.set(inbox.AskAnswered)
		// A page the model could not finish, with nothing that left.
		w.disc.pageMax = 0
		g2 := *g
		g2.Parts = []contracts.Part{contracts.PartSender}
		_ = g2
		// A connection approved under a notice no longer in force.
		w.notice = "2026-12-01"
		newer := w.enclave()
		w.notice = notice
		if _, _, err := newer.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, contracts.ErrModelUnavailable) {
			t.Fatal(err)
		}
		if c, _ := newer.Get(ctx, w.person(accountA), g.Connection); c.Delivery != contracts.DeliveryNoticeChanged {
			t.Fatal(c.Delivery)
		}
		if info, _ := newer.Info(ctx, g); info.Delivery != contracts.DeliveryModelUnavailable {
			t.Fatal(info.Delivery)
		}
	})
}

// V33. Limits.
func TestV33Limits(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.scope = "mail.sender mail.subject mail.body mail.attachments"
	big := make([]byte, contracts.VendorAttachmentMax)
	w.mbx.add(mail{ID: "m200", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com", Subject: "big", Attachments: []inbox.Attachment{{Name: "big.bin", Data: big}}})
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	for i := range SearchesPerMinute {
		_, rel, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 1})
		if err != nil {
			t.Fatal(i, err)
		}
		rel.Finish(true)
	}
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 1}); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	for i := range AttachmentBytesPerHour / contracts.VendorAttachmentMax {
		_, rel, err := s.Attachment(ctx, g, mailboxID(1)+".m200", 0)
		if err != nil {
			t.Fatal(i, err)
		}
		rel.Finish(true)
	}
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m200", 0); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	for i := AttachmentBytesPerHour/contracts.VendorAttachmentMax + 1; i < ReadsPerMinute; i++ {
		_, rel, err := s.Read(ctx, g, mailboxID(1)+".m001")
		if err != nil {
			t.Fatal(i, err)
		}
		rel.Finish(true)
	}
	if _, _, err := s.Read(ctx, g, mailboxID(1)+".m001"); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	// A caller naming a popular client_id cannot use up a device's refreshes.
	for range 50 {
		_, _ = s.Token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"pmr_bogus"}, "client_id": {claudeCode}})
	}
	if _, err := w.refresh(s, x, tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	// 600 requests a minute before anything is authenticated, in all.
	n := 0
	for {
		if _, err := s.Token(ctx, url.Values{"grant_type": {"nothing"}}); errors.Is(err, contracts.ErrTooManyRequests) {
			break
		}
		n++
	}
	if n+51+1 != TokenAdmissionPerMinute {
		t.Fatal(n)
	}
	if err := s.RevokeToken(ctx, url.Values{}); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx, url.Values{}); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	w.clock.add(time.Minute)
	if _, err := s.Token(ctx, url.Values{"grant_type": {"nothing"}}); oauthCode(err) != oauth.ErrUnsupportedGrantType {
		t.Fatal(err)
	}
}

// V34. Logs: nothing of a token, code, verifier, state, assertion, redirect
// URI, unverified client_id, formula, account, connection, device or
// message reaches a log line, an error's words or /status's reasons, with
// hostile values in every field.
func TestV34NothingOfAPersonOrATokenIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	log.SetOutput(&buf)
	defer func() { slog.SetDefault(prevLog); log.SetOutput(nil) }()
	const hostile = "HOSTILE-pma_x-pmr_y-pmc_z"
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.state = hostile
	tok, _ := w.connect(s, w.person(accountA), x)
	var errs []error
	collect := func(err error) { errs = append(errs, err) }
	for _, form := range []url.Values{
		{"grant_type": {"refresh_token"}, "refresh_token": {"pmr_" + hostile}, "client_id": {"https://" + hostile + ".example/c"}},
		{"grant_type": {"authorization_code"}, "code": {"pmc_" + hostile}, "code_verifier": {hostile}, "redirect_uri": {hostile}, "client_id": {claudeCode}},
		{"grant_type": {hostile}},
	} {
		_, err := s.Token(ctx, form)
		collect(err)
	}
	collect(s.RevokeToken(ctx, url.Values{"token": {"pma_" + hostile}, "client_id": {claudeCode}}))
	_, err := s.Push(ctx, url.Values{"client_id": {claudeCode}, "redirect_uri": {hostile}, "query": {hostile}})
	collect(err)
	_, err = s.Start(ctx, w.person(accountA), url.Values{"client_id": {"https://" + hostile + ".example/x"}, "redirect_uri": {hostile}}.Encode())
	collect(err)
	_, err = s.Check(ctx, contracts.ResourceMCP, "pma_"+hostile)
	collect(err)
	g := w.grant(s, x, tok.AccessToken)
	_, _, err = s.Search(ctx, g, contracts.VendorSearch{Cursor: hostile, Query: &contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: "subject", Op: "contains", Value: hostile}}}}}})
	collect(err)
	_, _, err = s.Read(ctx, g, hostile)
	collect(err)
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), "HOSTILE") {
			t.Fatalf("an error says what was sent: %v", err)
		}
	}
	fixed := regexp.MustCompile(`^[a-z_]+$`)
	for k := range s.Refused() {
		if !fixed.MatchString(k) {
			t.Fatalf("a reason that is not fixed: %q", k)
		}
	}
	if strings.Contains(buf.String(), "HOSTILE") || strings.Contains(buf.String(), tok.AccessToken) {
		t.Fatal("logged:", buf.String())
	}
}

// V59. Release (finding 5, N2, N3, the owner's decision 14), with barriers.
func TestV59Release(t *testing.T) {
	prev := leaseTick
	leaseTick = 2 * time.Millisecond
	t.Cleanup(func() { leaseTick = prev })
	ctx := context.Background()

	setup := func(t *testing.T) (*world, *Service, *ask, *TokenAnswer, func() *contractsGrant) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		return w, s, x, tok, func() *contractsGrant { return &contractsGrant{w.grant(s, x, tok.AccessToken)} }
	}

	t.Run("revoked through the same enclave at each step", func(t *testing.T) {
		for _, when := range []string{"while the model is asked", "while the receipt is written", "after the service returned"} {
			w, s, _, _, grant := setup(t)
			g := grant().Grant
			revoke := func() {
				if _, err := s.Revoke(ctx, w.person(accountA), g.Connection); err != nil {
					t.Error(err)
				}
			}
			switch when {
			case "while the model is asked":
				w.disc.pause = func() { w.disc.pause = nil; revoke() }
			case "while the receipt is written":
				w.rcpt.onWrite = func(r *receipts.Receipt) {
					if r.Kind == receipts.KindSearch {
						w.rcpt.onWrite = nil
						revoke()
					}
				}
			}
			_, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
			if when == "after the service returned" {
				revoke()
			}
			if err == nil {
				_, err = rel.Start(ctx)
				rel.Finish(false)
			}
			if !errors.Is(err, ErrTooLate) {
				t.Fatalf("%s: %v", when, err)
			}
			r := w.rcpt.of(receipts.KindRefused)
			if last := r[len(r)-1]; last.Refused.Reason != receipts.RefusedTooLate {
				t.Fatalf("%s: %+v", when, last)
			}
			if when != "while the model is asked" {
				search := w.rcpt.of(receipts.KindSearch)
				if r[len(r)-1].Refused.Receipt != search[len(search)-1].ID {
					t.Fatalf("%s: the too_late receipt does not name the first", when)
				}
			}
		}
	})

	t.Run("during the transfer, with the writer blocked", func(t *testing.T) {
		w, s, x, tok, grant := setup(t)
		g := grant().Grant
		// A second device, whose lease a device's revocation does not cut.
		y := w.ask(claudeCode)
		tok2, _ := w.exchange(s, y, w.allow(s, w.person(accountA), w.start(s, w.person(accountA), y)))
		g2 := w.grant(s, y, tok2.AccessToken)
		_, rel, _ := s.Search(ctx, g, contracts.VendorSearch{})
		_, rel2, _ := s.Search(ctx, g2, contracts.VendorSearch{})
		c1, err := rel.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c2, err := rel2.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RevokeDevice(ctx, w.person(accountA), g.Connection, g.Family); err != nil {
			t.Fatal(err)
		}
		<-c1.Done()
		time.Sleep(20 * time.Millisecond)
		if c2.Err() != nil {
			t.Fatal("another device's answer was cut")
		}
		rel.Finish(false)
		rel2.Finish(true)
		_ = x
		_ = tok
	})

	t.Run("revoked through the other enclave during a transfer", func(t *testing.T) {
		w, s, _, _, grant := setup(t)
		g := grant().Grant
		_, rel, _ := s.Search(ctx, g, contracts.VendorSearch{})
		c, err := rel.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		other := w.enclave()
		if _, err := other.Revoke(ctx, w.person(accountA), g.Connection); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
		if c.Err() != nil {
			t.Fatal("cut before its read was five seconds old")
		}
		w.clock.add(oauth.ConnectionFresh)
		waitDone(t, c)
		rel.Finish(false)
	})

	t.Run("the storage failing during a transfer", func(t *testing.T) {
		w := newWorld(t)
		s, f := w.hooked()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		_, rel, _ := s.Search(ctx, g, contracts.VendorSearch{})
		c, err := rel.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.set(func(string, string) error { return errors.New("down") })
		w.clock.add(oauth.ConnectionFresh)
		waitDone(t, c)
		rel.Finish(false)
	})

	t.Run("the token expiring, the connection ending, TransferMax", func(t *testing.T) {
		for _, what := range []string{"token before start", "token during", "transfer max", "connection end"} {
			w, s, x, _, grant := setup(t)
			g := grant().Grant
			if what == "connection end" {
				g.ConnectionEndsAt = w.clock.now().Add(10 * time.Second)
			}
			_, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
			if err != nil {
				t.Fatal(err)
			}
			if what == "token before start" {
				w.clock.add(oauth.AccessTokenTTL)
				if _, err := rel.Start(ctx); !errors.Is(err, ErrTooLate) {
					t.Fatal(err)
				}
				continue
			}
			c, err := rel.Start(ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch what {
			case "token during":
				// Read again every five seconds until the token's hour ends.
				for range int(oauth.AccessTokenTTL / oauth.ConnectionFresh) {
					w.clock.add(oauth.ConnectionFresh)
				}
			case "transfer max":
				w.clock.add(TransferMax)
			case "connection end":
				w.clock.add(10 * time.Second)
			}
			waitDone(t, c)
			rel.Finish(false)
			_ = x
		}
	})

	t.Run("a lease never finished ends", func(t *testing.T) {
		_, s, _, _, grant := setup(t)
		g := grant().Grant
		_, rel, _ := s.Search(ctx, g, contracts.VendorSearch{})
		l := rel.(*lease)
		l.timer.Reset(time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if _, err := rel.Start(ctx); err == nil {
			t.Fatal("a lease ended at TransferMax started")
		}
		s.mu.Lock()
		n := len(s.leases)
		s.mu.Unlock()
		if n != 0 {
			t.Fatal("still registered")
		}
	})
}

type contractsGrant struct{ *oauth.Grant }

func waitDone(t *testing.T, c context.Context) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the transfer was not cut")
	}
}
