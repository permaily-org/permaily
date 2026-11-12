package domain

import (
	"errors"
	"net/url"
	"regexp"
)

// What is taken out of an error before it is logged (ADR 0015: logs leave
// the enclave and are public). One redactor for every place that logs an
// error (services/accounts, services/mailboxes, httpapi), so no copy of the
// patterns falls behind another.
var (
	// prefixedIDs are the identifiers this deployment makes: accounts,
	// sessions, sign-in attempts, ceremonies, invitations and mailboxes.
	prefixedIDs = regexp.MustCompile(`\b(acc|ses|att|cer|inv|mbx)_[a-z0-9]+`)
	// hexIDs are message identifiers (32 hexadecimal characters) and the
	// longer digests and HMACs that name entries (an address's pointer).
	hexIDs = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	// addresses are anything shaped like name@host.
	addresses = regexp.MustCompile(`[^\s"'<>(),;:@/\\]+@[^\s"'<>(),;:@/\\]+`)
)

// Redact takes identifiers and addresses out of a text that is about to be
// logged: "mbx_…", "acc_…" and the other prefixed identifiers keep their
// prefix, a hexadecimal identifier becomes "…", an address "…@…".
func Redact(s string) string {
	s = prefixedIDs.ReplaceAllString(s, "${1}_…")
	s = hexIDs.ReplaceAllString(s, "…")
	return addresses.ReplaceAllString(s, "…@…")
}

// Redacted is what may be logged of an error. A transport error (a request
// to the storage, KMS or any other service that failed) is logged as its
// category and its method only: its URL names records and objects, and so
// identifiers. Anything else is its text, redacted.
func Redacted(err error) string {
	if err == nil {
		return ""
	}
	var u *url.Error
	if errors.As(err, &u) {
		return "transport error (" + u.Op + ")"
	}
	return Redact(err.Error())
}
