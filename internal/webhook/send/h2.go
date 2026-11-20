package send

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/url"
	"strconv"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
)

// HTTP/2's own numbers (RFC 9113).
const (
	// frameMin is the largest frame every peer takes (SETTINGS_MAX_FRAME_SIZE
	// cannot be less), and the largest this sender reads.
	frameMin = 16384
	// windowInitial is a connection's flow-control window before any
	// WINDOW_UPDATE, and a stream's before the peer's SETTINGS say otherwise.
	windowInitial = 65535
	// headerTable is the decoder's dynamic table, HTTP/2's default.
	headerTable = 4096
	// stream is the call's one stream.
	stream = 1
)

// request is a call framed: the host to dial and its HEADERS and DATA frames.
type request struct {
	host   string
	frames []byte
	body   int
	// list is its header list's size as HTTP/2 counts it, what the peer's
	// SETTINGS_MAX_HEADER_LIST_SIZE bounds.
	list uint32
}

// frame frames c: a POST of its URL exactly, with "Content-Type:
// application/json", its length and the three headers, nothing else (no
// cookie, credential, Referer, Accept-Encoding or User-Agent), in a HEADERS
// frame and one DATA frame with END_STREAM, at most
// webhook.RequestWriteBytesMax together. False for a call that is not one
// the design allows (not an https URL on a DNS name with no port, user or
// fragment; a body empty or over webhook.NoticeBytesMax; a header value that
// is not printable ASCII), which is refused before anything is dialled.
func frame(c webhook.Call) (request, bool) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Host != u.Hostname() || !dnsName(u.Host) {
		return request{}, false
	}
	if len(c.Body) == 0 || len(c.Body) > webhook.NoticeBytesMax ||
		!headerValue(c.ID, false) || !headerValue(c.Timestamp, false) || !headerValue(c.Signature, true) {
		return request{}, false
	}
	fields := []hpack.HeaderField{
		{Name: ":method", Value: "POST"},
		{Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: u.Host},
		{Name: ":path", Value: u.RequestURI()},
		{Name: "content-type", Value: "application/json"},
		{Name: "content-length", Value: strconv.Itoa(len(c.Body))},
		{Name: contracts.WebhookHeaderID, Value: c.ID},
		{Name: contracts.WebhookHeaderTimestamp, Value: c.Timestamp},
		{Name: contracts.WebhookHeaderSignature, Value: c.Signature},
	}
	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	// No dynamic table: every block decodes alone, whatever the peer's.
	enc.SetMaxDynamicTableSizeLimit(0)
	var list uint32
	for _, f := range fields {
		if err := enc.WriteField(f); err != nil {
			return request{}, false
		}
		list += f.Size()
	}
	if block.Len() > frameMin {
		return request{}, false
	}
	var out bytes.Buffer
	fr := http2.NewFramer(&out, nil)
	if fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true}) != nil ||
		fr.WriteData(stream, true, c.Body) != nil || out.Len() > webhook.RequestWriteBytesMax {
		return request{}, false
	}
	return request{host: u.Host, frames: out.Bytes(), body: len(c.Body), list: list}, true
}

// dnsName is a lower-case DNS name, as webhook.URL leaves a webhook's host.
func dnsName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// headerValue is a value of printable ASCII, spaces inside it only when
// spaced (a rotation's several signatures).
func headerValue(v string, spaced bool) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x21 || c > 0x7e {
			if c != ' ' || !spaced || i == 0 || i == len(v)-1 {
				return false
			}
		}
	}
	return true
}

// peer is what the receiver's SETTINGS said.
type peer struct {
	window     int64  // a stream's initial window
	connection int64  // the connection's window
	list       uint32 // SETTINGS_MAX_HEADER_LIST_SIZE, 0 when unbounded
}

// takes says the request fits the peer: its whole body within the stream's
// and the connection's windows (else a request split by flow control would
// leave its headers without its body), its header list within the peer's
// limit.
func (p peer) takes(r request) bool {
	return int64(r.body) <= p.window && int64(r.body) <= p.connection && (p.list == 0 || r.list <= p.list)
}

// conn is HTTP/2 over a call's TLS connection: what it writes goes in one
// Write each, what it reads is read a frame at a time.
type conn struct {
	w   io.Writer
	br  *bufio.Reader
	fr  *http2.Framer
	out bytes.Buffer
	dec *hpack.Decoder
	// headers is how much of the answer's header blocks was read.
	headers int
}

func newConn(rw io.ReadWriter) *conn {
	c := &conn{w: rw, br: bufio.NewReaderSize(rw, frameMin)}
	c.fr = http2.NewFramer(&c.out, c.br)
	c.fr.SetMaxReadFrameSize(frameMin)
	c.dec = hpack.NewDecoder(headerTable, nil)
	c.dec.SetMaxStringLength(webhook.ResponseHeaderBytesMax)
	return c
}

// flush writes what was framed in one Write.
func (c *conn) flush() error {
	_, err := c.w.Write(c.out.Bytes())
	c.out.Reset()
	return err
}

