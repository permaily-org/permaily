package mcpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

func attachmentCall(token string) request {
	return request{token: token, protocol: protocolCodex,
		body: `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_attachment","arguments":{"id":"mbx_A.msg_1","index":0}}}`}
}

// V50. Step-up: get_attachment without mail.attachments is HTTP 403
// insufficient_scope, naming in one challenge every scope the client then
// needs, before the SDK runs: the service is never asked.
func TestV50StepUpBeforeTheSDK(t *testing.T) {
	for _, c := range []struct {
		token, scope string
	}{
		{tokenMCP, "mail.sender mail.subject mail.body mail.attachments"},
		{tokenMCPNarrow, "mail.sender mail.attachments"},
	} {
		w := newWorld()
		rec := do(w.handler(), attachmentCall(c.token))
		want := `Bearer error="insufficient_scope", scope="` + c.scope + `", resource_metadata="` + metadataURL + `"`
		if rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != want {
			t.Fatalf("%d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		if slices.Contains(w.fake.Calls(), "Attachment") {
			t.Fatal("the service was asked")
		}
		// Before the SDK: a request the SDK itself would refuse (no Accept
		// header) is still the step-up.
		r := attachmentCall(c.token).http()
		r.Header.Del("Accept")
		rec = httptest.NewRecorder()
		w.handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("the SDK ran first: %d", rec.Code)
		}
	}
	// With the part, the attachment is answered.
	res := callTool(t, newWorld().handler(), attachmentCall(tokenMCPAttached))
	if res.IsError || len(res.Content) != 2 || res.Content[1].Resource == nil || string(res.Content[1].Resource.Blob) != "%PDF-" ||
		res.Content[1].Resource.MIMEType != "application/pdf" || res.Content[1].Resource.URI != "permaily://messages/mbx_A.msg_1/attachments/0" {
		t.Fatalf("%+v", res)
	}
}

// V50. The service saying the part is not approved (a token narrowed below
// what the check gave, a connection changed meanwhile) is the same step-up,
// and its lease is ended.
func TestV50TheServiceRefusingThePartIsAStepUp(t *testing.T) {
	w := newWorld()
	w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return nil, w.lease(), contracts.ErrPartNotApproved
	}
	rec := do(w.handler(), attachmentCall(tokenMCPAttached))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `scope="mail.sender mail.subject mail.body mail.attachments"`) {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if started, finished, _, _ := w.Leases()[0].State(); started || !finished {
		t.Fatal("the lease was not ended")
	}
}

