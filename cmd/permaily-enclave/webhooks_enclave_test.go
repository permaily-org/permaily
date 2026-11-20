//go:build !dev

package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/webhook"
)

// K57 (ADR 0039, "Calling a webhook"): the image sends notices through the
// host's vendor egress on port 8004 (or the port the flag names), never on
// the egress port of the allowlisted proxy, dialling the webhook's own
// host; and no file of the image but the composition makes a sender of
// notices.
func TestK57TheNoticesGoThroughTheVendorEgress(t *testing.T) {
	type dialled struct {
		port uint32
		host string
	}
	var got []dialled
	old := webhookEgress
	webhookEgress = func(port uint32) func(ctx context.Context, host string) (net.Conn, error) {
		return func(_ context.Context, host string) (net.Conn, error) {
			got = append(got, dialled{port, host})
			return nil, errors.New("the test's egress")
		}
	}
	t.Cleanup(func() { webhookEgress = old })
	call := func(cfg *config) webhook.Outcome {
		now := time.Now()
		return enclaveWebhookSender(*cfg).Send(context.Background(), webhook.Call{
			URL: "https://hooks.trip-app.com/permaily", Body: []byte(`{}`), ID: "ntc_x", Signature: "v2a,x", Timestamp: "1", WriteBy: now.Add(5 * time.Second),
		})
	}
	fs := flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	cfg := newConfig(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if o := call(cfg); o != webhook.OutcomeNoAnswer || len(got) != 1 || got[0] != (dialled{8004, "hooks.trip-app.com"}) || cfg.egressPort == 8004 {
		t.Fatalf("a notice was sent through %+v (%s)", got, o)
	}
	got = nil
	fs = flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	cfg = newConfig(fs)
	if err := fs.Parse([]string{"-vendor-egress-port", "9004"}); err != nil {
		t.Fatal(err)
	}
	if call(cfg); len(got) != 1 || got[0].port != 9004 {
		t.Fatalf("-vendor-egress-port 9004: %+v", got)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var code []string
		for _, line := range strings.Split(string(raw), "\n") {
			if c, _, _ := strings.Cut(line, "//"); strings.TrimSpace(c) != "" {
				code = append(code, c)
			}
		}
		src := strings.Join(code, "\n")
		for _, s := range []string{"send.New(", "enclave.WebhookEgressDialer", "webhookEgress(", "enclaveWebhookSender(cfg)"} {
			count[s] += strings.Count(src, s)
		}
	}
	// send.New once in the image's composition, twice in dev mode's (a
	// played vendor, or nobody), which the enclave build does not compile.
	if count["send.New("] != 3 || count["enclave.WebhookEgressDialer"] != 1 || count["webhookEgress("] != 1 || count["enclaveWebhookSender(cfg)"] != 1 {
		t.Fatalf("senders made: %v", count)
	}
}
