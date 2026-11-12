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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The accounts of the mailbox routes' tests, with identifiers shaped as the
// service makes them, so the log's check means something.
const (
	alice = "acc_aliceaaaaaaaaaaaaaaaaaaaaa"
	bob   = "acc_bobbbbbbbbbbbbbbbbbbbbbbbb"
)

// What a hostile sender puts in a message: none of it may reach an answer
// as markup, a header, an error or a log.
const (
	hostileSubject = `<img src=x onerror=alert(1)> Subject-7f3a`
	hostileSender  = "evil-7f3a@attacker.example"
	hostileName    = "../../<script>x</script>\"\r\nSet-Cookie: a=1;\u202Egnp.exe"
	hostileText    = `<script>alert("text-7f3a")</script>`
)

func hostileRaw() string {
	return "From: \"<b>Eve</b>\" <" + hostileSender + ">\r\nTo: box@inbox.permaily.test\r\nSubject: " + hostileSubject + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: multipart/alternative; boundary=c\r\n\r\n" +
		"--c\r\nContent-Type: text/html\r\n\r\n<html><body onload=alert(1)><p>" + hostileText + "</p><a href=\"javascript:alert(1)\">x</a></body></html>\r\n--c--\r\n" +
		"--b\r\nContent-Type: text/html\r\nContent-Disposition: attachment; filename=\"evil.html\"\r\n\r\n<script>alert(document.cookie)</script>\r\n" +
		"--b\r\nContent-Type: image/svg+xml\r\nContent-Disposition: attachment\r\n\r\n<svg onload=alert(1)/>\r\n--b--\r\n"
}

type sessions map[string]*accounts.Authenticated

func (s sessions) Authenticate(_ context.Context, cookie string) (*accounts.Authenticated, error) {
	if who, ok := s[cookie]; ok {
		return who, nil
	}
	return nil, contracts.ErrUnauthorized
}

// watched records which mailboxes the service opened and the reader read,
// so a test can show that another account's mailbox was never read.
type watched struct {
	httpapi.MailboxesService
	mu     sync.Mutex
	opened []string
}

func (w *watched) Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error) {
	b, err := w.MailboxesService.Open(ctx, c, id, a)
	if err == nil {
		w.mu.Lock()
		w.opened = append(w.opened, id)
		w.mu.Unlock()
	}
	return b, err
}

type watchedReader struct {
	inbox.Reader
	mu   sync.Mutex
	read []string
}

func (w *watchedReader) saw(b inbox.Box) {
	w.mu.Lock()
	w.read = append(w.read, b.MailboxID)
	w.mu.Unlock()
}

func (w *watchedReader) List(ctx context.Context, b inbox.Box, before string, limit int) (*contracts.MessagePage, error) {
	w.saw(b)
	return w.Reader.List(ctx, b, before, limit)
}

func (w *watchedReader) Search(ctx context.Context, b inbox.Box, q, before string, limit int) (*contracts.MessagePage, error) {
	w.saw(b)
	return w.Reader.Search(ctx, b, q, before, limit)
}

func (w *watchedReader) Get(ctx context.Context, b inbox.Box, id string) (*contracts.Message, error) {
	w.saw(b)
	return w.Reader.Get(ctx, b, id)
}

func (w *watchedReader) Raw(ctx context.Context, b inbox.Box, id string) ([]byte, error) {
	w.saw(b)
	return w.Reader.Raw(ctx, b, id)
}

func (w *watchedReader) Attachment(ctx context.Context, b inbox.Box, id string, index int) (string, []byte, error) {
	w.saw(b)
	return w.Reader.Attachment(ctx, b, id, index)
}

func (w *watchedReader) Delete(ctx context.Context, b inbox.Box, ids []string) (int, error) {
	w.saw(b)
	return w.Reader.Delete(ctx, b, ids)
}

// inboxApp is the person's mailbox routes over mailboxestest.Fake and the
// real reader on an in-memory store, with Alice's and Bob's sessions.
type inboxApp struct {
	t      *testing.T
	fake   *mailboxestest.Fake
	svc    *watched
	reader *watchedReader
	mem    *store.Memory
	cfg    httpapi.MailboxesConfig
	h      http.Handler
	logs   *bytes.Buffer
	now    time.Time
	n      int
}

