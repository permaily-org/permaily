package send

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
	"github.com/permaily-org/permaily/internal/webhook"
)

// gate is a dishonest host between the sender and the receiver: it relays
// the receiver's bytes as they come and the sender's a whole TLS record at a
// time. Once it relayed quota of the sender's encrypted records
// (application data, TLS's record type 23), it reads part bytes more of the
// next one (relayed too) and stops reading, as a host that stopped reading
// the vsock, until stopped; with cut, it closes both sides there instead.
// It decides before it reads a record, from the records it relayed whole.
type gate struct {
	client, server net.Conn
	quota, part    int
	cut            bool

	relayed int
	done    chan struct{}
	once    sync.Once
}

func newGate(client, server net.Conn, quota, part int, cut bool) *gate {
	g := &gate{client: client, server: server, quota: quota, part: part, cut: cut, done: make(chan struct{})}
	go func() { _, _ = io.Copy(client, server); g.close() }()
	go g.relay()
	return g
}

func (g *gate) close() {
	g.once.Do(func() {
		close(g.done)
		_ = g.client.Close()
		_ = g.server.Close()
	})
}

func (g *gate) relay() {
	for {
		if g.relayed == g.quota {
			if g.cut {
				g.close()
				return
			}
			if g.part > 0 {
				b := make([]byte, g.part)
				if _, err := io.ReadFull(g.client, b); err == nil {
					_, _ = g.server.Write(b)
				}
			}
			<-g.done
			return
		}
		head := make([]byte, 5)
		if _, err := io.ReadFull(g.client, head); err != nil {
			g.close()
			return
		}
		body := make([]byte, binary.BigEndian.Uint16(head[3:]))
		if _, err := io.ReadFull(g.client, body); err != nil {
			g.close()
			return
		}
		if _, err := g.server.Write(append(head, body...)); err != nil {
			g.close()
			return
		}
		if head[0] == 23 {
			g.relayed++
		}
	}
}

// opened is what a receiver behind a gate opened of a call.
type opened struct {
	bytes []byte
	err   error
}

// gated serves a call through a gate that relays the sender's encrypted
// records up to its preface and SETTINGS, its acknowledgement, and then
// records more (the request's), part bytes more, and stops reading or cuts
// (in TLS 1.3 the client's Finished is encrypted too, so one more). What
// the receiver's TLS opened after the preface is sent on out; stop ends a
// gate that stopped reading.
func gated(cert tls.Certificate, version uint16, records, part int, cut bool, out chan<- opened) (serve func(net.Conn), stop func()) {
	quota := 2 + records
	if version != tls.VersionTLS12 {
		quota++
	}
	gates := make(chan *gate, 1)
	serve = func(host net.Conn) {
		inner, server := net.Pipe()
		g := newGate(host, inner, quota, part, cut)
		gates <- g
		defer g.close()
		srv := tlsServer(server, cert, version)
		if _, err := clientStart(srv); err != nil {
			out <- opened{err: err}
			return
		}
		if err := serverSettings(srv); err != nil {
			out <- opened{err: err}
			return
		}
		out <- opened{bytes: readAll(srv)}
	}
	return serve, func() { (<-gates).close() }
}

