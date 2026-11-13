package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/mailhtml"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// A message's HTML (ADR 0026, "Showing a message's HTML", pull request 5):
// the frame's document, GET .../messages/{message}/html, the only route of
// the origin that answers content from a message as a document, with the
// frame's own headers on every answer; a link's destination, GET
// .../messages/{message}/links/{link}; and the render slots every render
// takes. The owner is checked before anything is read, the HTML is rendered
// on every request, and nothing of it is stored, cached or logged.

// FrameCSP is the frame's Content-Security-Policy for an app at appOrigin:
// the same sandbox as the iframe's attribute, nothing fetched but data:
// images, no form, no base, framed only by the app.
func FrameCSP(appOrigin string) string {
	return "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; " +
		"img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors " + appOrigin
}

// SetFrameHeaders sets the frame's response headers, replacing the router's
// policy: every answer of the frame's route carries them, errors included.
func SetFrameHeaders(h http.Header, appOrigin string) {
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", FrameCSP(appOrigin))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-DNS-Prefetch-Control", "off")
}

// frameRefusal is the status of a request for the frame's path (a
// canonical path, isFramePath) refused before the frame's handler: another
// site (403), a trailing slash, which names no route (404), and a method
// other than GET or HEAD (405); 0 when none refuses it.
func frameRefusal(r *http.Request) int {
	switch {
	case saysCrossSite(r):
		return http.StatusForbidden
	case strings.HasSuffix(r.URL.Path, "/"):
		return http.StatusNotFound
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		return http.StatusMethodNotAllowed
	}
	return 0
}

// answerFrameRefusal answers a refused request for the frame's path: the
// frame's headers and its fixed body.
func answerFrameRefusal(w http.ResponseWriter, status int, appOrigin string) {
	SetFrameHeaders(w.Header(), appOrigin)
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, HEAD")
	}
	frameFail(w, status)
}

// framePathGuard answers what every answer of the frame's path shares
// (ADR 0026, "The frame's path: every answer"), inside the API, whose own
// handler answers what it lets through: for a request for the frame's path
// it sets the frame's headers before anything can refuse it, so every
// answer after is the frame's, and answers the refusals of frameRefusal
// itself. It says whether it answered.
func framePathGuard(w http.ResponseWriter, r *http.Request, appOrigin string) bool {
	if !isFramePath(r.URL.Path) {
		return false
	}
	SetFrameHeaders(w.Header(), appOrigin)
	if status := frameRefusal(r); status != 0 {
		answerFrameRefusal(w, status, appOrigin)
		return true
	}
	return false
}

// The fixed bodies of the frame's route when it shows no message: never
// JSON, never text from the message. One for a request whose session did
// not come with it (the viewer's frame says to read the text), one for the
// rest.
const (
	frameNotShown = "<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Not shown</title></head>" +
		"<body style=\"font:15px/1.5 system-ui,sans-serif;color:#333;margin:16px\"><p>This message can't be shown here.</p></body></html>"
	frameNoSession = "<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Not shown</title></head>" +
		"<body style=\"font:15px/1.5 system-ui,sans-serif;color:#333;margin:16px\"><p>This message can't be shown formatted here. Choose Text above to read it.</p></body></html>"
)

// fromAFrame says whether a request may be the app's frame: a request with
// any Sec-Fetch-* header must say Sec-Fetch-Dest: iframe and
// Sec-Fetch-Site: same-origin; one with none (a browser that sends no Fetch
// Metadata, a proxy that strips it) is served, and the CSP sandbox holds.
func fromAFrame(r *http.Request) bool {
	h := r.Header
	sent := false
	for _, k := range []string{"Sec-Fetch-Dest", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-User"} {
		if _, ok := h[k]; ok {
			sent = true
		}
	}
	return !sent || h.Get("Sec-Fetch-Dest") == "iframe" && h.Get("Sec-Fetch-Site") == "same-origin"
}

func (a *mailboxesAPI) appOrigin(r *http.Request) string {
	if a.cfg.AppOrigin != "" {
		return a.cfg.AppOrigin
	}
	return "https://" + r.Host
}

