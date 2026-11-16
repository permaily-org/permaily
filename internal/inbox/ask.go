package inbox

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/query"
)

// The seam of ADR 0028 between stored mail and the model: what Matcher asks
// for a message whose value depends only on conditions a model decides
// (Asker, implemented by internal/services/decisions, its pull request 2),
// and the kept decisions of a message, sealed in its mailbox's partition
// beside its search entry (KeptDecisions, written and read by this
// package, its pull request 3). Nothing implements them yet.

// Asker answers, for one message, the atoms its value still depends on. It
// uses what was kept for the message first, asks the model only for what was
// not, within the spend guard and the account's share of it, and uses an
// answer of the model only once it is kept (ADR 0028, "Kept decisions"). It
// never fails for the model's sake: an atom it could not get a kept answer
// for is simply not in Asked.Answers, and is unknown; Asked.Outcome says
// why when it is not the model's own failure. An error is the storage's or
// the request's, and only ever before the model was asked anything for the
// question (ADR 0031, "Changed after the second design review", N1): a
// failure after that (an answer received and not kept, or kept and not
// confirmed) is answered in Asked.Unsettled, so a decision begun for the
// question is never dropped as if nothing had been asked. A kept answer an
// erroring Ask had read is not new: whoever writes the table could have
// removed it before the decision was begun as well.
type Asker interface {
	Ask(ctx context.Context, q *Question) (*Asked, error)
}

// Question is what Asker is given of one message, and all it is given: the
// box it is in (for its kept decisions, sealed under the box's key), the
// message's identifier and expiry (the kept entry's key and expiry), the
// atoms a value depends on, the subject, which view of the text is meant,
// and a way to get that text that is called only when the view cannot
// prove a kept answer is for it. Never a sender, a recipient or an
// attachment.
type Question struct {
	Box       Box
	MessageID string
	ExpiresAt time.Time
	// Account is whose share of the spend guard the asking is counted in
	// (WithAsker).
	Account string
	Atoms   []query.Atom
	Subject string
	// View names the bytes Text gives (ADR 0028, "Changed after the design
	// review", 3): in step 4 always ViewOriginal. A kept answer for a
	// message atom is used without calling Text only when it was kept for
	// this view, under the same subject; otherwise Text is read and its
	// content hashed.
	View TextView
	// Text is the message's text in View as a connection receives it: the
	// search entry's when it was not cut, otherwise the text taken out of
	// the raw message, at most MessageTextMax, read under the page's read
	// budget (ADR 0028, the owner's decision 3). TextUnavailable when it
	// cannot be read (no raw message, past its bound): the message atoms are
	// then unknown and the subject atoms still asked. TextUnaffordable when
	// the page cannot afford the read: Ask then asks nothing of the message
	// and answers AskStop, and the page ends before it.
	Text func(ctx context.Context) (text string, st TextState, err error)
}

// TextState is what became of reading a message's text for the model.
type TextState int8

const (
	// TextRead: text is the message's text in the view.
	TextRead TextState = iota + 1
	// TextUnavailable: the message has no text that can be read (its raw
	// message is missing or past its bound). Its message atoms are unknown.
	TextUnavailable
	// TextUnaffordable: the page has no raw read left for it. Nothing of
	// the message is asked; a later page reads it with a fresh budget.
	TextUnaffordable
)

// TextView names one view of a message's text: which (Name: "original"
// for the text as it came; step 7's removals add views of their own, named
// by what they removed) and how it is taken out of the message (Version,
// TextVersion for the original). Under one message identifier, one view
// is always the same bytes: that is what lets a kept answer be used without
// reading the text again. A change of how the text is taken out of a
// message, or of what a view removes, is a new Version or a new Name.
type TextView struct {
	Name    string
	Version int
}

// TextVersion is the version of how MessageText takes a message's text out
// of it; a change of it is a new view, so answers kept for the old one are
// not used for the new one.
const TextVersion = 1

// ViewOriginal is the text as it came, the one view of step 4.
var ViewOriginal = TextView{Name: "original", Version: TextVersion}

// Key is how a view is named in KeptDecisions.Views, after the prompt
// version: "original/1".
func (v TextView) Key() string { return v.Name + "/" + strconv.Itoa(v.Version) }

