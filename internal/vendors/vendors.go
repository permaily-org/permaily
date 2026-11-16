// Package vendors is who may ask for a connection and how the enclave knows
// them (ADR 0019, "Who may ask"; how in ADR 0030, "Vendors and the verified
// list"): any vendor, named by the host of its client metadata document's URL;
// the list of verified vendors compiled into the image (the owner's decision
// 3); and the seam through which a vendor's document and keys are fetched
// (Resolver), which internal/vendors/cimd implements over the vendor egress
// of the host (ADR 0030, "The egress for vendors' documents").
//
// The list and Verified are pure, committed with the design because they are
// the owner's decision and what /status shows; Resolver is implemented by ADR
// 0030's pull request 2.
package vendors

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
)

// Owner is a verified vendor: the exact host of its client_id URLs and who it
// is. A client_id whose host is Domain carries the mark and Name; no other
// host does, a subdomain of it included.
type Owner struct {
	Domain string
	Name   string
}

// verified is the list in the image (the owner's decision 3, 2026-11-16):
// Claude Code, Claude.ai and Claude Desktop publish their documents on
// claude.ai; ChatGPT and Codex on chatgpt.com. A change of it is a change of
// the image, and of ADR 0030.
var verified = []Owner{
	{Domain: "claude.ai", Name: "Anthropic"},
	{Domain: "chatgpt.com", Name: "OpenAI"},
}

// Verified is who owns domain, the host of a client_id URL in lower case as
// an A-label, when the image's list names it; "" and false otherwise.
func Verified(domain string) (string, bool) {
	for _, o := range verified {
		if o.Domain == domain {
			return o.Name, true
		}
	}
	return "", false
}

// List is the verified vendors, for /status, in the image's order.
func List() []Owner { return append([]Owner(nil), verified...) }

// Client is a vendor's client as the server uses it: the client_id URL, its
// host, its document as read and checked (oauth's rules), and when it was
// fetched. Keys is set only for a document that declares private_key_jwt.
type Client struct {
	ID        string
	Domain    string
	Document  oauth.ClientDocument
	FetchedAt time.Time
}

// Key is one public key of a vendor's key set, as the check of a client
// assertion uses it: its kid, its algorithm (RS256 or ES256) and the key.
type Key struct {
	ID        string
	Algorithm string
	Public    any // *rsa.PublicKey or *ecdsa.PublicKey
}

// Resolver fetches and caches vendors' documents and key sets (ADR 0030,
// "Clients"). It keeps them in the enclave's memory only, for the time their
// Cache-Control says (5 minutes to 24 hours), revalidates a stale one with
// If-None-Match or If-Modified-Since and keeps it on 304 Not Modified, and
// serves a stale one at most an hour more while revalidating fails. Every
// fetch goes through the vendor egress (HTTPS on port 443 only, no redirect,
// no cookie, no credential, a bounded body) and is refused before any
// connection when the URL breaks oauth's rules.
type Resolver interface {
	// Client resolves a client_id URL: ErrClientID for a URL the rules
	// refuse, ErrDocument for a document they refuse, ErrUnreachable when
	// it could not be fetched.
	Client(ctx context.Context, clientID string) (*Client, error)
	// Keys returns the key set at a document's jwks_uri; with refresh, it
	// fetches it again first (once a minute at most), for a kid it did not
	// have.
	Keys(ctx context.Context, c *Client, refresh bool) ([]Key, error)
}

// The ways resolving fails, by kind. None of them holds a URL, a host or a
// word of the vendor's answer: the reason is all a log line or an answer
// says (ADR 0030, "Logging").
var (
	ErrClientID    = errors.New("vendors: the client_id is not a URL that can be used")
	ErrDocument    = errors.New("vendors: the client's document is not one that can be used")
	ErrUnreachable = errors.New("vendors: the client's document or keys could not be fetched")
)
