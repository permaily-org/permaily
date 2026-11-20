package inbox

import "testing"

// Review of #199, finding 2: calls made at once for one page (WithMatchShare)
// together read, read raw and ask no more than the page, and each keeps
// enough to consider a message.
func TestMatchShareDividesAPage(t *testing.T) {
	if got := defaultMatchBounds.share(1); got != defaultMatchBounds {
		t.Fatalf("a share of one changed the page's bounds: %+v", got)
	}
	for of := 2; of <= MatchMaxTextReads; of++ {
		s := defaultMatchBounds.share(of)
		if !s.shared || s.read < 2 || s.textReads < 1 || s.asked < 1 {
			t.Fatalf("share of %d leaves too little to consider a message: %+v", of, s)
		}
		if of*s.read > MatchMaxRead || of*s.textReads > MatchMaxTextReads || of*s.asked > MatchMaxModelMessages {
			t.Fatalf("%d shares of %+v are more than a page", of, s)
		}
		if s.soft != defaultMatchBounds.soft || s.undecided != defaultMatchBounds.undecided || s.ask != defaultMatchBounds.ask {
			t.Fatalf("a share changed what is not divided: %+v", s)
		}
	}
}
