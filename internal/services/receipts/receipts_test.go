package receipts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
)

// Finding 10: the receipt of one message at the bounds a stored message may
// hold (ADR 0026: a From field of 16 KiB with 100 addresses, a subject of
// 2 KiB), every byte escaped as JSON escapes the worst, with the model's
// evidence and 50 held messages beside it, fits MaxDocumentBytes: the
// service can always end a page after at least one message, and never
// writes a receipt it would have to cut.
func TestTheLargestMessageFitsOneReceipt(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	worst := func(n int) string { return strings.Repeat("\x01", n) } // "\u0001": six bytes each
	var from []contracts.MailAddress
	for range 100 {
		from = append(from, contracts.MailAddress{Name: worst(16<<10/100 - 40), Address: strings.Repeat("a", 30) + "@example.org"})
	}
	subject := worst(2 << 10)
	model := []Model{{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: strings.Repeat("t", 200), P: 0.123, Name: "gpt-6-luna", Prompt: "p2-2026-11-15", CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}}
	r := Receipt{
		V: Version, ID: "rcp_" + strings.Repeat("0", 40), At: at, Kind: KindSearch, Connection: "con_" + strings.Repeat("a", 26), Device: "fam_" + strings.Repeat("a", 26),
		ClientID: "https://" + strings.Repeat("c", 400) + ".example/x", Vendor: "example", Resource: contracts.ResourceAPI,
		Rules: []rules.Ref{{ID: rules.DefaultID, Version: rules.DefaultVersion}},
		Delivered: []Delivered{{
			Mailbox: "mbx_" + strings.Repeat("a", 26), Message: strings.Repeat("f", 32), Received: at,
			Parts: []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments},
			From:  from, Subject: &subject, Model: model, Cleared: []Cleared{{Rule: rules.Ref{ID: rules.DefaultID, Version: 1}, At: at, Model: append(model, model...)}},
		}},
	}
	for range 50 {
		r.Held = append(r.Held, Held{Mailbox: "mbx_" + strings.Repeat("a", 26), Message: strings.Repeat("f", 32), Rule: rules.Ref{ID: rules.DefaultID, Version: 1}, Why: "decided", Model: append(model, model...)})
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxDocumentBytes {
		t.Fatalf("%d bytes, more than %d", len(b), MaxDocumentBytes)
	}
	// Sealed as text (base64 and the envelope's own bytes), with the
	// entry's other attributes, it fits one DynamoDB item.
	if sealed := (MaxDocumentBytes+64)*4/3 + 1024; sealed > 400<<10 {
		t.Fatalf("a full receipt seals to %d bytes", sealed)
	}
}
