package decisions

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
)

// A11. Kept first, by view.
func TestA11KeptFirstByView(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	e.model.set(subj("invoices"), 0.97)
	e.model.set(about("invoices"), 0.91)
	q := newQuestion("m1", "Invoice 12", "Please pay invoice 12.", subj("invoices"), about("invoices"))
	first := e.ask(q)
	if first.Answers[subj("invoices")] != 0.97 || first.Answers[about("invoices")] != 0.91 || q.textReads() != 1 || e.model.count() != 2 {
		t.Fatalf("first ask: %+v, %d reads, %d calls", first, q.textReads(), e.model.count())
	}

	t.Run("every atom kept for the view: no call, no text", func(t *testing.T) {
		q := newQuestion("m1", "Invoice 12", "Please pay invoice 12.", subj("invoices"), about("invoices"))
		out := e.ask(q)
		if out.Answers[about("invoices")] != 0.91 || out.Called != 0 || q.textReads() != 0 || e.model.count() != 2 {
			t.Fatalf("%+v, %d reads, %d calls", out, q.textReads(), e.model.count())
		}
	})
	t.Run("only subject atoms missing: no text", func(t *testing.T) {
		q := newQuestion("m1", "Invoice 12", "Please pay invoice 12.", subj("facturas"), about("invoices"))
		out := e.ask(q)
		if q.textReads() != 0 || out.Called != 1 || e.model.count() != 3 {
			t.Fatalf("%+v, %d reads", out, q.textReads())
		}
		if c := e.model.calls[2]; c.Content.Kind != decide.ContentSubject || c.Content.Body != "" {
			t.Fatalf("the subject call holds %+v", c.Content)
		}
	})
	t.Run("another subject under the same view reads the text", func(t *testing.T) {
		q := newQuestion("m1", "Invoice 12 (corrected)", "Please pay invoice 12.", about("invoices"))
		out := e.ask(q)
		if q.textReads() != 1 || out.Called != 1 {
			t.Fatalf("%+v, %d reads", out, q.textReads())
		}
	})
	t.Run("another TextVersion reads the text; the same bytes use their answers", func(t *testing.T) {
		q := newQuestion("m1", "Invoice 12", "Please pay invoice 12.", about("invoices"))
		q.View = inbox.TextView{Name: "original", Version: inbox.TextVersion + 1}
		calls := e.model.count()
		out := e.ask(q)
		if q.textReads() != 1 || out.Called != 0 || e.model.count() != calls || out.Answers[about("invoices")] != 0.91 {
			t.Fatalf("%+v, %d reads", out, q.textReads())
		}
	})
	t.Run("a view with the address removed keeps its own hash", func(t *testing.T) {
		e.model.set(about("a home address"), 0.9)
		orig := newQuestion("m2", "Moving", "We moved to 12 Elm Street, Springfield.", about("a home address"))
		if out := e.ask(orig); out.Answers[about("a home address")] != 0.9 {
			t.Fatalf("original: %+v", out)
		}
		e.model.set(about("a home address"), 0.02)
		removed := newQuestion("m2", "Moving", "We moved to [address removed].", about("a home address"))
		removed.View = inbox.TextView{Name: "without-address", Version: 1}
		if out := e.ask(removed); out.Answers[about("a home address")] != 0.02 || out.Called != 1 {
			t.Fatalf("removed view used the original's answer: %+v", out)
		}
		again := newQuestion("m2", "Moving", "We moved to 12 Elm Street, Springfield.", about("a home address"))
		if out := e.ask(again); out.Answers[about("a home address")] != 0.9 || again.textReads() != 0 {
			t.Fatalf("original view after the removed one: %+v", out)
		}
		kept, _ := w.kept.read(testBox.MailboxID + "/m2")
		if len(kept.Views) != 2 || kept.Views["p2-2026-11-15/original/1"].Content == kept.Views["p2-2026-11-15/without-address/1"].Content {
			t.Fatalf("views %+v", kept.Views)
		}
	})
	t.Run("two writers that kept different contents each keep theirs", func(t *testing.T) {
		other := w.keyed()
		e.model.set(about("travel bookings"), 0.8)
		other.model.set(about("travel bookings"), 0.1)
		a := e.ask(newQuestion("m3", "Trip", "Your flight to Lisbon is booked.", about("travel bookings")))
		qb := newQuestion("m3", "Trip", "Your flight to [place removed] is booked.", about("travel bookings"))
		qb.View = inbox.TextView{Name: "without-place", Version: 1}
		b := other.ask(qb)
		if a.Answers[about("travel bookings")] != 0.8 || b.Answers[about("travel bookings")] != 0.1 {
			t.Fatalf("%+v %+v", a, b)
		}
		d, _ := w.kept.read(testBox.MailboxID + "/m3")
		if len(d.Answers) != 2 || d.Answers[0].Content == d.Answers[1].Content {
			t.Fatalf("each writer keeps its own: %+v", d.Answers)
		}
	})
}

