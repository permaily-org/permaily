package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The record of what left, chained (ADR 0037, pull request 2): every
// receipt linked in its account's chain before its answer leaves, the
// daily audit under a lease, and what /status says of both.

// receiptRecord stands for every record of the chain (a link, an entry of
// the catalogue, a pass of the audit, an entry of the record of incidents)
// in /status: a link's name holds its account's identifier, and the
// numbers of the others say how many there are (ADR 0037, "When the link
// before cannot be read"; the review of #170, ruling 11).
const receiptRecord = "receipt link"

// incidentsEvery is how often an enclave reads the record of incidents and
// the newest pass of the audit again for /status (ADR 0037, "What /status
// and permaily-verify show"): well within ReceiptsIncidentsStaleAfter, so a
// read that fails twice still leaves a fresh one.
const incidentsEvery = 10 * time.Minute

// auditRetry is how often an enclave asks whether a pass of the audit is
// due: a pass begins AuditEvery after the newest finished one began, and
// one that did not finish begins again an hour later (ADR 0037, "The
// audit").
const auditRetry = time.Hour

// auditLease is how long the lease of a pass lasts: longer than a run, so
// the other enclave takes it over only once the run it covered is over.
const auditLease = receipts.AuditRunMax + 10*time.Minute

// auditLeasePartition and auditLeaseKey are where the audit's lease is
// kept: a hint between enclaves, never what a pass rests on.
const (
	auditLeasePartition = "receipts-audit-lease"
	auditLeaseKey       = "lease"
)

// auditLeasePurpose is the key a lease is sealed under: a lease that does
// not open is taken over as one that ended.
const auditLeasePurpose = "permaily receipts audit lease v1"

// errReceiptsChain refuses a chain that is not one strict Anchored over the
// chain's own table and grouped commits.
var errReceiptsChain = errors.New("the chain of receipts needs a strict Anchored over its ChainTable whose commits list groups")

// receiptsParts is what the chained receipts are composed from.
type receiptsParts struct {
	// chain is the one strict Anchored of the chain's records, over a
	// receipts.ChainTable of entries and commits grouped by
	// receipts.CommitGroup: no other Anchored reads or writes their names,
	// and it reads no other.
	chain *store.Anchored
	// entries holds the receipts, the links' values and the audit's lease.
	entries  store.Entries
	keys     receipts.Keys
	accounts receipts.Accounts
	now      func() time.Time
}

// composedReceipts is the chained receipts service and what /status
// reports of it.
type composedReceipts struct {
	svc     *receipts.Service
	chain   *store.Anchored
	entries store.Entries
	keys    receipts.Keys
	now     func() time.Time
	// holder is this enclave's name in the audit's lease.
	holder string

	mu sync.Mutex
	// incidents and audit are what the enclave last read of the record of
	// incidents and of the newest finished pass; nil until read.
	incidents *attestation.ReceiptsIncidents
	audit     *attestation.ReceiptsAudit
}

