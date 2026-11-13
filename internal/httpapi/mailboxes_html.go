package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
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
// for reading a message, a render slot taken, then the raw message read and
// rendered under the render's deadline.
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
	release, err := a.slots.take(r.Context(), c.Account)
	if err != nil {
		frameFail(w, a.frameStatus(err))
		return
	}
	defer release()
	held := &heldBudget{budget: a.budget, wait: a.cfg.Wait}
	defer held.release()
	ctx := inbox.WithReadBudget(r.Context(), held)
	id := r.PathValue("message")
	raw, err := a.cfg.Reader.Raw(ctx, b, id)
	if err != nil {
		frameFail(w, a.frameStatus(err))
		return
	}
	in, status, ok, err := inbox.HTMLInput(ctx, raw, b, id)
	switch {
	case err != nil:
		frameFail(w, a.frameStatus(err))
		return
	case !ok || status != "":
		frameFail(w, http.StatusNotFound)
		return
	}
	rctx, cancel := context.WithTimeout(ctx, inbox.RenderDeadline)
	defer cancel()
	var doc bytes.Buffer
	out, err := mailhtml.Render(rctx, in, &doc)
	switch {
	case err != nil:
		frameFail(w, a.frameStatus(err))
		return
	case out.Status != contracts.HTMLShown:
		frameFail(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(doc.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.Bytes()) //nolint:gosec // the sanitiser's document, served sandboxed by the frame's CSP to the frame alone
}

// link answers where a link of a message's HTML leads, for the app's link
// page: the walk of the frame's render again, and the link its token names
// when its tag still matches.
func (a *mailboxesAPI) link(w http.ResponseWriter, r *http.Request, c mailboxes.Caller) {
	b, ctx, release, ok := a.open(w, r, c, mailboxes.AccessReadMessage)
	if !ok {
		return
	}
	defer release()
	token := r.PathValue("link")
	if !contracts.MessageLinkToken(token) {
		failed(w, a.cfg.Log, contracts.ErrNotFound)
		return
	}
	free, err := a.slots.take(ctx, c.Account)
	if err != nil {
		a.failRender(w, err)
		return
	}
	defer free()
	id := r.PathValue("message")
	raw, err := a.cfg.Reader.Raw(ctx, b, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	in, status, ok, err := inbox.HTMLInput(ctx, raw, b, id)
	switch {
	case err != nil:
		a.failRender(w, err)
		return
	case !ok || status != "":
		failed(w, a.cfg.Log, contracts.ErrNotFound)
		return
	}
	rctx, cancel := context.WithTimeout(ctx, inbox.RenderDeadline)
	defer cancel()
	links, err := mailhtml.Links(rctx, in)
	switch {
	case errors.Is(err, mailhtml.ErrNotShown):
		failed(w, a.cfg.Log, contracts.ErrNotFound)
		return
	case err != nil:
		a.failRender(w, err)
		return
	}
	l, found := mailhtml.Find(links, token)
	if !found {
		writeError(w, http.StatusConflict, contracts.ErrLinkChanged.Code, contracts.ErrLinkChanged.Message)
		return
	}
	writeJSON(w, contracts.MessageLink{Link: token, URL: l.URL, Host: l.Host, Shown: l.Shown})
}

// failRender answers a render that could not run: busy (503) when no slot
// came or its deadline passed, too_many_requests for the account's share.
func (a *mailboxesAPI) failRender(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, contracts.ErrBusy), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, contracts.ErrBusy.Code, contracts.ErrBusy.Message)
	default:
		a.fail(w, err)
	}
}

// renderSlots are the renders one enclave runs at once (ADR 0026, "Limits"):
// a number running, a bounded queue that waits a bounded time, and a share
// per account, past which the account is answered too_many_requests.
type renderSlots struct {
	running, queue       int
	perRunning, perQueue int
	wait                 time.Duration

	mu       sync.Mutex
	active   int
	waiting  int
	accounts map[string]*renderShare
	changed  chan struct{}
}

type renderShare struct{ running, waiting int }

func newRenderSlots(running, queue int, wait time.Duration) *renderSlots {
	return &renderSlots{
		running: running, queue: queue, wait: wait,
		perRunning: contracts.MessageHTMLRendersPerAccount, perQueue: contracts.MessageHTMLQueuedPerAccount,
		accounts: map[string]*renderShare{}, changed: make(chan struct{}),
	}
}

// take waits for a slot for account: contracts.ErrTooManyRequests past the
// account's share, contracts.ErrBusy when the queue is full or the wait
// ends, ctx's error when it ends. The function returned gives it back once.
func (s *renderSlots) take(ctx context.Context, account string) (func(), error) {
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
	timer := time.NewTimer(s.wait)
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

// accountSlots are the render slots as one request's account takes them,
// for the reader (inbox.RenderSlots).
type accountSlots struct {
	slots   *renderSlots
	account string
}

func (s accountSlots) Take(ctx context.Context) (func(), error) { return s.slots.take(ctx, s.account) }
