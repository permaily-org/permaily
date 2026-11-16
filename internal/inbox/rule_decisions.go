package inbox

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// The records a protecting rule's decisions are kept in, and their state
// machine (ADR 0031, "Holds that cannot be undone"). services/mailboxes
// keeps, for each mailbox and day, a chain of blocks, each a strict anchored
// record (ADR 0016, 0026), sealed, read and written at the version read; the
// transitions are here, pure, so that its implementation (pull request 4),
// the fake of inbox/disclosetest and the tests of both follow one contract.
//
// A decision goes Pending (begun, before anything is asked) to Held or
// Cleared (settled, final, with the evidence it rests on), or is dropped
// (Abandoned) by the request that began it, and only on positive evidence
// that no answer of the model for it was received (SettleAfterAsk; ADR
// 0031, "Changed after the second design review", N1). Final decisions
// never change, and their evidence is kept with them (N4). A Pending older
// than PendingGrace is settled Held by whoever finds it, marked Recovered
// and with no evidence: its settlement was lost, and what it would have
// said cannot be recovered from the table, so it fails closed.
//
// A day's decisions are kept in blocks (the owner's decision 12): block 0
// first; when no block has room for a new decision, the last one is written
// Next and the next block made with CreateNew, up to DecisionBlocksMax. A
// decision stays in the block that began it. Every block of the day is read,
// strictly and now, before a decision is begun, so a decision is found
// wherever it is; which day holds a decision is in the message's identifier,
// so it cannot be changed by writing to the table.

// RecordRuleDecisions + the mailbox's identifier + "-" + the day (UTC,
// YYYYMMDD, of the message identifier's time) + "-" + the block (decimal,
// from 0) names one block of a day's decisions. A block that has none was
// never written, which a strict read tells from one lost.
const RecordRuleDecisions = "rule-decisions-"

// RuleDecisionsName is block of the day of the decisions about messageID.
func RuleDecisionsName(mailboxID, messageID string, block int) (string, error) {
	day, err := DecisionDay(messageID)
	if err != nil {
		return "", err
	}
	return RecordRuleDecisions + mailboxID + "-" + day + "-" + strconv.Itoa(block), nil
}

// DecisionDay is the day of messageID, UTC, as YYYYMMDD.
func DecisionDay(messageID string) (string, error) {
	if !isMessageIDShape(messageID) {
		return "", ErrNotAMessageID
	}
	return timeOfMessageID(messageID).Format("20060102"), nil
}

// ErrNotAMessageID is an identifier that is not one of a message.
var ErrNotAMessageID = errors.New("inbox: not a message identifier")

// Bounds of one block and of a day (findings 11 and N4, the owner's decision
// 12): how many decisions, how many bytes of JSON before it is sealed (a
// DynamoDB item holds 400 KiB with the record's other attributes; sealing
// adds a fixed overhead, not a factor, for a binary record), with every
// Pending counted at the size its settlement may reach, so a settlement
// always fits; how many versions (a strict read lists every version and
// fails past 10,000 objects), with one kept for the settlement of every
// Pending and one for writing the block Next, so a begun decision can
// always be settled; and how many blocks a day may have. A day past its
// last block's room answers Full to a new decision: the message is held,
// undecided, and the day recovers only as its decisions expire with their
// messages (dropped by a write made anyway, a begin's own included), which
// docs/data.md and ADR 0031 state. A settled decision of the default rule
// with its two answers is about 580 bytes, so a block filled by pages of
// DiscloseMaxLimit holds DecisionsMax - DiscloseMaxLimit to DecisionsMax
// settled ones, and a day at least DecisionsPerDay (28,800), far above any
// real mailbox's model-decided mail of one day; even with every decision
// pending at its largest at once, a block takes about 140.
const (
	DecisionsMax         = 500
	DecisionsBytesMax    = 300 << 10
	DecisionsVersionsMax = 9000
	DecisionBlocksMax    = 64
	// DecisionsPerDay is the least a mailbox's day holds of settled
	// decisions begun by pages of DiscloseMaxLimit (/status shows it).
	DecisionsPerDay = DecisionBlocksMax * (DecisionsMax - DiscloseMaxLimit)
	// DecisionEvidenceMax bounds the model's answers kept with one decision
	// (a rule's model atoms; the default rule has two), and
	// DecisionEvidenceBytesMax their JSON: a Pending is counted at
	// DecisionEvidenceBytesMax more than its own size. A settlement whose
	// evidence is past either is refused (the decision stays Pending, and
	// is recovered Held), never kept without its evidence.
	DecisionEvidenceMax      = 8
	DecisionEvidenceBytesMax = 2 << 10
	// PendingGrace is how long a decision may stay Pending before whoever
	// finds it settles it Held: longer than one message's ask
	// (decide.MessageDeadline, 20 seconds) and its settlement's tries.
	PendingGrace = 2 * time.Minute
	// DecisionTries bounds the reads again after store.ErrChanged.
	DecisionTries = 5
)

