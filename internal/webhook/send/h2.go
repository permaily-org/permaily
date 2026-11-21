package send

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/url"
	"strconv"

	"golang.org/x/net/http/httpguts"
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

// peer is what the receiver's SETTINGS said. A setting it did not send is
// unbounded (math.MaxUint32); one it sent, zero included, is its limit.
type peer struct {
	window     int64  // a stream's initial window
	connection int64  // the connection's window
	list       uint32 // SETTINGS_MAX_HEADER_LIST_SIZE
	streams    uint32 // SETTINGS_MAX_CONCURRENT_STREAMS
}

// takes says the request fits the peer: a stream it may open (the peer
// allows at least one at once), its whole body within the stream's and the
// connection's windows (else a request split by flow control would leave its
// headers without its body), its header list within the peer's limit.
func (p peer) takes(r request) bool {
	return p.streams >= 1 && int64(r.body) <= p.window && int64(r.body) <= p.connection && r.list <= p.list
}

// conn is HTTP/2 over a call's TLS connection: what it writes goes in one
// Write each, what it reads is read a frame at a time with exact reads,
// never ahead: a frame's nine-byte header, then its payload only when the
// frame is one to read (next). The TLS connection below holds at most the
// one record it is opening (16 KiB of plaintext), which TLS cannot avoid;
// nothing of it is taken past the frame read.
type conn struct {
	w   io.Writer
	r   *exact
	fr  *http2.Framer
	out bytes.Buffer
	dec *hpack.Decoder
	// headers is how much of the answer's header blocks was read.
	headers int
}

// exact reads a frame's header with an exact read and gives it back to the
// framer, which then reads the payload exactly too.
type exact struct {
	r    io.Reader
	head [9]byte
	held []byte
}

// header reads the next frame's header and holds it for the framer.
func (e *exact) header() (http2.FrameHeader, error) {
	if _, err := io.ReadFull(e.r, e.head[:]); err != nil {
		return http2.FrameHeader{}, err
	}
	e.held = e.head[:]
	return http2.ReadFrameHeader(bytes.NewReader(e.head[:]))
}

func (e *exact) Read(p []byte) (int, error) {
	if len(e.held) > 0 {
		n := copy(p, e.held)
		e.held = e.held[n:]
		return n, nil
	}
	return e.r.Read(p)
}

func newConn(rw io.ReadWriter) *conn {
	c := &conn{w: rw, r: &exact{r: rw}}
	c.fr = http2.NewFramer(&c.out, c.r)
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

// settings reads the server's preface, its SETTINGS, and what they say; a
// first frame of another type has only its header read.
func (c *conn) settings() (peer, error) {
	fh, err := c.r.header()
	if err != nil {
		return peer{}, err
	}
	if fh.Type != http2.FrameSettings {
		return peer{}, errProtocol
	}
	f, err := c.fr.ReadFrame()
	if err != nil {
		return peer{}, err
	}
	sf, ok := f.(*http2.SettingsFrame)
	if !ok || sf.IsAck() {
		return peer{}, errProtocol
	}
	p := peer{window: windowInitial, connection: windowInitial, list: math.MaxUint32, streams: math.MaxUint32}
	err = sf.ForeachSetting(func(s http2.Setting) error {
		if s.Valid() != nil {
			return errProtocol
		}
		switch s.ID {
		case http2.SettingInitialWindowSize:
			p.window = int64(s.Val)
		case http2.SettingMaxHeaderListSize:
			p.list = s.Val
		case http2.SettingMaxConcurrentStreams:
			p.streams = s.Val
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

// errBody is a DATA frame before the final status: the answer's body is
// never read.
var errBody = errors.New("send: a body before the answer's status")

// next reads one frame's header and then, only when it is one to read, its
// payload: never a DATA frame's (the body is never read, and comes only
// after a status, which ends the answer), never a header block's that would
// take the answer's past webhook.ResponseHeaderBytesMax; any other frame's
// of at most 16 KiB (SetMaxReadFrameSize), read and dropped.
func (c *conn) next() (http2.Frame, error) {
	fh, err := c.r.header()
	if err != nil {
		return nil, err
	}
	switch fh.Type {
	case http2.FrameData:
		return nil, errBody
	case http2.FrameHeaders, http2.FrameContinuation:
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
		case *http2.RSTStreamFrame, *http2.PushPromiseFrame:
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
// digits; the rest is decoded within webhook.ResponseHeaderBytesMax, checked
// as an HTTP/2 response's (RFC 9113, sections 8.2 and 8.3: ":status" alone
// and before every other field, names lower-case tokens, no
// connection-specific field, values without a control character but a
// tab) and dropped. A
// list that is not a valid response is not a status.
func (c *conn) status(block []byte) (int, bool) {
	code, statuses, size := 0, 0, uint32(0)
	ok, regular := true, false
	c.dec.SetEmitEnabled(true)
	c.dec.SetEmitFunc(func(f hpack.HeaderField) {
		size += f.Size()
		if size > webhook.ResponseHeaderBytesMax {
			ok = false
			c.dec.SetEmitEnabled(false)
			return
		}
		if f.Name != ":status" {
			if !responseField(f) {
				ok = false
			}
			regular = true
			return
		}
		if regular {
			ok = false
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

// responseField is a regular field an HTTP/2 response may carry: a
// lower-case token for a name, not one of HTTP/1's connection-specific
// fields (RFC 9113, section 8.2.2), a value without a control character but
// a tab (so no NUL, CR or LF). A pseudo-header is no token.
func responseField(f hpack.HeaderField) bool {
	if !httpguts.ValidHeaderFieldName(f.Name) || !httpguts.ValidHeaderFieldValue(f.Value) {
		return false
	}
	for i := 0; i < len(f.Name); i++ {
		if c := f.Name[i]; c >= 'A' && c <= 'Z' {
			return false
		}
	}
	switch f.Name {
	case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "te":
		return false
	}
	return true
}
