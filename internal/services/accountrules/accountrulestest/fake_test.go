package accountrulestest_test

import (
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
)

func TestTheFakeKeepsTheContract(t *testing.T) {
	f := &accountrulestest.Fake{Notice: "2026-11-20"}
	accountrulestest.Contract(t, accountrulestest.World{
		Source:  f,
		Save:    func(_ *testing.T, account string, rec *rules.Record) { f.Set(account, rec) },
		Break:   func(_ *testing.T, account string) { f.Fail(account, errors.New("forked")) },
		Client:  "https://claude.ai/oauth/mcp-oauth-client-metadata",
		Account: "acc_contract",
	})
}

func TestTheFakeKeepsTheRulesContract(t *testing.T) {
	f := &accountrulestest.Fake{Notice: "2026-11-20"}
	accountrulestest.RulesContract(t, accountrulestest.RulesWorld{
		Rules: f, Account: "acc_rules", Client: "https://claude.ai/oauth/mcp-oauth-client-metadata", Notice: "2026-11-20",
		FailReceipt: func(*testing.T) { f.FailNextReceipt = true },
		LoseNext:    func(*testing.T) { f.LoseNext = true },
		Receipts: func(*testing.T) int {
			n := 0
			for _, c := range f.Changes {
				if c.Account == "acc_rules" && c.Kind != "notice_agreed" {
					n++
				}
			}
			return n
		},
	})
}
