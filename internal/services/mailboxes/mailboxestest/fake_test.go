package mailboxestest

import (
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The fakes are what the pull requests built at the same time code against:
// they must fit the interfaces exactly, and Service must be both halves.
var (
	_ httpapi.MailboxesService = (*Fake)(nil)
	_ accounts.Mailboxes       = (*Fake)(nil)
	_ mailboxes.Accounts       = (*Accounts)(nil)
	_ mailboxes.Notifier       = (*Notices)(nil)
	_ httpapi.MailboxesService = Service(nil)
	_ accounts.Mailboxes       = Service(nil)
)

// The fake keeps the contract its real counterpart will be held to.
func TestTheFakeKeepsTheContract(t *testing.T) {
	Contract(t, FakeSubject)
}
