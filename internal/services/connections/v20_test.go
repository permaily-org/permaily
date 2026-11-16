package connections

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// V20. Start: a request with and without a query; refusals shown or
// redirected; the request sealed, bound to its session, expiring at 15
// minutes; a blocked, closing, gone or not-agreed account; the notice in
// force shown.
func TestV20Start(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	ctx := context.Background()
	p := w.person(accountA)

	t.Run("without a query", func(t *testing.T) {
		req := w.start(s, p, w.ask(claudeCode))
		if req.Query != nil || req.ModelNotice != notice || req.EndsInDays != oauth.ConnectionDefaultDays || !req.Since.New ||
			len(req.Draft.Mailboxes) != 2 || len(req.Rules) != 1 || req.Rules[0].ID == "" || req.Resource != contracts.ResourceMCP {
			t.Fatalf("%+v", req)
		}
		if strings.Join(partsStrings(req.Parts), " ") != "sender subject body" {
			t.Fatal(req.Parts)
		}
	})

	t.Run("with a query, pushed", func(t *testing.T) {
		a := w.ask(otherApp)
		a.resource = apiResource
		form := url.Values{"client_id": {otherApp}, "redirect_uri": {a.redirect}, "response_type": {"code"}, "code_challenge": {a.challenge},
			"code_challenge_method": {"S256"}, "resource": {apiResource}, "state": {"s"}, "ends_in_days": {"30"},
			"mail_since": {start.Add(-72 * time.Hour).Format(time.RFC3339)},
			"query":      {`{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]}]}`}}
		uri, err := s.Push(ctx, form)
		if err != nil || !strings.HasPrefix(uri, oauth.RequestURIPrefix+oauth.PrefixPushed) {
			t.Fatal(uri, err)
		}
		got, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode())
		if err != nil || got.Request == nil {
			t.Fatal(got, err)
		}
		req := got.Request
		if req.Query == nil || req.EndsInDays != 30 || req.Since.New || req.Resource != contracts.ResourceAPI || req.Vendor.Verified != "" || req.Loopback {
			t.Fatalf("%+v", req)
		}
		// A pushed request is used once.
		if _, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode()); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		// Taken only there: query on the address bar is refused, redirected.
		a2 := w.ask(otherApp)
		a2.extra = url.Values{"query": {"{}"}}
		if got, err := s.Start(ctx, p, a2.query()); err != nil || !strings.Contains(got.Redirect, "error=invalid_request") {
			t.Fatal(got, err)
		}
	})

	t.Run("refusals shown, never redirected", func(t *testing.T) {
		cases := map[string]struct {
			query string
			want  error
			why   contracts.ClientProblem
		}{
			"no client":       {"redirect_uri=x", contracts.ErrInvalidClient, contracts.ClientIDInvalid},
			"unknown client":  {url.Values{"client_id": {"https://nobody.example/c"}}.Encode(), contracts.ErrInvalidClient, contracts.ClientIDInvalid},
			"two client ids":  {url.Values{"client_id": {claudeCode, claudeCode}}.Encode(), contracts.ErrInvalidClient, contracts.ClientIDInvalid},
			"bad redirect":    {url.Values{"client_id": {claudeCode}, "redirect_uri": {"https://evil.example/x"}}.Encode(), contracts.ErrInvalidClient, contracts.RedirectURIInvalid},
			"no redirect":     {url.Values{"client_id": {claudeCode}}.Encode(), contracts.ErrInvalidClient, contracts.RedirectURIInvalid},
			"malformed query": {"%zz", contracts.ErrInvalidClient, contracts.ClientIDInvalid},
		}
		for name, c := range cases {
			got, err := s.Start(ctx, p, c.query)
			if !errors.Is(err, c.want) || got != nil || ClientProblemOf(err) != c.why {
				t.Errorf("%s: %v %v", name, got, err)
			}
		}
		w.vendors.mu.Lock()
		w.vendors.fail = vendors.ErrDocument
		w.vendors.mu.Unlock()
		if _, err := s.Start(ctx, p, w.ask(claudeCode).query()); ClientProblemOf(err) != contracts.ClientDocumentInvalid {
			t.Error(err)
		}
		w.vendors.mu.Lock()
		w.vendors.fail = vendors.ErrUnreachable
		w.vendors.mu.Unlock()
		if _, err := s.Start(ctx, p, w.ask(claudeCode).query()); !errors.Is(err, contracts.ErrVendorUnreachable) {
			t.Error(err)
		}
		w.vendors.mu.Lock()
		w.vendors.fail = nil
		w.vendors.mu.Unlock()
		if ClientProblemOf(errors.New("x")) != "" {
			t.Error("a reason for nothing")
		}
	})

	t.Run("refusals redirected once the redirect URI is proven", func(t *testing.T) {
		for name, change := range map[string]func(*ask){
			"response type": func(a *ask) { a.extra = url.Values{"response_type": {"token"}} },
			"plain PKCE":    func(a *ask) { a.extra = url.Values{"code_challenge_method": {"plain"}} },
			"no resource":   func(a *ask) { a.resource = "" },
			"other resource": func(a *ask) {
				a.resource = "https://elsewhere.example/mcp"
			},
			"unknown scope": func(a *ask) { a.scope = "mail.everything" },
			"repeated":      func(a *ask) { a.extra = url.Values{"state": {"a", "b"}} },
			"long state":    func(a *ask) { a.state = strings.Repeat("s", oauth.StateMaxBytes+1) },
			"request_uri":   func(a *ask) { a.extra = url.Values{"request_uri": {"urn:x"}} },
		} {
			a := w.ask(claudeCode)
			change(a)
			q := a.query()
			if name == "request_uri" {
				q = url.Values{"client_id": {claudeCode}, "request_uri": {oauth.RequestURIPrefix + "pmp_nothing"}}.Encode()
				if _, err := s.Start(ctx, p, q); !errors.Is(err, contracts.ErrRequestEnded) {
					t.Errorf("%s: %v", name, err)
				}
				continue
			}
			got, err := s.Start(ctx, p, q)
			if err != nil || got.Request != nil || !strings.HasPrefix(got.Redirect, loopback) || !strings.Contains(got.Redirect, "error=") ||
				!strings.Contains(got.Redirect, "iss=") {
				t.Errorf("%s: %+v %v", name, got, err)
			}
		}
	})

	t.Run("bound to its session and its 15 minutes", func(t *testing.T) {
		req := w.start(s, p, w.ask(claudeCode))
		other := p
		other.Session = "ses_other"
		if _, err := s.Decide(ctx, other, req.ID, contracts.AuthorizationDecision{}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		if _, err := s.Decide(ctx, p, "pmq_nothing", contracts.AuthorizationDecision{}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		if _, err := s.Decide(ctx, p, "not a request", contracts.AuthorizationDecision{}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		// Sealed: the table holds no request in clear.
		for _, e := range w.mem.AllEntries() {
			if strings.Contains(string(e.Value), "claude") || strings.Contains(string(e.Value), "localhost") {
				t.Fatalf("an entry in clear: %s", e.Partition)
			}
		}
		w.clock.add(oauth.RequestTTL)
		if _, err := s.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
	})

	t.Run("the account's standing", func(t *testing.T) {
		for st, want := range map[Standing]error{
			StandingBlocked: contracts.ErrUnauthorized, StandingClosing: contracts.ErrAccountClosing, StandingGone: contracts.ErrUnauthorized,
			StandingNotAgreed: contracts.ErrTermsChanged,
		} {
			w.accts.set(accountA, st)
			if _, err := s.Start(ctx, w.person(accountA), w.ask(claudeCode).query()); !errors.Is(err, want) {
				t.Errorf("%v: %v", st, err)
			}
		}
		w.accts.set(accountA, StandingOpen)
		w.accts.mu.Lock()
		w.accts.fail = errors.New("unreadable")
		w.accts.mu.Unlock()
		if _, err := s.Start(ctx, w.person(accountA), w.ask(claudeCode).query()); err == nil {
			t.Error("an unreadable account allowed")
		}
		w.accts.mu.Lock()
		w.accts.fail = nil
		w.accts.mu.Unlock()
	})
}

func partsStrings(ps []contracts.Part) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// V21. Decide.
func TestV21Decide(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	ctx := context.Background()
	p := w.person(accountA)

	t.Run("a fresh proof, and still fresh at the commit", func(t *testing.T) {
		req := w.start(a, p, w.ask(claudeCode))
		stale := p
		stale.ConfirmedUntil = w.clock.now()
		d := req.Draft
		in := contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}
		if _, err := a.Decide(ctx, stale, req.ID, in); !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Fatal(err)
		}
		// The proof ends while the document is fetched again.
		w.vendors.mu.Lock()
		w.vendors.onClient = func() { w.clock.add(11 * time.Minute) }
		w.vendors.mu.Unlock()
		t.Cleanup(func() {
			w.vendors.mu.Lock()
			w.vendors.onClient = nil
			w.vendors.mu.Unlock()
		})
		fresh := w.person(accountA)
		if _, err := a.Decide(ctx, fresh, req.ID, in); !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Fatal(err)
		}
		w.vendors.mu.Lock()
		w.vendors.onClient = nil
		w.vendors.mu.Unlock()
		if l, _, _ := a.readList(ctx, accountA); l != nil && len(l.Members) != 0 {
			t.Fatal("committed without a proof")
		}
	})

	t.Run("denied without one, once, through either enclave", func(t *testing.T) {
		req := w.start(a, w.person(accountA), w.ask(claudeCode))
		stale := w.person(accountA)
		stale.ConfirmedUntil = time.Time{}
		ans, err := b.Decide(ctx, stale, req.ID, contracts.AuthorizationDecision{})
		if err != nil || !strings.Contains(ans.Redirect, "error=access_denied") || !strings.Contains(ans.Redirect, "state=st-1") || !strings.Contains(ans.Redirect, "iss=") {
			t.Fatal(ans, err)
		}
		if _, err := b.Decide(ctx, stale, req.ID, contracts.AuthorizationDecision{}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		// Its mark deleted, and another enclave with no memory of it: the
		// list says it was decided.
		for _, e := range w.mem.AllEntries() {
			if e.Key == markKey {
				_ = w.mem.DeleteEntry(ctx, e.Partition, e.Key)
			}
		}
		d := req.Draft
		if _, err := w.enclave().Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		if len(w.rcpt.of("consent")) == 0 || w.rcpt.of("consent")[0].Consent.Allowed {
			t.Fatal("no receipt of the refusal")
		}
	})

	t.Run("the notice in force", func(t *testing.T) {
		req := w.start(a, w.person(accountA), w.ask(claudeCode))
		d := req.Draft
		in := contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: "an older one", Snapshot: &req.Snapshot}
		if _, err := a.Decide(ctx, w.person(accountA), req.ID, in); !errors.Is(err, contracts.ErrModelNoticeChanged) {
			t.Fatal(err)
		}
		// Decided through an enclave of a newer image, after a start through
		// an older one.
		w.notice = "2026-12-01"
		newer := w.enclave()
		w.notice = notice
		in.ModelNotice = req.ModelNotice
		if _, err := newer.Decide(ctx, w.person(accountA), req.ID, in); !errors.Is(err, contracts.ErrModelNoticeChanged) {
			t.Fatal(err)
		}
	})

	t.Run("a stale snapshot commits nothing", func(t *testing.T) {
		req := w.start(a, w.person(accountA), w.ask(claudeCode))
		d := req.Draft
		for _, snap := range []contracts.ConsentSnapshot{
			{Change: contracts.ChangeAddDevice, EndsAt: req.Snapshot.EndsAt, DraftID: req.Snapshot.DraftID},
			{Change: req.Snapshot.Change, EndsAt: req.Snapshot.EndsAt.Add(-2 * time.Minute), DraftID: req.Snapshot.DraftID},
			{Change: req.Snapshot.Change, EndsAt: req.Snapshot.EndsAt, DraftID: "drf_other"},
			{Change: req.Snapshot.Change, EndsAt: req.Snapshot.EndsAt, DraftID: req.Snapshot.DraftID, Connection: "con_x", Version: 3},
		} {
			if _, err := a.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &snap}); !errors.Is(err, contracts.ErrConsentChanged) {
				t.Fatal(err)
			}
		}
		if _, err := a.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, ModelNotice: notice}); !errors.Is(err, ErrInvalidBody) {
			t.Fatal(err)
		}
		if l, _, _ := a.readList(ctx, accountA); l != nil {
			for _, m := range l.Members {
				t.Fatalf("committed: %+v", m)
			}
		}
	})

	t.Run("started through A, allowed through B: code, state and iss", func(t *testing.T) {
		req := w.start(a, w.person(accountA), w.ask(claudeCode))
		d := req.Draft
		ans, err := b.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(ans.Redirect)
		q := u.Query()
		if !strings.HasPrefix(q.Get("code"), oauth.PrefixCode) || q.Get("state") != "st-1" || q.Get("iss") != issuer || u.Host != "localhost" {
			t.Fatal(ans.Redirect)
		}
		if c := w.rcpt.of("consent"); !c[len(c)-1].Consent.Allowed || c[len(c)-1].Connection == "" {
			t.Fatalf("%+v", c[len(c)-1])
		}
	})

	t.Run("a document that no longer has the redirect", func(t *testing.T) {
		req := w.start(a, w.person(accountA), w.ask(otherApp))
		w.vendors.mu.Lock()
		doc := w.vendors.clients[otherApp]
		saved := doc.Document.RedirectURIs
		doc.Document.RedirectURIs = []string{"https://tools.example.org/elsewhere"}
		w.vendors.mu.Unlock()
		d := req.Draft
		if _, err := a.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); ClientProblemOf(err) != contracts.RedirectURIInvalid {
			t.Fatal(err)
		}
		w.vendors.mu.Lock()
		doc.Document.RedirectURIs = saved
		w.vendors.mu.Unlock()
	})
}