// DecisionState is where one decision is.
type DecisionState string

const (
	// Pending: begun and not settled. Stored.
	DecisionPending DecisionState = "pending"
	// Held and Cleared: settled, final. Stored.
	DecisionHeld    DecisionState = "held"
	DecisionCleared DecisionState = "cleared"
	// Begun: Begin's answer for a decision this call began (stored as
	// Pending). The caller may ask the model and must settle it, or leave
	// it Pending when SettleAfterAsk says so.
	DecisionBegun DecisionState = "begun"
	// Full: Begin's or Settle's answer when the day has no room; nothing
	// was stored for it.
	DecisionFull DecisionState = "full"
	// Abandoned: Settle's input for a decision the caller began and for
	// which SettleAfterAsk found positive evidence that no answer of the
	// model was received; its Pending is dropped. Settle answers it for an
	// id with nothing stored.
	DecisionAbandoned DecisionState = "abandoned"
)

// Decision is a decision as Begin and Settle answer it.
type Decision struct {
	State DecisionState
	// At is when it was begun (Pending, Begun) or settled (Held, Cleared).
	At time.Time
	// Evidence is what a final decision rests on, as its settlement kept
	// it (ADR 0031, N4): the model's answers, or its kept refusals, the
	// ask used. Recovered is a Held settled by whoever found its Pending
	// past PendingGrace: it rests on no answer that can be recovered, and
	// says so instead of naming one.
	Evidence  []Decided
	Recovered bool
}

// Settlement is what Settle is asked to commit for one decision: its state
// (Held, Cleared or Abandoned) and, for Held and Cleared, the evidence it
// rests on (Asked.Evidence of the rule's atoms). Held with no evidence is a
// recovery.
type Settlement struct {
	State    DecisionState
	Evidence []Decided
}

// DecisionRecord is one block's document, sealed in the store.
type DecisionRecord struct {
	V       int    `json:"v"`
	Mailbox string `json:"mailbox"`
	Day     string `json:"day"`
	Block   int    `json:"block"`
	// Next says block+1 was opened: it is read too. Written before the next
	// block is made, so a crash between the two leaves a Next whose block a
	// strict read finds never written, which the next writer makes.
	Next    bool            `json:"next,omitzero"`
	Entries []DecisionEntry `json:"entries"`
}

// DecisionEntry is one stored decision.
type DecisionEntry struct {
	Message string        `json:"m"`
	Rule    string        `json:"r"`
	Version int           `json:"rv"`
	State   DecisionState `json:"s"`
	At      time.Time     `json:"at"`
	// Expires is the message's; the entry is dropped from the first version
	// written after it.
	Expires   time.Time          `json:"x,omitzero"`
	Evidence  []DecisionEvidence `json:"e,omitempty"`
	Recovered bool               `json:"rc,omitzero"`
}

// DecisionEvidence is one answer kept with a final decision: inbox.Evidence
// with its atom, in short names.
type DecisionEvidence struct {
	Var           string    `json:"v"`
	Op            string    `json:"o"`
	Topic         string    `json:"t"`
	ByRule        bool      `json:"br,omitzero"`
	Refused       bool      `json:"rf,omitzero"`
	P             float64   `json:"p,omitzero"`
	Model         string    `json:"m,omitempty"`
	Prompt        string    `json:"pv,omitempty"`
	At            time.Time `json:"at,omitzero"`
	CanaryChecked string    `json:"cc,omitempty"`
	CanaryChange  string    `json:"cx,omitempty"`
}

func storedEvidence(ds []Decided) []DecisionEvidence {
	if len(ds) == 0 {
		return nil
	}
	out := make([]DecisionEvidence, len(ds))
	for i, d := range ds {
		e := d.Evidence
		out[i] = DecisionEvidence{
			Var: string(d.Atom.Var), Op: string(d.Atom.Op), Topic: d.Atom.Topic,
			ByRule: e.ByRule, Refused: e.Refused, P: e.P, Model: e.Model, Prompt: e.Prompt, At: e.At,
			CanaryChecked: e.CanaryChecked, CanaryChange: e.CanaryChange,
		}
	}
	return out
}