// K18, re-review N1: a host that takes exactly the first whole TLS record of
// the request, then stops reading or cuts, with the largest requests: the
// design's largest notice and the largest request the sender frames, after
// the real HTTP/2 preface and SETTINGS, over TLS 1.2 and 1.3. The receiver
// opens all of the request or none of it, never a part; when it opened it,
// the call is not cut or too_late (closing cannot retract a whole record),
// and when the call says cut or too_late, it opened nothing.
func TestK18AHostThatTakesTheFirstRecordGetsTheWholeRequestOrNothing(t *testing.T) {
	roots, cert := pki(t, host)
	calls := map[string]func(*testing.T) webhook.Call{
		"the largest notice":  func(*testing.T) webhook.Call { return largestNotice() },
		"the largest request": func(t *testing.T) webhook.Call { return framedAt(t, webhook.RequestWriteBytesMax) },
	}
	for name, mk := range calls {
		for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			for _, cut := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s, TLS %x, cut %v", name, version, cut), func(t *testing.T) {
					c := mk(t)
					req, ok := frame(c)
					if !ok {
						t.Fatal("not framed")
					}
					c.WriteBy = time.Now().Add(300 * time.Millisecond)
					out := make(chan opened, 1)
					serve, stop := gated(cert, version, 1, 0, cut, out)
					d := pipeDialer(serve)
					s := New(d.dial, roots, time.Now)
					s.timeout = time.Second
					r := s.Attempt(context.Background(), c)
					stop()
					got := <-out
					d.wg.Wait()
					if got.err != nil {
						t.Fatal(got.err)
					}
					request := bytes.TrimPrefix(got.bytes, ackFrame)
					if len(request) == len(got.bytes) {
						t.Fatalf("the receiver did not open the acknowledgement first: % x", got.bytes[:min(len(got.bytes), 16)])
					}
					switch {
					case bytes.Equal(request, req.frames):
						if r.Handed != HandedWhole || r.Outcome == webhook.OutcomeCut || r.Outcome == webhook.OutcomeTooLate {
							t.Errorf("the receiver opened the whole request, the call says %+v", r)
						}
					case len(request) == 0:
						if r.Handed == HandedWhole {
							t.Errorf("the receiver opened nothing, the call says %+v", r)
						}
					default:
						t.Fatalf("the receiver opened %d bytes of a %d-byte request (a part of it); the call says %+v", len(request), len(req.frames), r)
					}
					// One record is the whole request: the host that took
					// the first record took it.
					if !bytes.Equal(request, req.frames) || r.Outcome != webhook.OutcomeNoAnswer {
						t.Errorf("want the whole request opened and no answer, got %d bytes and %+v", len(request), r)
					}
					d.allClosed(t)
				})
			}
		}
	}
}

// K18: a host that stops reading at the request's Write, past WriteBy: the
// connection is closed under the Write and the receiver opens nothing of
// it, whether the host read none of its record or part of it.
func TestK18AHostThatStopsReadingAtTheWriteGetsNothing(t *testing.T) {
	roots, cert := pki(t, host)
	for _, part := range []int{0, 1, 100} {
		t.Run(fmt.Sprint(part, " bytes of the record read"), func(t *testing.T) {
			c := largestNotice()
			c.WriteBy = time.Now().Add(200 * time.Millisecond)
			out := make(chan opened, 1)
			serve, stop := gated(cert, 0, 0, part, false, out)
			d := pipeDialer(serve)
			s := New(d.dial, roots, time.Now)
			s.timeout = 5 * time.Second
			start := time.Now()
			r := s.Attempt(context.Background(), c)
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("the call took %s past WriteBy", took)
			}
			stop()
			got := <-out
			d.wg.Wait()
			want := Result{Outcome: webhook.OutcomeTooLate, Handed: HandedPart}
			if part == 0 {
				want.Handed = HandedNothing
			}
			if r != want {
				t.Errorf("%+v, want %+v", r, want)
			}
			if !bytes.Equal(got.bytes, ackFrame) {
				t.Errorf("the receiver opened % x", got.bytes)
			}
			if n := s.Counts().HandedPart; (n == 1) != (part > 0) {
				t.Errorf("counted %d calls handed in part", n)
			}
			d.allClosed(t)
		})
	}
}

// K18: a handshake held past WriteBy is too_late, and the receiver sees no
// request.
func TestK18AHandshakeHeldPastWriteByIsTooLate(t *testing.T) {
	roots, _ := pki(t, host)
	var read int
	d := pipeDialer(func(c net.Conn) {
		b := make([]byte, 64<<10)
		for {
			n, err := c.Read(b)
			read += n
			if err != nil {
				return
			}
		}
	})
	s := New(d.dial, roots, time.Now)
	c := aCall(aNotice(300))
	c.WriteBy = time.Now().Add(150 * time.Millisecond)
	if r := s.Attempt(context.Background(), c); r != (Result{Outcome: webhook.OutcomeTooLate}) {
		t.Errorf("%+v", r)
	}
	d.wg.Wait()
	if read == 0 {
		t.Error("the hello was never sent")
	}
	d.allClosed(t)
	// WriteBy passed before the call: nothing is dialled.
	c.WriteBy = time.Now().Add(-time.Second)
	if r := s.Attempt(context.Background(), c); r.Outcome != webhook.OutcomeTooLate || d.dials() != 1 {
		t.Errorf("%+v after %d dials", r, d.dials())
	}
	c.WriteBy = time.Time{}
	if r := s.Attempt(context.Background(), c); r.Outcome != webhook.OutcomeTooLate || d.dials() != 1 {
		t.Errorf("no WriteBy: %+v after %d dials", r, d.dials())
	}
}