func newInboxApp(t *testing.T, edit func(*httpapi.MailboxesConfig)) *inboxApp {
	t.Helper()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	fake := mailboxestest.New()
	fake.Now = func() time.Time { return now }
	mem := store.NewMemory()
	mem.Now = func() time.Time { return now }
	a := &inboxApp{t: t, fake: fake, mem: mem, logs: &bytes.Buffer{}, now: now}
	a.svc = &watched{MailboxesService: fake}
	a.reader = &watchedReader{Reader: inbox.NewReader(mem)}
	a.cfg = httpapi.MailboxesConfig{
		Service: a.svc, Reader: a.reader, Log: slog.New(slog.NewJSONHandler(a.logs, nil)),
		Sessions: sessions{
			"alice":       {AccountID: alice, Session: contracts.Session{ID: "ses_a", ConfirmedUntil: now.Add(time.Minute)}},
			"alice-stale": {AccountID: alice, Session: contracts.Session{ID: "ses_b"}},
			"bob":         {AccountID: bob, Session: contracts.Session{ID: "ses_c", ConfirmedUntil: now.Add(time.Minute)}},
		},
		Wait: 50 * time.Millisecond, Now: func() time.Time { return now },
	}
	if edit != nil {
		edit(&a.cfg)
	}
	a.h = httpapi.MailboxesAPI(a.cfg)
	return a
}

func (a *inboxApp) call(cookie, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "https://app.permaily.test"+path, nil)
	} else {
		r = httptest.NewRequest(method, "https://app.permaily.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		r.Header.Set("Origin", appOrigin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	return w
}

// seed stores a message in a mailbox as delivery does.
func (a *inboxApp) seed(mailbox string, raw string, meta inbox.StoredMeta) string {
	a.t.Helper()
	a.n++
	at := a.now.Add(-time.Duration(100-a.n) * time.Minute)
	id := fmt.Sprintf("%016x%016x", at.UnixNano(), a.n)
	b := inbox.Box{MailboxID: mailbox, Key: mailboxestest.Key(mailbox)}
	m := inboxtest.Message{ID: id, ReceivedAt: at, RetentionDays: 30, Raw: []byte(raw), Meta: meta,
		Search: &inbox.SearchEntry{From: meta.From, Subject: meta.Subject, Text: hostileText, ReceivedAt: at}}
	if err := inboxtest.Seed(context.Background(), a.mem, a.mem, b, m); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func hostileMeta() inbox.StoredMeta {
	return inbox.StoredMeta{
		From: []contracts.MailAddress{{Name: "<b>Eve</b>", Address: hostileSender}}, Subject: hostileSubject, Snippet: hostileText,
		Attachments: []contracts.MessageAttachment{{Index: 0, Name: hostileName, Type: "text/html", Size: 40}, {Index: 1, Name: "", Type: "image/svg+xml", Size: 22}},
	}
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e httpapi.APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("%d %q is not an error", rec.Code, rec.Body)
	}
	return e.Error.Code
}

func want(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, status, code)
	}
	if code != "" {
		if got := errorCode(t, rec); got != code {
			t.Fatalf("code %q, want %q", got, code)
		}
	}
}

// route is one route of the person's table, called on a mailbox and a
// message.
type route struct {
	name, method, path, body string
	ok                       int
}

func routes(mailbox, message string) []route {
	m := "/api/v1/mailboxes/" + mailbox
	return []route{
		{"list mailboxes", "GET", "/api/v1/mailboxes", "", 200},
		{"make a mailbox", "POST", "/api/v1/mailboxes", `{"label":"Work"}`, 201},
		{"a mailbox", "GET", m, "", 200},
		{"change a mailbox", "PATCH", m, `{"label":"Home"}`, 200},
		{"list messages", "GET", m + "/messages", "", 200},
		{"search", "GET", m + "/messages?q=subject-7f3a", "", 200},
		{"a message", "GET", m + "/messages/" + message, "", 200},
		{"the raw message", "GET", m + "/messages/" + message + "/raw", "", 200},
		{"an attachment", "GET", m + "/messages/" + message + "/attachments/0", "", 200},
		{"delete many", "POST", m + "/messages/delete", `{"ids":["` + message + `"]}`, 200},
		{"delete one", "DELETE", m + "/messages/" + message, "", 204},
		{"disconnect", "DELETE", m, "", 204},
	}
}