// A12. The key of an answer: message, content hash, atom, model and prompt
// version; never a formula.
func TestA12TheKeyOfAnAnswer(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	e.model.set(mentions("invoices"), 0.9)
	// Two formulas holding one atom read one answer (Q38).
	q := newQuestion("m1", "Hi", "the invoice is attached", mentions("invoices"), mentions("invoices"))
	out := e.ask(q)
	if len(e.model.calls) != 1 || len(e.model.calls[0].Statements) != 1 || out.Answers[mentions("invoices")] != 0.9 {
		t.Fatalf("one atom asked once: %+v, %+v", out, e.model.calls)
	}
	again := e.ask(newQuestion("m1", "Hi", "the invoice is attached", mentions("invoices")))
	if again.Called != 0 || again.Answers[mentions("invoices")] != 0.9 {
		t.Fatalf("a second formula with the atom: %+v", again)
	}

	t.Run("another prompt version or model name asks again", func(t *testing.T) {
		d, _ := w.kept.read(testBox.MailboxID + "/m1")
		for i := range d.Answers {
			d.Answers[i].Prompt = "p1-2026-11-15"
		}
		d2 := *d
		d2.Answers = slices.Clone(d.Answers)
		for i := range d2.Answers {
			d2.Answers[i].Prompt, d2.Answers[i].Model = decide.PromptVersion, "gpt-7"
		}
		d.Answers = append(d.Answers, d2.Answers...)
		w.kept.put("m1", d)
		out := e.ask(newQuestion("m1", "Hi", "the invoice is attached", mentions("invoices")))
		if out.Called != 1 {
			t.Fatalf("kept under another version or model was used: %+v", out)
		}
	})
	t.Run("a kept refusal stays unknown and is not asked again", func(t *testing.T) {
		e.model.refuse[about("job offers")] = true
		q := newQuestion("m4", "Hi", "text", about("job offers"))
		out := e.ask(q)
		if _, ok := out.Answers[about("job offers")]; ok || out.Called != 1 || len(out.Held) != 0 {
			t.Fatalf("refusal: %+v", out)
		}
		out = e.ask(newQuestion("m4", "Hi", "text", about("job offers")))
		if _, ok := out.Answers[about("job offers")]; ok || out.Called != 0 {
			t.Fatalf("refusal asked again: %+v", out)
		}
		if s := e.Status(context.Background()); s.Calls["refusal"] != 1 {
			t.Fatalf("refusals counted %d", s.Calls["refusal"])
		}
	})
	t.Run("the rule's 0 for an empty field is not kept, and never for a text that could not be read", func(t *testing.T) {
		q := newQuestion("m5", "", "", subj("invoices"), about("invoices"))
		out := e.ask(q)
		if p, ok := out.Answers[subj("invoices")]; !ok || p != 0 {
			t.Fatalf("empty subject: %+v", out)
		}
		if p, ok := out.Answers[about("invoices")]; !ok || p != 0 || out.Called != 0 {
			t.Fatalf("empty subject and text: %+v", out)
		}
		if d, _ := w.kept.read(testBox.MailboxID + "/m5"); d != nil {
			t.Fatalf("a rule's answer was kept: %+v", d)
		}
		u := newQuestion("m6", "", "", about("invoices"))
		u.Text = func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextUnavailable, nil }
		out = e.ask(u)
		if _, ok := out.Answers[about("invoices")]; ok || out.Called != 0 || len(out.Held) != 0 {
			t.Fatalf("a text that could not be read got an answer: %+v", out)
		}
	})
}

// A13. Two enclaves ask one message at once, the provider answering
// differently to each: both use the answer kept first.
func TestA13TwoEnclavesUseTheAnswerKeptFirst(t *testing.T) {
	w := newWorld(t)
	a, b := w.keyed(), w.keyed()
	a.model.set(about("invoices"), 0.9)
	b.model.set(about("invoices"), 0.1)
	// a's write comes first: b, which asked at the same time, writes after.
	var once sync.Once
	ready := make(chan struct{})
	w.kept.before = func(id string, try int) {}
	b.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
		once.Do(func() { <-ready })
		return nil, nil
	})
	var outB *inbox.Asked
	done := make(chan struct{})
	go func() {
		outB = b.ask(newQuestion("m1", "Invoice", "pay it", about("invoices")))
		close(done)
	}()
	outA := a.ask(newQuestion("m1", "Invoice", "pay it", about("invoices")))
	close(ready)
	<-done
	if outA.Answers[about("invoices")] != 0.9 || outB.Answers[about("invoices")] != 0.9 {
		t.Fatalf("a %+v, b %+v: both use the answer kept first", outA, outB)
	}
	d, _ := w.kept.read(testBox.MailboxID + "/m1")
	if len(d.Answers) != 1 {
		t.Fatalf("kept %+v", d.Answers)
	}
}