func (e DecisionEntry) decision() Decision {
	d := Decision{State: e.State, At: e.At, Recovered: e.Recovered}
	for _, s := range e.Evidence {
		d.Evidence = append(d.Evidence, Decided{
			Atom: query.Atom{Var: contracts.Variable(s.Var), Op: contracts.Operator(s.Op), Topic: s.Topic},
			Evidence: Evidence{
				ByRule: s.ByRule, Refused: s.Refused, P: s.P, Model: s.Model, Prompt: s.Prompt, At: s.At,
				CanaryChecked: s.CanaryChecked, CanaryChange: s.CanaryChange,
			},
		})
	}
	return d
}

// evidenceFits says a settlement's evidence is within its bounds.
func evidenceFits(ds []Decided) bool {
	if len(ds) > DecisionEvidenceMax {
		return false
	}
	b, err := json.Marshal(storedEvidence(ds))
	return err == nil && len(b) <= DecisionEvidenceBytesMax
}

func (r *DecisionRecord) find(rule rules.Ref, id string) int {
	for i, e := range r.Entries {
		if e.Message == id && e.Rule == rule.ID && e.Version == rule.Version {
			return i
		}
	}
	return -1
}

func (r *DecisionRecord) prune(now time.Time) {
	kept := r.Entries[:0]
	for _, e := range r.Entries {
		if e.Expires.IsZero() || now.Before(e.Expires) {
			kept = append(kept, e)
		}
	}
	r.Entries = kept
}

func (r *DecisionRecord) pending() int {
	n := 0
	for _, e := range r.Entries {
		if e.State == DecisionPending {
			n++
		}
	}
	return n
}

// fits says the block is within its count and its bytes, every Pending
// counted at its largest settlement.
func (r *DecisionRecord) fits() bool {
	if len(r.Entries) > DecisionsMax {
		return false
	}
	b, err := json.Marshal(r)
	return err == nil && len(b)+r.pending()*DecisionEvidenceBytesMax <= DecisionsBytesMax
}

// versionsFor says a block at versions can take one more write that leaves
// pending decisions in it: one version kept for each one's settlement and
// one for writing it Next.
func versionsFor(versions uint64, pending int) bool {
	if pending < 0 || pending > DecisionsMax {
		return false
	}
	return versions+1+uint64(pending)+1 <= DecisionsVersionsMax //nolint:gosec // G115: pending is bounded above
}

// Begin is the transition of Decisions.Begin on one block read at versions
// (the block's committed versions, 0 for none): it answers each id and says
// whether the block changed and must be written at the version read (a
// write refused with store.ErrChanged is read again and begun again, at
// most DecisionTries times; a write that fails otherwise holds every id of
// it, undecided). ids must all be of this block's day. An id the block has
// no room or versions for is answered Full; DecisionBlocks.Begin then tries
// the day's other blocks.
func (r *DecisionRecord) Begin(rule rules.Ref, ids []DecisionKey, now time.Time, versions uint64) (map[string]Decision, bool) {
	out := make(map[string]Decision, len(ids))
	var fresh []DecisionKey
	for _, k := range ids {
		if _, seen := out[k.ID]; seen {
			continue
		}
		if i := r.find(rule, k.ID); i >= 0 {
			out[k.ID] = r.Entries[i].decision()
			continue
		}
		out[k.ID] = Decision{State: DecisionFull}
		fresh = append(fresh, k)
	}
	if len(fresh) == 0 {
		return out, false
	}
	// Pruning is kept only with a write made anyway.
	before := append([]DecisionEntry(nil), r.Entries...)
	r.prune(now)
	began := false
	for _, k := range fresh {
		if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
			continue // its message has expired: nothing to decide, held
		}
		r.Entries = append(r.Entries, DecisionEntry{Message: k.ID, Rule: rule.ID, Version: rule.Version, State: DecisionPending, At: now, Expires: k.ExpiresAt})
		if !r.fits() || !versionsFor(versions, r.pending()) {
			r.Entries = r.Entries[:len(r.Entries)-1]
			continue
		}
		out[k.ID] = Decision{State: DecisionBegun, At: now}
		began = true
	}
	if !began {
		r.Entries = before
	}
	return out, began
}

