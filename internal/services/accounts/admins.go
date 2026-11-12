package accounts

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// Admins (ADR 0022). Admins manage the service from the web app, signed in to
// their own accounts, with what the operator's routes do (operator.go), plus
// the admins and the log.
//
// The super admin is whichever account has the address compiled into the
// image (Config.SuperAdmin) as its contact address, worked out from the
// signed record each time and never stored: nothing written in the table
// makes one. The other admins are accounts whose signed record says so (who
// made them one, and when), set and unset only by the super admin, through
// change of the record like everything else of it. An admin's powers work
// only while their account is "passkey only", so that whoever reads the
// admin's mail, or a provider that lies, is not an admin; and every change
// they make needs a fresh proof (confirm.go), which for such an account is a
// passkey.
//
// Every action of an admin is counted for /status by its kind, as the
// operator's are, and written in the admin log: an entry in the storage,
// signed, kept 400 days, naming the admin and the account or invitation it was
// about by their identifiers. Never in the enclave's log, which is public.

// adminDoc is, in an account's record, that the account is an admin.
type adminDoc struct {
	// By is the account of the super admin who made it one.
	By    string    `json:"by"`
	Since time.Time `json:"since"`
}

// role is what an account is in the admin panel: the super admin, an admin,
// or nothing ("").
func (s *Service) role(doc *accountDoc) string {
	switch {
	case s.cfg.SuperAdmin != "" && doc.Address == s.cfg.SuperAdmin:
		return contracts.AdminSuper
	case doc.Admin != nil:
		return contracts.AdminRole
	}
	return ""
}

// acting is who acts on accounts through the admin panel: an admin, the
// super admin or not, with a session whose proof was fresh or not when the
// request came. nil is the operator with the admin token, whom nothing here
// limits.
type acting struct {
	id        string
	super     bool
	confirmed bool
}

// changing refuses a change without a fresh proof.
func (a *acting) changing() error {
	if a != nil && !a.confirmed {
		return contracts.ErrConfirmationNeeded
	}
	return nil
}

// superOnly refuses a change that only the super admin makes.
func (a *acting) superOnly() error {
	if a != nil && !a.super {
		return contracts.ErrSuperAdminOnly
	}
	return a.changing()
}

// guard refuses an account that whoever acts may not act on: nobody blocks or
// deletes the super admin's account through the admin panel, and an admin
// who is not the super admin acts on no admin's account, theirs included.
// It runs on the record as it is when the change is written.
func (a *acting) guard(s *Service, kind string) func(*accountDoc) error {
	if a == nil {
		return nil
	}
	return func(doc *accountDoc) error {
		role := s.role(doc)
		switch {
		case role == contracts.AdminSuper && (kind == ActionBlock || kind == ActionDelete):
			return contracts.ErrSuperAdminKept
		case role != "" && !a.super:
			return contracts.ErrSuperAdminOnly
		}
		return nil
	}
}

// Admin is what an admin can do, as one admin, for the requests of one
// session. It is what the admin panel's routes are given, and holds nothing
// that changes an address or a way into an account.
type Admin struct {
	s  *Service
	by *acting
}

// Admin returns what the admin behind a session can do. Someone who is not
// an admin is ErrNotFound, and an admin whose account is not "passkey only"
// is ErrPasskeyOnlyNeeded: neither can do anything. What the session's proof
// allows is decided now, for this request.
func (s *Service) Admin(ctx context.Context, who *Authenticated) (*Admin, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	role := s.role(doc)
	if role == "" {
		return nil, contracts.ErrNotFound
	}
	if !doc.PasskeyOnly {
		return nil, contracts.ErrPasskeyOnlyNeeded
	}
	return &Admin{s: s, by: &acting{id: doc.ID, super: role == contracts.AdminSuper, confirmed: s.fresh(who)}}, nil
}

// What an admin looks at.

func (a *Admin) AccountRecord(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.AccountRecord(ctx, id)
}

func (a *Admin) AccountRecords(ctx context.Context, after string, limit int) (*contracts.AccountList, error) {
	return a.s.AccountRecords(ctx, after, limit)
}

func (a *Admin) AccountRecordByAddress(ctx context.Context, address string) (*contracts.AccountList, error) {
	return a.s.AccountRecordByAddress(ctx, address)
}

