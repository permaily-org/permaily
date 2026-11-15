package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Asking for one message (ADR 0028, "Kept decisions", "What the matcher
// does"): what was kept first, by view; the text read only when a message
// atom has no kept answer for the view; the subject call and the message
// call sent at once, each within the guard; and an answer used only once it
// is kept, as the entry Keep returns holds it.

// errNoQuestion is Ask's answer to a nil question.
var errNoQuestion = errors.New("decisions: no question")

// subjectDigest is the hex SHA-256 of a subject, as KeptView keeps it.
func subjectDigest(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

// keptFor finds the answer kept for a key: the content's hash, the atom,
// the model and the prompt version.
func keptFor(k *inbox.KeptDecisions, content string, a query.Atom, model string) (inbox.KeptAnswer, bool) {
	if k == nil || content == "" || model == "" {
		return inbox.KeptAnswer{}, false
	}
	for _, ka := range k.Answers {
		if ka.Content == content && ka.Var == string(a.Var) && ka.Op == string(a.Op) && ka.Topic == a.Topic &&
			ka.Model == model && ka.Prompt == decide.PromptVersion {
			return ka, true
		}
	}
	return inbox.KeptAnswer{}, false
}

// asking is one Ask's state.
type asking struct {
	out      *inbox.Asked
	kept     *inbox.KeptDecisions
	model    string
	subjHash string
	msgHash  string
}

// use puts a kept answer in out: its probability, or nothing for a
// refusal, which stays unknown.
func (a *asking) use(atom query.Atom, ka inbox.KeptAnswer) {
	if !ka.Refused {
		a.out.Answers[atom] = ka.P
	}
}

// hashOf is the content hash an atom's kept answer is for, "" when it is
// not known yet.
func (a *asking) hashOf(atom query.Atom) string {
	if decide.KindOf(atom) == decide.ContentSubject {
		return a.subjHash
	}
	return a.msgHash
}

// holdAll holds every atom of list for the outcome h.
func (a *asking) holdAll(list []query.Atom, h hold) {
	if len(list) == 0 || h == holdNone {
		return
	}
	a.out.Held = append(a.out.Held, list...)
	switch {
	case h == holdOff:
		a.out.Outcome = inbox.AskOff
	case a.out.Outcome != inbox.AskOff:
		a.out.Outcome = inbox.AskLimit
	}
}

// Ask implements inbox.Asker.
func (s *Service) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	if q == nil {
		return nil, errNoQuestion
	}
	a := &asking{out: &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered}}
	var atoms []query.Atom
	seen := map[query.Atom]bool{}
	for _, atom := range q.Atoms {
		if seen[atom] || decide.KindOf(atom) == 0 {
			continue
		}
		seen[atom] = true
		if len(atoms) == decide.MaxAtomsPerMessage {
			s.logKind(slog.LevelWarn, "decisions: a message has more atoms than one is asked; the others stay unknown", "too_many_atoms")
			break
		}
		atoms = append(atoms, atom)
	}
	if len(atoms) == 0 {
		return a.out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, decide.MessageDeadline)
	defer cancel()
	kept, err := s.cfg.Kept.Kept(ctx, q.Box, q.MessageID)
	if err != nil {
		return nil, err
	}
	a.kept = kept
	if s.model != nil {
		a.model = s.model.Name()
	}
	a.subjHash = decide.ContentHash(decide.Content{Kind: decide.ContentSubject, Subject: q.Subject})
	viewKey := decide.PromptVersion + "/" + q.View.Key()
	subjDigest := subjectDigest(q.Subject)
	if kept != nil {
		if v, ok := kept.Views[viewKey]; ok && v.Subject == subjDigest {
			a.msgHash = v.Content
		}
	}
	// What was kept, then what an empty subject decides by rule.
	var missing []query.Atom
	for _, atom := range atoms {
		if ka, ok := keptFor(kept, a.hashOf(atom), atom, a.model); ok {
			a.use(atom, ka)
			s.count(attestation.ModelCallKept, 1)
			continue
		}
		missing = append(missing, atom)
	}
	if len(missing) == 0 {
		return a.out, nil
	}
	var byRule []query.Atom
	missing = filterAtoms(missing, func(atom query.Atom) bool {
		if decide.KindOf(atom) == decide.ContentSubject && q.Subject == "" {
			byRule = append(byRule, atom)
			return false
		}
		return true
	})
	setRule := func() {
		for _, atom := range byRule {
			a.out.Answers[atom] = 0
		}
	}
	if len(missing) == 0 {
		setRule()
		return a.out, nil
	}
	// Nothing is read for a message the guard would not ask about.
	k := s.keyState(ctx)
	switch {
	case s.model == nil || !k.set:
		setRule()
		a.holdAll(missing, holdOff)
		return a.out, nil
	default:
		if h := s.held(k.fp); h != holdNone {
			setRule()
			a.holdAll(missing, h)
			return a.out, nil
		}
	}
	// A new message atom the entry has no room for stays unknown before
	// the text is read, so it spends no read and stops no page.
	missing = s.withinRoom(missing, kept, a.model, q.Subject != "")
	// The text, before anything is asked, when a message atom needs it.
	text, hasText := "", false
	if containsKind(missing, decide.ContentMessage) {
		st := inbox.TextUnavailable
		if q.Text != nil {
			var err error
			if text, st, err = q.Text(ctx); err != nil {
				return nil, err
			}
		}
		switch st {
		case inbox.TextUnaffordable:
			a.out.Outcome = inbox.AskStop
			return a.out, nil
		case inbox.TextRead:
			hasText = true
			a.msgHash = decide.ContentHash(decide.Content{Kind: decide.ContentMessage, Subject: q.Subject, Body: text})
			missing = filterAtoms(missing, func(atom query.Atom) bool {
				if decide.KindOf(atom) != decide.ContentMessage {
					return true
				}
				ka, ok := keptFor(kept, a.msgHash, atom, a.model)
				if ok {
					a.use(atom, ka)
					s.count(attestation.ModelCallKept, 1)
				}
				return !ok
			})
		default:
			text = ""
		}
	}
	setRule()
	calls, rule := plan(missing, q.Subject, text, hasText)
	for atom, p := range rule {
		a.out.Answers[atom] = p
	}
	calls = s.fit(calls, missing, kept)
	if len(calls) == 0 {
		return a.out, nil
	}
	results := make([]called, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() { results[i] = s.call(ctx, c, q.Account, false) })
	}
	wg.Wait()
	checked, change := s.canaryDays()
	add := &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}}
	if hasText {
		add.Views[viewKey] = inbox.KeptView{Content: a.msgHash, Subject: subjDigest}
	}
	now := s.now()
	var asked []query.Atom // the atom of each of add.Answers
	for i, r := range results {
		a.out.Called += r.attempts
		a.holdAll(calls[i].Statements, r.hold)
		if r.res == nil {
			continue
		}
		content := decide.ContentHash(calls[i].Content)
		for j, ans := range r.res.Answers {
			atom := calls[i].Statements[j]
			if !ans.OK && !ans.Refused {
				continue // no answer: unknown, and nothing kept
			}
			if ans.Refused {
				s.count(attestation.ModelCallRefusal, 1)
			}
			p := ans.P
			if !ans.OK {
				p = 0
			}
			add.Answers = append(add.Answers, inbox.KeptAnswer{
				Content: content, Var: string(atom.Var), Op: string(atom.Op), Topic: atom.Topic,
				Model: r.res.Model, Prompt: decide.PromptVersion, P: p, Refused: ans.Refused, At: now,
				CanaryChecked: checked, CanaryChange: change,
			})
			asked = append(asked, atom)
		}
	}
	if len(add.Answers) == 0 {
		return a.out, nil
	}
	written, err := s.cfg.Kept.Keep(ctx, q.Box, q.MessageID, q.ExpiresAt, add)
	switch {
	case errors.Is(err, inbox.ErrGone):
		s.logKind(slog.LevelInfo, "decisions: a message was deleted while it was asked about; nothing new is used", "message_gone")
		return a.out, nil
	case err != nil:
		s.logKind(slog.LevelWarn, "decisions: a message's answers could not be kept; none of them is used", "keep_failed")
		return a.out, nil
	}
	for i, ka := range add.Answers {
		atom := asked[i]
		if w, ok := keptFor(written, ka.Content, atom, a.model); ok {
			a.use(atom, w)
		}
	}
	return a.out, nil
}

