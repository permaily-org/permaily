package receipts

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// ReceiptWriteMax bounds each write of a record of the chain from when it
// begins (store.WriteWithin): no value of a link is stored more than this
// after its write began, which account removal's second pass relies on
// (ADR 0037, "Appending"). It is connections.ReceiptWriteMax, which bounds
// writing one receipt, and a group whose waiters have no deadline.
const ReceiptWriteMax = 5 * time.Second

// Accounts is what the chain asks of the accounts service
// (accounts.Service).
type Accounts interface {
	// Removing answers whether account is removed (its record removed by
	// its terminal commit) or being removed (its record says so, or its
	// day to close has come), read strictly from its anchored record, or
	// live; anything else (the record lost, not found, not opening, the
	// storage failing) is an error, never taken for either.
	Removing(ctx context.Context, account string) (bool, error)
	// AccountRecords is a page of the accounts' list, oldest first.
	AccountRecords(ctx context.Context, after string, limit int) (*contracts.AccountList, error)
}

// chainState is what a chained Service holds of the chain.
type chainState struct {
	anchored *store.Anchored
	scanner  store.Scanner
	accounts Accounts

	mu        sync.Mutex
	appenders map[string]*linkAppender
	entered   map[string]bool
	// pending are the findings found and not recorded yet; recorded those
	// this enclave recorded since it started.
	pending  map[Finding]bool
	recorded map[Finding]bool

	catalogue *appender
	incidents *appender
}

var (
	_ Chain   = (*Service)(nil)
	_ Auditor = (*Service)(nil)
)

// errNotChained is the answer of the chain's methods on a Service made by
// New.
var errNotChained = errors.New("receipts: the service is not chained")

// NewChained is New with every receipt linked in its account's chain
// before Write returns (ADR 0037), over chain, a strict store.Anchored
// whose records are kept in a ChainTable and whose commits list groups
// (store.RangeReader), reading and writing the chain's names and no other;
// accounts tells removed accounts apart and lists them. It refuses a
// chain that is missing or not strict, accounts missing, and entries that
// cannot be scanned.
func NewChained(entries store.Entries, keys Keys, now func() time.Time, chain *store.Anchored, accounts Accounts) (*Service, error) {
	scanner, scans := entries.(store.Scanner)
	switch {
	case chain == nil || !chain.Strict:
		return nil, errors.New("receipts: a chain needs a strict anchored store")
	case accounts == nil:
		return nil, errors.New("receipts: a chain needs the accounts")
	case !scans:
		return nil, errors.New("receipts: a chain needs entries it can scan")
	}
	if _, ok := chain.Commits.(store.RangeReader); !ok {
		return nil, errors.New("receipts: a chain needs commits that list groups")
	}
	s := New(entries, keys, now, nil)
	c := &chainState{
		anchored: chain, scanner: scanner, accounts: accounts,
		appenders: map[string]*linkAppender{}, entered: map[string]bool{},
		pending: map[Finding]bool{}, recorded: map[Finding]bool{},
	}
	c.catalogue = &appender{s: s, sr: catalogueSeriesOf}
	c.incidents = &appender{s: s, sr: incidentSeriesOf}
	s.chain = c
	return s, nil
}

// Chained says whether Write links every receipt (NewChained) or not (New).
func (s *Service) Chained() bool { return s.chain != nil }

func (s *Service) chainKey(ctx context.Context) ([]byte, error) {
	return s.keys.Derive(ctx, ChainPurpose)
}

// Link implements Chain.
func (s *Service) Link(ctx context.Context, account string, ref Ref) (Position, error) {
	if s.chain == nil {
		return Position{}, errNotChained
	}
	if !validAccount(account) || !ValidID(ref.ID) {
		return Position{}, errors.New("receipts: a link names a receipt of an account")
	}
	if err := ctx.Err(); err != nil {
		return Position{}, err
	}
	return s.chain.appender(s, account).add(ctx, ref)
}

// recordedMax bounds what an enclave remembers of the findings it
// recorded: one forgotten and found again is recorded again, which only
// adds an entry.
const recordedMax = 1 << 16

// record records findings in the record of incidents, those this enclave
// did not record since it started (ADR 0037, "The record of incidents"):
// each is pending, counted by Broken, until an entry naming it is
// committed; one pending from a record that failed is recorded again when
// found again. An error leaves them pending and fails what was to report
// them.
func (s *Service) record(ctx context.Context, findings []Finding) error {
	c := s.chain
	c.mu.Lock()
	var todo []Finding
	seen := map[Finding]bool{}
	for _, f := range findings {
		if c.recorded[f] || seen[f] {
			continue
		}
		seen[f] = true
		c.pending[f] = true
		todo = append(todo, f)
	}
	c.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return err
	}
	c.incidents.write.Lock()
	defer c.incidents.write.Unlock()
	for len(todo) > 0 {
		chunk := todo[:min(len(todo), IncidentFindingsMax)]
		if _, err := c.incidents.putLocked(ctx, key, func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte {
			return mustJSON(&Incident{V: IncidentVersion, I: n, At: at, Prev: prev, Break: br, Findings: chunk})
		}, nil); err != nil {
			return err
		}
		c.mu.Lock()
		if len(c.recorded) >= recordedMax {
			clear(c.recorded)
		}
		for _, f := range chunk {
			delete(c.pending, f)
			c.recorded[f] = true
		}
		c.mu.Unlock()
		todo = todo[len(chunk):]
	}
	return nil
}

// Incidents implements Auditor: the record of incidents' head, found by
// listing its commits, whatever became of its entries' values.
func (s *Service) Incidents(ctx context.Context) (attestation.ReceiptsIncidents, error) {
	if s.chain == nil {
		return attestation.ReceiptsIncidents{}, errNotChained
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return attestation.ReceiptsIncidents{}, err
	}
	head, err := s.findHead(ctx, key, incidentSeriesOf)
	if err != nil {
		return attestation.ReceiptsIncidents{}, err
	}
	return attestation.ReceiptsIncidents{Recorded: head.n, Read: s.now().UTC()}, nil
}

// Broken implements Auditor: the findings pending, by kind.
func (s *Service) Broken() map[string]int64 {
	out := make(map[string]int64, len(attestation.ReceiptsBrokenKinds))
	for _, k := range attestation.ReceiptsBrokenKinds {
		out[k] = 0
	}
	if s.chain == nil {
		return out
	}
	s.chain.mu.Lock()
	defer s.chain.mu.Unlock()
	for f := range s.chain.pending {
		out[f.Kind]++
	}
	return out
}
