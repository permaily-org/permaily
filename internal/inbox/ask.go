package inbox

import (
	"context"
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
// not, within the spend guard and the account's share of it, and keeps what
// the model answered (ADR 0028, "Kept decisions"). It never fails for the
// model's sake: an atom it could not get an answer for is simply not in
// Asked.Answers, and is unknown. An error is the storage's or the request's.
type Asker interface {
	Ask(ctx context.Context, q *Question) (*Asked, error)
}

// Question is what Asker is given of one message, and all it is given: the
// box it is in (for its kept decisions, sealed under the box's key), the
// message's identifier and expiry (the kept entry's key and expiry), the
// atoms a value depends on, the subject, and a way to get the text that is
// called only when a message atom has no kept answer. Never a sender, a
// recipient or an attachment.
type Question struct {
	Box       Box
	MessageID string
	ExpiresAt time.Time
	// Account is whose share of the spend guard the asking is counted in
	// (WithAsker).
	Account string
	Atoms   []query.Atom
	Subject string
	// Text is the message's text as a connection receives it: the search
	// entry's when it was not cut, otherwise the text taken out of the raw
	// message, at most MessageTextMax, read under the read budget (ADR 0028,
	// the owner's decision 3). ok is false when it cannot be read (no raw
	// message, past its bound, or the page cannot afford the read); the
	// message atoms are then unknown and the subject atoms still asked.
	Text func(ctx context.Context) (text string, ok bool, err error)
}

// Asked is what Ask got for a message.
type Asked struct {
	// Answers are the probabilities it has, kept or new, by atom.
	Answers query.Answers
	// Held lists the atoms it did not ask because the spend guard or the
	// account's share did not let it (contracts.MatchModelLimit): the
	// message is listed as needing a model, not as unknown, so a later page
	// asks again.
	Held []query.Atom
	// Called is how many calls to the model it made (0 when every atom was
	// kept).
	Called int
}

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
// model about at most (ADR 0028, "Pages"). The next message that needs it
// ends the page before it, as a raw read the page cannot afford does, so
// pages skip nothing.
const MatchMaxModelMessages = 25

// KeyDecision is the key of a message's kept decisions in its mailbox's
// partition (pk=mailbox#<id>), + the message's identifier: one entry per
// message, deleted with it and with the mailbox, expiring with it.
const KeyDecision = "dec#"

// PurposeDecision seals KeptDecisions with envelope.SealText, bound to the
// mailbox and the message (envelope.AAD(PurposeDecision, mailbox, id)).
const PurposeDecision = "message-decisions"

// KeptMaxAnswers is how many answers one message's entry keeps. Past it a
// new answer is used for the request and not kept (ADR 0028, "Kept
// decisions").
const KeptMaxAnswers = 256

// KeptStore reads and writes a message's kept decisions. Pull request 3
// implements it on the store (NewKeptStore); Asker uses it.
type KeptStore interface {
	// Kept is the message's kept decisions; nil and no error when it has
	// none, or when its entry does not open (it is then asked again, and
	// what is asked replaces it).
	Kept(ctx context.Context, b Box, id string) (*KeptDecisions, error)
	// Keep adds add's content hashes and answers to the message's entry,
	// written at the version read and read again when another enclave
	// changed it, a bounded number of times. An answer whose key is already
	// kept is not added: the kept one stays. It returns the entry as
	// written, which is what the caller evaluates with, so two enclaves that
	// asked at once use one answer. The entry expires at expires.
	Keep(ctx context.Context, b Box, id string, expires time.Time, add *KeptDecisions) (*KeptDecisions, error)
}

// KeptDecisions is what the model said of one message, sealed under its
// mailbox's key (ADR 0028, "Kept decisions"). An answer is keyed by the
// hash of the content the model saw, the atom, the model and the prompt's
// version, never by a formula (ADR 0027); the first answer kept for a key
// is the one every enclave uses from then on.
type KeptDecisions struct {
	V int `json:"v"`
	// Content is the hash of the content each kind of call gave the model
	// for this message, by prompt version and kind ("p1-2026-11-15/subject",
	// "p1-2026-11-15/message"), so a later ask finds its kept answers
	// without reading the text again.
	Content map[string]string `json:"content"`
	Answers []KeptAnswer      `json:"answers"`
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
	// Since is the day the canary last saw the model change, or the day it
	// was first run: what a receipt says as "behaviour as of".
	Since string `json:"since"`
}
