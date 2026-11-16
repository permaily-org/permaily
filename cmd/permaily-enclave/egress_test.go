package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/decide/openai"
	"github.com/permaily-org/permaily/internal/idp"
)

// The host lets the enclave reach only what its allowlist names, and does
// not link the code that knows where the providers are. So the list is
// written out there, and this fails when a provider's address changes and
// the list does not: signing in with it would stop in production, and
// nowhere else.
func TestEgressAllowsTheProviders(t *testing.T) {
	list := hostAllowlist(t)
	hosts := idp.Hosts()
	if len(hosts) == 0 {
		t.Fatal("no provider has an address")
	}
	for _, host := range hosts {
		if !strings.Contains(list, `"`+host+`",`) {
			t.Errorf("the host does not let the enclave reach %s", host)
		}
	}
	// Nothing of a provider is allowed that is not asked: where the browser
	// is sent is the person's to reach, not the enclave's.
	for _, never := range []string{"accounts.google.com", "www.facebook.com"} {
		if strings.Contains(list, never) {
			t.Errorf("the host lets the enclave reach %s, which only a browser goes to", never)
		}
	}
}

// hostAllowlist is the source of the host's allowlist.
func hostAllowlist(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("../permaily-host/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(source), "var defaultEgress = []string{")
	list, _, closed := strings.Cut(list, "\n}\n")
	if !ok || !closed {
		t.Fatal("the host's allowlist is not where it was")
	}
	return list
}

// A5 (ADR 0028): the host lets the enclave reach the model's provider, at
// the address its client calls, and its list gains nothing else: every
// entry is AWS, ACME, DNS, a provider of sign-in or the model.
func TestEgressAllowsTheModel(t *testing.T) {
	list := hostAllowlist(t)
	for _, host := range openai.Hosts() {
		if !strings.Contains(list, `"`+host+`",`) {
			t.Errorf("the host does not let the enclave reach %s", host)
		}
	}
	want := []string{
		"acme-v02.api.letsencrypt.org:443",
		"acme-staging-v02.api.letsencrypt.org:443",
		"kms.us-east-2.amazonaws.com:443",
		"dynamodb.us-east-2.amazonaws.com:443",
		"s3.us-east-2.amazonaws.com:443",
		"email.us-east-2.amazonaws.com:443",
		"cloudflare-dns.com:443",
	}
	want = append(append(want, idp.Hosts()...), openai.Hosts()...)
	var got []string
	for line := range strings.Lines(list) {
		if _, entry, ok := strings.Cut(line, `"`); ok {
			entry, _, _ = strings.Cut(entry, `"`)
			got = append(got, entry)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the host's allowlist is %v, want %v", got, want)
	}
}
