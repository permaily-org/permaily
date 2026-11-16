package vendors

import (
	"testing"
	"time"
)

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

// N7 (the second design review): a stored document or key set is served
// stale on a failed revalidation only when its directives allow it, within
// StaleMax; no-cache, must-revalidate, proxy-revalidate, s-maxage and
// no-store fail closed, as RFC 9111 requires.
func TestReview2N7RestrictiveDirectivesAreNeverServedStale(t *testing.T) {
	cases := []struct {
		header             string
		maxAge             time.Duration
		freshAt, staleOKAt time.Duration
		fresh, staleOK     bool
	}{
		{"public, max-age=300", 300 * time.Second, 299 * time.Second, 300*time.Second + StaleMax - time.Second, true, true},
		{"public, max-age=300", 300 * time.Second, 300 * time.Second, 300*time.Second + StaleMax, false, false},
		{"", 0, 0, StaleMax - time.Second, false, true},
		{"no-cache", 0, 0, 0, false, false},
		{"max-age=30, must-revalidate", 30 * time.Second, time.Second, 31 * time.Second, true, false},
		{"max-age=30, Proxy-Revalidate", 30 * time.Second, time.Second, 31 * time.Second, true, false},
		{"s-maxage=60, max-age=600", 60 * time.Second, 59 * time.Second, 61 * time.Second, true, false},
		{"no-store, max-age=600", 0, 0, 0, false, false},
		{"max-age=0", 0, 0, time.Second, false, true},
		{"max-age=\"120\"", 120 * time.Second, time.Second, 121 * time.Second, true, true},
		{"max-age=abc", 0, 0, time.Second, false, true},
		{"max-age=60, max-age=120", 0, 0, time.Second, false, true},
		{"max-age=-5", 0, 0, time.Second, false, true},
		{"max-age=99999999999", CacheMaxAge, time.Hour, CacheMaxAge + time.Minute, true, true},
	}
	for _, c := range cases {
		p := ParseCacheControl(c.header)
		if p.MaxAge != c.maxAge || p.Fresh(c.freshAt) != c.fresh || p.StaleOnError(c.staleOKAt) != c.staleOK {
			t.Errorf("%q: %+v fresh %v stale %v", c.header, p, p.Fresh(c.freshAt), p.StaleOnError(c.staleOKAt))
		}
	}
}
