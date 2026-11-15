package decide

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The spike's synthetic corpus (ADR 0028, "The spike"): no real mail. Its
// labels were fixed, and these hashes taken, before any model was called
// (2026-11-15, 19:29:54 UTC for the corpus, 19:33:56 UTC for the adaptive
// set, 20:38:55 UTC for the re-measure's hostile topics and maximum-size
// requests, ADR 0028 "The re-measure"). The eval of pull request 1 and the
// canary read it from here.

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
		`{"v":1,"prompt_version":"p2-2026-11-15","tolerance":0.02,"messages":[]}`,
		`{"v":1,"prompt_version":"p2-2026-11-15","tolerance":0.02,"messages":[{"atoms":[]}]}`,
		`{"v":1,"prompt_version":"p2-2026-11-15","tolerance":0.02,"messages":[{"atoms":[{"var":"body","op":"contains","topic":"x","p":1}]}]}`,
		`{"v":1,"prompt_version":"p2-2026-11-15","tolerance":0.02,"messages":[{"atoms":[{"var":"message","op":"about","topic":"x","p":1.5}]}]}`,
		`{"v":1,"prompt_version":"p2-2026-11-15","tolerance":0.9,"messages":[{"atoms":[{"var":"message","op":"about","topic":"x","p":1}]}]}`,
	} {
		canaryJSON = []byte(bad)
		if _, err := LoadCanary(); err == nil {
			t.Errorf("LoadCanary accepted %s", bad)
		}
	}
}

// hostileCase is one call of the re-measure's hostile topics
// (testdata/hostile.jsonl, ADR 0028 "The re-measure"): target atoms with the
// corpus's labels and hostile atoms whose topic carries an instruction, put
// in one call on a corpus message.
type hostileCase struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Mode    string `json:"mode"`
	Attack  string `json:"attack"`
	Atoms   []struct {
		Role  string `json:"role"`
		Kind  string `json:"kind"`
		Core  string `json:"core"`
		Topic string `json:"topic"`
		Label *bool  `json:"label"`
	} `json:"atoms"`
}

// maxCase is one of the re-measure's maximum-size requests
// (testdata/maxsize.jsonl.gz): a subject of 2 KiB and a text of 256 KiB,
// with the topic in the last line only.
type maxCase struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Subject string  `json:"subject"`
	Body    string  `json:"body"`
	Atoms   [][]any `json:"atoms"`
}