// A18. Failures are unknown: a timeout, ErrUnavailable after the retries,
// ErrRequest and ErrInvalidAnswer leave their atoms out of Answers and out
// of Held, and keep nothing.
func TestA18FailuresAreUnknown(t *testing.T) {
	cases := map[string]error{
		"timeout":     &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true},
		"unavailable": &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true},
		"request":     &decide.AttemptError{Err: decide.ErrRequest, Sent: true},
		"invalid":     &decide.AttemptError{Err: decide.ErrInvalidAnswer, Sent: true},
		"other model": &decide.AttemptError{Err: decide.ErrModelChanged, Sent: true},
		"not ours":    errors.New("transport"),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) { return nil, failure })
			ctx := context.Background()
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				e.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
			}
			out, err := e.Ask(ctx, newQuestion("m1", "Hi", "text", subj("invoices"), about("invoices")).Question)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Answers) != 0 || len(out.Held) != 0 || out.Outcome != inbox.AskAnswered {
				t.Fatalf("%+v", out)
			}
			if w.kept.keeps != 0 {
				t.Fatalf("kept %d", w.kept.keeps)
			}
		})
	}
	t.Run("retried twice, then unknown, each attempt reserved", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true}
		})
		out := e.ask(newQuestion("m1", "Hi", "text", about("invoices")))
		if out.Called != 1+decide.Retries || len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
		s := e.Status(context.Background())
		if s.Calls["unavailable"] != 1 {
			t.Fatalf("calls %+v", s.Calls)
		}
	})
}

// A38. The outcome of an ask.
func TestA38TheOutcomeOfAnAsk(t *testing.T) {
	atoms := []query.Atom{subj("invoices"), about("invoices")}
	t.Run("no key is AskOff", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		out := e.ask(newQuestion("m1", "Hi", "text", atoms...))
		if out.Outcome != inbox.AskOff || len(out.Held) != 2 || len(out.Answers) != 0 || e.model.count() != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("no model is AskOff", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave(withoutModel())
		if out := e.ask(newQuestion("m1", "Hi", "text", atoms...)); out.Outcome != inbox.AskOff || len(out.Held) != 2 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("a refused key is AskOff", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrKeyRefused, Sent: true}
		})
		out := e.ask(newQuestion("m1", "Hi", "text", atoms...))
		if out.Outcome != inbox.AskOff || len(out.Held) != 2 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("the provider's limit is AskLimit", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrProviderLimit, Sent: true}
		})
		if out := e.ask(newQuestion("m1", "Hi", "text", atoms...)); out.Outcome != inbox.AskLimit || len(out.Held) != 2 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("the month's limit is AskLimit", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend("2026-11", "2026-11-01", 500)
		e := w.keyed()
		if out := e.ask(newQuestion("m1", "Hi", "text", atoms...)); out.Outcome != inbox.AskLimit || len(out.Held) != 2 || e.model.count() != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("the account's share is AskLimit", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(Limits{Month: DefaultLimits.Month, DayAlarm: DefaultLimits.DayAlarm, AccountDay: 1000, Block: DefaultLimits.Block}))
		if out := e.ask(newQuestion("m1", "Hi", "text", atoms...)); out.Outcome != inbox.AskLimit || len(out.Held) != 2 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an unreadable spend record is AskLimit", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend("2026-11", "2026-11-01", 1)
		for _, n := range w.versions(RecordSpend + "2026-11") {
			_ = w.records.DeleteRecord(context.Background(), n)
		}
		e := w.keyed()
		if out := e.ask(newQuestion("m1", "Hi", "text", atoms...)); out.Outcome != inbox.AskLimit || len(out.Held) != 2 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("every atom kept is AskAnswered with no attempt", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.ask(newQuestion("m1", "Hi", "text", atoms...))
		_ = e.RemoveKey(context.Background())
		out := e.ask(newQuestion("m1", "Hi", "text", atoms...))
		if out.Outcome != inbox.AskAnswered || out.Called != 0 || len(out.Answers) != 2 || len(out.Held) != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("kept answers with some atoms held", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.ask(newQuestion("m1", "Hi", "text", subj("invoices")))
		_ = e.RemoveKey(context.Background())
		out := e.ask(newQuestion("m1", "Hi", "text", atoms...))
		if out.Outcome != inbox.AskOff || len(out.Answers) != 1 || !slices.Equal(out.Held, []query.Atom{about("invoices")}) {
			t.Fatalf("%+v", out)
		}
		if _, ok := out.Answers[about("invoices")]; ok {
			t.Fatal("a held atom has a probability")
		}
	})
	t.Run("TextUnaffordable is AskStop with nothing asked or reserved", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		q := newQuestion("m1", "Hi", "text", atoms...)
		q.Text = func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextUnaffordable, nil }
		out := e.ask(q)
		if out.Outcome != inbox.AskStop || len(out.Answers) != 0 || len(out.Held) != 0 || e.model.count() != 0 {
			t.Fatalf("%+v", out)
		}
		if d := w.versions(RecordSpend + "2026-11"); len(d) != 0 {
			t.Fatalf("reserved: %v", d)
		}
	})
	t.Run("a text that cannot be read: message atoms unknown, subject atoms asked", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		q := newQuestion("m1", "Hi", "", atoms...)
		q.Text = func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextUnavailable, nil }
		out := e.ask(q)
		if _, ok := out.Answers[subj("invoices")]; !ok || len(out.Answers) != 1 || len(out.Held) != 0 || out.Outcome != inbox.AskAnswered {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("a storage error is an error", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		q := newQuestion("m1", "Hi", "", atoms...)
		q.Text = func(context.Context) (string, inbox.TextState, error) { return "", 0, errors.New("storage") }
		if _, err := e.Ask(context.Background(), q.Question); err == nil {
			t.Fatal("no error")
		}
		if _, err := e.Ask(context.Background(), nil); err == nil {
			t.Fatal("nil question")
		}
	})
	t.Run("no atom a model decides", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		out := e.ask(newQuestion("m1", "Hi", "", query.Atom{Var: "from_domain", Op: "is", Topic: "x"}))
		if out.Outcome != inbox.AskAnswered || len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
	})
}