// Settle is the transition of Decisions.Settle on one block read at
// versions. For each id: a Pending becomes what its settlement says (Held
// or Cleared with its evidence, or dropped for Abandoned); a final decision
// stays as it is and is answered; an id with nothing stored is answered
// Abandoned, and for Held a final Held is stored when the block has room
// (it fails closed; DecisionBlocks.Settle tries the day's other blocks).
// Held with no evidence is stored Recovered. A settlement whose evidence is
// past its bounds changes nothing: the Pending stays, and is recovered Held.
// A caller settles Held a Pending it did not begin only once it is older
// than PendingGrace (OverdueAt).
func (r *DecisionRecord) Settle(rule rules.Ref, settle map[string]Settlement, now time.Time, versions uint64) (map[string]Decision, bool) {
	out := make(map[string]Decision, len(settle))
	writable := versions+1 <= DecisionsVersionsMax
	changed := false
	for id, want := range settle {
		i := r.find(rule, id)
		final := want.State == DecisionHeld || want.State == DecisionCleared
		switch {
		case i >= 0 && r.Entries[i].State != DecisionPending:
			out[id] = r.Entries[i].decision()
		case i >= 0 && (!writable || (final && !evidenceFits(want.Evidence))):
			out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
		case i >= 0 && want.State == DecisionAbandoned:
			r.Entries = append(r.Entries[:i], r.Entries[i+1:]...)
			out[id] = Decision{State: DecisionAbandoned}
			changed = true
		case i >= 0 && final:
			e := &r.Entries[i]
			e.State, e.At = want.State, now
			e.Evidence, e.Recovered = storedEvidence(want.Evidence), want.State == DecisionHeld && len(want.Evidence) == 0
			out[id] = e.decision()
			changed = true
		case i >= 0:
			out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
		case want.State == DecisionHeld && writable && evidenceFits(want.Evidence):
			r.Entries = append(r.Entries, DecisionEntry{
				Message: id, Rule: rule.ID, Version: rule.Version, State: DecisionHeld, At: now,
				Evidence: storedEvidence(want.Evidence), Recovered: len(want.Evidence) == 0,
			})
			if r.fits() {
				out[id] = r.Entries[len(r.Entries)-1].decision()
				changed = true
			} else {
				r.Entries = r.Entries[:len(r.Entries)-1]
				out[id] = Decision{State: DecisionFull}
			}
		case want.State == DecisionHeld:
			out[id] = Decision{State: DecisionFull}
		default:
			out[id] = Decision{State: DecisionAbandoned}
		}
	}
	if changed {
		r.prune(now)
	}
	return out, changed
}

// DecisionBlocks is one mailbox-day's chain of blocks as read, strictly and
// now, by following Next from block 0: Blocks[i] is block i, Versions[i] its
// committed versions. The real Decisions (pull request 4) and the fake
// apply these transitions and write the blocks they say changed, in their
// order: an existing block at the version read, a new one (Versions 0) with
// CreateNew once the block before it is written Next.
type DecisionBlocks struct {
	Mailbox  string
	Day      string
	Blocks   []*DecisionRecord
	Versions []uint64
}

// find is the block holding id's decision of rule, -1 for none.
func (d *DecisionBlocks) find(rule rules.Ref, id string) int {
	for b, r := range d.Blocks {
		if r.find(rule, id) >= 0 {
			return b
		}
	}
	return -1
}

// open makes the next block, writing the last one Next, when the day may
// have one more; it answers the new block's index, -1 when it may not.
func (d *DecisionBlocks) open(changed map[int]bool) int {
	n := len(d.Blocks)
	if n >= DecisionBlocksMax {
		return -1
	}
	if n > 0 {
		last := d.Blocks[n-1]
		if !last.Next {
			if d.Versions[n-1]+1 > DecisionsVersionsMax {
				return -1
			}
			last.Next = true
			changed[n-1] = true
		}
	}
	d.Blocks = append(d.Blocks, &DecisionRecord{V: 1, Mailbox: d.Mailbox, Day: d.Day, Block: n})
	d.Versions = append(d.Versions, 0)
	return n
}

// Begin is Decisions.Begin on one day: every id found in any block is
// answered as it is; the others are begun in the first block with room,
// a new block opened when none has; Full past DecisionBlocksMax. It answers
// the blocks to write, in order.
func (d *DecisionBlocks) Begin(rule rules.Ref, ids []DecisionKey, now time.Time) (map[string]Decision, []int) {
	out := make(map[string]Decision, len(ids))
	changed := map[int]bool{}
	var todo []DecisionKey
	for _, k := range ids {
		if _, seen := out[k.ID]; seen {
			continue
		}
		if b := d.find(rule, k.ID); b >= 0 {
			out[k.ID] = d.Blocks[b].Entries[d.Blocks[b].find(rule, k.ID)].decision()
			continue
		}
		out[k.ID] = Decision{State: DecisionFull}
		todo = append(todo, k)
	}
	for b := 0; len(todo) > 0; b++ {
		if b == len(d.Blocks) && d.open(changed) < 0 {
			break
		}
		got, wrote := d.Blocks[b].Begin(rule, todo, now, d.Versions[b])
		if wrote {
			changed[b] = true
		}
		var left []DecisionKey
		for _, k := range todo {
			if got[k.ID].State == DecisionFull {
				left = append(left, k)
				continue
			}
			out[k.ID] = got[k.ID]
		}
		if len(left) == len(todo) && len(d.Blocks[b].Entries) == 0 {
			// An empty block took none: what is left cannot be begun
			// anywhere (its message has expired). Stop.
			break
		}
		todo = left
	}
	return out, sorted(changed)
}

