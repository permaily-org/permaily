// Package mailboxestest has fakes of the seams of ADR 0026, for the pull
// requests built at the same time as the mailboxes service: Fake is the
// service as the person's routes (httpapi.MailboxesService, pull request 2)
// and accounts (accounts.Mailboxes, pull request 4) see it; Accounts and
// Notices are what the service needs of accounts (pull request 1). Each keeps
// the policy of the interface it fakes, so a test against it fails where the
// real one would: the owner, the account's standing, the fresh proof, the
// limits, a closed list, a lost or forked record, the order of the errors.
// Contract runs the same cases against Fake here and, from pull request 1,
// against *mailboxes.Service. Nothing here is anchored, sealed or shared
// between enclaves. Never imported outside tests and dev mode (depguard).
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

// What a fake mailbox's record is, as ADR 0026's state machine has it.
type recordState int

const (
	recordActive recordState = iota
	// recordLeaving is disconnecting, cancelled or gone: it is not listed,
	// does not count, and its slot is free.
	recordLeaving
	// recordLost is a record whose newest value is gone (store.ErrLost): it
	// counts, and disconnecting it removes it.
	recordLost
	// recordForked is a record with a fork in its history
	// (store.ErrForked): it counts, and nothing can end it.
	recordForked
)

type box struct {
	account string
	mailbox contracts.Mailbox
	state   recordState
	changes []time.Time
}

func (b *box) unreadable() bool { return b.state == recordLost || b.state == recordForked }

// list is an account's list of mailboxes: whether it is closed and when the
// last ten were made.
type list struct {
	closed bool
	made   []time.Time
}

// Fake is the mailboxes service in memory, with the policy of ADR 0026: the
// owner, the account's standing (Accounts), the fresh proof, five mailboxes
// and ten made in thirty days, a closed list, a lost or forked record,
// pausing, and the order of the errors (httpapi.MailboxesService). It
// implements httpapi.MailboxesService and accounts.Mailboxes; Contract
// checks it.
type Fake struct {
	Accounts *Accounts
	Notices  *Notices
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu    sync.Mutex
	boxes map[string]*box
	order []string
	lists map[string]*list
	made  int
	// settings are the times of each mailbox's changes of its settings in
	// the last minute: ten at most, as the service's own budget.
	settings map[string][]time.Time
}

// maxSettingsChanges is how many changes of one mailbox's settings are taken
// in a minute (the service's budget, per enclave).
const maxSettingsChanges = 10

// Sweep does what the sweep's last pass does: every mailbox that was
// disconnected is forgotten, and from then on it cannot be told from one
// never made, by its owner either (ADR 0026, "Disconnecting").
func (f *Fake) Sweep() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = slices.DeleteFunc(f.order, func(id string) bool {
		if f.boxes[id].state == recordLeaving {
			delete(f.boxes, id)
			delete(f.settings, id)
			return true
		}
		return false
	})
}

// New returns an empty Fake with its own Accounts and Notices.
func New() *Fake {
	return &Fake{Accounts: NewAccounts(), Notices: &Notices{}, boxes: map[string]*box{}, lists: map[string]*list{}, settings: map[string][]time.Time{}}
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

// Add makes a mailbox of an account as Create would, without its checks; it
// counts toward the ten made in thirty days.
func (f *Fake) Add(account, label string) contracts.Mailbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(account, label)
}

func (f *Fake) listOf(account string) *list {
	l := f.lists[account]
	if l == nil {
		l = &list{}
		f.lists[account] = l
	}
	return l
}

