package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

func testGrant(parts ...contracts.Part) *oauth.Grant {
	return &oauth.Grant{Parts: parts, Scopes: []string{oauth.ScopeSender}, TokenExpiresAt: time.Now().Add(time.Hour)}
}

func testHandler(f *connectionstest.Fake) *handler {
	return New(f, oauth.Config{MCPResource: "https://mcp.permaily.test/mcp"}).(*handler)
}

func requestWith(c *call) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{extraCall: c}}}}
}

// A tool reached without the middleware's grant (no token's information, or
// one that carries no call) answers nothing and asks the service nothing.
func TestAToolWithoutTheMiddlewaresGrantAnswersNothing(t *testing.T) {
	f := &connectionstest.Fake{}
	h := testHandler(f)
	ctx := context.Background()
	for _, req := range []*mcp.CallToolRequest{
		nil,
		{},
		{Extra: &mcp.RequestExtra{}},
		{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{}}},
		requestWith(&call{}),
	} {
		_, _, e1 := h.connectionInfo(ctx, req, NoInput{})
		_, _, e2 := h.searchMail(ctx, req)
		_, _, e3 := h.readMessage(ctx, req, MessageID{})
		_, _, e4 := h.getAttachment(ctx, req, AttachmentID{})
		for _, err := range []error{e1, e2, e3, e4} {
			if err == nil || !strings.Contains(err.Error(), `"unauthorized"`) {
				t.Fatalf("%v", err)
			}
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("the service was asked %v", f.Calls())
	}
	if _, err := verified(ctx, "", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatal(err)
	}
}

// Mail for an answer the middleware did not hold for its lease, or a second
// lease for one answer, is never sent: the lease is ended and the tool
// answers "internal".
func TestMailForAnAnswerNotHeldIsNeverSent(t *testing.T) {
	l := &connectionstest.Lease{}
	if err := (&call{abortable: true}).hold(l); !errors.Is(err, errNotBuffered) {
		t.Fatal(err)
	}
	if started, finished, _, _ := l.State(); started || !finished {
		t.Fatal("the lease was not ended")
	}
	for _, c := range []*call{
		{grant: testGrant(contracts.PartSender), buffered: true, abortable: true, release: &connectionstest.Lease{}},
	} {
		f := &connectionstest.Fake{}
		l := &connectionstest.Lease{}
		f.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
			return &contracts.VendorMessage{ID: "secret"}, l, nil
		}
		f.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
			return &inbox.Attachment{Data: []byte("secret")}, l, nil
		}
		h := testHandler(f)
		if _, out, err := h.readMessage(context.Background(), requestWith(c), MessageID{ID: "x"}); err == nil || out != nil || !strings.Contains(err.Error(), `"internal"`) {
			t.Fatalf("%v %v", out, err)
		}
		if started, finished, _, _ := l.State(); started || !finished {
			t.Fatal("the lease was not ended")
		}
		c.grant.Parts = append(c.grant.Parts, contracts.PartAttachments)
		l = &connectionstest.Lease{}
		if _, out, err := h.getAttachment(context.Background(), requestWith(c), AttachmentID{ID: "x"}); err == nil || out != nil {
			t.Fatalf("%v %v", out, err)
		}
		if started, finished, _, _ := l.State(); started || !finished {
			t.Fatal("the lease was not ended")
		}
	}
}

// The tool checks the part itself, whatever the middleware read of the
// body: without it the service is asked for nothing but to record the
// refusal, with the call's grant, and the answer is the step-up; when the
// refusal cannot be recorded, the call is 503 and not the step-up.
func TestTheToolChecksThePartItself(t *testing.T) {
	f := &connectionstest.Fake{RefuseFunc: func(context.Context, *oauth.Grant, connections.Refusal) error { return nil }}
	g := testGrant(contracts.PartSender, contracts.PartSubject)
	g.Resource = contracts.ResourceMCP
	c := &call{grant: g, buffered: true, abortable: true}
	if _, _, err := testHandler(f).getAttachment(context.Background(), requestWith(c), AttachmentID{ID: "x"}); err == nil {
		t.Fatal("answered")
	}
	want := []connections.Refusal{{Operation: connections.OperationAttachment, Reason: receipts.RefusedPartNotGiven}}
	if _, stepUp, _, _ := c.taken(); !slices.Equal(stepUp, []contracts.Part{contracts.PartAttachments}) ||
		!slices.Equal(f.Calls(), []string{"Refuse"}) || !slices.Equal(f.Refusals(), want) {
		t.Fatalf("%v %v %v", stepUp, f.Calls(), f.Refusals())
	}

	f = &connectionstest.Fake{} // its Refuse answers ErrNotSet
	c = &call{grant: g, buffered: true, abortable: true}
	if _, _, err := testHandler(f).getAttachment(context.Background(), requestWith(c), AttachmentID{ID: "x"}); err == nil {
		t.Fatal("answered")
	}
	if _, stepUp, status, _ := c.taken(); len(stepUp) != 0 || status != http.StatusServiceUnavailable || !slices.Equal(f.Calls(), []string{"Refuse"}) {
		t.Fatalf("%v %d %v", stepUp, status, f.Calls())
	}
}

