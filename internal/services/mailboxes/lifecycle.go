package mailboxes

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// ---- the account's standing ----

// standing reads the account's standing: contracts.ErrUnauthorized for a
// blocked account or one that is gone.
func (s *Service) standing(ctx context.Context, account string) (Standing, error) {
	if account == "" {
		return Standing{}, contracts.ErrUnauthorized
	}
	return s.cfg.Accounts.Standing(ctx, account)
}

// refuse says whether a standing allows a change (making a mailbox, its
// settings, deleting messages): account_closing first, then terms_needed.
func refuse(st Standing) error {
	if st.Closing {
		return contracts.ErrAccountClosing
	}
	if !st.TermsAgreed {
		return contracts.ErrTermsChanged
	}
	return nil
}

// allows is the table of Access.
func allows(st Standing, a Access) error {
	switch a {
	case AccessListMessages, AccessDownload:
		return nil
	case AccessSearch, AccessReadMessage:
		if !st.TermsAgreed {
			return contracts.ErrTermsChanged
		}
		return nil
	case AccessDeleteMessages:
		return refuse(st)
	}
	return contracts.ErrNotFound
}

// ---- reading a list (ADR 0026, "An identifier in the list") ----

// slot is an identifier of a list as a reader of it finds it.
type slot struct {
	entry listEntry
	l     *loaded
}

// settle does what any reader of a list does with each identifier in it
// (ADR 0026, "An identifier in the list"): a reservation older than
// ReservationAge is fenced by its tombstone, and an identifier whose record
// is disconnecting, cancelled or gone is taken out, its disconnect entry put
// back and its pointer deleted. It returns the slots that stay, which count,
// and whether the list must be written without the others.
func (s *Service) settle(ctx context.Context, list *listDoc) ([]slot, bool, error) {
	var kept []slot
	changed := false
	for _, e := range list.Mailboxes {
		l, err := s.load(ctx, e.ID)
		if err != nil {
			return nil, false, err
		}
		if l.doc != nil && l.doc.Account != list.Account {
			changed = true // not this account's: it does not stay here
			continue
		}
		if l.state == stNone && s.now().Sub(e.AddedAt) >= ReservationAge {
			// A reservation whose record never came: fence it. ErrExists
			// says a commit is there: the record was made, or removed.
			switch err := s.tombstone(ctx, e.ID, list.Account); {
			case err == nil:
				l.state = stCancelled
			case errors.Is(err, store.ErrExists):
				if l, err = s.load(ctx, e.ID); err != nil {
					return nil, false, err
				}
				if l.state == stNone {
					l.state = stCancelled // removed: gone
				}
			case errors.Is(err, store.ErrForked):
				l.state = stForked
			default:
				return nil, false, err
			}
		}
		if l.state.leaving() {
			if err := s.finish(ctx, l, e.Rcpt); err != nil {
				return nil, false, err
			}
			changed = true
			continue
		}
		kept = append(kept, slot{entry: e, l: l})
	}
	return kept, changed, nil
}

// finish does what is left of a mailbox that is leaving, but taking it out
// of its list: its disconnect entry is there, its pointer and its index entry
// are gone. Doing it twice does it once.
func (s *Service) finish(ctx context.Context, l *loaded, rcpt string) error {
	if l.state == stDisconnecting {
		if err := s.queue(ctx, l.doc.DisconnectedAt, l.id); err != nil {
			return err
		}
	}
	if l.doc != nil && l.doc.Rcpt != "" {
		rcpt = l.doc.Rcpt
	}
	if err := s.unpoint(ctx, rcpt, l.id); err != nil {
		return err
	}
	if l.state.leaving() {
		return s.unindex(ctx, l.id)
	}
	return nil
}

// settled reads an account's list and settles it, writing it back when
// identifiers left it (another enclave doing so first is as good). No list
// is an empty one.
func (s *Service) settled(ctx context.Context, account string) (*listDoc, []slot, error) {
	list, at, err := s.loadList(ctx, account)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	slots, changed, err := s.settle(ctx, list)
	if err != nil {
		return nil, nil, err
	}
	if changed {
		next := *list
		next.Mailboxes = entries(slots)
		if err := s.putList(ctx, &next, at); err != nil && !errors.Is(err, store.ErrChanged) {
			return nil, nil, err
		}
	}
	return list, slots, nil
}

