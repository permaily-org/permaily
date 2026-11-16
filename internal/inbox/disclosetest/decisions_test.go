package disclosetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/rules"
)

func TestTheFakeKeepsTheContract(t *testing.T) {
	Contract(t, func(now func() time.Time) inbox.Decisions { return NewDecisions(now) })
}

// Each day is its own record, and one that cannot be read fails the call:
// the caller holds every message of it.
func TestOneRecordADay(t *testing.T) {
	at := time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)
	d := NewDecisions(func() time.Time { return at })
	box := inbox.Box{MailboxID: "mbx_a"}
	rule := rules.Ref{ID: "r", Version: 1}
	late, early := inbox.NewMessageID(at), inbox.NewMessageID(at.Add(2*time.Second))
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: late}, {ID: early}}); err != nil {
		t.Fatal(err)
	}
	if d.Versions(box, late, 0) != 1 || d.Versions(box, early, 0) != 1 || d.Blocks(box, late) != 1 {
		t.Fatalf("versions %d %d", d.Versions(box, late, 0), d.Versions(box, early, 0))
	}
	d.Fail = errors.New("lost")
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: late}}); err == nil {
		t.Fatal("a record that cannot be read")
	}
}