// A39. A full entry.
func TestA39AFullEntry(t *testing.T) {
	full := func(n int) *inbox.KeptDecisions {
		d := &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}}
		h := decide.ContentHash(decide.Content{Kind: decide.ContentSubject, Subject: "Hi"})
		for i := range n {
			d.Answers = append(d.Answers, inbox.KeptAnswer{Content: h, Var: "subject", Op: "about", Topic: "topic " + itoa(i), Model: "gpt-6-luna", Prompt: decide.PromptVersion, P: 0.5})
		}
		return d
	}
	for _, have := range []int{254, 255, 256} {
		t.Run("room for "+itoa(inbox.KeptMaxAnswers-have), func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			w.kept.put("m1", full(have))
			out := e.ask(newQuestion("m1", "Hi", "text", subj("topic 0"), subj("new a"), subj("new b")))
			if out.Answers[subj("topic 0")] != 0.5 {
				t.Fatalf("an existing answer was not used: %+v", out)
			}
			room := inbox.KeptMaxAnswers - have
			got := 0
			for _, a := range []query.Atom{subj("new a"), subj("new b")} {
				if _, ok := out.Answers[a]; ok {
					got++
				}
			}
			if got != min(room, 2) || len(out.Held) != 0 {
				t.Fatalf("room %d, %d new answers: %+v", room, got, out)
			}
			if room == 1 {
				if _, ok := out.Answers[subj("new a")]; !ok {
					t.Fatal("the first atom in the question's order is the one asked")
				}
			}
		})
	}
	t.Run("two services race two atoms for the last slot", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		w.kept.put("m1", full(255))
		var bOut *inbox.Asked
		w.kept.before = func(id string, try int) {
			if try == 1 && bOut == nil {
				w.kept.before = nil
				bOut = b.ask(newQuestion("m1", "Hi", "text", subj("new b")))
			}
		}
		aOut := a.ask(newQuestion("m1", "Hi", "text", subj("new a")))
		_, aok := aOut.Answers[subj("new a")]
		_, bok := bOut.Answers[subj("new b")]
		if aok || !bok {
			t.Fatalf("one kept (b's, written first), the other unknown and unused: a %+v, b %+v", aOut, bOut)
		}
	})
	t.Run("two answers for one atom when the entry is full", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		a.model.set(subj("new"), 0.9)
		b.model.set(subj("new"), 0.1)
		w.kept.put("m1", full(255))
		w.kept.before = func(id string, try int) {
			if try == 1 {
				w.kept.before = nil
				w.kept.put("m1", full(256)) // another atom took the last slot
			}
		}
		out := a.ask(newQuestion("m1", "Hi", "text", subj("new")))
		if _, ok := out.Answers[subj("new")]; ok {
			t.Fatalf("an answer the entry has no room for was used: %+v", out)
		}
		out = b.ask(newQuestion("m1", "Hi", "text", subj("new")))
		if _, ok := out.Answers[subj("new")]; ok || out.Called != 0 {
			t.Fatalf("b: %+v", out)
		}
	})
	t.Run("Keep failing uses none of the new answers", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		w.kept.fail = errors.New("the store refused the write")
		out := e.ask(newQuestion("m1", "Hi", "text", subj("x"), about("x")))
		if len(out.Answers) != 0 || out.Called != 2 {
			t.Fatalf("%+v", out)
		}
		w.kept.fail = nil
		w.kept.before = func(id string, try int) { w.kept.put("m1", &inbox.KeptDecisions{V: 1}) } // KeepTries exhausted
		out = e.ask(newQuestion("m1", "Hi", "text", subj("x")))
		if len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("a message deleted while it was asked about uses nothing new", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		w.kept.gone[testBox.MailboxID+"/m1"] = true
		out := e.ask(newQuestion("m1", "Hi", "text", subj("x")))
		if len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
		if d, _ := w.kept.read(testBox.MailboxID + "/m1"); d != nil {
			t.Fatal("kept for a message that is gone")
		}
	})
	t.Run("more atoms than a message is asked", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		var atoms []query.Atom
		for i := range decide.MaxAtomsPerMessage + 3 {
			atoms = append(atoms, subj("t"+itoa(i)))
		}
		out := e.ask(newQuestion("m1", "Hi", "text", atoms...))
		if len(out.Answers) != decide.MaxAtomsPerMessage {
			t.Fatalf("%d answers", len(out.Answers))
		}
	})
}

