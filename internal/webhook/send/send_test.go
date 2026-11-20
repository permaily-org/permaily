package send

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
)

// served is a played receiver speaking Go's own HTTP/2 server behind TLS
// with the played certificate, and a sender that dials it whatever the host.
func served(t *testing.T, h http.Handler, protos ...string) (*Sender, *dialer) {
	t.Helper()
	roots, cert := pki(t, host)
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	if protos == nil {
		ts.EnableHTTP2 = true
		protos = []string{"h2"}
	}
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: protos}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	d := tcpDialer(ts.Listener.Addr().String())
	return New(d.dial, roots, time.Now), d
}

// K16: a call is a POST of the URL with the three headers and
// application/json, and nothing else: no cookie, credential, Referer,
// Accept-Encoding or User-Agent; its HEADERS and DATA leave in one TLS
// record, the receiver's TLS opening both in one record and nothing else
// in it, for a small notice, the design's largest and the largest request
// the sender frames (re-review N1: Go's default record sizing would split
// the last).
func TestK16ACallIsOnePOSTInOneTLSRecord(t *testing.T) {
	roots, cert := pki(t, host)
	calls := map[string]webhook.Call{
		"a notice":            aCall(aNotice(300)),
		"the largest notice":  largestNotice(),
		"the largest request": framedAt(t, webhook.RequestWriteBytesMax),
	}
	for name, c := range calls {
		for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			t.Run(name+", TLS "+strconv.FormatUint(uint64(version), 16), func(t *testing.T) {
				req, ok := frame(c)
				if !ok {
					t.Fatal("not framed")
				}
				type seen struct {
					record []byte
					fields []hpack.HeaderField
					err    error
				}
				out := make(chan seen, 1)
				d := pipeDialer(func(raw net.Conn) {
					srv := tlsServer(raw, cert, version)
					if _, err := clientStart(srv); err != nil {
						out <- seen{err: err}
						return
					}
					if err := serverSettings(srv); err != nil {
						out <- seen{err: err}
						return
					}
					buf := make([]byte, 64<<10)
					// One Read opens one record: the acknowledgement, then
					// the request.
					n, err := srv.Read(buf)
					if err != nil || !bytes.Equal(buf[:n], ackFrame) {
						out <- seen{err: errors.New("no acknowledgement in a record of its own")}
						return
					}
					n, err = srv.Read(buf)
					if err != nil {
						out <- seen{err: err}
						return
					}
					record := append([]byte(nil), buf[:n]...)
					fr := http2.NewFramer(nil, bytes.NewReader(record))
					fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
					f, err := fr.ReadFrame()
					if err != nil {
						out <- seen{err: err}
						return
					}
					mh, _ := f.(*http2.MetaHeadersFrame)
					_, _ = srv.Write(status("204"))
					if mh == nil {
						out <- seen{record: record}
						return
					}
					out <- seen{record: record, fields: mh.Fields}
				})
				r := New(d.dial, roots, time.Now).Attempt(context.Background(), c)
				got := <-out
				d.wg.Wait()
				if got.err != nil {
					t.Fatal(got.err)
				}
				if r != (Result{Outcome: webhook.OutcomeSent, Handed: HandedWhole}) {
					t.Errorf("%+v", r)
				}
				if !bytes.Equal(got.record, req.frames) {
					t.Fatalf("the record opened %d bytes, the request is %d", len(got.record), len(req.frames))
				}
				typ, flags, length := frameOf(got.record)
				data := got.record[9+length:]
				dtyp, dflags, dlength := frameOf(data)
				if typ != http2.FrameHeaders || !flags.Has(http2.FlagHeadersEndHeaders) || flags.Has(http2.FlagHeadersEndStream) ||
					dtyp != http2.FrameData || !dflags.Has(http2.FlagDataEndStream) || dlength != len(c.Body) || !bytes.Equal(data[9:], c.Body) {
					t.Errorf("frames %v %v, %v %v %d", typ, flags, dtyp, dflags, dlength)
				}
				u := c.URL[len("https://"+host):]
				want := []hpack.HeaderField{
					{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: host},
					{Name: ":path", Value: u}, {Name: "content-type", Value: "application/json"},
					{Name: "content-length", Value: strconv.Itoa(len(c.Body))}, {Name: contracts.WebhookHeaderID, Value: c.ID},
					{Name: contracts.WebhookHeaderTimestamp, Value: c.Timestamp}, {Name: contracts.WebhookHeaderSignature, Value: c.Signature},
				}
				if !slices.EqualFunc(got.fields, want, func(a, b hpack.HeaderField) bool { return a.Name == b.Name && a.Value == b.Value }) {
					t.Errorf("headers %v", got.fields)
				}
				d.allClosed(t)
			})
		}
	}
}

