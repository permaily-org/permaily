// Package mailboxestest has fakes of the seams of ADR 0026, for the pull
// requests built at the same time as the mailboxes service: Fake is the
// service as the person's routes (httpapi.MailboxesService, pull request 2)
// and accounts (accounts.Mailboxes, pull request 4) see it; Accounts and
// Notices are what the service needs of accounts (pull request 1). Each keeps
// the policy of the interface it fakes, so a test against it fails where the
// real one would: the owner, the account's standing, the fresh proof.
// Nothing here is anchored, sealed or shared between enclaves. Never imported
// outside tests and dev mode (depguard).
package mailboxestest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// Accounts fakes mailboxes.Accounts: an account is agreed and open unless
// set otherwise, and one that is Blocked (or Gone) is unauthorized.
type Accounts struct {
	mu       sync.Mutex
	standing map[string]mailboxes.Standing
	blocked  map[string]bool
}

// NewAccounts returns Accounts where every account is agreed and open.
func NewAccounts() *Accounts {
	return &Accounts{standing: map[string]mailboxes.Standing{}, blocked: map[string]bool{}}
}

// Set gives an account a standing.
func (a *Accounts) Set(account string, s mailboxes.Standing) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.standing[account] = s
}

// Block makes an account blocked, or gone: its standing is unauthorized.
func (a *Accounts) Block(account string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.blocked[account] = true
}

// Standing implements mailboxes.Accounts.
func (a *Accounts) Standing(_ context.Context, account string) (mailboxes.Standing, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blocked[account] || account == "" {
		return mailboxes.Standing{}, contracts.ErrUnauthorized
	}
	if s, ok := a.standing[account]; ok {
		return s, nil
	}
	return mailboxes.Standing{TermsAgreed: true}, nil
}

// Notice is a notice a Notices was asked to send.
type Notice struct {
	Account string
	Kind    mailboxes.NoticeKind
	At      time.Time
}

// Notices fakes mailboxes.Notifier: it keeps what it is asked to send.
type Notices struct {
	mu   sync.Mutex
	sent []Notice
}

// MailboxNotice implements mailboxes.Notifier.
func (n *Notices) MailboxNotice(_ context.Context, account string, kind mailboxes.NoticeKind, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, Notice{account, kind, at})
}

// Sent returns what was sent, in order.
func (n *Notices) Sent() []Notice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.sent)
}

type box struct {
	account    string
	mailbox    contracts.Mailbox
	unreadable bool
	changes    []time.Time
}

// Fake is the mailboxes service in memory, with the policy of ADR 0026: the
// owner, the account's standing (Accounts), the fresh proof, the limit of
// five, a mailbox that cannot be read, pausing. It implements
// httpapi.MailboxesService and accounts.Mailboxes; mailboxestest's own tests
// check both.
type Fake struct {
	Accounts *Accounts
	Notices  *Notices
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu    sync.Mutex
	boxes map[string]*box
	order []string
	made  int
}

// New returns an empty Fake with its own Accounts and Notices.
func New() *Fake {
	return &Fake{Accounts: NewAccounts(), Notices: &Notices{}, boxes: map[string]*box{}}
}

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now().UTC()
	}
	return time.Now().UTC()
}

// Key is the data key Open gives a mailbox: the same on every call, so a test
// can seed its messages with inboxtest.Seed before opening it.
func Key(mailboxID string) []byte {
	k := sha256.Sum256([]byte("mailboxestest key " + mailboxID))
	return k[:]
}

// Add makes a mailbox of an account as Create would, without its checks.
func (f *Fake) Add(account, label string) contracts.Mailbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(account, label)
}

func (f *Fake) add(account, label string) contracts.Mailbox {
	f.made++
	id := fmt.Sprintf("mbx_%026d", f.made)
	m := contracts.Mailbox{
		ID: id, Address: fmt.Sprintf("fake%016d@inbox.permaily.test", f.made), Label: label,
		State: contracts.MailboxWaiting, CreatedAt: f.now(),
		RetentionDays: contracts.MailboxRetentionDefault, RetentionOptions: slices.Clone(contracts.MailboxRetentionDays),
		Confirmations: []contracts.ForwardingConfirmation{},
	}
	f.boxes[id] = &box{account: account, mailbox: m}
	f.order = append(f.order, id)
	return m
}

// Change edits a mailbox as delivery would (a confirmation, receiving_since).
func (f *Fake) Change(id string, edit func(*contracts.Mailbox)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.boxes[id]; ok {
		edit(&b.mailbox)
	}
}

// MakeUnreadable makes a mailbox's record unreadable, as a fork or a loss does.
func (f *Fake) MakeUnreadable(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.boxes[id]; ok {
		b.unreadable = true
	}
}

