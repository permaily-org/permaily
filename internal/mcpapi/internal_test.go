package mcpapi

import (
	"context"
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
		_, _, e2 := h.searchMail(ctx, req, contracts.VendorSearch{})
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

// Mail for an answer the middleware did not hold for its lease (the SDK
// read the body otherwise than the middleware did), or a second lease for
// one answer, is never sent: the lease is ended and the tool answers
// "internal".
func TestMailForAnAnswerNotHeldIsNeverSent(t *testing.T) {
	for _, c := range []*call{
		{grant: testGrant(contracts.PartSender)},
		{grant: testGrant(contracts.PartSender), buffered: true, release: &connectionstest.Lease{}},
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
// body: without it the service is not asked and the answer is the step-up.
func TestTheToolChecksThePartItself(t *testing.T) {
	f := &connectionstest.Fake{}
	c := &call{grant: testGrant(contracts.PartSender, contracts.PartSubject), buffered: true}
	if _, _, err := testHandler(f).getAttachment(context.Background(), requestWith(c), AttachmentID{ID: "x"}); err == nil {
		t.Fatal("answered")
	}
	if _, stepUp, _, _ := c.taken(); !slices.Equal(stepUp, []contracts.Part{contracts.PartAttachments}) || len(f.Calls()) != 0 {
		t.Fatalf("%v %v", stepUp, f.Calls())
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
		func() {
			defer func() {
				if p, _ := recover().(error); !errors.Is(p, http.ErrAbortHandler) {
					t.Fatalf("not aborted: %v", p)
				}
			}()
			h.answer(w, r, c, rec)
		}()
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
			if got := h.answer(w, httptest.NewRequest(http.MethodPost, "/mcp", nil), c, rec); got != "internal" || w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
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
	w := &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 1}
	if sent, cut := send(context.Background(), w, rec); sent || cut {
		t.Fatalf("sent %v cut %v", sent, cut)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 2, onWrite: cancel}
	if sent, cut := send(ctx, w, rec); sent || !cut {
		t.Fatalf("sent %v cut %v", sent, cut)
	}
	ctx, cancel = context.WithCancel(context.Background())
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 100, onWrite: cancel}
	if sent, cut := send(ctx, w, rec); sent || !cut || w.writes != 1 {
		t.Fatalf("sent %v cut %v after %d writes", sent, cut, w.writes)
	}
	w = &failingWriter{ResponseRecorder: httptest.NewRecorder(), failAfter: 100, failFlush: true}
	if sent, _ := send(context.Background(), w, rec); sent {
		t.Fatal("sent without a flush")
	}
}

type failingWriter struct {
	*httptest.ResponseRecorder
	failAfter int
	writes    int
	onWrite   func()
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