// receiver serves one call with the played certificate: the client's
// preface read, the server's SETTINGS written, then what it opens returned;
// before it writes its SETTINGS it runs between.
func receiver(cert tls.Certificate, between func(), settings []http2.Setting, out chan<- opened) func(net.Conn) {
	return func(raw net.Conn) {
		srv := tlsServer(raw, cert, 0)
		if _, err := clientStart(srv); err != nil {
			out <- opened{err: err}
			return
		}
		if between != nil {
			between()
		}
		if err := serverSettings(srv, settings...); err != nil {
			out <- opened{}
			return
		}
		out <- opened{bytes: readAll(srv)}
	}
}

// K18: a permit cut after the handshake and before the write is cut, and the
// receiver sees no request; so is one cut just as the Write is admitted (the
// cut and the admission are serialized), and one whose WriteBy passes then.
func TestK18APermitCutBeforeTheWriteWritesNothing(t *testing.T) {
	roots, cert := pki(t, host)

	t.Run("after the handshake", func(t *testing.T) {
		ctx, cut := context.WithCancelCause(context.Background())
		out := make(chan opened, 1)
		d := pipeDialer(receiver(cert, func() { cut(webhook.ErrCut) }, nil, out))
		r := New(d.dial, roots, time.Now).Attempt(ctx, aCall(aNotice(300)))
		got := <-out
		// The cut closes the connection as soon as its callback runs; the
		// acknowledgement may leave before that, the request never.
		if r != (Result{Outcome: webhook.OutcomeCut}) || len(bytes.TrimPrefix(got.bytes, ackFrame)) != 0 {
			t.Errorf("%+v; the receiver opened % x", r, got.bytes)
		}
		d.wg.Wait()
		d.allClosed(t)
	})

	t.Run("as the write is admitted", func(t *testing.T) {
		ctx, cut := context.WithCancelCause(context.Background())
		out := make(chan opened, 1)
		d := pipeDialer(receiver(cert, nil, nil, out))
		s := New(d.dial, roots, time.Now)
		s.beforeAdmit = func() { cut(webhook.ErrCut) }
		r := s.Attempt(ctx, aCall(aNotice(300)))
		got := <-out
		if r != (Result{Outcome: webhook.OutcomeCut}) || !bytes.Equal(got.bytes, ackFrame) {
			t.Errorf("%+v; the receiver opened % x", r, got.bytes)
		}
		d.wg.Wait()
		d.allClosed(t)
	})

	t.Run("WriteBy passing as the write is admitted", func(t *testing.T) {
		start := time.Now()
		var late sync.Mutex
		var skew time.Duration
		now := func() time.Time { late.Lock(); defer late.Unlock(); return time.Now().Add(skew) }
		out := make(chan opened, 1)
		d := pipeDialer(receiver(cert, nil, nil, out))
		s := New(d.dial, roots, now)
		s.beforeAdmit = func() { late.Lock(); skew = time.Hour; late.Unlock() }
		c := aCall(aNotice(300))
		c.WriteBy = start.Add(5 * time.Second)
		r := s.Attempt(context.Background(), c)
		got := <-out
		if r != (Result{Outcome: webhook.OutcomeTooLate}) || !bytes.Equal(got.bytes, ackFrame) {
			t.Errorf("%+v; the receiver opened % x", r, got.bytes)
		}
		d.wg.Wait()
		d.allClosed(t)
	})

	t.Run("before the call", func(t *testing.T) {
		ctx, cut := context.WithCancelCause(context.Background())
		cut(webhook.ErrCut)
		d := pipeDialer(func(net.Conn) {})
		if r := New(d.dial, roots, time.Now).Attempt(ctx, aCall(aNotice(300))); r.Outcome != webhook.OutcomeCut || d.dials() != 0 {
			t.Errorf("%+v after %d dials", r, d.dials())
		}
		// A context that ended at WriteBy is too_late.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if r := New(d.dial, roots, time.Now).Attempt(ctx, aCall(aNotice(300))); r.Outcome != webhook.OutcomeTooLate || d.dials() != 0 {
			t.Errorf("%+v after %d dials", r, d.dials())
		}
	})
}

