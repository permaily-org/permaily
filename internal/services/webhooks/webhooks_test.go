package webhooks

import (
	"fmt"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/webhook"
)

// The service's errors are of a fixed kind, logged as themselves.
func TestAnErrorIsLoggedAsItsKind(t *testing.T) {
	for _, err := range []error{ErrNoWebhook, ErrRulesUnavailable} {
		if err.(interface{ LogKind() string }).LogKind() != err.Error() {
			t.Error("a fixed kind is logged as itself")
		}
	}
}

// A position orders by received time, then mailbox, then message: a burst
// in one instant has a stable order (design review, finding 3).
func TestACursorOrdersEveryMessage(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	in := []Cursor{
		{},
		{Received: at, Mailbox: "mbx_a", Message: "msg_1"},
		{Received: at, Mailbox: "mbx_a", Message: "msg_2"},
		{Received: at, Mailbox: "mbx_b", Message: "msg_0"},
		{Received: at.Add(time.Nanosecond), Mailbox: "mbx_a", Message: "msg_0"},
	}
	for i := range in {
		for j := range in {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := in[i].Compare(in[j]); got != want {
				t.Errorf("%d against %d: %d, want %d", i, j, got, want)
			}
		}
	}
}

// The late sweep's window keeps what was evaluated within ArrivalLag before
// the cursor, at most RecentMax, and moves LateFrom past what it drops, so
// a dropped message is never evaluated again and the cursor never waits.
func TestTheLateSweepsWindowIsBounded(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	cursor := Cursor{Received: at, Mailbox: "mbx_a", Message: "msg_999"}
	var recent []Cursor
	for i := range RecentMax + 10 {
		recent = append(recent, Cursor{Received: at.Add(-time.Duration(RecentMax+10-i) * time.Millisecond), Mailbox: "mbx_a", Message: fmt.Sprintf("msg_%03d", i)})
	}
	old := Cursor{Received: at.Add(-webhook.ArrivalLag - time.Second), Mailbox: "mbx_a", Message: "msg_old"}
	kept, lateFrom := Window(cursor, append(recent, old, recent[3]), time.Time{})
	if len(kept) != RecentMax || kept[0] != recent[10] || kept[len(kept)-1] != recent[len(recent)-1] {
		t.Fatalf("kept %d: %+v … %+v", len(kept), kept[0], kept[len(kept)-1])
	}
	if !lateFrom.After(recent[9].Received) || lateFrom.After(recent[10].Received) {
		t.Fatalf("late from %s, past the dropped %s and not past the kept %s", lateFrom, recent[9].Received, recent[10].Received)
	}
	// Within its bound nothing is dropped and LateFrom stays.
	if kept, lf := Window(cursor, recent[:5], time.Time{}); len(kept) != 5 || !lf.IsZero() {
		t.Fatalf("%d %s", len(kept), lf)
	}
	// The cursor moved past the lag: the window is empty.
	if kept, _ := Window(Cursor{Received: at.Add(webhook.ArrivalLag + time.Second)}, recent, time.Time{}); len(kept) != 0 {
		t.Fatalf("%d kept past the lag", len(kept))
	}
}