// standing is the account's, or the error every request of it gets.
func (f *Fake) standing(ctx context.Context, account string) (mailboxes.Standing, error) {
	return f.Accounts.Standing(ctx, account)
}

// refuse says whether a standing allows a change (create, update, delete
// messages): account_closing first, then terms_needed.
func refuse(s mailboxes.Standing) error {
	if s.Closing {
		return contracts.ErrAccountClosing
	}
	if !s.TermsAgreed {
		return contracts.ErrTermsChanged
	}
	return nil
}

// allows is the table of mailboxes.Access.
func allows(s mailboxes.Standing, a mailboxes.Access) error {
	switch a {
	case mailboxes.AccessListMessages, mailboxes.AccessDownload:
		return nil
	case mailboxes.AccessSearch, mailboxes.AccessReadMessage:
		if !s.TermsAgreed {
			return contracts.ErrTermsChanged
		}
		return nil
	case mailboxes.AccessDeleteMessages:
		return refuse(s)
	}
	return contracts.ErrNotFound
}

// own is the caller's mailbox, or not_found.
func (f *Fake) own(c mailboxes.Caller, id string) (*box, error) {
	b, ok := f.boxes[id]
	if !ok || b.account != c.Account {
		return nil, contracts.ErrNotFound
	}
	return b, nil
}

func (b *box) view() contracts.Mailbox {
	if b.unreadable {
		return contracts.Mailbox{ID: b.mailbox.ID, State: contracts.MailboxUnreadable, CreatedAt: b.mailbox.CreatedAt, RetentionOptions: []int{}, Confirmations: []contracts.ForwardingConfirmation{}}
	}
	m := b.mailbox
	m.RetentionOptions = slices.Clone(m.RetentionOptions)
	m.Confirmations = slices.Clone(m.Confirmations)
	return m
}

func validLabel(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64 {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

func (f *Fake) mine(account string) []*box {
	var out []*box
	for _, id := range f.order {
		if b := f.boxes[id]; b != nil && b.account == account {
			out = append(out, b)
		}
	}
	return out
}

// List implements httpapi.MailboxesService.
func (f *Fake) List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error) {
	if _, err := f.standing(ctx, c.Account); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &contracts.MailboxList{Mailboxes: []contracts.Mailbox{}, Limit: mailboxes.MaxMailboxes}
	for _, b := range f.mine(c.Account) {
		out.Mailboxes = append(out.Mailboxes, b.view())
	}
	return out, nil
}

// Create implements httpapi.MailboxesService.
func (f *Fake) Create(ctx context.Context, c mailboxes.Caller, in contracts.MailboxCreate) (*contracts.Mailbox, error) {
	s, err := f.standing(ctx, c.Account)
	if err != nil {
		return nil, err
	}
	if err := refuse(s); err != nil {
		return nil, err
	}
	if !validLabel(in.Label) {
		return nil, contracts.ErrInvalidLabel
	}
	f.mu.Lock()
	if len(f.mine(c.Account)) >= mailboxes.MaxMailboxes {
		f.mu.Unlock()
		return nil, contracts.ErrTooManyMailboxes
	}
	m := f.add(c.Account, in.Label)
	f.mu.Unlock()
	f.Notices.MailboxNotice(ctx, c.Account, mailboxes.NoticeAdded, m.CreatedAt)
	return &m, nil
}

// Get implements httpapi.MailboxesService.
func (f *Fake) Get(ctx context.Context, c mailboxes.Caller, id string) (*contracts.Mailbox, error) {
	if _, err := f.standing(ctx, c.Account); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.own(c, id)
	if err != nil {
		return nil, err
	}
	m := b.view()
	return &m, nil
}

// Update implements httpapi.MailboxesService.
func (f *Fake) Update(ctx context.Context, c mailboxes.Caller, id string, in contracts.MailboxUpdate) (*contracts.Mailbox, error) {
	s, err := f.standing(ctx, c.Account)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.own(c, id)
	if err != nil {
		return nil, err
	}
	if err := refuse(s); err != nil {
		return nil, err
	}
	if b.unreadable {
		return nil, contracts.ErrMailboxUnreadable
	}
	if in.Label != nil && !validLabel(*in.Label) {
		return nil, contracts.ErrInvalidLabel
	}
	if in.RetentionDays != nil && !slices.Contains(contracts.MailboxRetentionDays, *in.RetentionDays) {
		return nil, contracts.ErrInvalidRetention
	}
	if in.Label != nil {
		b.mailbox.Label = *in.Label
	}
	if in.RetentionDays != nil {
		b.mailbox.RetentionDays = *in.RetentionDays
	}
	m := b.view()
	return &m, nil
}

// Disconnect implements httpapi.MailboxesService.
func (f *Fake) Disconnect(ctx context.Context, c mailboxes.Caller, id string) error {
	if _, err := f.standing(ctx, c.Account); err != nil {
		return err
	}
	f.mu.Lock()
	b, err := f.own(c, id)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	now := f.now()
	if !now.Before(c.ConfirmedUntil) {
		f.mu.Unlock()
		return contracts.ErrConfirmationNeeded
	}
	f.remove(id)
	f.mu.Unlock()
	f.Notices.MailboxNotice(ctx, b.account, mailboxes.NoticeDisconnected, now)
	return nil
}

func (f *Fake) remove(id string) {
	delete(f.boxes, id)
	f.order = slices.DeleteFunc(f.order, func(o string) bool { return o == id })
}

// Open implements httpapi.MailboxesService.
func (f *Fake) Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error) {
	s, err := f.standing(ctx, c.Account)
	if err != nil {
		return inbox.Box{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.own(c, id)
	if err != nil {
		return inbox.Box{}, err
	}
	if err := allows(s, a); err != nil {
		return inbox.Box{}, err
	}
	if b.unreadable {
		return inbox.Box{}, contracts.ErrMailboxUnreadable
	}
	return inbox.Box{MailboxID: id, Key: Key(id)}, nil
}

// RemoveAccount implements accounts.Mailboxes.
func (f *Fake) RemoveAccount(_ context.Context, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.mine(account) {
		f.remove(b.mailbox.ID)
	}
	return nil
}

// Count implements accounts.Mailboxes.
func (f *Fake) Count(_ context.Context, account string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mine(account)), nil
}

// Total implements accounts.Mailboxes.
func (f *Fake) Total(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.boxes), nil
}

