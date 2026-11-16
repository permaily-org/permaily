//go:build dev

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
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