func (a *Admin) Invitations(ctx context.Context) (*contracts.InvitationList, error) {
	return a.s.Invitations(ctx)
}

func (a *Admin) ProviderApps(ctx context.Context) (*contracts.ProviderApps, error) {
	return a.s.ProviderApps(ctx)
}

func (a *Admin) Stats(ctx context.Context) (*contracts.Stats, error) { return a.s.Stats(ctx) }

func (a *Admin) Admins(ctx context.Context) (*contracts.AdminList, error) { return a.s.Admins(ctx) }

func (a *Admin) AdminLog(ctx context.Context, after string, limit int) (*contracts.AdminLog, error) {
	return a.s.AdminLog(ctx, after, limit)
}

// What an admin changes, each with a fresh proof.

func (a *Admin) BlockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.block(ctx, a.by, id)
}

func (a *Admin) UnblockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.unblock(ctx, a.by, id)
}

func (a *Admin) DeleteAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.deleteAccount(ctx, a.by, id)
}

func (a *Admin) KeepAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.keepAccount(ctx, a.by, id)
}

func (a *Admin) CloseAccountSessions(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return a.s.closeAccountSessions(ctx, a.by, id)
}

func (a *Admin) CreateInvitations(ctx context.Context, count int) (*contracts.Invitations, error) {
	return a.s.createInvitations(ctx, a.by, count)
}

func (a *Admin) RevokeInvitation(ctx context.Context, id string) error {
	return a.s.revokeInvitation(ctx, a.by, id)
}

// SetProviderApp and RemoveProviderApp are the super admin's alone.
func (a *Admin) SetProviderApp(ctx context.Context, provider string, in contracts.ProviderAppSet) (*contracts.ProviderApp, error) {
	return a.s.setProviderApp(ctx, a.by, provider, in)
}

func (a *Admin) RemoveProviderApp(ctx context.Context, provider string) error {
	return a.s.removeProviderApp(ctx, a.by, provider)
}

// MakeAdmin makes an account an admin: the super admin's alone. One that is
// an admin already, or is the super admin's, stays as it is. It answers the
// admins after it.
func (a *Admin) MakeAdmin(ctx context.Context, id string) (*contracts.AdminList, error) {
	return a.s.makeAdmin(ctx, a.by, id)
}

// RemoveAdmin makes an admin stop being one: the super admin's alone. The
// super admin stays the super admin (ErrSuperAdminStays), and an account that
// is no admin stays as it is. It answers the admins after it.
func (a *Admin) RemoveAdmin(ctx context.Context, id string) (*contracts.AdminList, error) {
	return a.s.removeAdmin(ctx, a.by, id)
}

// ErrSuperAdminStays has the code of ErrSuperAdminOnly: the super admin is
// whoever has the address compiled into the image, and stops being it only by
// changing that address.
var ErrSuperAdminStays = &contracts.Error{Code: contracts.ErrSuperAdminOnly.Code, Message: "the super admin stays the super admin: its address is compiled into the image"}

// The admins are listed by pointers, one per admin, signed where they are
// stored and keyed by the account. A pointer proves nothing: what is shown is
// the account it names, if its signed record says it is an admin.
const adminsPartition = "admins"

// maxAdmins is how many admins are listed.
const maxAdmins = 1000

