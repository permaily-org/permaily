package decide

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The spike's synthetic corpus (ADR 0028, "The spike"): no real mail. Its
// labels were fixed, and these hashes taken, before any model was called
// (2026-11-15, 19:29:54 UTC for the corpus, 19:33:56 UTC for the adaptive
// set). The eval of pull request 1 and the canary read it from here.

type corpusMessage struct {
	ID       string                      `json:"id"`
	Lang     string                      `json:"lang"`
	Category string                      `json:"category"`
	Tags     []string                    `json:"tags"`
	Subject  string                      `json:"subject"`
	Body     string                      `json:"body"`
	Labels   map[string]map[string]*bool `json:"labels"`
}

func readCorpus(t *testing.T, name string) []corpusMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var out []corpusMessage
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m corpusMessage
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("%s line %d: %v", name, len(out)+1, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCorpusIsTheSpikes(t *testing.T) {
	for name, want := range map[string]string{
		"corpus.jsonl":   "4cd1dd5535c02261a77ce283a7d6016fa0fa78166f3f941606651a7004951f02",
		"adaptive.jsonl": "de85b043bb24ed0d52222a635cf5f1e65358d876923921d4faff9e636fa8412c",
	} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Errorf("%s is not the file whose labels the spike fixed before its runs", name)
		}
	}
	var topics map[string]string
	data, err := os.ReadFile("testdata/topics.json")
	if err != nil || json.Unmarshal(data, &topics) != nil || len(topics) != 13 {
		t.Fatalf("topics.json: 13 topics, got %d (%v)", len(topics), err)
	}
	kinds := []string{"subject_about", "message_about", "message_mentions"}
	count := func(ms []corpusMessage) (atoms, positive, excluded int) {
		for _, m := range ms {
			for topic, labels := range m.Labels {
				if _, ok := topics[topic]; !ok {
					t.Fatalf("%s: unknown topic %q", m.ID, topic)
				}
				for _, k := range kinds {
					v, ok := labels[k]
					if !ok {
						t.Fatalf("%s %s: no label for %s", m.ID, topic, k)
					}
					atoms++
					switch {
					case v == nil:
						excluded++
					case *v:
						positive++
					}
				}
			}
		}
		return
	}
	corpus := readCorpus(t, "corpus.jsonl")
	if atoms, positive, excluded := count(corpus); len(corpus) != 198 || atoms != 7722 || excluded != 223 || positive != 473 {
		t.Errorf("corpus: %d messages, %d atoms, %d excluded, %d positive; the ADR says 198, 7,722, 223 and 473", len(corpus), atoms, excluded, positive)
	}
	if adaptive := readCorpus(t, "adaptive.jsonl"); len(adaptive) != 11 {
		t.Errorf("adaptive: %d messages, want 11", len(adaptive))
	}
}

// TestTheCanaryIsTheCorpusAsMeasured checks the canary against the corpus:
// its messages are the corpus's, word for word, its topics the corpus's,
// and every message holds every atom of every topic, as the spike asked.
func TestTheCanaryIsTheCorpusAsMeasured(t *testing.T) {
	c, err := LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "gpt-6-luna" || c.Tolerance != 0.02 || len(c.Messages) != 4 {
		t.Fatalf("canary: model %q, tolerance %v, %d messages", c.Model, c.Tolerance, len(c.Messages))
	}
	var topics map[string]string
	data, _ := os.ReadFile("testdata/topics.json")
	_ = json.Unmarshal(data, &topics)
	names := map[string]bool{}
	for _, name := range topics {
		names[name] = true
	}
	byID := map[string]corpusMessage{}
	for _, m := range readCorpus(t, "corpus.jsonl") {
		byID[m.ID] = m
	}
	for _, m := range c.Messages {
		src, ok := byID[m.ID]
		if !ok || src.Subject != m.Subject || src.Body != m.Body {
			t.Errorf("canary %s is not the corpus's message", m.ID)
		}
		if len(m.Atoms) != 3*len(topics) {
			t.Errorf("canary %s: %d atoms, want %d", m.ID, len(m.Atoms), 3*len(topics))
		}
		seen := map[CanaryAtom]bool{}
		for _, a := range m.Atoms {
			if !names[a.Topic] {
				t.Errorf("canary %s: topic %q is not the corpus's", m.ID, a.Topic)
			}
			key := CanaryAtom{Var: a.Var, Op: a.Op, Topic: a.Topic}
			if seen[key] {
				t.Errorf("canary %s: %v twice", m.ID, key)
			}
			seen[key] = true
		}
	}
}

func TestLoadCanaryRefusesAnotherPrompt(t *testing.T) {
	saved := canaryJSON
	t.Cleanup(func() { canaryJSON = saved })
	for _, bad := range []string{
		`not json`,
		`{"v":1,"prompt_version":"p0","tolerance":0.02,"messages":[{"atoms":[{"var":"message","op":"about","topic":"x","p":1}]}]}`,
		`{"v":1,"prompt_version":"p1-2026-11-15","tolerance":0.02,"messages":[]}`,
		`{"v":1,"prompt_version":"p1-2026-11-15","tolerance":0.02,"messages":[{"atoms":[]}]}`,
		`{"v":1,"prompt_version":"p1-2026-11-15","tolerance":0.02,"messages":[{"atoms":[{"var":"body","op":"contains","topic":"x","p":1}]}]}`,
		`{"v":1,"prompt_version":"p1-2026-11-15","tolerance":0.02,"messages":[{"atoms":[{"var":"message","op":"about","topic":"x","p":1.5}]}]}`,
		`{"v":1,"prompt_version":"p1-2026-11-15","tolerance":0.9,"messages":[{"atoms":[{"var":"message","op":"about","topic":"x","p":1}]}]}`,
	} {
		canaryJSON = []byte(bad)
		if _, err := LoadCanary(); err == nil {
			t.Errorf("LoadCanary accepted %s", bad)
		}
	}
}