// R1: every route of the person's table, with every cell of the access
// table (ADR 0026, "Access while an account is closing or has not agreed").
func TestR1EveryRouteInEveryStanding(t *testing.T) {
	type cell struct {
		status int
		code   string
	}
	yes := cell{}
	closing := cell{409, "account_closing"}
	terms := cell{400, "terms_needed"}
	table := map[string][3]cell{ // agreed and open, closing, terms not agreed
		"list mailboxes":   {yes, yes, yes},
		"make a mailbox":   {yes, closing, terms},
		"a mailbox":        {yes, yes, yes},
		"change a mailbox": {yes, closing, terms},
		"list messages":    {yes, yes, yes},
		"search":           {yes, yes, terms},
		"a message":        {yes, yes, terms},
		"the raw message":  {yes, yes, yes},
		"an attachment":    {yes, yes, yes},
		"delete many":      {yes, closing, terms},
		"delete one":       {yes, closing, terms},
		"disconnect":       {yes, yes, yes},
	}
	standings := []mailboxes.Standing{{TermsAgreed: true}, {Closing: true, TermsAgreed: true}, {}}
	for i, standing := range standings {
		for _, name := range slices.Sorted(func(yield func(string) bool) {
			for k := range table {
				if !yield(k) {
					return
				}
			}
		}) {
			a := newInboxApp(t, nil)
			box := a.fake.Add(alice, "Mine")
			msg := a.seed(box.ID, hostileRaw(), hostileMeta())
			a.fake.Accounts.Set(alice, standing)
			var r route
			for _, x := range routes(box.ID, msg) {
				if x.name == name {
					r = x
				}
			}
			rec := a.call("alice", r.method, r.path, r.body)
			c := table[name][i]
			if c == yes {
				if rec.Code != r.ok {
					t.Errorf("standing %d, %s: %d %s, want %d", i, name, rec.Code, rec.Body, r.ok)
				}
				continue
			}
			if rec.Code != c.status || errorCode(t, rec) != c.code {
				t.Errorf("standing %d, %s: %d %s, want %d %s", i, name, rec.Code, rec.Body, c.status, c.code)
			}
		}
	}

	t.Run("closing and not agreed: account_closing where the closing column refuses", func(t *testing.T) {
		a := newInboxApp(t, nil)
		box := a.fake.Add(alice, "Mine")
		msg := a.seed(box.ID, hostileRaw(), hostileMeta())
		a.fake.Accounts.Set(alice, mailboxes.Standing{Closing: true})
		want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 409, "account_closing")
		want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 400, "terms_needed")
		want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/raw", ""), 200, "")
	})

	t.Run("a blocked account, no session, a session that stands for nothing: 401 everywhere", func(t *testing.T) {
		a := newInboxApp(t, nil)
		box := a.fake.Add(alice, "Mine")
		msg := a.seed(box.ID, hostileRaw(), hostileMeta())
		for _, r := range routes(box.ID, msg) {
			for _, cookie := range []string{"", "forged"} {
				rec := a.call(cookie, r.method, r.path, r.body)
				want(t, rec, 401, "unauthorized")
				if cookie == "forged" && !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
					t.Errorf("%s: the cookie is kept", r.name)
				}
			}
		}
		a.fake.Accounts.Block(alice)
		for _, r := range routes(box.ID, msg) {
			want(t, a.call("alice", r.method, r.path, r.body), 401, "unauthorized")
		}
	})
}

// R1 and R8: another account's mailbox, or its message, answers exactly as
// one that does not exist, and before anything of it is read.
func TestR1AStrangersMailboxIsNotFound(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, hostileRaw(), hostileMeta())
	mine := a.fake.Add(bob, "Bob's")
	mineMsg := a.seed(mine.ID, plainRaw, inbox.StoredMeta{Subject: "hello"})
	touched := false
	a.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == "mailbox#"+box.ID {
			touched = true
		}
		return nil
	}
	for _, r := range routes(box.ID, msg) {
		if r.path == "/api/v1/mailboxes" {
			continue
		}
		stranger := a.call("bob", r.method, r.path, r.body)
		never := a.call("bob", r.method, strings.Replace(r.path, box.ID, "mbx_00000000000000000000000999", 1), r.body)
		want(t, stranger, 404, "not_found")
		if stranger.Body.String() != never.Body.String() || stranger.Code != never.Code {
			t.Errorf("%s: %q and %q", r.name, stranger.Body, never.Body)
		}
	}
	if touched || slices.Contains(a.svc.opened, box.ID) || slices.Contains(a.reader.read, box.ID) {
		t.Fatalf("Alice's mailbox was read for Bob: store %v, opened %v, read %v", touched, a.svc.opened, a.reader.read)
	}
	// Alice's message asked for through Bob's own mailbox is not there.
	for _, path := range []string{"", "/raw", "/attachments/0"} {
		want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+mine.ID+"/messages/"+msg+path, ""), 404, "not_found")
	}
	want(t, a.call("bob", "DELETE", "/api/v1/mailboxes/"+mine.ID+"/messages/"+msg, ""), 404, "not_found")
	if touched {
		t.Fatal("Alice's partition was read")
	}
	want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+mine.ID+"/messages/"+mineMsg, ""), 200, "")
}

