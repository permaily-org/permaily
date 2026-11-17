package connections

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

var listAt = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

// C2 (the second design review): the account's list is bounded by count
// and bytes on every write, keeps a request decided once for RequestTTL,
// fences a closing account, and rolls to its next epoch with everything it
// holds before its history reaches the strict read's bound.
func TestReview2C2TheListIsBounded(t *testing.T) {
	l := &ListState{V: 1, Account: "acc_a", Current: map[string]string{}}
	if err := l.Decide("r1", listAt); err != nil {
		t.Fatal(err)
	}
	if err := l.Decide("r1", listAt.Add(time.Minute)); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Fatalf("decided twice: %v", err)
	}
	if err := l.Decide("r1", listAt.Add(oauth.RequestTTL)); err != nil {
		t.Fatalf("a decision kept past its request's life: %v", err)
	}
	for i := range ListDecidedMax - 1 {
		if err := l.Decide(fmt.Sprint("d", i), listAt.Add(oauth.RequestTTL)); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := l.Decide("one more", listAt.Add(oauth.RequestTTL)); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("past its decisions: %v", err)
	}

	for i := range ListActiveMax {
		id := fmt.Sprint("con_", i)
		if err := l.AddPending(id, fmt.Sprint("https://c", i, ".example/x"), listAt); err != nil {
			t.Fatal(i, err)
		}
		l.Current[fmt.Sprint("https://c", i, ".example/x")] = id
	}
	if err := l.AddPending("con_new", "https://new.example/x", listAt); !errors.Is(err, contracts.ErrTooManyConnections) {
		t.Fatalf("past its active ones: %v", err)
	}
	if err := l.AddPending("con_replacing", "https://c0.example/x", listAt); err != nil {
		t.Fatalf("a replacement counted twice: %v", err)
	}
	if !l.Fits() {
		t.Fatal("a full list does not fit")
	}
	b, _ := json.Marshal(l)
	if len(b) > ListBytesMax {
		t.Fatalf("%d bytes", len(b))
	}

	// Ended ones past ListEndedMax go, oldest first; active ones never.
	var removed []string
	for i := range ListActiveMax {
		removed = append(removed, l.End(fmt.Sprint("con_", i), listAt.Add(time.Duration(i)*time.Second))...)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v at %d ended", removed, ListActiveMax)
	}
	if err := l.AddPending("con_more", "https://more.example/x", listAt); err != nil {
		t.Fatal(err)
	}
	if got := l.End("con_more", listAt.Add(time.Hour)); len(got) != 1 || got[0] != "con_0" {
		t.Fatalf("removed %v", got)
	}
	l.RemoveEnded([]string{"con_0", "con_replacing"})
	for _, m := range l.Members {
		if m.ID == "con_0" {
			t.Fatal("an ended one kept")
		}
		if m.ID == "con_replacing" && m.State != contracts.ConnectionActive {
			t.Fatal("an active one removed")
		}
	}

	// The fence.
	l.Closing, l.Generation = true, 3
	if err := l.AddPending("con_late", "https://late.example/x", listAt); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Fatalf("an approval past the fence: %v", err)
	}
	if err := l.Decide("late", listAt.Add(time.Hour)); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Fatalf("a decision past the fence: %v", err)
	}

	// Its epochs: the next carries everything, the fence included.
	if l.Rotate(ListEpochVersions-2) || !l.Rotate(ListEpochVersions-1) {
		t.Fatal("rotation")
	}
	n := l.Next()
	if n.Epoch != 1 || n.Moved || !n.Closing || n.Generation != 3 || len(n.Members) != len(l.Members) || len(n.Decided) != len(l.Decided) || n.Current["https://c1.example/x"] != "" {
		t.Fatalf("%+v", n)
	}
	if ListName("acc_a", 2) != "connections-acc_a-2" {
		t.Fatal(ListName("acc_a", 2))
	}
}
