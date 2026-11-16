package domain

import (
	"context"
	"errors"
	"net/url"
	"regexp"
)

// The patterns Redact takes out of a text (ADR 0015: logs leave the enclave
// and are public). What is logged of an error is Redacted's, never these.
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

// Redact takes identifiers, URLs and addresses out of a text: "mbx_…",
// "pma_…", "con_…" and the other prefixed identifiers keep their prefix, a
// URL its scheme ("https://…"), a hexadecimal identifier becomes "…", an
// address "…@…".
//
// Redact is not a safety boundary, and an error's text is never logged
// through it (Redacted logs none). It knows the literal spelling of what it
// looks for and nothing else: a secret or a URL percent-encoded
// ("pma%5F…"), JSON-escaped ("pma\u005f…", "https:\/\/…"), in base64, in
// upper case, without a scheme, or split across two errors passes through
// it whole, and what has no shape (a PKCE verifier or challenge, a state, a
// client assertion, a jti, a client_id's host written alone) cannot be
// recognised at all (PR #137's review, finding 2).
//
// The longer values go first: a connection's base64url secret may hold
// "-acc_a" or any older prefix after a hyphen, and replacing that first
// would cut the secret in two and leave its end (PR #137's review,
// finding 1).
func Redact(s string) string {
	s = urls.ReplaceAllString(s, "${1}://…")
	s = connectionIDs.ReplaceAllString(s, "${1}_…")
	s = prefixedIDs.ReplaceAllString(s, "${1}_…")
	s = hexIDs.ReplaceAllString(s, "…")
	return addresses.ReplaceAllString(s, "…@…")
}

// FixedKind is an error of a fixed kind: what it says in a log is its kind, a
// fixed text that holds nothing of a request (contracts.ClientError,
// connections.OAuthError). LogKind returns words written in the code, or
// chosen from a fixed set after checking the value against it, never a
// value it was given: it is trusted, and Redacted logs it as it is.
type FixedKind interface {
	LogKind() string
}

// Unclassified is what is logged of an error that is not of a fixed kind,
// a transport error or a context's end: its text is not logged at all,
// whatever it holds (PR #137's review, finding 2).
const Unclassified = "error (its text is not logged)"

// Redacted is what may be logged of an error, safe by construction: never
// its text, only a fixed kind. An error of a fixed kind (FixedKind) anywhere
// in its tree is logged as that kind alone, whatever wraps it. A transport
// error (a request to the storage, KMS or any other service that failed)
// is logged as its category and its HTTP method only: its URL names records
// and objects. A context's end is logged as that. Anything else is
// Unclassified: storage, the connections service and the OAuth paths make
// errors whose text may hold a secret in a form no pattern recognises, so
// an error is logged by what it is, never by what it says. The error is
// read, never changed: callers wrap with %w and keep the chain for
// errors.Is and errors.As. An error formatted with %v or %s instead of %w
// is no longer of its kind, and is Unclassified.
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
		if m, ok := methods[u.Op]; ok {
			return "transport error (" + m + ")"
		}
		return "transport error"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context deadline exceeded"
	}
	return Unclassified
}

// methods are the Ops net/http gives a *url.Error: an HTTP method, the
// first letter upper case. Any other Op is not logged.
var methods = map[string]string{
	"Get": "Get", "Head": "Head", "Post": "Post", "Put": "Put", "Patch": "Patch", "Delete": "Delete", "Options": "Options",
}