const plainRaw = "From: a@example.org\r\nSubject: hello\r\nContent-Type: text/plain\r\n\r\nHello\r\n"

// R1: each route's answers and errors besides the standing, and their order.
func TestR1AnswersAndErrors(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	base := "/api/v1/mailboxes/" + box.ID
	var ids []string
	for range 3 {
		ids = append(ids, a.seed(box.ID, plainRaw, inbox.StoredMeta{Subject: "hello", From: []contracts.MailAddress{{Address: "a@example.org"}}}))
	}

	t.Run("mailboxes", func(t *testing.T) {
		var list contracts.MailboxList
		rec := a.call("alice", "GET", "/api/v1/mailboxes", "")
		read(t, rec, 200, &list)
		if len(list.Mailboxes) != 1 || list.Limit != mailboxes.MaxMailboxes {
			t.Fatalf("%+v", list)
		}
		var made contracts.Mailbox
		read(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":"Work"}`), 201, &made)
		want(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":"`+strings.Repeat("x", 65)+`"}`), 400, "invalid_label")
		want(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":`), 400, "invalid_body")
		want(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":"a","x":1}`), 400, "invalid_body")
		for range 3 {
			read(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":"More"}`), 201, nil)
		}
		want(t, a.call("alice", "POST", "/api/v1/mailboxes", `{"label":"Sixth"}`), 409, "too_many_mailboxes")
		var got contracts.Mailbox
		read(t, a.call("alice", "GET", "/api/v1/mailboxes/"+made.ID, ""), 200, &got)
		read(t, a.call("alice", "PATCH", "/api/v1/mailboxes/"+made.ID, `{"retention_days":30}`), 200, &got)
		if got.RetentionDays != 30 {
			t.Fatalf("%+v", got)
		}
		want(t, a.call("alice", "PATCH", "/api/v1/mailboxes/"+made.ID, `{"retention_days":31}`), 400, "invalid_retention")
		want(t, a.call("alice", "PATCH", "/api/v1/mailboxes/"+made.ID, `nonsense`), 400, "invalid_body")
		want(t, a.call("bob", "PATCH", "/api/v1/mailboxes/"+made.ID, `nonsense`), 404, "not_found")
		want(t, a.call("alice-stale", "DELETE", "/api/v1/mailboxes/"+made.ID, ""), 403, "confirmation_needed")
		want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+made.ID, ""), 204, "")
		want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+made.ID, ""), 204, "") // a second disconnect answers as the first
		want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+made.ID, ""), 404, "not_found")
		// Until the sweep's last pass, not after it (C17): then the owner's
		// disconnect is not_found, as everyone else's.
		a.fake.Sweep()
		want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+made.ID, ""), 404, "not_found")
		want(t, a.call("bob", "DELETE", "/api/v1/mailboxes/"+made.ID, ""), 404, "not_found")
	})

	t.Run("an unreadable mailbox", func(t *testing.T) {
		lost := a.fake.Add(bob, "Lost")
		forked := a.fake.Add(bob, "Forked")
		a.fake.MakeLost(lost.ID)
		a.fake.MakeForked(forked.ID)
		for _, id := range []string{lost.ID, forked.ID} {
			want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+id+"/messages?limit=0", ""), 409, "mailbox_unreadable")
			want(t, a.call("bob", "PATCH", "/api/v1/mailboxes/"+id, `{"label":"x"}`), 409, "mailbox_unreadable")
		}
		rec := a.call("bob", "DELETE", "/api/v1/mailboxes/"+forked.ID, "")
		want(t, rec, 409, "mailbox_unreadable")
		var e httpapi.APIError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error.Message != contracts.ErrMailboxForked.Message {
			t.Fatalf("%q", e.Error.Message)
		}
		want(t, a.call("bob", "DELETE", "/api/v1/mailboxes/"+lost.ID, ""), 204, "")
	})

	t.Run("messages: limits, cursors and queries, after the standing", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=101", "limit=x", "limit=1&limit=2", "limit="} {
			want(t, a.call("alice", "GET", base+"/messages?"+q, ""), 400, "invalid_limit")
		}
		for _, q := range []string{"after=", "after=x", "after=" + strings.ToUpper(ids[0]), "after=" + ids[0] + "0"} {
			want(t, a.call("alice", "GET", base+"/messages?"+q, ""), 400, "invalid_cursor")
		}
		for _, q := range []string{"q=", "q=%20%20", "q=" + strings.Repeat("a", 201), "q=a&q=b", "q=%ff"} {
			want(t, a.call("alice", "GET", base+"/messages?"+q, ""), 400, "invalid_query")
		}
		read(t, a.call("alice", "GET", base+"/messages?q="+strings.Repeat("é", 200), ""), 200, nil)
		a.fake.Accounts.Set(alice, mailboxes.Standing{})
		want(t, a.call("alice", "GET", base+"/messages?q=", ""), 400, "terms_needed")
		read(t, a.call("alice", "GET", base+"/messages?limit=1", ""), 200, nil)
		a.fake.Accounts.Set(alice, mailboxes.Standing{TermsAgreed: true})
		var page contracts.MessagePage
		read(t, a.call("alice", "GET", base+"/messages?limit=2", ""), 200, &page)
		if len(page.Messages) != 2 || page.Next != page.Messages[1].ID {
			t.Fatalf("%+v", page)
		}
		var second contracts.MessagePage
		read(t, a.call("alice", "GET", base+"/messages?limit=2&after="+page.Next, ""), 200, &second)
		if len(second.Messages) != 1 || second.Next != "" {
			t.Fatalf("%+v", second)
		}
		var found contracts.MessagePage
		read(t, a.call("alice", "GET", base+"/messages?q=hello", ""), 200, &found)
		if len(found.Messages) != 3 || found.SearchedTo.IsZero() {
			t.Fatalf("%+v", found)
		}
	})

	t.Run("a message, its raw form, its attachments", func(t *testing.T) {
		var m contracts.Message
		read(t, a.call("alice", "GET", base+"/messages/"+ids[0], ""), 200, &m)
		if m.Text != "Hello\n" || m.HTML != nil {
			t.Fatalf("%+v", m)
		}
		for _, path := range []string{"/nothex", "/" + ids[0] + "0", "/../" + ids[0]} {
			want(t, a.call("alice", "GET", base+"/messages"+path, ""), 404, "not_found")
		}
		for _, index := range []string{"0", "01", "-1", "100", "x", "99999999999999999999"} {
			want(t, a.call("alice", "GET", base+"/messages/"+ids[0]+"/attachments/"+index, ""), 404, "not_found")
		}
		want(t, a.call("alice", "GET", base+"/messages/"+ids[0]+"/html", ""), 404, "not_found")
		want(t, a.call("alice", "GET", base+"/messages/"+ids[0]+"/links/0.AAAAAAAAAAAAAAAA", ""), 404, "not_found")
	})

	t.Run("deleting", func(t *testing.T) {
		want(t, a.call("alice", "POST", base+"/messages/delete", `{"ids":[`+strings.Repeat(`"x",`, 100)+`"x"]}`), 400, "too_many_messages")
		want(t, a.call("alice", "POST", base+"/messages/delete", `{"ids":`), 400, "invalid_body")
		var deleted contracts.MessagesDeleted
		read(t, a.call("alice", "POST", base+"/messages/delete", `{"ids":["`+ids[0]+`","`+ids[0]+`","nothex"]}`), 200, &deleted)
		if deleted.Deleted != 1 {
			t.Fatalf("%+v", deleted)
		}
		want(t, a.call("alice", "DELETE", base+"/messages/"+ids[1], ""), 204, "")
		want(t, a.call("alice", "DELETE", base+"/messages/"+ids[1], ""), 404, "not_found")
		want(t, a.call("alice", "GET", base+"/messages/"+ids[1], ""), 404, "not_found")
	})

	t.Run("methods, paths and origins", func(t *testing.T) {
		for _, c := range []struct{ method, path, allow string }{
			{"PUT", "/api/v1/mailboxes", "GET, POST"},
			{"POST", base, "GET, PATCH, DELETE"},
			{"POST", base + "/messages", "GET"},
			{"PUT", base + "/messages/" + ids[2], "GET, DELETE"},
			{"POST", base + "/messages/" + ids[2] + "/raw", "GET"},
			{"DELETE", base + "/messages/" + ids[2] + "/attachments/0", "GET"},
		} {
			rec := a.call("alice", c.method, c.path, "")
			want(t, rec, 405, "method_not_allowed")
			if rec.Header().Get("Allow") != c.allow {
				t.Errorf("%s %s: allow %q", c.method, c.path, rec.Header().Get("Allow"))
			}
		}
		want(t, a.call("alice", "GET", base+"/elsewhere", ""), 404, "not_found")
		r := httptest.NewRequest("POST", "https://app.permaily.test/api/v1/mailboxes", strings.NewReader(`{"label":""}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://evil.example")
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, r)
		want(t, rec, 403, "forbidden")
	})
}

// R2: no route answers anything of a message as a document. Every answer of
// every route, answered and refused, has nosniff; errors are JSON with fixed
// words; JSON answers are JSON; downloads are downloads; and nothing of
// another account's mailbox was read.
func TestMailboxRoutesNeverAnswerADocument(t *testing.T) {
	fixed := map[string]bool{"no such endpoint": true, "internal error": true, "this request did not come from the app": true, "send JSON": true,
		"the request is not the JSON this endpoint takes": true}
	for _, e := range []*contracts.Error{contracts.ErrUnauthorized, contracts.ErrNotFound, contracts.ErrAccountClosing, contracts.ErrTermsChanged,
		contracts.ErrConfirmationNeeded, contracts.ErrMailboxUnreadable, contracts.ErrMailboxForked, contracts.ErrInvalidLabel,
		contracts.ErrInvalidRetention, contracts.ErrInvalidQuery, contracts.ErrInvalidLimit, contracts.ErrInvalidCursor,
		contracts.ErrTooManyMessages, contracts.ErrTooManyRequests, contracts.ErrTooManyMailboxes} {
		fixed[e.Message] = true
	}
	check := func(t *testing.T, name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", name)
		}
		body := rec.Body.String()
		for _, s := range []string{"7f3a", "<script", "<img", "onload", "alert("} {
			if strings.Contains(body, s) && h.Get("Content-Disposition") == "" && !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
				t.Errorf("%s: %q in a body that is not JSON nor a download", name, s)
			}
		}
		switch {
		case rec.Code == 204:
			if body != "" {
				t.Errorf("%s: a body with 204", name)
			}
		case h.Get("Content-Disposition") != "":
			if ct := h.Get("Content-Type"); ct != "application/octet-stream" && ct != "message/rfc822" {
				t.Errorf("%s: a download of type %q", name, ct)
			}
			if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment; ") || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" ||
				h.Get("Cross-Origin-Resource-Policy") != "same-origin" || h.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: download headers %v", name, h)
			}
		default:
			if h.Get("Content-Type") != "application/json" {
				t.Errorf("%s: %d of type %q", name, rec.Code, h.Get("Content-Type"))
			}
			if rec.Code >= 400 {
				var e httpapi.APIError
				if json.Unmarshal(rec.Body.Bytes(), &e) != nil || !fixed[e.Error.Message] {
					t.Errorf("%s: error %q", name, body)
				}
			} else if !json.Valid(rec.Body.Bytes()) {
				t.Errorf("%s: not JSON", name)
			}
		}
	}
	for _, standing := range []mailboxes.Standing{{TermsAgreed: true}, {Closing: true, TermsAgreed: true}, {}} {
		a := newInboxApp(t, nil)
		box := a.fake.Add(alice, "Mine")
		msg := a.seed(box.ID, hostileRaw(), hostileMeta())
		a.fake.Accounts.Set(alice, standing)
		for _, r := range routes(box.ID, msg) {
			check(t, r.name, a.call("alice", r.method, r.path, r.body))
			if r.path != "/api/v1/mailboxes" {
				check(t, "stranger: "+r.name, a.call("bob", r.method, r.path, r.body))
			}
			check(t, "no session: "+r.name, a.call("", r.method, r.path, r.body))
		}
		for _, extra := range []string{"/messages/" + msg + "/html", "/messages/" + msg + "/attachments/1", "/messages/" + msg + "/attachments/7", "/nothing"} {
			check(t, extra, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+extra, ""))
		}
		if slices.Contains(a.reader.read, "") || len(a.reader.read) == 0 {
			t.Fatalf("read %v", a.reader.read)
		}
		for _, id := range a.reader.read {
			if id != box.ID {
				t.Fatalf("the reader read %s", id)
			}
		}
	}

	t.Run("the message as JSON holds its hostile parts as strings, and the text holds nothing a script tag held", func(t *testing.T) {
		a := newInboxApp(t, nil)
		box := a.fake.Add(alice, "Mine")
		msg := a.seed(box.ID, hostileRaw(), hostileMeta())
		var m contracts.Message
		read(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 200, &m)
		if m.Subject != hostileSubject || m.TextFrom != contracts.TextFromHTML || strings.Contains(m.Text, "alert(\"") || m.Text != "x [javascript:alert(1)]" {
			t.Fatalf("%+v", m)
		}
	})
}

// R8: downloads, with the names a sender chose cleaned.
func TestR8Downloads(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, hostileRaw(), hostileMeta())
	base := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg
	rec := a.call("alice", "GET", base+"/raw", "")
	want(t, rec, 200, "")
	if rec.Header().Get("Content-Type") != "message/rfc822" || rec.Body.String() != hostileRaw() ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="`+msg+`.eml"; filename*=UTF-8''`+msg+`.eml` {
		t.Fatalf("raw: %v", rec.Header())
	}
	rec = a.call("alice", "GET", base+"/attachments/0", "")
	want(t, rec, 200, "")
	disp := rec.Header().Get("Content-Disposition")
	if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Body.String() != "<script>alert(document.cookie)</script>" ||
		strings.ContainsAny(disp, "\r\n\u202E") || strings.Contains(disp, "Set-Cookie: ") || strings.Contains(disp, "/") || strings.Count(disp, `"`) != 2 {
		t.Fatalf("attachment: %q %q", disp, rec.Body)
	}
	if want := `attachment; filename="scriptSet-Cookie a=1_gnp.exe"; filename*=UTF-8''scriptSet-Cookie%20a%3D1%3Bgnp.exe`; disp != want {
		t.Fatalf("%q\nwant %q", disp, want)
	}
	rec = a.call("alice", "GET", base+"/attachments/1", "")
	if want := `attachment; filename="attachment-1"; filename*=UTF-8''attachment-1`; rec.Header().Get("Content-Disposition") != want {
		t.Fatalf("%q", rec.Header().Get("Content-Disposition"))
	}

}

// R6: the read budget: a request that finds it full waits, then is answered
// 429; what a request took is given back once its answer is written.
func TestR6TheReadBudget(t *testing.T) {
	size := int64(len(plainRaw)) + 1<<10
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.ReadBudget = size })
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, plainRaw, inbox.StoredMeta{Subject: "hello"})
	held := make(chan struct{})
	release := make(chan struct{})
	reads := 0
	var mu sync.Mutex
	a.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step != store.StepRead || key != "msg#"+msg {
			return nil
		}
		mu.Lock()
		reads++
		second := reads == 2 // the item, then the raw object
		mu.Unlock()
		if second {
			close(held)
			<-release
		}
		return nil
	}
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/raw"
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- a.call("alice", "GET", path, "") }()
	<-held
	want(t, a.call("alice", "GET", path, ""), 429, "too_many_requests")
	want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 429, "too_many_requests")
	close(release)
	want(t, <-first, 200, "")
	want(t, a.call("alice", "GET", path, ""), 200, "")
	want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 200, "")
}

