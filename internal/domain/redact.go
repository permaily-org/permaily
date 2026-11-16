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
	// connectionIDs are what connections make (ADR 0030, "Codes and
	// tokens"): access and refresh tokens, codes, authorization and pushed
	// requests, cursors (pma_, pmr_, pmc_, pmq_, pmp_, pmk_: base64url, so
	// upper case, '_' and '-'), connections, token families, receipts and
	// drafts (con_, fam_, rcp_, qry_). Matched wherever the prefix starts,
	// so a name glued to it (oauth-family-fam_…) is no hiding place.
	connectionIDs = regexp.MustCompile(`(pma|pmr|pmc|pmq|pmp|pmk|con|fam|rcp|qry)_[A-Za-z0-9_-]+`)
	// urls are anything with a scheme and "://": a vendor's client_id, a
	// redirect URI with its code and state, a document's or a key set's
	// address. The scheme stays, the rest goes.
	urls = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*)://[^\s"'<>]+`)
	// hexIDs are message identifiers (32 hexadecimal characters) and the
	// longer digests and HMACs that name entries (an address's pointer).
	hexIDs = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	// addresses are anything shaped like name@host.
	addresses = regexp.MustCompile(`[^\s"'<>(),;:@/\\]+@[^\s"'<>(),;:@/\\]+`)
)

// Redact takes identifiers, URLs and addresses out of a text that is about
// to be logged: "mbx_…", "pma_…", "con_…" and the other prefixed
// identifiers keep their prefix, a URL its scheme ("https://…"), a
// hexadecimal identifier becomes "…", an address "…@…".
//
// What has no shape cannot be recognised: a PKCE verifier or challenge, a
// state, a client assertion, a jti, a client_id's host written alone. An
// error that may carry one is never text that wraps it: it is a fixed kind
// (FixedKind), which Redacted logs alone.
func Redact(s string) string {
	s = urls.ReplaceAllString(s, "${1}://…")
	s = prefixedIDs.ReplaceAllString(s, "${1}_…")
	s = connectionIDs.ReplaceAllString(s, "${1}_…")
	s = hexIDs.ReplaceAllString(s, "…")
	return addresses.ReplaceAllString(s, "…@…")
}

// FixedKind is an error of a fixed kind: what it says in a log is its kind, a
// fixed text that holds nothing of a request (contracts.ClientError,
// connections.OAuthError). Every error of connections that may carry a
// value no pattern of Redact recognises (a verifier, a challenge, a state,
// an assertion, a jti, an unverified client_id or its host, a formula) is
// one, or a sentinel with fixed words, never text formatted with the value
// (ADR 0030, "Logging", "Seams added while building").
type FixedKind interface {
	LogKind() string
}

// Redacted is what may be logged of an error. An error of a fixed kind
// (FixedKind) anywhere in its chain is logged as that kind alone: whatever
// wraps it is dropped, so text formatted around it cannot leak. A transport
// error (a request to the storage, KMS or any other service that failed)
// is logged as its category and its method only: its URL names records and
// objects, and so identifiers. Anything else is its text, redacted.
func Redacted(err error) string {
	if err == nil {
		return ""
	}
	var k FixedKind
	if errors.As(err, &k) {
		return k.LogKind()
	}
	var u *url.Error
	if errors.As(err, &u) {
		return "transport error (" + u.Op + ")"
	}
	return Redact(err.Error())
}