// Asked is what Ask got for a message.
type Asked struct {
	// Answers are the probabilities it has, kept before or kept now, by
	// atom: never an answer that could not be kept.
	Answers query.Answers
	// Held lists the atoms it did not ask because of Outcome AskOff or
	// AskLimit: the message is listed as needing a model, not as unknown,
	// so a later page asks again. A held atom is never in Answers, never
	// with a probability of zero or any other: it is unknown to Eval. An
	// atom left out for any other reason (a failure, a refusal, a full
	// entry, a text that cannot be read) is in neither Answers nor Held: it
	// is unknown.
	Held []query.Atom
	// Outcome is why atoms were held, by the precedence of AskOutcome.
	Outcome AskOutcome
	// Called is how many attempts to the model it made (0 when every atom
	// was kept).
	Called int
	// Evidence is what each answer in Answers came from, and each kept
	// refusal (which is in neither Answers nor Held): the very kept answer
	// used, as this Ask read or wrote it, never read again later (ADR 0030,
	// "Changed after the design review", 14). An atom decided by rule (an
	// empty subject or text) has an Evidence with ByRule set. The discloser
	// tells a persisted refusal (Refused) from a failure (no Evidence), and
	// receipts name the model, the prompt and the canary's days of the
	// answer a decision rested on.
	Evidence map[query.Atom]Evidence
	// Unsettled are the atoms whose answer (or refusal) this Ask received
	// from the model and could not confirm kept: Keep failed, wrote and
	// then failed, or kept another answer first. They are in neither
	// Answers nor Evidence, and no one can say what the kept entry now
	// holds: a decision resting on one stays Pending (SettleAfterAsk), and
	// is recovered Held, never Abandoned (N1).
	Unsettled []query.Atom
}

// Evidence is one answer Ask used, as it was kept.
type Evidence struct {
	// ByRule: decided without the model (an empty subject or text); the
	// other fields are empty.
	ByRule bool
	// Refused: the model's kept refusal; the atom is unknown.
	Refused bool
	// P is the kept probability (0 for a refusal or by rule).
	P      float64
	Model  string
	Prompt string
	// At is when the answer was kept; CanaryChecked and CanaryChange are
	// KeptAnswer's.
	At            time.Time
	CanaryChecked string
	CanaryChange  string
}

// AskOutcome says why Ask did not ask the model of every atom it had no
// kept answer for (ADR 0028, "What the page says"). One message has one,
// the first that applies of AskStop, AskOff, AskLimit and AskAnswered; a
// page of matches says MatchModelOff when one of its messages was AskOff,
// otherwise MatchModelLimit when one was AskLimit, otherwise
// MatchModelAsked.
type AskOutcome int8

const (
	// AskAnswered: every atom was kept, asked or left unknown for a reason
	// of its own; Held is empty.
	AskAnswered AskOutcome = iota + 1
	// AskLimit: the month's limit, the provider's limit, the account's
	// share or a spend record that cannot be read held some atoms.
	AskLimit
	// AskOff: no key is set, or the provider refused it; the atoms without
	// a kept answer are all held.
	AskOff
	// AskStop: the text could not be afforded on this page
	// (TextUnaffordable). Nothing was asked or spent for the message, and
	// Answers holds only what was kept; the page ends before the message
	// and its next points at the message before it.
	AskStop
)

type askerKey struct{}

// Asking is the Asker a request's pages of matches use, and the account
// they count against.
type Asking struct {
	Asker   Asker
	Account string
}

// WithAsker makes the Matcher ask a for the messages whose value depends
// only on conditions a model decides. The route sets it when the request
// asks for the model (contracts.MatchRequest.AskModel) and the account
// agreed to the model's notice; without it, Matcher lists those messages as
// needing a model, as it did before ADR 0028.
func WithAsker(ctx context.Context, a Asking) context.Context {
	return context.WithValue(ctx, askerKey{}, a)
}

// AskerFrom is the Asking set on ctx, and whether one is.
func AskerFrom(ctx context.Context) (Asking, bool) {
	a, ok := ctx.Value(askerKey{}).(Asking)
	return a, ok && a.Asker != nil
}

// MatchMaxModelMessages is how many messages one page of matches asks the
// model about at most (ADR 0028, "Bounds of a page"). The next message that
// needs it ends the page before it, as a raw read the page cannot afford
// does (AskStop), so pages skip nothing.
const MatchMaxModelMessages = 25

// KeyDecision is the key of a message's kept decisions in its mailbox's
// partition (pk=mailbox#<id>), + the message's identifier: one entry per
// message, deleted with it and with the mailbox, expiring with it.
const KeyDecision = "dec#"

// PurposeDecision seals KeptDecisions with envelope.SealText, bound to the
// mailbox and the message (envelope.AAD(PurposeDecision, mailbox, id)).
const PurposeDecision = "message-decisions"

// GonePartition holds a mark for each message being deleted (key
// "<mailbox id>#<message id>"), written by Reader.Delete before it deletes
// anything (ADR 0028, "Changed after the design review", 10). KeptStore.Keep
// reads it after its write and deletes what it wrote when the mark is
// there. The mark is also the work of deleting dec#<id> once more after
// SweepGrace, when every Keep that read the message before the mark has
// ended, for a Keep that stopped between its write and its read: the
// mailboxes' sweep does it, and deletes the mark only once that deletion
// succeeded. So a mark expires no earlier than the message it is for (its
// ExpiresAt, which a kept entry shares; none when the message has none),
// and a sweep that cannot run for days, or fails, leaves it for the next
// (ADR 0028, "Changed after the second design review", 3). The store has
// no write conditional on another item, so this is how a decision is never
// kept for a message that is gone.
const GonePartition = "message-gone"