// frameFail answers the frame's route with a status and its fixed body.
func frameFail(w http.ResponseWriter, status int) {
	body := frameNotShown
	if status == http.StatusUnauthorized {
		body = frameNoSession
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// frameStatus is the status the frame's route answers for an error: what a
// caller may not see is 404, as what does not exist.
func (a *mailboxesAPI) frameStatus(err error) int {
	switch {
	case errors.Is(err, contracts.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, contracts.ErrTooManyRequests):
		return http.StatusTooManyRequests
	case errors.Is(err, contracts.ErrBusy):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable
	}
	var known *contracts.Error
	if errors.As(err, &known) {
		return http.StatusNotFound
	}
	a.cfg.Log.Error("mailboxes: internal error", "err", domain.Redacted(err))
	return http.StatusInternalServerError
}

// frame answers the frame's document: the session's, the mailbox opened as
// for reading a message, then the render (renderFrame), and only once its
// slot is given back, the answer it made.
func (a *mailboxesAPI) frame(w http.ResponseWriter, r *http.Request) {
	SetFrameHeaders(w.Header(), a.appOrigin(r))
	if !fromAFrame(r) {
		frameFail(w, http.StatusNotFound)
		return
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		frameFail(w, http.StatusUnauthorized)
		return
	}
	who, err := a.cfg.Sessions.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		frameFail(w, a.frameStatus(err))
		return
	}
	c := mailboxes.Caller{Account: who.AccountID, ConfirmedUntil: who.Session.ConfirmedUntil}
	b, err := a.cfg.Service.Open(r.Context(), c, r.PathValue("id"), mailboxes.AccessReadMessage)
	if err != nil {
		frameFail(w, a.frameStatus(err))
		return
	}
	held := &heldBudget{budget: a.budget, wait: a.cfg.Wait}
	defer held.release()
	res := a.renderFrame(r.Context(), c.Account, b, r.PathValue("message"), held)
	if res.status != http.StatusOK {
		frameFail(w, res.status)
		return
	}
	defer res.done()
	w.Header().Set("Content-Length", strconv.Itoa(len(res.doc)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.doc) //nolint:gosec // the sanitiser's document, served sandboxed by the frame's CSP to the frame alone
}

// frameResult is what the frame route's render gives its answer: the
// document, charged to the read budget and to the account's share of
// documents waiting (done gives the share back; the budget goes back with
// the request's held bytes), or the status of a refusal, which holds
// nothing.
type frameResult struct {
	status int
	doc    []byte
	done   func()
}

// renderFrame is the first of the frame route's two phases (ADR 0026,
// "Admission of a render"): under a render slot and the render's deadline,
// it reads the raw message, renders the document into memory and charges
// it to the read budget in place of the raw message (waiting for room
// within the deadline and the admission's wait); a document that cannot be
// charged is discarded. It gives the slot back before it returns, whatever
// it returns, so no answer is ever written while a slot is held and no
// finished document exists that is not charged: one under its slot, or one
// charged.
func (a *mailboxesAPI) renderFrame(ctx context.Context, account string, b inbox.Box, id string, held *heldBudget) frameResult {
	rctx, release, err := a.admit(inbox.WithReadBudget(ctx, held), account, a.cfg.RenderWait)
	if err != nil {
		return frameResult{status: a.frameStatus(err)}
	}
	defer release()
	refuse := func(status int) frameResult {
		held.release()
		return frameResult{status: status}
	}
	raw, err := a.cfg.Reader.Raw(rctx, b, id)
	if err != nil {
		return refuse(a.frameStatus(err))
	}
	in, status, ok, err := inbox.HTMLInput(rctx, raw, b, id)
	switch {
	case err != nil:
		return refuse(a.frameStatus(err))
	case !ok || status != "":
		return refuse(http.StatusNotFound)
	}
	var doc bytes.Buffer
	out, err := mailhtml.Render(rctx, in, &doc)
	switch {
	case err != nil:
		return refuse(a.frameStatus(err))
	case out.Status != contracts.HTMLShown:
		return refuse(http.StatusNotFound)
	}
	// The document (at most contracts.MessageHTMLMaxMarkupBytes and the
	// images' base64, under 11 MiB) waits for its reader held against the
	// read budget in place of the raw message, and within the account's
	// share, both taken while the slot still covers it.
	done, err := a.waitingDocument(rctx, account, held, int64(doc.Len()))
	if err != nil {
		return refuse(a.frameStatus(err))
	}
	return frameResult{status: http.StatusOK, doc: doc.Bytes(), done: done}
}

// waitingDocument admits a finished document of account to wait for its
// reader: within the account's share (DocumentsWaitingPerAccount, else
// contracts.ErrTooManyRequests) and held against the read budget in place
// of what the request held for its raw message (waiting for room as a raw
// read does, contracts.ErrTooManyRequests past it). done gives the share
// back; the budget goes back with the request's held bytes.
func (a *mailboxesAPI) waitingDocument(ctx context.Context, account string, held *heldBudget, n int64) (func(), error) {
	a.mu.Lock()
	if a.waiting[account] >= DocumentsWaitingPerAccount {
		a.mu.Unlock()
		return nil, contracts.ErrTooManyRequests
	}
	a.waiting[account]++
	a.mu.Unlock()
	done := func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.waiting[account]--; a.waiting[account] <= 0 {
			delete(a.waiting, account)
		}
	}
	if err := held.Resize(ctx, n); err != nil {
		done()
		return nil, err
	}
	return done, nil
}

// link answers where a link of a message's HTML leads, for the app's link
// page: the walk of the frame's render again (findLink), and the link its
// token names when its tag still matches, answered once the render's slot
// is given back.
func (a *mailboxesAPI) link(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessReadMessage)
	if !ok {
		return
	}
	defer release()
	token := r.PathValue("link")
	if !contracts.MessageLinkToken(token) {
		failed(w, r, a.cfg.Log, contracts.ErrNotFound)
		return
	}
	l, err := a.findLink(ctx, c.Account, b, r.PathValue("message"), token)
	switch {
	case errors.Is(err, contracts.ErrLinkChanged):
		writeError(w, http.StatusConflict, contracts.ErrLinkChanged.Code, contracts.ErrLinkChanged.Message)
	case err != nil:
		a.failRender(w, r, err)
	default:
		writeJSON(w, contracts.MessageLink{Link: token, URL: l.URL, Host: l.Host, Shown: l.Shown})
	}
}

