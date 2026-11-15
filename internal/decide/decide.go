// Package decide is the one interface between the enclave and a model that
// decides what depends on meaning (ADR 0020, how in ADR 0028): statements
// about one content in, a probability for each out.
//
// It is pure: it builds what is put to a model and checks what comes back,
// and knows nothing of HTTP, storage, keys or a provider's wire format.
// depguard's decide-is-pure rule lists what it may import (.golangci.yml).
// The provider is an adapter of its own (internal/decide/openai, ADR 0028's
// pull request 1), and what asks it for a message, keeps its answers and
// counts what they cost is internal/services/decisions (pull request 2).
//
// This file and prompt.go are the seam, committed with the design: the
// types the pull requests share, the limits of a call, the prices the guard
// counts with, and the prompt's first version, whose words are part of what
// a kept decision is keyed by. Nothing implements Model yet.
package decide

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/query"
)

// Model answers statements about one content: the one interface ADR 0020
// asks for, with the provider, the model and its address compiled into the
// image. Another provider is another implementation, in a release.
type Model interface {
	// Decide puts c's statements to the model and returns one Answer per
	// statement, in their order, and what the call cost. An error means no
	// statement was answered: every one is unknown (ADR 0028, "Failures").
	// A refusal or an answer that does not check is an Answer with OK false,
	// never an error, and the others still count.
	Decide(ctx context.Context, c Call) (*Result, error)
	// Name is the model as receipts and kept decisions name it, such as
	// "gpt-6-luna". It never changes for the life of the value.
	Name() string
}

// Call is one request to the model: one content and the statements about
// it. Plan makes them from a message's atoms; a Call made by hand is not
// checked.
type Call struct {
	Content    Content
	Statements []query.Atom
}

// Content is what the model is given of one message, and nothing else: its
// subject alone, for statements on the subject (Kind ContentSubject); or its
// subject and its text, for statements on the message (ContentMessage).
// Never a sender, a recipient, an attachment, an identifier of the message,
// the mailbox or the account (ADR 0020, ADR 0028 "What is sent").
type Content struct {
	Kind    ContentKind
	Subject string
	// Body is the message's text as a connection receives it: the stored
	// entry's text when it was not cut, otherwise the text taken out of the
	// raw message, at most inbox.MessageTextMax (ADR 0028, the owner's
	// decision 3). Empty for ContentSubject.
	Body string
}

// ContentKind says which fields of a message a Content holds.
type ContentKind int8

const (
	// ContentSubject: the subject alone, for subject atoms.
	ContentSubject ContentKind = iota + 1
	// ContentMessage: the subject and the text, for message atoms.
	ContentMessage
)

// Answer is what the model said of one statement. OK is false for a
// refusal, a missing answer, a probability that is not a number between 0
// and 1, or an answer that does not match its statement: the statement is
// then unknown.
type Answer struct {
	P       float64
	OK      bool
	Refused bool // the model answered with a refusal
}

// Result is what one call answered and cost.
type Result struct {
	Answers []Answer
	// Model is the model the provider says answered; a call whose answer
	// names another than Model.Name fails (ErrInvalidAnswer).
	Model string
	Usage Usage
	// Took is how long the call took, retries included.
	Took time.Duration
}

// Usage is what the provider counted for a call. Cost turns it into money.
type Usage struct {
	InputTokens      int64
	CachedTokens     int64
	CacheWriteTokens int64
	// Reported is false when the answer said nothing of its usage: the
	// call is then charged its estimate (Estimate).
	Reported bool
}

// The limits of what is put to the model (ADR 0028, "The request").
const (
	// MaxStatementsPerCall is how many statements one call holds at most;
	// Plan splits beyond it. The provider allows 400, and the spike
	// measured latency up to 64.
	MaxStatementsPerCall = 64
	// MaxAtomsPerMessage is how many distinct atoms one message is asked at
	// most: three formulas evaluated together, each with at most
	// contracts.MaxModelConditions (ADR 0027).
	MaxAtomsPerMessage = 24
	// CallTimeout is how long one attempt waits for its answer, for a
	// content of at most LongContent bytes; LongCallTimeout beyond.
	CallTimeout     = 3 * time.Second
	LongCallTimeout = 6 * time.Second
	LongContent     = 32 << 10
	// Retries is how many times a call is tried again after a timeout, a
	// transport error, a 500, a 502, a 503, a 504 or a 429 that only asks to
	// slow down; never after anything else (ADR 0028, "Failures").
	Retries = 2
	// MaxRetryAfter is the longest Retry-After honoured.
	MaxRetryAfter = 5 * time.Second
	// MessageDeadline is how long the calls for one message may take
	// together, retries and waiting for a slot included.
	MessageDeadline = 10 * time.Second
	// Concurrent is how many calls one enclave has open at once.
	Concurrent = 16
)

// Prices of gpt-6-luna on the Decisions API, in billionths of a US dollar
// per token (ADR 0028, "Spend"; OpenAI's pricing page, read 2026-11-15:
// $0.10, $0.01 and $0.125 per million tokens). Output is not charged.
// Requests over LongRequestTokens input tokens cost twice. A change of price
// is a release.
const (
	NanoUSDPerInputToken      = 100
	NanoUSDPerCachedToken     = 10
	NanoUSDPerCacheWriteToken = 125
	LongRequestTokens         = 272_000
)

// Failures of a call, as Decide returns them. None says why in words of the
// provider: an error's text may echo a key or a request.
var (
	// ErrUnavailable: no answer within the deadline, after the retries
	// allowed (timeouts, transport errors, 5xx, 429 slow_down).
	ErrUnavailable = errors.New("decide: the model did not answer")
	// ErrKeyRefused: the provider refused the key (401, 403), or none is
	// set. Nothing is asked until the operator sets one again, or until a
	// probe succeeds (ADR 0028, "The key").
	ErrKeyRefused = errors.New("decide: the provider refused the key")
	// ErrProviderLimit: the provider's own limit was reached (429 with
	// credit_balance_exhausted, a spend limit or a usage limit). Never
	// retried; the guard stops asking (ADR 0028, "Spend").
	ErrProviderLimit = errors.New("decide: the provider's spend limit was reached")
	// ErrRequest: the provider refused the request as malformed (400, 404,
	// 422): a bug of the enclave. Never retried.
	ErrRequest = errors.New("decide: the provider refused the request")
	// ErrInvalidAnswer: a 200 whose body does not read as an answer to the
	// call (not JSON, another number of answers, another model).
	ErrInvalidAnswer = errors.New("decide: the answer does not match the call")
)
