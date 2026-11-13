package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// MailboxesService is what the person's routes under /api/v1/mailboxes ask of
// the service (ADR 0026, "The API"); *mailboxes.Service implements it (pull
// request 1), and pull request 2 writes the routes on it and on an
// inbox.Reader. The adapter authenticates the session, fills a
// mailboxes.Caller from it and decides nothing else: who owns a mailbox,
// what its state allows and what the account's standing allows are decided
// here, by the service.
//
// Every method checks in this order and answers the first failure
// (ADR 0026, "The API"), so a stranger learns nothing a missing mailbox
// would not tell them:
//
//  1. contracts.ErrUnauthorized: the account is blocked or gone.
//  2. contracts.ErrNotFound: the mailbox is not the caller's account's, was
//     never made, or is leaving (disconnecting, cancelled, gone), except
//     for Disconnect, below.
//  3. The account's standing: contracts.ErrAccountClosing, then
//     contracts.ErrTermsChanged (mailboxes.Access).
//  4. contracts.ErrConfirmationNeeded (Disconnect only).
//  5. The mailbox's record: contracts.ErrMailboxUnreadable (lost or
//     forked), contracts.ErrMailboxForked (Disconnect of a forked one).
//  6. The request's own input: contracts.ErrInvalidLabel,
//     contracts.ErrInvalidRetention.
//  7. Limits: contracts.ErrTooManyRequests, then
//     contracts.ErrTooManyMailboxes.
//
// mailboxestest.Contract runs these cases against mailboxestest.Fake and,
// from pull request 1, against *mailboxes.Service.
type MailboxesService interface {
	// List is the account's mailboxes, the oldest first, without the ones
	// being disconnected; one whose record cannot be read is listed with
	// state "unreadable" so it can be disconnected. Allowed in every standing.
	// Reading the list finishes what a disconnect or a creation left undone
	// (ADR 0026, "State machines").
	List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error)
	// Create makes a mailbox. contracts.ErrAccountClosing while the account
	// is closing, contracts.ErrTermsChanged while the terms in force are not
	// agreed, contracts.ErrTooManyMailboxes at mailboxes.MaxMailboxes,
	// contracts.ErrTooManyRequests past mailboxes.MaxMadePerPeriod,
	// contracts.ErrInvalidLabel.
	Create(ctx context.Context, c mailboxes.Caller, in contracts.MailboxCreate) (*contracts.Mailbox, error)
	// Get is one mailbox. Allowed in every standing.
	Get(ctx context.Context, c mailboxes.Caller, id string) (*contracts.Mailbox, error)
	// Update changes the label or the retention: contracts.ErrAccountClosing,
	// contracts.ErrTermsChanged, contracts.ErrInvalidLabel,
	// contracts.ErrInvalidRetention, contracts.ErrMailboxUnreadable.
	Update(ctx context.Context, c mailboxes.Caller, id string, in contracts.MailboxUpdate) (*contracts.Mailbox, error)
	// CheckChange answers, and changes nothing, what Create (id "") or
	// Update (id the mailbox's) would answer before it reads its input:
	// contracts.ErrUnauthorized; for Update contracts.ErrNotFound; the
	// standing (contracts.ErrAccountClosing, then contracts.ErrTermsChanged);
	// for Update contracts.ErrMailboxUnreadable; nil when the change may go
	// on to its input. The adapter asks it before it reads a request's body,
	// so a body that is not one is answered after all of these.
	CheckChange(ctx context.Context, c mailboxes.Caller, id string) error
	// Disconnect stops the mailbox and deletes its mail (ADR 0026,
	// "Disconnecting"). Allowed in every standing (leaving never needs the
	// terms); contracts.ErrConfirmationNeeded once c.ConfirmedUntil has
	// passed. It always ends in a terminal commit of the mailbox's record
	// before the slot is freed and before it answers: a readable record goes
	// through disconnecting; a lost one is removed directly. A mailbox of the
	// caller's that is disconnected already, by this request or another, is
	// nil, the same answer, until the sweep's last pass has removed it
	// (mailboxes.SweepGrace after the disconnect, or later): from then it
	// cannot be told from a mailbox never made, and is contracts.ErrNotFound
	// to everyone, its owner included. No permanent receipt is kept. One
	// whose record is forked takes no commit: contracts.ErrMailboxForked,
	// and it stays listed and counted. One that is not the caller's, or
	// never existed, is contracts.ErrNotFound, the same for both.
	Disconnect(ctx context.Context, c mailboxes.Caller, id string) error
	// Open checks that the mailbox is the caller's, that its record can be
	// read and that the account's standing allows a, and returns it opened
	// for one request (mailboxes.Access says which standing allows what):
	// contracts.ErrAccountClosing, contracts.ErrTermsChanged,
	// contracts.ErrMailboxUnreadable. A paused mailbox opens: pausing stops
	// new mail only.
	Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error)
}