// Searches: four at once at most, and thirty pages a minute an account.
func TestSearchesAreBounded(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	other := a.fake.Add(bob, "Bob's")
	a.seed(box.ID, plainRaw, inbox.StoredMeta{Subject: "hello"})
	path := "/api/v1/mailboxes/" + box.ID + "/messages?q=hello"
	for range httpapi.SearchPagesPerMinute {
		want(t, a.call("alice", "GET", path, ""), 200, "")
	}
	want(t, a.call("alice", "GET", path, ""), 429, "too_many_requests")
	want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+other.ID+"/messages?q=hello", ""), 200, "")

	b := newInboxApp(t, nil)
	mine := b.fake.Add(alice, "Mine")
	release := make(chan struct{})
	var holding sync.WaitGroup
	holding.Add(httpapi.SearchesAtOnce)
	b.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == inbox.KeySearch {
			holding.Done()
			<-release
		}
		return nil
	}
	path = "/api/v1/mailboxes/" + mine.ID + "/messages?q=hello"
	done := make(chan int, httpapi.SearchesAtOnce)
	for range httpapi.SearchesAtOnce {
		go func() { done <- b.call("alice", "GET", path, "").Code }()
	}
	holding.Wait()
	b.mem.Interrupt = nil
	want(t, b.call("alice", "GET", path, ""), 429, "too_many_requests")
	close(release)
	for range httpapi.SearchesAtOnce {
		if code := <-done; code != 200 {
			t.Fatalf("%d", code)
		}
	}
	want(t, b.call("alice", "GET", path, ""), 200, "")
}