func entries(slots []slot) []listEntry {
	out := make([]listEntry, 0, len(slots))
	for _, sl := range slots {
		out = append(out, sl.entry)
	}
	return out
}

// takeOut takes an identifier out of its account's list at the version read.
func (s *Service) takeOut(ctx context.Context, account, id string) error {
	for range maxTries {
		list, at, err := s.loadList(ctx, account)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := list.find(id); !ok {
			return nil
		}
		list.Mailboxes = list.without(id)
		if err := s.putList(ctx, list, at); !errors.Is(err, store.ErrChanged) {
			return err
		}
	}
	return errors.New("mailboxes: an account's list keeps changing")
}

// ---- what a person sees ----

// view is a mailbox as its person sees it.
func (s *Service) view(ctx context.Context, sl slot) (contracts.Mailbox, error) {
	if sl.l.state != stActive {
		return contracts.Mailbox{ID: sl.entry.ID, State: contracts.MailboxUnreadable, CreatedAt: sl.entry.AddedAt, RetentionOptions: []int{}, Confirmations: []contracts.ForwardingConfirmation{}}, nil
	}
	l := sl.l
	key, err := s.mailboxKey(ctx, l)
	if err != nil {
		return contracts.Mailbox{}, err
	}
	p, err := s.private(key, l)
	if err != nil {
		return contracts.Mailbox{}, err
	}
	set, err := s.readSettings(ctx, l.id, key)
	if err != nil {
		return contracts.Mailbox{}, err
	}
	m := contracts.Mailbox{
		ID: l.id, Address: p.Address, Label: set.label, State: stateOf(l.doc), CreatedAt: l.doc.CreatedAt,
		RetentionDays: set.retentionDays, RetentionOptions: slices.Clone(contracts.MailboxRetentionDays),
		ReceivingSince: l.doc.ReceivingSince, PausedAt: l.doc.PausedAt,
	}
	if p.Source != nil {
		m.Source = &contracts.MailboxSource{Provider: contracts.MailboxSourceGmail, Address: p.Source.Address, VerifiedAt: p.Source.VerifiedAt}
	}
	if m.Confirmations, err = s.confirmations(ctx, inbox.Box{MailboxID: l.id, Key: key}); err != nil {
		return contracts.Mailbox{}, err
	}
	return m, nil
}

func stateOf(d *mailboxDoc) contracts.MailboxState {
	switch {
	case !d.PausedAt.IsZero():
		return contracts.MailboxPaused
	case !d.ReceivingSince.IsZero():
		return contracts.MailboxReceiving
	}
	return contracts.MailboxWaiting
}

// maxConfirmations is how many forwarding confirmations a mailbox shows.
const maxConfirmations = 10

// confirmations are a mailbox's forwarding confirmations that have not
// ended, the newest first, ten at most, from one bounded scan.
func (s *Service) confirmations(ctx context.Context, b inbox.Box) ([]contracts.ForwardingConfirmation, error) {
	out := []contracts.ForwardingConfirmation{}
	page, err := s.cfg.Storage.ScanEntriesDown(ctx, inbox.Partition(b.MailboxID), inbox.KeyConfirm, "", store.MaxScanRead)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range page.Entries {
		if len(out) == maxConfirmations {
			break
		}
		e := &page.Entries[i]
		c := inbox.OpenConfirm(b, e, now)
		if c == nil {
			continue
		}
		f := contracts.ForwardingConfirmation{Code: c.Code, Requester: c.Requester, ReceivedAt: c.ReceivedAt, Verified: c.Verified, ForAnotherAddress: c.ForAnotherAddress, Message: e.Key[len(inbox.KeyConfirm):]}
		if c.ForAnotherAddress {
			f.Code = ""
		}
		out = append(out, f)
	}
	return out, nil
}