// composeReceipts is the receipts service with every receipt linked in its
// account's chain before Write returns (receipts.NewChained), the chain's
// records in one strict Anchored over the chain's table, its commits
// grouped.
func composeReceipts(r receiptsParts) (*composedReceipts, error) {
	if r.chain == nil || !r.chain.Strict {
		return nil, errReceiptsChain
	}
	if _, ok := r.chain.Records.(receipts.ChainTable); !ok {
		return nil, errReceiptsChain
	}
	if _, ok := r.chain.Commits.(store.RangeReader); !ok || !chainGrouped(r.chain.Commits) {
		return nil, errReceiptsChain
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	svc, err := receipts.NewChained(r.entries, r.keys, now, r.chain, r.accounts)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &composedReceipts{svc: svc, chain: r.chain, entries: r.entries, keys: r.keys, now: now, holder: hex.EncodeToString(id)}, nil
}

// chainGrouped says whether c keeps the chain's records' commits grouped as
// receipts.CommitGroup says (ADR 0037, "Where a link is kept"): commits of
// the anchors bucket, or in memory, whose Group is the chain's, or a
// wrapper of such (Unwrap). Anything else would read a link's commits
// where they are not, one by one.
func chainGrouped(c store.Commits) bool {
	var group func(string) (string, string, bool)
	switch c := c.(type) {
	case *store.S3Commits:
		group = c.Group
	case *store.MemoryCommits:
		group = c.Group
	case interface{ Unwrap() store.Commits }:
		return chainGrouped(c.Unwrap())
	}
	if group == nil {
		return false
	}
	for _, name := range []string{
		receipts.LinkName(probeAccount, 1), receipts.CatalogueName(1), receipts.AuditName(1), receipts.IncidentName(1),
	} {
		g, m, ok := group(name)
		wg, wm, _ := receipts.CommitGroup(name)
		if !ok || g != wg || m != wm {
			return false
		}
	}
	_, _, ok := group("tls-acme-v02.api.letsencrypt.org")
	return !ok
}

// probeAccount is an account's identifier that names nobody, for
// chainGrouped's check.
const probeAccount = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

// receiptsTicks, when set, tick runReceipts' loops in place of tickers of
// auditRetry and incidentsEvery: a test's. The image sets neither.
type receiptsTicks struct {
	audit, incidents <-chan time.Time
}

// runReceipts reads the record of incidents and the newest pass on their
// ticker, and runs the audit on its own, each once when it starts, until
// ctx ends. Errors are logged by their kind alone: never an account, a
// number, a digest or a count.
func runReceipts(ctx context.Context, r *composedReceipts, log *slog.Logger, ticks receiptsTicks) {
	go every(ctx, incidentsEvery, ticks.incidents, func() {
		if err := r.refresh(ctx); err != nil && ctx.Err() == nil {
			log.Warn("receipts: the record of incidents was not read", "kind", "incidents")
		}
	})
	go every(ctx, auditRetry, ticks.audit, func() {
		if err := r.auditOnce(ctx); err != nil && ctx.Err() == nil {
			log.Warn("receipts: a pass of the audit did not finish", "kind", "audit")
		}
	})
}

// refresh reads the record of incidents and the newest finished pass. What
// fails to be read keeps what was read before, with when, so /status shows
// it growing old and permaily-verify fails once it is.
func (r *composedReceipts) refresh(ctx context.Context) error {
	inc, ierr := r.svc.Incidents(ctx)
	pass, perr := r.svc.Audited(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ierr == nil {
		r.incidents = &inc
	}
	if perr == nil && pass != nil {
		a := pass.Audit
		r.audit = &a
	}
	return errors.Join(ierr, perr)
}

// auditOnce runs a pass of the audit when one is due and this enclave
// takes the lease. The lease only shares the work: two enclaves running
// at once, or a lease erased or forged, cost a pass twice, never a pass
// reported that was not made (ADR 0037, "The audit").
func (r *composedReceipts) auditOnce(ctx context.Context) error {
	newest, err := r.svc.Audited(ctx)
	if err != nil {
		return err
	}
	if newest != nil && r.now().Sub(newest.Audit.Began) < receipts.AuditEvery {
		return nil
	}
	lease, err := r.takeLease(ctx)
	if err != nil || lease == nil {
		return err
	}
	defer r.releaseLease(lease)
	pass, err := r.svc.Audit(ctx)
	if err != nil {
		return err
	}
	if pass != nil {
		return r.refresh(ctx)
	}
	return nil
}

// leaseDoc is a lease, sealed.
type leaseDoc struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
}

func leaseAAD() []byte { return envelope.AAD("entry", auditLeasePartition, auditLeaseKey) }

// takeLease takes the audit's lease and returns the entry written, nil
// when another enclave holds it: made at version 0, or taken over at the
// version read once it ended or when it does not open. Of two enclaves
// taking it at once, one is refused by the table (ErrExists, ErrConflict).
func (r *composedReceipts) takeLease(ctx context.Context) (*store.Entry, error) {
	key, err := r.keys.Derive(ctx, auditLeasePurpose)
	if err != nil {
		return nil, err
	}
	now := r.now()
	e, err := r.entries.GetEntry(ctx, auditLeasePartition, auditLeaseKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = &store.Entry{Partition: auditLeasePartition, Key: auditLeaseKey}
	case err != nil:
		return nil, errors.New("receipts: the audit's lease could not be read")
	default:
		var l leaseDoc
		if plain, oerr := envelope.OpenText(key, e.Value, leaseAAD()); oerr == nil && json.Unmarshal(plain, &l) == nil &&
			l.Holder != r.holder && l.Until.After(now) && l.Until.Sub(now) <= auditLease {
			return nil, nil
		}
	}
	raw, _ := json.Marshal(leaseDoc{Holder: r.holder, Until: now.Add(auditLease)})
	value, err := envelope.SealText(key, raw, leaseAAD())
	if err != nil {
		return nil, err
	}
	e.Value, e.UpdatedAt = value, now
	switch err := r.entries.PutEntry(ctx, e); {
	case errors.Is(err, store.ErrExists), errors.Is(err, store.ErrConflict):
		return nil, nil
	case err != nil:
		return nil, errors.New("receipts: the audit's lease could not be written")
	}
	return e, nil
}

// releaseLease removes the lease this enclave took, if nobody took it over,
// so the other enclave need not wait for its end to try again.
func (r *composedReceipts) releaseLease(e *store.Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = r.entries.DeleteEntryAt(ctx, e)
}

// status is /status's receipts (ADR 0037, "What /status and
// permaily-verify show"): sealed, chained by strict links, the record of
// incidents and the newest pass as last read, and what this enclave found
// and has not recorded yet, by every kind.
func (r *composedReceipts) status() attestation.ReceiptsStatus {
	st := attestation.ReceiptsStatus{
		Sealed: true, Chained: r.svc.Chained(), Broken: r.svc.Broken(),
		Decisions: "strict", DecisionsPerDay: inbox.DecisionsPerDay,
	}
	if st.Chained {
		st.Chain = attestation.ReceiptsChainStrict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.incidents != nil {
		inc := *r.incidents
		st.Incidents = &inc
	}
	if r.audit != nil {
		a := *r.audit
		a.Broken = make(map[string]int64, len(r.audit.Broken))
		for k, n := range r.audit.Broken {
			a.Broken[k] = n
		}
		st.Audit = &a
	}
	return st
}

// receiptRegressions are the chain's regressions in /status under the
// fixed name receiptRecord: never a record's name or version.
func receiptRegressions(chain *store.Anchored) []attestation.AnchorRegression {
	if chain == nil {
		return nil
	}
	var out []attestation.AnchorRegression
	for _, r := range chain.Regressions() {
		out = append(out, attestation.AnchorRegression{Name: receiptRecord, At: r.At, Fork: r.Fork, Unlocked: r.Unlocked, Lost: r.Lost})
	}
	return out
}
