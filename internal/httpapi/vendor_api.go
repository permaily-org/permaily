package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
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
// attachment without mail.attachments is the service's to refuse, answered
// 403 part_not_approved with the insufficient_scope challenge naming the
// connection's scopes and mail.attachments (oauth.Grant.StepUp). A refusal
// the API makes itself once Check accepted the token (a body it cannot
// read, a formula it cannot parse, an attachment's index it cannot read) is
// recorded through the service's Refuse before it is answered, and is 503
// with Retry-After when it could not be. While the model cannot be asked,
// 503 model_unavailable with Retry-After. A search, a read and an
// attachment are sent only under the lease the service answered with
// (connections.Release): Start immediately before the first byte (503 and
// nothing of the answer when it refuses), every byte under its context, and
// the response aborted, a write blocked in the connection included, when
// that context ends while it is sent. Over HTTP/2 that needs the server's
// ConnContext to be this package's (ConnContext): without it a leased answer
// is 503 before its lease starts.
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
	invalidBody := func() {
		if v.recorded(w, r, g, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}) {
			writeInvalidBody(w)
		}
	}
	members, ok := parseObjectUpTo(w, r, maxVendorSearchBody, "query", "limit", "cursor")
	if !ok {
		invalidBody()
		return
	}
	var in contracts.VendorSearch
	if raw, ok := members["query"]; ok && string(raw) != "null" {
		// Read strictly, so a refusal says where in the formula as sent and
		// why; the service is given it as sent, so the parts it checks a
		// condition against are placed the same way.
		if _, err := query.Parse(raw, nil); err != nil {
			if v.recorded(w, r, g, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedFormula}) {
				v.failed(w, r, g, err)
			}
			return
		}
		in.Query = new(contracts.Formula)
		_ = json.Unmarshal(raw, in.Query) // what Parse takes is a Formula's JSON
	}
	if raw, ok := members["limit"]; ok && json.Unmarshal(raw, &in.Limit) != nil {
		invalidBody()
		return
	}
	if raw, ok := members["cursor"]; ok && json.Unmarshal(raw, &in.Cursor) != nil {
		invalidBody()
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
	id, s := r.PathValue("id"), r.PathValue("index")
	index, err := strconv.Atoi(s)
	if err != nil || index < 0 || strconv.Itoa(index) != s {
		// Not one the connection reads: recorded, with the message as the
		// request named it when it is not too long for a receipt (the
		// service keeps it only when it is one of the connection's).
		named := id
		if len(named) > connections.RefusalMessageMax {
			named = ""
		}
		if v.recorded(w, r, g, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedOutsideScope, Message: named}) {
			failed(w, r, v.cfg.Log, contracts.ErrNotFound)
		}
		return
	}
	// Without mail.attachments, the service refuses it, and records it with
	// its evidence: the route never does.
	att, rel, err := v.cfg.Service.Attachment(r.Context(), g, id, index)
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

