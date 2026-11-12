package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// adminWords says each kind of what an operator does to accounts through the
// enclave, as it is printed.
var adminWords = map[string]string{
	attestation.AdminBlock:              "blocked an account",
	attestation.AdminUnblock:            "unblocked an account",
	attestation.AdminDelete:             "asked to delete an account",
	attestation.AdminDeleteCancel:       "cancelled the deletion of an account",
	attestation.AdminSessionsClose:      "signed the devices of an account out",
	attestation.AdminInvitationsMade:    "made an invitation",
	attestation.AdminInvitationRevoked:  "revoked an invitation",
	attestation.AdminProviderAppSet:     "set a provider's application",
	attestation.AdminProviderAppRemoved: "removed a provider's application",
	attestation.AdminMade:               "made an account an admin",
	attestation.AdminRemoved:            "made an admin stop being one",
}

// printAdmin prints what the enclave counts of what its operator did to
// accounts through it. It checks nothing: an operator may block an account or
// ask to delete one, and through the enclave cannot do what would let them
// into one. It is printed so that it is seen.
func printAdmin(w io.Writer, a *attestation.AdminStatus) {
	if a == nil {
		return
	}
	// The kinds this verifier knows in their order, then any other the
	// enclave counts, by name.
	kinds := slices.Clone(attestation.AdminActions)
	var others []string
	for kind := range a.Actions {
		if !slices.Contains(kinds, kind) {
			others = append(others, kind)
		}
	}
	slices.Sort(others)
	var did []string
	for _, kind := range append(kinds, others...) {
		n := a.Actions[kind]
		if n <= 0 {
			continue
		}
		words, known := adminWords[kind]
		if !known {
			words = fmt.Sprintf("did what this verifier does not know (%q)", kind)
		}
		times := "times"
		if n == 1 {
			times = "time"
		}
		did = append(did, fmt.Sprintf("%s %d %s", words, n, times))
	}
	what := "nothing"
	if len(did) > 0 {
		what = strings.Join(did, "; ")
	}
	fmt.Fprintf(w, "      what the operator did to accounts through this enclave since %s: %s\n", a.Since.UTC().Format(time.RFC3339), what)
	fmt.Fprintln(w, "      note: counted by the enclave, by kind and never by account, in memory: a restart starts the count again. The operator cannot change an account's address or how it is signed into through the enclave; what is written to the storage by hand is not seen here, and is refused by the enclave where it would matter")
}
