// Package decidetest is a model for tests (ADR 0028, "The one interface,
// and where it lives"): a Table of what it answers and how its attempts
// fail; a Fake decide.Model that answers from it; a played Provider, an
// HTTPS server speaking OpenAI's Decisions API from the same table, for the
// real client (internal/decide/openai); and Contract, the behaviour both
// keep. Never in production (depguard no-fakes-in-production).
package decidetest

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

// Name is the model the fake and the played provider answer as.
const Name = "gpt-6-luna"

// AnotherModel is the model an answer names on OtherModel.
const AnotherModel = "gpt-6-luna-2027-01-01"

// Failure is how an attempt fails, as the played provider answers it and as
// the real client reads it (ADR 0028, "Failures").
type Failure int

const (
	// Unavailable is a 503, with the table's Retry-After when one is set:
	// decide.ErrUnavailable, sent, tried again.
	Unavailable Failure = iota + 1
	// SlowDown is a 429 slow_down, with the table's Retry-After:
	// decide.ErrUnavailable, sent, tried again.
	SlowDown
	// KeyRefused is a 401: decide.ErrKeyRefused, sent.
	KeyRefused
	// ProviderLimit is a 429 project_spend_limit_exceeded:
	// decide.ErrProviderLimit, sent.
	ProviderLimit
	// BadRequest is a 400: decide.ErrRequest, sent.
	BadRequest
	// Redirect is a 307 to another host, never followed: decide.ErrRequest,
	// sent.
	Redirect
	// Invalid is a 200 whose answers are not the call's (one more than its
	// statements): decide.ErrInvalidAnswer, sent.
	Invalid
	// OtherModel is a 200 that checks but names AnotherModel:
	// decide.ErrModelChanged, sent.
	OtherModel
	// Drop is a request processed and its answer dropped (the connection
	// closed): decide.ErrUnavailable, sent, tried again.
	Drop
	// Late is an answer that comes only once the attempt's timeout has
	// passed: decide.ErrUnavailable, sent; the late answer is never used.
	Late
)

// Table is what a model for tests answers, and what it was asked. Its
// methods are safe to call at once.
type Table struct {
	mu         sync.Mutex
	byAtom     map[query.Atom]float64
	byContent  map[contentAtom]float64
	refused    map[query.Atom]bool
	key        string
	next       []Failure
	always     Failure
	delay      time.Duration
	retryAfter time.Duration
	noUsage    bool
	attempts   int
	calls      []decide.Call
	keys       []string
}

type contentAtom struct {
	hash string
	atom query.Atom
}

// NewTable is a table that answers 0 to every statement and accepts any
// key.
func NewTable() *Table {
	return &Table{byAtom: map[query.Atom]float64{}, byContent: map[contentAtom]float64{}, refused: map[query.Atom]bool{}}
}

// Set makes every content answer p for a.
func (t *Table) Set(a query.Atom, p float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byAtom[a] = p
}

// SetFor makes the content of a message with this subject and text answer
// p for a, over Set: the subject alone for a subject atom, the subject and
// the text for a message atom (decide.KindOf).
func (t *Table) SetFor(subject, text string, a query.Atom, p float64) {
	c := decide.Content{Kind: decide.KindOf(a), Subject: subject}
	if c.Kind == decide.ContentMessage {
		c.Body = text
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byContent[contentAtom{decide.ContentHash(c), a}] = p
}

// Refuse makes a a refusal on every content.
func (t *Table) Refuse(a query.Atom) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refused[a] = true
}

// RequireKey makes every attempt with another key a KeyRefused; "" accepts
// any.
func (t *Table) RequireKey(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.key = key
}

// Fail makes the next attempts fail, one failure each, in order.
func (t *Table) Fail(fs ...Failure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next = append(t.next, fs...)
}

// FailAlways makes every attempt after those of Fail fail with f; 0 stops
// it.
func (t *Table) FailAlways(f Failure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.always = f
}

// SetDelay makes every answer come after d (an attempt whose timeout is
// shorter has none).
func (t *Table) SetDelay(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.delay = d
}

// SetRetryAfter is the Retry-After of Unavailable and SlowDown; 0 sends
// none.
func (t *Table) SetRetryAfter(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryAfter = d
}

// OmitUsage makes answers say nothing of their usage.
func (t *Table) OmitUsage(omit bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.noUsage = omit
}

// Attempts is how many attempts reached the model, failed ones included.
func (t *Table) Attempts() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// Calls are the calls that reached the model, in the order they came.
func (t *Table) Calls() []decide.Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]decide.Call(nil), t.calls...)
}

