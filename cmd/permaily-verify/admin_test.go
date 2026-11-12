package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestPrintAdmin(t *testing.T) {
	at := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	// An image without accounts, or older: nothing is printed.
	if printAdmin(&out, nil); out.Len() != 0 {
		t.Fatalf("printed for an image that says nothing: %s", out.String())
	}
	none := map[string]int{}
	for _, kind := range attestation.AdminActions {
		none[kind] = 0
	}
	printAdmin(&out, &attestation.AdminStatus{Since: at, Actions: none})
	if !strings.Contains(out.String(), "what the operator did to accounts through this enclave since 2026-11-11T08:00:00Z: nothing\n") || !strings.Contains(out.String(), "a restart starts the count again") || !strings.Contains(out.String(), "nor read, disconnect or delete a mailbox, only pause and resume one") {
		t.Fatalf("nothing done:\n%s", out.String())
	}
	out.Reset()
	printAdmin(&out, &attestation.AdminStatus{Since: at, Actions: map[string]int{
		attestation.AdminInvitationsMade: 12, attestation.AdminBlock: 1, attestation.AdminUnblock: 0, attestation.AdminDeleteCancel: 2,
		"something_new": 3, "another_new": 1, attestation.AdminProviderAppRemoved: 1,
	}})
	want := `since 2026-11-11T08:00:00Z: blocked an account 1 time; cancelled the deletion of an account 2 times; made an invitation 12 times; removed a provider's application 1 time; ` +
		`did what this verifier does not know ("another_new") 1 time; did what this verifier does not know ("something_new") 3 times` + "\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	// A mailbox paused and resumed (ADR 0026): by kind, never which one.
	out.Reset()
	printAdmin(&out, &attestation.AdminStatus{Since: at, Actions: map[string]int{attestation.AdminMailboxPaused: 2, attestation.AdminMailboxResumed: 1}})
	if !strings.Contains(out.String(), "since 2026-11-11T08:00:00Z: paused a mailbox 2 times; resumed a mailbox 1 time\n") {
		t.Fatalf("mailboxes:\n%s", out.String())
	}
	// Every kind has its words.
	out.Reset()
	all := map[string]int{}
	for _, kind := range attestation.AdminActions {
		all[kind] = 2
	}
	printAdmin(&out, &attestation.AdminStatus{Since: at, Actions: all})
	if strings.Contains(out.String(), "does not know") || strings.Count(out.String(), "2 times") != len(attestation.AdminActions) {
		t.Fatalf("every kind:\n%s", out.String())
	}
}

// What the operator did is reported and fails nothing, whatever it says.
func TestCheckStatusAdmin(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	host := hostResult{URL: "https://app.example.org", SPKIHash: "ab"}
	for name, admin := range map[string]*attestation.AdminStatus{
		"nothing done":     {Since: now.Add(-time.Hour), Actions: map[string]int{attestation.AdminBlock: 0}},
		"many accounts":    {Since: now.Add(-time.Hour), Actions: map[string]int{attestation.AdminBlock: 100000, attestation.AdminDelete: 100000}},
		"an unknown kind":  {Since: now, Actions: map[string]int{"something_new": 1}},
		"no time, no kind": {},
		"a negative count": {Since: now, Actions: map[string]int{attestation.AdminBlock: -1}},
	} {
		r := checkStatus(&attestation.Status{Commit: "abc", Admin: admin}, host, imageA, kmsOptions{now: now})
		if !r.OK || r.Error != "" || r.Admin != admin {
			t.Errorf("%s: %+v", name, r)
		}
		var out bytes.Buffer
		printHuman(&out, "test", map[uint][]byte{0: {1}, 1: {2}, 2: {3}}, []hostResult{r})
		if !strings.Contains(out.String(), "PASS  https://app.example.org") || !strings.Contains(out.String(), "what the operator did to accounts through this enclave since ") {
			t.Errorf("%s: output:\n%s", name, out.String())
		}
	}
	if r := checkStatus(&attestation.Status{Commit: "abc"}, host, imageA, kmsOptions{now: now}); !r.OK || r.Admin != nil {
		t.Fatalf("an image that says nothing: %+v", r)
	}
}
