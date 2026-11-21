//go:build modeleval

package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// billedProvider answers every statement of a request 0.9 and reports
// usage, the response's usage member as given ("" for none).
type billedProvider struct {
	usage string
	calls atomic.Int64
}

func (b *billedProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr := httptrace.ContextClientTrace(r.Context()); tr != nil && tr.GotConn != nil {
		tr.GotConn(httptrace.GotConnInfo{})
	}
	b.calls.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var req struct{ Questions []struct{ Name string } }
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	items := make([]string, len(req.Questions))
	for i, q := range req.Questions {
		items[i] = `{"type":"predicate","name":` + strconv.Quote(q.Name) + `,"probability":0.9}`
	}
	return respond(r, 200, nil, `{"model":"`+Model+`","answers":[`+strings.Join(items, ",")+`]`+b.usage+`}`), nil
}

// U34's billing, against a played provider (no key, nothing sent to
// OpenAI): a run whose calls cost more than decide.Estimate, report no
// usage, or read or write cached tokens fails, however good its answers,
// since the estimate is what the service reserves before an attempt and
// the prompt cache is off (the review of pull request 4, finding 1). Every
// statement is answered 0.9, so recall, the hostile pairs and the failed
// calls all pass and only the billing can fail the run.
func TestU34OverEstimateOrCacheUseFailsTheEval(t *testing.T) {
	for _, c := range []struct {
		name  string
		usage string // the response's usage member, or "" for none
		want  string // a failure the run must give, or "" for none
	}{
		{"usage within the estimate", `,"usage":{"input_tokens":900,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, ""},
		{"usage past the estimate", `,"usage":{"input_tokens":100000,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, "cost more than their estimate"},
		{"no usage", "", "reported no usage"},
		{"cached tokens read", `,"usage":{"input_tokens":900,"input_tokens_details":{"cached_tokens":128,"cache_write_tokens":0}}`, "cached tokens"},
		{"cached tokens written", `,"usage":{"input_tokens":900,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":128}}`, "cached tokens"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &billedProvider{usage: c.usage}
			client := New(&http.Client{Transport: b}, key(testKey), "", "")
			report, failures := u34(t, client, u34Budget, "")
			if b.calls.Load() == 0 {
				t.Fatal("nothing asked")
			}
			got := strings.Join(failures, "; ")
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("failures %q, want %q; report %v", got, c.want, report)
			}
		})
	}
}
