package inbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func writer(mem *store.Memory, c *clock) *inbox.Writer {
	return &inbox.Writer{Entries: mem, Parts: mem, Now: c.now, SeenFor: 7 * 24 * time.Hour, ClaimStale: 2 * time.Minute}
}

var box = inbox.Box{MailboxID: "mbx_a", Key: make([]byte, envelope.KeySize)}

func TestSeenKey(t *testing.T) {
	a, _ := inbox.SeenKey(box.Key, "x@example.org", []byte("raw"))
	b, _ := inbox.SeenKey(box.Key, "x@example.org", []byte("raw"))
	c, _ := inbox.SeenKey(box.Key, "y@example.org", []byte("raw"))
	other := make([]byte, envelope.KeySize)
	other[0] = 1
	d, _ := inbox.SeenKey(other, "x@example.org", []byte("raw"))
	if a != b || a == c || a == d || len(a) != 64 {
		t.Fatal(a, b, c, d)
	}
	id := inbox.NewMessageID(time.Unix(1, 0))
	if len(id) != 32 || id[:16] != "000000003b9aca00" || inbox.NewMessageID(time.Unix(2, 0)) <= id {
		t.Fatal(id)
	}
}

// A delivery claims, stores in its order and marks; a retry finds it stored.
func TestDelivery(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	mem.Now = c.now
	w := writer(mem, c)
	var steps []string
	mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step != store.StepRead {
			steps = append(steps, string(step)+" "+key[:strings.IndexByte(key, '#')+1])
		}
		return nil
	}
	p := inbox.Parse(context.Background(), []byte(confirmation("forwarding-noreply@google.com", address, template(address))))
	claim, err := w.Claim(ctx, box, "seen1", 30)
	if err != nil || claim.Stored() || claim.Seen.RetentionDays != 30 || !claim.Seen.ExpiresAt.Equal(c.t.AddDate(0, 0, 30)) {
		t.Fatalf("%+v %v", claim, err)
	}
	confirm := &inbox.ConfirmEntry{Code: "1", ReceivedAt: claim.Seen.ReceivedAt, Ends: claim.Seen.ReceivedAt.Add(time.Hour)}
	if err := w.Store(ctx, box, claim, &p, []byte("raw"), confirm); err != nil {
		t.Fatal(err)
	}
	if err := w.Mark(ctx, box, claim); err != nil {
		t.Fatal(err)
	}
	want := []string{"entry seen#", "raw msg#", "entry txt#", "entry confirm#", "item msg#", "entry seen#"}
	if !slices.Equal(steps, want) {
		t.Fatalf("order of writes %v", steps)
	}
	again, err := w.Claim(ctx, box, "seen1", 7)
	if err != nil || !again.Stored() || again.Seen.Message != claim.Seen.Message {
		t.Fatalf("%+v %v", again, err)
	}
	item, err := mem.Item(ctx, box.MailboxID, claim.Seen.Message)
	if err != nil || item.RetentionDays != 30 || item.Size != 3 {
		t.Fatalf("%+v %v", item, err)
	}
	entries, _ := mem.ListEntries(ctx, inbox.Partition(box.MailboxID), inbox.KeyConfirm, "", 10)
	if len(entries) != 1 || inbox.OpenConfirm(box, &entries[0], c.t) == nil || inbox.OpenConfirm(box, &entries[0], c.t.Add(2*time.Hour)) != nil {
		t.Fatal("the confirmation entry")
	}
	bad := entries[0]
	bad.Key = inbox.KeyConfirm + "0000000000000000aaaaaaaaaaaaaaaa"
	if inbox.OpenConfirm(box, &bad, c.t) != nil || inbox.OpenConfirm(box, &store.Entry{Key: "confirm#"}, c.t) != nil {
		t.Fatal("an entry moved to another message opened")
	}
}

// A claim stands for ClaimStale, then is taken over with what it fixed; a
// delivery stopped before each of its writes is completed by the retry.
func TestClaimTakenOverAfterAStoppedDelivery(t *testing.T) {
	ctx := context.Background()
	p := inbox.Parse(context.Background(), []byte(plainMessage))
	for _, stop := range []store.Step{store.StepRaw, store.StepEntry, store.StepItem} {
		mem := store.NewMemory()
		c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
		mem.Now = c.now
		w := writer(mem, c)
		first, err := w.Claim(ctx, box, "seen1", 7)
		if err != nil {
			t.Fatal(err)
		}
		mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
			if step == stop && key != inbox.KeySeen+"seen1" {
				return errors.New("stopped")
			}
			return nil
		}
		if err := w.Store(ctx, box, first, &p, []byte(plainMessage), nil); err == nil {
			t.Fatal("not stopped")
		}
		mem.Interrupt = nil
		if _, err := w.Claim(ctx, box, "seen1", 365); !errors.Is(err, inbox.ErrClaimed) {
			t.Fatalf("within ClaimStale: %v", err)
		}
		c.add(3 * time.Minute)
		second, err := w.Claim(ctx, box, "seen1", 365)
		if err != nil || second.Seen.Message != first.Seen.Message || second.Seen.RetentionDays != 7 || !second.Seen.ReceivedAt.Equal(first.Seen.ReceivedAt) {
			t.Fatalf("taken over: %+v %v", second, err)
		}
		if err := w.Store(ctx, box, second, &p, []byte(plainMessage), nil); err != nil {
			t.Fatal(err)
		}
		// The first delivery's mark comes late: the claim was taken over, and
		// the message is stored either way.
		if err := w.Mark(ctx, box, first); err != nil {
			t.Fatal(err)
		}
		if err := w.Mark(ctx, box, second); err != nil {
			t.Fatal(err)
		}
		msgs, _ := mem.ListMessages(ctx, box.MailboxID, 10, "")
		if len(msgs) != 1 || msgs[0].RetentionDays != 7 {
			t.Fatalf("stopped at %s: %+v", stop, msgs)
		}
	}
}

func TestClaimErrors(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	c := &clock{t: time.Now().UTC()}
	w := writer(mem, c)
	mem.SetEntry(store.Entry{Partition: inbox.Partition(box.MailboxID), Key: inbox.KeySeen + "junk", Value: []byte("not sealed"), Version: 1})
	if _, err := w.Claim(ctx, box, "junk", 7); err == nil {
		t.Fatal("a seen entry that does not open")
	}
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepRead {
			return errors.New("down")
		}
		return nil
	}
	if _, err := w.Claim(ctx, box, "x", 7); err == nil {
		t.Fatal("a read that failed")
	}
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepEntry {
			return errors.New("down")
		}
		return nil
	}
	if _, err := w.Claim(ctx, box, "y", 7); err == nil {
		t.Fatal("a write that failed")
	}
	// Contended twice: another delivery takes the entry each time.
	contended := &racing{Memory: mem}
	mem.Interrupt = nil
	w.Entries = contended
	if _, err := w.Claim(ctx, box, "z", 7); !errors.Is(err, inbox.ErrClaimed) {
		t.Fatalf("contended: %v", err)
	}
}

// racing writes the entry first every time a claim is about to write it.
type racing struct{ *store.Memory }

func (r *racing) PutEntry(ctx context.Context, e *store.Entry) error {
	other := *e
	other.Version = 0
	_ = r.Memory.PutEntry(ctx, &other)
	return r.Memory.PutEntry(ctx, e)
}
