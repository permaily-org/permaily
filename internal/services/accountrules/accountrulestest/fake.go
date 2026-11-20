// Package accountrulestest is a Source in memory for the services and
// adapters that apply the person's rules (ADR 0040), with no cache: a change
// is seen by the next call, which is the strictest a real Source can be.
package accountrulestest

import (
	"context"
	"fmt"
	"sync"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
)

// Fake keeps each account's record, and the notice in force. The zero value
// is ready, with every account the default alone.
type Fake struct {
	mu      sync.Mutex
	records map[string]*rules.Record
	fail    map[string]error
	version map[string]uint64
	// Notice is the rules' notice in force (domain.RulesNoticeVersion when
	// the service is built).
	Notice string
	// Calls counts Applying's calls, by account.
	Calls map[string]int
}

var _ accountrules.Source = (*Fake)(nil)

// Set makes rec the account's record (nil: none), at the next version.
func (f *Fake) Set(account string, rec *rules.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records, f.version = map[string]*rules.Record{}, map[string]uint64{}
	}
	f.records[account] = rec
	f.version[account]++
}

// Fail makes Applying fail for account with err (nil: works again).
func (f *Fake) Fail(account string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[account] = err
}

// Applying answers the account's record's rules for client.
func (f *Fake) Applying(ctx context.Context, account, client string) (accountrules.Applying, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Calls == nil {
		f.Calls = map[string]int{}
	}
	f.Calls[account]++
	if err := ctx.Err(); err != nil {
		return accountrules.Applying{}, fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
	}
	if err := f.fail[account]; err != nil {
		return accountrules.Applying{}, fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
	}
	rec := f.records[account]
	rs, err := rec.For(client, f.Notice)
	if err != nil {
		return accountrules.Applying{}, fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
	}
	a := accountrules.Applying{Rules: rs}
	if rec != nil {
		a.Epoch, a.Version = rec.Epoch, f.version[account]
	}
	return a, nil
}
