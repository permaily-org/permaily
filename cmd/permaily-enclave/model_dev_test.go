//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// A34. Dev mode composes the model as the image does, with decidetest's
// fake behind it, which never reaches OpenAI: off until the operator gives
// a key, then answering, and its canary finds nothing moved.
func TestA34DevModeAsksTheFake(t *testing.T) {
	if devModelClient.Load() != nil {
		t.Fatal("a played provider is set")
	}
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config{domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing", superAdmin: defaultSuperAdmin}
	p, err := newPlatform(ctx, cfg, router.HostsForDomain(cfg.domain), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.listener.Close(); _ = p.smtpListener.Close() })
	if p.model == nil {
		t.Fatal("dev mode composes no model")
	}
	q := &inbox.Question{
		Box: inbox.Box{MailboxID: "mbx_dev", Key: make([]byte, 32)}, MessageID: "msg_dev", ExpiresAt: devClock().Add(24 * time.Hour), Account: "acc_dev",
		Atoms:   []query.Atom{{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}},
		Subject: "An invoice", View: inbox.ViewOriginal,
		Text: func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextRead, nil },
	}
	if got, err := p.model.Ask(ctx, q); err != nil || got.Outcome != inbox.AskOff {
		t.Fatalf("no key: %+v %v", got, err)
	}
	if _, err := p.model.SetKey(ctx, contracts.ModelKeySet{Key: "pm-dev-model-key"}); err != nil {
		t.Fatal(err)
	}
	// Asked, through the fake (the message is not stored, so its answer is
	// not kept, and not used).
	if got, err := p.model.Ask(ctx, q); err != nil || got.Outcome != inbox.AskAnswered || got.Called != 1 {
		t.Fatalf("with a key: %+v %v", got, err)
	}
	if err := p.model.RunCanary(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := decide.LoadCanary()
	if st := p.model.Status(ctx); st.Canary == nil || st.Canary.State != attestation.CanarySame || st.Canary.Baseline != c.Baseline || st.Model != "gpt-6-luna" {
		t.Fatalf("dev mode's canary: %+v", st.Canary)
	}
}

// A34. The image runs the canary and sends the alarms on its own: run, the
// enclave's startup, with the real client over a played provider, ticks the
// test gives and nothing called by the test but the routes. The operator
// gives the key and the canary's answer moves only once both loops are
// past the run they make when they start, so only a tick runs the canary,
// which finds the answer moved, and only a tick sends its alarm, by mail to
// the super admin. Without runModel in run, or without either loop, it
// fails.
func TestA34TheImageRunsTheCanaryAndTheAlarmsOnItsOwn(t *testing.T) {
	t.Cleanup(func() { devAhead.Store(0) })
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	tab := devCanary(decidetest.NewTable())
	provider := decidetest.NewProvider(t, tab)
	devModelClient.Store(provider.Client())
	t.Cleanup(func() { devModelClient.Store(nil) })
	const admin = "pma_a34_ticks_token"
	sum := sha256.Sum256([]byte(admin))
	canaryTick, alarmTick := make(chan time.Time), make(chan time.Time)
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing", superAdmin: defaultSuperAdmin,
		adminTokenSHA256: hex.EncodeToString(sum[:]), model: modelConfig{organization: "org-a34", project: "proj-a34"},
		modelTicks: modelTicks{canary: canaryTick, alarms: alarmTick},
	}
	var logs lockedBuffer
	go func() {
		_ = run(cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode's self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}
	call := func(method, url, body string, into any) {
		t.Helper()
		var res *http.Response
		for range 50 {
			req, _ := http.NewRequest(method, url, strings.NewReader(body))
			if strings.HasPrefix(url, "https://api.") {
				req.Header.Set("Authorization", "Bearer "+admin)
			}
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if b, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || json.Unmarshal(b, into) != nil {
			t.Fatalf("%s %s: %d %s", method, url, res.StatusCode, b)
		}
	}
	model := func() *attestation.ModelStatus {
		var st attestation.Status
		call("GET", "https://app.permaily.test/status", "", &st)
		if st.Model == nil {
			t.Fatal("/status has no model")
		}
		return st.Model
	}
	alarms := func() (sent []struct{ To, Subject string }) {
		call("GET", "https://app.permaily.test/dev/outbox", "", &sent)
		return sent
	}
	if st := model(); st.KeySet || len(provider.Requests()) != 0 {
		t.Fatalf("a key, or the provider asked, before the operator gave one: %+v", st)
	}
	tick(t, "the alarms' loop", alarmTick)
	tick(t, "the canary's loop", canaryTick)

	const key = "sk-proj-test-runtickscanary"
	tab.RequireKey(key)
	var set contracts.ModelKey
	call("PUT", "https://api.permaily.test/v1/admin/model-key", `{"key":"`+key+`"}`, &set)
	if !set.Set {
		t.Fatalf("the key: %+v", set)
	}
	c, err := decide.LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	m := c.Messages[0]
	tab.SetFor(m.Subject, m.Body, m.Atoms[0].Atom(), 1-m.Atoms[0].P)
	// Without a key the check did not complete: it is tried again once its
	// lease is over.
	devAhead.Store(int64(decisions.CanaryLease + time.Second))
	tick(t, "the canary's loop", canaryTick)
	waitUntil(t, "the canary did not run on its ticker", func() bool {
		st := model()
		return st.Canary != nil && st.Canary.State == attestation.CanaryChanged
	})
	if n := len(provider.Requests()); n != 8 {
		t.Fatalf("the canary is 8 calls, sent %d", n)
	}
	tick(t, "the alarms' loop", alarmTick)
	waitUntil(t, "the alarm was not sent on its ticker", func() bool { return len(alarms()) > 0 })
	if sent := alarms(); len(sent) != 1 || sent[0].To != defaultSuperAdmin || sent[0].Subject != "Permaily: the model's canary saw an answer move" {
		t.Fatalf("the alarm: %+v", sent)
	}
	if strings.Contains(logs.String(), key) {
		t.Fatal("the key is in the log")
	}
}
