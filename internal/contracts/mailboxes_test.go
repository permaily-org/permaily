package contracts

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

// The web app reads these names (web/src/lib/api.ts): a change here is a
// change of the contract, in ADR 0026 and there too.
func TestMailboxShapes(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"a mailbox waiting", Mailbox{
			ID: "mbx_x", Address: "k7m2q9@inbox.permaily.com", State: MailboxWaiting, CreatedAt: at,
			RetentionDays: 90, RetentionOptions: MailboxRetentionDays, Confirmations: []ForwardingConfirmation{},
		}, `{"id":"mbx_x","address":"k7m2q9@inbox.permaily.com","label":"","state":"waiting","created_at":"2026-11-12T10:00:00Z","retention_days":90,"retention_options":[7,30,90,180,365],"confirmations":[]}`},
		{"a mailbox receiving, verified", Mailbox{
			ID: "mbx_x", Address: "a@inbox.permaily.com", Label: "Work", State: MailboxReceiving, CreatedAt: at,
			RetentionDays: 30, RetentionOptions: MailboxRetentionDays, ReceivingSince: at,
			Source:        &MailboxSource{Provider: MailboxSourceGmail, Address: "me@gmail.com", VerifiedAt: at},
			Confirmations: []ForwardingConfirmation{{Code: "123456789", Requester: "me@gmail.com", ReceivedAt: at, Verified: true, Message: "m1"}},
		}, `{"id":"mbx_x","address":"a@inbox.permaily.com","label":"Work","state":"receiving","created_at":"2026-11-12T10:00:00Z","retention_days":30,"retention_options":[7,30,90,180,365],"receiving_since":"2026-11-12T10:00:00Z","source":{"provider":"gmail","address":"me@gmail.com","verified_at":"2026-11-12T10:00:00Z"},"confirmations":[{"code":"123456789","requester":"me@gmail.com","received_at":"2026-11-12T10:00:00Z","verified":true,"message":"m1"}]}`},
		{"a page of a search", MessagePage{
			Messages: []MessageSummary{{ID: "m1", ReceivedAt: at, ExpiresAt: at, Size: 10, From: []MailAddress{{Name: "Ann", Address: "ann@example.org"}}, Subject: "Hi", Snippet: "Hello", Attachments: 1}},
			Next:     "m1", SearchedTo: at,
		}, `{"messages":[{"id":"m1","received_at":"2026-11-12T10:00:00Z","expires_at":"2026-11-12T10:00:00Z","size":10,"from":[{"name":"Ann","address":"ann@example.org"}],"subject":"Hi","snippet":"Hello","attachments":1,"confirmation":false}],"next":"m1","searched_to":"2026-11-12T10:00:00Z"}`},
		{"the last page of a list", MessagePage{Messages: []MessageSummary{}}, `{"messages":[]}`},
		{"a message", Message{
			ID: "m1", ReceivedAt: at, ExpiresAt: at, Size: 10, From: []MailAddress{{Address: "a@example.org"}},
			To: []MailAddress{}, Cc: []MailAddress{}, ReplyTo: []MailAddress{}, Subject: "Hi", Date: "Thu, 12 Nov 2026 10:00:00 +0000",
			Text: "Hello", TextFrom: TextFromHTML, Attachments: []MessageAttachment{{Index: 0, Name: "a.pdf", Type: "application/pdf", Size: 3}},
			Auth: []AuthCheck{{Method: "dkim", Domain: "example.org", Result: "pass"}},
		}, `{"id":"m1","received_at":"2026-11-12T10:00:00Z","expires_at":"2026-11-12T10:00:00Z","size":10,"from":[{"address":"a@example.org"}],"to":[],"cc":[],"reply_to":[],"subject":"Hi","date":"Thu, 12 Nov 2026 10:00:00 +0000","text":"Hello","text_from":"html","text_truncated":false,"attachments":[{"index":0,"name":"a.pdf","type":"application/pdf","size":3}],"auth":[{"method":"dkim","domain":"example.org","result":"pass"}],"confirmation":false,"unparsed":false}`},
		{"what the operator sees", MailboxRecord{ID: "mbx_x", Account: "acc_y", State: MailboxPaused, CreatedAt: at, RetentionDays: 90, PausedAt: at},
			`{"id":"mbx_x","account":"acc_y","state":"paused","created_at":"2026-11-12T10:00:00Z","retention_days":90,"paused_at":"2026-11-12T10:00:00Z","verified":false}`},
	}
	for _, c := range cases {
		out, err := json.Marshal(c.v)
		if err != nil || string(out) != c.want {
			t.Errorf("%s:\n got %s %v\nwant %s", c.name, out, err, c.want)
		}
	}
}

// What the operator sees of a mailbox holds nothing that is the person's: no
// address, no label, no account at a provider.
func TestTheOperatorSeesNoAddressOfAMailbox(t *testing.T) {
	out, err := json.Marshal(MailboxRecord{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"address", "label", "source", "confirmations"} {
		if _, ok := fields[name]; ok {
			t.Errorf("a mailbox record has %q", name)
		}
	}
}

// A request that changes a mailbox leaves what it does not name as it is.
func TestMailboxUpdateNamesWhatChanges(t *testing.T) {
	var u MailboxUpdate
	if err := json.Unmarshal([]byte(`{"retention_days":30}`), &u); err != nil || u.Label != nil || u.RetentionDays == nil || *u.RetentionDays != 30 {
		t.Fatalf("%+v %v", u, err)
	}
	if err := json.Unmarshal([]byte(`{"label":""}`), &u); err != nil || u.Label == nil || *u.Label != "" {
		t.Fatalf("an empty label is a change: %+v %v", u, err)
	}
}

func TestMailboxErrors(t *testing.T) {
	codes := map[string]bool{}
	for _, e := range []*Error{ErrTooManyMailboxes, ErrInvalidLabel, ErrInvalidRetention, ErrInvalidQuery, ErrTooManyMessages, ErrMailboxMoved} {
		if codes[e.Code] {
			t.Errorf("%s is used twice", e.Code)
		}
		codes[e.Code] = true
		if errors.Is(e, ErrNotFound) {
			t.Errorf("%s is taken for not_found", e.Code)
		}
	}
	if ErrInvalidRetention.Code != "invalid_retention" {
		t.Error("the F1 API's code changed")
	}
	if !slices.Contains(MailboxRetentionDays, MailboxRetentionDefault) {
		t.Error("the default is not among the choices")
	}
}
