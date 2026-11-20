package connections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// *Service implements webhooks.Connections (ADR 0039's pull request 2):
// every answer is read from the connection's strict record (at most
// oauth.ConnectionFresh old in this enclave, at once after a change made
// through it), the account's standing, the model's gate and the account's
// rules; nothing of the queue decides anything here.

var (
	_ webhooks.Connections = webhookConnections{}
	_ WebhookAPI           = (*Service)(nil)
)

// WebhookConnections is the service as services/webhooks reads it. *Service
// has the methods but one: its Stop is the service's own (refusals.go), so
// the vendor's stop is StopWebhook, and this is the type that names it
// Stop.
func (s *Service) WebhookConnections() webhooks.Connections { return webhookConnections{s} }

type webhookConnections struct{ s *Service }

func (w webhookConnections) Subscription(ctx context.Context, id string) (*webhooks.Subscription, error) {
	return w.s.Subscription(ctx, id)
}

func (w webhookConnections) Admit(ctx context.Context, id string) (*webhooks.Subscription, webhooks.Permit, error) {
	return w.s.Admit(ctx, id)
}

func (w webhookConnections) Pending(ctx context.Context, id string, after webhooks.Progress) (*webhooks.Evaluation, error) {
	return w.s.Pending(ctx, id, after)
}

func (w webhookConnections) Recheck(ctx context.Context, id string, m webhooks.Match) (bool, error) {
	return w.s.Recheck(ctx, id, m)
}

func (w webhookConnections) Webhooks(ctx context.Context, after string, limit int) ([]string, error) {
	return w.s.Webhooks(ctx, after, limit)
}

func (w webhookConnections) Stop(ctx context.Context, id string) error { return w.s.StopWebhook(ctx, id) }

// WebhookStates is what the person's page shows of the queue's state of a
// connection's webhook (services/webhooks' States: the next run, the last
// notice taken, since when and why calls fail). It decides nothing; an
// error shows the webhook without them.
type WebhookStates interface {
	States(ctx context.Context, connection string) (*contracts.ConnectionWebhook, error)
}

// connFreshAt is connFresh with the record's mark.
func (s *Service) connFreshAt(ctx context.Context, id string) (*connDoc, store.Mark, time.Time, error) {
	s.mu.Lock()
	c, ok := s.conns[id]
	s.mu.Unlock()
	if ok && s.now().Sub(c.read) < oauthFresh && c.err == nil {
		return c.doc.clone(), c.at, c.read, nil
	}
	return s.readConnAt(ctx, id)
}

// webhookConn is a connection with a webhook, read as Subscription reads
// it: webhooks.ErrNoWebhook for one that has none or was removed.
func (s *Service) webhookConn(ctx context.Context, id string) (*connDoc, store.Mark, time.Time, error) {
	d, at, read, err := s.connFreshAt(ctx, id)
	if err != nil {
		return nil, at, read, err
	}
	if d == nil || d.Webhook == nil || d.Webhook.private == nil {
		return nil, at, read, webhooks.ErrNoWebhook
	}
	return d, at, read, nil
}

// ---- the seam with ADR 0040's pull request 5 ----

// webhookRules are the rules a connection's reads apply now, and the
// version of the account's record they were read at: through
// Config.AccountRules (accountrules.Source) when it is set, else the
// rules every connection applies (Config.Rules: the default rule, with
// the rules' basis zero, as an account with no record of rules).
//
// ADR 0040's pull request 5 makes the connections service read its rules
// through accountrules.Source for every read and adds Service.Disclosure,
// which builds a read's inbox.Disclosure in one place; once it is merged,
// this and webhookDisclosure become calls of it, so that delivery and a
// read can never apply different rules (ADR 0040, "The seam with
// delivery").
func (s *Service) webhookRules(ctx context.Context, d *connDoc) ([]rules.Rule, webhooks.Basis, error) {
	if s.cfg.AccountRules == nil {
		return s.cfg.Rules, webhooks.Basis{}, nil
	}
	a, err := s.cfg.AccountRules.Applying(ctx, d.Account, d.ClientID)
	if err != nil {
		return nil, webhooks.Basis{}, fmt.Errorf("%w: %w", webhooks.ErrRulesUnavailable, err)
	}
	return a.Rules, webhooks.Basis{RulesEpoch: a.Epoch, RulesVersion: a.Version}, nil
}