// R7: the store held under a request: the request ends at its deadline from
// its start, with an error, and writes nothing after.
func TestR7ARequestEndsAtItsDeadline(t *testing.T) {
	const deadline = 150 * time.Millisecond
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.Deadline = deadline })
	box := a.fake.Add(alice, "Mine")
	ids := []string{a.seed(box.ID, plainRaw, inbox.StoredMeta{}), a.seed(box.ID, plainRaw, inbox.StoredMeta{}), a.seed(box.ID, plainRaw, inbox.StoredMeta{})}
	var mu sync.Mutex
	var after []string
	var expired bool
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		mu.Lock()
		if expired {
			after = append(after, string(step)+" "+key)
		}
		mu.Unlock()
		if step == store.StepDeleteRaw && key == "msg#"+ids[1] {
			<-ctx.Done()
			mu.Lock()
			expired = true
			mu.Unlock()
			return ctx.Err()
		}
		return nil
	}
	start := time.Now()
	rec := a.call("alice", "POST", "/api/v1/mailboxes/"+box.ID+"/messages/delete", `{"ids":["`+ids[0]+`","`+ids[1]+`","`+ids[2]+`"]}`)
	took := time.Since(start)
	want(t, rec, 500, "internal")
	if took < deadline || took > deadline+2*time.Second {
		t.Fatalf("took %v", took)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(after) != 0 {
		t.Fatalf("after the deadline: %v", after)
	}
	a.mem.Interrupt = nil
	for _, id := range ids[1:] {
		want(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+id, ""), 200, "")
	}
	if !strings.Contains(a.logs.String(), "past the request's deadline") {
		t.Fatalf("log %s", a.logs)
	}
}

