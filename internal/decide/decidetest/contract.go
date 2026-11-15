package decidetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

// NewModel builds the model under contract: answering from tab, and asking
// key for its key on each attempt.
type NewModel func(t *testing.T, tab *Table, key decide.Credential) decide.Model

// Contract is the behaviour of decide.Model that the fake and the real
// client over a played provider both keep (ADR 0028, A7): answers in the
// order of the statements, a refusal for one statement alone, one attempt a
// call, every failure the same AttemptError, nothing sent without a key or
// a live context or for a call the client does not send.
func Contract(t *testing.T, newModel NewModel) {
	subject := query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}
	about := query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "invoices"}
	mentions := query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: `a "home" address\`}
	mentions2 := query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "facturas électroniques"}
	message := decide.Content{Kind: decide.ContentMessage, Subject: "Invoice 12", Body: "Please pay \"invoice\" 12.\nThanks"}
	onMessage := decide.Call{Content: message, Statements: []query.Atom{about, mentions, mentions2}}
	onSubject := decide.Call{Content: decide.Content{Kind: decide.ContentSubject, Subject: "Invoice 12"}, Statements: []query.Atom{subject}}
	withKey := func(key string) decide.Credential {
		return func(context.Context) (string, bool) { return key, key != "" }
	}
	setup := func(t *testing.T) (*Table, decide.Model) {
		tab := NewTable()
		return tab, newModel(t, tab, withKey("sk-test"))
	}

	t.Run("answers in order, a refusal alone", func(t *testing.T) {
		tab, m := setup(t)
		tab.Set(about, 0.91)
		tab.Set(mentions, 0.5)
		tab.SetFor("Another", "text", mentions, 0.07) // another content: not this one
		tab.SetFor(message.Subject, message.Body, about, 0.98)
		tab.Refuse(mentions2)
		res, err := m.Decide(context.Background(), onMessage)
		if err != nil {
			t.Fatal(err)
		}
		want := []decide.Answer{{P: 0.98, OK: true}, {P: 0.5, OK: true}, {Refused: true}}
		if !reflect.DeepEqual(res.Answers, want) {
			t.Errorf("answers = %+v, want %+v", res.Answers, want)
		}
		if m.Name() != Name || res.Model != m.Name() {
			t.Errorf("Name %q, Result.Model %q", m.Name(), res.Model)
		}
		if res.Usage != Usage(onMessage) || res.Took < 0 {
			t.Errorf("usage %+v, took %v", res.Usage, res.Took)
		}
		if tab.Attempts() != 1 || !reflect.DeepEqual(tab.Calls(), []decide.Call{onMessage}) || !reflect.DeepEqual(tab.Keys(), []string{"sk-test"}) {
			t.Errorf("attempts %d, calls %+v, keys %d", tab.Attempts(), tab.Calls(), len(tab.Keys()))
		}
		res, err = m.Decide(context.Background(), onSubject)
		if err != nil || len(res.Answers) != 1 || res.Answers[0] != (decide.Answer{P: 0, OK: true}) {
			t.Errorf("a statement the table does not set: %+v, %v", res, err)
		}
	})

	t.Run("usage not reported", func(t *testing.T) {
		tab, m := setup(t)
		tab.OmitUsage(true)
		res, err := m.Decide(context.Background(), onSubject)
		if err != nil || res.Usage.Reported {
			t.Errorf("%+v, %v", res, err)
		}
	})

	for _, c := range []struct {
		failure    Failure
		err        error
		retry      bool
		retryAfter time.Duration
		set        time.Duration
	}{
		{Unavailable, decide.ErrUnavailable, true, 0, 0},
		{Unavailable, decide.ErrUnavailable, true, 1500 * time.Millisecond, 1500 * time.Millisecond},
		{SlowDown, decide.ErrUnavailable, true, 2 * time.Second, 2 * time.Second},
		{SlowDown, decide.ErrUnavailable, true, decide.MaxRetryAfter, 30 * time.Second},
		{Drop, decide.ErrUnavailable, true, 0, time.Second},
		{KeyRefused, decide.ErrKeyRefused, false, 0, time.Second},
		{ProviderLimit, decide.ErrProviderLimit, false, 0, time.Second},
		{BadRequest, decide.ErrRequest, false, 0, 0},
		{Redirect, decide.ErrRequest, false, 0, 0},
		{Invalid, decide.ErrInvalidAnswer, false, 0, 0},
		{OtherModel, decide.ErrModelChanged, false, 0, 0},
	} {
		t.Run(fmt.Sprintf("failure %d after %v", c.failure, c.set), func(t *testing.T) {
			tab, m := setup(t)
			tab.SetRetryAfter(c.set)
			tab.Fail(c.failure)
			res, err := m.Decide(context.Background(), onMessage)
			check(t, res, err, c.err, true, c.retry, c.retryAfter)
			if tab.Attempts() != 1 {
				t.Errorf("%d attempts, want 1", tab.Attempts())
			}
			if _, err := m.Decide(context.Background(), onMessage); err != nil {
				t.Errorf("the next attempt: %v", err)
			}
		})
	}

	t.Run("every attempt failing", func(t *testing.T) {
		tab, m := setup(t)
		tab.FailAlways(Unavailable)
		for range 3 {
			res, err := m.Decide(context.Background(), onSubject)
			check(t, res, err, decide.ErrUnavailable, true, true, 0)
		}
		tab.FailAlways(0)
		if _, err := m.Decide(context.Background(), onSubject); err != nil || tab.Attempts() != 4 {
			t.Errorf("after: %v, %d attempts", err, tab.Attempts())
		}
	})

	t.Run("no key: nothing sent", func(t *testing.T) {
		tab := NewTable()
		m := newModel(t, tab, withKey(""))
		res, err := m.Decide(context.Background(), onMessage)
		check(t, res, err, decide.ErrKeyRefused, false, false, 0)
		if tab.Attempts() != 0 {
			t.Errorf("%d attempts", tab.Attempts())
		}
	})

	t.Run("another key: refused by the provider", func(t *testing.T) {
		tab := NewTable()
		tab.RequireKey("sk-right")
		res, err := newModel(t, tab, withKey("sk-wrong")).Decide(context.Background(), onSubject)
		check(t, res, err, decide.ErrKeyRefused, true, false, 0)
		if _, err := newModel(t, tab, withKey("sk-right")).Decide(context.Background(), onSubject); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(tab.Keys(), []string{"sk-wrong", "sk-right"}) {
			t.Errorf("keys %q", tab.Keys())
		}
	})

	t.Run("a context that ended: nothing sent", func(t *testing.T) {
		tab, m := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := m.Decide(ctx, onMessage)
		check(t, res, err, decide.ErrUnavailable, false, false, 0)
		if tab.Attempts() != 0 {
			t.Errorf("%d attempts", tab.Attempts())
		}
	})

	t.Run("an answer after the deadline is not used", func(t *testing.T) {
		tab, m := setup(t)
		tab.Fail(Late)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		res, err := m.Decide(ctx, onMessage)
		check(t, res, err, decide.ErrUnavailable, true, false, 0)
		if tab.Attempts() != 1 {
			t.Errorf("%d attempts", tab.Attempts())
		}
	})

	t.Run("a slow answer within the deadline", func(t *testing.T) {
		tab, m := setup(t)
		tab.SetDelay(50 * time.Millisecond)
		res, err := m.Decide(context.Background(), onSubject)
		if err != nil || res.Took < 50*time.Millisecond {
			t.Errorf("%+v, %v", res, err)
		}
	})

	t.Run("a call the client does not send", func(t *testing.T) {
		tab, m := setup(t)
		exact := query.Atom{Var: contracts.VarBody, Op: contracts.OpContains, Topic: "x"}
		many := make([]query.Atom, decide.MaxStatementsPerCall+1)
		for i := range many {
			many[i] = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: fmt.Sprint(i)}
		}
		for name, c := range map[string]decide.Call{
			"no statement":                  {Content: message},
			"a statement no model decides":  {Content: message, Statements: []query.Atom{exact}},
			"a subject atom on the message": {Content: message, Statements: []query.Atom{subject}},
			"a message atom on the subject": {Content: onSubject.Content, Statements: []query.Atom{about}},
			"no kind of content":            {Content: decide.Content{Subject: "x"}, Statements: []query.Atom{subject}},
			"past MaxStatementsPerCall":     {Content: message, Statements: many},
		} {
			res, err := m.Decide(context.Background(), c)
			if t.Run(name, func(t *testing.T) { check(t, res, err, decide.ErrRequest, false, false, 0) }); tab.Attempts() != 0 {
				t.Fatalf("%s: sent", name)
			}
		}
	})

	t.Run("attempts at once", func(t *testing.T) {
		tab, m := setup(t)
		tab.Set(subject, 0.8)
		var wg sync.WaitGroup
		errs := make(chan error, decide.Concurrent)
		for range decide.Concurrent {
			wg.Go(func() {
				res, err := m.Decide(context.Background(), onSubject)
				if err == nil && res.Answers[0].P != 0.8 {
					err = errors.New("another answer")
				}
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
		if tab.Attempts() != decide.Concurrent {
			t.Errorf("%d attempts", tab.Attempts())
		}
	})
}

// check asserts an attempt failed as want, with no result.
func check(t *testing.T, res *decide.Result, err, want error, sent, retry bool, retryAfter time.Duration) {
	t.Helper()
	var ae *decide.AttemptError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an AttemptError", err)
	}
	if res != nil || !errors.Is(err, want) || ae.Sent != sent || ae.Retry != retry || ae.RetryAfter != retryAfter {
		t.Errorf("got %+v (result %v), want %v sent %v retry %v after %v", *ae, res, want, sent, retry, retryAfter)
	}
}