// webhookDisclosure is what an evaluation of a connection's new mail is
// given: the account's rules first, the connection's own formulas, the
// model where a read would ask it. No part is read: whether a message
// leaves does not depend on them. See webhookRules for ADR 0040's pull
// request 5.
func (s *Service) webhookDisclosure(ctx context.Context, d *connDoc, at store.Mark) (inbox.Disclosure, webhooks.Basis, error) {
	rs, basis, err := s.webhookRules(ctx, d)
	basis.Connection = at.Version()
	if err != nil {
		return inbox.Disclosure{}, basis, err
	}
	sc, err := d.scope()
	if err != nil {
		return inbox.Disclosure{}, basis, err
	}
	return inbox.Disclosure{Formulas: sc.list(), Rules: rs, Account: d.Account, Model: s.delivery(ctx, d) == contracts.DeliveryOn}, basis, nil
}

// ---- what a call rests on ----

// Subscription implements webhooks.Connections.
func (s *Service) Subscription(ctx context.Context, id string) (*webhooks.Subscription, error) {
	sub, _, err := s.subscription(ctx, id)
	return sub, err
}

func (s *Service) subscription(ctx context.Context, id string) (*webhooks.Subscription, *connDoc, error) {
	d, at, read, err := s.webhookConn(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	w := d.Webhook
	sub := &webhooks.Subscription{
		Connection: id, Account: d.Account, ID: w.private.Subscription, ClientID: d.ClientID, URL: w.private.URL, NewMail: w.NewMail,
		Mailboxes: slices.Clone(d.Mailboxes), Schedule: copySchedule(w.Schedule), TimeZone: w.private.TimeZone, MadeAt: d.MadeAt,
		State: w.State, Delivery: s.delivery(ctx, d), ReadAt: read,
	}
	_, basis, rulesErr := s.webhookRules(ctx, d)
	basis.Connection = at.Version()
	sub.Basis = basis
	switch {
	case !d.live(s.now()) || s.revokedHere(id, ""):
		sub.Why = webhooks.SkipEnded
	default:
		st, stRead, err := s.standing(ctx, d.Account, false)
		if err != nil {
			return nil, nil, err
		}
		if stRead.Before(sub.ReadAt) {
			sub.ReadAt = stRead
		}
		switch {
		case st != StandingOpen && st != StandingNotAgreed:
			sub.Why = webhooks.SkipEnded
		case s.callableDevices(d) == 0:
			sub.Why = webhooks.SkipNoDevice
		case w.State != contracts.WebhookOn || s.offHere(id):
			sub.Why = webhooks.SkipOff
		case sub.Delivery != contracts.DeliveryOn:
			sub.Why = webhooks.SkipModel
		case rulesErr != nil:
			sub.Why = webhooks.SkipRules
		}
	}
	sub.Calls = sub.Why == ""
	return sub, d, nil
}

// callableDevices counts the devices that could fetch after a notice:
// active, not revoked here, and either exchanged or with a code that may
// still be.
func (s *Service) callableDevices(d *connDoc) int {
	now, n := s.now(), 0
	for _, dev := range d.Devices {
		if dev.State != contracts.ConnectionActive || s.revokedHere(d.ID, dev.ID) {
			continue
		}
		if dev.Code.Status != oauth.CodeExchanged && !now.Before(dev.Code.ExpiresAt) {
			continue
		}
		n++
	}
	return n
}

// admitTries bounds the reads of one Admit that a stop made meanwhile
// sends back to read again.
const admitTries = 3

// errAdmitBusy is an admission that kept meeting stops.
var errAdmitBusy error = &kind{"connections: the webhook kept changing"}

// Admit implements webhooks.Connections: the permit is registered only if
// no stop came through this enclave since the read began, so a stop that
// returned cut every permit a call could start with.
func (s *Service) Admit(ctx context.Context, id string) (*webhooks.Subscription, webhooks.Permit, error) {
	for range admitTries {
		gen := s.permitGen(id)
		sub, d, err := s.subscription(ctx, id)
		if err != nil || !sub.Calls {
			return sub, nil, err
		}
		if p, ok := s.registerPermit(id, gen, webhookWriteBy(sub.ReadAt, d.EndsAt)); ok {
			return sub, p, nil
		}
	}
	return nil, nil, errAdmitBusy
}

// ---- stops ----

// StopWebhook is webhooks.Connections' Stop: the vendor's 410, written at
// the version read, its permits cut first. A connection replaced since is
// another one: nothing of the new one changes.
func (s *Service) StopWebhook(ctx context.Context, id string) error {
	return s.turnOff(ctx, id, contracts.WebhookStopped, nil, nil)
}

// WebhookOff implements WebhookAPI.
func (s *Service) WebhookOff(ctx context.Context, p Person, id string, version uint64) (*contracts.Connection, error) {
	d, at, err := s.owned(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if d.Webhook == nil || d.Webhook.private == nil {
		return nil, contracts.ErrNotFound
	}
	if at.Version() != version {
		return nil, contracts.ErrConsentChanged
	}
	if err := s.turnOff(ctx, id, contracts.WebhookOff, &at, func(wctx context.Context, d *connDoc) error {
		return s.webhookOffReceipt(wctx, d)
	}); err != nil {
		return nil, err
	}
	s.count("webhook_off")
	return s.Get(ctx, p, id)
}

// turnOff writes a webhook off or stopped: marked in memory and its
// permits cut before the record is written, so no call starts writing
// after it returns. With at, only at that version (contracts
// .ErrConsentChanged otherwise, the mark taken back); without, at the
// version read, read again when another write won. A webhook already off
// or stopped stays as it is. receipt, when set, is written before the
// record, as consent is.
func (s *Service) turnOff(ctx context.Context, id string, state contracts.WebhookState, at *store.Mark, receipt func(context.Context, *connDoc) error) error {
	undo := s.markOff(id)
	for range RecordTries {
		d, mark, err := s.readConn(ctx, id)
		if err != nil {
			undo()
			return err
		}
		if d == nil || d.Webhook == nil {
			undo()
			return webhooks.ErrNoWebhook
		}
		if at != nil && mark.Version() != at.Version() {
			undo()
			return contracts.ErrConsentChanged
		}
		if d.Webhook.State != contracts.WebhookOn {
			return nil
		}
		d.Webhook.State = state
		if receipt != nil {
			if err := receipt(ctx, d); err != nil {
				undo()
				return err
			}
		}
		err = s.writeConn(ctx, d, mark, false)
		if retry(err) && at == nil {
			continue
		}
		if retry(err) {
			undo()
			return contracts.ErrConsentChanged
		}
		if err != nil {
			return err
		}
		// What an admission registered while the record was being written
		// is cut too: the mark refuses every later one.
		s.cutPermits(id)
		if s.cfg.Webhooks != nil {
			s.cfg.Webhooks.Ended(id)
		}
		return nil
	}
	return errBusy
}

// webhookOffReceipt records the person turning a webhook off, as consent.
func (s *Service) webhookOffReceipt(ctx context.Context, d *connDoc) error {
	sc, _ := d.scope()
	r := &receipts.Receipt{
		V: receipts.Version, Kind: receipts.KindConsent, Connection: d.ID, ClientID: d.ClientID, Vendor: d.Vendor, Resource: d.Resource,
		Formulas: consentedFormulas(sc), Rules: s.ruleRefs(),
		Consent: &receipts.Consent{
			Allowed: true, Sentences: d.private.Shown.Sentences, Parts: d.Parts, Since: d.private.Since, EndsAt: d.EndsAt,
			Mailboxes: d.Mailboxes, ModelNotice: d.ModelNotice,
			Webhook: &receipts.ConsentWebhook{
				Host: webhookHost(d.Webhook.private.URL), NewMail: d.Webhook.NewMail, Schedule: copySchedule(d.Webhook.Schedule),
				TimeZone: d.Webhook.private.TimeZone, State: contracts.WebhookOff,
			},
		},
	}
	return s.writeReceipt(ctx, d.Account, r, time.Time{})
}

// subscribe tells the queue of a connection just made with a webhook,
// after its record and before its code: an error fails the approval.
func (s *Service) subscribe(ctx context.Context, d *connDoc) error {
	if d.Webhook == nil {
		return nil
	}
	w := d.Webhook
	return s.cfg.Webhooks.Subscribe(ctx, webhooks.Subscription{
		Connection: d.ID, Account: d.Account, ID: w.private.Subscription, ClientID: d.ClientID, URL: w.private.URL, NewMail: w.NewMail,
		Mailboxes: slices.Clone(d.Mailboxes), Schedule: copySchedule(w.Schedule), TimeZone: w.private.TimeZone, MadeAt: d.MadeAt,
		State: w.State, Calls: true, Delivery: s.delivery(ctx, d), ReadAt: s.now(),
	})
}

// webhookEnded hints the queue that a connection ended here.
func (s *Service) webhookEnded(id string) {
	if s.cfg.Webhooks != nil {
		s.cfg.Webhooks.Ended(id)
	}
}

// ---- every webhook, from the records ----

// webhooksListedFor is how long the listing Webhooks made for its first
// page serves the pages after it.
const webhooksListedFor = 10 * time.Minute

// Webhooks implements webhooks.Connections: every connection whose strict
// record holds a webhook, found from the connections' own records (the
// service's index of accounts, which the queue never writes, each
// account's strict list, each connection's strict record), in the order of
// their identifiers. The first page ("" after) lists them again; the
// pages after it are cut from that listing while it is younger than
// webhooksListedFor, and list again after.
func (s *Service) Webhooks(ctx context.Context, after string, limit int) ([]string, error) {
	if limit < 1 {
		return nil, nil
	}
	s.mu.Lock()
	all, read := s.hooks.listed, s.hooks.listedRead
	s.mu.Unlock()
	if after == "" || all == nil || s.now().Sub(read) >= webhooksListedFor {
		var err error
		if all, err = s.listWebhooks(ctx); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.hooks.listed, s.hooks.listedRead = all, s.now()
		s.mu.Unlock()
	}
	i, _ := slices.BinarySearch(all, after)
	if i < len(all) && all[i] == after {
		i++
	}
	return slices.Clone(all[i:min(len(all), i+limit)]), nil
}

// listWebhooks walks every account the service's index names.
func (s *Service) listWebhooks(ctx context.Context) ([]string, error) {
	out := []string{}
	after := ""
	for {
		entries, err := s.cfg.Entries.ListEntries(ctx, partitionAccounts, "", after, sweepPage)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			after = e.Key
			raw, err := envelope.Verified(s.signKey, e.Value, entryAAD(partitionAccounts, e.Key))
			if err != nil || string(raw) != strconv.Quote(e.Key) {
				continue // not written by this deployment
			}
			ids, err := s.accountWebhooks(ctx, e.Key)
			if err != nil {
				return nil, err
			}
			out = append(out, ids...)
		}
		if len(entries) < sweepPage {
			break
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// accountWebhooks are the connections of an account's list whose records
// hold a webhook. A record that cannot be read fails the listing: the
// repair tries again rather than drop a webhook it could not see.
func (s *Service) accountWebhooks(ctx context.Context, account string) ([]string, error) {
	l, _, err := s.readList(ctx, account)
	if err != nil || l == nil {
		return nil, err
	}
	var out []string
	for _, m := range l.Members {
		if m.Pending {
			continue
		}
		d, _, err := s.readConn(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		if d != nil && d.Account == account && d.Webhook != nil {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

// ---- new mail ----

// Pending implements webhooks.Connections: the connection's new mail after
// the progress, oldest first, each message evaluated once as a read of the
// connection would evaluate it (inbox.Discloser.Message with
// webhookDisclosure), at most webhook.ArrivalPage, the late sweep's first.
// Nothing is evaluated while the model cannot be asked for the account or
// its rules cannot be read.
func (s *Service) Pending(ctx context.Context, id string, after webhooks.Progress) (*webhooks.Evaluation, error) {
	d, at, _, err := s.webhookConn(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.delivery(ctx, d) != contracts.DeliveryOn {
		return nil, contracts.ErrModelUnavailable
	}
	disc, basis, err := s.webhookDisclosure(ctx, d, at)
	if err != nil {
		return nil, err
	}
	floor := after.Cursor.Received.Add(-webhook.ArrivalLag)
	if after.LateFrom.After(floor) {
		floor = after.LateFrom
	}
	boxes := map[string]inbox.Box{}
	var cands []webhooks.Cursor
	for _, mbx := range d.Mailboxes {
		box, err := s.cfg.Mailboxes.OpenForConnection(ctx, d.Account, mbx)
		if errors.Is(err, contracts.ErrNotFound) {
			continue // the mailbox was removed: nothing of it is told
		}
		if err != nil {
			return nil, err
		}
		boxes[mbx] = box
		found, err := s.arrivals(ctx, box, after, floor)
		if err != nil {
			return nil, err
		}
		cands = append(cands, found...)
	}
	slices.SortFunc(cands, webhooks.Cursor.Compare)
	more := len(cands) > webhook.ArrivalPage
	cands = cands[:min(len(cands), webhook.ArrivalPage)]
	through := after.Cursor
	recent := slices.Clone(after.Recent)
	var leave []webhooks.Cursor
	ask := s.asking(ctx, disc)
	for _, c := range cands {
		o, err := s.cfg.Discloser.Message(ask, boxes[c.Mailbox], disc, c.Message)
		switch {
		case errors.Is(err, contracts.ErrNotFound):
		case err != nil:
			return nil, err
		case o.Disclosed != nil:
			leave = append(leave, c)
		}
		if c.Compare(through) > 0 {
			through = c
		}
		recent = append(recent, c)
	}
	e := &webhooks.Evaluation{More: more, Through: webhooks.Progress{Cursor: through}}
	e.Through.Recent, e.Through.LateFrom = webhooks.Window(through, recent, after.LateFrom)
	if len(leave) > 0 {
		from := after
		from.Recent = slices.Clone(after.Recent)
		e.Match = &webhooks.Match{Basis: basis, From: from, Messages: leave[:min(len(leave), webhook.MatchMessagesMax)]}
	}
	return e, nil
}

// arrivalWindow is the first span arrivals reads at once, doubled while it
// finds too few; arrivalWalkMax bounds the messages one span reads, halved
// below it (never under a millisecond) when a burst fills it.
const (
	arrivalWindow  = time.Minute
	arrivalWalkMax = 1000
)

// everythingDisclosure lists a mailbox's messages without evaluating
// anything of a connection: every message leaves it, with no part read.
func everythingDisclosure(account string) inbox.Disclosure {
	return inbox.Disclosure{Formulas: []*query.Canonical{everything()}, Account: account}
}

// arrivals are a mailbox's messages a pass of Pending evaluates: received
// at or after floor, and either after the progress's cursor or within the
// late sweep and not in its Recent, oldest first, at most
// webhook.ArrivalPage + 1 (enough to say More). Messages are found by
// their identifiers, which begin with their received time
// (inbox.NewMessageID): spans of time from floor up, each read newest
// first below its end.
func (s *Service) arrivals(ctx context.Context, box inbox.Box, after webhooks.Progress, floor time.Time) ([]webhooks.Cursor, error) {
	list := everythingDisclosure("")
	if epoch := time.Unix(0, 0); floor.Before(epoch) {
		floor = epoch
	}
	end := s.now().Add(webhook.ArrivalLag)
	var out []webhooks.Cursor
	lo, span := floor, arrivalWindow
	for !lo.After(end) && len(out) <= webhook.ArrivalPage {
		hi := lo.Add(span)
		bound := arrivalWalkMax
		if span <= time.Millisecond {
			bound = 0 // a millisecond is never split: read it whole
		}
		got, full, err := s.arrivalSpan(ctx, box, list, lo, hi, bound)
		if err != nil {
			return nil, err
		}
		if full {
			span /= 2
			continue
		}
		for _, c := range got {
			if c.Compare(after.Cursor) > 0 || !slices.Contains(after.Recent, c) {
				out = append(out, c)
			}
		}
		lo, span = hi, span*2
	}
	return out, nil
}

// idBefore is the smallest message identifier of the instant t
// (inbox.NewMessageID): every message received before t is below it.
func idBefore(t time.Time) string {
	return fmt.Sprintf("%016x%016x", uint64(t.UnixNano()), 0) //nolint:gosec // after 1970
}

// arrivalSpan is a mailbox's messages received in [lo, hi), oldest first,
// and whether the span held more than bound (0: no bound).
func (s *Service) arrivalSpan(ctx context.Context, box inbox.Box, list inbox.Disclosure, lo, hi time.Time, bound int) ([]webhooks.Cursor, bool, error) {
	var got []webhooks.Cursor
	before := idBefore(hi)
	for {
		page, err := s.cfg.Discloser.Page(ctx, box, list, before, inbox.DiscloseMaxLimit)
		if err != nil {
			return nil, false, err
		}
		for _, it := range page.Items {
			if it.Disclosed == nil {
				continue
			}
			at := it.Disclosed.Message.ReceivedAt
			if at.Before(lo) {
				slices.Reverse(got)
				return got, false, nil
			}
			if !at.Before(hi) {
				continue
			}
			got = append(got, webhooks.Cursor{Received: at, Mailbox: box.MailboxID, Message: it.Disclosed.ID})
			if bound > 0 && len(got) > bound {
				return nil, true, nil
			}
		}
		if page.Next == "" {
			break
		}
		before = page.Next
	}
	slices.Reverse(got)
	return got, false, nil
}

// Recheck implements webhooks.Connections.
func (s *Service) Recheck(ctx context.Context, id string, m webhooks.Match) (bool, error) {
	d, at, _, err := s.webhookConn(ctx, id)
	if err != nil {
		return false, err
	}
	_, basis, err := s.webhookRules(ctx, d)
	if err != nil {
		return false, err
	}
	basis.Connection = at.Version()
	if basis != m.Basis {
		return false, nil
	}
	list := everythingDisclosure(d.Account)
	for _, c := range m.Messages {
		if !slices.Contains(d.Mailboxes, c.Mailbox) {
			continue
		}
		box, err := s.cfg.Mailboxes.OpenForConnection(ctx, d.Account, c.Mailbox)
		if errors.Is(err, contracts.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		o, err := s.cfg.Discloser.Message(ctx, box, list, c.Message)
		if errors.Is(err, contracts.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if o.Disclosed != nil {
			return true, nil
		}
	}
	return false, nil
}
