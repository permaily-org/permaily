package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/services/receipts/receiptstest"
	"github.com/permaily-org/permaily/internal/store"
)

// The person's receipts (ADR 0037, pull request 3: L23, and the export's
// half of L24): the routes over the real accounts service, whose sessions
// they take, and the real chained receipts service, over one storage.

// receiptsApp is the accounts API and the receipts routes over real
// services, and a browser's cookie jar.
type receiptsApp struct {
	*app
	chain *receipts.Service
	// terms is the version of the terms in force.
	terms atomic.Value
}

func newReceiptsApp(t *testing.T) *receiptsApp {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemory()
	a := &app{t: t, out: &accounts.Outbox{}, logs: &bytes.Buffer{}, mem: mem, idp: &providers{who: map[string]idp.Identity{}}, keys: &keyFake{},
		commits: store.NewMemoryCommits(), sealer: k}
	ra := &receiptsApp{app: a}
	ra.terms.Store(domain.TermsVersion)
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	now := func() time.Time { return time.Now().Add(a.ahead) }
	a.svc, err = accounts.New(accounts.Config{
		Records: &store.Anchored{Records: mem, Commits: a.commits, OnlyNewest: true}, Entries: mem,
		Keys: k, Mailer: a.out, Providers: a.idp, AppURL: appOrigin, SuperAdmin: superAdmin, Log: log, Now: now,
		Terms: func() string { return ra.terms.Load().(string) },
	})
	if err != nil {
		t.Fatal(err)
	}
	chainCommits := store.NewMemoryCommits()
	chainCommits.Group = receipts.CommitGroup
	chain := &store.Anchored{Records: receipts.ChainTable{Entries: mem}, Commits: chainCommits, Strict: true, Now: now}
	if ra.chain, err = receipts.NewChained(mem, k, now, chain, a.svc); err != nil {
		t.Fatal(err)
	}
	mux := httpapi.NewAppMux()
	mux.Handle("/api/", httpapi.AccountsAPI(a.svc, log))
	mine := httpapi.ReceiptsAPI(httpapi.ReceiptsConfig{Chain: ra.chain, Sessions: a.svc, Log: log})
	mux.Handle("/api/v1/receipts", mine)
	mux.Handle("/api/v1/receipts/", mine)
	a.h = mux
	return ra
}

// write writes n receipts of account through the chained service, each in
// a link of its own, and returns their identifiers, the newest first.
func (ra *receiptsApp) write(account string, n int) []string {
	ra.t.Helper()
	var ids []string
	for range n {
		r := &receipts.Receipt{Kind: receipts.KindEnded, ClientID: "https://client.example/meta", Vendor: "client.example", Resource: contracts.ResourceMCP, Ended: &receipts.Ended{Reason: contracts.EndedByReplaced}}
		if err := ra.chain.Write(context.Background(), account, r); err != nil {
			ra.t.Fatal(err)
		}
		ids = append([]string{r.ID}, ids...)
	}
	return ids
}

// walk reads every page of the session's receipts, limit a page, and
// returns the head the first said and every receipt's identifier in order.
func (ra *receiptsApp) walk(limit int) (contracts.ReceiptsHead, []string) {
	ra.t.Helper()
	var head contracts.ReceiptsHead
	var ids []string
	before := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			ra.t.Fatal("the walk does not end")
		}
		path := fmt.Sprintf("/api/v1/receipts?limit=%d", limit)
		if before != "" {
			path += "&before=" + before
		}
		var page contracts.ReceiptsPage
		read(ra.t, ra.call("GET", path, "", nil), 200, &page)
		if pages == 0 {
			head = page.Head
		}
		for _, l := range page.Links {
			if l.Verdict != "ok" || l.Kind != "receipts" || l.Prev == "" {
				ra.t.Fatalf("link %d: %+v", l.N, l)
			}
			for _, r := range l.Receipts {
				var doc receipts.Receipt
				if r.Verdict != "ok" || json.Unmarshal(r.Receipt, &doc) != nil || doc.ID != r.ID || len(r.SHA256) != 64 {
					ra.t.Fatalf("receipt %d.%d: %+v", l.N, r.Index, r)
				}
				ids = append(ids, r.ID)
			}
		}
		if page.Done {
			return head, ids
		}
		before = page.Next.String()
	}
}