// What the person's routes hold the enclave to (ADR 0026, "Admission" and
// "Limits"); each enclave its own.
const (
	// MailReadBudget is how many bytes of mail are open for reading at once:
	// a raw message's bound is taken from it before the message is read
	// from the bucket, and held until the response is written.
	MailReadBudget = 128 << 20
	// AdmissionWait is how long a request waits for room in a budget before
	// it is answered contracts.ErrTooManyRequests.
	AdmissionWait = 10 * time.Second
	// SearchesAtOnce is how many search pages run at once.
	SearchesAtOnce = 4
	// SearchPagesPerMinute is how many search pages one account asks for in
	// a minute.
	SearchPagesPerMinute = 30
	// MaxQueryRunes bounds a search.
	MaxQueryRunes = 200
	// DefaultMessagesLimit is the page size without a limit; MaxMessagesLimit
	// the largest one.
	DefaultMessagesLimit = 50
	MaxMessagesLimit     = 100
	// maxDeleteBody bounds the body of POST .../messages/delete: a hundred
	// identifiers of 32 characters and some room.
	maxDeleteBody = 8 << 10
)

// MailboxesConfig is what the person's mailbox routes are made of. The zero
// values of the limits are the ones above; tests set smaller ones.
type MailboxesConfig struct {
	Service  MailboxesService
	Reader   inbox.Reader
	Sessions Sessions
	Log      *slog.Logger
	// Deadline bounds every request from its start (mailboxes.RequestDeadline).
	Deadline time.Duration
	// ReadBudget is MailReadBudget; Wait is AdmissionWait.
	ReadBudget int64
	Wait       time.Duration
	// Now is the clock of the search pages' rate (time.Now).
	Now func() time.Time
	// AppOrigin is the app's origin, the only one that may frame a
	// message's HTML (frame-ancestors): https://app.<domain>, with its port
	// when it is not 443. Without one, the request's own host.
	AppOrigin string
	// Renders, RenderQueue and RenderWait are the render slots
	// (contracts.MessageHTMLRenders, MessageHTMLRenderQueue,
	// MessageHTMLRenderWait); tests set smaller ones.
	Renders     int
	RenderQueue int
	RenderWait  time.Duration
}

