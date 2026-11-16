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
	"strconv"
	"strings"
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
// "Clients", revised after its review, choice 18). It keeps them in the
// enclave's memory only, at most CacheMax of them, for the time their
// Cache-Control says and no longer (no-store, no-cache or max-age=0: used
// for the request that fetched it and fetched again for the next; at most
// CacheMaxAge), revalidates a stale one with If-None-Match or
// If-Modified-Since, takes 304 Not Modified only for a document it holds
// (with that 304's validators and Cache-Control), and serves a stale one,
// when its revalidation fails, only as CachePolicy.StaleOnError allows: never
// for no-store, no-cache, must-revalidate, proxy-revalidate or s-maxage
// (N7), and at most StaleMax after its last successful fetch or
// revalidation went stale, never extended by failures. Key sets are cached
// by the same policy. Every fetch goes through the vendor egress
// (HTTPS on port 443 only, HTTP/2 only, no redirect, no cookie, no
// credential, a bounded body), at most FetchesInFlight at once, and is
// refused before any connection when the URL breaks oauth's rules.
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

// Bounds of the resolver, each enclave.
const (
	CacheMax        = 1024
	CacheMaxAge     = 24 * time.Hour
	StaleMax        = time.Hour
	FetchesInFlight = 8
	// FetchesPerMinute bounds the fetches that are not revalidations of a
	// cached document.
	FetchesPerMinute = 60
)

// The ways resolving fails, by kind. None of them holds a URL, a host or a
// word of the vendor's answer: the reason is all a log line or an answer
// says (ADR 0030, "Logging").
var (
	ErrClientID    = errors.New("vendors: the client_id is not a URL that can be used")
	ErrDocument    = errors.New("vendors: the client's document is not one that can be used")
	ErrUnreachable = errors.New("vendors: the client's document or keys could not be fetched")
)

// CachePolicy is what a stored document's or key set's Cache-Control lets
// the resolver do with it (ADR 0030, "Caching"; revised after the second
// design review, N7, as RFC 9111 sections 4.2.4 and 5.2.2 require):
// MaxAge is its freshness, capped at CacheMaxAge; NoStore, NoCache and
// Revalidate (must-revalidate, proxy-revalidate or s-maxage: this cache
// serves every person, so it is a shared one) forbid using it stale.
type CachePolicy struct {
	MaxAge     time.Duration
	NoStore    bool
	NoCache    bool
	Revalidate bool
}

// ParseCacheControl reads a Cache-Control header value. Directive names are
// case-insensitive; a max-age or s-maxage that is not a non-negative
// integer, or that is given twice with different values, is 0 (stale at
// once); s-maxage, for a shared cache, takes the place of max-age. An
// absent header is MaxAge 0 and nothing forbidden: used for the request
// that fetched it, revalidated for the next, served stale within StaleMax.
func ParseCacheControl(h string) CachePolicy {
	var p CachePolicy
	var maxAge, sMaxAge []string
	for _, d := range strings.Split(h, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), "=")
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "no-store":
			p.NoStore = true
		case "no-cache":
			p.NoCache = true
		case "must-revalidate", "proxy-revalidate":
			p.Revalidate = true
		case "max-age":
			maxAge = append(maxAge, value)
		case "s-maxage":
			sMaxAge = append(sMaxAge, value)
			p.Revalidate = true
		}
	}
	values := maxAge
	if len(sMaxAge) > 0 {
		values = sMaxAge
	}
	p.MaxAge = seconds(values)
	if p.NoStore || p.NoCache {
		p.MaxAge = 0
	}
	return p
}

// seconds is the one delta-seconds of values, capped at CacheMaxAge; 0 for
// none, one that is not a non-negative integer, or two that differ.
func seconds(values []string) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var n int64
	for i, v := range values {
		m, err := strconv.ParseInt(v, 10, 64)
		if err != nil || m < 0 || (i > 0 && m != n) {
			return 0
		}
		n = m
	}
	if n >= int64(CacheMaxAge/time.Second) {
		return CacheMaxAge
	}
	return time.Duration(n) * time.Second
}

// Fresh says the stored response may be used without revalidation at age
// (since its last successful fetch or revalidation).
func (p CachePolicy) Fresh(age time.Duration) bool {
	return !p.NoStore && !p.NoCache && age < p.MaxAge
}

// StaleOnError says the stored response may be served when its
// revalidation fails, at age since its last successful fetch or
// revalidation: only when nothing it says forbids serving it stale, and
// only within StaleMax of when it went stale (never extended by further
// failures). A key set or document whose directives forbid it fails
// closed: its client is unreachable until it can be fetched again.
func (p CachePolicy) StaleOnError(age time.Duration) bool {
	if p.NoStore || p.NoCache || p.Revalidate {
		return false
	}
	return age < p.MaxAge+StaleMax
}