// fit keeps of calls only as many statements as the message's entry has
// room for, the first of missing, which is in the order of the question's
// atoms; the others stay unknown (ADR 0028, "Bounded").
func (s *Service) fit(calls []decide.Call, missing []query.Atom, kept *inbox.KeptDecisions) []decide.Call {
	room := inbox.KeptMaxAnswers
	if kept != nil {
		room -= len(kept.Answers)
	}
	planned := map[query.Atom]bool{}
	for _, c := range calls {
		for _, a := range c.Statements {
			planned[a] = true
		}
	}
	if len(planned) <= room {
		return calls
	}
	s.logKind(slog.LevelWarn, "decisions: a message's entry is full; new atoms are not asked", "entry_full")
	chosen := map[query.Atom]bool{}
	for _, a := range missing {
		if len(chosen) >= room {
			break
		}
		if planned[a] {
			chosen[a] = true
		}
	}
	var out []decide.Call
	for _, c := range calls {
		c.Statements = filterAtoms(c.Statements, func(a query.Atom) bool { return chosen[a] })
		if len(c.Statements) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// withinRoom leaves out, before the text is read, the message atoms that
// fit would leave out after it: an atom with no kept answer under any
// content (definitely new) once the atoms before it in the question's
// order that are definitely new took the entry's room. An atom kept for
// another content may find its answer under the text's hash, so it is
// kept and takes no room here; subject atoms are never left out here, so a
// text that cannot be read leaves them their room. With an empty subject
// nothing is left out: an empty text decides message atoms by rule, which
// takes no room.
func (s *Service) withinRoom(missing []query.Atom, kept *inbox.KeptDecisions, model string, subject bool) []query.Atom {
	if !subject {
		return missing
	}
	room := inbox.KeptMaxAnswers
	if kept != nil {
		room -= len(kept.Answers)
	}
	dropped := false
	out := filterAtoms(missing, func(atom query.Atom) bool {
		message := decide.KindOf(atom) == decide.ContentMessage
		if message && keptAnyContent(kept, atom, model) {
			return true
		}
		if room > 0 {
			room--
			return true
		}
		if !message {
			return true
		}
		dropped = true
		return false
	})
	if dropped {
		s.logKind(slog.LevelWarn, "decisions: a message's entry is full; new atoms are not asked", "entry_full")
	}
	return out
}

// keptAnyContent reports whether k keeps an answer for atom, model and
// prompt version under any content.
func keptAnyContent(k *inbox.KeptDecisions, atom query.Atom, model string) bool {
	if k == nil || model == "" {
		return false
	}
	for _, ka := range k.Answers {
		if ka.Var == string(atom.Var) && ka.Op == string(atom.Op) && ka.Topic == atom.Topic && ka.Model == model && ka.Prompt == decide.PromptVersion {
			return true
		}
	}
	return false
}

func filterAtoms(list []query.Atom, keep func(query.Atom) bool) []query.Atom {
	out := list[:0:0]
	for _, a := range list {
		if keep(a) {
			out = append(out, a)
		}
	}
	return out
}

func containsKind(list []query.Atom, kind decide.ContentKind) bool {
	for _, a := range list {
		if decide.KindOf(a) == kind {
			return true
		}
	}
	return false
}
