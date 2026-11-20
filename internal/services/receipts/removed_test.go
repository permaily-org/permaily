package receipts

import (
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// U24: a message that left with details removed is recorded with what each
// rule removed and the answers it rested on, never the removed content, and
// with the subject and senders that left.
func TestU24DeliveredKeepsWhatEachRuleRemoved(t *testing.T) {
	subject := "Call [removed]"
	atom := query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a phone number"}
	d := &inbox.Disclosed{ID: "m1", Message: contracts.VendorMessage{Subject: &subject, From: []contracts.MailAddress{}}, Removed: []inbox.RemovedBy{
		{Rule: rules.Ref{ID: "rul_a", Version: 2}, Removed: contracts.RemovedSummary{Parts: []contracts.Part{contracts.PartSender}, Shapes: map[contracts.ShapeKind]int{contracts.ShapePhoneNumber: 1}}},
		{Rule: rules.Ref{ID: "rul_b", Version: 1}, Removed: contracts.RemovedSummary{Sentences: 1, Of: 3}, Decided: []inbox.Decided{{Atom: atom, Evidence: inbox.Evidence{P: 0.1, Model: "m"}}}},
	}}
	got := DeliveredOf("mbx_a", d, []contracts.Part{contracts.PartSender, contracts.PartSubject})
	if len(got.Removed) != 2 || got.Removed[0].Rule.ID != "rul_a" || got.Removed[0].Removed.Shapes[contracts.ShapePhoneNumber] != 1 ||
		got.Removed[1].Removed.Of != 3 || len(got.Removed[1].Model) != 1 || got.Removed[1].Model[0].P != 0.1 {
		t.Fatalf("%+v", got.Removed)
	}
	if got.Subject == nil || *got.Subject != subject || len(got.From) != 0 {
		t.Fatalf("not what left: %+v", got)
	}
}
