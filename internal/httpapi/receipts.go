package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The person's receipts (ADR 0037, "What a person can verify"; pull
// request 3): the head of their account's chain and the walk's pages with
// what was found of each link and receipt, and the export's pages, every
// document as its bytes as stored, which permaily-verify -receipts checks
// offline. They are an adapter of receipts.Chain: what is found, and what
// is recorded as an incident before it is shown, is the service's. Nothing
// of a receipt, a link, an account or a digest is logged.

// ReceiptsConfig is what the person's receipts routes are made of.
type ReceiptsConfig struct {
	// Chain is the receipts service (receiptstest.Fake in tests).
	Chain    receipts.Chain
	Sessions Sessions
	Log      *slog.Logger
	// Deadline bounds every request from its start
	// (mailboxes.RequestDeadline).
	Deadline time.Duration
}

const (
	// DefaultReceiptsLimit is how many receipts a page of the walk holds
	// without a limit, and MaxReceiptsLimit the most one asks
	// (contracts.ErrInvalidLimit).
	DefaultReceiptsLimit = 50
	MaxReceiptsLimit     = 100
	// continuationTries bounds how often a request asks again for a page
	// that begins at a link whose commit expired (receipts.ErrContinuation):
	// each try reads up to a page's bounds further down, so this many reach
	// a few thousand links above it.
	continuationTries = 16
)

// ReceiptsAPI returns the person's receipts routes, on the app's host and
// answering only to the app (the session cookie), behind AppHost's
// cross-site rule as every route under /api/ is:
//
//	GET /api/v1/receipts?before=<n>.<index>&limit=<1-100>  ReceiptsPage
//	GET /api/v1/receipts/export?before=<n>.<index>        ReceiptsExportPage, one line
//
// For the session's own account only: no request names another. Allowed
// under terms the account has not agreed to and while it is closing, as
// the connections' pages are; a removed account has no session. A page
// that begins at a link whose authentication passes a page's bounds is
// asked again here (receipts.ErrContinuation), each try reading further,
// and answered busy (503, Retry-After) when it still does not end.
func ReceiptsAPI(cfg ReceiptsConfig) http.Handler {
	if cfg.Deadline <= 0 {
		cfg.Deadline = mailboxes.RequestDeadline
	}
	a := &receiptsAPI{cfg: cfg}
	mux := NewAppMux()
	in := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return withSession(cfg.Sessions, cfg.Log, func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
			next(w, r, who.AccountID)
		})
	}
	const path = "/api/v1/receipts"
	mux.HandleFunc("GET "+path, in(a.walk))
	mux.HandleFunc(path, refuse(http.MethodGet))
	mux.HandleFunc("GET "+path+"/export", in(a.export))
	mux.HandleFunc(path+"/export", refuse(http.MethodGet))
	mux.HandleFunc(path+"/", noSuchEndpoint)
	app := fromTheApp(mux)
	return RequireCanonicalPath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Deadline)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		app.ServeHTTP(w, r.WithContext(ctx))
	}))
}

type receiptsAPI struct{ cfg ReceiptsConfig }