// V50. A body over 1 MiB is refused before the SDK; one of 1 MiB is read; a
// batch is refused (one answer carries one tool's mail).
func TestV50ABodyOverOneMiBIsRefused(t *testing.T) {
	w := newWorld()
	h := w.handler()
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`
	exact := call + strings.Repeat(" ", mcpapi.BodyMax-len(call))
	if rec := do(h, request{token: tokenMCP, body: exact}); rec.Code != http.StatusOK {
		t.Fatalf("1 MiB: %d", rec.Code)
	}
	rec := do(h, request{token: tokenMCP, body: exact + " "})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB and a byte: %d", rec.Code)
	}
	rec = do(h, request{token: tokenMCP, body: "  [" + call + "," + call + "]"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a batch: %d", rec.Code)
	}
	if n := strings.Count(strings.Join(w.fake.Calls(), " "), "Read"); n != 1 {
		t.Fatalf("%d reads", n)
	}
	// A body that cannot be read is refused too.
	r := request{token: tokenMCP}.http()
	r.Body = io.NopCloser(io.MultiReader(strings.NewReader("{"), errReader{}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable body: %d", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the client went away") }

// V50. An attachment of 10 MiB is answered within 14 MiB, measured on the
// SDK's whole response: one embedded blob, the metadata alone repeated (the
// structured result and the text part). One byte more is refused by the
// adapter itself, and its lease ended.
func TestV50AnAttachmentOf10MiBIsAnsweredWithin14MiB(t *testing.T) {
	data := make([]byte, contracts.VendorAttachmentMax)
	_, _ = rand.Read(data)
	w := newWorld()
	name := "a-name-the-sender-wrote.bin"
	w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Name: name, Type: "application/x-made-up; q=1", Data: data}, w.lease(), nil
	}
	rec := do(w.handler(), attachmentCall(tokenMCPAttached))
	body := rec.Body.Bytes()
	if rec.Code != http.StatusOK || len(body) > mcpapi.AnswerMax {
		t.Fatalf("%d, %d bytes", rec.Code, len(body))
	}
	t.Logf("a 10 MiB attachment's whole answer: %d bytes (%.2f MiB)", len(body), float64(len(body))/(1<<20))
	if n := bytes.Count(body, []byte(base64.StdEncoding.EncodeToString(data[:3<<10]))); n != 1 {
		t.Fatalf("the blob %d times", n)
	}
	if n := bytes.Count(body, []byte(name)); n != 2 {
		t.Fatalf("the metadata %d times", n)
	}
	var r response
	_ = json.Unmarshal(body, &r)
	var res toolResult
	_ = json.Unmarshal(r.Result, &res)
	if len(res.Content) != 2 || !bytes.Equal(res.Content[1].Resource.Blob, data) || res.Content[1].Resource.MIMEType != "application/x-made-up" {
		t.Fatal("the blob is not the attachment")
	}
	var meta contracts.VendorAttachment
	if err := json.Unmarshal(res.StructuredContent, &meta); err != nil || meta != (contracts.VendorAttachment{Index: 0, Name: name, Type: "application/x-made-up; q=1", Size: len(data)}) {
		t.Fatalf("%v %+v", err, meta)
	}
	if n, sent := w.Leases()[0].Finishes(); n != 1 || !sent[0] {
		t.Fatalf("finished %d %v", n, sent)
	}

	big := append(data, 0)
	w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Name: name, Type: "%%%", Data: big}, w.lease(), nil
	}
	if e := errorOf(t, callTool(t, w.handler(), attachmentCall(tokenMCPAttached))); e.Error.Code != contracts.ErrAttachmentTooLarge.Code {
		t.Fatalf("%+v", e)
	}
	if started, finished, _, _ := w.Leases()[1].State(); started || !finished {
		t.Fatal("the lease was not ended")
	}
	// A type that is not a media type is sent as bytes.
	w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Name: name, Type: "not a type", Data: []byte("x")}, w.lease(), nil
	}
	if res := callTool(t, w.handler(), attachmentCall(tokenMCPAttached)); res.Content[1].Resource.MIMEType != "application/octet-stream" {
		t.Fatal(res.Content[1].Resource.MIMEType)
	}
}

// V50 and V45's terms, around the SDK's writer with connectionstest.Lease
// (N2): cut after the service returned and before the first byte, or the
// lease refusing to start, is a 503 with nothing of the answer; a whole
// answer is Finish(true) once.
func TestV50TheLeaseIsHonouredBeforeTheFirstByte(t *testing.T) {
	for _, c := range []struct {
		name  string
		lease func(*countingLease)
	}{
		{"cut after the service returned", func(l *countingLease) { l.Cut() }},
		{"too late to start", func(l *countingLease) { l.TooLate = true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			w.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
				l := w.lease()
				c.lease(l) // between the service's return and the first byte
				m := message
				return &m, l, nil
			}
			srv := hooked(w.handler())
			resp := post(t, srv.URL, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`))
			body := readAll(t, resp.Body)
			srv.Close() // waits for the handler
			if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" {
				t.Fatalf("%d %v", resp.StatusCode, resp.Header)
			}
			for _, s := range []string{"mbx_A", "billing", subject, "IGNORE", "invoice"} {
				if bytes.Contains(body, []byte(s)) {
					t.Fatalf("the answer leaked %q: %s", s, body)
				}
			}
			l := w.Leases()[0]
			started, _, sent, tooLate := l.State()
			if n, args := l.Finishes(); started || sent || tooLate != 1 || n != 1 || args[0] {
				t.Fatalf("started %v sent %v too late %d finished %d %v", started, sent, tooLate, n, args)
			}
		})
	}
	t.Run("a whole answer", func(t *testing.T) {
		w := newWorld()
		srv := hooked(w.handler())
		resp := post(t, srv.URL, toolCall(1, "search_mail", `{}`))
		body := readAll(t, resp.Body)
		srv.Close() // waits for the handler
		if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("pmk_next")) || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		l := w.Leases()[0]
		started, finished, sent, _ := l.State()
		if n, _ := l.Finishes(); !started || !finished || !sent || n != 1 {
			t.Fatalf("started %v finished %v sent %v, %d finishes", started, finished, sent, n)
		}
	})
}

// hooked is a started server with the connection's hook, as the
// composition sets it.
func hooked(h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnContext = mcpapi.ConnContext
	srv.Start()
	return srv
}