// failing is the records of one enclave with a test's hook before a write.
type failing struct {
	Records
	mu     sync.Mutex
	before func(op, name string) error
}

func (f *failing) hook(op, name string) error {
	f.mu.Lock()
	h := f.before
	f.mu.Unlock()
	if h == nil {
		return nil
	}
	return h(op, name)
}

func (f *failing) set(h func(op, name string) error) {
	f.mu.Lock()
	f.before = h
	f.mu.Unlock()
}

func (f *failing) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	if err := f.hook("put", r.Name); err != nil {
		return err
	}
	return f.Records.PutRecordAt(ctx, r, at)
}

func (f *failing) CreateNew(ctx context.Context, r *store.Record) error {
	if err := f.hook("create", r.Name); err != nil {
		return err
	}
	return f.Records.CreateNew(ctx, r)
}

func (f *failing) RemoveRecord(ctx context.Context, name string) error {
	if err := f.hook("remove", name); err != nil {
		return err
	}
	return f.Records.RemoveRecord(ctx, name)
}

func (f *failing) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	if err := f.hook("get", name); err != nil {
		return nil, store.Mark{}, err
	}
	return f.Records.GetRecordAt(ctx, name)
}

// hooked is an enclave whose writes a test can stop.
func (w *world) hooked() (*Service, *failing) {
	w.t.Helper()
	f := &failing{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}}
	s, err := New(w.config(f))
	if err != nil {
		w.t.Fatal(err)
	}
	return s, f
}

