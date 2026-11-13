package mailhtml

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"path"
	"strconv"

	"github.com/permaily-org/permaily/internal/contracts"
)

// linkTag is the tag of link n of a message (ADR 0026, "The link's token"):
// HMAC-SHA-256 under the mailbox's link key over the message identifier,
// the number and the canonical URL, truncated to MessageLinkTagBytes and
// written in unpadded base64url (16 characters).
func linkTag(key []byte, message string, n int, canonical string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.Itoa(n)))
	mac.Write([]byte{0})
	mac.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:contracts.MessageLinkTagBytes])
}

// Token is a link's token as the route names it: "<n>.<tag>".
func (l Link) Token() string { return path.Base(l.Path) }

// Find is the link a token names among links: its number must be one of
// them and its tag that link's, compared in constant time. ok is false
// otherwise: the link changed since the page that holds the token was
// rendered (contracts.ErrLinkChanged). The token must have the shape
// contracts.MessageLinkToken checks.
func Find(links []Link, token string) (Link, bool) {
	dot := 0
	for dot < len(token) && token[dot] != '.' {
		dot++
	}
	n, err := strconv.Atoi(token[:dot])
	if err != nil || n < 0 || n >= len(links) {
		return Link{}, false
	}
	want := links[n].Token()
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return Link{}, false
	}
	return links[n], true
}