func (b *box) record() contracts.MailboxRecord {
	if b.unreadable {
		return contracts.MailboxRecord{ID: b.mailbox.ID, Account: b.account, State: contracts.MailboxUnreadable, CreatedAt: b.mailbox.CreatedAt}
	}
	m := b.mailbox
	return contracts.MailboxRecord{
		ID: m.ID, Account: b.account, State: m.State, CreatedAt: m.CreatedAt, RetentionDays: m.RetentionDays,
		ReceivingSince: m.ReceivingSince, PausedAt: m.PausedAt, Verified: m.Source != nil,
	}
}

// Records implements accounts.Mailboxes.
func (f *Fake) Records(_ context.Context, account string) (*contracts.MailboxRecordList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}
	for _, b := range f.mine(account) {
		out.Mailboxes = append(out.Mailboxes, b.record())
	}
	return out, nil
}

// ByAddress implements accounts.Mailboxes.
func (f *Fake) ByAddress(_ context.Context, address string) (*contracts.MailboxRecordList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}
	for _, id := range f.order {
		if b := f.boxes[id]; !b.unreadable && strings.EqualFold(b.mailbox.Address, strings.TrimSpace(address)) {
			out.Mailboxes = append(out.Mailboxes, b.record())
		}
	}
	return out, nil
}

// Record implements accounts.Mailboxes.
func (f *Fake) Record(_ context.Context, id string) (*contracts.MailboxRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[id]
	if !ok {
		return nil, contracts.ErrNotFound
	}
	r := b.record()
	return &r, nil
}

// Pause implements accounts.Mailboxes.
func (f *Fake) Pause(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return f.pause(ctx, id, true)
}

// Resume implements accounts.Mailboxes.
func (f *Fake) Resume(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return f.pause(ctx, id, false)
}

func (f *Fake) pause(ctx context.Context, id string, on bool) (*contracts.MailboxRecord, error) {
	f.mu.Lock()
	b, ok := f.boxes[id]
	if !ok || b.unreadable {
		f.mu.Unlock()
		return nil, contracts.ErrNotFound
	}
	now := f.now()
	if on == !b.mailbox.PausedAt.IsZero() {
		r := b.record()
		f.mu.Unlock()
		return &r, nil
	}
	b.changes = slices.DeleteFunc(b.changes, func(t time.Time) bool { return now.Sub(t) >= mailboxes.Period })
	if len(b.changes) >= mailboxes.MaxStateChangesPerPeriod {
		f.mu.Unlock()
		return nil, contracts.ErrTooManyRequests
	}
	b.changes = append(b.changes, now)
	kind := mailboxes.NoticeResumed
	if on {
		kind = mailboxes.NoticePaused
		b.mailbox.PausedAt, b.mailbox.State = now, contracts.MailboxPaused
	} else {
		b.mailbox.PausedAt, b.mailbox.State = time.Time{}, contracts.MailboxWaiting
		if !b.mailbox.ReceivingSince.IsZero() {
			b.mailbox.State = contracts.MailboxReceiving
		}
	}
	r := b.record()
	account := b.account
	f.mu.Unlock()
	f.Notices.MailboxNotice(ctx, account, kind, now)
	return &r, nil
}
