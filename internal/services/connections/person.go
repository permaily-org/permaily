package connections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The person's connections (ADR 0030, "Revocation and two enclaves"): the
// list, one connection, revoking a connection or a device, renewing.

func jsonSize(v any) (int, error) {
	b, err := json.Marshal(v)
	return len(b), err
}

// List implements API: the active connections and those ended in the last
// 30 days, from the account's list.
func (s *Service) List(ctx context.Context, p Person) (*contracts.ConnectionList, error) {
	l, _, err := s.readList(ctx, p.Account)
	if err != nil {
		return nil, err
	}
	out := &contracts.ConnectionList{Connections: []contracts.Connection{}}
	if l == nil {
		return out, nil
	}
	for _, m := range l.Members {
		if m.Pending {
			continue
		}
		d, at, err := s.readConn(ctx, m.ID)
		if err != nil && !refusedRecord(err) {
			return nil, err
		}
		if d == nil || d.Account != p.Account {
			continue
		}
		out.Connections = append(out.Connections, s.view(ctx, d, at.Version()))
	}
	return out, nil
}

// owned is one of the person's connections, read strictly and now.
func (s *Service) owned(ctx context.Context, p Person, id string) (*connDoc, store.Mark, error) {
	if !strings.HasPrefix(id, "con_") || len(id) != len("con_")+26 {
		return nil, store.Mark{}, contracts.ErrNotFound
	}
	d, at, err := s.readConn(ctx, id)
	if err != nil && !refusedRecord(err) {
		return nil, at, err
	}
	if d == nil || d.Account != p.Account {
		return nil, at, contracts.ErrNotFound
	}
	return d, at, nil
}

// Get implements API.
func (s *Service) Get(ctx context.Context, p Person, id string) (*contracts.Connection, error) {
	d, at, err := s.owned(ctx, p, id)
	if err != nil {
		return nil, err
	}
	v := s.view(ctx, d, at.Version())
	return &v, nil
}

// Revoke implements API: with no fresh proof, also under unagreed terms.
func (s *Service) Revoke(ctx context.Context, p Person, id string) (*contracts.Connection, error) {
	if _, _, err := s.owned(ctx, p, id); err != nil {
		return nil, err
	}
	if err := s.endConnection(ctx, id, contracts.EndedByPerson, "", false); err != nil {
		return nil, err
	}
	s.count("revoked_person")
	return s.Get(ctx, p, id)
}

