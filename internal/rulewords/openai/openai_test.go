package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/rulewords"
)

const key = "sk-test-words-0123456789"

func keyed(context.Context) (string, bool) { return key, true }

func attempt(t *testing.T, err error) *decide.AttemptError {
	t.Helper()
	var ae *decide.AttemptError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an AttemptError", err)
	}
	return ae
}

// TestU11TheWordsRequestOnTheWire: one POST to the compiled address over
// HTTP/2, its body rulewords.Request of the words byte for byte (the pinned
// developer message and schema, store off, no tools, max_output_tokens,
// strict), the key, the pins, and no User-Agent.
func TestU11TheWordsRequestOnTheWire(t *testing.T) {
	w := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply {
		return decidetest.WriterReply{InputTokens: 1200, CachedTokens: 200, OutputTokens: 300}
	})
	c := New(w.Client(), keyed, "org-1", "proj_1")
	words := "Hide mail from bank.example \"now\" <b>ñ"
	a, err := c.Write(context.Background(), words)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != decidetest.HoldText || a.Model != rulewords.ModelName || a.Refused || a.Incomplete ||
		a.Usage != (rulewords.Usage{InputTokens: 1200, CachedTokens: 200, OutputTokens: 300, Reported: true}) {
		t.Errorf("answer = %+v", a)
	}
	reqs := w.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Host != "api.openai.com" || r.Path != "/v1/responses" || r.Proto != "HTTP/2.0" {
		t.Errorf("request %s %s%s %s", r.Method, r.Host, r.Path, r.Proto)
	}
	if !bytes.Equal(r.Body, rulewords.Request(words)) {
		t.Errorf("body is not rulewords.Request(words):\n%s", r.Body)
	}
	if got, ok := decidetest.Words(r.Body); !ok || got != words {
		t.Error("the played writer does not read back the words")
	}
	h := r.Header
	if h.Get("Authorization") != "Bearer "+key || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "" ||
		h.Get("OpenAI-Organization") != "org-1" || h.Get("OpenAI-Project") != "proj_1" || h.Get("Cookie") != "" {
		t.Errorf("headers %v", h)
	}
	if c.Name() != rulewords.ModelName || len(Hosts()) != 1 || Hosts()[0] != "api.openai.com:443" {
		t.Error("Name and Hosts")
	}
}

// TestU13NothingIsSentWithoutAKeyOrWords: no key, a key that cannot be
// sent, words that may not be sent, or a context already done: nothing
// reaches the provider, and the attempt is not charged.
func TestU13NothingIsSentWithoutAKeyOrWords(t *testing.T) {
	w := decidetest.NewWriter(t, false, nil)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		key   decide.Credential
		words string
		ctx   context.Context
		want  error
	}{
		"no key":       {func(context.Context) (string, bool) { return "", false }, "hide x", context.Background(), decide.ErrKeyRefused},
		"nil key":      {nil, "hide x", context.Background(), decide.ErrKeyRefused},
		"a bad key":    {func(context.Context) (string, bool) { return "sk bad\n", true }, "hide x", context.Background(), decide.ErrKeyRefused},
		"no words":     {keyed, " ", context.Background(), decide.ErrRequest},
		"too many":     {keyed, strings.Repeat("w", 1001), context.Background(), decide.ErrRequest},
		"context done": {keyed, "hide x", done, decide.ErrUnavailable},
	} {
		_, err := New(w.Client(), c.key, "", "").Write(c.ctx, c.words)
		ae := attempt(t, err)
		if !errors.Is(ae, c.want) || ae.Sent {
			t.Errorf("%s: %v, sent %v", name, ae, ae.Sent)
		}
	}
	if n := len(w.Requests()); n != 0 {
		t.Errorf("%d requests reached the provider", n)
	}
}

