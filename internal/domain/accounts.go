// Package domain holds the rules of the product that need nothing but their
// arguments: no storage, no network, no clock of their own. Everything here
// can be tested with a table, and the services apply it.
//
// It imports only the standard library and internal/contracts.
package domain

import (
	"strings"
	"time"

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
func FormatInvitation(code string) string {
	var groups []string
	for len(code) > 4 {
		groups, code = append(groups, code[:4]), code[4:]
	}
	return strings.ToUpper(strings.Join(append(groups, code), "-"))
}

// NormalizeInvitation returns the invitation a person typed as it is kept:
// without spaces and dashes, in lower case. It reports false for anything
// that cannot be one.
func NormalizeInvitation(s string) (string, bool) {
	s = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != InvitationLength {
		return "", false
	}
	for _, c := range s {
		if !strings.ContainsRune(InvitationAlphabet, c) {
			return "", false
		}
	}
	return s, true
}

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