// MailboxesAPI returns every route of the person's table under
// /api/v1/mailboxes (ADR 0026, "The API"), on the app's host, answering only
// to the app (the session cookie, same origin for changes). Each request runs
// under a context that ends Deadline after it started, set before anything
// else and given to the service, the reader and the response. A mailbox is
// opened (its owner, its record and the account's standing checked) before
// anything of its mail is read. Every answer is JSON or, for the raw message
// and attachments, a download that no browser renders; nothing of a message
// is in an error or a log.
func MailboxesAPI(cfg MailboxesConfig) http.Handler {
	if cfg.Deadline <= 0 {
		cfg.Deadline = mailboxes.RequestDeadline
	}
	if cfg.ReadBudget <= 0 {
		cfg.ReadBudget = MailReadBudget
	}
	if cfg.Wait <= 0 {
		cfg.Wait = AdmissionWait
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Renders <= 0 {
		cfg.Renders = contracts.MessageHTMLRenders
	}
	if cfg.RenderQueue <= 0 {
		cfg.RenderQueue = contracts.MessageHTMLRenderQueue
	}
	if cfg.RenderWait <= 0 {
		cfg.RenderWait = contracts.MessageHTMLRenderWait
	}
	a := &mailboxesAPI{
		cfg: cfg, budget: newByteBudget(cfg.ReadBudget), searches: make(chan struct{}, SearchesAtOnce),
		pages: map[string][]time.Time{}, slots: newRenderSlots(cfg.Renders, cfg.RenderQueue),
	}
	mux := http.NewServeMux()
	const base = "/api/v1/mailboxes"
	in := func(next func(http.ResponseWriter, *http.Request, mailboxes.Caller)) http.HandlerFunc {
		return withSession(cfg.Sessions, cfg.Log, func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
			next(w, r, mailboxes.Caller{Account: who.AccountID, ConfirmedUntil: who.Session.ConfirmedUntil})
		})
	}
	mux.HandleFunc("GET "+base, in(a.list))
	mux.HandleFunc("POST "+base, in(a.create))
	mux.HandleFunc(base, refuse("GET, POST"))
	mux.HandleFunc("GET "+base+"/{id}", in(a.get))
	mux.HandleFunc("PATCH "+base+"/{id}", in(a.update))
	mux.HandleFunc("DELETE "+base+"/{id}", in(a.disconnect))
	mux.HandleFunc(base+"/{id}", refuse("GET, PATCH, DELETE"))
	mux.HandleFunc("GET "+base+"/{id}/messages", in(a.messages))
	mux.HandleFunc(base+"/{id}/messages", refuse(http.MethodGet))
	mux.HandleFunc("POST "+base+"/{id}/messages/delete", in(a.deleteMany))
	mux.HandleFunc("GET "+base+"/{id}/messages/{message}", in(a.message))
	mux.HandleFunc("DELETE "+base+"/{id}/messages/{message}", in(a.deleteOne))
	mux.HandleFunc(base+"/{id}/messages/{message}", refuse("GET, DELETE"))
	mux.HandleFunc("GET "+base+"/{id}/messages/{message}/raw", in(a.raw))
	mux.HandleFunc(base+"/{id}/messages/{message}/raw", refuse(http.MethodGet))
	mux.HandleFunc("GET "+base+"/{id}/messages/{message}/attachments/{index}", in(a.attachment))
	mux.HandleFunc(base+"/{id}/messages/{message}/attachments/{index}", refuse(http.MethodGet))
	mux.HandleFunc("GET "+base+"/{id}/messages/{message}/html", a.frame)            // mailboxes_html.go
	mux.HandleFunc("GET "+base+"/{id}/messages/{message}/links/{link}", in(a.link)) // mailboxes_html.go
	mux.HandleFunc(base+"/", noSuchEndpoint)
	app := fromTheApp(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The deadline runs from the request's start, before anything else.
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Deadline)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// A path that is not clean names nothing here: ServeMux would answer
		// it with a redirect whose body is HTML, and this API answers JSON.
		if p := r.URL.Path; p != cleanPath(p) {
			noSuchEndpoint(w, r)
			return
		}
		app.ServeHTTP(w, r.WithContext(ctx))
	})
}

// cleanPath is p as ServeMux would clean it, its trailing slash kept.
func cleanPath(p string) string {
	c := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

type mailboxesAPI struct {
	cfg      MailboxesConfig
	budget   *byteBudget
	searches chan struct{}
	slots    *renderSlots

	mu    sync.Mutex
	pages map[string][]time.Time // search pages asked in the last minute, by account
}

// fail answers an error of the service or the reader. One a caller can act
// on goes out with its code and fixed words; any other is logged without
// identifiers and answered with nothing of its own.
func (a *mailboxesAPI) fail(w http.ResponseWriter, err error) {
	var known *contracts.Error
	if errors.As(err, &known) {
		failed(w, a.cfg.Log, known)
		return
	}
	var msg string
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		msg = "past the request's deadline"
	} else {
		msg = domain.Redacted(err)
	}
	a.cfg.Log.Error("mailboxes: internal error", "err", msg)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *mailboxesAPI) list(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	out, err := a.cfg.Service.List(r.Context(), c)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *mailboxesAPI) create(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	// Who the caller is and what their standing allows come before the
	// body (ADR 0026, "The API").
	if err := a.cfg.Service.CheckChange(r.Context(), c, ""); err != nil {
		a.fail(w, err)
		return
	}
	var in contracts.MailboxCreate
	if !decode(w, r, &in) {
		return
	}
	out, err := a.cfg.Service.Create(r.Context(), c, in)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}