func (f *Fake) add(account, label string) contracts.Mailbox {
	f.made++
	now := f.now()
	id := fmt.Sprintf("mbx_%026d", f.made)
	m := contracts.Mailbox{
		ID: id, Address: fmt.Sprintf("fake%016d@inbox.permaily.test", f.made), Label: label,
		State: contracts.MailboxWaiting, CreatedAt: now,
		RetentionDays: contracts.MailboxRetentionDefault, RetentionOptions: slices.Clone(contracts.MailboxRetentionDays),
		Confirmations: []contracts.ForwardingConfirmation{},
	}
	f.boxes[id] = &box{account: account, mailbox: m}
	f.order = append(f.order, id)
	l := f.listOf(account)
	l.made = append(l.made, now)
	if len(l.made) > mailboxes.MaxMadePerPeriod {
		l.made = l.made[len(l.made)-mailboxes.MaxMadePerPeriod:]
	}
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

// MakeLost makes a mailbox's record lost, as deleting its newest value does
// (store.ErrLost).
func (f *Fake) MakeLost(id string) { f.set(id, recordLost) }

// MakeForked makes a mailbox's record forked, as a second commit at one of
// its versions does (store.ErrForked).
func (f *Fake) MakeForked(id string) { f.set(id, recordForked) }

func (f *Fake) set(id string, s recordState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.boxes[id]; ok {
		b.state = s
	}
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

// own is the caller's mailbox that is not leaving, or not_found.
func (f *Fake) own(c mailboxes.Caller, id string) (*box, error) {
	b, ok := f.boxes[id]
	if !ok || b.account != c.Account || b.state == recordLeaving {
		return nil, contracts.ErrNotFound
	}
	return b, nil
}

func (b *box) view() contracts.Mailbox {
	if b.unreadable() {
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

// mine is the account's mailboxes that count: not leaving.
func (f *Fake) mine(account string) []*box {
	var out []*box
	for _, id := range f.order {
		if b := f.boxes[id]; b.account == account && b.state != recordLeaving {
			out = append(out, b)
		}
	}
	return out
}

// List implements httpapi.MailboxesService.
func (f *Fake) List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error) {
	if _, err := f.Accounts.Standing(ctx, c.Account); err != nil {
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
	s, err := f.Accounts.Standing(ctx, c.Account)
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
	l := f.listOf(c.Account)
	now := f.now()
	recent := 0
	for _, t := range l.made {
		if now.Sub(t) < mailboxes.Period {
			recent++
		}
	}
	switch {
	case l.closed:
		f.mu.Unlock()
		return nil, contracts.ErrAccountClosing
	case recent >= mailboxes.MaxMadePerPeriod:
		f.mu.Unlock()
		return nil, contracts.ErrTooManyRequests
	case len(f.mine(c.Account)) >= mailboxes.MaxMailboxes:
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
	if _, err := f.Accounts.Standing(ctx, c.Account); err != nil {
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
	s, err := f.Accounts.Standing(ctx, c.Account)
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
	if b.unreadable() {
		return nil, contracts.ErrMailboxUnreadable
	}
	if in.Label != nil && !validLabel(*in.Label) {
		return nil, contracts.ErrInvalidLabel
	}
	if in.RetentionDays != nil && !slices.Contains(contracts.MailboxRetentionDays, *in.RetentionDays) {
		return nil, contracts.ErrInvalidRetention
	}
	now := f.now()
	recent := slices.DeleteFunc(f.settings[id], func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	if len(recent) >= maxSettingsChanges {
		f.settings[id] = recent
		return nil, contracts.ErrTooManyRequests
	}
	f.settings[id] = append(recent, now)
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
	if _, err := f.Accounts.Standing(ctx, c.Account); err != nil {
		return err
	}
	f.mu.Lock()
	b, ok := f.boxes[id]
	if !ok || b.account != c.Account {
		f.mu.Unlock()
		return contracts.ErrNotFound
	}
	if b.state == recordLeaving {
		f.mu.Unlock()
		// Disconnected already: the same answer, until Sweep forgets it.
		return nil
	}
	now := f.now()
	if !now.Before(c.ConfirmedUntil) {
		f.mu.Unlock()
		return contracts.ErrConfirmationNeeded
	}
	if b.state == recordForked {
		f.mu.Unlock()
		return contracts.ErrMailboxForked
	}
	b.state = recordLeaving
	f.mu.Unlock()
	f.Notices.MailboxNotice(ctx, b.account, mailboxes.NoticeDisconnected, now)
	return nil
}

// Open implements httpapi.MailboxesService.
func (f *Fake) Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error) {
	s, err := f.Accounts.Standing(ctx, c.Account)
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
	if b.unreadable() {
		return inbox.Box{}, contracts.ErrMailboxUnreadable
	}
	return inbox.Box{MailboxID: id, Key: Key(id)}, nil
}

// RemoveAccount implements accounts.Mailboxes: the list ends closed, made
// closed when there was none; every mailbox ends but a forked one, which
// stays named in it.
func (f *Fake) RemoveAccount(_ context.Context, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.listOf(account)
	l.closed, l.made = true, nil
	for _, b := range f.mine(account) {
		if b.state != recordForked {
			b.state = recordLeaving
		}
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
	n := 0
	for _, b := range f.boxes {
		if b.state != recordLeaving {
			n++
		}
	}
	return n, nil
}

func (b *box) record() contracts.MailboxRecord {
	if b.unreadable() {
		return contracts.MailboxRecord{ID: b.mailbox.ID, Account: b.account, State: contracts.MailboxUnreadable, CreatedAt: b.mailbox.CreatedAt}
	}
	m := b.mailbox
	return contracts.MailboxRecord{
		ID: m.ID, Account: b.account, State: m.State, CreatedAt: m.CreatedAt, RetentionDays: m.RetentionDays,
		ReceivingSince: m.ReceivingSince, PausedAt: m.PausedAt, Verified: m.Source != nil,
	}
}

// Records implements accounts.Mailboxes: an account with no list, or no
// account at all, is an empty list.
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
		b := f.boxes[id]
		if b.state == recordActive && strings.EqualFold(b.mailbox.Address, strings.TrimSpace(address)) {
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
	if !ok || b.state == recordLeaving {
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
	if !ok || b.state != recordActive {
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
