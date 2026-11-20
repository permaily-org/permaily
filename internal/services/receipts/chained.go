package receipts

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/notices"
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
	// continued is, of each account, a link whose commit may have expired
	// and the digest the link above it names, authenticated when a page
	// ended above it or a continuation read down to it (continuation).
	continued map[string]authenticated

	catalogue *appender
	incidents *appender
	// notices, when given (WithNotices), is told of every account an
	// incident's entry names once the entry commits.
	notices notices.Queue
}

// ChainedOption is an option of NewChained.
type ChainedOption func(*chainState)

// WithNotices queues the notice that an account's receipts were found
// deleted or changed (notices.KindReceiptsIncident) for every account an
// entry of the record of incidents names, once that entry is committed,
// under notices.IncidentKey of the entry's time: the queue keeps one a day
// per account, whatever was found that day, and whichever enclave found it
// (ADR 0041, "Which notices"). An entry that fails to commit queues
// nothing. Queuing holds the caller at most notices.EnqueueMax, outside the
// record's lock and whatever became of the caller's context, and a notice
// that cannot be queued fails nothing of the record nor of what reported
// it; the chain logs nothing, so it is not logged here either.
func WithNotices(q notices.Queue) ChainedOption {
	return func(c *chainState) { c.notices = q }
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
//
// chain's Regressions name the chain's records, and a link's name holds its
// account's identifier: the composition reports them under the fixed name
// "receipt link", never their names, and nothing of the chain logs an
// account (ADR 0037, "When the link before cannot be read"; the review of
// #170, ruling 11).
func NewChained(entries store.Entries, keys Keys, now func() time.Time, chain *store.Anchored, accounts Accounts, opts ...ChainedOption) (*Service, error) {
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
		continued: map[string]authenticated{},
	}
	c.catalogue = &appender{s: s, sr: catalogueSeriesOf}
	c.incidents = &appender{s: s, sr: incidentSeriesOf}
	for _, o := range opts {
		o(c)
	}
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
	committed, err := s.recordLocked(ctx, todo)
	s.noticeIncidents(ctx, committed)
	return err
}

// incidentAt is an entry of the record of incidents that was committed: its
// time and the accounts its findings name.
type incidentAt struct {
	at       time.Time
	accounts []string
}

// recordLocked writes todo in entries of the record of incidents, in chunks
// of IncidentFindingsMax, holding the record's lock, and returns the entries
// it committed, those before an error included.
func (s *Service) recordLocked(ctx context.Context, todo []Finding) ([]incidentAt, error) {
	c := s.chain
	key, err := s.chainKey(ctx)
	if err != nil {
		return nil, err
	}
	c.incidents.write.Lock()
	defer c.incidents.write.Unlock()
	var committed []incidentAt
	for len(todo) > 0 {
		chunk := todo[:min(len(todo), IncidentFindingsMax)]
		var at time.Time
		if _, err := c.incidents.putLocked(ctx, key, func(n uint64, t time.Time, prev *Digest, br BreakReason) []byte {
			at = t
			return mustJSON(&Incident{V: IncidentVersion, I: n, At: t, Prev: prev, Break: br, Findings: chunk})
		}, nil); err != nil {
			return committed, err
		}
		committed = append(committed, incidentAt{at: at, accounts: accountsOf(chunk)})
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
	return committed, nil
}

// accountsOf are the accounts findings name, each once, in order; a
// finding of the catalogue names none.
func accountsOf(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		if f.Account != "" {
			out = append(out, f.Account)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// noticeIncidents queues the notice of every account the committed entries
// name (WithNotices), bounded by notices.EnqueueMax; an error is dropped.
func (s *Service) noticeIncidents(ctx context.Context, committed []incidentAt) {
	q := s.chain.notices
	if q == nil || len(committed) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notices.EnqueueMax)
	defer cancel()
	for _, e := range committed {
		for _, account := range e.accounts {
			_ = q.Enqueue(ctx, notices.Notice{
				Account: account, Kind: notices.KindReceiptsIncident, At: e.at,
				Key: notices.IncidentKey(account, e.at),
			})
		}
	}
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