// The log of every route holds no address, subject, sender, content, token,
// or account, mailbox or message identifier: it is public.
func TestTheMailboxRoutesLogNothingOfAMessage(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, hostileRaw(), hostileMeta())
	for _, standing := range []mailboxes.Standing{{TermsAgreed: true}, {Closing: true}, {}} {
		a.fake.Accounts.Set(alice, standing)
		for _, r := range routes(box.ID, msg) {
			a.call("alice", r.method, r.path, r.body)
			a.call("bob", r.method, r.path, r.body)
		}
	}
	// A storage error that names what it was about, as an SDK's error might.
	other := a.fake.Add(bob, "Bob's")
	otherMsg := a.seed(other.ID, hostileRaw(), hostileMeta())
	realShaped := "mbx_abcdefghijklmnopqrstuvwxyz"
	a.mem.Interrupt = func(_ context.Context, _ store.Step, partition, key string) error {
		return errors.New("storage: failed on " + partition + " " + key + " " + realShaped + " " + bob)
	}
	for _, path := range []string{"/messages", "/messages?q=x", "/messages/" + otherMsg, "/messages/" + otherMsg + "/raw"} {
		want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+other.ID+path, ""), 500, "internal")
	}
	logs := a.logs.String()
	if !strings.Contains(logs, "mailboxes: internal error") {
		t.Fatalf("nothing logged: %s", logs)
	}
	for _, secret := range []string{realShaped, bob, alice, box.ID, other.ID, msg, otherMsg, hostileSender, "7f3a", "Eve", "evil", "alice", "Set-Cookie"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			t.Fatalf("not a log line: %q", line)
		}
	}
}