// List implements httpapi.MailboxesService.
func (s *Service) List(ctx context.Context, c Caller) (*contracts.MailboxList, error) {
	if _, err := s.standing(ctx, c.Account); err != nil {
		return nil, err
	}
	_, slots, err := s.settled(ctx, c.Account)
	if err != nil {
		return nil, err
	}
	out := &contracts.MailboxList{Mailboxes: []contracts.Mailbox{}, Limit: MaxMailboxes}
	for _, sl := range slots {
		if sl.l.state == stNone {
			continue // a reservation: nothing of it to show
		}
		m, err := s.view(ctx, sl)
		if err != nil {
			return nil, err
		}
		out.Mailboxes = append(out.Mailboxes, m)
	}
	return out, nil
}

// own is the caller's mailbox that is not leaving, as its slot: an active
// record of the caller's, or an unreadable one the caller's list names.
// contracts.ErrNotFound otherwise, the same for every reason.
func (s *Service) own(ctx context.Context, c Caller, id string) (slot, error) {
	if !validMailboxID(id) {
		return slot{}, contracts.ErrNotFound
	}
	l, err := s.load(ctx, id)
	if err != nil {
		return slot{}, err
	}
	switch {
	case l.state == stActive && l.doc.Account == c.Account:
		return slot{entry: listEntry{ID: id, Rcpt: l.doc.Rcpt, AddedAt: l.doc.CreatedAt}, l: l}, nil
	case l.state.unreadable():
		list, _, err := s.loadList(ctx, c.Account)
		if errors.Is(err, store.ErrNotFound) {
			return slot{}, contracts.ErrNotFound
		}
		if err != nil {
			return slot{}, err
		}
		if e, ok := list.find(id); ok {
			return slot{entry: e, l: l}, nil
		}
	}
	return slot{}, contracts.ErrNotFound
}

