package inbox

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
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
// 0031, "Changed after the second design review", N1). Each Pending keeps
// the attempt that began it (Attempt), and only a settlement naming that
// attempt drops or clears it ("Changed after the fourth design review",
// F1). Final decisions never change, and their evidence is kept with them
// (N4). A Pending older
// than PendingGrace is settled Held by whoever finds it, marked Recovered
// and with no evidence: its settlement was lost, and what it would have
// said cannot be recovered from the table, so it fails closed.
//
// A day's decisions are kept in blocks (the owner's decision 12), a chain
// from block 0, each written Next once its successor is opened. Only the
// chain's tail, the one block not written Next, ever takes a new decision,
// and opening its successor is itself a write of the tail at the version
// read: so every new decision of a day is written to one record at a time,
// under that record's version, and two requests can never begin one message
// in two blocks (ADR 0031, "Changed after the third design review", R1). A
// decision stays in the block that began it; every block of the day is read,
// strictly and now, before one is begun, so a decision is found wherever it
// is; which day holds a decision is in the message's identifier, so it
// cannot be changed by writing to the table. Should one message ever be
// found in two blocks, its most protective decision is answered and neither
// is written (DecisionBlocks.lookup).

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
	return decisionsName(mailboxID, day, block), nil
}

func decisionsName(mailboxID, day string, block int) string {
	return RecordRuleDecisions + mailboxID + "-" + day + "-" + strconv.Itoa(block)
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
// always be settled and the tail can always open its successor; and how
// many blocks a day may have. A tail out of room, bytes or versions opens
// its successor (R3), unless it is out of bytes only for what its live
// Pending keep for their settlements: then it waits for them, and what it
// cannot take now is Full for this request (F2, waits); past the last
// block a new decision is Full: the message is held, undecided. A block's
// room comes back as its decisions expire with their messages (dropped by a
// write made anyway, a begin's own included), which docs/data.md and ADR
// 0031 state; its versions never come back (a commit is history), and only
// the tail's room is used for new decisions, so a full day takes new ones
// again as its last block's decisions expire. A settled decision of the
// default rule with its two answers is about 580 bytes, so DecisionsMax of
// them fit a block's bytes and a tail filled by pages of DiscloseMaxLimit
// is retired at DecisionsMax: a day holds at least DecisionsPerDay
// (28,800), far above any real mailbox's model-decided mail of one day;
// even with every decision pending at its largest at once, a block takes
// about 140.
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
	// DecisionKeptAfter is how long a decision outlives its message before a
	// write drops it: far longer than two enclaves' clocks differ, so a
	// request whose clock is behind never finds a message still there and
	// its decision already gone (ADR 0031, "Changed after the third design
	// review"). Begin refuses a message already expired by its own clock.
	DecisionKeptAfter = time.Hour
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
	// Expired: Begin's answer for a message already expired by the
	// caller's clock: nothing is stored or decided, and it does not leave.
	// Told from Full, which is the day's room (R3).
	DecisionExpired DecisionState = "expired"
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
	// Attempt is, for Begun, the attempt this call began: its caller gives
	// it back in the Settlement, so that its settlement changes only its
	// own Pending (ADR 0031, "Changed after the fourth design review", F1).
	Attempt Attempt
}

// Attempt names one begun Pending: the block it was begun in and the
// version of that block its Begin wrote. A block's committed writes are
// each made at a version no other was read at, so no two attempts of a day
// share one. A Settlement that drops or clears a Pending names the attempt
// that began it, and changes nothing on another's (F1); the zero Attempt
// names none.
type Attempt struct {
	Block   int
	Version uint64
}

// Settlement is what Settle is asked to commit for one decision: its state
// (Held, Cleared or Abandoned) and, for Held and Cleared, the evidence it
// rests on (Asked.Evidence of the rule's atoms). Held with no evidence is a
// recovery.
type Settlement struct {
	State    DecisionState
	Evidence []Decided
	// Attempt is the Begun decision's: Abandoned and Cleared change only
	// the Pending it began. Held needs none (it settles whatever Pending it
	// finds, as a recovery does).
	Attempt Attempt
}