// A40. Today's behaviour when the table is rewritten, pinned. It does not
// meet step 7's prerequisite that rule-mode answers be rollback resistant
// before a rule that protects relies on one (docs/plan.md, step 7): a lost
// answer is asked again, and may come back different after a change of
// the model.
func TestA40TheTableRewrittenAsksAgainNeverAnError(t *testing.T) {
	atom := mentions("a home address")
	ask := func(e *enclave) *inbox.Asked {
		return e.ask(newQuestion("m1", "Hi", "We moved to 12 Elm Street.", atom))
	}
	setup := func(t *testing.T) (*world, *enclave, *inbox.KeptDecisions) {
		w := newWorld(t)
		e := w.keyed()
		e.model.set(atom, 0.9)
		if out := ask(e); out.Answers[atom] != 0.9 {
			t.Fatalf("%+v", out)
		}
		d, _ := w.kept.read(testBox.MailboxID + "/m1")
		return w, e, d
	}
	delivers := func(out *inbox.Asked) bool { return query.Classify(out.Answers[atom], query.ForQuery) == query.True }
	t.Run("deleted", func(t *testing.T) {
		w, e, _ := setup(t)
		w.kept.remove("m1")
		e.model.set(atom, 0.1)
		out := ask(e)
		if out.Called != 1 || out.Answers[atom] != 0.1 || delivers(out) {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("changed by a byte (does not open: none)", func(t *testing.T) {
		w, e, _ := setup(t)
		w.kept.remove("m1") // the real KeptStore reads an entry that does not open as none
		e.model.set(atom, 0.1)
		if out := ask(e); out.Called != 1 || delivers(out) {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an earlier copy without the atom", func(t *testing.T) {
		w, e, _ := setup(t)
		w.kept.put("m1", &inbox.KeptDecisions{V: 1})
		e.model.set(atom, 0.1)
		if out := ask(e); out.Called != 1 || out.Answers[atom] != 0.1 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an earlier copy with an older answer for the same key", func(t *testing.T) {
		w, e, d := setup(t)
		d.Answers[0].P = 0.3
		w.kept.put("m1", d)
		e.model.set(atom, 0.1)
		if out := ask(e); out.Called != 0 || out.Answers[atom] != 0.3 || delivers(out) {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("restarted, the provider answering the opposite", func(t *testing.T) {
		w, _, _ := setup(t)
		w.kept.remove("m1")
		e := w.keyed()
		e.model.set(atom, 0.05)
		out := ask(e)
		if out.Called != 1 || out.Answers[atom] != 0.05 || delivers(out) {
			t.Fatalf("%+v", out)
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