func (a *mailboxesAPI) get(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	out, err := a.cfg.Service.Get(r.Context(), c, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *mailboxesAPI) update(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	// A body that is not one is the request's own input, answered after
	// what comes first (ADR 0026, "The API"): a mailbox that is not the
	// caller's is not_found, and the standing and the record are answered,
	// whatever the body.
	if err := a.cfg.Service.CheckChange(r.Context(), c, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	var in contracts.MailboxUpdate
	if !decode(w, r, &in) {
		return
	}
	out, err := a.cfg.Service.Update(r.Context(), c, r.PathValue("id"), in)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *mailboxesAPI) disconnect(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	if err := a.cfg.Service.Disconnect(r.Context(), c, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// open opens the request's mailbox for what it does, and gives the request
// its share of the read budget, released by the returned function once the
// response is written. Nothing of the mailbox's mail is read before it.
func (a *mailboxesAPI) open(w http.ResponseWriter, r *http.Request, c mailboxes.Caller, access mailboxes.Access) (inbox.Box, context.Context, func(), bool) {
	b, err := a.cfg.Service.Open(r.Context(), c, r.PathValue("id"), access)
	if err != nil {
		a.fail(w, err)
		return inbox.Box{}, nil, nil, false
	}
	held := &heldBudget{budget: a.budget, wait: a.cfg.Wait}
	ctx := inbox.WithRenderAdmission(inbox.WithReadBudget(r.Context(), held), summaryAdmission{a, c.Account})
	return b, ctx, held.release, true
}

func (a *mailboxesAPI) messages(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	query := r.URL.Query()
	_, searching := query["q"]
	access := mailboxes.AccessListMessages
	if searching {
		access = mailboxes.AccessSearch
	}
	b, ctx, release, ok := a.open(w, r, c, access)
	if !ok {
		return
	}
	defer release()
	limit := DefaultMessagesLimit
	if s, ok := query["limit"]; ok {
		n, err := strconv.Atoi(s[0])
		if err != nil || n < 1 || n > MaxMessagesLimit || len(s) > 1 {
			failed(w, a.cfg.Log, contracts.ErrInvalidLimit)
			return
		}
		limit = n
	}
	after := query.Get("after")
	if _, ok := query["after"]; ok && !inbox.IsMessageID(after) {
		failed(w, a.cfg.Log, contracts.ErrInvalidCursor)
		return
	}
	if !searching {
		page, err := a.cfg.Reader.List(ctx, b, after, limit)
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, page)
		return
	}
	q := query.Get("q")
	if n := utf8.RuneCountInString(q); n < 1 || n > MaxQueryRunes || !utf8.ValidString(q) || strings.TrimSpace(q) == "" || len(query["q"]) > 1 {
		failed(w, a.cfg.Log, contracts.ErrInvalidQuery)
		return
	}
	if !a.searchAllowed(c.Account) {
		failed(w, a.cfg.Log, contracts.ErrTooManyRequests)
		return
	}
	select {
	case a.searches <- struct{}{}:
	case <-time.After(a.cfg.Wait):
		failed(w, a.cfg.Log, contracts.ErrTooManyRequests)
		return
	case <-ctx.Done():
		a.fail(w, ctx.Err())
		return
	}
	defer func() { <-a.searches }()
	page, err := a.cfg.Reader.Search(ctx, b, q, after, limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, page)
}

// searchAllowed counts a search page of an account, and says whether it is
// within SearchPagesPerMinute.
func (a *mailboxesAPI) searchAllowed(account string) bool {
	now := a.cfg.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pages) > 10_000 {
		for k, v := range a.pages {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= time.Minute {
				delete(a.pages, k)
			}
		}
	}
	recent := a.pages[account][:0:0]
	for _, t := range a.pages[account] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= SearchPagesPerMinute {
		a.pages[account] = recent
		return false
	}
	a.pages[account] = append(recent, now)
	return true
}

func (a *mailboxesAPI) message(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessReadMessage)
	if !ok {
		return
	}
	defer release()
	m, err := a.cfg.Reader.Get(ctx, b, r.PathValue("message"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, m)
}

// download answers a file that a browser saves and never shows: a type no
// browser renders, as an attachment, sandboxed should one show it anyway.
func download(w http.ResponseWriter, contentType, name string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", contentDisposition(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data) //nolint:gosec // a download: octet-stream or message/rfc822, attachment, sandboxed, never rendered
}

func (a *mailboxesAPI) raw(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessDownload)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("message")
	data, err := a.cfg.Reader.Raw(ctx, b, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	download(w, "message/rfc822", id+".eml", data) // the reader read it, so id is 32 hexadecimal characters
}

func (a *mailboxesAPI) attachment(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessDownload)
	if !ok {
		return
	}
	defer release()
	s := r.PathValue("index")
	index, err := strconv.Atoi(s)
	if err != nil || index < 0 || index >= inbox.MaxAttachmentsListed || strconv.Itoa(index) != s {
		failed(w, a.cfg.Log, contracts.ErrNotFound)
		return
	}
	name, data, err := a.cfg.Reader.Attachment(ctx, b, r.PathValue("message"), index)
	if err != nil {
		a.fail(w, err)
		return
	}
	download(w, "application/octet-stream", domain.AttachmentFilename(name, index), data)
}

func (a *mailboxesAPI) deleteOne(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessDeleteMessages)
	if !ok {
		return
	}
	defer release()
	n, err := a.cfg.Reader.Delete(ctx, b, []string{r.PathValue("message")})
	if err != nil {
		a.fail(w, err)
		return
	}
	if n == 0 {
		failed(w, a.cfg.Log, contracts.ErrNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *mailboxesAPI) deleteMany(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessDeleteMessages)
	if !ok {
		return
	}
	defer release()
	var in contracts.MessagesDelete
	if !decodeUpTo(w, r, &in, maxDeleteBody) {
		return
	}
	if len(in.IDs) > inbox.MaxDelete {
		failed(w, a.cfg.Log, contracts.ErrTooManyMessages)
		return
	}
	n, err := a.cfg.Reader.Delete(ctx, b, in.IDs)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, contracts.MessagesDeleted{Deleted: n})
}