// KeptMaxAnswers is how many answers one message's entry keeps. Past it a
// new atom is not asked and stays unknown, and an answer the entry has no
// room for when it is written is not used (ADR 0028, "Kept decisions").
const KeptMaxAnswers = 256

// KeepTries is how many times Keep reads the entry again after another
// writer changed it (store.ErrConflict on an update, store.ErrExists on a
// create) before it gives up; it then keeps nothing and the caller uses
// none of its new answers.
const KeepTries = 4

// ErrGone is Keep's answer when the message was deleted while it was asked
// about: nothing is kept, and nothing new is used.
var ErrGone = errors.New("inbox: the message was deleted while it was asked about")

// KeptStore reads and writes a message's kept decisions. Pull request 3
// implements it on the store (NewKeptStore); Asker uses it.
//
// What whoever writes the table can do to an entry (ADR 0028, "What it
// guarantees"): delete it, damage it or put back an earlier copy. Kept
// then reads none, or the earlier one; what is missing is asked again, and
// a different answer is possible only if the model's behaviour changed,
// which the canary samples. Never an error that delivers, and never an
// answer they wrote. In step 4 only the owner's previews use kept answers;
// before a rule that protects relies on one (step 7), rule-mode answers
// need rollback resistance (docs/plan.md, step 7).
type KeptStore interface {
	// Kept is the message's kept decisions; nil and no error when it has
	// none, or when its entry does not open (it is then asked again, and
	// what is asked replaces it).
	Kept(ctx context.Context, b Box, id string) (*KeptDecisions, error)
	// Keep adds add's views and answers to the message's entry, written at
	// the version read and read again when another writer changed it (at
	// most KeepTries times). An answer whose key is already kept is not
	// added: the kept one stays. An answer past KeptMaxAnswers is not
	// added. It then reads the message's mark in GonePartition: when there
	// is one, it deletes what it wrote and returns ErrGone. Otherwise it
	// returns the entry as written, which is what the caller evaluates
	// with, so two enclaves that asked at once use one answer and an
	// answer that could not be kept is used by neither. The entry expires
	// at expires.
	Keep(ctx context.Context, b Box, id string, expires time.Time, add *KeptDecisions) (*KeptDecisions, error)
}

// KeptDecisions is what the model said of one message, sealed under its
// mailbox's key (ADR 0028, "Kept decisions"). An answer is keyed by the
// hash of the content the model saw, the atom, the model and the prompt's
// version, never by a formula (ADR 0027); the first answer kept for a key
// is the one every enclave uses from then on.
type KeptDecisions struct {
	V int `json:"v"`
	// Views says, for each prompt version and view of the text
	// ("p2-2026-11-15/original/1"), the hash of the message content that
	// view gave the model, so a later ask for the same view finds its kept
	// answers without reading the text again. A subject content is never
	// here: its hash is taken from Question.Subject each time.
	Views   map[string]KeptView `json:"views"`
	Answers []KeptAnswer        `json:"answers"`
}

// KeptView is the content one view of the text gave the model.
type KeptView struct {
	// Content is decide.ContentHash of the message content.
	Content string `json:"content"`
	// Subject is the hex SHA-256 of the subject in it: a Question with
	// another subject does not use Content, and reads the text.
	Subject string `json:"subject"`
}

// KeptAnswer is one answer of the model, or its refusal.
type KeptAnswer struct {
	Content string  `json:"content"` // decide.ContentHash of what the model saw
	Var     string  `json:"var"`
	Op      string  `json:"op"`
	Topic   string  `json:"topic"`
	Model   string  `json:"model"`
	Prompt  string  `json:"prompt"`
	P       float64 `json:"p"`
	// Refused: the model refused this statement; P is then 0 and is not
	// read, and the atom is unknown for as long as this is kept.
	Refused bool      `json:"refused,omitempty"`
	At      time.Time `json:"at"`
	// CanaryChecked is the day (UTC, YYYY-MM-DD) of the last canary check
	// that had completed when this was asked, and CanaryChange the last day
	// a check observed a change ("" for none since the canary's baseline):
	// what a receipt says of the model, a heuristic sample of 156 fixed
	// statements, never a proof of when the model changed (ADR 0028, "The
	// canary").
	CanaryChecked string `json:"canary_checked"`
	CanaryChange  string `json:"canary_change,omitempty"`
}