// K16: the TLS of a call: HTTP/2 only (a receiver of HTTP/1.1 alone is
// refused, whether it says so or negotiates nothing), TLS 1.2 or later,
// records of full size, checked against the image's roots (a certificate
// for another name, or self-signed, refused); nothing written to any of
// them.
func TestK16OnlyHTTP2OverVerifiedTLS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for name, protos := range map[string][]string{"HTTP/1.1 alone": {"http/1.1"}, "no ALPN": {}} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			s, d := served(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); ok(w, r) }), protos...)
			if o := s.Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeRefused || calls.Load() != 0 {
				t.Errorf("%s, %d calls", o, calls.Load())
			}
			d.allClosed(t)
		})
	}

	_, cert := pki(t, "other.example")
	_, mine := pki(t, host)
	roots, _ := pki(t, host)
	for name, c := range map[string]tls.Certificate{"another name": cert, "another authority": mine} {
		t.Run(name, func(t *testing.T) {
			d := tcpServe(t, func(raw net.Conn) {
				_ = tlsServer(raw, c, 0).Handshake()
			})
			if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeRefused {
				t.Errorf("%s", o)
			}
			d.allClosed(t)
		})
	}

	t.Run("not TLS", func(t *testing.T) {
		d := tcpServe(t, func(raw net.Conn) {
			_, _ = raw.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			_, _ = io.Copy(io.Discard, raw)
		})
		if o := New(d.dial, nil, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeRefused {
			t.Errorf("%s", o)
		}
	})

	cfg := New(nil, nil, nil).config(host)
	if cfg.MinVersion != tls.VersionTLS12 || !cfg.DynamicRecordSizingDisabled || cfg.InsecureSkipVerify || !slices.Equal(cfg.NextProtos, []string{"h2"}) || cfg.ServerName != host {
		t.Errorf("TLS %+v", cfg)
	}
	if cfg.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: "http/1.1"}) == nil || cfg.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: "h2"}) != nil {
		t.Error("ALPN not checked")
	}
}

// K16: what the design does not allow is refused before anything is dialled:
// a URL that is not https on a DNS name with no port, user or fragment; a
// body empty or over 2 KiB; headers that are not printable ASCII; a request
// framed past webhook.RequestWriteBytesMax by a byte (the largest framed is
// written, TestK16ACallIsOnePOSTInOneTLSRecord).
func TestK16WhatTheDesignDoesNotAllowIsRefusedBeforeTheDial(t *testing.T) {
	bad := map[string]func(*webhook.Call){
		"http":              func(c *webhook.Call) { c.URL = "http://" + host + "/h" },
		"a port":            func(c *webhook.Call) { c.URL = "https://" + host + ":443/h" },
		"a user":            func(c *webhook.Call) { c.URL = "https://u@" + host + "/h" },
		"a fragment":        func(c *webhook.Call) { c.URL = "https://" + host + "/h#f" },
		"an address":        func(c *webhook.Call) { c.URL = "https://[::1]/h" },
		"an upper case":     func(c *webhook.Call) { c.URL = "https://Trip-App.com/h" },
		"no host":           func(c *webhook.Call) { c.URL = "https:///h" },
		"a long host":       func(c *webhook.Call) { c.URL = "https://" + strings.Repeat("a.", 127) + "com/h" },
		"not a URL":         func(c *webhook.Call) { c.URL = "https://%zz/" },
		"an opaque URL":     func(c *webhook.Call) { c.URL = "https:" + host },
		"no body":           func(c *webhook.Call) { c.Body = nil },
		"a body over 2 KiB": func(c *webhook.Call) { c.Body = aNotice(webhook.NoticeBytesMax + 1) },
		"a new line":        func(c *webhook.Call) { c.ID += "\r\nCookie: a" },
		"no identifier":     func(c *webhook.Call) { c.ID = "" },
		"a spaced id":       func(c *webhook.Call) { c.ID = "a b" },
		"a leading space":   func(c *webhook.Call) { c.Signature = " " + c.Signature },
		"a trailing space":  func(c *webhook.Call) { c.Signature += " " },
		"a control":         func(c *webhook.Call) { c.Timestamp = "1\x00" },
		"not ASCII":         func(c *webhook.Call) { c.Signature = "v2a,é" },
		"a byte too large":  func(c *webhook.Call) { *c = framedAt(t, webhook.RequestWriteBytesMax+1) },
		"a header block over a frame": func(c *webhook.Call) {
			c.Signature = "v2a," + strings.Repeat("X", frameMin)
		},
	}
	for name, change := range bad {
		d := pipeDialer(func(net.Conn) {})
		c := aCall(aNotice(300))
		change(&c)
		if o := New(d.dial, nil, time.Now).Send(context.Background(), c); o != webhook.OutcomeRefused || d.dials() != 0 {
			t.Errorf("%s: %s after %d dials", name, o, d.dials())
		}
	}
	if _, ok := frame(framedAt(t, webhook.RequestWriteBytesMax)); !ok {
		t.Error("the largest request is refused")
	}
	if n := framedLen(largestNotice()); n > 4<<10 {
		t.Errorf("the largest notice frames to %d bytes", n)
	}
}

