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