// DecisionRecord is one block's document, sealed in the store.
type DecisionRecord struct {
	V       int    `json:"v"`
	Mailbox string `json:"mailbox"`
	Day     string `json:"day"`
	Block   int    `json:"block"`
	// Next says block+1 was opened: it is read too, and this block takes no
	// new decision again (only the tail does, R1). Written before the next
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
	// written DecisionKeptAfter after it.
	Expires   time.Time          `json:"x,omitzero"`
	Evidence  []DecisionEvidence `json:"e,omitempty"`
	Recovered bool               `json:"rc,omitzero"`
	// Attempt is, for a Pending, the version of its block its Begin wrote
	// (Attempt.Version); dropped when it is settled.
	Attempt uint64 `json:"ag,omitzero"`
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

// began says entry i is the Pending attempt a began (F1).
func (r *DecisionRecord) began(i int, a Attempt) bool {
	return a.Version != 0 && a.Block == r.Block && r.Entries[i].Attempt == a.Version
}

func (r *DecisionRecord) prune(now time.Time) {
	kept := r.Entries[:0]
	for _, e := range r.Entries {
		if e.Expires.IsZero() || now.Before(e.Expires.Add(DecisionKeptAfter)) {
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
	return pending >= 0 && pending <= DecisionsMax && versions+1+uint64(pending)+1 <= DecisionsVersionsMax //nolint:gosec // G115: pending is bounded first
}

// Begin is the transition of Decisions.Begin on one block read at versions
// (the block's committed versions, 0 for none): it answers each id and says
// whether the block changed and must be written at the version read (a
// write refused with store.ErrChanged is read again and begun again, at
// most DecisionTries times; a write that fails otherwise holds every id of
// it, undecided). ids must all be of this block's day. An id whose message
// has expired by now is answered Expired; one the block has no room or
// versions for, and every new one when the block is written Next (only the
// tail takes new decisions), Full: DecisionBlocks.Begin then opens the
// tail's successor, unless the tail waits for its Pending (waits). Each id
// begun is answered with its Attempt, kept with its Pending.
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
		if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
			out[k.ID] = Decision{State: DecisionExpired} // nothing to decide, and it does not leave
			continue
		}
		out[k.ID] = Decision{State: DecisionFull}
		fresh = append(fresh, k)
	}
	if len(fresh) == 0 || r.Next {
		return out, false
	}
	// Pruning is kept only with a write made anyway.
	before := append([]DecisionEntry(nil), r.Entries...)
	r.prune(now)
	// The block's bytes, counted once and then by each entry added (one
	// more for its comma: never fewer than the block's JSON).
	size, err := json.Marshal(r)
	n, pending := len(size), r.pending()
	attempt := Attempt{Block: r.Block, Version: versions + 1}
	began := false
	for _, k := range fresh {
		e := pendingEntry(rule, k, now)
		e.Attempt = attempt.Version
		b, errEntry := json.Marshal(e)
		if err != nil || errEntry != nil || len(r.Entries)+1 > DecisionsMax ||
			n+len(b)+1+(pending+1)*DecisionEvidenceBytesMax > DecisionsBytesMax || !versionsFor(versions, pending+1) {
			continue
		}
		r.Entries = append(r.Entries, e)
		n, pending = n+len(b)+1, pending+1
		out[k.ID] = Decision{State: DecisionBegun, At: now, Attempt: attempt}
		began = true
	}
	if !began {
		r.Entries = before
	}
	return out, began
}

// waits says the tail, with no room for e now, is out of it only for what
// its live Pending (younger than PendingGrace) keep for their settlements:
// it will take e once they settle, so it is not retired for them (ADR
// 0031, "Changed after the fourth design review", F2), and e is held for
// now (Full). A tail out of decisions or versions for e, or out of bytes
// even with its live Pending counted at their present size (an overdue one
// still at its largest, since its settlement may never come), does not
// wait: its successor is opened (R3), so a lost settlement never stops the
// day.
func (r *DecisionRecord) waits(e DecisionEntry, now time.Time, versions uint64) bool {
	c := DecisionRecord{V: r.V, Mailbox: r.Mailbox, Day: r.Day, Block: r.Block, Entries: slices.Clone(r.Entries)}
	c.prune(now)
	c.Entries = append(c.Entries, e)
	live, kept := 0, 0
	for _, x := range c.Entries {
		switch {
		case x.State != DecisionPending:
		case x.Message != e.Message && !OverdueAt(x.At, now):
			live++
		default:
			kept++
		}
	}
	if r.Next || live == 0 || len(c.Entries) > DecisionsMax || !versionsFor(versions, live+kept) {
		return false
	}
	b, err := json.Marshal(&c)
	return err == nil && len(b)+kept*DecisionEvidenceBytesMax <= DecisionsBytesMax
}

