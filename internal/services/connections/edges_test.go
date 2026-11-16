package connections

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The service's edges: what it is given and the storage failing at each
// step.

type badKeys struct{}

func (badKeys) Derive(context.Context, string) ([]byte, error) { return nil, errors.New("no root") }

func TestNewNeedsWhatItIsGiven(t *testing.T) {
	w := newWorld(t)
	if _, err := New(Config{}); !errors.Is(err, errMissing) {
		t.Fatal(err)
	}
	c := w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true})
	c.Keys = badKeys{}
	if _, err := New(c); err == nil {
		t.Fatal("made without its keys")
	}
	c = w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true})
	c.Now, c.Random = nil, nil
	s, err := New(c)
	if err != nil || s.now().IsZero() {
		t.Fatal(err)
	}
	if id, err := s.newID("con"); err != nil || len(id) != len("con_")+26 {
		t.Fatal(id, err)
	}
	s.cfg.Random = func([]byte) error { return errors.New("no randomness") }
	if _, err := s.newID("con"); err == nil {
		t.Fatal("an identifier without randomness")
	}
}

// The host a redirect URI names, as the consent screen shows it.
func TestRedirectHost(t *testing.T) {
	if h, lo := redirectHost("http://[::1]:9/cb"); h != "[::1]" || !lo {
		t.Error(h, lo)
	}
	if h, lo := redirectHost("https://claude.ai/x"); h != "claude.ai" || lo {
		t.Error(h, lo)
	}
	if h, _ := redirectHost("%zz"); h != "" {
		t.Error(h)
	}
}

// A preview's refusals, and its page with the model.
func TestPreviewRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.mbx.add(mail{ID: "m009", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "codes@example.com", Subject: "code", Held: true})
	s := w.enclave()
	p := w.person(accountA)
	req := w.start(s, p, w.ask(claudeCode))
	all := contracts.ConsentDraft{Mailboxes: req.Mailboxes}
	for name, in := range map[string]contracts.ConsentPreview{
		"empty draft":     {Mailbox: mailboxID(1)},
		"another mailbox": {Draft: all, Mailbox: mailboxID(3)},
		"a draft twice":   {Draft: contracts.ConsentDraft{Mailboxes: []string{mailboxID(1), mailboxID(1)}}, Mailbox: mailboxID(1)},
		"a future since":  {Draft: contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: contracts.MailSince{At: start.Add(time.Hour)}}, Mailbox: mailboxID(1)},
		"new and a date":  {Draft: contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: contracts.MailSince{New: true, At: start.Add(-time.Hour)}}, Mailbox: mailboxID(1)},
	} {
		if _, err := s.Preview(ctx, p, req.ID, in); !errors.Is(err, ErrInvalidBody) {
			t.Error(name, err)
		}
	}
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), Limit: 51}); !errors.Is(err, contracts.ErrInvalidLimit) {
		t.Error(err)
	}
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), After: "m005", DraftID: "drf_old"}); !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Error(err)
	}
	if _, err := s.Preview(ctx, w.person(accountB), req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1)}); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Error(err)
	}
	page, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), AskModel: true, Limit: 3})
	if err != nil || len(page.Page.Held) != 1 || page.Page.Model != contracts.MatchModelAsked || page.Page.Next == "" || len(page.Page.Messages) != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	next, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), After: page.Page.Next, DraftID: page.DraftID})
	if err != nil || len(next.Page.Messages) == 0 || next.Page.Held != nil {
		t.Fatal(next, err)
	}
	// New mail only: empty.
	if page, _ := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: req.Draft, Mailbox: mailboxID(1)}); len(page.Page.Messages) != 0 {
		t.Fatal(page.Page.Messages)
	}
	// A request with a query takes its own mailboxes and since only.
	x := w.ask(otherApp)
	qreq := w.pushed(s, p, x, fromExample, url.Values{"mail_since": {start.Add(-time.Hour).Format(time.RFC3339)}})
	for _, d := range []contracts.ConsentDraft{
		{Mailboxes: []string{mailboxID(1)}, Since: qreq.Since},
		{Mailboxes: qreq.Mailboxes, Since: contracts.MailSince{At: start.Add(-2 * time.Hour)}},
	} {
		if _, err := s.Preview(ctx, p, qreq.ID, contracts.ConsentPreview{Draft: d, Mailbox: mailboxID(1)}); !errors.Is(err, ErrInvalidBody) {
			t.Error(err)
		}
	}
	if _, err := s.Preview(ctx, p, qreq.ID, contracts.ConsentPreview{Draft: contracts.ConsentDraft{Mailboxes: qreq.Mailboxes, Since: contracts.MailSince{New: true}}, Mailbox: mailboxID(1)}); err != nil {
		t.Error(err)
	}
	// A mailbox the account no longer has.
	w.mbx.mu.Lock()
	w.mbx.gone[mailboxID(1)] = true
	w.mbx.mu.Unlock()
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1)}); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
}