// V50 (the third review of #131 and #144, finding 1 and ruling 2): each mail
// tool is its own boundary, whatever the middleware read of the body: a call
// whose answer is not held for its lease, or that did not come over HTTP/1.1
// on a connection a cut can close, is 503 no_abort and the service is asked
// nothing, so it prepares no mail and no receipt.
func TestV50CEachMailToolRefusesOffAnAbortCapableConnection(t *testing.T) {
	for _, held := range []struct{ buffered, abortable bool }{{false, false}, {true, false}, {false, true}} {
		f := &connectionstest.Fake{}
		h := testHandler(f)
		ctx := context.Background()
		tools := map[string]func(*call) error{
			ToolSearchMail: func(c *call) error {
				_, _, err := h.searchMail(ctx, requestWith(c))
				return err
			},
			ToolReadMessage: func(c *call) error {
				_, _, err := h.readMessage(ctx, requestWith(c), MessageID{ID: "mbx_A.msg_1"})
				return err
			},
			ToolGetAttachment: func(c *call) error {
				_, _, err := h.getAttachment(ctx, requestWith(c), AttachmentID{ID: "mbx_A.msg_1"})
				return err
			},
		}
		for tool, run := range tools {
			c := &call{grant: testGrant(contracts.PartSender, contracts.PartAttachments), buffered: held.buffered, abortable: held.abortable}
			if err := run(c); err == nil || !strings.Contains(err.Error(), "temporarily_unavailable") {
				t.Fatalf("%s: %v", tool, err)
			}
			if _, _, status, outcome := c.taken(); status != http.StatusServiceUnavailable || outcome != "no_abort" {
				t.Fatalf("%s: %d %s", tool, status, outcome)
			}
		}
		if calls := f.Calls(); len(calls) != 0 {
			t.Fatalf("the service was asked %v", calls)
		}
	}
}

// An answer whose lease's context has already ended when it would start (the
// client went away) is aborted with nothing sent, and finished once, not
// sent; so is one the SDK made larger than AnswerMax, or not at all.
func TestAnAnswerThatCannotBeSentIsNotSent(t *testing.T) {
	h := testHandler(&connectionstest.Fake{})
	t.Run("the request ended", func(t *testing.T) {
		l := &connectionstest.Lease{}
		c := &call{grant: testGrant(), buffered: true, release: l}
		rec := newRecorder(AnswerMax)
		_, _ = rec.Write([]byte("secret answer"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		if outcome, abort := h.answer(w, r, c, rec); !abort || outcome != "client_left" {
			t.Fatalf("not aborted: %s %v", outcome, abort)
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("sent")
		}
		if started, finished, sent, _ := l.State(); !started || !finished || sent {
			t.Fatal("the lease")
		}
	})
	for name, rec := range map[string]*recorder{
		"too large":   {header: http.Header{}, max: 4, status: 0},
		"never wrote": newRecorder(AnswerMax),
	} {
		t.Run(name, func(t *testing.T) {
			_, _ = rec.Write([]byte("secret answer"))
			_, _ = rec.Write([]byte("more"))
			rec.WriteHeader(http.StatusTeapot)
			if name == "never wrote" {
				rec = newRecorder(AnswerMax)
			}
			l := &connectionstest.Lease{}
			c := &call{grant: testGrant(), buffered: true, release: l}
			w := httptest.NewRecorder()
			if got, abort := h.answer(w, httptest.NewRequest(http.MethodPost, "/mcp", nil), c, rec); got != "internal" || abort || w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("%s %d %s", got, w.Code, w.Body.String())
			}
			if started, finished, _, _ := l.State(); started || !finished {
				t.Fatal("the lease")
			}
		})
	}
}

// A write that fails during the transfer (the client went away) ends it:
// not sent, and nothing more written; one whose context ends while it
// writes reports the cut.
func TestAFailedWriteEndsTheTransfer(t *testing.T) {
	rec := newRecorder(AnswerMax)
	_, _ = rec.Write(make([]byte, 3*sendChunk))
	bg := context.Background()
	w := &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 1}
	if sent, ended := send(bg, bg, w, nil, rec); sent || ended != "write_failed" {
		t.Fatalf("sent %v ended %q", sent, ended)
	}
	ctx, cancel := context.WithCancel(bg)
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 2, onWrite: cancel}
	if sent, ended := send(ctx, bg, w, nil, rec); sent || ended != "cut" {
		t.Fatalf("sent %v ended %q", sent, ended)
	}
	ctx, cancel = context.WithCancel(bg)
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 100, onWrite: cancel}
	if sent, ended := send(ctx, ctx, w, nil, rec); sent || ended != "client_left" || w.writes != 1 {
		t.Fatalf("sent %v ended %q after %d writes", sent, ended, w.writes)
	}
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 100, failFlush: true}
	if sent, ended := send(bg, bg, w, nil, rec); sent || ended != "write_failed" {
		t.Fatalf("sent %v ended %q without a flush", sent, ended)
	}
}

