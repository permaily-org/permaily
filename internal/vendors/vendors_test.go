package vendors

import "testing"

// The owner's decision 3 (ADR 0030): Claude and ChatGPT/Codex, by the exact
// host of their documents' URLs; every other host is shown by its domain
// without the mark.
func TestTheVerifiedVendorsAreTheOwnersList(t *testing.T) {
	cases := map[string]string{
		"claude.ai":         "Anthropic",
		"chatgpt.com":       "OpenAI",
		"www.claude.ai":     "",
		"claude.ai.evil.io": "",
		"chatgpt.co":        "",
		"CLAUDE.AI":         "", // hosts are compared after lower-casing, by the caller
		"":                  "",
	}
	for domain, want := range cases {
		got, ok := Verified(domain)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", domain, got, ok, want)
		}
	}
	l := List()
	if len(l) != 2 || l[0].Domain != "claude.ai" || l[1].Domain != "chatgpt.com" {
		t.Fatalf("list %v", l)
	}
	l[0].Domain = "x"
	if got, _ := Verified("claude.ai"); got != "Anthropic" {
		t.Fatal("List hands out the list itself")
	}
}
