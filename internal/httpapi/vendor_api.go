package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
// nothing of the answer when it refuses), every chunk admitted only while
// its context lasts, the response aborted, a write blocked in the connection
// included, when that context ends while it is sent, and Finish(true) only
// once the transport handed the last byte to the socket. Only HTTP/1.1
// confirms that: over HTTP/2 those three routes are 503 with Retry-After
// before the token is checked (sendsMail).
func VendorAPI(cfg ConnectionsConfig) http.Handler {
	cfg = cfg.withDefaults()
	v := &vendorAPI{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc(oauth.PathAPI+"/connection", v.route("connection", http.MethodGet, false, v.info))
	mux.HandleFunc(oauth.PathAPI+"/messages/search", v.route("search", http.MethodPost, sendsMail, v.search))
	mux.HandleFunc(oauth.PathAPI+"/messages/{id}", v.route("read", http.MethodGet, sendsMail, v.read))
	mux.HandleFunc(oauth.PathAPI+"/messages/{id}/attachments/{index}", v.route("attachment", http.MethodGet, sendsMail, v.attachment))
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

// sendsMail marks a route whose answer is sent under a lease.
const sendsMail = true

// route answers method alone, and runs next with the grant of the request's
// bearer token; name is the route in the request's line. A route that sends
// mail (leased) is refused over HTTP/2, or on a connection the server did
// not keep, before the token is checked, so nothing is prepared, recorded or
// sent (refusedWithoutAbort).
func (v *vendorAPI) route(name, method string, leased bool, next func(http.ResponseWriter, *http.Request, *oauth.Grant)) http.HandlerFunc {
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
		if leased && refusedWithoutAbort(w, r) {
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

// refusedWithoutAbort answers a request for mail that a cut could not
// abort: 503 with Retry-After, logged no_abort, and true. Over HTTP/2: Go's
// HTTP/2 server writes a response's last bytes (its END_STREAM) after the
// handler returned, into a buffer another goroutine flushes, so nothing in
// the handler can learn when they reached the socket, and a lease could end
// while they wait there (ADR 0030, "Changed after the second review of pull
// request 6"); over HTTP/1.1 the last Flush returns once the socket took
// every byte. On a connection the server did not keep (ConnContext not set):
// nothing could close it, and the response's deadlines alone do not end
// every wait of its writer (ADR 0030, "Changed after the third review of
// pull requests 6 and 7").
func refusedWithoutAbort(w http.ResponseWriter, r *http.Request) bool {
	if conn, _ := r.Context().Value(connKey{}).(net.Conn); r.ProtoMajor == 1 && conn != nil {
		return false
	}
	setOutcome(r, outcomeNoAbort)
	w.Header().Set("Retry-After", retryAfterStorage)
	writeError(w, http.StatusServiceUnavailable, "unavailable", "this answer can't be given now; try again later")
	return true
}

// send writes a prepared answer under its lease (ADR 0030, "Release"): Start
// immediately before the first byte, refused by 503 with nothing of the
// answer; then the answer written by transfer, every chunk admitted only
// while Start's context lasts. Finish is called once, after the transfer,
// sent only for an answer whose last byte the transport handed to the
// socket while the lease was held; a transfer that did not end so aborts the
// response, logged by why. Over HTTP/2, or without the connection's hook,
// the route refused it before (route).
func (v *vendorAPI) send(w http.ResponseWriter, r *http.Request, rel connections.Release, contentType string, body []byte, header func(http.Header)) {
	if r.Context().Err() != nil {
		rel.Finish(false) // the client left: nothing is started
		setOutcome(r, outcomeClientLeft)
		return
	}
	conn, _ := r.Context().Value(connKey{}).(net.Conn)
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

// transfer writes body under ctx (the lease's; req is the request's) over
// HTTP/1.1, in chunks of sendChunk, and returns once the transport handed
// its last byte, the header included, to the socket: the last Flush of
// HTTP/1.1 returns only then, an empty answer's included. Every chunk, and
// every flush, enters the transport only through its admission (admission),
// which the abort shares: once the lease is seen to have ended, by the
// transfer or by the context's callback, the abort (the socket's write
// deadline, the connection's close, the response's deadline) has completed
// and no write enters after it. A cut that lands between a chunk's admission
// and its write lets that one chunk go (at most sendChunk bytes of the
// answer, with the header when it is the first), and nothing more. sent says
// every byte was handed to the socket; ended, when not "", why the transfer
// ended otherwise: outcomeCut (ctx ended), outcomeClientLeft (ctx ended with
// req) or outcomeAborted (a write failed). Once the last flush returned the
// transfer is complete (admission.complete): a cut seen before then leaves
// it unsent, the client leaving or a cut after it does not. The abort never
// runs once transfer returned, on a writer its handler no longer has.
func transfer(ctx, req context.Context, w http.ResponseWriter, conn net.Conn, body []byte) (sent bool, ended string) {
	a := &admission{ctx: ctx, req: req, conn: conn, rc: http.NewResponseController(w), done: make(chan struct{})}
	stop := context.AfterFunc(ctx, a.abort)
	defer func() {
		if !stop() {
			<-a.done // the callback's abort: what it did is done
		}
		if why, cut := a.aborted(); cut {
			sent, ended = false, why
		}
	}()
	if !a.admit() {
		return false, "" // not even the header goes
	}
	w.WriteHeader(http.StatusOK) // HTTP/1.1 holds it for the first write
	for off := 0; ; off += sendChunk {
		chunk := body[off:min(off+sendChunk, len(body))]
		if len(chunk) > 0 {
			if !a.admit() {
				return false, ""
			}
			if _, err := w.Write(chunk); err != nil {
				return false, outcomeAborted
			}
		}
		if !a.admit() {
			return false, "" // what the write left buffered is never flushed
		}
		if a.rc.Flush() != nil {
			return false, outcomeAborted
		}
		if off+len(chunk) == len(body) {
			a.complete()
			return true, "" // unless a cut came first (aborted)
		}
	}
}

// admission is one transfer's state, shared by its writer and its abort:
// open until the lease's context is seen to have ended, then aborted, and
// never open again.
type admission struct {
	ctx, req context.Context
	conn     net.Conn
	rc       *http.ResponseController

	once sync.Once
	done chan struct{} // closed when the abort completed

	mu        sync.Mutex
	cut       bool
	why       string
	completed bool // the last flush returned before a cut of the lease
}

// admit says a write may enter the transport: the lease's context has not
// ended and the transfer was not aborted. Otherwise it aborts the transfer,
// returning once the abort completed, and false.
func (a *admission) admit() bool {
	a.mu.Lock()
	open := !a.cut && a.ctx.Err() == nil
	a.mu.Unlock()
	if !open {
		a.abort()
	}
	return open
}

// abort closes the admission and the connection, once; a second call
// returns when the first completed. The socket's deadlines go first: a TLS
// Close waits for TLS's output lock, which a write blocked on the socket (a
// KeyUpdate's reply among them) holds, and would hold the cut as long as the
// peer reads nothing. The read's deadline is set too, the response's as
// well as the socket's: Go's HTTP/1.1 server drains a body nobody read as
// the answer's header is first written, a read no write deadline ends.
func (a *admission) abort() {
	a.once.Do(func() {
		defer close(a.done)
		a.mu.Lock()
		a.cut, a.why = true, outcomeCut
		if a.req.Err() != nil {
			a.why = outcomeClientLeft
		}
		a.mu.Unlock()
		if a.conn != nil {
			_ = a.conn.SetDeadline(time.Now())
			_ = a.conn.Close()
		}
		_ = a.rc.SetReadDeadline(time.Now())
		_ = a.rc.SetWriteDeadline(time.Now())
	})
}

// complete records that the last flush returned: every byte, the header
// included, was handed to the socket. The transfer is then complete unless
// the lease was cut before it was recorded; the client leaving (the
// request's context ending, which ends the lease's) unsends nothing the
// socket already took, whenever its abort ran. Once complete, a later abort
// may still close the connection but no longer changes what was sent.
func (a *admission) complete() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completed = !a.cut || a.why == outcomeClientLeft
}

// aborted says whether the transfer was aborted before it was complete, and
// why.
func (a *admission) aborted() (why string, cut bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.why, a.cut && !a.completed
}

type connKey struct{}

// ConnContext is the enclosing http.Server's ConnContext (the composition
// sets it, with internal/mcpapi's when one server serves both): it keeps
// each connection in its requests' context, so that a lease that ends while
// its answer is sent closes the connection itself, the socket's deadline
// set first, and nothing the server still holds of the response leaves when
// it closes it. Without it a route that sends mail is refused (503 no_abort)
// before the token is checked.
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
