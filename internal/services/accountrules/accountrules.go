// Package accountrules keeps the person's own rules (ADR 0040): the account's
// record of rules, a strict anchored record sealed under the root secret,
// read and written at its version, rolling to a next epoch at its versions'
// bound; turning the default rule off; the rules' notice; and the rules a
// connection applies before its formulas.
//
// This file is the seam, committed with the design: what the connections
// service, the consent screen's preview and delivery (ADR 0039) ask of it,
// with a fake that keeps its contract (accountrulestest). ADR 0040's pull
// request 1 writes the service behind it.
package accountrules

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
)

// Source is the rules a connection applies. It is the one way the
// connections service, the consent screen's preview and delivery's matching
// (ADR 0039: a notice is sent only for a message a read would give) learn
// them, so all three apply the same rules in the same order.
type Source interface {
	// Applying is the rules a connection of client applies now, in the
	// order they are evaluated (rules.Record.For): read strictly, from a
	// cache at most Fresh old in this enclave, and from the record at once
	// after a change made through this enclave. An error (the record lost,
	// forked, unopenable or unreadable, a rule that does not compile, the
	// context's end) means nothing of the account's mail leaves: the caller
	// answers unavailable and gives nothing, never the default alone.
	Applying(ctx context.Context, account, client string) (Applying, error)
}

// Applying is the rules a request applies, and the version of the account's
// record they were read at, which receipts name with each rule's own.
type Applying struct {
	Rules []rules.Rule
	// Epoch and Version are the record's epoch and version read; zero for
	// an account with no record (the default alone, on).
	Epoch   uint64
	Version uint64
}

// Fresh bounds how old the rules a request applies may be in an enclave
// other than the one a change was made through: the same five seconds as a
// revocation (ADR 0030), so a rule added, changed or turned on applies to
// every request that begins five seconds after it is saved, through either
// enclave.
const Fresh = 5 * time.Second

// ErrUnavailable is Applying's answer when the record cannot be used now;
// it wraps the cause, which a log line names by kind only.
var ErrUnavailable = errors.New("accountrules: the account's rules cannot be read now")