func (s *Service) makeAdmin(ctx context.Context, by *acting, id string) (*contracts.AdminList, error) {
	if err := by.superOnly(); err != nil {
		return nil, err
	}
	at := s.now().Truncate(time.Second)
	doc, changed, err := s.operate(ctx, id, nil, func(doc *accountDoc) error {
		if s.role(doc) != "" {
			return errUnchanged
		}
		// The pointer goes in before the record: one that the record does not
		// bear out lists nothing, where an admin without one would not be listed.
		err := s.point(ctx, adminsPartition, doc.ID, doc.ID, 0)
		if err != nil && !errors.Is(err, store.ErrExists) {
			return err
		}
		doc.Admin = &adminDoc{By: by.id, Since: at}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.acted(ActionAdminMade, 1)
	s.logged(ctx, by, ActionAdminMade, doc.ID, "")
	if changed {
		s.notify(ctx, noticeAdminMade, adminMail(doc.Address, s.rp.ID, at, true))
	}
	return s.Admins(ctx)
}

func (s *Service) removeAdmin(ctx context.Context, by *acting, id string) (*contracts.AdminList, error) {
	if err := by.superOnly(); err != nil {
		return nil, err
	}
	doc, changed, err := s.operate(ctx, id, nil, func(doc *accountDoc) error {
		switch s.role(doc) {
		case contracts.AdminSuper:
			return ErrSuperAdminStays
		case "":
			return errUnchanged
		}
		doc.Admin = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The record says it is no admin, so the pointer is nothing now. One that
	// stays lists nothing.
	if err := s.forget(ctx, adminsPartition, doc.ID); err != nil {
		s.cfg.Log.Warn("accounts: the pointer of an admin that was removed could not be deleted")
	}
	s.acted(ActionAdminRemoved, 1)
	s.logged(ctx, by, ActionAdminRemoved, doc.ID, "")
	if changed {
		s.notify(ctx, noticeAdminRemoved, adminMail(doc.Address, s.rp.ID, s.now(), false))
	}
	return s.Admins(ctx)
}

// Admins lists the admins: the super admin first, if its account exists, and
// then the others, the oldest first. Each is read from its signed record; an
// account that cannot be read is left out, and counted in the log.
func (s *Service) Admins(ctx context.Context) (*contracts.AdminList, error) {
	out := &contracts.AdminList{Admins: []contracts.AdminEntry{}}
	if s.cfg.SuperAdmin != "" {
		doc, err := s.find(ctx, s.cfg.SuperAdmin)
		if err != nil {
			return nil, err
		}
		if doc != nil && s.role(doc) == contracts.AdminSuper {
			out.Admins = append(out.Admins, contracts.AdminEntry{ID: doc.ID, Address: doc.Address, Name: doc.Name, Super: true})
		}
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.cfg.Entries.ListEntries(ctx, adminsPartition, "", "", maxAdmins)
	if err != nil {
		return nil, err
	}
	var others []contracts.AdminEntry
	unreadable := 0
	for i := range entries {
		id := entries[i].Key
		if pointed(key, &entries[i]) != id {
			continue
		}
		doc, err := s.read(ctx, id)
		if err != nil {
			unreadable++
			continue
		}
		if doc == nil || s.role(doc) != contracts.AdminRole {
			continue
		}
		others = append(others, contracts.AdminEntry{ID: doc.ID, Address: doc.Address, Name: doc.Name, Since: doc.Admin.Since})
	}
	if unreadable > 0 {
		s.cfg.Log.Error("accounts: records of admins could not be read", "count", unreadable)
	}
	slices.SortFunc(others, func(a, b contracts.AdminEntry) int { return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.ID, b.ID)) })
	out.Admins = append(out.Admins, others...)
	return out, nil
}

// ---- the admin log ----

// The admin log is entries of one partition, keyed so that the newest comes
// first: the time counted back from the end of time, and a random part.
const (
	adminLogPartition = "admin-log"
	// adminLogLifetime is how long an entry of the log is kept.
	adminLogLifetime = 400 * 24 * time.Hour
)

var logKeyRE = regexp.MustCompile(`^\d{19}\.[0-9a-f]{16}$`)

func logKey(at time.Time) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%019d.%s", math.MaxInt64-at.UnixNano(), hex.EncodeToString(b))
}

// logDoc is an entry of the admin log as stored: signed with where it is
// stored. It names accounts by their identifiers alone: the address of one is
// read from its record when the log is read, so an account that is gone
// leaves no address here.
type logDoc struct {
	V          int       `json:"v"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Admin      string    `json:"admin"`
	Account    string    `json:"account,omitempty"`
	Invitation string    `json:"invitation,omitempty"`
}

// logged writes what an admin did in the admin log. The operator with the
// admin token is not written here: that is counted only. An entry that
// cannot be written does not undo what was done, which is done already; it is
// logged by its kind alone.
func (s *Service) logged(ctx context.Context, by *acting, kind, account, invitation string) {
	if by == nil {
		return
	}
	if err := s.writeLog(ctx, logDoc{V: 1, At: s.now(), Kind: kind, Admin: by.id, Account: account, Invitation: invitation}); err != nil {
		s.cfg.Log.Warn("accounts: an entry of the admin log could not be written", "kind", kind)
	}
}

func (s *Service) writeLog(ctx context.Context, doc logDoc) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	e := &store.Entry{Partition: adminLogPartition, Key: logKey(doc.At), ExpiresAt: doc.At.Add(adminLogLifetime), UpdatedAt: doc.At}
	raw, _ := json.Marshal(doc)
	if e.Value, err = envelope.Sign(key, raw, entryAAD(e.Partition, e.Key)); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// AdminLog returns a page of the admin log, the newest first, starting after
// the place a page before ended at ("" for the first). An entry that does not
// verify, or is older than the log keeps, is left out: whoever writes to the
// table can delete entries, and cannot write one.
func (s *Service) AdminLog(ctx context.Context, after string, limit int) (*contracts.AdminLog, error) {
	if limit < 1 || limit > domain.MaxAccountsPage {
		return nil, contracts.ErrInvalidLimit
	}
	if after != "" && !logKeyRE.MatchString(after) {
		return nil, contracts.ErrInvalidCursor
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.cfg.Entries.ListEntries(ctx, adminLogPartition, "", after, limit)
	if err != nil {
		return nil, err
	}
	out, now := &contracts.AdminLog{Entries: []contracts.LogEntry{}}, s.now()
	addresses := map[string]string{}
	address := func(id string) string {
		if a, ok := addresses[id]; ok {
			return a
		}
		// Gone, or not to be read: no address.
		doc, err := s.read(ctx, id)
		if err == nil && doc != nil {
			addresses[id] = doc.Address
		} else {
			addresses[id] = ""
		}
		return addresses[id]
	}
	for i := range entries {
		e := &entries[i]
		var doc logDoc
		raw, err := envelope.Verified(key, e.Value, entryAAD(e.Partition, e.Key))
		if err != nil || json.Unmarshal(raw, &doc) != nil || !validID("acc", doc.Admin) || !doc.At.Add(adminLogLifetime).After(now) {
			continue
		}
		entry := contracts.LogEntry{At: doc.At, Kind: doc.Kind, Admin: contracts.LogAccount{ID: doc.Admin, Address: address(doc.Admin)}, Invitation: doc.Invitation}
		if doc.Account != "" {
			entry.Account = &contracts.LogAccount{ID: doc.Account, Address: address(doc.Account)}
		}
		out.Entries = append(out.Entries, entry)
	}
	if len(entries) == limit {
		out.Next = entries[len(entries)-1].Key
	}
	return out, nil
}

// ---- counts ----

// maxCounted is how many accounts one request counts, countPage at a time;
// the invitations are counted as the list shows them, a thousand at most
// (domain.MaxInvitationsListed).
const (
	maxCounted = 1000
	countPage  = 100
)

// Stats counts the accounts, by state, and the invitations not used yet, from
// their signed records: a thousand of each at most. It says it is not
// complete when there were more, or some could not be read.
func (s *Service) Stats(ctx context.Context) (*contracts.Stats, error) {
	out := &contracts.Stats{Complete: true}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	after, unreadable := "", 0
	for read := 0; ; read += countPage {
		limit := countPage
		if read >= maxCounted {
			limit = 1 // only to know whether there are more
		}
		entries, err := s.cfg.Entries.ListEntries(ctx, accountsPartition, "", after, limit)
		if err != nil {
			return nil, err
		}
		if read >= maxCounted {
			out.Complete = out.Complete && len(entries) == 0
			break
		}
		for i := range entries {
			after = entries[i].Key
			_, id, ok := parseTimedKey(entries[i].Key)
			if !ok || pointed(key, &entries[i]) != id {
				continue
			}
			doc, err := s.read(ctx, id)
			if err != nil {
				unreadable++
				continue
			}
			if doc == nil {
				continue // removed since, or never made
			}
			out.Accounts.Total++
			if doc.State == contracts.AccountClosing {
				out.Accounts.Closing++
			} else {
				out.Accounts.Active++
			}
			if doc.blocked() {
				out.Accounts.Blocked++
			}
		}
		if len(entries) < countPage {
			break
		}
	}
	if unreadable > 0 {
		out.Complete = false
		s.cfg.Log.Error("accounts: records of accounts could not be read for the admin panel's counts", "count", unreadable)
	}
	invitations, err := s.Invitations(ctx)
	if err != nil {
		return nil, err
	}
	out.Invitations.Unused = len(invitations.Invitations)
	if invitations.Truncated {
		out.Complete = false
	}
	return out, nil
}
