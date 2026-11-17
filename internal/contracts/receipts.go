package contracts

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// The person's receipts and their export (ADR 0037, "What a person can
// verify"; pull request 3): the shapes of GET /api/v1/receipts and GET
// /api/v1/receipts/export. They are the receipts service's walk and export
// pages (receipts.Walked, receipts.ExportPage) as they cross the boundary,
// field for field; an export's page is exactly what permaily-verify
// -receipts reads, one a line.

// ErrInvalidBefore refuses a page asked from a position that is not one: not
// "<n>.<index>", or one the account's chain does not have (400).
var ErrInvalidBefore = &Error{"invalid_before", "before is not a place on the chain; use the next of the page before"}

// ErrReceiptsUnreadable says the account's chain cannot be read now: what is
// stored under it passes what a page reads, which only whoever writes the
// storage can do (503). Nothing is left out: no page is answered.
var ErrReceiptsUnreadable = &Error{"receipts_unreadable", "your receipts can't be read now: what Permaily stores for them was changed by someone else. Write to support@permaily.com"}

// ReceiptsIndexMax is the highest index a position may name: a link names
// at most this many receipts (receipts.LinkReceiptsMax).
const ReceiptsIndexMax = 64

// ReceiptsPosition is where a receipt is on its account's chain: its link's
// number and its index in that link. As where a page begins (before), {N,
// 0} begins at link N-1. In a query it is written "<n>.<index>".
type ReceiptsPosition struct {
	N     uint64 `json:"n"`
	Index int    `json:"index"`
}

// String writes p as a query's before reads it.
func (p ReceiptsPosition) String() string {
	return strconv.FormatUint(p.N, 10) + "." + strconv.Itoa(p.Index)
}

// ParseReceiptsPosition reads "<n>.<index>": n from 1, index from 0 to
// ReceiptsIndexMax, in decimal without signs, spaces or leading zeros.
// Anything else is ErrInvalidBefore.
func ParseReceiptsPosition(s string) (ReceiptsPosition, error) {
	n, index, ok := strings.Cut(s, ".")
	if !ok || !decimal(n) || !decimal(index) || n == "0" {
		return ReceiptsPosition{}, ErrInvalidBefore
	}
	num, err := strconv.ParseUint(n, 10, 64)
	if err != nil {
		return ReceiptsPosition{}, ErrInvalidBefore
	}
	i, err := strconv.Atoi(index)
	if err != nil || i > ReceiptsIndexMax {
		return ReceiptsPosition{}, ErrInvalidBefore
	}
	return ReceiptsPosition{N: num, Index: i}, nil
}

// decimal says s is digits without a leading zero, or "0".
func decimal(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ReceiptsHead is the newest link of the account's chain when the page was
// read: its number, what was found of it, and, when it is "ok", the digest
// of its document (lowercase hexadecimal SHA-256) and when it was written.
// That number and digest are what the person notes, to check an export
// later (permaily-verify -receipts-head <n>:<sha256>). N is 0 for a chain
// not begun.
type ReceiptsHead struct {
	N       uint64    `json:"n"`
	Verdict string    `json:"verdict,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// ReceiptsPage is one page of the account's receipts, the newest first:
// links in descending number, and in each its receipts in descending index.
// A page ends at a bound and the next begins from Next (before=Next); Done
// says nothing is left.
type ReceiptsPage struct {
	Head  ReceiptsHead     `json:"head"`
	Links []ReceiptsLink   `json:"links"`
	Next  ReceiptsPosition `json:"next"`
	Done  bool             `json:"done,omitempty"`
}

// ReceiptsLink is one link of a page: its verdict ("ok", "prev", "expired",
// "lost", "forked" or "unreadable") and, when it could be read ("ok",
// "prev", "expired"), what it says: its kind ("receipts", "adopted" for
// receipts from before the chain, "recovered" for receipts linked late),
// when, the digest of the link before it or why it could not name it
// (Break), and the receipts of it the page holds.
type ReceiptsLink struct {
	N        uint64          `json:"n"`
	Verdict  string          `json:"verdict"`
	Kind     string          `json:"kind,omitempty"`
	At       time.Time       `json:"at,omitzero"`
	Prev     string          `json:"prev,omitempty"`
	Break    string          `json:"break,omitempty"`
	Before   time.Time       `json:"before,omitzero"`
	More     bool            `json:"more,omitempty"`
	Receipts []ReceiptsEntry `json:"receipts,omitempty"`
}

// ReceiptsEntry is one receipt a link names, at Index in it: its identifier,
// the digest the link names, its verdict ("ok", "missing", "unreadable" or
// "changed") and, when it is "ok", the receipt itself (internal/services/
// receipts.Receipt as JSON).
type ReceiptsEntry struct {
	Index   int             `json:"index"`
	ID      string          `json:"id"`
	SHA256  string          `json:"sha256"`
	Verdict string          `json:"verdict"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
}

// ReceiptsExportPage is one page of an export (receipts.ExportPage): every
// link and receipt as stored, each document's exact bytes (base64 in JSON).
// An export is its pages in order, one JSON object a line.
type ReceiptsExportPage struct {
	V       int                  `json:"v"`
	Account string               `json:"account"`
	Head    ReceiptsHead         `json:"head"`
	Links   []ReceiptsExportLink `json:"links"`
	Next    ReceiptsPosition     `json:"next"`
	Done    bool                 `json:"done,omitempty"`
}

// ReceiptsExportLink is one link of an export: Doc is set when its verdict
// says it was read.
type ReceiptsExportLink struct {
	N        uint64                `json:"n"`
	Verdict  string                `json:"verdict"`
	Doc      []byte                `json:"doc,omitempty"`
	Receipts []ReceiptsExportEntry `json:"receipts,omitempty"`
}

// ReceiptsExportEntry is one receipt of an export: Doc is set unless its
// verdict is "missing" or "unreadable".
type ReceiptsExportEntry struct {
	Index   int    `json:"index"`
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Doc     []byte `json:"doc,omitempty"`
}
