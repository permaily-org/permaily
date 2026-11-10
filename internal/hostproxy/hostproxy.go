// Package hostproxy is the untrusted glue on the parent EC2 instance. It only
// moves bytes: inbound TCP connections are piped to the enclave over vsock,
// and the enclave's outbound connections go through an HTTP CONNECT proxy
// restricted to an allowlist. TLS is terminated inside the enclave in both
// directions, so nothing here can read or alter traffic undetected.
package hostproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Forward accepts connections on ln and pipes each one to a connection
// returned by dial until ctx is done.
func Forward(ctx context.Context, ln net.Listener, dial func(context.Context) (net.Conn, error), log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		in, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		go func() {
			defer in.Close()
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			out, err := dial(dctx)
			cancel()
			if err != nil {
				log.Warn("forward: dial upstream", "err", err)
				return
			}
			defer out.Close()
			pipe(in, out)
		}()
	}
}

// logLineLimit bounds one line from the enclave; longer ones are cut.
const logLineLimit = 16 << 10

// Logs accepts connections on ln (the enclave's log sink) and copies each line
// to out with the prefix "enclave: ", until ctx is done. The enclave has no
// console in production, so this is the only way to see why it fails. Lines
// are data from the enclave and are never interpreted.
func Logs(ctx context.Context, ln net.Listener, out io.Writer) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var mu sync.Mutex
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReaderSize(conn, logLineLimit)
			for {
				line, isPrefix, err := r.ReadLine()
				if err != nil {
					return
				}
				mu.Lock()
				_, _ = out.Write(append(append([]byte("enclave: "), line...), '\n'))
				mu.Unlock()
				// Skip the rest of a line longer than the limit.
				for isPrefix && err == nil {
					_, isPrefix, err = r.ReadLine()
				}
			}
		}()
	}
}

// pipe copies in both directions until either side closes.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		// Half-close when supported so the peer sees EOF, otherwise close.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// ConnectProxy is an HTTP CONNECT proxy that only tunnels to allowlisted
// host:port targets.
type ConnectProxy struct {
	Allow []string
	Dial  func(ctx context.Context, network, addr string) (net.Conn, error)
	Log   *slog.Logger
}

// ServeHTTP implements http.Handler.
func (p *ConnectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}
	if !slices.Contains(p.Allow, r.Host) {
		p.Log.Warn("egress: denied", "target", r.Host)
		http.Error(w, "target not allowed", http.StatusForbidden)
		return
	}
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	upstream, err := dial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	// Bytes the client sent right after the CONNECT request (e.g. a TLS ClientHello).
	if n := buf.Reader.Buffered(); n > 0 {
		early, _ := buf.Peek(n)
		if _, err := upstream.Write(early); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	p.Log.Info("egress: tunnel", "target", r.Host)
	go func() {
		defer client.Close()
		defer upstream.Close()
		pipe(client, upstream)
	}()
}