// RevokeDevice implements API.
func (s *Service) RevokeDevice(ctx context.Context, p Person, id, device string) (*contracts.Connection, error) {
	d, _, err := s.owned(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if d.device(device) == nil {
		return nil, contracts.ErrNotFound
	}
	if !d.live(s.now()) {
		return nil, contracts.ErrConnectionEnded
	}
	if err := s.endDevice(ctx, id, device, contracts.EndedByPerson); err != nil {
		return nil, err
	}
	s.count("revoked_person")
	return s.Get(ctx, p, id)
}

// RenewWriteMax bounds a renewal's writes after its decision: its consent
// receipt and its one version-checked write of the connection's record. A
// renewal is decided only while its proof and the connection's existing end
// each have at least RenewWriteMax left (ADR 0030, "Changed while building
// pull request 3", renewal): so its writes, bounded by it, end before
// either passes, and a write that lands later anyway (the table's writer
// held it) applies only that decision.
const RenewWriteMax = 5 * time.Second

// Renew implements API: the end set to the decision's time plus the
// connection's days, with a fresh proof, under the notice in force, while
// it is active and before its end, at the version the page showed.
//
// Its decision is its last check, made with RenewWriteMax to spare on the
// proof and on the connection's existing end; the end it writes is
// absolute, worked out from that decision's time. Then come the consent
// receipt and one write of the record at the version the decision read,
// both under RenewWriteMax, and nothing after it is checked or undone
// (PR #136's third review, findings 1 and 2): whenever that write lands, it
// can apply only this decision, made with a fresh proof before the old end;
// a revocation or another renewal since moved the version, so it loses;
// and it never extends past the end the person approved. An uncertain
// outcome (a timeout) leaves the record as it was or as decided, never a
// third state.
func (s *Service) Renew(ctx context.Context, p Person, id string, in contracts.ConnectionRenewal) (*contracts.Connection, error) {
	if !s.now().Before(p.ConfirmedUntil) {
		return nil, contracts.ErrConfirmationNeeded
	}
	if err := s.openForApproval(ctx, p.Account); err != nil {
		return nil, err
	}
	if in.ModelNotice != s.cfg.ModelNotice {
		return nil, contracts.ErrModelNoticeChanged
	}
	d, at, err := s.owned(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !d.live(s.now()) {
		return nil, contracts.ErrConnectionEnded
	}
	if at.Version() != in.Version {
		return nil, contracts.ErrConsentChanged
	}
	// The rules the renewal's receipt names: those its client's reads apply
	// now (ADR 0040), read before the decision; none read, no renewal.
	ap, err := s.rulesFor(ctx, p.Account, d.ClientID)
	if err != nil {
		return nil, err
	}
	// The decision.
	now := s.now()
	switch {
	case p.ConfirmedUntil.Sub(now) < RenewWriteMax:
		return nil, contracts.ErrConfirmationNeeded
	case d.EndsAt.Sub(now) < RenewWriteMax:
		return nil, contracts.ErrConnectionEnded
	}
	wctx, cancel := context.WithTimeout(ctx, RenewWriteMax)
	defer cancel()
	d.EndsAt, d.ModelNotice = endsAt(now, d.Days), s.cfg.ModelNotice
	// A renewal is consent: it is recorded before it is made, and made only
	// at the version the decision read (finding 6). A write that loses then
	// leaves a receipt of a renewal that did not happen, which says more
	// than happened; never one made and not recorded.
	sc, _ := d.scope()
	r := &receipts.Receipt{
		V: receipts.Version, Kind: receipts.KindConsent, Connection: d.ID, ClientID: d.ClientID, Vendor: d.Vendor, Resource: d.Resource,
		Formulas: consentedFormulas(sc), Rules: refsOf(ap.Rules),
		Consent: &receipts.Consent{Allowed: true, Renewal: true, Sentences: d.private.Shown.Sentences, Parts: d.Parts, Since: d.private.Since,
			EndsAt: d.EndsAt, Mailboxes: d.Mailboxes, ModelNotice: d.ModelNotice},
	}
	if err := s.writeReceipt(wctx, d.Account, r, time.Time{}); err != nil {
		return nil, err
	}
	if err := s.writeConn(wctx, d, at, false); err != nil {
		if retry(err) {
			return nil, contracts.ErrConsentChanged
		}
		return nil, err
	}
	// Written: told of with its new end, under its consent receipt (ADR
	// 0041, E31). A write that lost or whose answer was lost tells nothing.
	s.notifyRenewed(ctx, d, r.ID)
	return s.Get(ctx, p, id)
}

// ---- ending ----

// revoke notes a connection (device "") or a device revoked in this
// enclave's memory and cuts every lease of it: from now nothing more of it
// leaves this enclave, also when writing the record then fails.
func (s *Service) revoke(connection, device string) {
	s.revokeOnce(connection, device)
}

// revokeOnce is revoke, and answers what takes back this one mark (and only
// it: another revocation of the same connection stands), for an end that
// was refused by its record's version before it was written.
func (s *Service) revokeOnce(connection, device string) (undo func()) {
	s.mu.Lock()
	key := connection
	if device != "" {
		key = connection + " " + device
	}
	s.revoked[key]++
	undo = sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.revoked[key]--; s.revoked[key] <= 0 {
			delete(s.revoked, key)
		}
	})
	delete(s.conns, connection)
	var cut []*lease
	for l := range s.leases[connection] {
		if device == "" || l.grant.Family == device {
			cut = append(cut, l)
		}
	}
	s.mu.Unlock()
	for _, l := range cut {
		l.cut()
	}
	return undo
}

// revokedHere says this enclave revoked the connection or the device.
func (s *Service) revokedHere(connection, device string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[connection] > 0 || s.revoked[connection+" "+device] > 0
}

