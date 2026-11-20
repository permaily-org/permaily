package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// state is a connection's entry (PartitionState): what the queue keeps of
// its webhook, sealed and bound to where it is stored. It decides nothing:
// every call is admitted by Connections.Admit, every match rechecked.
type state struct {
	Connection string `json:"connection"`
	Account    string `json:"account"`
	// Host is the webhook's canonical host (the client_id's): its fair
	// share's queue.
	Host string `json:"host"`
	// Mailboxes are those its index entries name (PartitionMailbox), for a
	// webhook of new mail only.
	Mailboxes []string `json:"mailboxes,omitempty"`
	// InFlight is the notice in flight, frozen from its first attempt;
	// Waiting the generation behind it.
	InFlight *notice `json:"in_flight,omitempty"`
	Waiting  waiting `json:"waiting,omitzero"`
	Lease    lease   `json:"lease,omitzero"`
	// Progress is how far its new mail was evaluated.
	Progress Progress `json:"progress"`
	// LastRun is the latest scheduled run already turned into a notice (or
	// the connection's making), NextRun the one after it, and Coverage the
	// scheduled time of the last scheduled notice the vendor took
	// (webhook.Coverage at first).
	LastRun  time.Time `json:"last_run,omitzero"`
	NextRun  time.Time `json:"next_run,omitzero"`
	Coverage time.Time `json:"coverage,omitzero"`
	// LastTaken is when the vendor last took a notice (a 2xx);
	// FailingSince and Failure since when and why calls fail.
	LastTaken    time.Time                `json:"last_taken,omitzero"`
	FailingSince time.Time                `json:"failing_since,omitzero"`
	Failure      contracts.WebhookFailure `json:"failure,omitempty"`
	// Due is the minute of its due entry (zero: none).
	Due time.Time `json:"due,omitzero"`
}

// notice is a notice whose first attempt began: its identifier and body
// never change again.
type notice struct {
	ID           string                      `json:"id"`
	Type         contracts.WebhookNoticeType `json:"type"`
	Subscription string                      `json:"subscription"`
	Audience     string                      `json:"aud"`
	Body         []byte                      `json:"body"`
	CreatedAt    time.Time                   `json:"created_at"`
	ScheduledAt  time.Time                   `json:"scheduled_at,omitzero"`
	Match        *Match                      `json:"match,omitempty"`
	Calls        int                         `json:"calls"`
	NextAt       time.Time                   `json:"next_at"`
	// late is a scheduled candidate's, until its body says it.
	late bool
}

// waiting is the generation behind the notice in flight: a match of new
// mail (merged) and a scheduled run (the latest), never one notice.
type waiting struct {
	Match *Match    `json:"match,omitempty"`
	Run   time.Time `json:"run,omitzero"`
	Late  bool      `json:"late,omitempty"`
}

func (w waiting) empty() bool { return w.Match == nil && w.Run.IsZero() }

// lease is an enclave's hold on the entry while it works on it.
type lease struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
}

// route is what a due entry and a mailbox's index entry hold: where the
// connection's notices queue (its host and its account).
type route struct {
	Host    string `json:"host"`
	Account string `json:"account"`
}

// shard is the state partition of a connection: two hexadecimal digits of
// its identifier's hash, Shards of them.
func shard(connection string) string {
	h := sha256.Sum256([]byte(connection))
	return PartitionState + fmt.Sprintf("%02x", h[0]%Shards)
}

// minuteKey names a due minute's partition.
func minuteKey(t time.Time) string { return PartitionDue + t.UTC().Format("200601021504") }

// hostOf is a URL's host, the part between "https://" and the path, in
// lower case: the webhook's URL is on its client_id's host (webhook.URL).
func hostOf(raw string) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(raw, "https://"), "/")
	return strings.ToLower(host)
}

// idEncoding writes a notice's identifier: 16 random bytes in 26 base32
// characters.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func aad(partition, key string) []byte {
	return envelope.AAD("permaily webhook queue entry", partition, key)
}

// sealInto seals v as e's value, bound to its partition and key.
func sealInto(key []byte, e *store.Entry, v any) error {
	// Every value is one of this file's types, which always marshal.
	raw, _ := json.Marshal(v)
	var err error
	e.Value, err = envelope.SealText(key, raw, aad(e.Partition, e.Key))
	return err
}

// openFrom opens e's value into v: an entry moved to another partition or
// key, or written by anyone without the key, does not open.
func openFrom(key []byte, e *store.Entry, v any) error {
	raw, err := envelope.OpenText(key, e.Value, aad(e.Partition, e.Key))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// put writes v into e at the version it was read at (0: a new entry).
func (s *Service) put(ctx context.Context, key []byte, e *store.Entry, v any) error {
	if err := sealInto(key, e, v); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// later and sooner are the later and the sooner of two instants.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func sooner(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
