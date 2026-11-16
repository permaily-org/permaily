package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// V45 (the third review of #131 and #144, finding 2 and ruling 2): a route
// that sends mail answers only on a connection the abort can close, the
// server's ConnContext set to httpapi.ConnContext. Without it, over HTTP/1.1
// too, it is refused before the token is checked, as over HTTP/2: the
// generic 503 with Retry-After, logged no_abort, the service not asked, so
// nothing is prepared or recorded. With it the same request answers.
func TestV45CWithoutTheConnectionsHookMailIsRefusedBeforeTheTokenIsChecked(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{"POST", "/v1/messages/search", `{}`},
		{"GET", "/v1/messages/mbx_1.1", ""},
		{"GET", "/v1/messages/mbx_1.1/attachments/0", ""},
	} {
		for _, hook := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/hook=%v", route.path, hook), func(t *testing.T) {
				f := &connectionstest.Fake{}
				grants(f, apiGrant)
				f.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
					return &contracts.VendorPage{}, newLease(), nil
				}
				f.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
					return &contracts.VendorMessage{ID: "mbx_1.1"}, newLease(), nil
				}
				f.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
					return &inbox.Attachment{Data: []byte("private_attachment_marker")}, newLease(), nil
				}
				var logs lockedBuffer
				srv := httptest.NewUnstartedServer(httpapi.VendorAPI(httpapi.ConnectionsConfig{
					Service: f, OAuth: testOAuth, Challenge: oauth.Challenge, Log: slog.New(slog.NewJSONHandler(&logs, nil)),
				}))
				if hook {
					srv.Config.ConnContext = httpapi.ConnContext
				}
				srv.Start()
				t.Cleanup(srv.Close)
				r, err := http.NewRequest(route.method, srv.URL+route.path, strings.NewReader(route.body))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+apiToken)
				resp, err := srv.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				srv.Close() // waits for the handler
				if hook {
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("%d %s", resp.StatusCode, body)
					}
					return
				}
				if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" || strings.Contains(string(body), "private_attachment_marker") {
					t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, body)
				}
				if calls := f.Calls(); len(calls) != 0 {
					t.Fatalf("the service was called: %v", calls)
				}
				if s := logs.String(); !strings.Contains(s, `"outcome":"no_abort"`) || strings.Contains(s, apiToken) {
					t.Fatalf("logged %s", s)
				}
			})
		}
	}
}

// V45 (the third review's finding 2, its probe): a cut while the server
// drains a request's unread body, which Go's HTTP/1.1 server does as the
// answer's header is first written, returns the handler at once. With the
// hook the abort closes the connection, which ends the read; without it the
// attachment is refused before anything starts (the test above), and the
// abort's own deadlines, the read's among them, end the read too
// (TestV45CTheAbortEndsARead).
func TestV45CACutWhileTheRequestsBodyIsDrainedReturnsAtOnce(t *testing.T) {
	for _, hook := range []bool{true, false} {
		t.Run(fmt.Sprintf("hook=%v", hook), func(t *testing.T) {
			l := newDrainLease()
			h := attachmentAPI([]byte("private_drain_marker"), l, io.Discard)
			done := make(chan struct{})
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); h.ServeHTTP(w, r) }))
			s.Config.ErrorLog = log.New(io.Discard, "", 0)
			if hook {
				s.Config.ConnContext = httpapi.ConnContext
			}
			ln := &drainListener{Listener: s.Listener, started: l.started, reading: make(chan struct{})}
			s.Listener = ln
			s.Start()
			t.Cleanup(s.Close)
			c, err := net.Dial("tcp", s.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			fmt.Fprintf(c, "GET /v1/messages/mbx_1.1/attachments/0 HTTP/1.1\r\nHost: api.permaily.test\r\nAuthorization: Bearer %s\r\nContent-Length: 8\r\n\r\n", apiToken)
			if !hook {
				wait(t, done, "the refusal")
				if started, finished, _, _ := l.State(); started || finished {
					t.Fatalf("started %v finished %v", started, finished)
				}
				return
			}
			wait(t, l.started, "Start")
			wait(t, ln.reading, "the request body's drain")
			l.Cut()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = c.Close()
				wait(t, done, "the handler after the client closed")
				t.Fatal("the cut did not end the drain: the handler returned only once the client closed its socket")
			}
			if _, finished, sent, _ := l.State(); !finished || sent {
				t.Fatalf("finished %v sent %v", finished, sent)
			}
		})
	}
}

// V45 (the third review's finding 2, the read): the abort sets the
// response's read deadline as well as its write deadline, so a read the
// response's writer makes (the drain of a body nobody read) cannot hold a
// cut, even with no connection to close. The transfer is driven without the
// hook, which the routes refuse, through a real HTTP/1.1 server.
func TestV45CTheAbortEndsARead(t *testing.T) {
	started := make(chan struct{})
	ctx, cut := context.WithCancel(context.Background())
	defer cut()
	done := make(chan struct{})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		close(started)
		httpapi.Transfer(ctx, r.Context(), w, nil, []byte("private_drain_marker"))
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	ln := &drainListener{Listener: s.Listener, started: started, reading: make(chan struct{})}
	s.Listener = ln
	s.Start()
	t.Cleanup(s.Close)
	c, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: api.permaily.test\r\nContent-Length: 8\r\n\r\n")
	wait(t, ln.reading, "the request body's drain")
	cut()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.Close()
		wait(t, done, "the handler after the client closed")
		t.Fatal("the abort did not end the drain's read")
	}
}

func wait(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

// drainLease is a lease that says when it started.
type drainLease struct {
	*connectionstest.Lease
	started chan struct{}
}

func newDrainLease() *drainLease {
	return &drainLease{Lease: &connectionstest.Lease{}, started: make(chan struct{})}
}

func (l *drainLease) Start(ctx context.Context) (context.Context, error) {
	c, err := l.Lease.Start(ctx)
	close(l.started)
	return c, err
}

// drainListener's connections say when the server first reads after
// started: the drain of the request's body.
type drainListener struct {
	net.Listener
	started <-chan struct{}
	reading chan struct{}
	once    sync.Once
}

type drainConn struct {
	net.Conn
	l *drainListener
}

func (l *drainListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &drainConn{c, l}, nil
}

func (c *drainConn) Read(b []byte) (int, error) {
	select {
	case <-c.l.started:
		c.l.once.Do(func() { close(c.l.reading) })
	default:
	}
	return c.Conn.Read(b)
}
