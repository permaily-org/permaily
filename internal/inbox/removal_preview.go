package inbox

import (
	"context"
	"slices"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// RemovalPreview is one message as a rule's removal would leave it, for the
// person's own preview (contracts.RuleRemovalPreview): its subject and text
// as they would leave (nil when removed whole), and what was removed, never
// what.
type RemovalPreview struct {
	Subject *string
	Text    *string
	Removed contracts.RemovedSummary
}

// PreviewRemoval is what rm would leave of a message whose subject and text
// are given (the text as MessageText gives it) and which holds attachments
// attachments, as the discloser removes it on the message's one view (ADR
// 0040, "The order", 3): the parts removed whole; every piece of the shapes
// rm names, by r; and, for topics, the sentences of the view asked through
// asker with every fixed shape masked, kept only when every topic was
// answered below the rules' threshold, every masked piece removed too (N2),
// as KeptSentences and MeaningSpans say. It begins and settles no decision
// and keeps no answer (ADR 0040, "Previews and consent"): a preview says
// what the model answers now, not what a connection's read decided. With no
// asker, a map that breaks SentenceMap.Check, or an ask that fails, the
// whole subject and text are removed, Why undecided, as a read that cannot
// decide removes them. account is whose share the ask is in.
func PreviewRemoval(ctx context.Context, r redact.Redactor, subject, text string, attachments int, rm *rules.Removal, asker SentenceAsker, account string) (*RemovalPreview, error) {
	v := redact.NewView(subject, text)
	sum := contracts.RemovedSummary{}
	var spans []redact.Span
	for _, p := range rm.Parts {
		switch p {
		case contracts.PartSubject:
			spans = append(spans, v.Subject)
		case contracts.PartBody:
			spans = append(spans, v.Body)
		}
		if p != contracts.PartAttachments && !slices.Contains(sum.Parts, p) {
			sum.Parts = append(sum.Parts, p)
		}
	}
	if rm.Sender() && !slices.Contains(sum.Parts, contracts.PartSender) {
		sum.Parts = append(sum.Parts, contracts.PartSender)
	}
	if rm.Attachments() {
		sum.Attachments = attachments
		if slices.Contains(rm.Parts, contracts.PartAttachments) {
			sum.Parts = append(sum.Parts, contracts.PartAttachments)
		}
	}
	if len(rm.Shapes) > 0 {
		for _, pc := range r.Shapes(v.Text, rm.Shapes) {
			spans = append(spans, pc.Span)
			count(&sum, pc.Kind)
		}
	}
	if len(rm.Topics) > 0 {
		sp, err := previewMeaning(ctx, r, v, rm, asker, account, &sum)
		if err != nil {
			return nil, err
		}
		spans = append(spans, sp...)
	}
	u := redact.Union(spans)
	out := &RemovalPreview{Removed: sum}
	s, t := redact.Join(v, u)
	if !covers(u, v.Subject) && !slices.Contains(rm.Parts, contracts.PartSubject) {
		out.Subject = &s
	}
	if !covers(u, v.Body) && !slices.Contains(rm.Parts, contracts.PartBody) {
		out.Text = &t
	}
	return out, nil
}

// previewMeaning is what rm's topics remove from v, asked now: the spans,
// with the summary's sentences or Whole in sum. Only the caller's giving up
// is an error.
func previewMeaning(ctx context.Context, r redact.Redactor, v redact.View, rm *rules.Removal, asker SentenceAsker, account string, sum *contracts.RemovedSummary) ([]redact.Span, error) {
	whole := func() []redact.Span {
		sum.Whole, sum.Why = true, string(HeldUndecided)
		return []redact.Span{v.Subject, v.Body}
	}
	sm := r.Sentences(v)
	if asker == nil || sm.Check(v) != nil || len(sm.Sentences) == 0 {
		return whole(), nil
	}
	asked, err := asker.AskSentences(ctx, &SentenceQuestion{Account: account, Sentences: MaskedSentences(r, v, sm), Topics: slices.Clone(rm.Topics)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return whole(), nil
	}
	if asked == nil || len(asked.Answers)+len(asked.Refused) == 0 {
		return whole(), nil
	}
	kept := KeptSentences(asked, len(sm.Sentences), len(rm.Topics))
	removed, of := 0, len(sm.Sentences)
	for _, k := range kept {
		if !k {
			removed++
		}
	}
	if sm.Cut < len(v.Text) {
		removed, of = removed+1, of+1
	}
	sum.Sentences, sum.Of = removed, of
	for _, pc := range r.Shapes(v.Text, contracts.ShapeKinds) {
		count(sum, pc.Kind)
	}
	return MeaningSpans(r, v, sm, kept), nil
}