// start writes the client's preface and SETTINGS: no push, no stream of the
// server's, a window of webhook.AnswerBytesMax for the answer's body (more
// is never read), a header list of at most webhook.ResponseHeaderBytesMax.
func (c *conn) start() error {
	c.out.WriteString(http2.ClientPreface)
	// The framer writes to a buffer, which takes every write.
	_ = c.fr.WriteSettings(
		http2.Setting{ID: http2.SettingEnablePush, Val: 0},
		http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 0},
		http2.Setting{ID: http2.SettingInitialWindowSize, Val: webhook.AnswerBytesMax},
		http2.Setting{ID: http2.SettingMaxHeaderListSize, Val: webhook.ResponseHeaderBytesMax},
	)
	return c.flush()
}

// errProtocol is a receiver that does not speak HTTP/2 as it must.
var errProtocol = errors.New("send: the receiver broke HTTP/2")

// settings reads the server's preface, its SETTINGS, and what they say.
func (c *conn) settings() (peer, error) {
	f, err := c.fr.ReadFrame()
	if err != nil {
		return peer{}, err
	}
	sf, ok := f.(*http2.SettingsFrame)
	if !ok || sf.IsAck() {
		return peer{}, errProtocol
	}
	p := peer{window: windowInitial, connection: windowInitial}
	err = sf.ForeachSetting(func(s http2.Setting) error {
		if s.Valid() != nil {
			return errProtocol
		}
		switch s.ID {
		case http2.SettingInitialWindowSize:
			p.window = int64(s.Val)
		case http2.SettingMaxHeaderListSize:
			p.list = s.Val
		}
		return nil
	})
	return p, err
}

// ack acknowledges the server's SETTINGS, in a Write of its own.
func (c *conn) ack() error {
	_ = c.fr.WriteSettingsAck() // to the buffer
	return c.flush()
}

// errHeadersTooLarge is an answer whose header blocks pass
// webhook.ResponseHeaderBytesMax.
var errHeadersTooLarge = errors.New("send: the answer's headers are too large")

// next reads one frame, refusing before reading it a header block that would
// take the answer's past webhook.ResponseHeaderBytesMax.
func (c *conn) next() (http2.Frame, error) {
	head, err := c.br.Peek(9)
	if err != nil {
		return nil, err
	}
	fh, err := http2.ReadFrameHeader(bytes.NewReader(head))
	if err != nil {
		return nil, err
	}
	if fh.Type == http2.FrameHeaders || fh.Type == http2.FrameContinuation {
		if c.headers+int(fh.Length) > webhook.ResponseHeaderBytesMax {
			return nil, errHeadersTooLarge
		}
		c.headers += int(fh.Length)
	}
	return c.fr.ReadFrame()
}

// answer reads the answer's final status and maps it: 2xx sent, 410 gone,
// 3xx redirect (never followed), any other status; no_answer when none
// came (reset, refused, closed, timed out, malformed, too large). Its body
// is never read.
func (c *conn) answer() webhook.Outcome {
	var block []byte
	for {
		f, err := c.next()
		if err != nil {
			return webhook.OutcomeNoAnswer
		}
		var ended bool
		switch f := f.(type) {
		case *http2.HeadersFrame:
			if f.StreamID != stream {
				return webhook.OutcomeNoAnswer
			}
			block, ended = append(block, f.HeaderBlockFragment()...), f.HeadersEnded()
		case *http2.ContinuationFrame:
			if f.StreamID != stream {
				return webhook.OutcomeNoAnswer
			}
			block, ended = append(block, f.HeaderBlockFragment()...), f.HeadersEnded()
		case *http2.GoAwayFrame:
			if f.LastStreamID < stream {
				return webhook.OutcomeNoAnswer
			}
		case *http2.DataFrame, *http2.RSTStreamFrame, *http2.PushPromiseFrame:
			return webhook.OutcomeNoAnswer
		}
		if !ended {
			continue
		}
		code, ok := c.status(block)
		switch {
		case !ok || code == 101:
			return webhook.OutcomeNoAnswer
		case code < 200:
			block = nil // an interim answer: the final one follows
			continue
		case code < 300:
			return webhook.OutcomeSent
		case code < 400:
			return webhook.OutcomeRedirect
		case code == 410:
			return webhook.OutcomeGone
		}
		return webhook.OutcomeStatus
	}
}

// status decodes a header block and reads its one ":status", of three
// digits; the rest is decoded within webhook.ResponseHeaderBytesMax and
// dropped.
func (c *conn) status(block []byte) (int, bool) {
	code, statuses, size := 0, 0, uint32(0)
	ok := true
	c.dec.SetEmitEnabled(true)
	c.dec.SetEmitFunc(func(f hpack.HeaderField) {
		size += f.Size()
		if size > webhook.ResponseHeaderBytesMax {
			ok = false
			c.dec.SetEmitEnabled(false)
			return
		}
		if f.Name != ":status" {
			return
		}
		statuses++
		n, err := strconv.Atoi(f.Value)
		if err != nil || len(f.Value) != 3 || n < 100 {
			ok = false
			return
		}
		code = n
	})
	if _, err := c.dec.Write(block); err != nil || c.dec.Close() != nil {
		return 0, false
	}
	return code, ok && statuses == 1
}
