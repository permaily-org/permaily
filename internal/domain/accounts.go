// Package domain holds the rules of the product that need nothing but their
// arguments: no storage, no network, no clock of their own. Everything here
// can be tested with a table, and the services apply it.
//
// It imports only the standard library and internal/contracts.
package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Sessions (ADR 0017): seven days without use, thirty days at most.
const (
	SessionIdle = 7 * 24 * time.Hour
	SessionMax  = 30 * 24 * time.Hour
	// SessionTouch is how stale the time of last use may get before it is
	// written again: not every request is a write.
	SessionTouch = time.Hour
)

// SessionExpiry is when a session ends, given when it was made and last used.
func SessionExpiry(created, lastUsed time.Time) time.Time {
	idle, max := lastUsed.Add(SessionIdle), created.Add(SessionMax)
	if idle.Before(max) {
		return idle
	}
	return max
}

// Signing in by email (ADR 0017): one message with a link and a six-digit
// code, both for a short time and a few tries.
const (
	EmailCodeDigits   = 6
	EmailCodeLifetime = 10 * time.Minute
	// EmailCodeTries is how many wrong answers a sign-in takes before it is
	// dropped: five guesses at a million codes.
	EmailCodeTries = 5
	// EmailStartsPerHour is how many sign-in messages one address may be sent.
	EmailStartsPerHour = 5
)

// InvitationLifetime is how long a sign-up code can be used.
const InvitationLifetime = 30 * 24 * time.Hour

// maxAddress is the longest address SMTP carries (RFC 5321).
const maxAddress = 254

// NormalizeAddress returns the form of an email address accounts are found
// by: trimmed and in lower case. It is strict on purpose: one address, plain
// ASCII, no display name, no comments, nothing a mail system could read in
// two ways.
func NormalizeAddress(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	local, host, ok := strings.Cut(s, "@")
	if !ok || len(s) > maxAddress || local == "" || len(local) > 64 || !validHost(host) {
		return "", contracts.ErrInvalidAddress
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", contracts.ErrInvalidAddress
	}
	for _, c := range local {
		if !isAlnum(c) && !strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", c) {
			return "", contracts.ErrInvalidAddress
		}
	}
	return s, nil
}

func isAlnum(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// validHost accepts a domain name with at least two labels.
func validHost(host string) bool {
	labels := strings.Split(host, ".")
	if len(labels) < 2 || len(host) > 253 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !isAlnum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

// Invitations are twelve characters of an alphabet without the letters and
// digits that are taken for one another, written in groups of four.
const (
	InvitationAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	InvitationLength   = 12
)

// FormatInvitation writes an invitation the way it is shown: XXXX-XXXX-XXXX.
func FormatInvitation(code string) string { return formatCode(code) }

// NormalizeInvitation returns the invitation a person typed as it is kept:
// without spaces and dashes, in lower case. It reports false for anything
// that cannot be one.
func NormalizeInvitation(s string) (string, bool) { return normalizeCode(s, InvitationLength) }

// formatCode writes a code of the invitation alphabet in groups of four.
func formatCode(code string) string {
	var groups []string
	for len(code) > 4 {
		groups, code = append(groups, code[:4]), code[4:]
	}
	return strings.ToUpper(strings.Join(append(groups, code), "-"))
}

// normalizeCode undoes formatCode and whatever a person adds while typing.
func normalizeCode(s string, length int) (string, bool) {
	s = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != length {
		return "", false
	}
	for _, c := range s {
		if !strings.ContainsRune(InvitationAlphabet, c) {
			return "", false
		}
	}
	return s, true
}

// Passkeys (ADR 0017).
const (
	// PasskeyCeremonyLifetime is how long the browser has to answer once
	// making a passkey, or signing in with one, was started.
	PasskeyCeremonyLifetime = 5 * time.Minute
	// MaxPasskeys is how many passkeys an account keeps.
	MaxPasskeys = 20
	// maxName is the longest name a person gives a passkey, in characters.
	maxName = 64
)

// PasskeyName returns the name a person gave a passkey as it is kept:
// trimmed. It must be one line, with something in it, and short.
func PasskeyName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxName {
		return "", contracts.ErrInvalidName
	}
	for _, c := range s {
		if !unicode.IsPrint(c) {
			return "", contracts.ErrInvalidName
		}
	}
	return s, nil
}

// Recovery codes (ADR 0017), for an account that signs in with passkeys
// only. They are written like invitations and are longer: their hashes are
// in the account's record, which whoever runs the storage reads, and sixteen
// characters are past what can be guessed against a hash.
const (
	RecoveryCodes      = 10
	RecoveryCodeLength = 16
	// RecoveryTriesPerHour is how many wrong recovery codes an address takes
	// in an hour.
	RecoveryTriesPerHour = 5
)

// FormatRecoveryCode writes a recovery code the way it is shown:
// XXXX-XXXX-XXXX-XXXX.
func FormatRecoveryCode(code string) string { return formatCode(code) }

// NormalizeRecoveryCode returns the recovery code a person typed as it is
// hashed. It reports false for anything that cannot be one.
func NormalizeRecoveryCode(s string) (string, bool) { return normalizeCode(s, RecoveryCodeLength) }

// maxDevice bounds what is kept of how a browser describes itself.
const maxDevice = 200

// Device returns what is kept of a User-Agent header: printable ASCII, cut.
func Device(userAgent string) string {
	var b strings.Builder
	for _, c := range userAgent {
		if b.Len() == maxDevice {
			break
		}
		if c >= ' ' && c <= '~' {
			b.WriteRune(c)
		}
	}
	return strings.TrimSpace(b.String())
}