func readLines[T any](t *testing.T, data []byte) []T {
	t.Helper()
	var out []T
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<21), 1<<21)
	for sc.Scan() {
		var v T
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("line %d: %v", len(out)+1, err)
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestTheHostileTopicsAreTheReMeasures pins the hostile-topic cases by the
// hash taken before any call, and checks that every topic is one the query
// language could hold (lower case, 1 to 100 characters) and every label the
// corpus's.
func TestTheHostileTopicsAreTheReMeasures(t *testing.T) {
	data, err := os.ReadFile("testdata/hostile.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if sha(data) != "c6268cecae984aa5560c4224e199d6359dbc51193cc4ceac82887e5179c02ff0" {
		t.Fatal("hostile.jsonl is not the file whose labels were fixed before the re-measure")
	}
	byID := map[string]corpusMessage{}
	for _, m := range readCorpus(t, "corpus.jsonl") {
		byID[m.ID] = m
	}
	cases := readLines[hostileCase](t, data)
	targets, hostile := 0, 0
	attacks := map[string]int{}
	for _, c := range cases {
		m, ok := byID[c.Message]
		if !ok {
			t.Fatalf("%s: message %s is not the corpus's", c.ID, c.Message)
		}
		attacks[c.Attack]++
		for _, a := range c.Atoms {
			if n := len([]rune(a.Topic)); n == 0 || n > 100 || a.Topic != string(bytes.ToLower([]byte(a.Topic))) {
				t.Errorf("%s: topic %q is not one the query language holds", c.ID, a.Topic)
			}
			switch a.Role {
			case "target":
				targets++
				if want := m.Labels[a.Core][a.Kind]; want == nil || a.Label == nil || *want != *a.Label {
					t.Errorf("%s: a target's label is not the corpus's", c.ID)
				}
			case "hostile":
				hostile++
			default:
				t.Errorf("%s: role %q", c.ID, a.Role)
			}
		}
	}
	if len(cases) != 24 || targets != 30 || hostile != 24 || attacks["cross"] != 14 || attacks["scope"] != 4 || attacks["negation"] != 3 || attacks["self"] != 3 {
		t.Errorf("hostile: %d cases, %d targets, %d hostile atoms, attacks %v; the ADR says 24, 30, 24 and 14 cross, 4 scope, 3 negation, 3 self", len(cases), targets, hostile, attacks)
	}
}

// TestTheLongestRequestIsWhatWasMeasured pins the maximum-size requests by
// the hash taken before any call, and checks that ContentJSON gives them the
// bytes the re-measure sent (their SHA-256, from its client), so the 256 KiB
// text it answered is the one the enclave would send.
func TestTheLongestRequestIsWhatWasMeasured(t *testing.T) {
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
	if sha(data) != "c7c43b3ee26888553205c49fd0edabe9124860d5c5ed3558c9306714993b0fb7" {
		t.Fatal("maxsize.jsonl is not the file whose labels were fixed before the re-measure")
	}
	want := map[string]struct {
		size int
		hash string
	}{
		"max01": {265929, "8098b7905814fdad59943ac9cee9f1a9f29984439cdcfab145c1312cfd8e612b"},
		"max02": {364265, "d64b1fb497e40e220d1a4f6929b3eb0cbf4e9dfc5690e1d634b51f2a10876b15"},
		"max03": {265813, "929ae47cd35b3173c01c2497e1d93c44d3c094a41c9a1dd96f1dd70166ff2761"},
		"max04": {528346, "30d6bdc876e2554fcdda641f2a24639e7df7f21d2ea008c3bd464677c902344e"},
	}
	cases := readLines[maxCase](t, data)
	if len(cases) != len(want) {
		t.Fatalf("%d cases", len(cases))
	}
	for _, c := range cases {
		if len(c.Subject) != 2<<10 || len(c.Body) != 256<<10 {
			t.Errorf("%s: subject %d and text %d bytes, want 2 KiB and 256 KiB", c.ID, len(c.Subject), len(c.Body))
		}
		got := ContentJSON(Content{Kind: ContentMessage, Subject: c.Subject, Body: c.Body})
		if w := want[c.ID]; len(got) != w.size || sha(got) != w.hash {
			t.Errorf("%s: ContentJSON is %d bytes, sha256 %s; the re-measure sent %d, %s", c.ID, len(got), sha(got), w.size, w.hash)
		}
	}
}

// TestTheCanaryMovesAtABoundary: an answer has moved when it is further
// than the tolerance from what is expected, or when it classifies otherwise
// in either mode however small the move (ADR 0028, "Changed after the design
// review", 14: 0.28 to 0.30 is within 0.02 and changes what a rule does).
func TestTheCanaryMovesAtABoundary(t *testing.T) {
	at := func(p float64) CanaryAtom {
		return CanaryAtom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "bank account activity", P: p}
	}
	for _, c := range []struct {
		expected, now float64
		moved         bool
	}{
		{0.28, 0.28, false},
		{0.28, 0.29, false},
		{0.28, 0.30, true}, // a rule: False to Unknown
		{0.29, 0.30, true},
		{0.30, 0.29, true},
		{0.20, 0.21, true}, // a query: False to Unknown
		{0.79, 0.80, true}, // a query: Unknown to True
		{0.69, 0.70, true}, // a rule: Unknown to True
		{0.50, 0.51, false},
		{0.50, 0.53, true}, // past the tolerance
		{0.97, 1.00, true},
		{0.99, 1.00, false},
		{0.50, math.NaN(), true},
		{0.50, -0.01, true},
		{0.50, 1.01, true},
	} {
		if got := at(c.expected).Moved(c.now, 0.02); got != c.moved {
			t.Errorf("expected %v, answered %v: moved %v, want %v", c.expected, c.now, got, c.moved)
		}
	}
	c, err := LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	if c.Baseline != sha(canaryJSON) || len(c.Baseline) != 64 {
		t.Errorf("Baseline = %q, want the hash of canary.json", c.Baseline)
	}
	for _, m := range c.Messages {
		for _, a := range m.Atoms {
			if a.Moved(a.P, c.Tolerance) {
				t.Errorf("%s %v: the expected answer moved from itself", m.ID, a.Atom())
			}
		}
	}
}