// L23: the head and the walk's pages, with their verdicts, for the session's
// own account; another account's never, whatever the request names.
func TestL23ThePersonSeesTheirOwnChain(t *testing.T) {
	ra := newReceiptsApp(t)
	bea := ra.signIn("bea@example.org").Account.ID
	beas := ra.write(bea, 3)
	ra.call("POST", "/api/v1/auth/sign-out", "", nil)
	ana := ra.signIn("ana@example.org").Account.ID
	anas := ra.write(ana, 5)

	head, ids := ra.walk(2)
	if head.N != 5 || head.Verdict != "ok" || len(head.SHA256) != 64 || head.At.IsZero() {
		t.Fatalf("the head: %+v", head)
	}
	if fmt.Sprint(ids) != fmt.Sprint(anas) {
		t.Fatalf("the walk: %v, want %v", ids, anas)
	}
	h, err := ra.chain.Head(context.Background(), ana)
	if err != nil || h.Digest.String() != head.SHA256 {
		t.Fatalf("the head the person notes is not the chain's: %+v %v", h, err)
	}
	// Naming another account changes nothing: the session decides.
	var page contracts.ReceiptsPage
	read(t, ra.call("GET", "/api/v1/receipts?account="+bea, "", nil), 200, &page)
	for _, l := range page.Links {
		for _, r := range l.Receipts {
			for _, b := range beas {
				if r.ID == b {
					t.Fatalf("another account's receipt %s", r.ID)
				}
			}
		}
	}
	var export contracts.ReceiptsExportPage
	read(t, ra.call("GET", "/api/v1/receipts/export?account="+bea, "", nil), 200, &export)
	if export.Account != ana || export.Head.SHA256 != head.SHA256 {
		t.Fatalf("the export is another's: %+v", export)
	}
	// Without a session, nothing.
	ra.call("POST", "/api/v1/auth/sign-out", "", nil)
	for _, path := range []string{"/api/v1/receipts", "/api/v1/receipts/export"} {
		if rec := ra.call("GET", path, "", nil); rec.Code != 401 {
			t.Fatalf("%s without a session: %d %s", path, rec.Code, rec.Body)
		}
	}
	// Bea's session sees Bea's.
	ra.signIn("bea@example.org")
	if _, ids := ra.walk(100); fmt.Sprint(ids) != fmt.Sprint(beas) {
		t.Fatalf("Bea's walk: %v, want %v", ids, beas)
	}
	if strings.Contains(ra.logs.String(), ana) || strings.Contains(ra.logs.String(), anas[0]) {
		t.Fatalf("the log names an account or a receipt:\n%s", ra.logs)
	}
}

// L23: allowed under terms the account has not agreed to, and while the
// account is closing, as the connections' pages are.
func TestL23AllowedUnderUnagreedTermsAndWhileClosing(t *testing.T) {
	ra := newReceiptsApp(t)
	ana := ra.signIn("ana@example.org").Account.ID
	anas := ra.write(ana, 2)
	ra.terms.Store("2099-01-01")
	var e httpapi.APIError
	if rec := ra.call("PATCH", "/api/v1/account", `{"name":"Ana"}`, nil); json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != contracts.ErrTermsChanged.Code {
		t.Fatalf("the terms are not unagreed: %d %s", rec.Code, rec.Body)
	}
	if _, ids := ra.walk(10); fmt.Sprint(ids) != fmt.Sprint(anas) {
		t.Fatalf("under unagreed terms: %v", ids)
	}
	var closing contracts.Account
	if read(t, ra.call("POST", "/api/v1/account/delete", "", nil), 200, &closing); closing.State != contracts.AccountClosing {
		t.Fatalf("not closing: %+v", closing)
	}
	if _, ids := ra.walk(10); fmt.Sprint(ids) != fmt.Sprint(anas) {
		t.Fatalf("while closing: %v", ids)
	}
	if rec := ra.call("GET", "/api/v1/receipts/export", "", nil); rec.Code != 200 {
		t.Fatalf("the export while closing: %d %s", rec.Code, rec.Body)
	}
}

