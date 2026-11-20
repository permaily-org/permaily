package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// checkSentMail checks what an enclave says about the mail it sends through
// Amazon SES, a sign-in code among it. Whoever administers the account can
// give an identity a configuration set that applies to everything sent from
// it, and with one SES can be told to keep a copy. The enclave reads whether
// one applies, about once a minute and around each message, and sends only
// on a reading of the last seconds; so none may apply now, and none may have
// applied in any state the enclave has seen since it started.
func checkSentMail(m *attestation.MailStatus, now time.Time) error {
	if m.Domain == "" || m.Address == "" {
		return errors.New("the enclave does not say who its mail is from")
	}
	if m.State == nil || m.CheckedAt.IsZero() {
		return errors.New("the enclave has not read from SES what it does with a message; it sends none until it has")
	}
	if age := now.Sub(m.CheckedAt); age > maxPolicyAge || age < -maxPolicyAge {
		return fmt.Errorf("what SES does with a message was read by the enclave at %s, not within %s of now", m.CheckedAt.UTC().Format(time.RFC3339), maxPolicyAge)
	}
	// A configuration set put for a moment and taken away is still a copy of
	// what was sent in that moment.
	if m.HistoryTruncated || len(m.History) == 0 {
		return errors.New("what SES does with a message changed more times than the enclave remembers since it started; its history is incomplete")
	}
	if m.History[len(m.History)-1].MailState != *m.State {
		return errors.New("the history the enclave reports does not end in the current state")
	}
	if why := notClean(m, *m.State); why != "" {
		return errors.New(why)
	}
	for _, past := range m.History[:len(m.History)-1] {
		if why := notClean(m, past.MailState); why != "" {
			return fmt.Errorf("from %s to %s: %s", past.FirstSeen.UTC().Format(time.RFC3339), past.LastSeen.UTC().Format(time.RFC3339), why)
		}
	}
	return nil
}

// notClean says why a state lets someone get a copy of the mail, with the
// same rule the enclave sends by (MailState.Clean); empty when it does not.
func notClean(m *attestation.MailStatus, s attestation.MailState) string {
	const set = "the SES identity %s has a default configuration set, which applies to every message sent from it and can make SES keep a copy"
	switch {
	case s.Clean():
		return ""
	case s.DomainIdentity == attestation.MailConfigurationSet:
		return fmt.Sprintf(set, m.Domain)
	case s.AddressIdentity == attestation.MailConfigurationSet:
		return fmt.Sprintf(set, m.Address)
	case s.DomainIdentity == attestation.MailAbsent || s.DomainIdentity == attestation.MailNotVerified:
		return fmt.Sprintf("SES has no identity that may send for %s, so mail from %s would go out under another identity, whose settings the enclave does not read", m.Domain, m.Address)
	}
	return fmt.Sprintf("the enclave reports a state of SES this verifier does not know (domain identity %q, address identity %q)", s.DomainIdentity, s.AddressIdentity)
}

func printSentMail(w io.Writer, m *attestation.MailStatus) {
	if m == nil {
		return
	}
	address := "there is no identity for the address alone"
	if m.State.AddressIdentity != attestation.MailAbsent {
		address = "none on the identity of the address"
	}
	fmt.Fprintf(w, "      mail from %s goes out through Amazon SES with no configuration set, which is how SES can be told to keep a copy: none on the identity %s, and %s\n", m.Address, m.Domain, address)
	what := "unchanged"
	if n := len(m.History) - 1; n > 0 {
		what = fmt.Sprintf("%d other state(s), none with a configuration set", n)
	}
	fmt.Fprintf(w, "      that read by the enclave every minute and around each message since %s, last at %s: %s\n", m.WatchedSince.UTC().Format(time.RFC3339), m.CheckedAt.UTC().Format(time.RFC3339), what)
	if m.Failing {
		fmt.Fprintln(w, "      warning: the enclave's latest reading of SES failed; it sends no mail until one succeeds")
	}
	fmt.Fprintln(w, "      note: whoever administers the account can set one; the enclave sends only on a reading of the last seconds, and it would show here while this enclave runs, not before it started or after a restart")
	printNotices(w, m.Notices)
}

// printNotices says what the enclave that answered did with the queue of
// notices by mail since it started (ADR 0041, "What /status and
// permaily-verify show"), and warns when it dropped a notice or found an
// entry that does not verify. Neither fails the check: a notice tells, the
// app shows, and the queue's code is what the measurements say. Nothing
// is printed for an image older than the queue.
func printNotices(w io.Writer, n *attestation.NoticesStatus) {
	if n == nil {
		return
	}
	fmt.Fprintf(w, "      notices by mail about connections and receipts, this enclave's own since %s: %d queued, %d sent, %d attempt(s) failed and tried again, %d dropped, %d skipped, %d refused\n",
		n.Since.UTC().Format(time.RFC3339), n.Queued, n.Sent, n.Failed, n.Dropped, n.Skipped, n.Refused)
	if n.Dropped > 0 {
		fmt.Fprintf(w, "      warning: %d notice(s) were given up after a day of failed attempts, so someone was not told of a change to their connections or receipts (docs/operations.md, \"The queue of notices\")\n", n.Dropped)
	}
	if n.Refused > 0 {
		fmt.Fprintf(w, "      warning: %d entry(ies) of the queue did not verify or were not notices this image words, and nothing sent them: someone wrote the table, or an image wrote what this one does not know (docs/operations.md, \"The queue of notices\")\n", n.Refused)
	}
}
