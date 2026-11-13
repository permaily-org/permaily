package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
)

// mailboxesDown fails an account's removal with what the storage's client
// returns: a transport error whose URL names a mailbox's record, or a
// message's object.
type mailboxesDown struct {
	*mailboxestest.Fake
	err error
}

func (m mailboxesDown) RemoveAccount(context.Context, string) error { return m.err }

// PR #91's review, finding 2: an account that is due is removed by an
// ordinary request; when its mailboxes' removal fails with a transport error
// whose URL holds a mailbox's or a message's identifier, the log line holds
// neither.
func TestReview2NoMailboxOrMessageIdentifierReachesTheLog(t *testing.T) {
	mailbox := "mbx_abcdefghijklmnopqrstuvwxyz"
	message := "0123456789abcdef0123456789abcdef"
	for name, err := range map[string]error{
		"a mailbox's record": fmt.Errorf("mailboxes: a mailbox's record: %w", &url.Error{Op: "Put", URL: "https://permaily-commits.s3.us-east-2.amazonaws.com/commits/mailbox-" + mailbox + "/00000000000000000002", Err: errors.New("connection reset by peer")}),
		"a message's object": fmt.Errorf("store: %w", &url.Error{Op: "Delete", URL: "https://permaily-mail.s3.us-east-2.amazonaws.com/" + mailbox + "/" + message + "?versionId=x", Err: errors.New("timeout")}),
		"a plain error":      fmt.Errorf("store: put mailbox-%s message %s for ana@example.org: down", mailbox, message),
	} {
		t.Run(name, func(t *testing.T) {
			a := newApp(t)
			a.svc.SetMailboxes(mailboxesDown{Fake: mailboxestest.New(), err: err})
			a.signIn("ana@example.org")
			if rec := a.call("POST", "/api/v1/account/delete", "", nil); rec.Code != 200 {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body)
			}
			a.ahead = domain.AccountClosing + 60e9
			// A sign-in started for its address looks it up: it is due, and
			// removed then; the removal fails and accounts logs it.
			a.cookie = nil
			a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, nil)
			a.svc.Wait()
			// The same error reaching the API's own log of internal errors.
			a.h = httpapi.AccountsAPI(broken{err: err}, slog.New(slog.NewTextHandler(a.logs, nil)))
			if rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, nil); rec.Code != 500 {
				t.Fatalf("%d", rec.Code)
			}
			logs := a.logs.String()
			if !strings.Contains(logs, "api: internal error") || !strings.Contains(logs, "accounts: looking an address up") {
				t.Fatalf("not logged:\n%s", logs)
			}
			for _, secret := range []string{mailbox, message, "ana@example.org", "abcdefghijklmnopqrstuvwxyz"} {
				if strings.Contains(logs, secret) {
					t.Fatalf("the log holds %q:\n%s", secret, logs)
				}
			}
		})
	}
	_ = accounts.Outbox{}
}