// Keys are the keys the attempts that reached the model came with.
func (t *Table) Keys() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.keys...)
}

// outcome is how one attempt ends, before the fake or the played provider
// says it its own way.
type outcome struct {
	failure    Failure
	answers    []decide.Answer
	usage      decide.Usage
	delay      time.Duration
	retryAfter time.Duration
}

// attempt records an attempt that reached the model with key, and says how
// it ends.
func (t *Table) attempt(call decide.Call, key string) outcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts++
	t.calls = append(t.calls, call)
	t.keys = append(t.keys, key)
	o := outcome{delay: t.delay, retryAfter: t.retryAfter}
	switch {
	case t.key != "" && key != t.key:
		o.failure = KeyRefused
		return o
	case len(t.next) > 0:
		o.failure, t.next = t.next[0], t.next[1:]
	default:
		o.failure = t.always
	}
	hash := decide.ContentHash(call.Content)
	o.answers = make([]decide.Answer, len(call.Statements))
	for i, a := range call.Statements {
		if t.refused[a] {
			o.answers[i] = decide.Answer{Refused: true}
			continue
		}
		p, ok := t.byContent[contentAtom{hash, a}]
		if !ok {
			p = t.byAtom[a]
		}
		o.answers[i] = decide.Answer{P: p, OK: true}
	}
	if !t.noUsage {
		o.usage = Usage(call)
	}
	return o
}

// Usage is what the played provider reports a call used: the re-measure's
// fit (ADR 0028, "The re-measure": 169 tokens, 190 a statement and 0.204 a
// byte of the content's JSON), under decide.Estimate.
func Usage(call decide.Call) decide.Usage {
	n := int64(169) + 190*int64(len(call.Statements)) + int64(len(decide.ContentJSON(call.Content)))*204/1000
	return decide.Usage{InputTokens: n, Reported: true}
}

// sendable says whether a call is one the real client sends: 1 to
// decide.MaxStatementsPerCall statements, each one a model decides on the
// call's content.
func sendable(call decide.Call) bool {
	if n := len(call.Statements); n == 0 || n > decide.MaxStatementsPerCall {
		return false
	}
	for _, a := range call.Statements {
		if decide.KindOf(a) == 0 || decide.KindOf(a) != call.Content.Kind {
			return false
		}
	}
	return true
}

// timeout is an attempt's own timeout, as the real client's.
func timeout(call decide.Call) time.Duration {
	if len(decide.ContentJSON(call.Content)) > decide.LongContent {
		return decide.LongCallTimeout
	}
	return decide.CallTimeout
}

// AtomOf is the atom whose statement is instructions (decide.Instructions
// read back), or false.
func AtomOf(instructions string) (query.Atom, bool) {
	for _, shape := range shapes {
		if !strings.HasPrefix(instructions, shape.prefix) || !strings.HasSuffix(instructions, shape.suffix) ||
			len(instructions) < len(shape.prefix)+len(shape.suffix) {
			continue
		}
		topic, ok := unquote(instructions[len(shape.prefix) : len(instructions)-len(shape.suffix)])
		if !ok {
			continue
		}
		a := query.Atom{Var: shape.atom.Var, Op: shape.atom.Op, Topic: topic}
		if again, _ := decide.Instructions(a); again == instructions {
			return a, true
		}
	}
	return query.Atom{}, false
}

// shapes are the three statements, cut around their topic.
var shapes = func() []struct {
	atom           query.Atom
	prefix, suffix string
} {
	const marker = "\x00"
	var out []struct {
		atom           query.Atom
		prefix, suffix string
	}
	for _, a := range []query.Atom{
		{Var: contracts.VarSubject, Op: contracts.OpAbout},
		{Var: contracts.VarMessage, Op: contracts.OpAbout},
		{Var: contracts.VarMessage, Op: contracts.OpMentions},
	} {
		a.Topic = marker
		text, _ := decide.Instructions(a)
		prefix, suffix, _ := strings.Cut(text, `{"topic": "\u0000"}`)
		out = append(out, struct {
			atom           query.Atom
			prefix, suffix string
		}{a, prefix + `{"topic": `, `}` + suffix})
	}
	return out
}()

// unquote is a JSON string's value, or false.
func unquote(s string) (string, bool) {
	var v string
	if json.Unmarshal([]byte(s), &v) != nil {
		return "", false
	}
	return v, true
}