// endConnection ends a connection: first in this enclave's memory and its
// leases, then its record at the version read, then its families, and, unless
// listed (the caller writes the list), its entry in the account's list. A
// connection whose record was never made (a pending one fenced) is made
// ended with CreateNew, so it can never be made active.
func (s *Service) endConnection(ctx context.Context, id string, reason contracts.EndReason, replacedBy string, listed bool) error {
	s.revoke(id, "")
	var ended *connDoc
	for i := 0; ; i++ {
		if i == RecordTries {
			return errBusy
		}
		d, at, err := s.readConn(ctx, id)
		if err != nil && !refusedRecord(err) {
			return err
		}
		if err != nil {
			return nil // refused already: lost, rolled back or forked
		}
		if d == nil {
			return nil // never made: fenced by fence
		}
		now := s.now()
		if !d.end(reason, replacedBy, now) {
			break
		}
		if err := s.writeConn(ctx, d, at, false); retry(err) {
			continue
		} else if err != nil {
			return err
		}
		ended = d
		break
	}
	if ended == nil {
		return nil
	}
	for _, dev := range ended.Devices {
		if err := s.endFamily(ctx, dev.ID, reason); err != nil {
			return err
		}
	}
	s.endedReceipt(ctx, ended, "", reason, replacedBy)
	if !listed {
		now := s.now()
		if _, err := s.updateList(ctx, ended.Account, func(l *ListState) error {
			s.dropEnded(ctx, l, l.End(id, now), nil)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// replaceConn ends the connection a replacement's screen showed, only at the
// version it showed (N5, finding 7): contracts.ErrConsentChanged, with
// nothing of it ended, when it is no longer that one. Like every end it is
// revoked in this enclave's memory before its record is written; a write
// that loses to another takes back only its own mark.
func (s *Service) replaceConn(ctx context.Context, id string, version uint64, by string) error {
	d, at, err := s.readConn(ctx, id)
	if err != nil && !refusedRecord(err) {
		return err
	}
	now := s.now()
	if err != nil || d == nil || at.Version() != version || !d.live(now) {
		return contracts.ErrConsentChanged
	}
	undo := s.revokeOnce(id, "")
	d.end(contracts.EndedByReplaced, by, now)
	if err := s.writeConn(ctx, d, at, false); err != nil {
		if retry(err) {
			undo()
			return contracts.ErrConsentChanged
		}
		return err
	}
	for _, dev := range d.Devices {
		if err := s.endFamily(ctx, dev.ID, contracts.EndedByReplaced); err != nil {
			return err
		}
	}
	s.endedReceipt(ctx, d, "", contracts.EndedByReplaced, by)
	return nil
}

// abandon ends what an approval refused at its commit had made of its own
// connection, and nothing else: its pending entry fenced (its record made
// ended, or ended if it was made) and ended in the list. Sweep fences what
// a failure here leaves.
func (s *Service) abandon(ctx context.Context, account, id, client string, madeAt time.Time, generation uint64) {
	ctx = context.WithoutCancel(ctx)
	m := ListMember{ID: id, ClientID: client, State: contracts.ConnectionActive, Pending: true, MadeAt: madeAt, Generation: generation}
	if err := s.fence(ctx, account, m, contracts.EndedByTime); err != nil {
		s.count("abandon_failed")
		return
	}
	now := s.now()
	if _, err := s.updateList(ctx, account, func(l *ListState) error {
		s.dropEnded(ctx, l, l.End(id, now), nil)
		return nil
	}); err != nil {
		s.count("abandon_failed")
	}
}

// fence makes a pending connection that was never made ended, with
// CreateNew, so that its maker cannot make it after; one that was made is
// ended.
func (s *Service) fence(ctx context.Context, account string, m ListMember, reason contracts.EndReason) error {
	now := s.now()
	stub := &connDoc{
		V: 1, ID: m.ID, Account: account, ClientID: m.ClientID, State: contracts.ConnectionEnded, MadeAt: m.MadeAt,
		EndsAt: now, EndedAt: now, EndReason: reason, Generation: m.Generation, Devices: []deviceDoc{}, private: &connPrivate{},
	}
	s.revoke(m.ID, "")
	err := s.writeConn(ctx, stub, store.Mark{}, true)
	if errors.Is(err, store.ErrExists) {
		return s.endConnection(ctx, m.ID, reason, "", true)
	}
	return err
}

// endDevice ends one device of a connection: in memory and its leases
// first, then the connection's record, then its family.
func (s *Service) endDevice(ctx context.Context, id, device string, reason contracts.EndReason) error {
	_, err := s.endDeviceHere(ctx, id, device, reason)
	return err
}

// endDeviceHere is endDevice, and answers the connection as written when
// this call's write ended the device (nil when it had ended before, or its
// connection is gone): what a device ended for reuse is told from.
func (s *Service) endDeviceHere(ctx context.Context, id, device string, reason contracts.EndReason) (*connDoc, error) {
	s.revoke(id, device)
	for range RecordTries {
		d, at, err := s.readConn(ctx, id)
		if err != nil || d == nil {
			if err != nil && !refusedRecord(err) {
				return nil, err
			}
			return nil, nil
		}
		dev := d.device(device)
		if dev == nil || dev.State != contracts.ConnectionActive {
			return nil, s.endFamily(ctx, device, reason)
		}
		now := s.now()
		dev.State, dev.EndedAt, dev.EndReason = contracts.ConnectionEnded, now, reason
		drop := d.tidy(now)
		for _, f := range drop {
			if f != device {
				if err := s.removeFamily(ctx, f); err != nil {
					return nil, err
				}
			}
		}
		d.dropDevices(slices.DeleteFunc(drop, func(f string) bool { return f == device }))
		if err := s.writeConn(ctx, d, at, false); retry(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := s.endFamily(ctx, device, reason); err != nil {
			return d, err
		}
		s.endedReceipt(ctx, d, device, reason, "")
		return d, nil
	}
	return nil, errBusy
}

// dropEnded removes the records and families of the ended members the
// list drops past ListEndedMax, then their entries: the record first, so
// nothing is left that the list no longer names.
//
// While the list is closing (the account's deletion asked, or its removal
// under way) a connection that cannot be read is kept (removeConnStrict):
// its families are known only from its record, and the account's removal
// must find them, or fail (second review of #152, finding 1). The oldest
// ended member that can be removed goes in its place, so the list stays
// within its bounds. One that fails otherwise stays, as before, until the
// list's next write. With note (the account's final removal, endAll),
// each one removed is noted first, with its families (removeConnStrict).
func (s *Service) dropEnded(ctx context.Context, l *ListState, remove []string, note func(id string) error) {
	var gone []string
	if !l.Closing {
		for _, id := range remove {
			if err := s.removeConn(ctx, id); err == nil {
				gone = append(gone, id)
			}
		}
		l.RemoveEnded(gone)
		return
	}
	var ended []ListMember
	for _, m := range l.Members {
		if m.State == contracts.ConnectionEnded {
			ended = append(ended, m)
		}
	}
	slices.SortStableFunc(ended, func(a, b ListMember) int { return a.EndedAt.Compare(b.EndedAt) })
	for _, m := range ended {
		if len(gone) == len(remove) {
			break
		}
		err := s.removeConnStrict(ctx, m.ID, note)
		if err == nil {
			gone = append(gone, m.ID)
		} else if !refusedRecord(err) {
			break
		}
	}
	l.RemoveEnded(gone)
}

// removeConn removes an ended connection's record with its families'. A
// connection that cannot be read is removed without them; only while its
// list is not closing (dropEnded, sweepAccount), when nothing says that
// what is left of the account is gone.
func (s *Service) removeConn(ctx context.Context, id string) error {
	d, _, err := s.readConn(ctx, id)
	if err != nil && !refusedRecord(err) {
		return err
	}
	return s.removeConnOf(ctx, id, d)
}

// removeConnStrict is removeConn for an account's removal (RemoveAll) and
// for every removal of a closing list's member (dropEnded, sweepAccount):
// a connection that cannot be read (lost, put back, not signed) fails it,
// since its families are known only from its record. One removed already,
// or never made (a pending one, whose name the removal fences), has none.
//
// With note (RemoveAll, and the trimming its EndAll does), the
// connection's identifier and its families' are noted first, for
// RemoveAgain to find their names once nothing else does.
func (s *Service) removeConnStrict(ctx context.Context, id string, note func(id string) error) error {
	d, _, err := s.readConn(ctx, id)
	if err != nil {
		return err
	}
	if note != nil {
		if err := note(id); err != nil {
			return err
		}
		if d != nil {
			for _, dev := range d.Devices {
				if err := note(dev.ID); err != nil {
					return err
				}
			}
		}
	}
	return s.removeConnOf(ctx, id, d)
}

// removeConnOf removes connection id, read as d (nil when it could not be
// read), with its families' records.
func (s *Service) removeConnOf(ctx context.Context, id string, d *connDoc) error {
	if d != nil {
		for _, dev := range d.Devices {
			if err := s.removeFamily(ctx, dev.ID); err != nil {
				return err
			}
		}
	}
	s.revoke(id, "")
	return s.cfg.Records.RemoveRecord(ctx, connName(id))
}

// endedReceipt records a connection or a device that ended. It is written
// after the end, and its failure does not undo it.
func (s *Service) endedReceipt(ctx context.Context, d *connDoc, device string, reason contracts.EndReason, replacedBy string) {
	r := &receipts.Receipt{
		V: receipts.Version, Kind: receipts.KindEnded, Connection: d.ID, Device: device, ClientID: d.ClientID, Vendor: d.Vendor,
		Resource: d.Resource, Ended: &receipts.Ended{Reason: reason, ReplacedBy: replacedBy},
	}
	if err := s.writeReceipt(ctx, d.Account, r, time.Time{}); err != nil {
		s.count("receipt_failed")
	}
}