// findLink is the first of the link route's two phases: under a render
// slot and the render's deadline, the walk of the message's HTML and the
// link token names; contracts.ErrNotFound when the message has no HTML
// shown, contracts.ErrLinkChanged when the token's tag no longer matches.
// It gives the slot back before it returns, whatever it returns.
func (a *mailboxesAPI) findLink(ctx context.Context, account string, b inbox.Box, id, token string) (mailhtml.Link, error) {
	ctx, free, err := a.admit(ctx, account, a.cfg.RenderWait)
	if err != nil {
		return mailhtml.Link{}, err
	}
	defer free()
	raw, err := a.cfg.Reader.Raw(ctx, b, id)
	if err != nil {
		return mailhtml.Link{}, err
	}
	in, status, ok, err := inbox.HTMLInput(ctx, raw, b, id)
	switch {
	case err != nil:
		return mailhtml.Link{}, err
	case !ok || status != "":
		return mailhtml.Link{}, contracts.ErrNotFound
	}
	links, err := mailhtml.Links(ctx, in)
	switch {
	case errors.Is(err, mailhtml.ErrNotShown):
		return mailhtml.Link{}, contracts.ErrNotFound
	case err != nil:
		return mailhtml.Link{}, err
	}
	l, found := mailhtml.Find(links, token)
	if !found {
		return mailhtml.Link{}, contracts.ErrLinkChanged
	}
	return l, nil
}

// failRender answers a render that could not run: busy (503) when no slot
// came or its deadline passed, too_many_requests for the account's share.
func (a *mailboxesAPI) failRender(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, contracts.ErrBusy), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, contracts.ErrBusy.Code, contracts.ErrBusy.Message)
	default:
		a.fail(w, r, err)
	}
}

// renderSlots are the renders one enclave runs at once (ADR 0026, "Limits"):
// a number running, a bounded queue that waits a bounded time, and a share
// per account, past which the account is answered too_many_requests.
type renderSlots struct {
	running, queue       int
	perRunning, perQueue int

	mu       sync.Mutex
	active   int
	waiting  int
	accounts map[string]*renderShare
	changed  chan struct{}
}