// K18: a receiver whose stream window, or header list limit, is smaller
// than the request is refused and sees no HEADERS (a request split by flow
// control would leave its headers without its body). The connection's
// window starts at 65,535 bytes and only grows, past every body the design
// allows; the check is shown on it too.
func TestK18AReceiverThatCannotTakeTheWholeRequestIsRefused(t *testing.T) {
	roots, cert := pki(t, host)
	for name, s := range map[string]http2.Setting{
		"a small window":          {ID: http2.SettingInitialWindowSize, Val: 100},
		"a small header list":     {ID: http2.SettingMaxHeaderListSize, Val: 100},
		"a window one byte short": {ID: http2.SettingInitialWindowSize, Val: 299},
	} {
		t.Run(name, func(t *testing.T) {
			out := make(chan opened, 1)
			d := pipeDialer(receiver(cert, nil, []http2.Setting{s}, out))
			r := New(d.dial, roots, time.Now).Attempt(context.Background(), aCall(aNotice(300)))
			got := <-out
			if r != (Result{Outcome: webhook.OutcomeRefused}) || !bytes.Equal(got.bytes, ackFrame) {
				t.Errorf("%+v; the receiver opened % x", r, got.bytes)
			}
			d.wg.Wait()
			d.allClosed(t)
		})
	}
	req, _ := frame(aCall(aNotice(300)))
	if (peer{window: 300, connection: 299, list: req.list, streams: 1}).takes(req) || !(peer{window: 300, connection: 300, list: req.list, streams: 1}).takes(req) {
		t.Error("the windows are not both checked")
	}
	// A window that fits it: written.
	out := make(chan opened, 1)
	d := pipeDialer(receiver(cert, nil, []http2.Setting{{ID: http2.SettingInitialWindowSize, Val: 300}}, out))
	s := New(d.dial, roots, time.Now)
	s.timeout = 300 * time.Millisecond
	if r := s.Attempt(context.Background(), aCall(aNotice(300))); r.Handed != HandedWhole {
		t.Errorf("%+v", r)
	}
	<-out
	d.wg.Wait()
}

// K18: a revocation through the same enclave between Admit and the first
// write: the permit is cut and no request reaches the receiver; one before
// the permit starts gives no call at all.
func TestK18ARevocationBetweenAdmitAndTheWriteSendsNoRequest(t *testing.T) {
	roots, cert := pki(t, host)
	ctx := context.Background()
	fake := &webhookstest.Fake{}
	sub := webhooks.Subscription{
		Connection: "con_1", Account: "acc_1", ID: "sub_0123456789abcdefghijklmnop",
		ClientID: "https://" + host + "/oauth/client.json", URL: "https://" + host + "/hooks/permaily",
		NewMail: true, Mailboxes: []string{"mbx_1"}, MadeAt: time.Now(), State: contracts.WebhookOn,
	}
	fake.Make(sub)
	_, p, err := fake.Admit(ctx, sub.Connection)
	if err != nil || p == nil {
		t.Fatalf("no permit: %v", err)
	}
	call, err := p.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan opened, 1)
	d := pipeDialer(receiver(cert, func() { fake.Revoke(sub.Connection) }, nil, out))
	c := aCall(aNotice(300))
	c.WriteBy = p.WriteBy()
	r := New(d.dial, roots, time.Now).Attempt(call, c)
	p.Finish()
	got := <-out
	if r != (Result{Outcome: webhook.OutcomeCut}) || len(bytes.TrimPrefix(got.bytes, ackFrame)) != 0 {
		t.Errorf("%+v; the receiver opened % x", r, got.bytes)
	}
	d.wg.Wait()
	d.allClosed(t)

	// Revoked before the permit starts: it never starts, so nothing calls.
	fake.Make(webhooks.Subscription{Connection: "con_2", Account: "acc_1", ID: sub.ID, ClientID: sub.ClientID, URL: sub.URL, NewMail: true, MadeAt: time.Now()})
	_, p, err = fake.Admit(ctx, "con_2")
	if err != nil || p == nil {
		t.Fatalf("no permit: %v", err)
	}
	fake.Revoke("con_2")
	if _, err := p.Start(ctx); err == nil {
		t.Error("a permit revoked before it started started")
	}
}

