package connections

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// A connection's refusals in a window (ADR 0031, "Refusals past a window's
// bound"; the owner's decision 1 of 2026-11-17): at most RefusalsPerWindow
// refused receipts a connection, window and enclave, the others counted by
// operation and reason in one refusals receipt for the window, written when
// the window closes (FlushRefusals) or on the connection's next refusal
// after it.

// refusalsWorld is a world with one connection of accountA and its grant.
func refusalsWorld(t *testing.T) (*world, *Service, *oauth.Grant) {
	t.Helper()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	return w, s, w.grant(s, x, tok.AccessToken)
}

// refuseOnce is one refusal an adapter makes of g's request.
func refuseOnce(t *testing.T, s *Service, g *oauth.Grant, op Operation, reason receipts.RefusedReason) {
	t.Helper()
	if err := s.Refuse(context.Background(), g, Refusal{Operation: op, Reason: reason}); err != nil {
		t.Fatal(err)
	}
}

// counted is how many refusals rs count, all summaries together, and how
// many they say were recorded one by one.
func counted(rs []receipts.Receipt) (n, recorded int) {
	for _, r := range rs {
		recorded += r.Refusals.Recorded
		for _, c := range r.Refusals.Counts {
			n += c.Count
		}
	}
	return n, recorded
}

// toTheNextWindow moves w's clock into the next window.
func toTheNextWindow(w *world) {
	now := w.clock.now()
	w.clock.add(now.Truncate(RefusalWindow).Add(RefusalWindow).Sub(now))
}

