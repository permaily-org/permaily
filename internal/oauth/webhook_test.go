package oauth

import (
	"strings"
	"testing"
)

// K23 (ADR 0039), this package's half: a pushed request's "webhook" is kept
// as sent, bounded, once; the address bar's is ignored. What it says is
// read by webhook.ParseRequest in the connections service, which knows the
// client's host.
func TestK23APushedRequestKeepsItsWebhookAndTheAddressDoesNot(t *testing.T) {
	hook := `{"url":"https://claude.ai/hooks/permaily","new_mail":true}`
	p, err := Pushed(with(pushed(), set("webhook", hook)), claudeAI(), config)
	if err != nil || p.Webhook != hook {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := Pushed(pushed(), claudeAI(), config); err != nil || p.Webhook != "" {
		t.Fatalf("a request without it has a webhook: %+v %v", p, err)
	}
	for name, change := range map[string]func(q map[string][]string){
		"empty":     func(q map[string][]string) { q["webhook"] = []string{""} },
		"too large": func(q map[string][]string) { q["webhook"] = []string{strings.Repeat("a", WebhookMaxBytes+1)} },
		"twice":     func(q map[string][]string) { q["webhook"] = []string{hook, hook} },
	} {
		q := pushed()
		change(q)
		_, err := Pushed(q, claudeAI(), config)
		code(t, err, ErrInvalidRequest, string(ErrInvalidRequest))
		if err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	// In the address bar it is ignored, as query is.
	p, _, err = Authorize(with(request(), set("webhook", hook)), claudeCode(), config)
	if err != nil || p.Webhook != "" {
		t.Fatalf("taken from the address: %+v %v", p, err)
	}
}