// Get implements httpapi.MailboxesService.
func (s *Service) Get(ctx context.Context, c Caller, id string) (*contracts.Mailbox, error) {
	if _, err := s.standing(ctx, c.Account); err != nil {
		return nil, err
	}
	sl, err := s.own(ctx, c, id)
	if err != nil {
		return nil, err
	}
	m, err := s.view(ctx, sl)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Open implements httpapi.MailboxesService.
func (s *Service) Open(ctx context.Context, c Caller, id string, a Access) (inbox.Box, error) {
	st, err := s.standing(ctx, c.Account)
	if err != nil {
		return inbox.Box{}, err
	}
	sl, err := s.own(ctx, c, id)
	if err != nil {
		return inbox.Box{}, err
	}
	if err := allows(st, a); err != nil {
		return inbox.Box{}, err
	}
	if sl.l.state != stActive {
		return inbox.Box{}, contracts.ErrMailboxUnreadable
	}
	key, err := s.mailboxKey(ctx, sl.l)
	if err != nil {
		return inbox.Box{}, err
	}
	return inbox.Box{MailboxID: id, Key: key}, nil
}

// CheckChange implements httpapi.MailboxesService: the checks Create and
// Update make before their input, in their order, reading and writing
// nothing else.
func (s *Service) CheckChange(ctx context.Context, c Caller, id string) error {
	st, err := s.standing(ctx, c.Account)
	if err != nil {
		return err
	}
	if id == "" {
		return refuse(st)
	}
	sl, err := s.own(ctx, c, id)
	if err != nil {
		return err
	}
	if err := refuse(st); err != nil {
		return err
	}
	if sl.l.state != stActive {
		return contracts.ErrMailboxUnreadable
	}
	return nil
}

// ---- making a mailbox (ADR 0026, "Making a mailbox") ----

// Create implements httpapi.MailboxesService.
func (s *Service) Create(ctx context.Context, c Caller, in contracts.MailboxCreate) (*contracts.Mailbox, error) {
	st, err := s.standing(ctx, c.Account)
	if err != nil {
		return nil, err
	}
	if err := refuse(st); err != nil {
		return nil, err
	}
	if !domain.MailboxLabel(in.Label) {
		return nil, contracts.ErrInvalidLabel
	}
	for range maxTries {
		// 1. The list at its version, made open and empty when there is none.
		list, at, err := s.loadList(ctx, c.Account)
		if errors.Is(err, store.ErrNotFound) {
			err = s.createList(ctx, &listDoc{Account: c.Account, State: listOpen})
			if err != nil && !errors.Is(err, store.ErrExists) {
				return nil, err
			}
			continue // read what is there now
		}
		if err != nil {
			return nil, err
		}
		if list.State != listOpen {
			return nil, contracts.ErrAccountClosing
		}
		slots, _, err := s.settle(ctx, list)
		if err != nil {
			return nil, err
		}
		now := s.now()
		recent := 0
		for _, t := range list.Made {
			if now.Sub(t) < Period {
				recent++
			}
		}
		if recent >= MaxMadePerPeriod {
			return nil, contracts.ErrTooManyRequests
		}
		if len(slots) >= MaxMailboxes {
			return nil, contracts.ErrTooManyMailboxes
		}
		// 2. The address and its HMAC.
		id := newMailboxID()
		address := domain.NewMailboxAddress(s.cfg.Domain)
		local, _ := domain.MailboxLocalPart(address, s.cfg.Domain)
		rcpt, err := s.rcptOf(ctx, local)
		if err != nil {
			return nil, err
		}
		// 3. The reservation, written at the version the count was made at.
		next := *list
		next.Mailboxes = append(entries(slots), listEntry{ID: id, Rcpt: rcpt, AddedAt: now.Truncate(time.Second)})
		next.Made = append(slices.Clone(list.Made), now)
		if len(next.Made) > MaxMadePerPeriod {
			next.Made = next.Made[len(next.Made)-MaxMadePerPeriod:]
		}
		if err := s.putList(ctx, &next, at); errors.Is(err, store.ErrChanged) {
			continue
		} else if err != nil {
			return nil, err
		}
		return s.make(ctx, c.Account, id, address, rcpt, in.Label, now)
	}
	return nil, errors.New("mailboxes: an account's list keeps changing")
}

// make does steps 4 to 7 of making a mailbox, its identifier reserved.
func (s *Service) make(ctx context.Context, account, id, address, rcpt, label string, now time.Time) (*contracts.Mailbox, error) {
	// 4. The pointer, create-only. Another mailbox's address: give the
	// reservation up.
	if err := s.putPointer(ctx, rcpt, id); err != nil {
		if errors.Is(err, store.ErrExists) {
			s.giveUp(ctx, account, id)
			return nil, errors.New("mailboxes: a new address is another mailbox's")
		}
		return nil, err
	}
	// 5. The record, made only if nothing was ever committed for its name: a
	// reader that cancelled the reservation meanwhile wrote its tombstone.
	dk, err := s.cfg.Keys.NewDataKey(ctx, keyContext(id))
	if err != nil {
		return nil, err
	}
	sealed, err := sealPrivate(dk.Plaintext, id, &privateDoc{Address: address})
	if err != nil {
		return nil, err
	}
	doc := &mailboxDoc{ID: id, Account: account, State: stateActive, Rcpt: rcpt, CreatedAt: now.Truncate(time.Second), Sealed: sealed}
	rec, err := s.signedRecord(ctx, doc, dk.Wrapped)
	if err != nil {
		return nil, err
	}
	if err := s.cfg.Records.CreateNew(ctx, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			_ = s.unpoint(ctx, rcpt, id)
		}
		return nil, err
	}
	// 6. The settings and the index entry. A failure leaves a mailbox with
	// the default settings, listed; the answer is an error.
	errSettings := s.writeSettings(ctx, id, dk.Plaintext, 0, contracts.MailboxRetentionDefault, label)
	errIndex := s.putSigned(ctx, indexPartition, id, indexDoc{Account: account})
	// 7. The notice.
	s.notify(ctx, account, NoticeAdded, now)
	if errSettings != nil && !errors.Is(errSettings, store.ErrExists) {
		return nil, errSettings
	}
	if errIndex != nil && !errors.Is(errIndex, store.ErrExists) {
		return nil, errIndex
	}
	m := contracts.Mailbox{
		ID: id, Address: address, Label: label, State: contracts.MailboxWaiting, CreatedAt: doc.CreatedAt,
		RetentionDays: contracts.MailboxRetentionDefault, RetentionOptions: slices.Clone(contracts.MailboxRetentionDays),
		Confirmations: []contracts.ForwardingConfirmation{},
	}
	return &m, nil
}

