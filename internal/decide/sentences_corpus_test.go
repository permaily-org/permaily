package decide

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/query"
)

// sentencesCorpusHash pins testdata/sentences.jsonl, U34's synthetic corpus
// (ADR 0040): its labels were fixed, and this hash taken, before any model
// was called.
const sentencesCorpusHash = "6ee041f69b7d2c172b3cd46d6848fa46d01fad52321fc02b834ae4b5dc8fe354"

// TestU34TheCorpusIsPinned checks the eval's corpus is the one measured and
// is what the enclave would send: each case a question PlanSentences asks,
// each sentence within 1,000 runes and the whole within 32 KiB, no piece a
// fixed shape would have taken out (a run of 4 digits, an at sign, a
// scheme), labels for every sentence and topic, and every shape-only
// statement a positive.
func TestU34TheCorpusIsPinned(t *testing.T) {
	data, err := os.ReadFile("testdata/sentences.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != sentencesCorpusHash {
		t.Fatalf("testdata/sentences.jsonl is not the corpus measured: %s", got)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	ids := map[string]bool{}
	for sc.Scan() {
		var c struct {
			ID        string   `json:"id"`
			Lang      string   `json:"lang"`
			Tags      []string `json:"tags"`
			Topics    []string `json:"topics"`
			Sentences []string `json:"sentences"`
			Labels    [][]*int `json:"labels"`
			ShapeOnly [][2]int `json:"shape_only"`
			Base      string   `json:"base"`
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			t.Fatal(err)
		}
		ids[c.ID] = true
		if c.Base != "" && !ids[c.Base] {
			t.Errorf("%s: its base %s is not before it", c.ID, c.Base)
		}
		plan, ok := PlanSentences(c.Sentences, mentionsAll(c.Topics))
		if !ok || len(plan) == 0 {
			t.Errorf("%s: not a question the enclave asks", c.ID)
		}
		size := 0
		for i, s := range c.Sentences {
			size += len(s) + 1
			if utf8.RuneCountInString(s) > 1000 || strings.TrimSpace(s) == "" {
				t.Errorf("%s/%d: not a sentence the split gives", c.ID, i)
			}
			if strings.ContainsAny(s, "@＠") || strings.Contains(s, "://") || digitRun(s) >= 4 {
				t.Errorf("%s/%d: holds what a fixed shape takes out: %q", c.ID, i, s)
			}
			if len(c.Labels[i]) != len(c.Topics) {
				t.Errorf("%s/%d: %d labels for %d topics", c.ID, i, len(c.Labels[i]), len(c.Topics))
			}
		}
		if size > 32<<10 || len(c.Labels) != len(c.Sentences) {
			t.Errorf("%s: %d bytes, %d labels", c.ID, size, len(c.Labels))
		}
		for _, so := range c.ShapeOnly {
			if l := c.Labels[so[0]][so[1]]; l == nil || *l != 1 || !strings.Contains(c.Sentences[so[0]], "[removed]") {
				t.Errorf("%s: shape-only %v is not a masked positive", c.ID, so)
			}
		}
	}
	if len(ids) < 50 {
		t.Fatalf("%d cases", len(ids))
	}
}

func mentionsAll(topics []string) []query.Atom {
	out := make([]query.Atom, len(topics))
	for i, tp := range topics {
		out[i] = messageMentions(tp)
	}
	return out
}

// digitRun is the longest run of digits in s, with at most two separators
// between two of them, as a fixed shape's digit group counts them.
func digitRun(s string) int {
	best, run, gap := 0, 0, 0
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			run, gap = run+1, 0
			best = max(best, run)
		case gap < 2 && run > 0 && (unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Pd, r) || strings.ContainsRune("-.,/()", r)):
			gap++
		default:
			run, gap = 0, 0
		}
	}
	return best
}