// K17: outcomes by status: 200 and 204 sent, 410 gone, 301, 302, 307 and
// 308 redirect and never followed (to the instance metadata address or
// localhost), 400, 404, 429 and 500 status; an interim 103 then the final
// status; one request per call and no connection left open.
func TestK17OutcomesByStatusAndNoRedirectFollowed(t *testing.T) {
	cases := map[int]webhook.Outcome{
		200: webhook.OutcomeSent, 204: webhook.OutcomeSent, 410: webhook.OutcomeGone,
		301: webhook.OutcomeRedirect, 302: webhook.OutcomeRedirect, 307: webhook.OutcomeRedirect, 308: webhook.OutcomeRedirect,
		400: webhook.OutcomeStatus, 404: webhook.OutcomeStatus, 429: webhook.OutcomeStatus, 500: webhook.OutcomeStatus,
	}
	for code, want := range cases {
		for _, to := range []string{"https://169.254.169.254/latest/meta-data/", "https://localhost/"} {
			var calls atomic.Int32
			s, d := served(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.ProtoMajor != 2 || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
					r.Header.Get("Referer") != "" || r.Header.Get("Accept-Encoding") != "" || r.Header.Get("Content-Type") != "application/json" {
					w.WriteHeader(599)
					return
				}
				w.Header().Set("Location", to)
				if code == 200 {
					w.WriteHeader(http.StatusEarlyHints)
				}
				w.WriteHeader(code)
			}))
			if o := s.Send(context.Background(), aCall(aNotice(300))); o != want || calls.Load() != 1 || d.dials() != 1 {
				t.Errorf("%d to %s: %s, %d calls, %d dials", code, to, o, calls.Load(), d.dials())
			}
			d.allClosed(t)
		}
	}
}

