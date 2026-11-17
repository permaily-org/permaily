package main

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The composition of the refusals' pass and of the stop (ADR 0031,
// "Refusals past a window's bound"; the review of pull request 161,
// findings 4 and 7).

var signInCode = regexp.MustCompile(`\n    (\d{6})\n`)

// signedUp is an account made through the composed accounts service, with
// its standing open: the refusals' writes read it.
func signedUp(t *testing.T, w *connectionsWorld) string {
	t.Helper()
	ctx := context.Background()
	acc := w.parts.accounts
	inv, err := acc.CreateInvitations(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := acc.StartEmail(ctx, contracts.EmailStart{Address: "person@example.com", Invitation: inv.Invitations[0], Terms: domain.TermsVersion})
	if err != nil {
		t.Fatal(err)
	}
	acc.Wait() // the message is sent in the background
	sent := w.outbox.Sent()
	if len(sent) == 0 {
		t.Fatal("no message")
	}
	m := signInCode.FindStringSubmatch(sent[len(sent)-1].Text)
	if m == nil {
		t.Fatal("no code")
	}
	me, _, err := acc.FinishEmail(ctx, contracts.EmailFinish{Attempt: res.Attempt, Code: m[1]}, accounts.Browser{UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return me.Account.ID
}

// refusalsOf are account's summaries of refusals in the world's storage.
func refusalsOf(t *testing.T, w *connectionsWorld, account string) []receipts.Receipt {
	t.Helper()
	rs, err := receipts.New(w.mem, w.keys, w.now, nil).List(context.Background(), account, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []receipts.Receipt
	for _, r := range rs {
		if r.Kind == receipts.KindRefusals {
			out = append(out, r)
		}
	}
	return out
}

func countedIn(r receipts.Receipt) int {
	n := 0
	for _, c := range r.Refusals.Counts {
		n += c.Count
	}
	return n
}

// H41 (finding 7): the refusals' pass runs refusalPassDelay after each
// minute of the clock ends, whatever second the enclave started at.
func TestH41TheRefusalsPassIsAMomentAfterEachMinute(t *testing.T) {
	for _, c := range []struct {
		at   string
		want time.Duration
	}{
		{"12:00:00", 2 * time.Second},
		{"12:00:01.5", 500 * time.Millisecond},
		{"12:00:02", time.Minute},
		{"12:00:30", 32 * time.Second},
		{"12:00:59.999", 2*time.Second + time.Millisecond},
	} {
		at, err := time.Parse("15:04:05.999", c.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := untilRefusalPass(at); got != c.want {
			t.Errorf("at %s, the next pass in %s, want %s", c.at, got, c.want)
		}
	}
}

// H34 and H41, composed: an enclave started at a minute's thirtieth second
// writes that minute's summary on its pass two seconds after the minute
// ends, not a minute after it started; its stop waits for the pass, admits
// no more requests and writes the open window up to the stop.
func TestH34TheEnclaveWritesRefusalsOnItsPassAndAtItsStop(t *testing.T) {
	w := newConnectionsWorld(t)
	c, err := composeConnections(w.parts)
	if err != nil {
		t.Fatal(err)
	}
	account := signedUp(t, w)
	g := &oauth.Grant{
		Connection: "con_aaaaaaaaaaaaaaaaaaaaaaaaaa", Family: "fam_aaaaaaaaaaaaaaaaaaaaaaaaaa", Account: account,
		ClientID: "https://claude.ai/oauth/claude-code-client-metadata", Vendor: "claude.ai", Resource: contracts.ResourceMCP,
	}
	refuse := func(n int) {
		t.Helper()
		for range n {
			if err := c.svc.Refuse(context.Background(), g, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.advance(30 * time.Second) // the enclave starts at 12:00:30
	waits, fire := make(chan time.Duration, 1), make(chan time.Time)
	after := func(d time.Duration) <-chan time.Time { waits <- d; return fire }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passed := runConnections(ctx, c.svc, slog.New(slog.DiscardHandler), make(chan time.Time), refusalClock{now: w.now, after: after})
	if d := <-waits; d != 32*time.Second {
		t.Fatalf("the first pass in %s, want 32s", d)
	}
	refuse(40)
	w.advance(32 * time.Second) // 12:01:02
	fire <- w.now()
	if d := <-waits; d != time.Minute {
		t.Fatalf("the next pass in %s, want a minute", d)
	}
	sum := refusalsOf(t, w, account)
	if len(sum) != 1 || countedIn(sum[0]) != 10 || sum[0].Refusals.Recorded != connections.RefusalsPerWindow ||
		!sum[0].Refusals.Until.Equal(time.Date(2026, 11, 16, 12, 1, 0, 0, time.UTC)) {
		t.Fatalf("the pass wrote %d summaries: %+v", len(sum), sum)
	}
	// The stop: the pass ends, then the open window is written.
	refuse(35)
	w.advance(5 * time.Second)
	cancel()
	if err := stopConnections(c.svc, passed, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	sum = refusalsOf(t, w, account)
	if len(sum) != 2 || countedIn(sum[1]) != 5 || !sum[1].Refusals.Until.Equal(w.now()) {
		t.Fatalf("the stop wrote %d summaries: %+v", len(sum), sum)
	}
	err = c.svc.Refuse(context.Background(), g, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody})
	if !errors.Is(err, connections.ErrUnavailable) {
		t.Fatalf("a request after the stop: %v", err)
	}
}

// H34, composed: a stop that cannot write what it counted says so.
func TestH34AStopThatCannotWriteSaysSo(t *testing.T) {
	w := newConnectionsWorld(t)
	c, err := composeConnections(w.parts)
	if err != nil {
		t.Fatal(err)
	}
	account := signedUp(t, w)
	g := &oauth.Grant{Connection: "con_aaaaaaaaaaaaaaaaaaaaaaaaaa", Account: account, ClientID: "https://claude.ai/oauth/claude-code-client-metadata", Vendor: "claude.ai", Resource: contracts.ResourceMCP}
	for range 40 {
		if err := c.svc.Refuse(context.Background(), g, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}); err != nil {
			t.Fatal(err)
		}
	}
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		if step == store.StepEntry && partition == receipts.Partition(account) {
			return errors.New("the storage refuses")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	passed := runConnections(ctx, c.svc, slog.New(slog.DiscardHandler), make(chan time.Time), refusalClock{now: w.now, after: func(time.Duration) <-chan time.Time { return nil }})
	cancel()
	if err := stopConnections(c.svc, passed, slog.New(slog.DiscardHandler)); !errors.Is(err, connections.ErrUnavailable) {
		t.Fatalf("a stop that wrote nothing: %v", err)
	}
}