// pendingEntry and heldEntry are the entries Begin and Settle add.
func pendingEntry(rule rules.Ref, k DecisionKey, now time.Time) DecisionEntry {
	return DecisionEntry{Message: k.ID, Rule: rule.ID, Version: rule.Version, State: DecisionPending, At: now, Expires: k.ExpiresAt, Attempt: 1}
}

func heldEntry(rule rules.Ref, id string, s Settlement, now time.Time) DecisionEntry {
	return DecisionEntry{
		Message: id, Rule: rule.ID, Version: rule.Version, State: DecisionHeld, At: now,
		Evidence: storedEvidence(s.Evidence), Recovered: len(s.Evidence) == 0,
	}
}

// Settle is the transition of Decisions.Settle on one block read at
// versions. For each id: a Pending becomes what its settlement says (Held
// or Cleared with its evidence, or dropped for Abandoned), Cleared and
// Abandoned only when the settlement names the attempt that began it (F1:
// one that finds another attempt's Pending, a retry's or a late one's,
// changes nothing and is answered Pending); a final decision
// stays as it is and is answered; an id with nothing stored is answered
// Abandoned, and for Held a final Held is stored when the block is the tail
// and has room and versions (one kept for each of its Pending and for Next);
// it fails closed, and DecisionBlocks.Settle opens the tail's successor.
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
		destructive := want.State == DecisionCleared || want.State == DecisionAbandoned
		switch {
		case i >= 0 && r.Entries[i].State != DecisionPending:
			out[id] = r.Entries[i].decision()
		case i >= 0 && (!writable || (final && !evidenceFits(want.Evidence)) || (destructive && !r.began(i, want.Attempt))):
			out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
		case i >= 0 && want.State == DecisionAbandoned:
			r.Entries = append(r.Entries[:i], r.Entries[i+1:]...)
			out[id] = Decision{State: DecisionAbandoned}
			changed = true
		case i >= 0 && final:
			e := &r.Entries[i]
			e.State, e.At, e.Attempt = want.State, now, 0
			e.Evidence, e.Recovered = storedEvidence(want.Evidence), want.State == DecisionHeld && len(want.Evidence) == 0
			out[id] = e.decision()
			changed = true
		case i >= 0:
			out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
		case want.State == DecisionHeld && !r.Next && versionsFor(versions, r.pending()) && evidenceFits(want.Evidence):
			r.Entries = append(r.Entries, heldEntry(rule, id, want, now))
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
// committed versions. Every block but the last is written Next; the last is
// the tail, which alone takes new decisions, unless it is written Next too
// (a crash left its successor never written, which the next writer makes).
// BlockDecisions applies these transitions and writes the blocks they say
// changed, in their order: an existing block at the version read, a new one
// (Versions 0) made only if nobody made it meanwhile, once the block before
// it is written Next.
type DecisionBlocks struct {
	Mailbox  string
	Day      string
	Blocks   []*DecisionRecord
	Versions []uint64
}

// located is a message's decision of a rule in the day, and the blocks
// that hold it.
type located struct {
	dec Decision
	in  []int
	// attempt is its Pending's, when one block holds it.
	attempt Attempt
}

// lookup is, for each of ids, its decision of rule in the day and the
// blocks that hold it, in one pass over the day. A message is in one block
// (only the tail takes new decisions, R1); should it ever be in more, the
// most protective of its decisions is answered (Held, then Pending, then
// Cleared), and the caller writes none of them.
func (d *DecisionBlocks) lookup(rule rules.Ref, ids []string) map[string]*located {
	out := make(map[string]*located, len(ids))
	for _, id := range ids {
		out[id] = &located{}
	}
	for b, r := range d.Blocks {
		for _, e := range r.Entries {
			l := out[e.Message]
			if l == nil || e.Rule != rule.ID || e.Version != rule.Version {
				continue
			}
			if dec := e.decision(); len(l.in) == 0 || protection(dec.State) > protection(l.dec.State) {
				l.dec = dec
			}
			l.attempt = Attempt{}
			if len(l.in) == 0 && e.State == DecisionPending {
				l.attempt = Attempt{Block: r.Block, Version: e.Attempt}
			}
			l.in = append(l.in, b)
		}
	}
	return out
}

// protection orders stored states from the one that lets a message leave.
func protection(s DecisionState) int {
	switch s {
	case DecisionHeld:
		return 2
	case DecisionPending:
		return 1
	default:
		return 0
	}
}

// tail is the block that takes new decisions: the last one, or its
// successor opened when it is written Next or the day has none; -1 when
// none may be opened.
func (d *DecisionBlocks) tail(changed map[int]bool) int {
	if n := len(d.Blocks); n > 0 && !d.Blocks[n-1].Next {
		return n - 1
	}
	return d.open(changed)
}

// open makes the tail's successor: the tail written Next (in the write that
// changes it anyway, or with the version every write keeps for it), then a
// new block. It answers the new block's index, -1 when the day may not have
// one more or the tail has no version left.
func (d *DecisionBlocks) open(changed map[int]bool) int {
	n := len(d.Blocks)
	if n >= DecisionBlocksMax {
		return -1
	}
	if n > 0 {
		last := d.Blocks[n-1]
		if !last.Next {
			if !changed[n-1] && d.Versions[n-1]+1 > DecisionsVersionsMax {
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
// answered as it is (lookup); one whose message has expired by now is
// Expired; the others are begun in the tail, its successor opened while
// some are left (the tail out of decisions, bytes or versions, R3); Full
// past DecisionBlocksMax. It answers the blocks to write, in order.
func (d *DecisionBlocks) Begin(rule rules.Ref, ids []DecisionKey, now time.Time) (map[string]Decision, []int) {
	out := make(map[string]Decision, len(ids))
	changed := map[int]bool{}
	names := make([]string, len(ids))
	for i, k := range ids {
		names[i] = k.ID
	}
	found := d.lookup(rule, names)
	var todo []DecisionKey
	for _, k := range ids {
		if _, seen := out[k.ID]; seen {
			continue
		}
		if l := found[k.ID]; len(l.in) > 0 {
			out[k.ID] = l.dec
			continue
		}
		if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
			out[k.ID] = Decision{State: DecisionExpired}
			continue
		}
		out[k.ID] = Decision{State: DecisionFull}
		todo = append(todo, k)
	}
	b := -1
	if len(todo) > 0 {
		b = d.tail(changed)
	}
	for b >= 0 && len(todo) > 0 {
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
		if len(left) == len(todo) && d.Versions[b] == 0 && len(d.Blocks[b].Entries) == 0 {
			break // a new block took none: no other would
		}
		if todo = left; len(todo) > 0 {
			if d.Blocks[b].waits(pendingEntry(rule, todo[0], now), now, d.Versions[b]) {
				break // held for now: the tail's room comes back as its Pending settle (F2)
			}
			b = d.open(changed)
		}
	}
	return out, sorted(changed)
}

// Settle is Decisions.Settle on one day: each id is settled in the block
// that holds it (one found in more is answered as lookup says and not
// written); a Held for an id no block holds is stored in the tail, its
// successor opened when it has no room (and the tail does not wait for its
// Pending, F2).
func (d *DecisionBlocks) Settle(rule rules.Ref, settle map[string]Settlement, now time.Time) (map[string]Decision, []int) {
	out, changed, _ := d.settle(rule, settle, now)
	return out, changed
}

// settle is Settle, and for each id answered from a block it changed, that
// block: its answer is committed once that block's write is (F1).
func (d *DecisionBlocks) settle(rule rules.Ref, settle map[string]Settlement, now time.Time) (map[string]Decision, []int, map[string]int) {
	out := make(map[string]Decision, len(settle))
	where := map[string]int{}
	changed := map[int]bool{}
	byBlock := map[int]map[string]Settlement{}
	var unknown []string
	found := d.lookup(rule, slices.Collect(maps.Keys(settle)))
	for id, s := range settle {
		switch l := found[id]; {
		case len(l.in) > 1:
			out[id] = l.dec
		case len(l.in) == 1:
			if byBlock[l.in[0]] == nil {
				byBlock[l.in[0]] = map[string]Settlement{}
			}
			byBlock[l.in[0]][id] = s
		case s.State == DecisionHeld:
			unknown = append(unknown, id)
		default:
			out[id] = Decision{State: DecisionAbandoned}
		}
	}
	for b, s := range byBlock {
		got, wrote := d.Blocks[b].Settle(rule, s, now, d.Versions[b])
		for id, dec := range got {
			out[id] = dec
			if wrote {
				changed[b], where[id] = true, b
			}
		}
	}
	slices.Sort(unknown)
	for _, id := range unknown {
		out[id] = Decision{State: DecisionFull}
		if !evidenceFits(settle[id].Evidence) {
			continue // never kept without its evidence: held, undecided
		}
		for b := d.tail(changed); b >= 0; b = d.open(changed) {
			got, wrote := d.Blocks[b].Settle(rule, map[string]Settlement{id: settle[id]}, now, d.Versions[b])
			if got[id].State != DecisionFull {
				out[id] = got[id]
				if wrote {
					changed[b], where[id] = true, b
				}
				break
			}
			if d.Versions[b] == 0 && len(d.Blocks[b].Entries) == 0 {
				break
			}
			if d.Blocks[b].waits(heldEntry(rule, id, settle[id], now), now, d.Versions[b]) {
				break // held, undecided, for now (F2)
			}
		}
	}
	return out, sorted(changed), where
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
// for it (ADR 0031, "Ask and settle", N1, R2): r is the rule's formulas
// evaluated for a rule (query.ForRule) with a.Answers, atoms the rule's
// model atoms asked, a and err what Ask answered. True is Held and False
// Cleared, each with the evidence of atoms. Unknown applies the rule, so it
// is Held, with the evidence of whatever answers of atoms were received (an
// answer in doubt, a refusal, or any answer beside one that is missing): a
// received answer is never forgotten. With none received but one in
// a.Unsettled (received and not confirmed kept), the decision is left
// Pending (State Pending: do not settle it): this request holds the message,
// undecided, and the first request after PendingGrace settles it Held. Only
// when the model answered no part of it, on positive evidence (Ask failed,
// which it does only before asking anything, by the Asker's contract; or no
// atom of the rule has evidence or is unsettled: the guard stopped the ask,
// or every call failed without an answer), is it Abandoned, so a later
// request decides it.
func SettleAfterAsk(r query.Result, atoms []query.Atom, a *Asked, err error) Settlement {
	var ev []Decided
	unsettled := false
	if a != nil && err == nil {
		for _, atom := range atoms {
			if e, ok := a.Evidence[atom]; ok {
				ev = append(ev, Decided{Atom: atom, Evidence: e})
			}
			unsettled = unsettled || slices.Contains(a.Unsettled, atom)
		}
	}
	switch {
	case r.Value == query.True:
		return Settlement{State: DecisionHeld, Evidence: ev}
	case r.Value == query.False:
		return Settlement{State: DecisionCleared, Evidence: ev}
	case len(ev) > 0 || len(r.Model) == 0:
		// Unknown on an answer received, or not for want of one.
		return Settlement{State: DecisionHeld, Evidence: ev}
	case unsettled:
		return Settlement{State: DecisionPending}
	default:
		return Settlement{State: DecisionAbandoned}
	}
}

// OverdueAt says a Pending decision begun at began is to be settled Held by
// whoever finds it at now.
func OverdueAt(began, now time.Time) bool { return !now.Before(began.Add(PendingGrace)) }

// Leaves says a message may leave on this decision: only a final Cleared.
func (d Decision) Leaves() bool { return d.State == DecisionCleared }