func (a *receiptsAPI) walk(w http.ResponseWriter, r *http.Request, account string) {
	before, ok := a.before(w, r)
	if !ok {
		return
	}
	limit := DefaultReceiptsLimit
	if s, ok := r.URL.Query()["limit"]; ok {
		n, err := strconv.Atoi(s[0])
		if err != nil || n < 1 || n > MaxReceiptsLimit || len(s) > 1 {
			failed(w, r, a.cfg.Log, contracts.ErrInvalidLimit)
			return
		}
		limit = n
	}
	page, err := again(r.Context(), func() (*receipts.Walked, error) {
		return a.cfg.Chain.Walk(r.Context(), account, before, limit)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, walkedPage(page))
}

func (a *receiptsAPI) export(w http.ResponseWriter, r *http.Request, account string) {
	before, ok := a.before(w, r)
	if !ok {
		return
	}
	page, err := again(r.Context(), func() (*receipts.ExportPage, error) {
		return a.cfg.Chain.Export(r.Context(), account, before)
	})
	if err == nil {
		// What leaves is a page permaily-verify -receipts reads: never one
		// off the contract, whatever the chain answered.
		err = page.Check()
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, exportedPage(page))
}

// before reads where a page begins: from the head without one.
func (a *receiptsAPI) before(w http.ResponseWriter, r *http.Request) (receipts.Position, bool) {
	s, ok := r.URL.Query()["before"]
	if !ok {
		return receipts.Position{}, true
	}
	p, err := contracts.ParseReceiptsPosition(s[0])
	if err != nil || len(s) > 1 {
		failed(w, r, a.cfg.Log, contracts.ErrInvalidBefore)
		return receipts.Position{}, false
	}
	return receipts.Position{N: p.N, Index: p.Index}, true
}

// again asks for a page until it is not a continuation, at most
// continuationTries times and within ctx; one that still is, is busy.
func again[T any](ctx context.Context, page func() (T, error)) (T, error) {
	var out T
	var err error
	for range continuationTries {
		if out, err = page(); !errors.Is(err, receipts.ErrContinuation) || ctx.Err() != nil {
			return out, err
		}
	}
	return out, err
}

// fail answers what the chain refused or could not do: a position it does
// not have (400), a chain whose storage passes what a page reads (503,
// fixed words), a page still continuing (503, asked again later), and
// anything else as internalFailure does, logged by its kind alone.
func (a *receiptsAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, receipts.ErrBefore):
		failed(w, r, a.cfg.Log, contracts.ErrInvalidBefore)
	case errors.Is(err, receipts.ErrHistoryTooLarge):
		writeError(w, http.StatusServiceUnavailable, contracts.ErrReceiptsUnreadable.Code, contracts.ErrReceiptsUnreadable.Message)
	case errors.Is(err, receipts.ErrContinuation):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, contracts.ErrBusy.Code, contracts.ErrBusy.Message)
	default:
		internalFailure(w, r, a.cfg.Log, "receipts", err)
	}
}

// headOf is a head as the person sees it: its digest and time only when it
// is "ok", the one they may note.
func headOf(h receipts.Head) contracts.ReceiptsHead {
	out := contracts.ReceiptsHead{N: h.N, Verdict: string(h.Verdict)}
	if h.Verdict == receipts.LinkOK {
		out.SHA256, out.At = h.Digest.String(), h.At
	}
	return out
}

func positionOf(p receipts.Position) contracts.ReceiptsPosition {
	return contracts.ReceiptsPosition{N: p.N, Index: p.Index}
}

// walkedPage is a page of the walk as it crosses the boundary.
func walkedPage(p *receipts.Walked) contracts.ReceiptsPage {
	out := contracts.ReceiptsPage{Head: headOf(p.Head), Links: make([]contracts.ReceiptsLink, 0, len(p.Links)), Next: positionOf(p.Next), Done: p.Done}
	for _, l := range p.Links {
		cl := contracts.ReceiptsLink{N: l.N, Verdict: string(l.Verdict)}
		if d := l.Link; d != nil {
			cl.Kind, cl.At, cl.Break, cl.Before, cl.More = string(d.Kind), d.At, string(d.Break), d.Before, d.More
			if d.Prev != nil {
				cl.Prev = d.Prev.String()
			}
		}
		for _, rc := range l.Receipts {
			e := contracts.ReceiptsEntry{Index: rc.Index, ID: rc.Ref.ID, SHA256: rc.Ref.Digest.String(), Verdict: string(rc.Verdict)}
			if rc.Receipt != nil {
				e.Receipt, _ = json.Marshal(rc.Receipt) // a Receipt always marshals
			}
			cl.Receipts = append(cl.Receipts, e)
		}
		out.Links = append(out.Links, cl)
	}
	return out
}

// exportedPage is a page of the export as it crosses the boundary: the
// same JSON as receipts.ExportPage, its documents' bytes untouched.
func exportedPage(p *receipts.ExportPage) contracts.ReceiptsExportPage {
	out := contracts.ReceiptsExportPage{V: p.V, Account: p.Account, Head: headOf(p.Head), Links: make([]contracts.ReceiptsExportLink, 0, len(p.Links)), Next: positionOf(p.Next), Done: p.Done}
	for _, l := range p.Links {
		cl := contracts.ReceiptsExportLink{N: l.N, Verdict: string(l.Verdict), Doc: l.Doc}
		for _, rc := range l.Receipts {
			cl.Receipts = append(cl.Receipts, contracts.ReceiptsExportEntry{Index: rc.Index, ID: rc.ID, Verdict: string(rc.Verdict), Doc: rc.Doc})
		}
		out.Links = append(out.Links, cl)
	}
	return out
}