var errCrash = errors.New("the enclave stopped here")

// V22. Making a connection: the standing read uncached, the list's pending
// entry, the record by CreateNew, the client's current one; a crash after
// each step fenced by Sweep; two approvals for one client through two
// enclaves at once.
func TestV22MakingAConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("the standing read uncached", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		req := w.start(s, w.person(accountA), w.ask(claudeCode))
		w.accts.set(accountA, StandingBlocked) // within the cache's five seconds
		d := req.Draft
		if _, err := s.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatal(err)
		}
	})

	t.Run("its steps", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		w.connect(s, w.person(accountA), w.ask(claudeCode))
		l, _, _ := s.readList(ctx, accountA)
		if len(l.Members) != 1 || l.Members[0].Pending || l.Current[claudeCode] != l.Members[0].ID {
			t.Fatalf("%+v", l)
		}
		d, at, err := s.readConn(ctx, l.Members[0].ID)
		if err != nil || d.State != contracts.ConnectionActive || at.Version() != 2 { // made, then its code exchanged
			t.Fatal(d, at.Version(), err)
		}
		// Made with CreateNew: it cannot be made again over itself.
		if err := s.writeConn(ctx, d, store.Mark{}, true); !errors.Is(err, store.ErrExists) {
			t.Fatal(err)
		}
	})

	for _, crash := range []struct {
		name string
		at   func(op, name string) bool
	}{
		{"after the pending entry", func(op, name string) bool { return op == "create" && strings.HasPrefix(name, RecordConnection) }},
		{"after the record", func(op, name string) bool {
			return op == "put" && strings.HasPrefix(name, RecordList)
		}},
	} {
		t.Run("a crash "+crash.name+", fenced by the sweep", func(t *testing.T) {
			w := newWorld(t)
			s, f := w.hooked()
			req := w.start(s, w.person(accountA), w.ask(claudeCode))
			puts := 0
			f.set(func(op, name string) error {
				if (op == "put" || op == "create") && strings.HasPrefix(name, RecordList) {
					puts++
					if puts == 1 {
						return nil // the pending entry is written
					}
				}
				if crash.at(op, name) {
					return errCrash
				}
				return nil
			})
			d := req.Draft
			if _, err := s.Decide(ctx, w.person(accountA), req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, errCrash) {
				t.Fatal(err)
			}
			f.set(nil)
			l, _, _ := s.readList(ctx, accountA)
			if len(l.Members) != 1 || !l.Members[0].Pending {
				t.Fatalf("%+v", l.Members)
			}
			id := l.Members[0].ID
			other := w.enclave()
			if err := other.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if l, _, _ := other.readList(ctx, accountA); !l.Members[0].Pending { // too young to fence
				t.Fatal("fenced while its maker may still be at work")
			}
			w.clock.add(pendingFenceAfter)
			if err := other.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			l, _, _ = other.readList(ctx, accountA)
			if l.Members[0].State != contracts.ConnectionEnded || l.Current[claudeCode] != "" {
				t.Fatalf("%+v", l.Members)
			}
			c, _, err := other.readConn(ctx, id)
			if err != nil || c == nil || c.State != contracts.ConnectionEnded {
				t.Fatal(c, err)
			}
		})
	}

	t.Run("two approvals for one client through two enclaves at once", func(t *testing.T) {
		w := newWorld(t)
		a, fa := w.hooked()
		b, fb := w.hooked()
		p1, p2 := w.person(accountA), w.person(accountA)
		p2.Session = "ses_second"
		r1 := w.start(a, p1, w.ask(claudeCode))
		r2 := w.start(b, p2, w.ask(claudeCode))
		// Both have worked out what approving does before either writes.
		var arrived sync.WaitGroup
		arrived.Add(2)
		for _, f := range []*failing{fa, fb} {
			var once sync.Once
			f.set(func(op, name string) error {
				if (op == "create" || op == "put") && strings.HasPrefix(name, RecordList) {
					once.Do(func() { arrived.Done(); arrived.Wait() })
				}
				return nil
			})
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, x := range []struct {
			s   *Service
			p   Person
			req *contracts.AuthorizationRequest
		}{{a, p1, r1}, {b, p2, r2}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := x.req.Draft
				_, errs[i] = x.s.Decide(ctx, x.p, x.req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &x.req.Snapshot})
			}()
		}
		wg.Wait()
		fa.set(nil)
		fb.set(nil)
		if errs[0] != nil || errs[1] != nil {
			t.Fatal(errs)
		}
		l, _, _ := a.readList(ctx, accountA)
		active, replaced := 0, 0
		for _, m := range l.Members {
			c, _, _ := a.readConn(ctx, m.ID)
			switch {
			case c.State == contracts.ConnectionActive && l.Current[claudeCode] == c.ID:
				active++
			case c.EndReason == contracts.EndedByReplaced && c.ReplacedBy == l.Current[claudeCode]:
				replaced++
			}
		}
		if active != 1 || replaced != 1 {
			t.Fatalf("%d current, %d replaced: %+v", active, replaced, l)
		}
	})
}
