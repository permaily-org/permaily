package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/connections"
)

const (
	// retryAfterStorage is what a vendor is told to wait when the storage
	// could not be read or an answer could no longer be released;
	// retryAfterModel when the model cannot be asked (the owner's decision
	// 9), which lasts longer.
	retryAfterStorage = "5"
	retryAfterModel   = "60"
	// maxVendorSearchBody bounds POST /v1/messages/search: a formula of at
	// most contracts.MaxFormulaBytes, a limit and a cursor.
	maxVendorSearchBody = contracts.MaxFormulaBytes + 4<<10
	// sendChunk is how much of an answer is written, and flushed, at once:
	// each chunk only while the answer's lease lets it (connections.Release).
	sendChunk = 32 << 10
)

// VendorAPI returns the API for apps on the api host (ADR 0030, "The API for
// apps"), each route behind a bearer token for https://api.<domain>/v1 and
// nothing else (never in a query or a body), answering no-store and CORS for
// any origin without credentials:
//
//	GET  /v1/connection                          VendorConnection
//	POST /v1/messages/search                     VendorSearch → VendorPage
//	GET  /v1/messages/{id}                       VendorMessage
//	GET  /v1/messages/{id}/attachments/{index}   the bytes, a download
//
// Without a token: 401 with the challenge naming the resource's metadata
// and the default scopes; with one that is not valid (an MCP token among
// them): the same with invalid_token; a storage that could not be read:
// 503 with Retry-After, never 401, so a client keeps its tokens. An
// attachment without mail.attachments is 403 part_not_approved with the
// insufficient_scope challenge naming every scope then needed. While the
// model cannot be asked, 503 model_unavailable with Retry-After. A search,
// a read and an attachment are sent only under the lease the service
// answered with (connections.Release): Start immediately before the first
// byte (503 and nothing of the answer when it refuses), every byte under its
// context, and the response aborted, a blocked write included, when that
// context ends while it is sent.
func VendorAPI(cfg ConnectionsConfig) http.Handler {
	cfg = cfg.withDefaults()
	v := &vendorAPI{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc(oauth.PathAPI+"/connection", v.route("connection", http.MethodGet, v.info))
	mux.HandleFunc(oauth.PathAPI+"/messages/search", v.route("search", http.MethodPost, v.search))
	mux.HandleFunc(oauth.PathAPI+"/messages/{id}", v.route("read", http.MethodGet, v.read))
	mux.HandleFunc(oauth.PathAPI+"/messages/{id}/attachments/{index}", v.route("attachment", http.MethodGet, v.attachment))
	mux.HandleFunc("/", noSuchEndpoint)
	return logRequests(cfg.Log, "api: request", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Retry-After")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !requestCanonical(r) {
			noSuchEndpoint(w, r) // never ServeMux's redirect to the path cleaned
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

type vendorAPI struct{ cfg ConnectionsConfig }

// route answers method alone, and runs next with the grant of the request's
// bearer token; name is the route in the request's line.
func (v *vendorAPI) route(name, method string, next func(http.ResponseWriter, *http.Request, *oauth.Grant)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setRoute(r, name)
		if r.Method != method {
			refuse(method)(w, r)
			return
		}
		token, sent := bearer(r)
		if !sent {
			v.challenge(w, http.StatusUnauthorized, "", nil, "unauthorized", "a bearer token for this API is required")
			return
		}
		g, err := v.cfg.Service.Check(r.Context(), contracts.ResourceAPI, token)
		if err != nil {
			v.failed(w, r, nil, err)
			return
		}
		next(w, r, g)
	}
}

// bearer is the request's bearer token, from its one Authorization header
// (RFC 6750 section 2.1; the scheme in any case). sent is false when the
// request carries none: no header, or another scheme. Two headers, or a
// bearer without a token, are a token that is not valid ("").
func bearer(r *http.Request) (token string, sent bool) {
	v := r.Header.Values("Authorization")
	if len(v) == 0 {
		return "", false
	}
	if len(v) > 1 {
		return "", true
	}
	scheme, token, _ := strings.Cut(v[0], " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func (v *vendorAPI) info(w http.ResponseWriter, r *http.Request, g *oauth.Grant) {
	out, err := v.cfg.Service.Info(r.Context(), g)
	if err != nil {
		v.failed(w, r, g, err)
		return
	}
	writeJSON(w, out)
}

func (v *vendorAPI) search(w http.ResponseWriter, r *http.Request, g *oauth.Grant) {
	members, ok := readObjectUpTo(w, r, maxVendorSearchBody, "query", "limit", "cursor")
	if !ok {
		return
	}
	var in contracts.VendorSearch
	if raw, ok := members["query"]; ok && string(raw) != "null" {
		// Read strictly, so a refusal says where in the formula as sent and
		// why; the service is given it as sent, so the parts it checks a
		// condition against are placed the same way.
		if _, err := query.Parse(raw, nil); err != nil {
			v.failed(w, r, g, err)
			return
		}
		in.Query = new(contracts.Formula)
		_ = json.Unmarshal(raw, in.Query) // what Parse takes is a Formula's JSON
	}
	if raw, ok := members["limit"]; ok && json.Unmarshal(raw, &in.Limit) != nil {
		writeInvalidBody(w)
		return
	}
	if raw, ok := members["cursor"]; ok && json.Unmarshal(raw, &in.Cursor) != nil {
		writeInvalidBody(w)
		return
	}
	page, rel, err := v.cfg.Service.Search(r.Context(), g, in)
	if err != nil {
		v.refused(w, r, g, rel, err)
		return
	}
	v.sendJSON(w, r, rel, page)
}

func (v *vendorAPI) read(w http.ResponseWriter, r *http.Request, g *oauth.Grant) {
	msg, rel, err := v.cfg.Service.Read(r.Context(), g, r.PathValue("id"))
	if err != nil {
		v.refused(w, r, g, rel, err)
		return
	}
	v.sendJSON(w, r, rel, msg)
}

func (v *vendorAPI) attachment(w http.ResponseWriter, r *http.Request, g *oauth.Grant) {
	s := r.PathValue("index")
	index, err := strconv.Atoi(s)
	if err != nil || index < 0 || strconv.Itoa(index) != s {
		failed(w, r, v.cfg.Log, contracts.ErrNotFound)
		return
	}
	if !slices.Contains(g.Parts, contracts.PartAttachments) {
		v.insufficient(w, g)
		return
	}
	att, rel, err := v.cfg.Service.Attachment(r.Context(), g, r.PathValue("id"), index)
	if err != nil {
		v.refused(w, r, g, rel, err)
		return
	}
	if len(att.Data) > contracts.VendorAttachmentMax {
		rel.Finish(false)
		v.failed(w, r, g, contracts.ErrAttachmentTooLarge)
		return
	}
	v.send(w, r, rel, "application/octet-stream", att.Data, func(h http.Header) {
		h.Set("Content-Disposition", "attachment")
	})
}

// refused answers a refusal of the service, ending a lease that came with it.
func (v *vendorAPI) refused(w http.ResponseWriter, r *http.Request, g *oauth.Grant, rel connections.Release, err error) {
	if rel != nil {
		rel.Finish(false)
	}
	v.failed(w, r, g, err)
}

func (v *vendorAPI) sendJSON(w http.ResponseWriter, r *http.Request, rel connections.Release, out any) {
	b, _ := json.Marshal(out) // the contract's structs: strings, times, numbers, lists
	v.send(w, r, rel, "application/json", append(b, '\n'), nil)
}

// send writes a prepared answer under its lease (ADR 0030, "Release"): Start
// immediately before the first byte, refused by 503 with nothing of the
// answer; then every chunk written, and flushed, only while Start's context
// lasts, read before each write and before each flush. When it ends during
// the transfer (a revocation, the authority read
// again and found ended, the token's or the connection's end, TransferMax),
// the write deadline is set to now, so a write blocked on a client that
// reads nothing returns, and the response is aborted: no byte is written
// after it. Finish is called once, sent only for the whole answer.
func (v *vendorAPI) send(w http.ResponseWriter, r *http.Request, rel connections.Release, contentType string, body []byte, header func(http.Header)) {
	if r.Context().Err() != nil {
		rel.Finish(false) // the client left: nothing is started
		setOutcome(r, outcomeClientLeft)
		return
	}
	ctx, err := rel.Start(r.Context())
	if err != nil {
		rel.Finish(false)
		setOutcome(r, outcomeNotReleased)
		w.Header().Set("Retry-After", retryAfterStorage)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this answer can't be given now; try again later")
		return
	}
	sent := false
	defer func() {
		switch {
		case sent:
		case r.Context().Err() != nil:
			setOutcome(r, outcomeClientLeft)
		case ctx.Err() != nil:
			setOutcome(r, outcomeCut)
		}
		rel.Finish(sent)
	}()
	rc := http.NewResponseController(w)
	stop := context.AfterFunc(ctx, func() { _ = rc.SetWriteDeadline(time.Now()) })
	defer stop()
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if header != nil {
		header(h)
	}
	// Ended already (the deadline is set by AfterFunc's own goroutine, so it
	// is checked here too): not even the header goes.
	if ctx.Err() != nil {
		panic(http.ErrAbortHandler)
	}
	w.WriteHeader(http.StatusOK)
	// The lease is read before every write and again before every flush:
	// the deadline above only interrupts a write already blocked, and its
	// callback may not have run yet when the context ends, so a chunk is
	// never written, nor one already buffered flushed, once it has ended.
	for off := 0; off < len(body); off += sendChunk {
		if ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(body[off:min(off+sendChunk, len(body))]); err != nil || ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		if rc.Flush() != nil {
			panic(http.ErrAbortHandler)
		}
	}
	sent = true
}

// failed answers a refusal of the API: a token not valid with its challenge,
// the storage or a lease that could not go on with 503, the model unavailable
// with 503 and its own wait, an attachment not approved with the step-up
// challenge, one too large 413, and any other as failed answers it (404
// not_found, 400 invalid_formula with its details, invalid_cursor,
// invalid_limit, 429, and an internal failure logged without what it was
// about).
func (v *vendorAPI) failed(w http.ResponseWriter, r *http.Request, g *oauth.Grant, err error) {
	switch {
	case errors.Is(err, connections.ErrInvalidToken), errors.Is(err, connections.ErrWrongResource):
		v.challenge(w, http.StatusUnauthorized, oauth.ErrInvalidToken, nil, string(oauth.ErrInvalidToken), "the token is not valid; sign in again")
	case errors.Is(err, connections.ErrUnavailable), errors.Is(err, connections.ErrTooLate):
		w.Header().Set("Retry-After", retryAfterStorage)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this answer can't be given now; try again later")
	case errors.Is(err, contracts.ErrModelUnavailable):
		w.Header().Set("Retry-After", retryAfterModel)
		writeError(w, http.StatusServiceUnavailable, contracts.ErrModelUnavailable.Code, contracts.ErrModelUnavailable.Message)
	case errors.Is(err, contracts.ErrPartNotApproved):
		v.insufficient(w, g)
	case errors.Is(err, contracts.ErrAttachmentTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, contracts.ErrAttachmentTooLarge.Code, contracts.ErrAttachmentTooLarge.Message)
	case refusal(err):
		failed(w, r, v.cfg.Log, err)
	default:
		internalFailure(w, r, v.cfg.Log, "vendor api", err)
	}
}

// insufficient answers a read that needs mail.attachments, which the grant
// does not have: 403 part_not_approved, and the step-up challenge naming
// every scope the client then needs, in one (ADR 0030, "Errors and
// challenges").
func (v *vendorAPI) insufficient(w http.ResponseWriter, g *oauth.Grant) {
	var scopes []string
	for _, sp := range oauth.ScopeParts {
		if sp.Scope == oauth.ScopeAttachments || g != nil && slices.Contains(g.Scopes, sp.Scope) {
			scopes = append(scopes, sp.Scope)
		}
	}
	v.challenge(w, http.StatusForbidden, oauth.ErrInsufficientScope, scopes, contracts.ErrPartNotApproved.Code, contracts.ErrPartNotApproved.Message)
}

// challenge answers status with the resource's WWW-Authenticate (scopes nil:
// the default three) and the API's error body.
func (v *vendorAPI) challenge(w http.ResponseWriter, status int, code oauth.ErrorCode, scopes []string, body, message string) {
	if scopes == nil {
		scopes = oauth.DefaultScopes
	}
	w.Header().Set("WWW-Authenticate", v.cfg.Challenge(v.cfg.OAuth, contracts.ResourceAPI, code, scopes))
	writeError(w, status, body, message)
}

// ConnContext is the enclosing http.Server's ConnContext hook (not yet
// keeping the connection).
func ConnContext(ctx context.Context, _ net.Conn) context.Context { return ctx }
