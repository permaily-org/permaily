package inbox_test

import (
	"math"
	"testing"

	"github.com/permaily-org/permaily/internal/inbox"
)

// U17's rule of what a removal by meaning keeps: only a sentence the model
// answered below 0.30 for every topic; a refusal, an answer in the band of
// doubt, one at 0.70 or above, no answer at all, a probability that is not
// one, and nothing asked, each remove it.
func TestU17ASentenceIsKeptOnlyOnLowAnswersForEveryTopic(t *testing.T) {
	asked := &inbox.SentencesAsked{
		Answers: map[inbox.SentenceTopic]float64{
			{0, 0}: 0.01, {0, 1}: 0.29, // kept
			{1, 0}: 0.01, {1, 1}: 0.30, // the band's edge: removed
			{2, 0}: 0.95, {2, 1}: 0.01, // above: removed
			{3, 0}: 0.01,               // the second topic unanswered: removed
			{4, 0}: 0.01, {4, 1}: 0.01, // refused below: removed
			{5, 0}: math.NaN(), {5, 1}: 0.01, // not a probability: removed
			{6, 0}: -0.1, {6, 1}: 0.01, // outside 0 to 1: removed
		},
		Refused: map[inbox.SentenceTopic]bool{{4, 1}: true},
	}
	want := []bool{true, false, false, false, false, false, false, false}
	got := inbox.KeptSentences(asked, 8, 2)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d kept %v, want %v", i, got[i], want[i])
		}
	}
	for i, k := range inbox.KeptSentences(nil, 3, 1) {
		if k {
			t.Errorf("nothing asked kept sentence %d", i)
		}
	}
	for i, k := range inbox.KeptSentences(&inbox.SentencesAsked{Answers: map[inbox.SentenceTopic]float64{{0, 0}: 0}}, 1, 0) {
		if k {
			t.Errorf("a removal with no topic kept sentence %d", i)
		}
	}
}
