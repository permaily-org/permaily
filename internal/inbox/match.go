package inbox

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The seam of ADR 0027 between the query language (internal/query, its pull
// request 1) and stored mail (its pull request 3, which implements Matcher on
// the store next to Reader, makes the reader's raw reads for it go through
// ReadGrants, and writes what delivery adds for it: the List-Id and the size
// in the meta, the meta's format, and whether a search entry's text was cut).

// Matcher is a page of the messages of an opened mailbox that formulas match
// together (ADR 0027, "Evaluation over stored mail"): fs are canonical and
// never simplified together (a consented scope, the request that fits in it,
// a preview's formula), and a message matches when the least of their values
// is True. It scans the box's message items newest first from below before
// ("" for the newest), opens each item's sealed meta before it classifies
// the message, reads the search entry only when the value still depends on a
// body condition, and the raw message only when that body condition depends
// on a text the entry cut. It ends the page when it has limit matches (1 to
// 100), MatchMaxUndecided messages that need a model or whose data could not
// be read, or when it reaches MatchMaxRead, MatchMaxTextReads or
// MatchSoftDeadline. A message is either considered whole on a page or left
// for the next: Next is the last message considered, so pages skip nothing. A
// received condition that bounds every match bounds the scan by the time in
// the message identifiers: before is where the scan starts; since stops it
// after the first batch that crosses it (the scan has no lower bound, so at
// most one batch is read below it), and nothing below it is evaluated or
// listed. Search entries are read by key (ReadStore.GetEntry), never
// scanned.
//
// Each raw read takes its bound from the request's read budget through
// ReadGrants and gives it back as soon as that message's text is taken out,
// before the next raw read, so a page holds one raw message's reservation at
// a time. A read budget in ctx that is not a ReadGrants is refused with
// contracts.ErrTooManyRequests before the first raw read; with no budget in
// ctx, nothing is taken (tests of the reader alone).
//
// The page it answers is the owner's own preview (contracts.MatchPage), never
// a vendor's result. It checks nothing of who asks: mailboxes.Service.Open
// did.
type Matcher interface {
	Match(ctx context.Context, b Box, fs []*query.Canonical, before string, limit int) (*contracts.MatchPage, error)
}

// What a page of matches is bounded by (ADR 0027, "Budgets of a page").
const (
	// MatchMaxRead is how many stored items a page reads at most: message
	// items and search entries, scanned or read one by one, expired ones
	// included.
	MatchMaxRead = 500
	// MatchMaxTextReads is how many raw messages a page reads at most, one
	// at a time, each under its own grant of the read budget, for a body
	// condition whose entry's text was cut.
	MatchMaxTextReads = 10
	// MatchMaxUndecided is how many messages a page lists as needing a model
	// or as unknown.
	MatchMaxUndecided = 100
	// MatchSoftDeadline is how long a page considers messages before it
	// answers what it has, well inside mailboxes.RequestDeadline.
	MatchSoftDeadline = 20 * time.Second
)