func post(t *testing.T, base string, q request) *http.Response {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(q.body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header = q.http().Header
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// V50 and V45's terms (N2, the owner's decision 14): a lease cut during a
// transfer whose client reads nothing (the write blocked) aborts the
// response: the blocked write returns, no byte leaves after the cut, the
// connection is closed short of the answer's length, and the lease is
// finished once, not sent.
func TestV50ACutDuringABlockedTransferAbortsIt(t *testing.T) {
	data := make([]byte, contracts.VendorAttachmentMax)
	_, _ = rand.Read(data)
	w := newWorld()
	w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Name: "a.bin", Type: "application/octet-stream", Data: data}, w.lease(), nil
	}
	srv := httptest.NewUnstartedServer(w.handler())
	srv.Config.ConnContext = mcpapi.ConnContext
	ln := &stallListener{Listener: srv.Listener, allow: 256 << 10, stalled: make(chan struct{})}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(ln.unblock) // first, so that a failure never leaves a handler blocked

	got := make(chan int, 1)
	go func() {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			got <- -1
			return
		}
		defer conn.Close()
		q := attachmentCall(tokenMCPAttached)
		fmt.Fprintf(conn, "POST /mcp HTTP/1.1\r\nHost: mcp.permaily.test\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMcp-Protocol-Version: %s\r\nContent-Length: %d\r\n\r\n%s", q.token, q.protocol, len(q.body), q.body)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			got <- -1
			return
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		if resp.ContentLength <= n {
			got <- -2 // the whole answer arrived
			return
		}
		got <- int(n)
	}()

	select {
	case <-ln.stalled:
	case <-time.After(10 * time.Second):
		t.Fatal("the transfer never blocked")
	}
	ln.cut()
	w.Leases()[0].Cut()
	select {
	case n := <-got:
		if n < 0 {
			t.Fatalf("the client got %d", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the response was not aborted")
	}
	srv.Close() // waits for the handler
	if after := ln.afterCut(); after != 0 {
		t.Fatalf("%d bytes left after the cut", after)
	}
	l := w.Leases()[0]
	started, finished, sent, _ := l.State()
	if n, args := l.Finishes(); !started || !finished || sent || n != 1 || args[0] {
		t.Fatalf("started %v finished %v sent %v, finished %d %v", started, finished, sent, n, args)
	}
}

// stallListener's connections write the first allow bytes, then block as a
// TCP connection whose peer reads nothing does, until their write deadline
// is set; it counts what is written after cut.
type stallListener struct {
	net.Listener
	allow   int
	stalled chan struct{}

	mu    sync.Mutex
	once  sync.Once
	isCut bool
	after int
	conns []*stallConn
}

func (l *stallListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	sc := &stallConn{Conn: c, l: l, deadline: make(chan struct{})}
	l.mu.Lock()
	l.conns = append(l.conns, sc)
	l.mu.Unlock()
	return sc, nil
}

// unblock expires every connection's writes.
func (l *stallListener) unblock() {
	l.mu.Lock()
	conns := append([]*stallConn(nil), l.conns...)
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.SetWriteDeadline(time.Now())
	}
}

func (l *stallListener) cut() {
	l.mu.Lock()
	l.isCut = true
	l.mu.Unlock()
}

func (l *stallListener) afterCutBytes(n int) {
	l.mu.Lock()
	if l.isCut {
		l.after += n
	}
	l.mu.Unlock()
}

func (l *stallListener) afterCut() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.after
}

type stallConn struct {
	net.Conn
	l       *stallListener
	written int

	mu        sync.Mutex
	deadline  chan struct{}
	isExpired bool
}

func (c *stallConn) expired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isExpired
}

func (c *stallConn) Write(p []byte) (int, error) {
	if c.expired() {
		return 0, os.ErrDeadlineExceeded
	}
	if room := c.l.allow - c.written; room < len(p) {
		if room > 0 {
			n, err := c.Conn.Write(p[:room])
			c.written += n
			c.l.afterCutBytes(n)
			if err != nil {
				return n, err
			}
		}
		c.l.once.Do(func() { close(c.l.stalled) })
		<-c.deadline // blocked, as on a peer that reads nothing
		return max(room, 0), os.ErrDeadlineExceeded
	}
	n, err := c.Conn.Write(p)
	c.written += n
	c.l.afterCutBytes(n)
	return n, err
}

func (c *stallConn) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		c.mu.Lock()
		if !c.isExpired {
			c.isExpired = true
			close(c.deadline)
		}
		c.mu.Unlock()
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *stallConn) SetDeadline(t time.Time) error {
	if err := c.SetWriteDeadline(t); err != nil {
		return err
	}
	return c.SetReadDeadline(t)
}

// connectionstest.ReleaseContract holds for the lease these tests drive the
// adapter with, so what they show is what a lease that keeps the contract
// does.
func TestTheTestsLeaseKeepsTheContract(t *testing.T) {
	connectionstest.ReleaseContract(t, func() (connections.Release, func()) {
		l := &countingLease{Lease: &connectionstest.Lease{}}
		return l, l.Cut
	})
}
