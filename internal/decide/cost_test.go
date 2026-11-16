package decide

import (
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/query"
)

// A9: Cost at the compiled prices.
func TestA9CostAtTheCompiledPrices(t *testing.T) {
	for _, c := range []struct {
		name string
		u    Usage
		want int64
	}{
		{"nothing", Usage{}, 0},
		{"a typical call ($0.10 a million)", Usage{InputTokens: 1390, Reported: true}, 139_000},
		{"cached tokens at $0.01", Usage{InputTokens: 2000, CachedTokens: 1000, Reported: true}, 1000*100 + 1000*10},
		{"cache writes at $0.125", Usage{InputTokens: 2000, CacheWriteTokens: 1000, Reported: true}, 1000*100 + 1000*125},
		{"both", Usage{InputTokens: 3000, CachedTokens: 1000, CacheWriteTokens: 1000}, 1000*100 + 1000*10 + 1000*125},
		{"at 272K, once", Usage{InputTokens: LongRequestTokens}, LongRequestTokens * 100},
		{"past 272K, twice", Usage{InputTokens: LongRequestTokens + 1}, (LongRequestTokens + 1) * 200},
		{"past 272K with cached tokens, twice", Usage{InputTokens: 300_000, CachedTokens: 100_000}, (200_000*100 + 100_000*10) * 2},
		{"below zero is zero", Usage{InputTokens: -5, CachedTokens: -1, CacheWriteTokens: -1}, 0},
		{"more cached than input is not negative", Usage{InputTokens: 10, CachedTokens: 20}, 20 * 10},
		{"an absurd count does not overflow", Usage{InputTokens: 1 << 62, CachedTokens: 1 << 62, CacheWriteTokens: 1 << 62}, (1 << 40) * (10 + 125) * 2},
	} {
		if got := Cost(c.u); got != c.want {
			t.Errorf("%s: Cost = %d, want %d", c.name, got, c.want)
		}
	}
}

// A9: the estimate's bound, and the ADR's figure for the largest possible
// request (ADR 0028, "Spend").
func TestA9EstimateIsTheADRsBound(t *testing.T) {
	var atoms []query.Atom
	for i := range MaxAtomsPerMessage {
		atoms = append(atoms, messageAbout(string(rune('a'+i))))
	}
	worst := Call{Content: Content{Kind: ContentMessage, Subject: strings.Repeat("\x01", 2<<10), Body: strings.Repeat("\x01", 256<<10)}, Statements: atoms}
	if n := len(ContentJSON(worst.Content)); n != 1_585_176 {
		t.Fatalf("the largest content is %d bytes, want 1,585,176", n)
	}
	if got := Estimate(worst); got != 319_155_200 { // 0.3191552 USD
		t.Errorf("Estimate(largest) = %d, want 319,155,200", got)
	}
	small := Call{Content: Content{Kind: ContentSubject, Subject: "Hi"}, Statements: []query.Atom{subjectAbout("x")}}
	if got := Estimate(small); got != int64(1000+400+len(`{"subject":"Hi"}`))*100 {
		t.Errorf("Estimate(small) = %d", got)
	}
}

// A9: Estimate is at least the cost the re-measure was charged for every
// call shape: the call with the largest tokens for its bound among each
// shape of the corpus, the adaptive set, the hostile topics, the canary and
// the maximum-size requests (ADR 0028, "The re-measure": reported tokens
// were at most half of the bound). The figures are the re-measure's, read
// from its raw answers.
func TestA9EstimateCoversWhatWasMeasured(t *testing.T) {
	for _, m := range []struct {
		shape        string
		kind         ContentKind
		statements   int
		contentBytes int
		tokens       int64
	}{
		{"max04, every byte a quote", ContentMessage, 2, 528_346, 264_706},
		{"max02, quotes and control characters", ContentMessage, 3, 364_265, 135_857},
		{"max03, Spanish, Chinese and emoji", ContentMessage, 3, 265_813, 76_848},
		{"max01, ASCII", ContentMessage, 3, 265_929, 56_647},
		{"the canary's mkt11, 26 statements", ContentMessage, 26, 196, 5_276},
		{"the adaptive set's ad08, 7 statements", ContentMessage, 7, 135, 1_661},
		{"the corpus's adv11, 5 statements", ContentMessage, 5, 117, 1_244},
		{"a hostile topic alone", ContentMessage, 1, 174, 478},
		{"the canary's inv01 subject, 13 statements", ContentSubject, 13, 58, 2_395},
		{"the adaptive set's ad09 subject, 7 statements", ContentSubject, 7, 22, 1_383},
		{"a hostile subject, 1 statement", ContentSubject, 1, 43, 389},
	} {
		// A content of exactly that many bytes of JSON, and that many statements.
		pad := m.contentBytes - len(`{"subject":""}`)
		c := Content{Kind: m.kind, Subject: strings.Repeat("x", pad)}
		if m.kind == ContentMessage {
			c.Subject, c.Body = "", strings.Repeat("x", pad-len(`,"body":""`))
		}
		if n := len(ContentJSON(c)); n != m.contentBytes {
			t.Fatalf("%s: built %d bytes, want %d", m.shape, n, m.contentBytes)
		}
		call := Call{Content: c, Statements: make([]query.Atom, m.statements)}
		measured := Cost(Usage{InputTokens: m.tokens, Reported: true})
		if est := Estimate(call); est < measured {
			t.Errorf("%s: Estimate %d is under the measured cost %d", m.shape, est, measured)
		}
	}
}

// A9: the maximum-size requests as the enclave would send them: each
// estimate is at least twice the cost the re-measure was charged.
func TestA9EstimateCoversTheLongestRequests(t *testing.T) {
	f, err := os.Open("testdata/maxsize.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	measured := map[string]int64{"max01": 56_647, "max02": 135_857, "max03": 76_848, "max04": 264_706}
	for _, m := range readLines[maxCase](t, data) {
		statements := make([]query.Atom, len(m.Atoms))
		call := Call{Content: Content{Kind: ContentMessage, Subject: m.Subject, Body: m.Body}, Statements: statements}
		est, cost := Estimate(call), Cost(Usage{InputTokens: measured[m.ID]})
		if m.ID == "max04" && est != 106_029_200 { // 0.1060292 USD, ADR 0028 "Spend"
			t.Errorf("Estimate(max04) = %d, want 106,029,200", est)
		}
		if est < 2*cost {
			t.Errorf("%s: Estimate %d is not at least twice the measured %d", m.ID, est, cost)
		}
	}
}