// TestU13TheProvidersFailures: each failure is the AttemptError the service
// reads, sent, never followed or tried again by the client.
func TestU13TheProvidersFailures(t *testing.T) {
	for name, c := range map[string]struct {
		reply decidetest.WriterReply
		want  error
	}{
		"a refused key":        {decidetest.WriterReply{Status: 401, Code: "invalid_api_key"}, decide.ErrKeyRefused},
		"a forbidden key":      {decidetest.WriterReply{Status: 403, Code: "forbidden"}, decide.ErrKeyRefused},
		"the provider's limit": {decidetest.WriterReply{Status: 429, Code: "insufficient_quota"}, decide.ErrProviderLimit},
		"a spend limit":        {decidetest.WriterReply{Status: 429, Code: "project_spend_limit_exceeded"}, decide.ErrProviderLimit},
		"slow down":            {decidetest.WriterReply{Status: 429, Code: "rate_limit_exceeded"}, decide.ErrUnavailable},
		"a 500":                {decidetest.WriterReply{Status: 500, Code: "server_error"}, decide.ErrUnavailable},
		"a 501":                {decidetest.WriterReply{Status: 501, Code: "x"}, decide.ErrUnavailable},
		"a 400":                {decidetest.WriterReply{Status: 400, Code: "invalid_request"}, decide.ErrRequest},
		"a redirect":           {decidetest.WriterReply{Redirect: true}, decide.ErrRequest},
		"a 204":                {decidetest.WriterReply{Status: 204}, decide.ErrInvalidAnswer},
		"another model":        {decidetest.WriterReply{Model: "gpt-6-sol"}, decide.ErrModelChanged},
		"not JSON":             {decidetest.WriterReply{Body: []byte("not json")}, decide.ErrInvalidAnswer},
		"no model":             {decidetest.WriterReply{Body: []byte(`{"status":"completed","output":[]}`)}, decide.ErrInvalidAnswer},
		"failed":               {decidetest.WriterReply{Body: []byte(`{"model":"gpt-6-luna","status":"failed","output":[]}`)}, decide.ErrInvalidAnswer},
		"no text":              {decidetest.WriterReply{Body: []byte(`{"model":"gpt-6-luna","status":"completed","output":[]}`)}, decide.ErrInvalidAnswer},
		"two texts": {decidetest.WriterReply{Body: []byte(`{"model":"gpt-6-luna","status":"completed","output":[{"type":"message","role":"assistant","content":[` +
			`{"type":"output_text","text":"{}"},{"type":"output_text","text":"{}"}]}]}`)}, decide.ErrInvalidAnswer},
		"another role": {decidetest.WriterReply{Body: []byte(`{"model":"gpt-6-luna","status":"completed","output":[{"type":"message","role":"user","content":[` +
			`{"type":"output_text","text":"{}"}]}]}`)}, decide.ErrInvalidAnswer},
		"too long": {decidetest.WriterReply{Body: bytes.Repeat([]byte(" "), MaxAnswer+1)}, decide.ErrInvalidAnswer},
	} {
		w := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return c.reply })
		_, err := New(w.Client(), keyed, "", "").Write(context.Background(), "hide x")
		ae := attempt(t, err)
		if !errors.Is(ae, c.want) || !ae.Sent {
			t.Errorf("%s: %v, sent %v; want %v, sent", name, ae, ae.Sent, c.want)
		}
		if n := len(w.Requests()); n != 1 {
			t.Errorf("%s: %d requests, want 1", name, n)
		}
	}
}

// TestU13TimeoutsAndProtocols: an answer later than the attempt's wait, and
// a provider that speaks only HTTP/1.1, are ErrUnavailable.
func TestU13TimeoutsAndProtocols(t *testing.T) {
	w := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return decidetest.WriterReply{Delay: time.Second} })
	c := New(w.Client(), keyed, "", "")
	c.timeout = 50 * time.Millisecond
	_, err := c.Write(context.Background(), "hide x")
	if ae := attempt(t, err); !errors.Is(ae, decide.ErrUnavailable) || !ae.Sent {
		t.Errorf("a late answer: %v, sent %v", ae, ae.Sent)
	}
	h1 := decidetest.NewWriter(t, true, nil)
	_, err = New(h1.Client(), keyed, "", "").Write(context.Background(), "hide x")
	if ae := attempt(t, err); !errors.Is(ae, decide.ErrUnavailable) {
		t.Errorf("HTTP/1.1 alone: %v", ae)
	}
	if n := len(h1.Requests()); n != 0 {
		t.Errorf("HTTP/1.1 alone: %d requests", n)
	}
}

// TestU12RefusalsAndCutsAreAnswersNotErrors: a refusal and an answer cut at
// max_output_tokens come back as such, with their usage, for the service
// to settle and to answer rule_words_unusable; an answer that reports no
// usage is not Reported.
func TestU12RefusalsAndCutsAreAnswersNotErrors(t *testing.T) {
	for name, c := range map[string]struct {
		reply decidetest.WriterReply
		check func(*rulewords.Answer) bool
	}{
		"refused":  {decidetest.WriterReply{Refusal: true, InputTokens: 10, OutputTokens: 5}, func(a *rulewords.Answer) bool { return a.Refused && a.Text == "" && a.Usage.Reported }},
		"cut":      {decidetest.WriterReply{Incomplete: true, InputTokens: 10, OutputTokens: 2000}, func(a *rulewords.Answer) bool { return a.Incomplete && a.Usage.OutputTokens == 2000 }},
		"no usage": {decidetest.WriterReply{NoUsage: true}, func(a *rulewords.Answer) bool { return !a.Usage.Reported && a.Text == decidetest.HoldText }},
		"a usage past its bound": {decidetest.WriterReply{InputTokens: 1 << 41, OutputTokens: 1}, func(a *rulewords.Answer) bool {
			return !a.Usage.Reported
		}},
	} {
		w := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return c.reply })
		a, err := New(w.Client(), keyed, "", "").Write(context.Background(), "hide x")
		if err != nil || !c.check(a) {
			t.Errorf("%s: %+v, %v", name, a, err)
		}
	}
}