// K17: no answer: slow headers past the attempt's ten seconds, a stream
// reset, a connection reset, a refused dial; and a receiver that goes away
// or stops the connection before answering.
func TestK17NoAnswer(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	t.Run("slow headers", func(t *testing.T) {
		s, d := served(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		s.timeout = 300 * time.Millisecond
		start := time.Now()
		if o := s.Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s", o)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("took %s", took)
		}
		d.allClosed(t)
		if New(nil, nil, nil).timeout != webhook.AttemptTimeout {
			t.Error("the attempt is not bounded by webhook.AttemptTimeout")
		}
	})
	t.Run("a stream reset", func(t *testing.T) {
		s, d := served(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
		if o := s.Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s", o)
		}
		d.allClosed(t)
	})
	t.Run("a refused dial", func(t *testing.T) {
		d := &dialer{connect: func(context.Context) (net.Conn, error) { return nil, syscall.ECONNREFUSED }}
		if o := New(d.dial, nil, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s", o)
		}
	})
	roots, cert := pki(t, host)
	ends := map[string]func(*tls.Conn){
		"closed after the request":    func(*tls.Conn) {},
		"a GOAWAY before the request": func(srv *tls.Conn) { _ = http2.NewFramer(srv, nil).WriteGoAway(0, http2.ErrCodeRefusedStream, nil) },
		"a reset stream": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WriteRSTStream(1, http2.ErrCodeInternal)
		},
		"data before headers": func(srv *tls.Conn) { _ = http2.NewFramer(srv, nil).WriteData(1, true, []byte("x")) },
		"a push": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WritePushPromise(http2.PushPromiseParam{StreamID: 1, PromiseID: 2, BlockFragment: []byte{0x82}, EndHeaders: true})
		},
		"headers of another stream": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: []byte{0x88}, EndHeaders: true})
		},
		"a continuation of another stream": func(srv *tls.Conn) {
			fr := http2.NewFramer(srv, nil)
			_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: []byte{0x88}})
			_ = fr.WriteContinuation(3, true, nil)
		},
		"a status of 101": func(srv *tls.Conn) { _, _ = srv.Write(status("101")) },
		"a status of 99":  func(srv *tls.Conn) { _, _ = srv.Write(status("099")) },
		"two statuses": func(srv *tls.Conn) {
			_, _ = srv.Write(answerFrames(hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: ":status", Value: "200"}))
		},
		"no status": func(srv *tls.Conn) { _, _ = srv.Write(answerFrames(hpack.HeaderField{Name: "x", Value: "y"})) },
		"a broken block": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: []byte{0xff}, EndHeaders: true})
		},
		"a frame too big": func(srv *tls.Conn) { _ = http2.NewFramer(srv, nil).WriteData(1, false, make([]byte, frameMin+1)) },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			d := pipeDialer(func(raw net.Conn) {
				srv := tlsServer(raw, cert, 0)
				if _, err := clientStart(srv); err != nil {
					return
				}
				_ = serverSettings(srv)
				buf := make([]byte, 64<<10)
				_, _ = srv.Read(buf) // the acknowledgement
				_, _ = srv.Read(buf) // the request
				end(srv)
			})
			s := New(d.dial, roots, time.Now)
			s.timeout = time.Second
			if r := s.Attempt(context.Background(), aCall(aNotice(300))); r.Outcome != webhook.OutcomeNoAnswer {
				t.Errorf("%+v", r)
			}
			d.wg.Wait()
			d.allClosed(t)
		})
	}
	// A GOAWAY that says the request was taken waits for its answer.
	d := pipeDialer(func(raw net.Conn) {
		srv := tlsServer(raw, cert, 0)
		if _, err := clientStart(srv); err != nil {
			return
		}
		_ = serverSettings(srv)
		buf := make([]byte, 64<<10)
		_, _ = srv.Read(buf)
		_, _ = srv.Read(buf)
		fr := http2.NewFramer(srv, nil)
		_ = fr.WriteGoAway(1, http2.ErrCodeNo, nil)
		_ = fr.WritePing(false, [8]byte{})
		_ = fr.WriteWindowUpdate(0, 100)
		_, _ = srv.Write(status("200"))
	})
	if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeSent {
		t.Errorf("after a graceful GOAWAY: %s", o)
	}
	d.wg.Wait()
}

