package main

import (
	"os"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/idp"
)

// The host lets the enclave reach only what its allowlist names, and does
// not link the code that knows where the providers are. So the list is
// written out there, and this fails when a provider's address changes and
// the list does not: signing in with it would stop in production, and
// nowhere else.
func TestEgressAllowsTheProviders(t *testing.T) {
	source, err := os.ReadFile("../permaily-host/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(source), "var defaultEgress = []string{")
	list, _, closed := strings.Cut(list, "\n}\n")
	if !ok || !closed {
		t.Fatal("the host's allowlist is not where it was")
	}
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