type renderShare struct{ running, waiting int }

func newRenderSlots(running, queue int) *renderSlots {
	return &renderSlots{
		running: running, queue: queue,
		perRunning: contracts.MessageHTMLRendersPerAccount, perQueue: contracts.MessageHTMLQueuedPerAccount,
		accounts: map[string]*renderShare{}, changed: make(chan struct{}),
	}
}

// take waits for a slot for account, at most wait:
// contracts.ErrTooManyRequests past the account's share, contracts.ErrBusy
// when the queue is full or the wait ends, ctx's error when it ends. The function returned gives it back once.
func (s *renderSlots) take(ctx context.Context, account string, wait time.Duration) (func(), error) {
	s.mu.Lock()
	sh := s.accounts[account]
	if sh == nil {
		sh = &renderShare{}
		s.accounts[account] = sh
	}
	if s.active < s.running && sh.running < s.perRunning {
		return s.granted(sh, account), nil
	}
	// The account's share of the queue, whatever it has running: two
	// waiting are refused a third even when none of its renders runs.
	if sh.waiting >= s.perQueue {
		s.forget(sh, account)
		s.mu.Unlock()
		return nil, contracts.ErrTooManyRequests
	}
	if s.waiting >= s.queue {
		s.forget(sh, account)
		s.mu.Unlock()
		return nil, contracts.ErrBusy
	}
	s.waiting++
	sh.waiting++
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ch := s.changed
		s.mu.Unlock()
		var err error
		select {
		case <-ch:
		case <-timer.C:
			err = contracts.ErrBusy
		case <-ctx.Done():
			err = ctx.Err()
		}
		s.mu.Lock()
		if err == nil && s.active < s.running && sh.running < s.perRunning {
			s.waiting--
			sh.waiting--
			return s.granted(sh, account), nil
		}
		if err != nil {
			s.waiting--
			sh.waiting--
			s.forget(sh, account)
			s.mu.Unlock()
			return nil, err
		}
	}
}

// granted takes a slot for sh, with s.mu held, and unlocks it.
func (s *renderSlots) granted(sh *renderShare, account string) func() {
	s.active++
	sh.running++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			sh.running--
			s.forget(sh, account)
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
		})
	}
}

// forget drops an account's share once it holds nothing, with s.mu held.
func (s *renderSlots) forget(sh *renderShare, account string) {
	if sh.running == 0 && sh.waiting == 0 {
		delete(s.accounts, account)
	}
}

// admit is the one admission of a render, for the frame's route, the link
// route and the summary GET message answers (ADR 0026, "Admission of a
// render"): it runs after the mailbox is opened (its owner, its record and
// the account's standing checked) and before anything of the message is
// read. It takes a render slot of the enclave and of the account, waiting
// at most wait, and starts the render's deadline there: the context it
// returns ends inbox.RenderDeadline after the slot was taken, or with the
// request (whose own deadline stays the outer bound), and the raw read, the
// MIME walk, the charset and the render all run under it. The release ends
// it and gives the slot back; defer it once.
func (a *mailboxesAPI) admit(ctx context.Context, account string, wait time.Duration) (context.Context, func(), error) {
	free, err := a.slots.take(ctx, account, wait)
	if err != nil {
		return nil, nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, inbox.RenderDeadline)
	return rctx, func() { cancel(); free() }, nil
}

// DocumentsWaitingPerAccount is how many finished documents of the frame's
// route one account may have waiting for their readers at once: a render
// slot covers the render, not the transfer, so what bounds documents in
// flight is the read budget they are held against, and this share.
const DocumentsWaitingPerAccount = 2

// SummaryWait is the longest GET message waits for a render slot for its
// HTML summary: past it, html is unavailable and the text is answered. The
// text view never waits behind renders longer than this and one render's
// deadline (ADR 0026, "Admission of a render").
const SummaryWait = time.Second

// summaryAdmission admits the summary of a message's HTML for the reader
// (inbox.RenderAdmission), as the request's account.
type summaryAdmission struct {
	api     *mailboxesAPI
	account string
}

func (s summaryAdmission) Admit(ctx context.Context) (context.Context, func(), error) {
	return s.api.admit(ctx, s.account, min(SummaryWait, s.api.cfg.RenderWait))
}