// L23: a removed account has no session, even once every value of it, its
// session, its record and its chain, is put back in the storage.
func TestL23ARemovedAccountsSessionIsRefusedWithItsValuesPutBack(t *testing.T) {
	ra := newReceiptsApp(t)
	ana := ra.signIn("ana@example.org").Account.ID
	ra.write(ana, 3)
	before := ra.mem.AllEntries()
	var records []store.Record
	for _, name := range ra.mem.RecordNames() {
		r, err := ra.mem.GetRecord(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, *r)
	}
	cookie := ra.cookie
	read(t, ra.call("POST", "/api/v1/account/delete", "", nil), 200, nil)
	ra.ahead = 8 * 24 * time.Hour
	if err := ra.svc.RemoveDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	ra.cookie = cookie
	if rec := ra.call("GET", "/api/v1/receipts", "", nil); rec.Code != 401 {
		t.Fatalf("a removed account's walk: %d %s", rec.Code, rec.Body)
	}
	for _, e := range before {
		ra.mem.SetEntry(e)
	}
	for _, r := range records {
		ra.mem.SetRecord(r)
	}
	ra.cookie = cookie
	for _, path := range []string{"/api/v1/receipts", "/api/v1/receipts/export"} {
		if rec := ra.call("GET", path, "", nil); rec.Code != 401 || strings.Contains(rec.Body.String(), "rcp_") {
			t.Fatalf("%s with its values put back: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// L23: limit and before are validated, and a position the chain does not
// have is refused, never taken for another.
func TestL23LimitAndBeforeAreValidated(t *testing.T) {
	ra := newReceiptsApp(t)
	ana := ra.signIn("ana@example.org").Account.ID
	ra.write(ana, 3)
	var e httpapi.APIError
	for _, q := range []string{"limit=0", "limit=101", "limit=a", "limit=-1", "limit=1&limit=2", "limit="} {
		if read(t, ra.call("GET", "/api/v1/receipts?"+q, "", nil), 400, &e); e.Error.Code != contracts.ErrInvalidLimit.Code {
			t.Errorf("%s: %+v", q, e)
		}
	}
	for _, path := range []string{"/api/v1/receipts", "/api/v1/receipts/export"} {
		// Not a position, or one the chain does not have: above its head,
		// past a link's receipts.
		for _, q := range []string{"before=", "before=x", "before=0.0", "before=1", "before=1.0&before=2.0", "before=4.0", "before=99.0", "before=2.2", "before=3.65"} {
			if read(t, ra.call("GET", path+"?"+q, "", nil), 400, &e); e.Error.Code != contracts.ErrInvalidBefore.Code {
				t.Errorf("%s?%s: %+v", path, q, e)
			}
		}
	}
	var page contracts.ReceiptsPage
	if read(t, ra.call("GET", "/api/v1/receipts?before=3.0&limit=1", "", nil), 200, &page); len(page.Links) != 1 || page.Links[0].N != 2 || page.Next != (contracts.ReceiptsPosition{N: 2}) {
		t.Fatalf("before 3.0: %+v", page)
	}
	if read(t, ra.call("GET", "/api/v1/receipts?before=1.0", "", nil), 200, &page); !page.Done || len(page.Links) != 0 {
		t.Fatalf("before 1.0: %+v", page)
	}
	if rec := ra.call("POST", "/api/v1/receipts", "", nil); rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
		t.Fatalf("POST: %d", rec.Code)
	}
	if rec := ra.call("POST", "/api/v1/receipts/export", "", nil); rec.Code != 405 {
		t.Fatalf("POST the export: %d", rec.Code)
	}
	if rec := ra.call("GET", "/api/v1/receipts/other", "", nil); rec.Code != 404 {
		t.Fatalf("another path: %d", rec.Code)
	}
}

// fakeSession plays the session of one account.
type fakeSession string

func (s fakeSession) Authenticate(context.Context, string) (*accounts.Authenticated, error) {
	return &accounts.Authenticated{AccountID: string(s)}, nil
}

const fakeAccount = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeReceipts is the routes over chain, for fakeAccount, with a log.
func fakeReceipts(chain receipts.Chain) (http.Handler, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return httpapi.ReceiptsAPI(httpapi.ReceiptsConfig{Chain: chain, Sessions: fakeSession(fakeAccount), Log: slog.New(slog.NewTextHandler(logs, nil))}), logs
}

func getAs(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", appOrigin+path, nil)
	req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// L23: a page is bounded as L17's are, whatever the chain holds: broken
// links count against WalkLinksMax, receipts' bytes against WalkBytesMax,
// and the pages of the walk and of the export move on to the end.
func TestL23APageIsBoundedAsL17(t *testing.T) {
	f := &receiptstest.Fake{}
	for range receipts.WalkLinksMax + 44 {
		f.Append(fakeAccount, []byte(`{}`))
	}
	for n := uint64(1); n <= receipts.WalkLinksMax+44; n++ {
		f.SetLink(fakeAccount, n, receipts.LinkLost)
	}
	big := bytes.Repeat([]byte("a"), 256<<10)
	docs := make([][]byte, 40)
	for i := range docs {
		docs[i] = big
	}
	f.Append(fakeAccount, docs...)
	h, _ := fakeReceipts(f)

	var page contracts.ReceiptsPage
	read(t, getAs(h, "/api/v1/receipts?limit=100"), 200, &page)
	if page.Done || len(page.Links) != 1 || len(page.Links[0].Receipts) >= 40 || page.Next.N != receipts.WalkLinksMax+45 || page.Next.Index == 0 {
		t.Fatalf("a page of large receipts: done %v, next %+v, %d links", page.Done, page.Next, len(page.Links))
	}
	seen := 0
	before := ""
	for pages := 0; ; pages++ {
		path := "/api/v1/receipts/export"
		if before != "" {
			path += "?before=" + before
		}
		var e receipts.ExportPage
		read(t, getAs(h, path), 200, &e)
		if err := e.Check(); err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, l := range e.Links {
			seen++
			if l.N == receipts.WalkLinksMax+45 {
				seen-- // the link of large receipts, over several pages
			}
		}
		if e.Done {
			break
		}
		if len(e.Links) > receipts.WalkLinksMax || e.Bytes() > receipts.WalkBytesMax {
			t.Fatalf("page %d past its bounds", pages)
		}
		before = contracts.ReceiptsPosition{N: e.Next.N, Index: e.Next.Index}.String()
		if pages > 100 {
			t.Fatal("the export does not end")
		}
	}
	if seen != receipts.WalkLinksMax+44 {
		t.Fatalf("%d broken links exported, want %d", seen, receipts.WalkLinksMax+44)
	}
}

// L23: the head the person may note is an "ok" one: of a head that is
// lost, forked or unreadable, the routes give the number and the verdict
// and no digest (ADR 0037, "Changed while building pull request 1", 4).
func TestL23OnlyAHealthyHeadHasADigest(t *testing.T) {
	f := &receiptstest.Fake{}
	f.Append(fakeAccount, []byte(`{}`))
	f.Append(fakeAccount, []byte(`{}`))
	f.SetLink(fakeAccount, 2, receipts.LinkLost)
	h, _ := fakeReceipts(f)
	var page contracts.ReceiptsPage
	if read(t, getAs(h, "/api/v1/receipts"), 200, &page); page.Head != (contracts.ReceiptsHead{N: 2, Verdict: "lost"}) {
		t.Fatalf("a lost head: %+v", page.Head)
	}
	var export contracts.ReceiptsExportPage
	if read(t, getAs(h, "/api/v1/receipts/export"), 200, &export); export.Head != (contracts.ReceiptsHead{N: 2, Verdict: "lost"}) {
		t.Fatalf("a lost head exported: %+v", export.Head)
	}
}

// L24: an export's documents are their bytes as stored, unknown fields and
// kinds included, and every page passes ExportPage.Check before it leaves.
func TestL24TheExportKeepsEveryByte(t *testing.T) {
	f := &receiptstest.Fake{}
	unknown := []byte(`{"v":9,"id":"rcp_x","kind":"a kind this build does not know","new":{"field":[1,2,3]}}`)
	f.Append(fakeAccount, unknown, []byte(`{"v":1,"kind":"ended"}`))
	f.Append(fakeAccount, []byte(`{"a":"\u00e9\u2028"}`))
	h, _ := fakeReceipts(f)
	rec := getAs(h, "/api/v1/receipts/export")
	var page receipts.ExportPage
	read(t, rec, 200, &page)
	want, err := f.Export(context.Background(), fakeAccount, receipts.Position{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(&page)
	if w, _ := json.Marshal(want); !bytes.Equal(got, w) {
		t.Fatalf("the export is not the chain's:\n%s\n%s", got, w)
	}
	if !bytes.Equal(page.Links[1].Receipts[1].Doc, unknown) || !bytes.Equal(page.Links[0].Receipts[0].Doc, []byte(`{"a":"\u00e9\u2028"}`)) {
		t.Fatalf("a document's bytes changed: %q", page.Links[1].Receipts[1].Doc)
	}
	if !strings.HasSuffix(rec.Body.String(), "}\n") || strings.Count(rec.Body.String(), "\n") != 1 {
		t.Fatalf("an export's page is not one line: %q", rec.Body.String())
	}
	// A page that does not keep the contract never leaves.
	h, logs := fakeReceipts(offContract{f})
	if rec := getAs(h, "/api/v1/receipts/export"); rec.Code != 500 || strings.Contains(rec.Body.String(), "rcp_") {
		t.Fatalf("a page off its contract: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), fakeAccount) || strings.Contains(logs.String(), "rcp_") {
		t.Fatalf("the log names the chain:\n%s", logs)
	}
}

// offContract answers export pages that fail ExportPage.Check.
type offContract struct{ receipts.Chain }

func (o offContract) Export(ctx context.Context, account string, before receipts.Position) (*receipts.ExportPage, error) {
	p, err := o.Chain.Export(ctx, account, before)
	if err == nil {
		p.V = 2
	}
	return p, err
}

// continuing answers receipts.ErrContinuation to its first calls, as the
// service does to a page that begins at a link whose commit expired and
// whose authentication passes a page's bounds; each try reads further.
type continuing struct {
	receipts.Chain
	mu    sync.Mutex
	left  int
	tries int
}

func (c *continuing) more() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries++
	if c.left < 0 || c.left > 0 {
		c.left--
		return receipts.ErrContinuation
	}
	return nil
}

func (c *continuing) Walk(ctx context.Context, account string, before receipts.Position, limit int) (*receipts.Walked, error) {
	if err := c.more(); err != nil {
		return nil, err
	}
	return c.Chain.Walk(ctx, account, before, limit)
}

func (c *continuing) Export(ctx context.Context, account string, before receipts.Position) (*receipts.ExportPage, error) {
	if err := c.more(); err != nil {
		return nil, err
	}
	return c.Chain.Export(ctx, account, before)
}

// The review of #170, finding 3: the route treats ErrContinuation as "ask
// again", and asks again itself, each try reading further down; one that
// never ends is answered busy, to be asked again later, never a page.
func TestL23AContinuationIsAskedAgain(t *testing.T) {
	f := &receiptstest.Fake{}
	f.Append(fakeAccount, []byte(`{}`))
	for _, path := range []string{"/api/v1/receipts", "/api/v1/receipts/export"} {
		c := &continuing{Chain: f, left: 5}
		h, _ := fakeReceipts(c)
		if rec := getAs(h, path); rec.Code != 200 || c.tries != 6 {
			t.Fatalf("%s after five continuations: %d, %d tries", path, rec.Code, c.tries)
		}
		c = &continuing{Chain: f, left: -1}
		h, logs := fakeReceipts(c)
		var e httpapi.APIError
		rec := getAs(h, path)
		if read(t, rec, 503, &e); e.Error.Code != contracts.ErrBusy.Code || rec.Header().Get("Retry-After") == "" || c.tries < 2 || c.tries > 64 {
			t.Fatalf("%s continuing for ever: %+v after %d tries", path, e, c.tries)
		}
		if logs.Len() != 0 {
			t.Fatalf("a continuation is logged:\n%s", logs)
		}
	}
}

// failing answers err to every call.
type failing struct {
	receipts.Chain
	err error
}

func (f failing) Walk(context.Context, string, receipts.Position, int) (*receipts.Walked, error) {
	return nil, f.err
}

func (f failing) Export(context.Context, string, receipts.Position) (*receipts.ExportPage, error) {
	return nil, f.err
}

// What the routes answer of the chain's failures, and log of them: kinds,
// never an account, a receipt or a link.
func TestL23TheChainsFailures(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
		logged bool
	}{
		{receipts.ErrHistoryTooLarge, 503, contracts.ErrReceiptsUnreadable.Code, false},
		{receipts.ErrBefore, 400, contracts.ErrInvalidBefore.Code, false},
		{fmt.Errorf("receipts: read the chain of %s: %w", fakeAccount, errors.New("storage down")), 500, "internal", true},
		{context.DeadlineExceeded, 500, "internal", true},
	} {
		for _, path := range []string{"/api/v1/receipts", "/api/v1/receipts/export"} {
			h, logs := fakeReceipts(failing{err: c.err})
			var e httpapi.APIError
			if read(t, getAs(h, path), c.status, &e); e.Error.Code != c.code {
				t.Errorf("%s, %v: %+v", path, c.err, e)
			}
			if (logs.Len() != 0) != c.logged || strings.Contains(logs.String(), fakeAccount) || strings.Contains(logs.String(), "storage down") {
				t.Errorf("%s, %v logged:\n%s", path, c.err, logs)
			}
		}
	}
	// A request its client left is not logged.
	h, logs := fakeReceipts(failing{err: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", appOrigin+"/api/v1/receipts", nil)
	req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "x"})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if logs.Len() != 0 {
		t.Fatalf("a request its client left is logged:\n%s", logs)
	}
}