// contentDisposition is an attachment's header: an ASCII filename, in which
// every other character is "_", for old browsers, and filename* with the
// name in UTF-8, percent-encoded (RFC 6266, RFC 8187). Neither can end the
// header or start another: name holds no control character, and both are
// written from a fixed set of bytes.
func contentDisposition(name string) string {
	var ascii, enc strings.Builder
	for _, r := range name {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' || r == '%' || r == ';' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	const attrChar = "!#$&+-.^_`|~"
	for _, c := range []byte(name) {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attrChar, c) >= 0) {
			enc.WriteByte(c)
		} else {
			enc.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)>>4, 16)+strconv.FormatUint(uint64(c)&15, 16)))
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + enc.String()
}

// byteBudget is a count of bytes taken and given back, shared by the
// requests of one enclave.
type byteBudget struct {
	mu      sync.Mutex
	total   int64
	free    int64
	changed chan struct{} // closed, and replaced, whenever bytes are given back
}

func newByteBudget(total int64) *byteBudget {
	return &byteBudget{total: total, free: total, changed: make(chan struct{})}
}

// take takes n bytes (the whole budget at most), waiting until wait has
// passed: contracts.ErrTooManyRequests then, or ctx's error when it ends.
func (b *byteBudget) take(ctx context.Context, n int64, wait time.Duration) (int64, error) {
	n = min(max(n, 0), b.total)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		if b.free >= n {
			b.free -= n
			b.mu.Unlock()
			return n, nil
		}
		ch := b.changed
		b.mu.Unlock()
		select {
		case <-ch:
		case <-timer.C:
			return 0, contracts.ErrTooManyRequests
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (b *byteBudget) give(n int64) {
	if n == 0 {
		return
	}
	b.mu.Lock()
	b.free += n
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// heldBudget is what one request took from the budget, given back once.
type heldBudget struct {
	budget *byteBudget
	wait   time.Duration
	mu     sync.Mutex
	held   int64
}

// Take implements inbox.ReadBudget.
func (h *heldBudget) Take(ctx context.Context, n int64) error {
	got, err := h.budget.take(ctx, n, h.wait)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.held += got
	h.mu.Unlock()
	return nil
}

func (h *heldBudget) release() {
	h.mu.Lock()
	n := h.held
	h.held = 0
	h.mu.Unlock()
	h.budget.give(n)
}