// K17: an answer of 1 MiB is not read (its status decides; the window the
// sender gives is webhook.AnswerBytesMax) and its connection is closed; a
// header block of 1 MiB is read to webhook.ResponseHeaderBytesMax, then the
// connection closed, the call no_answer.
func TestK17AnswersAreReadOnlyWithinTheirBounds(t *testing.T) {
	roots, cert := pki(t, host)
	// wrote counts what a scripted receiver got to write before the sender
	// closed the connection.
	type wrote struct {
		n   int
		err error
	}
	script := func(answer func(*tls.Conn) (int, error), out chan<- wrote) func(net.Conn) {
		return func(raw net.Conn) {
			srv := tlsServer(raw, cert, 0)
			settings, err := clientStart(srv)
			if err != nil {
				out <- wrote{err: err}
				return
			}
			want := []http2.Setting{
				{ID: http2.SettingEnablePush, Val: 0}, {ID: http2.SettingMaxConcurrentStreams, Val: 0},
				{ID: http2.SettingInitialWindowSize, Val: webhook.AnswerBytesMax}, {ID: http2.SettingMaxHeaderListSize, Val: webhook.ResponseHeaderBytesMax},
			}
			if !slices.Equal(settings, want) {
				out <- wrote{err: errors.New("the client's SETTINGS are not the bounds")}
				return
			}
			_ = serverSettings(srv)
			buf := make([]byte, 64<<10)
			_, _ = srv.Read(buf)
			_, _ = srv.Read(buf)
			n, err := answer(srv)
			out <- wrote{n: n, err: err}
		}
	}

	t.Run("a body of 1 MiB", func(t *testing.T) {
		out := make(chan wrote, 1)
		d := pipeDialer(script(func(srv *tls.Conn) (int, error) {
			fr := http2.NewFramer(srv, nil)
			var block bytes.Buffer
			_ = hpack.NewEncoder(&block).WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
			if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true}); err != nil {
				return 0, err
			}
			sent := 0
			for sent < 1<<20 {
				if err := fr.WriteData(1, false, make([]byte, 16<<10)); err != nil {
					return sent, err
				}
				sent += 16 << 10
			}
			return sent, nil
		}, out))
		if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeSent {
			t.Errorf("%s", o)
		}
		got := <-out
		if got.err == nil || got.n >= 1<<20 {
			t.Errorf("the receiver wrote %d bytes of its answer's body (%v)", got.n, got.err)
		}
		d.wg.Wait()
		d.allClosed(t)
	})

	t.Run("a header block of 1 MiB", func(t *testing.T) {
		out := make(chan wrote, 1)
		d := pipeDialer(script(func(srv *tls.Conn) (int, error) {
			fr := http2.NewFramer(srv, nil)
			chunk := bytes.Repeat([]byte{0x40, 0x01, 'x', 0x7f, 0x00}, 800) // 4,000 bytes of literals
			if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: chunk}); err != nil {
				return 0, err
			}
			sent := len(chunk)
			for sent < 1<<20 {
				if err := fr.WriteContinuation(1, false, chunk); err != nil {
					return sent, err
				}
				sent += len(chunk)
			}
			return sent, fr.WriteContinuation(1, true, nil)
		}, out))
		if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s", o)
		}
		got := <-out
		if got.err == nil || got.n > 64<<10 {
			t.Errorf("the receiver wrote %d bytes of its header block (%v)", got.n, got.err)
		}
		d.wg.Wait()
		d.allClosed(t)
	})

	t.Run("headers that decode past the bound", func(t *testing.T) {
		out := make(chan wrote, 1)
		d := pipeDialer(script(func(srv *tls.Conn) (int, error) {
			// One entry of 4 KiB in the dynamic table, then indexed eight
			// times: a small block that decodes to 32 KiB.
			var block bytes.Buffer
			enc := hpack.NewEncoder(&block)
			_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
			_ = enc.WriteField(hpack.HeaderField{Name: "x", Value: strings.Repeat("y", 4000)})
			for range 8 {
				_ = enc.WriteField(hpack.HeaderField{Name: "x", Value: strings.Repeat("y", 4000)})
			}
			return 0, http2.NewFramer(srv, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: true})
		}, out))
		if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s", o)
		}
		<-out
		d.wg.Wait()
	})
}