// H31: a flood of refusals of one connection in one window writes exactly
// RefusalsPerWindow refused receipts and, once the window closes, one
// refusals receipt that counts the rest, names the window, and holds no
// message, receipt or device; a window within the bound writes no summary,
// and a flush with nothing left writes nothing.
func TestH31AFloodIsBoundedToNAndOneSummary(t *testing.T) {
	w, s, g := refusalsWorld(t)
	before := len(w.rcpt.of(receipts.KindRefused))
	from := w.clock.now().Truncate(RefusalWindow)
	const flood = 1000
	for range flood {
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
	}
	if got := len(w.rcpt.of(receipts.KindRefused)) - before; got != RefusalsPerWindow {
		t.Fatalf("%d refused receipts in one window, want %d", got, RefusalsPerWindow)
	}
	if got := w.rcpt.of(receipts.KindRefusals); len(got) != 0 {
		t.Fatalf("a summary before its window closed: %+v", got)
	}
	// Within the window, a flush leaves it open.
	if err := s.FlushRefusals(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := w.rcpt.of(receipts.KindRefusals); len(got) != 0 {
		t.Fatalf("a summary of an open window: %+v", got)
	}
	toTheNextWindow(w)
	if err := s.FlushRefusals(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	sum := w.rcpt.of(receipts.KindRefusals)
	if len(sum) != 1 {
		t.Fatalf("%d summaries, want 1", len(sum))
	}
	r := sum[0]
	if r.Connection != g.Connection || r.ClientID != g.ClientID || r.Vendor != g.Vendor || r.Resource != g.Resource {
		t.Fatalf("the summary's attribution: %+v", r)
	}
	if r.Device != "" || r.Refused != nil || r.Delivered != nil || r.Held != nil || r.Formulas != nil {
		t.Fatalf("the summary holds more than counts: %+v", r)
	}
	want := receipts.Refusals{From: from, Until: from.Add(RefusalWindow), Recorded: RefusalsPerWindow,
		Counts: []receipts.RefusalCount{{Operation: string(OperationRead), Reason: receipts.RefusedInvalidBody, Count: flood - RefusalsPerWindow}}}
	if !r.Refusals.From.Equal(want.From) || !r.Refusals.Until.Equal(want.Until) || r.Refusals.Recorded != want.Recorded ||
		len(r.Refusals.Counts) != 1 || r.Refusals.Counts[0] != want.Counts[0] {
		t.Fatalf("the summary: %+v, want %+v", *r.Refusals, want)
	}
	// Nothing more to write; a window within the bound writes no summary.
	if err := s.FlushRefusals(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for range RefusalsPerWindow {
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
	}
	toTheNextWindow(w)
	if err := s.FlushRefusals(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := len(w.rcpt.of(receipts.KindRefusals)); got != 1 {
		t.Fatalf("%d summaries after a window within the bound, want 1", got)
	}
	if got := len(w.rcpt.of(receipts.KindRefused)) - before; got != 2*RefusalsPerWindow {
		t.Fatalf("%d refused receipts, want %d", got, 2*RefusalsPerWindow)
	}
}

// H32: the counts are exact by operation and reason, the service's own
// refusals and an adapter's alike, also when the refusals come at once; the
// summary is written on the connection's next refusal after its window,
// before that refusal's own receipt; a refusal past the bound is answered
// as it would be with its receipt.
func TestH32TheCountsAreExactByOperationAndReason(t *testing.T) {
	w, s, g := refusalsWorld(t)
	ctx := context.Background()
	// The bound reached at once, by many requests together.
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if err := s.Refuse(ctx, g, Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for range 5 {
		if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 1000}); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Fatalf("a search past the bound: %v", err)
		}
	}
	for range 7 {
		if _, _, err := s.Read(ctx, g, "not-a-message"); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("a read past the bound: %v", err)
		}
	}
	for range 11 {
		refuseOnce(t, s, g, OperationAttachment, receipts.RefusedPartNotGiven)
	}
	if got := len(w.rcpt.of(receipts.KindRefused)); got != RefusalsPerWindow {
		t.Fatalf("%d refused receipts, want %d", got, RefusalsPerWindow)
	}
	toTheNextWindow(w)
	// The next refusal writes the summary first, then its own receipt.
	all := w.rcpt.count()
	refuseOnce(t, s, g, OperationRead, receipts.RefusedOutsideScope)
	if w.rcpt.count() != all+2 {
		t.Fatalf("%d receipts on the next refusal, want the summary and its own", w.rcpt.count()-all)
	}
	w.rcpt.mu.Lock()
	first, second := w.rcpt.all[all], w.rcpt.all[all+1]
	w.rcpt.mu.Unlock()
	if first.Kind != receipts.KindRefusals || second.Kind != receipts.KindRefused {
		t.Fatalf("the order: %s then %s", first.Kind, second.Kind)
	}
	want := map[receipts.RefusalCount]bool{
		{Operation: "search", Reason: receipts.RefusedInvalidBody, Count: 200 - RefusalsPerWindow}: true,
		{Operation: "search", Reason: receipts.RefusedInvalidLimit, Count: 5}:                      true,
		{Operation: "read", Reason: receipts.RefusedOutsideScope, Count: 7}:                        true,
		{Operation: "attachment", Reason: receipts.RefusedPartNotGiven, Count: 11}:                 true,
	}
	if len(first.Refusals.Counts) != len(want) {
		t.Fatalf("the counts: %+v", first.Refusals.Counts)
	}
	for _, c := range first.Refusals.Counts {
		if !want[c] {
			t.Fatalf("the counts: %+v", first.Refusals.Counts)
		}
	}
	// In the repository's order: by operation, then reason.
	for i := 1; i < len(first.Refusals.Counts); i++ {
		a, b := first.Refusals.Counts[i-1], first.Refusals.Counts[i]
		if a.Operation > b.Operation || (a.Operation == b.Operation && a.Reason >= b.Reason) {
			t.Fatalf("the counts are not in order: %+v", first.Refusals.Counts)
		}
	}
	// Another connection has its own window.
	x := w.ask(otherApp)
	tok := w.connectAll(s, w.person(accountB), x)
	other := w.grant(s, x, tok.AccessToken)
	refuseOnce(t, s, other, OperationRead, receipts.RefusedInvalidBody)
	if r := w.rcpt.of(receipts.KindRefused); r[len(r)-1].Connection != other.Connection {
		t.Fatal("another connection's refusal was not recorded")
	}
}

// H33: two enclaves at once each keep the bound for themselves, so a
// connection has at most twice RefusalsPerWindow refused receipts and two
// summaries a window, and every refusal is in one or the other, exactly.
func TestH33TwoEnclavesEachKeepTheBound(t *testing.T) {
	w, a, g := refusalsWorld(t)
	b := w.enclave()
	const each = 100
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Go(func() {
			for range each {
				if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if got := len(w.rcpt.of(receipts.KindRefused)); got != 2*RefusalsPerWindow {
		t.Fatalf("%d refused receipts with two enclaves, want %d", got, 2*RefusalsPerWindow)
	}
	toTheNextWindow(w)
	for _, s := range []*Service{a, b} {
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	sum := w.rcpt.of(receipts.KindRefusals)
	if len(sum) != 2 {
		t.Fatalf("%d summaries, want one an enclave", len(sum))
	}
	for _, r := range sum {
		if n, rec := counted([]receipts.Receipt{r}); n != each-RefusalsPerWindow || rec != RefusalsPerWindow {
			t.Fatalf("an enclave's summary counts %d and recorded %d", n, rec)
		}
	}
	if n, rec := counted(sum); n+rec != 2*each {
		t.Fatalf("%d refusals accounted for, want %d", n+rec, 2*each)
	}
}

// H34: a crash between windows. What a closed window counted is written at
// its close, so a crash after it loses nothing of it; a crash loses only
// the counts of the window still open, and those of a closed window whose
// summary the storage refused until then (carried into the next summary
// meanwhile); a stop, as the composition makes at shutdown, writes the open
// window too. The enclave that starts again keeps its own bound.
func TestH34ACrashBetweenWindows(t *testing.T) {
	w, s, g := refusalsWorld(t)
	ctx := context.Background()
	for range 50 {
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
	}
	toTheNextWindow(w)
	if err := s.FlushRefusals(ctx, false); err != nil {
		t.Fatal(err)
	}
	// The next window, counted and not yet written, then a crash: s is
	// dropped without a flush.
	for range 40 {
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
	}
	again := w.enclave()
	for range 45 {
		refuseOnce(t, again, g, OperationRead, receipts.RefusedInvalidBody)
	}
	toTheNextWindow(w)
	if err := again.FlushRefusals(ctx, false); err != nil {
		t.Fatal(err)
	}
	sum := w.rcpt.of(receipts.KindRefusals)
	if len(sum) != 2 {
		t.Fatalf("%d summaries, want the first window's and the new enclave's", len(sum))
	}
	if n, _ := counted(sum[:1]); n != 20 {
		t.Fatalf("the first window's summary counts %d, want 20", n)
	}
	if n, _ := counted(sum[1:]); n != 15 {
		t.Fatalf("the new enclave's summary counts %d, want 15", n)
	}
	if got := len(w.rcpt.of(receipts.KindRefused)); got != 3*RefusalsPerWindow {
		t.Fatalf("%d refused receipts, want %d", got, 3*RefusalsPerWindow)
	}

	// A summary the storage refuses is kept, not lost: the connection's
	// next refusal writes it, before its own receipt.
	for range 35 {
		refuseOnce(t, again, g, OperationRead, receipts.RefusedInvalidBody)
	}
	from := w.clock.now().Truncate(RefusalWindow)
	toTheNextWindow(w)
	w.rcpt.mu.Lock()
	w.rcpt.fail = errors.New("the storage refuses")
	w.rcpt.mu.Unlock()
	if err := again.FlushRefusals(ctx, false); err == nil {
		t.Fatal("a flush the storage refused reported success")
	}
	w.rcpt.mu.Lock()
	w.rcpt.fail = nil
	w.rcpt.mu.Unlock()
	for range 33 {
		refuseOnce(t, again, g, OperationSearch, receipts.RefusedInvalidBody)
	}
	// A stop writes the open window, up to when it stops.
	w.clock.add(10 * time.Second)
	stopped := w.clock.now()
	if err := again.FlushRefusals(ctx, true); err != nil {
		t.Fatal(err)
	}
	sum = w.rcpt.of(receipts.KindRefusals)
	if len(sum) != 4 {
		t.Fatalf("%d summaries, want the carried one and the stop's", len(sum))
	}
	carried, last := sum[2], sum[3]
	if !carried.Refusals.From.Equal(from) || carried.Refusals.Recorded != RefusalsPerWindow {
		t.Fatalf("the carried summary: %+v", *carried.Refusals)
	}
	if n, _ := counted([]receipts.Receipt{carried}); n != 5 {
		t.Fatalf("the carried summary counts %d, want 5", n)
	}
	if !last.Refusals.Until.Equal(stopped) || last.Refusals.Recorded != RefusalsPerWindow {
		t.Fatalf("the stop's summary: %+v", *last.Refusals)
	}
	if n, _ := counted([]receipts.Receipt{last}); n != 3 {
		t.Fatalf("the stop's summary counts %d, want 3", n)
	}
	// Nothing is left after a stop.
	if err := again.FlushRefusals(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := len(w.rcpt.of(receipts.KindRefusals)); got != 4 {
		t.Fatalf("%d summaries after a second stop", got)
	}
}
