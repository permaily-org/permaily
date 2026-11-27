// Package imapwire captures what go-imap v2's client puts on the wire, for
// ADR 0042's spike item 7: the pinned version's transcript for read-only
// select, CONDSTORE and a streamed literal, against go-imap's in-memory
// server (and, where that server cannot answer, a scripted one quoting
// RFC 7162's examples). It is never imported by the main module nor deployed.
package imapwire

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Recorder wraps the client's side of a connection and keeps both
// directions in wire order: client bytes are recorded before they are sent,
// server bytes as soon as they are read.
type Recorder struct {
	net.Conn
	mu     sync.Mutex
	chunks []chunk
	read   atomic.Int64 // bytes the client has read from the server
}

type chunk struct {
	client bool
	data   []byte
}

// Record wraps conn.
func Record(conn net.Conn) *Recorder { return &Recorder{Conn: conn} }

func (r *Recorder) add(client bool, p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.chunks); n > 0 && r.chunks[n-1].client == client {
		r.chunks[n-1].data = append(r.chunks[n-1].data, p...)
		return
	}
	r.chunks = append(r.chunks, chunk{client: client, data: append([]byte(nil), p...)})
}

func (r *Recorder) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	if n > 0 {
		r.read.Add(int64(n))
		r.add(false, p[:n])
	}
	return n, err
}

// Write records p before it reaches the socket. Recording after Conn.Write
// returned raced the client's reader goroutine: the server's reply could be
// read and recorded first, putting it ahead of its command in the transcript.
// Recorded first, a command always precedes whatever the server sends in
// answer to it, so the transcript is in wire order. (A failed write is
// recorded in full; the test fails on the error anyway.)
func (r *Recorder) Write(p []byte) (int, error) {
	if len(p) > 0 {
		r.add(true, p)
	}
	return r.Conn.Write(p)
}

// BytesRead is how many bytes the client has read so far.
func (r *Recorder) BytesRead() int64 { return r.read.Load() }

var (
	literalAtEnd = regexp.MustCompile(`\{(\d+)\+?\}$`)
	loginLine    = regexp.MustCompile(`^(\S+ LOGIN) .*$`)
)

// elideOver is the size above which a literal's bytes are left out of the
// transcript (its length is kept).
const elideOver = 256

// Transcript renders the conversation, one line per protocol line, "C: "
// for the client and "S: " for the server. LOGIN arguments are redacted and
// literals larger than 256 bytes are replaced by their length.
func (r *Recorder) Transcript() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out strings.Builder
	for _, c := range r.chunks {
		prefix := "S: "
		if c.client {
			prefix = "C: "
		}
		data := c.data
		for len(data) > 0 {
			i := bytes.Index(data, []byte("\r\n"))
			line := data
			if i >= 0 {
				line, data = data[:i], data[i+2:]
			} else {
				data = nil
			}
			s := string(line)
			if c.client {
				s = loginLine.ReplaceAllString(s, "$1 <redacted>")
			}
			out.WriteString(prefix + s + "\n")
			if m := literalAtEnd.FindStringSubmatch(s); m != nil {
				n, _ := strconv.Atoi(m[1])
				if n > elideOver {
					skip := min(n, len(data))
					fmt.Fprintf(&out, "%s<%d bytes of literal elided>\n", prefix, n)
					data = data[skip:]
				} else if n <= len(data) {
					// A small literal: show it as is, then continue the line.
					lit := strings.ReplaceAll(string(data[:n]), "\r\n", "\\r\\n")
					fmt.Fprintf(&out, "%s<literal: %s>\n", prefix, lit)
					data = data[n:]
				}
			}
		}
	}
	return out.String()
}
