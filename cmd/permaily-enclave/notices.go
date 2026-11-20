package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// noticesParts is what the queue of the notices about connections is
// composed of (ADR 0041, pull request 7).
type noticesParts struct {
	// entries keeps the queue and its marks (notices#queue, notices#sent),
	// signed with the root secret's key for notices.PurposeSign.
	entries notices.Storage
	keys    notices.Keys
	now     func() time.Time
	// log takes the kind and the outcome of what the queue does, never an
	// account, a key, an address or an error's words.
	log *slog.Logger
}

// composedNotices is the queue and the Deliverer it sends through: the
// accounts service, which is composed after the queue because it is given
// the queue (the sign-out notice) and then given to it (deliverBy).
type composedNotices struct {
	svc *notices.Service
	to  *lateDeliverer
}

// composeNotices is the queue of the notices about connections (ADR 0041,
// "The queue"): its entries and marks in the deployment's table, signed
// with a key of the root secret, words and sent by accounts' Deliver
// through accounts' Mailer (SES, behind the watch of ADR 0021), once
// deliverBy has given it accounts. Until then an attempt fails and is tried
// again, as any attempt that cannot send now.
func composeNotices(n noticesParts) (*composedNotices, error) {
	to := &lateDeliverer{}
	svc, err := notices.New(notices.Config{Entries: n.entries, Keys: n.keys, Deliverer: to, Now: n.now, Log: n.log})
	if err != nil {
		return nil, err
	}
	return &composedNotices{svc: svc, to: to}, nil
}

// deliverBy makes a the queue's Deliverer. The composition calls it once,
// right after a is made with the queue.
func (c *composedNotices) deliverBy(a *accounts.Service) { c.to.a.Store(a) }

// status is /status's mail.notices: this enclave's counts (ADR 0041, "What
// /status and permaily-verify show").
func (c *composedNotices) status() *attestation.NoticesStatus {
	st := c.svc.Status()
	return &st
}

// runNotices runs the queue's runner in every enclave, a pass every
// notices.RunEvery, until ctx ends. The attempts under way then go on
// until stopNotices waits for them.
func runNotices(ctx context.Context, c *composedNotices) { go c.svc.Run(ctx) }

// noticesStopMax bounds a stop's wait for the notices' attempts under way:
// shorter than stopMax, and waited for while the server and the
// connections stop, so the stop keeps its budget.
const noticesStopMax = 15 * time.Second

// stopNotices ends the queue's work at a stop (the code review of ADR
// 0041's pull request 7, 1): it admits no attempt more, and waits, within
// noticesStopMax, for the runner and for the attempts under way, so a send
// SES accepted is marked and not sent again. It returns an error, and logs
// it by its kind, when they did not finish: those are abandoned, and sent
// again once their leases end (at least once).
func stopNotices(c *composedNotices, log *slog.Logger) error {
	return stopNoticesWithin(c, log, noticesStopMax)
}

func stopNoticesWithin(c *composedNotices, log *slog.Logger, within time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	if c.svc.Stop(ctx) != nil {
		log.Error("notices: the attempts under way did not finish at the stop", "kind", "notices")
		return errors.New("notices: the attempts under way did not finish at the stop")
	}
	return nil
}

// errNoDeliverer is an attempt made before accounts were given to the
// queue: tried again, as one that could not send now.
var errNoDeliverer = errors.New("the queue of notices has no Deliverer yet")

// lateDeliverer is accounts' Deliver once it is given (deliverBy).
type lateDeliverer struct {
	a atomic.Pointer[accounts.Service]
}

var _ notices.Deliverer = (*lateDeliverer)(nil)

// Deliver implements notices.Deliverer.
func (d *lateDeliverer) Deliver(ctx context.Context, n notices.Notice) error {
	a := d.a.Load()
	if a == nil {
		return errNoDeliverer
	}
	return a.Deliver(ctx, n)
}
