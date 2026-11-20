package enclave

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/internal/webhook/send"
)

// K21 (ADR 0039): the webhooks' sender dials only the vendor egress's port
// (8004), never the allowlisted proxy, whatever the webhook.
func TestK21TheWebhookSenderDialsOnlyTheVendorEgress(t *testing.T) {
	ports := recordDials(t)
	dial := WebhookEgressDialer(8004)
	if _, err := dial(context.Background(), "trip-app.com"); err == nil {
		t.Fatal("dialled without a host")
	}
	s := send.New(dial, nil, time.Now)
	for _, u := range []string{"https://trip-app.com/h", "https://kms.us-east-2.amazonaws.com/h", "https://api.openai.com/h"} {
		c := webhook.Call{URL: u, ID: "ntc_1", Timestamp: "1", Signature: "v2a,x", Body: []byte("{}"), WriteBy: time.Now().Add(5 * time.Second)}
		if o := s.Send(context.Background(), c); o != webhook.OutcomeNoAnswer {
			t.Errorf("%s: %s", u, o)
		}
	}
	if got := ports(); len(got) != 4 || slices.ContainsFunc(got, func(p uint32) bool { return p != 8004 }) {
		t.Fatalf("dialled %v, want only 8004", got)
	}
}

// playedEgress replaces dialHost with a played vendor egress on a pipe that
// reads the CONNECT, hands it to answer, and echoes what follows when the
// answer was 200.
func playedEgress(t *testing.T, answer func(r *http.Request) string) <-chan *http.Request {
	t.Helper()
	got := make(chan *http.Request, 1)
	old := dialHost
	dialHost = func(_ context.Context, port uint32) (net.Conn, error) {
		if port != 8004 {
			return nil, errors.New("not the vendor egress")
		}
		client, host := net.Pipe()
		go func() {
			defer host.Close()
			br := bufio.NewReader(host)
			r, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			got <- r
			a := answer(r)
			if _, err := io.WriteString(host, a); err != nil || !strings.HasPrefix(a, "HTTP/1.1 200 ") {
				return
			}
			_, _ = io.Copy(host, br)
		}()
		return client, nil
	}
	t.Cleanup(func() { dialHost = old })
	return got
}

// K21: the tunnel is a CONNECT to the host's 443 with no credential; the
// stream it gives is the tunnel's, nothing of the answer left in it.
func TestK21TheWebhookEgressDialerOpensATunnel(t *testing.T) {
	got := playedEgress(t, func(*http.Request) string { return "HTTP/1.1 200 Connection established\r\n\r\n" })
	conn, err := WebhookEgressDialer(8004)(context.Background(), "trip-app.com")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := <-got
	if r.Method != http.MethodConnect || r.Host != "trip-app.com:443" || r.RequestURI != "trip-app.com:443" ||
		r.Header.Get("Proxy-Authorization") != "" || len(r.Header) != 0 {
		t.Errorf("%s %s %s %v", r.Method, r.RequestURI, r.Host, r.Header)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err := io.ReadFull(conn, b); err != nil || string(b) != "hello" {
		t.Errorf("%q, %v", b, err)
	}
}

// K21: a tunnel the egress does not open, an answer past its bound, a host
// that is not a lower-case DNS name, a cancelled dial: no stream.
func TestK21TheWebhookEgressDialerRefuses(t *testing.T) {
	for name, a := range map[string]string{
		"forbidden":       "HTTP/1.1 403 Forbidden\r\n\r\n",
		"not HTTP":        "SSH-2.0-OpenSSH\r\n\r\n",
		"a long answer":   "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("a", WebhookConnectBytesMax) + "\r\n\r\n",
		"a broken answer": "HTTP/1.1 200",
	} {
		playedEgress(t, func(*http.Request) string { return a })
		if c, err := WebhookEgressDialer(8004)(context.Background(), "trip-app.com"); err == nil {
			c.Close()
			t.Errorf("%s: a tunnel", name)
		}
	}
	ports := recordDials(t)
	for _, h := range []string{"", "Trip-App.com", "trip-app.com:80", "a b", "x\r\nHost: y", strings.Repeat("a", 254)} {
		if _, err := WebhookEgressDialer(8004)(context.Background(), h); err == nil {
			t.Errorf("%q dialled", h)
		}
	}
	if len(ports()) != 0 {
		t.Errorf("dialled %v", ports())
	}

	// A context that ends while the egress says nothing.
	old := dialHost
	dialHost = func(context.Context, uint32) (net.Conn, error) {
		client, host := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, host) }()
		return client, nil
	}
	t.Cleanup(func() { dialHost = old })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WebhookEgressDialer(8004)(ctx, "trip-app.com"); err == nil {
		t.Error("a tunnel without an answer")
	}
	ctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := WebhookEgressDialer(8004)(ctx, "trip-app.com"); err == nil {
		t.Error("a tunnel after a cancel")
	}
}
