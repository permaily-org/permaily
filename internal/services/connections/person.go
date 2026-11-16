package connections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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

// Renew implements API: the end set to now plus the connection's days,
// with a fresh proof, under the notice in force, while it is active and
// before its end, at the version the page showed.
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
	now := s.now()
	if !d.live(now) {
		return nil, contracts.ErrConnectionEnded
	}
	if at.Version() != in.Version {
		return nil, contracts.ErrConsentChanged
	}
	if !now.Before(p.ConfirmedUntil) {
		return nil, contracts.ErrConfirmationNeeded
	}
	d.EndsAt, d.ModelNotice = endsAt(now, d.Days), s.cfg.ModelNotice
	if err := s.writeConn(ctx, d, at, false); err != nil {
		if retry(err) {
			return nil, contracts.ErrConsentChanged
		}
		return nil, err
	}
	sc, _ := d.scope()
	r := &receipts.Receipt{
		V: receipts.Version, Kind: receipts.KindConsent, Connection: d.ID, ClientID: d.ClientID, Vendor: d.Vendor, Resource: d.Resource,
		Formulas: consentedFormulas(sc), Rules: s.ruleRefs(),
		Consent: &receipts.Consent{Allowed: true, Renewal: true, Sentences: d.private.Shown.Sentences, Parts: d.Parts, Since: d.private.Since,
			EndsAt: d.EndsAt, Mailboxes: d.Mailboxes, ModelNotice: d.ModelNotice},
	}
	if err := s.writeReceipt(ctx, d.Account, r, time.Time{}); err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id)
}

// ---- ending ----

// revoke notes a connection (device "") or a device revoked in this
// enclave's memory and cuts every lease of it: from now nothing more of it
// leaves this enclave, also when writing the record then fails.
func (s *Service) revoke(connection, device string) {
	s.mu.Lock()
	key := connection
	if device != "" {
		key = connection + " " + device
	}
	s.revoked[key] = true
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
}

// revokedHere says this enclave revoked the connection or the device.
func (s *Service) revokedHere(connection, device string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[connection] || s.revoked[connection+" "+device]
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
			s.dropEnded(ctx, l, l.End(id, now))
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
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
	s.revoke(id, device)
	for range RecordTries {
		d, at, err := s.readConn(ctx, id)
		if err != nil || d == nil {
			if err != nil && !refusedRecord(err) {
				return err
			}
			return nil
		}
		dev := d.device(device)
		if dev == nil || dev.State != contracts.ConnectionActive {
			return s.endFamily(ctx, device, reason)
		}
		now := s.now()
		dev.State, dev.EndedAt, dev.EndReason = contracts.ConnectionEnded, now, reason
		drop := d.tidy(now)
		for _, f := range drop {
			if f != device {
				if err := s.removeFamily(ctx, f); err != nil {
					return err
				}
			}
		}
		d.dropDevices(slices.DeleteFunc(drop, func(f string) bool { return f == device }))
		if err := s.writeConn(ctx, d, at, false); retry(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := s.endFamily(ctx, device, reason); err != nil {
			return err
		}
		s.endedReceipt(ctx, d, device, reason, "")
		return nil
	}
	return errBusy
}

// dropEnded removes the records and families of the ended members the
// list drops past ListEndedMax, then their entries: the record first, so
// nothing is left that the list no longer names.
func (s *Service) dropEnded(ctx context.Context, l *ListState, remove []string) {
	var gone []string
	for _, id := range remove {
		if err := s.removeConn(ctx, id); err == nil {
			gone = append(gone, id)
		}
	}
	l.RemoveEnded(gone)
}

// removeConn removes an ended connection's record with its families'.
func (s *Service) removeConn(ctx context.Context, id string) error {
	d, _, err := s.readConn(ctx, id)
	if err != nil && !refusedRecord(err) {
		return err
	}
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