// Settle is Decisions.Settle on one day: each id is settled in the block
// that holds it; a Held for an id no block holds is stored in the first
// block with room (a new one opened when none has).
func (d *DecisionBlocks) Settle(rule rules.Ref, settle map[string]Settlement, now time.Time) (map[string]Decision, []int) {
	out := make(map[string]Decision, len(settle))
	changed := map[int]bool{}
	byBlock := map[int]map[string]Settlement{}
	var unknown []string
	for id, s := range settle {
		b := d.find(rule, id)
		if b < 0 {
			if s.State != DecisionHeld {
				out[id] = Decision{State: DecisionAbandoned}
				continue
			}
			unknown = append(unknown, id)
			continue
		}
		if byBlock[b] == nil {
			byBlock[b] = map[string]Settlement{}
		}
		byBlock[b][id] = s
	}
	for b, s := range byBlock {
		got, wrote := d.Blocks[b].Settle(rule, s, now, d.Versions[b])
		if wrote {
			changed[b] = true
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	for _, id := range unknown {
		out[id] = Decision{State: DecisionFull}
		for b := 0; ; b++ {
			if b == len(d.Blocks) && d.open(changed) < 0 {
				break
			}
			got, wrote := d.Blocks[b].Settle(rule, map[string]Settlement{id: settle[id]}, now, d.Versions[b])
			if got[id].State != DecisionFull {
				out[id] = got[id]
				if wrote {
					changed[b] = true
				}
				break
			}
			if b == len(d.Blocks)-1 && len(d.Blocks[b].Entries) == 0 {
				break
			}
		}
	}
	return out, sorted(changed)
}

func sorted(m map[int]bool) []int {
	var out []int
	for i := range DecisionBlocksMax {
		if m[i] {
			out = append(out, i)
		}
	}
	return out
}

// SettleAfterAsk is how a decision is settled once the discloser has asked
// for it (ADR 0031, "Ask and settle", N1): r is the rule's formulas
// evaluated for a rule (query.ForRule) with a.Answers, atoms the rule's
// model atoms asked, a and err what Ask answered. True, or Unknown with
// every atom it needs answered in doubt or refused, is Held; False is
// Cleared; each with the evidence of atoms. Unknown because an atom has no
// answer is Abandoned only on positive evidence that none was received:
// Ask failed (it fails only before asking anything, Asker's contract), or
// none of those atoms is in a.Unsettled. Otherwise the decision is left
// Pending (State Pending: do not settle it): this request holds the message,
// undecided, and the first request after PendingGrace settles it Held.
func SettleAfterAsk(r query.Result, atoms []query.Atom, a *Asked, err error) Settlement {
	var ev []Decided
	if a != nil {
		for _, atom := range atoms {
			if e, ok := a.Evidence[atom]; ok {
				ev = append(ev, Decided{Atom: atom, Evidence: e})
			}
		}
	}
	switch r.Value {
	case query.True:
		return Settlement{State: DecisionHeld, Evidence: ev}
	case query.False:
		return Settlement{State: DecisionCleared, Evidence: ev}
	}
	var missing []query.Atom
	for _, atom := range r.Model {
		if a == nil {
			missing = append(missing, atom)
			continue
		}
		if _, ok := a.Evidence[atom]; !ok {
			missing = append(missing, atom)
		}
	}
	if len(missing) == 0 {
		return Settlement{State: DecisionHeld, Evidence: ev}
	}
	if err != nil || a == nil {
		return Settlement{State: DecisionAbandoned}
	}
	for _, atom := range missing {
		for _, u := range a.Unsettled {
			if u == atom {
				return Settlement{State: DecisionPending}
			}
		}
	}
	return Settlement{State: DecisionAbandoned}
}

// OverdueAt says a Pending decision begun at began is to be settled Held by
// whoever finds it at now.
func OverdueAt(began, now time.Time) bool { return !now.Before(began.Add(PendingGrace)) }

// Leaves says a message may leave on this decision: only a final Cleared.
func (d Decision) Leaves() bool { return d.State == DecisionCleared }
