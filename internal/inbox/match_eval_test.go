package inbox_test

import (
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// The matcher on what query.Eval (pull request 1) decides of stored mail
// that the matcher's first tests, written against a stand-in of its
// contract, did not: addresses and signers as delivery stores them that are
// not valid, and a message whose header could not be read.

// An address stored with a quotation mark or a backslash left in its decoded
// local part, or with scripts mixed, and a signer's domain with scripts
// mixed, are not valid: every condition over them is unknown, for the
// condition and its negation alike, and the message is listed as unknown,
// never as a match nor passed over.
func TestMatchStoredValuesThatAreNotValid(t *testing.T) {
	for _, c := range []struct {
		name   string
		header string // the From header as sent
		signer string // a DKIM signature that passed, when not ""
		conds  []contracts.Condition
	}{
		{"a quoted local part", `"\"alice\""@example.com`, "", []contracts.Condition{
			cond(contracts.VarFromAddress, contracts.OpIs, "alice@example.com"), cond(contracts.VarFromDomain, contracts.OpIs, "example.com"),
		}},
		{"a backslash", `"al\\ice"@example.com`, "", []contracts.Condition{
			cond(contracts.VarFromAddress, contracts.OpIs, "alice@example.com"), cond(contracts.VarFromDomain, contracts.OpIs, "example.com"),
		}},
		{"a Cyrillic o in the local part", "invоice@example.com", "", []contracts.Condition{
			cond(contracts.VarFromAddress, contracts.OpIs, "invoice@example.com"), cond(contracts.VarFromDomain, contracts.OpIs, "example.com"),
		}},
		{"a Cyrillic a in a signer's label", "alice@example.com", "pаy.example.com", []contracts.Condition{
			cond(contracts.VarSignedBy, contracts.OpIs, "example.com"),
		}},
	} {
		st := newQStore()
		b := qBox(t)
		q := &qMsg{at: qAt(0), header: c.header, subject: "x", body: "y"}
		if c.signer != "" {
			q.signedBy = []string{c.signer}
		}
		meta := q.meta()
		if c.signer == "" && (len(meta.From) != 1 || meta.From[0].Address == "") {
			t.Fatalf("%s: delivery stored %+v", c.name, meta.From)
		}
		id := seedQ(t, st, b, q)
		mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
		for _, cd := range c.conds {
			for _, f := range []contracts.Condition{cd, not(cd)} {
				if l := listedFor(t, mt, b, []*query.Canonical{formula(group(f))}, id); l != listedUnknown {
					t.Errorf("%s, %+v: listed %v, want unknown", c.name, f, l)
				}
			}
		}
		// Settled by another group, the value no longer depends on them.
		subject := cond(contracts.VarSubject, contracts.OpContains, "x")
		if l := listedFor(t, mt, b, []*query.Canonical{formula(group(c.conds[0], subject))}, id); l != listedMatch {
			t.Errorf("%s, or the subject: listed %v, want a match", c.name, l)
		}
	}
}

// A message whose header could not be read (Unparsed) is unknown on every
// condition but those on its item, a model's included: no atom is asked of
// it, so it is listed as unknown, never as needing a model, and nothing more
// is read of it. A condition on its item still settles it.
func TestMatchAnUnparsedMessageNeedsNoModel(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	q := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: "invoices", unparsed: true}
	id := seedQ(t, st, b, q)
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	about := cond(contracts.VarMessage, contracts.OpAbout, "invoices")
	body := cond(contracts.VarBody, contracts.OpContains, "invoices")
	recent := cond(contracts.VarReceived, contracts.OpSince, rfc(qAt(1)))
	for _, c := range []struct {
		name string
		fs   []*query.Canonical
		want listing
	}{
		{"about", []*query.Canonical{formula(group(about))}, listedUnknown},
		{"not about", []*query.Canonical{formula(group(not(about)))}, listedUnknown},
		{"subject about", []*query.Canonical{formula(group(cond(contracts.VarSubject, contracts.OpAbout, "invoices")))}, listedUnknown},
		{"about or the body", []*query.Canonical{formula(group(about, body))}, listedUnknown},
		{"about, in its mailbox", []*query.Canonical{formula(group(about), group(cond(contracts.VarMailbox, contracts.OpIs, b.MailboxID)))}, listedUnknown},
		{"about or received since", []*query.Canonical{formula(group(about, recent))}, listedMatch},
		{"about, and received before", []*query.Canonical{formula(group(about), group(cond(contracts.VarReceived, contracts.OpBefore, rfc(qAt(1)))))}, listedNone},
	} {
		st.reset()
		if l := listedFor(t, mt, b, c.fs, id); l != c.want {
			t.Errorf("%s: listed %v, want %v", c.name, l, c.want)
		}
		if len(st.entries)+len(st.raws) != 0 {
			t.Errorf("%s: %d entries and %d raw messages read", c.name, len(st.entries), len(st.raws))
		}
		meta := q.meta()
		if res := query.Eval(c.fs, inbox.QueryMessage(&store.Message{MailboxID: b.MailboxID, ID: id}, &meta, nil, nil), nil, query.ForQuery); len(res.Model) != 0 {
			t.Errorf("%s: Eval asks %v of it", c.name, res.Model)
		}
	}
}