// recorded records a refusal the API makes itself of a request whose token
// Check accepted (connections.API.Refuse, with the grant Check returned for
// it), before the refusal is answered: true once it is stored. When it
// could not be (the receipt not written), the answer is 503 with
// Retry-After and nothing else, and false.
func (v *vendorAPI) recorded(w http.ResponseWriter, r *http.Request, g *oauth.Grant, refusal connections.Refusal) bool {
	if err := v.cfg.Service.Refuse(r.Context(), g, refusal); err != nil {
		setOutcome(r, outcomeNotRecorded)
		w.Header().Set("Retry-After", retryAfterStorage)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this answer can't be given now; try again later")
		return false
	}
	return true
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
// answer; then the answer written by transfer, every chunk only while
// Start's context lasts. Over HTTP/2 a cut must close the connection
// (ConnContext): without it, the answer is refused before its lease starts.
// Finish is called once, sent only for the whole answer; a transfer that
// did not end whole aborts the response, logged by why.
func (v *vendorAPI) send(w http.ResponseWriter, r *http.Request, rel connections.Release, contentType string, body []byte, header func(http.Header)) {
	if r.Context().Err() != nil {
		rel.Finish(false) // the client left: nothing is started
		setOutcome(r, outcomeClientLeft)
		return
	}
	conn, _ := r.Context().Value(connKey{}).(net.Conn)
	if r.ProtoMajor >= 2 && conn == nil {
		// A cut could not stop a frame already handed to the connection.
		rel.Finish(false)
		setOutcome(r, outcomeNoAbort)
		w.Header().Set("Retry-After", retryAfterStorage)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this answer can't be given now; try again later")
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
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if header != nil {
		header(h)
	}
	sent, ended := transfer(ctx, r.Context(), w, conn, body)
	rel.Finish(sent)
	if ended != "" {
		// The lease ended before the last byte left, or as it did, or a
		// write failed: the response is aborted, nothing more of it sent.
		setOutcome(r, ended)
		panic(http.ErrAbortHandler)
	}
}

// transfer writes body under ctx (the lease's; req is the request's), and
// the lease is read before the header, before every write and again before
// every flush: each chunk is written, and flushed, only while ctx lasts.
// When ctx ends during the transfer, the connection (conn, ConnContext's) is
// closed, the socket's write deadline set to now first, and the response's
// write deadline set to now: a write blocked on a client that reads nothing
// returns, over HTTP/1.1 as over HTTP/2, where the stream's deadline alone
// leaves a frame already handed to the connection to leave when the client
// reads again. That abort runs before transfer returns, never on a writer
// its handler no longer has. sent says every byte was written and flushed;
// ended, when not "", why the transfer ended otherwise: outcomeCut (ctx
// ended), outcomeClientLeft (ctx ended with req) or outcomeAborted (a write
// failed).
func transfer(ctx, req context.Context, w http.ResponseWriter, conn net.Conn, body []byte) (sent bool, ended string) {
	rc := http.NewResponseController(w)
	aborted := make(chan struct{})
	why := outcomeCut
	stop := context.AfterFunc(ctx, func() {
		defer close(aborted)
		if req.Err() != nil {
			why = outcomeClientLeft
		}
		if conn != nil {
			// The socket's write first: a TLS Close waits for TLS's output
			// lock, which a KeyUpdate's reply blocked on the socket holds,
			// and would hold the cut as long as the peer reads nothing.
			_ = conn.SetWriteDeadline(time.Now())
			_ = conn.Close()
		}
		_ = rc.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-aborted // the abort ran: what it did is done
			sent, ended = false, why
		}
	}()
	// Ended already: not even the header goes.
	if ctx.Err() != nil {
		return false, ""
	}
	w.WriteHeader(http.StatusOK)
	for off := 0; off < len(body); off += sendChunk {
		if ctx.Err() != nil {
			return false, ""
		}
		if _, err := w.Write(body[off:min(off+sendChunk, len(body))]); err != nil || ctx.Err() != nil {
			return false, outcomeAborted
		}
		if rc.Flush() != nil {
			return false, outcomeAborted
		}
	}
	return true, ""
}

type connKey struct{}

// ConnContext is the enclosing http.Server's ConnContext (the composition
// sets it, with internal/mcpapi's when one server serves both): it keeps
// each connection in its requests' context, so that a lease that ends while
// its answer is sent closes the connection itself. Over HTTP/2 that is the
// only way to stop a frame already handed to the connection; it ends the
// connection's other streams too, which their clients send again. Without
// it, a leased answer over HTTP/2 is 503.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
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
// the connection's scopes and mail.attachments, in one, even when a refresh
// narrowed the token (ADR 0030, "Errors and challenges"; oauth.Grant.StepUp).
func (v *vendorAPI) insufficient(w http.ResponseWriter, g *oauth.Grant) {
	scopes := []string{oauth.ScopeSender, oauth.ScopeAttachments}
	if g != nil {
		scopes = g.StepUp(oauth.ScopeAttachments)
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