// A client whose name is empty is shown by its domain; a long one is cut.
func TestClientNames(t *testing.T) {
	if clientName(&vendors.Client{Domain: "x.example"}) != "x.example" {
		t.Error("an empty name")
	}
	if got := clientName(&vendors.Client{Document: oauth.ClientDocument{ClientName: strings.Repeat("é", 100)}}); len([]rune(got)) != oauth.ClientNameMaxRunes {
		t.Error(len([]rune(got)))
	}
}

// A narrower device read through an enclave that has not seen its scope:
// from its family's record.
func TestADevicesScopeFromItsFamily(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connectAll(s, p, w.ask(claudeCode))
	y := w.ask(claudeCode)
	req := w.start(s, p, y)
	d := contracts.ConsentDraft{Mailboxes: []string{mailboxID(1)}}
	page := w.preview(s, p, req, d)
	tok, err := w.exchange(s, y, w.allowDraft(s, p, req, d, page.Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	g := w.grant(s, y, tok.AccessToken)
	s.mu.Lock()
	clear(s.scopes)
	s.mu.Unlock()
	got, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	rel.Finish(true)
	for _, m := range got.Messages {
		if !strings.HasPrefix(m.ID, mailboxID(1)) {
			t.Fatal("read past its scope", m.ID)
		}
	}
	// Its family's record lost: nothing is read.
	s.mu.Lock()
	clear(s.scopes)
	s.mu.Unlock()
	_ = s.removeFamily(ctx, g.Family)
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

// A family whose epoch was written moved and whose next one a crash left
// unmade is made again by the next reader.
func TestAFamilysEpochMadeAgain(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, w.person(accountA), x)
	fam := w.connectionOf(s, accountA, claudeCode).Devices[0].ID
	f, at, _ := s.readFamily(ctx, fam, 0)
	f.Status = oauth.FamilyMoved
	f.Generation = oauth.FamilyEpochRotations
	if err := s.writeSealed(ctx, familyName(fam, 0), f, at, false); err != nil {
		t.Fatal(err)
	}
	g, _, err := w.enclave().readFamily(ctx, fam, 0)
	if err != nil || g == nil || g.Epoch != 1 || g.Status != oauth.FamilyActive {
		t.Fatal(g, err)
	}
	// A family never made.
	if g, _, err := s.readFamily(ctx, "fam_nothing", 0); g != nil || err != nil {
		t.Fatal(g, err)
	}
	// An epoch missing whose one before was not moved.
	if g, _, err := w.enclave().readFamily(ctx, fam, 5); g != nil || err != nil {
		t.Fatal(g, err)
	}
	_ = tok
}

// The storage failing at each step of the token endpoint answers
// ErrUnavailable or a refusal, never a token.
func TestTheTokenEndpointWhenTheStorageFails(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, w.person(accountA), x)
	down := errors.New("down")
	for _, step := range []string{"get " + RecordFamily, "put " + RecordFamily, "get " + RecordConnection} {
		f.set(func(op, name string) error {
			if strings.HasPrefix(op+" "+name, step) {
				return down
			}
			return nil
		})
		if got, err := w.refresh(s, x, tok.RefreshToken); got != nil || err == nil {
			t.Fatal(step, got, err)
		}
	}
	f.set(nil)
	// The access entry's storage down.
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if strings.HasPrefix(partition, "oauth#") {
			return down
		}
		return nil
	}
	if _, err := s.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := w.refresh(s, x, tok.RefreshToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, url.Values{"token": {tok.AccessToken}, "client_id": {claudeCode}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, url.Values{"token": {tok.RefreshToken}, "client_id": {claudeCode}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := w.exchange(s, x, "pmc_x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
	// A refresh while the account's list is closing or the account blocked.
	w.accts.set(accountA, StandingBlocked)
	if _, err := w.refresh(w.enclave(), x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
	w.accts.set(accountA, StandingOpen)
	w.accts.mu.Lock()
	w.accts.fail = down
	w.accts.mu.Unlock()
	if _, err := w.refresh(w.enclave(), x, tok.RefreshToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	w.accts.mu.Lock()
	w.accts.fail = nil
	w.accts.mu.Unlock()
	// A vendor whose document cannot be fetched.
	w.vendors.mu.Lock()
	w.vendors.fail = vendors.ErrUnreachable
	w.vendors.mu.Unlock()
	if _, err := w.refresh(s, x, tok.RefreshToken); oauthCode(err) != oauth.ErrTemporarily {
		t.Fatal(err)
	}
	w.vendors.mu.Lock()
	w.vendors.fail = nil
	w.vendors.mu.Unlock()
	// Its resource, another.
	y := *x
	y.resource = apiResource
	if _, err := w.refresh(s, &y, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
	// A refresh token of a device the person revoked here.
	c := w.connectionOf(s, accountA, claudeCode)
	if _, err := s.RevokeDevice(ctx, w.person(accountA), c.ID, c.Devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.refresh(s, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
}

// The sweep ends devices whose codes were never exchanged.
func TestTheSweepTidiesDevices(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	w.allow(s, p, w.start(s, p, w.ask(claudeCode)))
	w.clock.add(oauth.CodeTTL)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c := w.connectionOf(s, accountA, claudeCode)
	if c.activeDevices() != 1 || c.Devices[1].EndReason != contracts.EndedByTime {
		t.Fatalf("%+v", c.Devices)
	}
	// The index unreadable fails the sweep.
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == partitionAccounts {
			return errors.New("down")
		}
		return nil
	}
	if err := s.Sweep(ctx); err == nil {
		t.Fatal("a sweep that read nothing succeeded")
	}
	w.mem.Interrupt = nil
}

// Limits keep their memory bounded; used requests are forgotten past their
// end.
func TestMemoryIsBounded(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	for i := range cacheMax + 1 {
		s.remember(string(rune(i)), w.clock.now().Add(time.Second))
		s.limits.allow(string(rune(i)), 1, time.Minute, w.clock.now())
	}
	w.clock.add(time.Hour)
	s.remember("again", w.clock.now().Add(time.Second))
	s.limits.allow("again", 1, time.Minute, w.clock.now())
	if len(s.used) > 2 || len(s.limits.w) > 2 {
		t.Fatal(len(s.used), len(s.limits.w))
	}
	if s.usedHere("again") != true || s.usedHere("nothing") {
		t.Fatal("used")
	}
}

// Attachments: what a read of one refuses.
func TestAttachmentRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.scope = "mail.sender mail.subject mail.body mail.attachments"
	w.mbx.add(mail{ID: "m300", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com", Held: true, Attachments: []inbox.Attachment{{Name: "x"}}})
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	for id, want := range map[string]error{
		mailboxID(1) + ".m300": contracts.ErrNotFound, // held
		mailboxID(1) + ".m999": contracts.ErrNotFound, // not there
		mailboxID(3) + ".m001": contracts.ErrNotFound, // another account's
	} {
		if _, _, err := s.Attachment(ctx, g, id, 0); !errors.Is(err, want) {
			t.Error(id, err)
		}
	}
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m001", -1); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m001", 5); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	w.mbx.add(mail{ID: "m301", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com", Attachments: []inbox.Attachment{{Name: "x", Data: make([]byte, contracts.VendorAttachmentMax+1)}}})
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m301", 0); !errors.Is(err, contracts.ErrAttachmentTooLarge) {
		t.Error(err)
	}
	att, rel, err := s.Attachment(ctx, g, mailboxID(1)+".m001", 0)
	if err != nil || string(att.Data) != "pdf" {
		t.Fatal(att, err)
	}
	rel.Finish(true)
	w.mbx.mu.Lock()
	w.mbx.gone[mailboxID(2)] = true
	w.mbx.mu.Unlock()
	if _, _, err := s.Read(ctx, g, mailboxID(2)+".m100"); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	// A search skips a mailbox no longer there.
	page, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	rel.Finish(true)
	for _, m := range page.Messages {
		if strings.HasPrefix(m.ID, mailboxID(2)) {
			t.Fatal("read a mailbox no longer there")
		}
	}
}

// The person's list skips a pending connection and survives one whose
// record is gone.
func TestTheListOfAPerson(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.AddPending("con_"+strings.Repeat("p", 26), otherApp, w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	l, err := s.List(ctx, p)
	if err != nil || len(l.Connections) != 1 {
		t.Fatal(l, err)
	}
	c := l.Connections[0]
	if !c.Since.New || !c.Since.At.Equal(start) || c.Delivery != contracts.DeliveryOn || c.Version == 0 {
		t.Fatalf("%+v", c)
	}
	// Past its end, it reads as ended before the sweep writes it so.
	w.clock.add(oauth.ConnectionDefaultDays * 24 * time.Hour)
	if got, _ := s.Get(ctx, p, c.ID); got.State != contracts.ConnectionEnded || got.EndReason != contracts.EndedByTime {
		t.Fatalf("%+v", got)
	}
}