// K20: nothing a receiver sends reaches a log: unsolicited bytes on an idle
// connection, a header of 1 MiB, a malformed status, frames of every kind;
// the package logs nothing at all and uses no net/http transport (it has
// no log, fmt or net/http to log or follow anything with).
func TestK20NothingAReceiverSendsReachesALog(t *testing.T) {
	var logged bytes.Buffer
	var mu sync.Mutex
	locked := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logged.Write(p) })
	oldLog, oldSlog := log.Writer(), slog.Default()
	log.SetOutput(locked)
	slog.SetDefault(slog.New(slog.NewTextHandler(locked, nil)))
	t.Cleanup(func() { log.SetOutput(oldLog); slog.SetDefault(oldSlog) })

	roots, cert := pki(t, host)
	hostile := map[string]func(*tls.Conn){
		"unsolicited bytes before SETTINGS": func(srv *tls.Conn) {
			_, _ = srv.Write([]byte("HTTP/1.1 200 OK\r\nX-Secret: hunter2\r\n\r\nsecret body"))
		},
		"unsolicited bytes after the request": func(srv *tls.Conn) {
			_ = serverSettings(srv)
			buf := make([]byte, 64<<10)
			_, _ = srv.Read(buf)
			_, _ = srv.Read(buf)
			_, _ = srv.Write([]byte("secret: hunter2 hunter2 hunter2 hunter2"))
		},
		"a malformed status": func(srv *tls.Conn) {
			_ = serverSettings(srv)
			buf := make([]byte, 64<<10)
			_, _ = srv.Read(buf)
			_, _ = srv.Read(buf)
			_, _ = srv.Write(status("hunter2"))
		},
		"a header of 1 MiB": func(srv *tls.Conn) {
			_ = serverSettings(srv)
			buf := make([]byte, 64<<10)
			_, _ = srv.Read(buf)
			_, _ = srv.Read(buf)
			_, _ = srv.Write(answerFrames(hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "x-secret", Value: strings.Repeat("hunter2", 150000)}))
		},
		"settings that are not": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WriteRawFrame(http2.FrameSettings, 0, 0, []byte("hunter2"))
		},
		"an invalid setting": func(srv *tls.Conn) {
			_ = serverSettings(srv, http2.Setting{ID: http2.SettingEnablePush, Val: 7})
		},
		"an acknowledgement first": func(srv *tls.Conn) { _ = http2.NewFramer(srv, nil).WriteSettingsAck() },
		"a ping first": func(srv *tls.Conn) {
			_ = http2.NewFramer(srv, nil).WritePing(false, [8]byte{'h', 'u', 'n', 't', 'e', 'r', '2'})
		},
	}
	for name, do := range hostile {
		d := pipeDialer(func(raw net.Conn) {
			srv := tlsServer(raw, cert, 0)
			if _, err := clientStart(srv); err != nil {
				return
			}
			do(srv)
			_, _ = io.Copy(io.Discard, srv)
		})
		s := New(d.dial, roots, time.Now)
		s.timeout = time.Second
		if o := s.Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s: %s", name, o)
		}
		d.wg.Wait()
		d.allClosed(t)
	}
	// Unknown frames and pings around an answer are ignored.
	d := pipeDialer(func(raw net.Conn) {
		srv := tlsServer(raw, cert, 0)
		if _, err := clientStart(srv); err != nil {
			return
		}
		_ = serverSettings(srv)
		buf := make([]byte, 64<<10)
		_, _ = srv.Read(buf)
		_, _ = srv.Read(buf)
		fr := http2.NewFramer(srv, nil)
		_ = fr.WriteRawFrame(0xfa, 0, 0, []byte("hunter2"))
		_ = fr.WritePing(false, [8]byte{})
		_ = fr.WriteSettings()
		_, _ = srv.Write(status("200"))
	})
	if o := New(d.dial, roots, time.Now).Send(context.Background(), aCall(aNotice(300))); o != webhook.OutcomeSent {
		t.Errorf("around frames of other kinds: %s", o)
	}
	d.wg.Wait()
	mu.Lock()
	if logged.Len() != 0 {
		t.Errorf("logged: %q", logged.String())
	}
	mu.Unlock()

	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			switch p, _ := strconv.Unquote(imp.Path.Value); p {
			case "log", "log/slog", "fmt", "net/http", "os":
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
	if _, err := os.Stat("send.go"); err != nil {
		t.Fatal("the package's files were not found")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// K22: the outcomes are counted by fixed key, every key at zero from the
// start, and the calls that handed part of their record apart.
func TestK22OutcomesAreCountedByFixedKey(t *testing.T) {
	s, _ := served(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(contracts.WebhookHeaderID) == "ntc_gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	got := s.Counts()
	if len(got.Outcomes) != 8 || got.HandedPart != 0 {
		t.Fatalf("%+v", got)
	}
	for _, o := range Outcomes {
		if n, ok := got.Outcomes[o]; !ok || n != 0 {
			t.Errorf("%s: %d, %v", o, n, ok)
		}
		if o != webhook.OutcomeSent && o != webhook.OutcomeGone && o.Failure() == "" && o != webhook.OutcomeCut {
			t.Errorf("%s is shown as no failure", o)
		}
	}
	c := aCall(aNotice(300))
	s.Send(context.Background(), c)
	s.Send(context.Background(), c)
	c.ID = "ntc_gone"
	s.Send(context.Background(), c)
	c.URL = "http://" + host + "/"
	s.Send(context.Background(), c)
	got = s.Counts()
	want := map[webhook.Outcome]uint64{webhook.OutcomeSent: 2, webhook.OutcomeGone: 1, webhook.OutcomeRefused: 1}
	for _, o := range Outcomes {
		if got.Outcomes[o] != want[o] {
			t.Errorf("%s: %d, want %d", o, got.Outcomes[o], want[o])
		}
	}
	// What Counts returns is a copy.
	got.Outcomes[webhook.OutcomeSent] = 99
	if s.Counts().Outcomes[webhook.OutcomeSent] != 2 {
		t.Error("the counts can be changed from outside")
	}
}