// giveUp cancels a reservation at once: its tombstone, then the identifier
// out of the list. What fails here a later reader of the list finishes.
func (s *Service) giveUp(ctx context.Context, account, id string) {
	if err := s.tombstone(ctx, id, account); err != nil && !errors.Is(err, store.ErrExists) {
		return
	}
	_ = s.takeOut(ctx, account, id)
}

// ---- label and retention ----

// maxSettingsChanges is how many changes of one mailbox's settings an
// enclave takes in a minute.
const maxSettingsChanges = 10

// Update implements httpapi.MailboxesService.
func (s *Service) Update(ctx context.Context, c Caller, id string, in contracts.MailboxUpdate) (*contracts.Mailbox, error) {
	st, err := s.standing(ctx, c.Account)
	if err != nil {
		return nil, err
	}
	sl, err := s.own(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if err := refuse(st); err != nil {
		return nil, err
	}
	if sl.l.state != stActive {
		return nil, contracts.ErrMailboxUnreadable
	}
	if in.Label != nil && !domain.MailboxLabel(*in.Label) {
		return nil, contracts.ErrInvalidLabel
	}
	if in.RetentionDays != nil && !slices.Contains(contracts.MailboxRetentionDays, *in.RetentionDays) {
		return nil, contracts.ErrInvalidRetention
	}
	if !s.settingsRoom(id) {
		return nil, contracts.ErrTooManyRequests
	}
	key, err := s.mailboxKey(ctx, sl.l)
	if err != nil {
		return nil, err
	}
	for range maxTries {
		set, err := s.readSettings(ctx, id, key)
		if err != nil {
			return nil, err
		}
		label, retention := set.label, set.retentionDays
		if in.Label != nil {
			label = *in.Label
		}
		if in.RetentionDays != nil {
			retention = *in.RetentionDays
		}
		var version uint64
		if set.entry != nil {
			version = set.entry.Version
		}
		err = s.writeSettings(ctx, id, key, version, retention, label)
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrExists) {
			continue
		}
		if err != nil {
			return nil, err
		}
		m, err := s.view(ctx, sl)
		if err != nil {
			return nil, err
		}
		return &m, nil
	}
	return nil, errors.New("mailboxes: a mailbox's settings keep changing")
}

