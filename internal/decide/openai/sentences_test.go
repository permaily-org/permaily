package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

var (
	mentionsHomeAddress = query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a home address"}
	mentionsPhone       = query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: `a "phone" number\`}
)

func sentencesCall(t *testing.T) decide.Call {
	t.Helper()
	calls, ok := decide.PlanSentences([]string{"Your code [removed]", "My home is at \"12\" <b>Elm</b>.\nThanks é"}, []query.Atom{mentionsHomeAddress, mentionsPhone})
	if !ok || len(calls) != 1 {
		t.Fatal(calls, ok)
	}
	return calls[0]
}

// U20's provider half: a sentences call's body is golden: the sentences'
// developer message, the masked sentences as compact JSON text and one
// statement per sentence and topic named sNtM; its answers are read by
// those names.
func TestU20TheSentencesRequestIsGolden(t *testing.T) {
	call := sentencesCall(t)
	p := &played{answer: func(r *http.Request) (*http.Response, error) {
		return respond(r, 200, nil, `{"model":"gpt-6-luna","answers":[`+
			`{"type":"predicate","name":"s0t0","probability":0.01},{"type":"predicate","name":"s0t1","probability":0.02},`+
			`{"type":"predicate","name":"s1t0","probability":0.93},{"type":"refusal","name":"s1t1"}],"usage":{"input_tokens":900}}`), nil
	}}
	res, err := newClient(p).Decide(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.bodies[0]) != goldenSentences {
		t.Fatalf("body:\n%s\nwant:\n%s", p.bodies[0], goldenSentences)
	}
	want := []decide.Answer{{P: 0.01, OK: true}, {P: 0.02, OK: true}, {P: 0.93, OK: true}, {Refused: true}}
	for i, a := range want {
		if res.Answers[i] != a {
			t.Fatalf("answer %d: %+v", i, res.Answers[i])
		}
	}
}

// Answers named as a message call's (s0, s1, …) are not a sentences call's.
func TestU20SentencesAnswersAreNamedByTheirSentence(t *testing.T) {
	p := &played{} // answers s0, s1, …
	res, err := newClient(p).Decide(context.Background(), sentencesCall(t))
	expect(t, res, err, decide.ErrInvalidAnswer, true, false, 0)
}

// A sentences call that PlanSentences would not make is never sent.
func TestU20ASentencesCallNotSent(t *testing.T) {
	p := &played{}
	c := newClient(p)
	good := sentencesCall(t)
	withSubject := good
	withSubject.Content.Subject = "secret"
	shortOn := good
	shortOn.On = good.On[:1]
	asQuery := good
	asQuery.Statements = []query.Atom{aboutInvoices, mentionsPhone, mentionsHomeAddress, mentionsPhone}
	for _, call := range []decide.Call{withSubject, shortOn, asQuery, {Content: decide.Content{Kind: 9}, Statements: good.Statements}} {
		res, err := c.Decide(context.Background(), call)
		expect(t, res, err, decide.ErrRequest, false, false, 0)
	}
	if p.count() != 0 {
		t.Error("sent")
	}
}
