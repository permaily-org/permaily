package contracts

import (
	"encoding/json"
	"testing"
	"time"
)

// What the operator's tool and the admin panel are told of mailboxes (ADR
// 0026, pull request 4). The web app reads these names (web/src/lib/api.ts,
// "Admin: mailboxes").
func TestAdminMailboxShapes(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"an account's count", AccountRecord{ID: "acc_x", Providers: []string{}, Mailboxes: 3},
			`{"id":"acc_x","state":"","address":"","name":"","language":"","created_at":"0001-01-01T00:00:00Z","address_verified_at":"0001-01-01T00:00:00Z","passkeys":0,"passkey_only":false,"providers":[],"sessions":0,"mailboxes":3}`},
		{"the counts", Stats{Mailboxes: MailboxCounts{Total: 7}, Complete: true},
			`{"accounts":{"active":0,"closing":0,"blocked":0,"total":0},"invitations":{"unused":0},"mailboxes":{"total":7},"complete":true}`},
		{"a pause in the admin log", LogEntry{At: at, Kind: "mailbox_paused", Admin: LogAccount{ID: "acc_a"}, Account: &LogAccount{ID: "acc_b"}, Mailbox: "mbx_m"},
			`{"at":"2026-11-12T10:00:00Z","kind":"mailbox_paused","admin":{"id":"acc_a"},"account":{"id":"acc_b"},"mailbox":"mbx_m"}`},
		{"an entry of no mailbox", LogEntry{At: at, Kind: "block", Admin: LogAccount{ID: "acc_a"}},
			`{"at":"2026-11-12T10:00:00Z","kind":"block","admin":{"id":"acc_a"}}`},
		{"a mailbox's record, paused", MailboxRecord{ID: "mbx_m", Account: "acc_b", State: MailboxPaused, CreatedAt: at, RetentionDays: 90, PausedAt: at, Verified: true},
			`{"id":"mbx_m","account":"acc_b","state":"paused","created_at":"2026-11-12T10:00:00Z","retention_days":90,"paused_at":"2026-11-12T10:00:00Z","verified":true}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil || string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s (%v)", c.name, got, c.want, err)
		}
	}
}