type failingWriter struct {
	*httptest.ResponseRecorder
	failAfter int
	writes    int
	onWrite   func()
	onFlush   func()
	failFlush bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.onWrite != nil {
		f.onWrite()
	}
	if f.writes >= f.failAfter {
		return 0, errors.New("broken pipe")
	}
	return f.ResponseRecorder.Write(p)
}

func (f *failingWriter) FlushError() error {
	if f.onFlush != nil {
		f.onFlush()
	}
	if f.failFlush {
		return errors.New("broken pipe")
	}
	return nil
}

func (f *failingWriter) SetWriteDeadline(time.Time) error { return nil }

// The recorder takes the SDK's flushes and keeps the first status.
func TestTheRecorderKeepsTheFirstStatus(t *testing.T) {
	rec := newRecorder(10)
	rec.Flush()
	rec.WriteHeader(http.StatusAccepted)
	rec.WriteHeader(http.StatusOK)
	if rec.status != http.StatusAccepted {
		t.Fatal(rec.status)
	}
	if _, err := rec.Write(make([]byte, 11)); !errors.Is(err, errAnswerTooLarge) || !rec.overflow {
		t.Fatal(err)
	}
	if _, err := rec.Write([]byte("x")); !errors.Is(err, errAnswerTooLarge) {
		t.Fatal(err)
	}
}

// The pass-through writer gives the SDK a stream it can flush, with no-store.
func TestThePassThroughWriterFlushes(t *testing.T) {
	w := httptest.NewRecorder()
	n := &noStore{ResponseWriter: w}
	n.Flush()
	if !w.Flushed || w.Header().Get("Cache-Control") != "no-store" || n.Unwrap() != w {
		t.Fatal(w.Header())
	}
}

// search_mail's arguments are one JSON object of query, limit and cursor,
// each at most once, with nothing after it; anything else is invalid_body,
// whatever the SDK would have let through.
func TestSearchArgsReadOnlySearchMailsArguments(t *testing.T) {
	for _, raw := range []string{`[]`, `"x"`, `{"limit":}`, `{"limit":1`, `{"limit":1} {}`, `{"limit":1,}`, `{1:2}`} {
		if _, err := searchArgs(json.RawMessage(raw)); !errors.Is(err, errInvalidArguments) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{``, ` `, `{}`, `{"query":null}`} {
		if in, err := searchArgs(json.RawMessage(raw)); err != nil || in.Query != nil {
			t.Fatalf("%q: %+v %v", raw, in, err)
		}
	}
}

// A lease that ends between two chunks, as one is flushed, stops the
// transfer before the next is written.
func TestACutBetweenChunksWritesNoMore(t *testing.T) {
	rec := newRecorder(AnswerMax)
	_, _ = rec.Write(make([]byte, 3*sendChunk))
	ctx, cancel := context.WithCancel(context.Background())
	w := &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 100, onFlush: cancel}
	if sent, ended := send(ctx, context.Background(), w, nil, rec); sent || ended != "cut" || w.writes != 1 {
		t.Fatalf("sent %v ended %q after %d writes", sent, ended, w.writes)
	}
}

// A type the schema's inference cannot take is a programming error.
func TestSchemaOfPanicsOnATypeWithoutASchema(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	schemaOf[chan int]()
}

// An empty answer's header is flushed under its lease, before send says it
// was sent (#131's second review, finding 1), never left for the server to
// write once the handler returned.
func TestAnEmptyAnswerIsFlushedUnderItsLease(t *testing.T) {
	rec := newRecorder(AnswerMax)
	rec.WriteHeader(http.StatusOK)
	w := httptest.NewRecorder()
	if sent, ended := send(context.Background(), context.Background(), w, nil, rec); !sent || ended != "" || !w.Flushed {
		t.Fatalf("sent %v ended %q flushed %v", sent, ended, w.Flushed)
	}
}