// settingsRoom counts a change of a mailbox's settings, and reports whether
// it is within the limit of this enclave.
func (s *Service) settingsRoom(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := slices.DeleteFunc(s.settings[id], func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	if len(recent) >= maxSettingsChanges {
		s.settings[id] = recent
		return false
	}
	s.settings[id] = append(recent, now)
	return true
}

// ---- disconnecting (ADR 0026, "Disconnecting") ----

// Disconnect implements httpapi.MailboxesService.
func (s *Service) Disconnect(ctx context.Context, c Caller, id string) error {
	if _, err := s.standing(ctx, c.Account); err != nil {
		return err
	}
	if !validMailboxID(id) {
		return contracts.ErrNotFound
	}
	fresh := s.now().Before(c.ConfirmedUntil)
	for range maxTries {
		list, _, err := s.loadList(ctx, c.Account)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		entry, inList := list.find(id)
		l, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		switch l.state {
		case stActive:
			if l.doc.Account != c.Account {
				return contracts.ErrNotFound
			}
			if !fresh {
				return contracts.ErrConfirmationNeeded
			}
			at, err := s.disconnect(ctx, l, byPerson)
			if errors.Is(err, store.ErrChanged) {
				continue // read again: another request may have done it
			}
			if err != nil {
				return err
			}
			// Committed: this call tells its person now, before any step that
			// can fail; a retry finds it disconnected and tells nobody.
			s.notify(ctx, c.Account, NoticeDisconnected, at)
			if err := s.leave(ctx, l, c.Account, l.doc.Rcpt); err != nil {
				return err
			}
			s.wake()
			return nil
		case stDisconnecting, stCancelled:
			if l.doc.Account != c.Account {
				return contracts.ErrNotFound
			}
			return s.leave(ctx, l, c.Account, l.doc.Rcpt) // disconnected already: the same answer
		case stLost:
			if !inList {
				return contracts.ErrNotFound
			}
			if !fresh {
				return contracts.ErrConfirmationNeeded
			}
			// The list is the proof whose it is: remove the record at once, as
			// it needs no value.
			at := s.now().Truncate(time.Second)
			if err := s.queue(ctx, at, id); err != nil {
				return err
			}
			if err := s.cfg.Records.RemoveRecord(ctx, recordName(id)); errors.Is(err, store.ErrForked) {
				continue
			} else if err != nil {
				return err
			}
			// Removed: told now, as a readable one is.
			s.notify(ctx, c.Account, NoticeDisconnected, at)
			if err := s.takeOut(ctx, c.Account, id); err != nil {
				return err
			}
			if err := s.unpoint(ctx, entry.Rcpt, id); err != nil {
				return err
			}
			s.wake()
			return nil
		case stForked:
			if !inList {
				return contracts.ErrNotFound
			}
			if !fresh {
				return contracts.ErrConfirmationNeeded
			}
			// Nothing can be committed for it: its pointer goes, and it stays.
			if err := s.unpoint(ctx, entry.Rcpt, id); err != nil {
				return err
			}
			return contracts.ErrMailboxForked
		}
		// No commit, or removed.
		if !inList {
			if owner, err := s.indexed(ctx, id); err != nil {
				return err
			} else if owner == c.Account {
				return nil // removed already, the index says whose it was
			}
			return contracts.ErrNotFound
		}
		if !fresh {
			return contracts.ErrConfirmationNeeded
		}
		// A reservation of the caller's, or a record removed with its
		// identifier still in the list: fence it and take it out.
		if err := s.tombstone(ctx, id, c.Account); errors.Is(err, store.ErrExists) {
			again, err := s.load(ctx, id)
			if err != nil {
				return err
			}
			if again.state != stNone {
				continue // made meanwhile: decide again on what it is
			}
		} else if err != nil {
			return err
		}
		if err := s.takeOut(ctx, c.Account, id); err != nil {
			return err
		}
		return s.unpoint(ctx, entry.Rcpt, id)
	}
	return errors.New("mailboxes: a mailbox keeps changing")
}

// disconnect writes the disconnect entry, then the record disconnecting at
// the version read, without its key or its sealed part: from that commit no
// request opens the mailbox and no mail is accepted for it.
func (s *Service) disconnect(ctx context.Context, l *loaded, by string) (time.Time, error) {
	at := s.now().Truncate(time.Second)
	if err := s.queue(ctx, at, l.id); err != nil {
		return at, err
	}
	doc := *l.doc
	doc.State, doc.DisconnectedAt, doc.DisconnectedBy, doc.Sealed = stateDisconnecting, at, by, nil
	rec, err := s.signedRecord(ctx, &doc, nil)
	if err != nil {
		return at, err
	}
	return at, s.cfg.Records.PutRecordAt(ctx, rec, l.at)
}

// leave takes a mailbox that is leaving out of its list and deletes its
// pointer and its index entry.
func (s *Service) leave(ctx context.Context, l *loaded, account, rcpt string) error {
	if err := s.takeOut(ctx, account, l.id); err != nil {
		return err
	}
	again, err := s.load(ctx, l.id)
	if err != nil {
		return err
	}
	if again.state == stNone {
		again.state = stCancelled
	}
	return s.finish(ctx, again, rcpt)
}
