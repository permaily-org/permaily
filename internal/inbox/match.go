package inbox

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The seam of ADR 0027 between the query language (internal/query, its pull
// request 1) and stored mail (its pull request 3, which implements Matcher on
// the store next to Reader, and writes what delivery adds for it: the List-Id
// in the meta, the meta's format, and whether a search entry's text was cut).

// Matcher is a page of what a formula matches in an opened mailbox (ADR 0027,
// "Evaluation over stored mail"). It reads the box's messages newest first
// from below before ("" for the newest), each source of a message only when
// the formula's value still depends on it, and ends the page when it has
// limit matches (1 to 100), MatchMaxUndecided messages that need a model or
// whose data could not be read, or when it reaches MatchMaxRead,
// MatchMaxTextReads or MatchSoftDeadline. A message is either considered
// whole on a page or left for the next: Next is the last message considered,
// so pages skip nothing. A received condition that bounds every match bounds
// the scan by the time in the message identifiers. It checks nothing of who
// asks: mailboxes.Service.Open did.
type Matcher interface {
	Match(ctx context.Context, b Box, f *query.Canonical, before string, limit int) (*contracts.MatchPage, error)
}

// What a page of matches is bounded by (ADR 0027, "Budgets of a page").
const (
	// MatchMaxRead is how many stored items a page reads at most: message
	// items and search entries, scanned or read one by one, expired ones
	// included.
	MatchMaxRead = 500
	// MatchMaxTextReads is how many raw messages a page reads at most, each
	// under the read budget, for a body condition the first 16 KiB of the
	// text did not settle.
	MatchMaxTextReads = 10
	// MatchMaxUndecided is how many messages a page lists as needing a model
	// or as unknown.
	MatchMaxUndecided = 100
	// MatchSoftDeadline is how long a page considers messages before it
	// answers what it has, well inside mailboxes.RequestDeadline.
	MatchSoftDeadline = 20 * time.Second
)
