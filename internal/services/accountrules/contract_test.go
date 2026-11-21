package accountrules_test

import (
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
)

// The service keeps the contracts its fake keeps (ADR 0040, U3, U4, U5, U7,
// U29's one-enclave half, U30), on strict records in memory.

func TestTheServiceKeepsTheContract(t *testing.T) {
	w := accountrules.NewTestWorld(t)
	s := w.Enclave()
	accountrulestest.Contract(t, accountrulestest.World{
		Source:  s,
		Save:    func(t *testing.T, account string, rec *rules.Record) { accountrules.Put(t, s, account, rec) },
		Break:   w.Break,
		Client:  "https://claude.ai/oauth/claude-code-client-metadata",
		Account: "acc_contractcontractcontract",
	})
}

func TestTheServiceKeepsTheRulesContract(t *testing.T) {
	w := accountrules.NewTestWorld(t)
	w.Clock.Set(time.Now())
	s := w.Enclave()
	const account = "acc_rulesrulesrulesrulesrules"
	accountrulestest.RulesContract(t, accountrulestest.RulesWorld{
		Rules: s, Account: account, Client: "https://claude.ai/oauth/claude-code-client-metadata", Notice: "2026-11-20",
		FailReceipt: func(*testing.T) { w.Receipts.FailNext() },
		LoseNext:    func(*testing.T) { accountrules.LoseNext(s) },
		Receipts:    func(*testing.T) int { return w.Receipts.Count(account) },
	})
}
