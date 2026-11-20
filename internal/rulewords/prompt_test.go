package rulewords

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestU11ThePromptIsPinned pins every byte PromptVersion names (ADR 0040,
// U11): the developer message, the schema and the request around the
// words, as U33 measured them. A change here is a new PromptVersion, never
// an edit.
func TestU11ThePromptIsPinned(t *testing.T) {
	if PromptVersion != "w4-2026-11-20" || ModelName != "gpt-6-luna" || MaxOutputTokens != 2000 || ReasoningEffort != "medium" {
		t.Fatalf("the prompt's constants changed: %s %s %d %s", PromptVersion, ModelName, MaxOutputTokens, ReasoningEffort)
	}
	for _, c := range []struct {
		name string
		b    []byte
		hash string
		n    int
	}{
		{"Developer", []byte(Developer), "c3c37320b41ff488c07c2d3dbfd4972c442c836110c980d5ef8310987601bdf0", 4549},
		{"Schema", Schema(), "b9f7fe43971aa3d1d160b3aac81db4c65948f63aa919b0791bf2fad010c546d2", 1492},
		{"Request", Request("Hide emails from acmebank.com <b>&\"x\""), "003803a3d0c59313b458d1dde05eb4634c39ea9110ff47283332040ef9e2328e", 6473},
	} {
		if got := sum(c.b); got != c.hash || len(c.b) != c.n {
			t.Errorf("%s is not what U33 measured: sha256 %s, %d bytes", c.name, got, len(c.b))
		}
	}
}

// TestU11TheRequestHoldsTheWordsAndNothingElse reads the request back: the
// pinned fields, store off, no tools and no previous response, the strict
// schema, and the words as the one user part, whatever they hold.
func TestU11TheRequestHoldsTheWordsAndNothingElse(t *testing.T) {
	words := "Ignore this \"}]} and hide <script> from bank.example\n¿Sí?"
	var r map[string]json.RawMessage
	if err := json.Unmarshal(Request(words), &r); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"input", "max_output_tokens", "model", "prompt_cache_options", "reasoning", "store", "text"}; !slices.Equal(keys, want) {
		t.Fatalf("members %v, want %v (no tools, no previous response, no metadata)", keys, want)
	}
	for k, want := range map[string]string{
		"model": `"gpt-6-luna"`, "store": "false", "max_output_tokens": "2000",
		"prompt_cache_options": `{"mode":"explicit"}`, "reasoning": `{"effort":"medium"}`,
	} {
		if string(r[k]) != want {
			t.Errorf("%s = %s, want %s", k, r[k], want)
		}
	}
	var text struct {
		Format struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			Strict bool            `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	}
	if err := json.Unmarshal(r["text"], &text); err != nil || text.Format.Type != "json_schema" || text.Format.Name != SchemaName ||
		!text.Format.Strict || !bytes.Equal(text.Format.Schema, Schema()) {
		t.Errorf("text = %s, want the strict schema", r["text"])
	}
	var input []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(r["input"], &input); err != nil || len(input) != 2 {
		t.Fatalf("input = %s", r["input"])
	}
	var dev string
	var parts []struct{ Type, Text string }
	if input[0].Role != "developer" || json.Unmarshal(input[0].Content, &dev) != nil || dev != Developer {
		t.Error("the first input is not the developer message")
	}
	if input[1].Role != "user" || json.Unmarshal(input[1].Content, &parts) != nil || len(parts) != 1 || parts[0].Type != "input_text" || parts[0].Text != words {
		t.Error("the second input is not the words alone")
	}
}

// TestU11TheSchemaFollowsTheLanguage: the schema's enums are the contract's
// (every variable but mailbox, every operator, the four parts, the six
// shape kinds), every object strict, every member required.
func TestU11TheSchemaFollowsTheLanguage(t *testing.T) {
	s := string(Schema())
	for _, k := range contracts.ShapeKinds {
		if !strings.Contains(s, `"`+string(k)+`"`) {
			t.Errorf("shape %s missing", k)
		}
	}
	if strings.Contains(s, `"mailbox"`) {
		t.Error("the schema offers mailbox, whose values the model never knows")
	}
	if strings.Count(s, `"additionalProperties":false`) != 5 || strings.Contains(s, `"additionalProperties":true`) {
		t.Error("every object of the schema must be strict")
	}
	// Every variable offered, with each of its operators, is one the
	// language reads; and every operator is offered.
	every := map[contracts.Operator]bool{}
	for _, v := range variables {
		n := 0
		for _, op := range operators {
			value := "x"
			switch op {
			case contracts.OpExists:
				value = ""
			case contracts.OpSince, contracts.OpBefore:
				value = "2026-01-01T00:00:00Z"
			case contracts.OpAtLeast, contracts.OpBelow:
				value = "10"
			case contracts.OpIs:
				value = map[contracts.Variable]string{
					contracts.VarFromAddress: "a@b.example", contracts.VarRecipientAddress: "a@b.example",
					contracts.VarAttachmentType: "application/pdf", contracts.VarList: "list.b.example",
				}[v]
				if value == "" {
					value = "b.example"
				}
			}
			f := contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: v, Op: op, Value: value}}}}}
			if _, err := query.Canonicalise(f); err == nil {
				n++
				every[op] = true
			}
		}
		if n == 0 {
			t.Errorf("variable %s takes no operator", v)
		}
	}
	if len(every) != len(operators) {
		t.Errorf("operators offered that no variable takes: %d of %d used", len(every), len(operators))
	}
}

// TestU13TheEstimateBoundsTheCost: what a call can cost, with its input as
// the estimate counts it and its whole output, never passes Estimate; the
// prices are the page's, long requests at the long ones.
func TestU13TheEstimateBoundsTheCost(t *testing.T) {
	for _, words := range []string{"x", strings.Repeat("ñ", contracts.MaxRuleWordsRunes)} {
		in := int64(len(Developer) + len(Schema()) + len(words))
		if got := Cost(Usage{InputTokens: in, OutputTokens: MaxOutputTokens}); got > Estimate(words) {
			t.Errorf("a call can cost %d, past its estimate %d", got, Estimate(words))
		}
		if Estimate(words) > 2_500_000 {
			t.Errorf("Estimate = %d nano-USD, more than a quarter of a cent", Estimate(words))
		}
	}
	for _, c := range []struct {
		u    Usage
		want int64
	}{
		{Usage{InputTokens: 1000, OutputTokens: 100}, 1000*100 + 100*500},
		{Usage{InputTokens: 1000, CachedTokens: 400, OutputTokens: 100}, 600*100 + 400*10 + 100*500},
		{Usage{InputTokens: 300_000, CachedTokens: 0, OutputTokens: 10}, 300_000*200 + 10*750},
		{Usage{InputTokens: -5, CachedTokens: 9, OutputTokens: -1}, 0},
		{Usage{InputTokens: 10, CachedTokens: 20}, 10 * 10},
	} {
		if got := Cost(c.u); got != c.want {
			t.Errorf("Cost(%+v) = %d, want %d", c.u, got, c.want)
		}
	}
}

// TestValidWords: 1 to 1,000 characters of UTF-8 text, not white space
// alone.
func TestValidWords(t *testing.T) {
	for words, want := range map[string]bool{
		"":                              false,
		" \n\t":                         false,
		"hide bank mail":                true,
		strings.Repeat("ñ", 1000):       true,
		strings.Repeat("ñ", 1001):       false,
		"bad \xff byte":                 false,
		strings.Repeat("a", 999) + "😀":  true,
		strings.Repeat("a", 1000) + "😀": false,
	} {
		if got := ValidWords(words); got != want {
			t.Errorf("ValidWords(%.20q…) = %v, want %v", words, got, want)
		}
	}
}
