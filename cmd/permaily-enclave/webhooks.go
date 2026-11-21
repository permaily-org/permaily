package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Connections' webhooks (ADR 0039, pull request 6): the queue and the
// scheduler over the deployment's table, the sender over the vendor egress,
// the signing key derived from the root secret, the connections service as
// what decides whether a notice may be sent, and the mailboxes telling it
// of every message stored.

// webhooksParts is what the webhooks are composed of.
type webhooksParts struct {
	// entries keeps the queue (webhooks.Partition*), sealed with a key of
	// the root secret.
	entries store.Entries
	// keys derives the queue's sealing key and the signing key
	// (webhook.KeyPurpose) from the root secret: both enclaves and every
	// release that receives the secret sign with the same key, so a key
	// changes only by a rotation (ADR 0039, "Rotation": a release that
	// signs with both keys for at least 30 days, then one that signs with
	// the new one alone and still publishes the old one for 30 more).
	keys webhooks.Keys
	// sender calls a webhook: internal/webhook/send over
	// enclave.WebhookEgressDialer(vendorEgressPort) in the image, a played
	// vendor's in dev mode, and nothing else.
	sender webhook.Sender
	// keysURL is where the api host publishes the key set, which /status
	// names (webhook.KeysPath on the api host).
	keysURL string
	now     func() time.Time
	// log takes kinds and outcomes, a vendor's host only when it is on the
	// verified list (webhooks.WithLog).
	log *slog.Logger
}

// composedWebhooks is the queue, and the connections it reads, given once
// they are made (readFrom): the connections service is given the queue
// (Subscribe, Ended) and then given to it.
type composedWebhooks struct {
	svc   *webhooks.Service
	conns *lateConnections
	// done is closed when the runner returned (runWebhooks).
	done chan struct{}
}

var errWebhooksParts = errors.New("the webhooks need the table, the root secret's keys and a sender")

// composeWebhooks is the queue and the scheduler of connections' webhooks
// (ADR 0039, "The queue"): its entries in the deployment's table, sealed
// and signed with keys of the root secret, calls through sender, the
// verified vendors named in the log and every other "other".
func composeWebhooks(w webhooksParts) (*composedWebhooks, error) {
	if w.entries == nil || w.keys == nil || w.sender == nil {
		return nil, errWebhooksParts
	}
	late := &lateConnections{}
	var verified []string
	for _, v := range vendors.List() {
		verified = append(verified, v.Domain)
	}
	log := w.log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	svc, err := webhooks.New(webhooks.Config{Entries: w.entries, Keys: w.keys, Connections: late, Sender: w.sender, Now: w.now},
		webhooks.WithLog(log, verified...), webhooks.WithKeysURL(w.keysURL))
	if err != nil {
		return nil, err
	}
	return &composedWebhooks{svc: svc, conns: late, done: make(chan struct{})}, nil
}

// webhookKeysURL is the key set's address on the api host: what /status
// names and permaily-verify fetches.
func webhookKeysURL(hosts router.Hosts, port uint32) string {
	return oauthConfig(hosts, port).Issuer + webhook.KeysPath
}

// readFrom gives the queue the connections it reads. The composition calls
// it once, right after the connections service is made with the queue.
func (c *composedWebhooks) readFrom(svc *connections.Service) {
	c.conns.c.Store(&connectionsRef{svc.WebhookConnections()})
}

// status is /status's webhooks: what the image compiles in, its keys and
// this enclave's calls and skips (ADR 0039, "What /status and
// permaily-verify show").
func (c *composedWebhooks) status() *attestation.WebhooksStatus { return c.svc.Status() }

// runWebhooks runs the queue's workers and its hourly repair in every
// enclave (either may call a notice the other queued) until ctx ends;
// stopWebhooks waits for them.
func runWebhooks(ctx context.Context, c *composedWebhooks) {
	go func() {
		defer close(c.done)
		c.svc.Run(ctx)
	}()
}

// webhooksStopMax bounds a stop's wait for the webhooks' runner: a call
// under way ends with its context, at the latest at its
// webhook.AttemptTimeout, so the runner returns within it. Shorter than
// stopMax, and waited for while the server and the connections stop.
const webhooksStopMax = webhook.AttemptTimeout + 2*time.Second

// stopWebhooks waits, within webhooksStopMax, for the runner that
// runWebhooks started to return, once the stop ended its context. It
// returns an error, logged by its kind, when it did not: the notice under
// way is called again once its lease ends (at least once).
func stopWebhooks(c *composedWebhooks, log *slog.Logger) error {
	return stopWebhooksWithin(c, log, webhooksStopMax)
}

func stopWebhooksWithin(c *composedWebhooks, log *slog.Logger, within time.Duration) error {
	select {
	case <-c.done:
		return nil
	case <-time.After(within):
		log.Error("webhooks: the calls under way did not finish at the stop", "kind", "webhooks")
		return errors.New("webhooks: the calls under way did not finish at the stop")
	}
}

// errNoConnections is the queue read before the connections were given to
// it: nothing is called, and the entry is tried again at its next pass.
var errNoConnections = errors.New("the webhooks have no connections yet")

// lateConnections is the connections service as the queue reads it, once
// given (readFrom). Before, every read fails, so nothing is called.
type lateConnections struct {
	c atomic.Pointer[connectionsRef]
}

type connectionsRef struct{ webhooks.Connections }

var _ webhooks.Connections = (*lateConnections)(nil)

func (l *lateConnections) get() (webhooks.Connections, error) {
	r := l.c.Load()
	if r == nil {
		return nil, errNoConnections
	}
	return r.Connections, nil
}

func (l *lateConnections) Subscription(ctx context.Context, id string) (*webhooks.Subscription, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.Subscription(ctx, id)
}

func (l *lateConnections) Admit(ctx context.Context, id string) (*webhooks.Subscription, webhooks.Permit, error) {
	c, err := l.get()
	if err != nil {
		return nil, nil, err
	}
	return c.Admit(ctx, id)
}

func (l *lateConnections) Pending(ctx context.Context, id string, after webhooks.Progress) (*webhooks.Evaluation, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.Pending(ctx, id, after)
}

func (l *lateConnections) Recheck(ctx context.Context, id string, m webhooks.Match) (bool, error) {
	c, err := l.get()
	if err != nil {
		return false, err
	}
	return c.Recheck(ctx, id, m)
}

func (l *lateConnections) Webhooks(ctx context.Context, after string, limit int) ([]string, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.Webhooks(ctx, after, limit)
}

func (l *lateConnections) Stop(ctx context.Context, id string) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.Stop(ctx, id)
}