// K18: a permit cut, or WriteBy passing, while the host is dialled: nothing
// is written, and a stream the dial gives then is closed at once.
func TestK18AStopDuringTheDialWritesNothing(t *testing.T) {
	roots, _ := pki(t, host)
	ctx, cut := context.WithCancelCause(context.Background())
	var given *tracked
	d := &dialer{connect: func(context.Context) (net.Conn, error) {
		cut(webhook.ErrCut)
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		given = &tracked{Conn: client}
		return given, nil
	}}
	if r := New(d.dial, roots, time.Now).Attempt(ctx, aCall(aNotice(300))); r != (Result{Outcome: webhook.OutcomeCut}) || !given.closed.Load() {
		t.Errorf("%+v, closed %v", r, given.closed.Load())
	}
	// Whether the cut's callback ran before the stream was given or after:
	// a guard already stopped closes the stream it is given.
	g := &guard{}
	g.stop(webhook.OutcomeCut)
	client, server := net.Pipe()
	defer server.Close()
	stream := &tracked{Conn: client}
	if _, ok := g.attach(stream); ok || !stream.closed.Load() || g.stopped() != webhook.OutcomeCut {
		t.Errorf("a stopped guard took a stream: closed %v", stream.closed.Load())
	}

	slow := &dialer{connect: func(ctx context.Context) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	c := aCall(aNotice(300))
	c.WriteBy = time.Now().Add(50 * time.Millisecond)
	if r := New(slow.dial, roots, time.Now).Attempt(context.Background(), c); r != (Result{Outcome: webhook.OutcomeTooLate}) {
		t.Errorf("a dial past WriteBy: %+v", r)
	}
}

// K18, review of #186 finding 1: a receiver that explicitly advertises zero
// for SETTINGS_MAX_HEADER_LIST_SIZE (no header list fits) or for
// SETTINGS_MAX_CONCURRENT_STREAMS (no stream may be opened) is refused and
// sees no HEADERS: zero is a limit, not the absence of one.
func TestK18ExplicitZeroPeerSettingsAreHonored(t *testing.T) {
	roots, cert := pki(t, host)
	for name, s := range map[string]http2.Setting{
		"no header list":        {ID: http2.SettingMaxHeaderListSize, Val: 0},
		"no concurrent streams": {ID: http2.SettingMaxConcurrentStreams, Val: 0},
	} {
		t.Run(name, func(t *testing.T) {
			out := make(chan opened, 1)
			d := pipeDialer(receiver(cert, nil, []http2.Setting{s}, out))
			s := New(d.dial, roots, time.Now)
			s.timeout = time.Second
			r := s.Attempt(context.Background(), aCall(aNotice(300)))
			got := <-out
			if r != (Result{Outcome: webhook.OutcomeRefused}) || !bytes.Equal(got.bytes, ackFrame) {
				t.Errorf("%+v; the receiver opened %d bytes", r, len(got.bytes))
			}
			d.wg.Wait()
			d.allClosed(t)
		})
	}
	// One concurrent stream, and a header list limit the request fits
	// exactly: written.
	req, _ := frame(aCall(aNotice(300)))
	out := make(chan opened, 1)
	d := pipeDialer(receiver(cert, nil, []http2.Setting{
		{ID: http2.SettingMaxConcurrentStreams, Val: 1}, {ID: http2.SettingMaxHeaderListSize, Val: req.list},
	}, out))
	s := New(d.dial, roots, time.Now)
	s.timeout = 300 * time.Millisecond
	if r := s.Attempt(context.Background(), aCall(aNotice(300))); r.Handed != HandedWhole {
		t.Errorf("%+v", r)
	}
	<-out
	d.wg.Wait()
}
