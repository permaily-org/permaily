package accounts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// superAdmin is the address compiled into a world's image: whichever account
// has it is the super admin.
const superAdmin = "boss@permaily.test"

// adminAccount is an account of a world, signed in, with a passkey.
type adminAccount struct {
	me     *contracts.Me
	cookie string
	auth   *webauthntest.Authenticator
	key    *contracts.Passkey
}

// account signs address up, gives it a passkey and, when only, turns
// "passkey only" on.
func (w *world) account(address string, only bool) *adminAccount {
	w.t.Helper()
	a := &adminAccount{auth: webauthntest.New(webauthntest.ES256)}
	a.me, a.cookie = w.signUp(address)
	a.key = w.addPasskey(w.who(a.cookie), a.auth, "key")
	if only {
		w.passkeyOnly(w.who(a.cookie))
	}
	return a
}

// as is what the account behind a, as an admin, can do with a fresh proof.
func (w *world) as(a *adminAccount) *Admin {
	w.t.Helper()
	admin, err := w.svc.Admin(context.Background(), w.fresh(w.who(a.cookie)))
	if err != nil {
		w.t.Fatalf("as an admin: %v", err)
	}
	return admin
}

// role is what the account behind a is, as it sees it.
func (w *world) role(a *adminAccount) string {
	w.t.Helper()
	me, err := w.svc.Me(context.Background(), w.who(a.cookie))
	if err != nil {
		w.t.Fatal(err)
	}
	return me.Account.Admin
}

func ids(list *contracts.AdminList) []string {
	var out []string
	for _, a := range list.Admins {
		out = append(out, a.ID)
	}
	return out
}

// The super admin is the account with the address compiled into the image,
// and only the super admin makes and removes admins. The person is told by
// mail each time; it is counted and in the log.
func TestOnlyTheSuperAdminMakesAdmins(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	bea := w.account("bea@example.org", true)
	if w.role(boss) != contracts.AdminSuper || w.role(ana) != "" {
		t.Fatalf("roles: %q %q", w.role(boss), w.role(ana))
	}
	list, err := w.svc.Admins(ctx)
	if err != nil || !slices.Equal(ids(list), []string{boss.me.Account.ID}) || !list.Admins[0].Super || !list.Admins[0].Since.IsZero() || list.Admins[0].Address != superAdmin {
		t.Fatalf("the admins at the start: %+v %v", list, err)
	}
	// Someone who is not an admin is nobody here.
	if _, err := w.svc.Admin(ctx, w.who(ana.cookie)); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("ana as an admin: %v", err)
	}

	n := w.count()
	w.now = w.now.Add(time.Hour)
	list, err = w.as(boss).MakeAdmin(ctx, ana.me.Account.ID)
	if err != nil || !slices.Equal(ids(list), []string{boss.me.Account.ID, ana.me.Account.ID}) || list.Admins[1].Super || !list.Admins[1].Since.Equal(w.now) {
		t.Fatalf("making ana an admin: %+v %v", list, err)
	}
	told := w.one(n, "ana@example.org", "Your Permaily account is now an admin")
	if !strings.Contains(told.Text, "was made an admin of Permaily") || !strings.Contains(told.Text, "When: 11 November 2026, 10:00 UTC") {
		t.Fatalf("the notice: %s", told.Text)
	}
	if doc := w.doc(ana.me.Account.ID); doc.Admin == nil || doc.Admin.By != boss.me.Account.ID || w.role(ana) != contracts.AdminRole {
		t.Fatalf("ana's record: %+v", doc.Admin)
	}
	if rec, _ := w.svc.AccountRecord(ctx, ana.me.Account.ID); rec.Admin != contracts.AdminRole {
		t.Fatalf("the record shown: %+v", rec)
	}
	// Again changes nothing and tells nobody; the super admin stays super.
	n, version := w.count(), w.version(ana.me.Account.ID)
	for _, id := range []string{ana.me.Account.ID, boss.me.Account.ID} {
		if list, err := w.as(boss).MakeAdmin(ctx, id); err != nil || len(list.Admins) != 2 {
			t.Fatalf("making %s again: %+v %v", id, list, err)
		}
	}
	if got, _ := w.sent(n); len(got) != 0 || w.version(ana.me.Account.ID) != version {
		t.Fatalf("again sent %+v", got)
	}

	// An admin who is not the super admin makes nobody one, and removes nobody.
	for what, err := range map[string]error{
		"make":        second2(w.as(ana).MakeAdmin(ctx, bea.me.Account.ID)),
		"remove":      second2(w.as(ana).RemoveAdmin(ctx, ana.me.Account.ID)),
		"remove boss": second2(w.as(ana).RemoveAdmin(ctx, boss.me.Account.ID)),
	} {
		if !errors.Is(err, contracts.ErrSuperAdminOnly) {
			t.Errorf("ana: %s: %v", what, err)
		}
	}
	if w.role(bea) != "" || w.role(ana) != contracts.AdminRole {
		t.Fatal("an admin changed who is one")
	}
	// Nobody removes the super admin.
	if _, err := w.as(boss).RemoveAdmin(ctx, boss.me.Account.ID); !errors.Is(err, contracts.ErrSuperAdminOnly) || err.Error() != "super_admin_only: the super admin stays the super admin: its address is compiled into the image" {
		t.Fatalf("removing the super admin: %v", err)
	}
	// An account that is not there is not found.
	for _, id := range []string{"", "x", newID("acc"), newID("ses")} {
		if _, err := w.as(boss).MakeAdmin(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("making %q: %v", id, err)
		}
		if _, err := w.as(boss).RemoveAdmin(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("removing %q: %v", id, err)
		}
	}

	n = w.count()
	list, err = w.as(boss).RemoveAdmin(ctx, ana.me.Account.ID)
	if err != nil || !slices.Equal(ids(list), []string{boss.me.Account.ID}) || w.role(ana) != "" || w.doc(ana.me.Account.ID).Admin != nil {
		t.Fatalf("removing ana: %+v %v", list, err)
	}
	if told := w.one(n, "ana@example.org", "Your Permaily account is no longer an admin"); !strings.Contains(told.Text, "is no longer an admin of Permaily") {
		t.Fatalf("the notice: %s", told.Text)
	}
	if _, err := w.svc.Admin(ctx, w.who(ana.cookie)); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("ana after: %v", err)
	}
	// Removing who is no admin changes nothing.
	n = w.count()
	if _, err := w.as(boss).RemoveAdmin(ctx, bea.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("removing nobody sent %+v", got)
	}
	_, counted := w.svc.OperatorActions()
	if counted[ActionAdminMade] != 3 || counted[ActionAdminRemoved] != 2 {
		t.Fatalf("counted: %v", counted)
	}
	w.quiet("ana@", "example.org", superAdmin, ana.me.Account.ID, boss.me.Account.ID)
}

// Nothing written in the table makes an admin: the role comes from the
// signed record, and the super admin from its address in it. A pointer in
// the list of admins written by hand, or copied, lists nobody, and an
// earlier copy of an admin's record does not come back.
func TestNothingInTheTableMakesAnAdmin(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	eve := w.account("eve@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}

	// Eve's pointer, by hand: unsigned, and signed for Ana's place.
	w.mem.SetEntry(store.Entry{Partition: adminsPartition, Key: eve.me.Account.ID, Value: []byte(`{"account":"` + eve.me.Account.ID + `"}`)})
	anas, _ := w.mem.GetEntry(ctx, adminsPartition, ana.me.Account.ID)
	copied := *anas
	copied.Key = eve.me.Account.ID
	w.mem.SetEntry(copied)
	if list, _ := w.svc.Admins(ctx); !slices.Equal(ids(list), []string{boss.me.Account.ID, ana.me.Account.ID}) {
		t.Fatalf("the admins: %+v", list)
	}
	if _, err := w.svc.Admin(ctx, w.fresh(w.who(eve.cookie))); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("eve with pointers by hand: %v", err)
	}

	// Ana's record from when she was an admin, put back after she stopped.
	before := w.snapshot()
	if _, err := w.as(boss).RemoveAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.putBack(before)
	if _, err := w.svc.Admin(ctx, w.fresh(w.who(ana.cookie))); err == nil {
		t.Fatal("an earlier copy made ana an admin again")
	}

	// The super admin is whose address it is, and nobody else: not an
	// account whose name, or whose address before a change, is it.
	if _, err := w.svc.UpdateAccount(ctx, w.who(eve.cookie), contracts.AccountUpdate{Name: text(superAdmin)}); err != nil {
		t.Fatal(err)
	}
	if w.role(eve) != "" {
		t.Fatal("a name made a super admin")
	}
	// An image without the address has none.
	w.svc.cfg.SuperAdmin = ""
	if w.role(boss) != "" {
		t.Fatal("a super admin without an address in the image")
	}
	if list, _ := w.svc.Admins(ctx); len(list.Admins) != 0 {
		t.Fatalf("the admins without a super admin: %+v", list)
	}
}

// An admin whose account is not "passkey only" does nothing at all: whoever
// reads their mail is not an admin. The app is told what is missing.
func TestAnAdminWhoseAccountIsNotPasskeyOnlyDoesNothing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, false)
	if w.role(boss) != contracts.AdminSuper {
		t.Fatalf("the role: %q", w.role(boss))
	}
	if _, err := w.svc.Admin(ctx, w.fresh(w.who(boss.cookie))); !errors.Is(err, contracts.ErrPasskeyOnlyNeeded) {
		t.Fatalf("the super admin without passkey only: %v", err)
	}
	w.passkeyOnly(w.who(boss.cookie))
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.SetPasskeyOnly(ctx, w.fresh(w.who(ana.cookie)), contracts.PasskeyOnly{On: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Admin(ctx, w.fresh(w.who(ana.cookie))); !errors.Is(err, contracts.ErrPasskeyOnlyNeeded) || w.role(ana) != contracts.AdminRole {
		t.Fatalf("an admin without passkey only: %v %q", err, w.role(ana))
	}
	// A blocked admin is nobody: their session stands for nothing.
	w.operator(w.svc.BlockAccount, boss.me.Account.ID)
	if _, err := w.svc.Admin(ctx, w.fresh(&Authenticated{AccountID: boss.me.Account.ID})); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a blocked admin: %v", err)
	}
}

// Every answer that is the person's own account says whether it is an admin
// and whether it signs in with passkeys only, so the web app, which takes the
// answer as the account, keeps showing the panel and knows whether it works.
func TestThePersonsOwnAccountSaysItsRoleAndPasskeyOnly(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, false)
	me, err := w.svc.Me(ctx, w.who(boss.cookie))
	if err != nil || me.Account.PasskeyOnly || me.Account.Admin != contracts.AdminSuper {
		t.Fatalf("me before passkey only: %+v %v", me, err)
	}
	w.passkeyOnly(w.who(boss.cookie))
	if me, err = w.svc.Me(ctx, w.who(boss.cookie)); err != nil || !me.Account.PasskeyOnly {
		t.Fatalf("me after passkey only: %+v %v", me, err)
	}
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	name := "Ana"
	answers := map[string]func() (*contracts.Account, error){
		"the name": func() (*contracts.Account, error) {
			return w.svc.UpdateAccount(ctx, w.who(ana.cookie), contracts.AccountUpdate{Name: &name})
		},
		"the address": func() (*contracts.Account, error) { return w.moveTo(w.fresh(w.who(ana.cookie)), "ana@example.net") },
		"deleting":    func() (*contracts.Account, error) { return w.svc.StartDeletion(ctx, w.fresh(w.who(ana.cookie))) },
		"cancelling":  func() (*contracts.Account, error) { return w.svc.CancelDeletion(ctx, w.who(ana.cookie)) },
	}
	for _, what := range []string{"the name", "the address", "deleting", "cancelling"} {
		got, err := answers[what]()
		if err != nil || got.Admin != contracts.AdminRole || !got.PasskeyOnly {
			t.Fatalf("the answer to %s: %+v %v", what, got, err)
		}
	}
}

// What an admin does is what the operator does, with limits: nobody blocks or
// deletes the super admin's account here, an admin acts on no admin's
// account, the providers' applications are the super admin's, and a deletion
// the person asked for is theirs to cancel. Each change needs a fresh proof.
func TestWhatAnAdminDoes(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	bea := w.account("bea@example.org", true)
	carl, _ := w.signUp("carl@example.org")
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.as(boss).MakeAdmin(ctx, bea.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	_, before := w.svc.OperatorActions()

	// Ana blocks, unblocks, deletes, cancels and signs out Carl.
	admin := w.as(ana)
	for _, act := range []func(context.Context, string) (*contracts.AccountRecord, error){
		admin.BlockAccount, admin.UnblockAccount, admin.DeleteAccount, admin.KeepAccount, admin.CloseAccountSessions,
	} {
		if rec, err := act(ctx, carl.Account.ID); err != nil || rec.ID != carl.Account.ID {
			t.Fatalf("ana on carl: %+v %v", rec, err)
		}
	}
	if rec, _ := w.svc.AccountRecord(ctx, carl.Account.ID); rec.State != contracts.AccountActive || !rec.BlockedAt.IsZero() || rec.Sessions != 0 {
		t.Fatalf("carl after: %+v", rec)
	}
	// A deletion Carl asked for is Carl's to cancel.
	_, carlCookie := w.signIn("carl@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.who(carlCookie)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.KeepAccount(ctx, carl.Account.ID); !errors.Is(err, contracts.ErrNotCancellable) {
		t.Fatalf("ana cancels what carl asked: %v", err)
	}
	// Ana acts on no admin's account, hers included, and nobody blocks or
	// deletes the super admin's.
	for _, target := range []*adminAccount{ana, bea, boss} {
		for kind, act := range map[string]func(context.Context, string) (*contracts.AccountRecord, error){
			ActionBlock: admin.BlockAccount, ActionUnblock: admin.UnblockAccount, ActionDelete: admin.DeleteAccount,
			ActionDeleteCancel: admin.KeepAccount, ActionSessionsClose: admin.CloseAccountSessions,
		} {
			if rec, err := act(ctx, target.me.Account.ID); !errors.Is(err, contracts.ErrSuperAdminOnly) || rec != nil {
				t.Errorf("ana %s on %s: %+v %v", kind, target.me.Account.Address, rec, err)
			}
		}
	}
	for _, act := range []func(context.Context, string) (*contracts.AccountRecord, error){w.as(boss).BlockAccount, w.as(boss).DeleteAccount} {
		if _, err := act(ctx, boss.me.Account.ID); !errors.Is(err, contracts.ErrSuperAdminKept) || err.Error() != "super_admin_only: the super admin's account is not blocked or deleted here" {
			t.Fatalf("the super admin on itself: %v", err)
		}
	}
	// The super admin acts on an admin's account.
	if rec, err := w.as(boss).CloseAccountSessions(ctx, bea.me.Account.ID); err != nil || rec.Sessions != 0 {
		t.Fatalf("the super admin signs bea out: %+v %v", rec, err)
	}
	if doc := w.doc(boss.me.Account.ID); doc.blocked() || doc.State != contracts.AccountActive {
		t.Fatalf("the super admin's account: %+v", doc)
	}

	// Invitations, and the providers' applications, which are the super
	// admin's.
	made, err := admin.CreateInvitations(ctx, 2)
	if err != nil || len(made.IDs) != 2 {
		t.Fatal(err)
	}
	if err := admin.RevokeInvitation(ctx, made.IDs[0]); err != nil {
		t.Fatal(err)
	}
	if list, _ := admin.Invitations(ctx); len(list.Invitations) == 0 {
		t.Fatal("no invitations listed")
	}
	set := contracts.ProviderAppSet{ClientID: "id", ClientSecret: "secret"}
	if _, err := admin.SetProviderApp(ctx, "google", set); !errors.Is(err, contracts.ErrSuperAdminOnly) {
		t.Fatalf("ana sets a provider: %v", err)
	}
	if err := admin.RemoveProviderApp(ctx, "google"); !errors.Is(err, contracts.ErrSuperAdminOnly) {
		t.Fatalf("ana removes a provider: %v", err)
	}
	if _, err := w.as(boss).SetProviderApp(ctx, "google", set); err != nil {
		t.Fatal(err)
	}
	if apps, err := admin.ProviderApps(ctx); err != nil || len(apps.Providers) != 1 {
		t.Fatalf("ana reads the providers: %+v %v", apps, err)
	}
	if err := w.as(boss).RemoveProviderApp(ctx, "google"); err != nil {
		t.Fatal(err)
	}
	// What an admin looks at is the operator's.
	if list, err := admin.AccountRecords(ctx, "", 10); err != nil || len(list.Accounts) != 4 {
		t.Fatalf("the accounts: %+v %v", list, err)
	}
	if list, err := admin.AccountRecordByAddress(ctx, superAdmin); err != nil || len(list.Accounts) != 1 || list.Accounts[0].Admin != contracts.AdminSuper {
		t.Fatalf("finding the super admin: %+v %v", list, err)
	}
	if rec, err := admin.AccountRecord(ctx, bea.me.Account.ID); err != nil || rec.Admin != contracts.AdminRole {
		t.Fatalf("bea: %+v %v", rec, err)
	}
	if list, err := admin.Admins(ctx); err != nil || len(list.Admins) != 3 {
		t.Fatalf("the admins: %+v %v", list, err)
	}

	// Counted as the operator's actions are; what was refused is not.
	_, after := w.svc.OperatorActions()
	for kind, n := range map[string]int{
		ActionBlock: 1, ActionUnblock: 1, ActionDelete: 1, ActionDeleteCancel: 1, ActionSessionsClose: 2,
		ActionInvitationsMade: 2, ActionInvitationRevoked: 1, ActionProviderAppSet: 1, ActionProviderAppRemove: 1,
	} {
		if after[kind]-before[kind] != n {
			t.Errorf("%s: %d, want %d", kind, after[kind]-before[kind], n)
		}
	}
	w.quiet("example.org", superAdmin, ana.me.Account.ID, carl.Account.ID, made.IDs[0])
}

// An admin changes nothing without a fresh proof, and with it changes the
// same things; looking needs none.
func TestAnAdminChangesNothingWithoutAFreshProof(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	carl, _ := w.signUp("carl@example.org")
	w.later()
	stale, err := w.svc.Admin(ctx, w.who(boss.cookie))
	if err != nil {
		t.Fatal(err)
	}
	version, n := w.version(carl.Account.ID), w.count()
	_, before := w.svc.OperatorActions()
	for what, err := range map[string]error{
		"block":          second2(stale.BlockAccount(ctx, carl.Account.ID)),
		"unblock":        second2(stale.UnblockAccount(ctx, carl.Account.ID)),
		"delete":         second2(stale.DeleteAccount(ctx, carl.Account.ID)),
		"cancel":         second2(stale.KeepAccount(ctx, carl.Account.ID)),
		"sessions":       second2(stale.CloseAccountSessions(ctx, carl.Account.ID)),
		"invitations":    second2(stale.CreateInvitations(ctx, 1)),
		"revoke":         stale.RevokeInvitation(ctx, newID("inv")),
		"provider":       second2(stale.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "a", ClientSecret: "b"})),
		"provider off":   stale.RemoveProviderApp(ctx, "google"),
		"make admin":     second2(stale.MakeAdmin(ctx, ana.me.Account.ID)),
		"remove admin":   second2(stale.RemoveAdmin(ctx, ana.me.Account.ID)),
		"look":           nil,
		"look, accounts": second2(stale.AccountRecords(ctx, "", 10)),
	} {
		var want error = contracts.ErrConfirmationNeeded
		if strings.HasPrefix(what, "look") {
			want = nil
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, after := w.svc.OperatorActions()
	if w.version(carl.Account.ID) != version || w.role(ana) != "" || w.count() != n || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("something changed")
	}
	if log, _ := w.svc.AdminLog(ctx, "", 10); len(log.Entries) != 0 {
		t.Fatalf("logged: %+v", log)
	}
	// With a passkey, confirmed: the same goes through.
	if _, err := w.confirmWith(w.svc, w.who(boss.cookie), boss.auth, boss.key.ID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := w.svc.Admin(ctx, w.who(boss.cookie))
	if _, err := fresh.BlockAccount(ctx, carl.Account.ID); err != nil {
		t.Fatal(err)
	}
}

// The admin log: who did what to which account or invitation, the newest
// first, a page at a time. It names accounts by identifier and shows their
// address as it is now, none when the account is gone. An entry written by
// hand, or older than 400 days, is not shown.
func TestTheAdminLog(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	carl, _ := w.signUp("carl@example.org")
	w.now = w.now.Add(time.Hour)
	first := w.now
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Minute)
	if _, err := w.as(ana).BlockAccount(ctx, carl.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Minute)
	made, _ := w.as(ana).CreateInvitations(ctx, 1)
	w.now = w.now.Add(time.Minute)
	if _, err := w.as(boss).SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "a", ClientSecret: "b"}); err != nil {
		t.Fatal(err)
	}
	// The operator with the token is counted, and not in the log.
	w.operator(w.svc.UnblockAccount, carl.Account.ID)

	log, err := w.svc.AdminLog(ctx, "", 10)
	if err != nil || len(log.Entries) != 4 || log.Next != "" {
		t.Fatalf("the log: %+v %v", log, err)
	}
	want := []struct {
		kind, admin, account, invitation string
	}{
		{ActionProviderAppSet, boss.me.Account.ID, "", ""},
		{ActionInvitationsMade, ana.me.Account.ID, "", made.IDs[0]},
		{ActionBlock, ana.me.Account.ID, carl.Account.ID, ""},
		{ActionAdminMade, boss.me.Account.ID, ana.me.Account.ID, ""},
	}
	for i, e := range log.Entries {
		account := ""
		if e.Account != nil {
			account = e.Account.ID
		}
		if e.Kind != want[i].kind || e.Admin.ID != want[i].admin || account != want[i].account || e.Invitation != want[i].invitation {
			t.Errorf("entry %d: %+v, want %+v", i, e, want[i])
		}
	}
	if e := log.Entries[2]; e.Admin.Address != "ana@example.org" || e.Account.Address != "carl@example.org" || !log.Entries[3].At.Equal(first) {
		t.Fatalf("the addresses: %+v", e)
	}
	// A page at a time.
	page, err := w.svc.AdminLog(ctx, "", 3)
	if err != nil || len(page.Entries) != 3 || page.Next == "" {
		t.Fatalf("a page: %+v %v", page, err)
	}
	rest, err := w.svc.AdminLog(ctx, page.Next, 3)
	if err != nil || len(rest.Entries) != 1 || rest.Entries[0].Kind != ActionAdminMade || rest.Next != "" {
		t.Fatalf("the rest: %+v %v", rest, err)
	}
	for _, limit := range []int{0, -1, domain.MaxAccountsPage + 1} {
		if _, err := w.svc.AdminLog(ctx, "", limit); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
	for _, after := range []string{"x", "2026", page.Next + "x", strings.Repeat("9", 19)} {
		if _, err := w.svc.AdminLog(ctx, after, 3); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Errorf("after %q: %v", after, err)
		}
	}

	// Written by hand: shown not.
	key := logKey(w.now.Add(time.Hour))
	w.mem.SetEntry(store.Entry{Partition: adminLogPartition, Key: key, Value: []byte(`{"v":1,"kind":"block","admin":"` + boss.me.Account.ID + `"}`)})
	if log, _ := w.svc.AdminLog(ctx, "", 10); len(log.Entries) != 4 {
		t.Fatalf("an entry by hand was shown: %+v", log.Entries[0])
	}
	// Carl's account is removed: the entries stay, without his address.
	w.operator(w.svc.DeleteAccount, carl.Account.ID)
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	log, _ = w.svc.AdminLog(ctx, "", 10)
	if e := log.Entries[2]; e.Account == nil || e.Account.ID != carl.Account.ID || e.Account.Address != "" {
		t.Fatalf("an entry about an account that is gone: %+v", e.Account)
	}
	// Kept 400 days, by the time that was signed.
	w.now = first.Add(400 * 24 * time.Hour)
	if log, _ := w.svc.AdminLog(ctx, "", 10); len(log.Entries) != 3 {
		t.Fatalf("after 400 days: %d entries", len(log.Entries))
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == adminLogPartition && e.Key != key && !e.ExpiresAt.After(first.Add(399*24*time.Hour)) {
			t.Fatalf("an entry of the log expires at %v", e.ExpiresAt)
		}
	}
	// The log is in the storage, never in the enclave's log.
	w.quiet("example.org", superAdmin, ana.me.Account.ID, carl.Account.ID, made.IDs[0])
}

// The counts of the first page of the panel, read from the signed records:
// a thousand accounts at most, and incomplete when there are more, or some
// cannot be read.
func TestStats(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana, anaCookie := w.signUp("ana@example.org")
	bea, _ := w.signUp("bea@example.org")
	w.signUp("carl@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(anaCookie))); err != nil {
		t.Fatal(err)
	}
	w.operator(w.svc.BlockAccount, ana.Account.ID)
	w.operator(w.svc.BlockAccount, bea.Account.ID)
	// One invitation for each sign-up was used; these two were not.
	if _, err := w.svc.CreateInvitations(ctx, 2); err != nil {
		t.Fatal(err)
	}
	got, err := w.as(boss).Stats(ctx)
	want := contracts.Stats{Accounts: contracts.AccountCounts{Active: 3, Closing: 1, Blocked: 2, Total: 4}, Invitations: contracts.InvitationCounts{Unused: 2}, Complete: true}
	if err != nil || *got != want {
		t.Fatalf("the counts: %+v %v, want %+v", got, err, want)
	}
	// An entry of the list written by hand is no account.
	w.mem.SetEntry(store.Entry{Partition: accountsPartition, Key: timedKey(w.now, newID("acc")), Value: []byte("{}")})
	if got, _ := w.svc.Stats(ctx); *got != want {
		t.Fatalf("with an entry by hand: %+v", got)
	}
	// An account whose record does not verify is there, and not counted.
	name := w.newest(bea.Account.ID)
	rec := w.record(name)
	broken := rec
	broken.Value = append([]byte{}, rec.Value...)
	broken.Value[len(broken.Value)-2] ^= 1
	w.mem.SetRecord(broken)
	if got, _ := w.svc.Stats(ctx); got.Complete || got.Accounts.Total != 3 {
		t.Fatalf("with one that cannot be read: %+v", got)
	}
	w.mem.SetRecord(rec)
	// A thousand are counted and no more. The list of accounts is pointers:
	// ones that lead nowhere fill it as well as accounts do.
	for i := range maxCounted - 5 {
		id := newID("acc")
		if err := w.svc.point(ctx, accountsPartition, timedKey(w.now.Add(time.Duration(i)), id), id, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := w.svc.Stats(ctx); !got.Complete || got.Accounts.Total != 4 {
		t.Fatalf("exactly a thousand: %+v", got)
	}
	id := newID("acc")
	if err := w.svc.point(ctx, accountsPartition, timedKey(w.now.Add(time.Hour), id), id, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.svc.Stats(ctx); got.Complete {
		t.Fatalf("more than a thousand: %+v", got)
	}
	// And more invitations than the list shows.
	w2 := newWorld(t)
	for range domain.MaxInvitationsListed/MaxInvitations + 1 {
		if _, err := w2.svc.CreateInvitations(ctx, MaxInvitations); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := w2.svc.Stats(ctx); got.Complete || got.Invitations.Unused != domain.MaxInvitationsListed {
		t.Fatalf("more invitations than are listed: %+v", got)
	}
}

// An account that was an admin, removed, leaves nothing in the list of
// admins.
func TestARemovedAdminLeavesNothing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.operator(w.svc.DeleteAccount, ana.me.Account.ID)
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	// The log keeps what was done to it, by its identifier, and never its
	// address.
	for _, left := range w.stored(ana.me.Account.ID, "ana@example.org") {
		if !strings.HasPrefix(left, "entry "+adminLogPartition+" ") {
			t.Fatalf("left: %v", left)
		}
	}
	if left := w.stored("ana@example.org"); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
	if list, _ := w.svc.Admins(ctx); len(list.Admins) != 1 {
		t.Fatalf("the admins: %+v", list)
	}
}

// A removal whose pointer of an admin cannot be deleted stops there, and is
// done again.
func TestARemovalOfAnAdminThatFailsHalfway(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	bea := w.account("bea@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, bea.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.operator(w.svc.DeleteAccount, bea.me.Account.ID)
	w.now = w.now.Add(domain.AccountClosing)
	f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{"delete admins": errors.New("down")}, skip: map[string]int{}}
	w.svc.cfg.Entries = f
	if err := w.svc.RemoveDue(ctx); err == nil || w.doc(bea.me.Account.ID) == nil {
		t.Fatalf("removed with the list of admins down: %v", err)
	}
	delete(f.fail, "delete admins")
	if err := w.svc.RemoveDue(ctx); err != nil || w.doc(bea.me.Account.ID) != nil {
		t.Fatalf("removed again: %v", err)
	}
}

// The super admin's address must be one: an image with another is no
// service. A pointer from it that leads nowhere is no super admin.
func TestTheSuperAdminsAddress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	if _, err := New(Config{Records: w.anchored, Entries: w.mem, Keys: w.svc.cfg.Keys, Mailer: w.out, AppURL: "https://app.permaily.test", SuperAdmin: "not an address"}); err == nil {
		t.Fatal("a service with a super admin that is no address")
	}
	svc, err := New(Config{Records: w.anchored, Entries: w.mem, Keys: w.svc.cfg.Keys, Mailer: w.out, AppURL: "https://app.permaily.test", SuperAdmin: " Boss@Permaily.TEST "})
	if err != nil || svc.cfg.SuperAdmin != superAdmin {
		t.Fatalf("the address as sign-in finds it: %q %v", svc.cfg.SuperAdmin, err)
	}
	if err := w.svc.point(ctx, loginPartition(superAdmin), loginKey, newID("acc"), 0); err != nil {
		t.Fatal(err)
	}
	if list, err := w.svc.Admins(ctx); err != nil || len(list.Admins) != 0 {
		t.Fatalf("a pointer that leads nowhere: %+v %v", list, err)
	}
}

// A storage that fails is an error, never an admin more or less.
func TestAdminFailures(t *testing.T) {
	down := errors.New("down")
	setup := func(t *testing.T) (*world, *failing, *adminAccount, *adminAccount) {
		w := newWorld(t)
		boss := w.account(superAdmin, true)
		ana := w.account("ana@example.org", true)
		if _, err := w.as(boss).MakeAdmin(context.Background(), ana.me.Account.ID); err != nil {
			t.Fatal(err)
		}
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f, boss, ana
	}
	ctx := context.Background()
	t.Run("reading", func(t *testing.T) {
		w, f, boss, _ := setup(t)
		admin := w.as(boss)
		for _, op := range []string{"get entry", "derive", "list admins", "get record"} {
			f.fail[op] = down
			if _, err := w.svc.Admins(ctx); !errors.Is(err, down) && op != "get record" {
				t.Errorf("admins, %s: %v", op, err)
			}
			delete(f.fail, op)
		}
		// Without a super admin, the key is first needed for the list.
		w.svc.cfg.SuperAdmin = ""
		f.fail["derive"] = down
		if _, err := w.svc.Admins(ctx); !errors.Is(err, down) {
			t.Errorf("admins without a super admin, derive: %v", err)
		}
		delete(f.fail, "derive")
		w.svc.cfg.SuperAdmin = superAdmin
		// A record that cannot be read leaves its admin out, and is logged.
		f.fail["get record"], f.skip["get record"] = down, 0
		w.svc.cfg.SuperAdmin = ""
		if list, err := w.svc.Admins(ctx); err != nil || len(list.Admins) != 0 || !strings.Contains(w.logs.String(), "records of admins could not be read") {
			t.Fatalf("an admin that cannot be read: %+v %v", list, err)
		}
		delete(f.fail, "get record")
		for _, op := range []string{"derive", "list admin-log"} {
			f.fail[op] = down
			if _, err := admin.AdminLog(ctx, "", 10); !errors.Is(err, down) {
				t.Errorf("the log, %s: %v", op, err)
			}
			delete(f.fail, op)
		}
		for _, op := range []string{"derive", "list accounts", "list invitations"} {
			f.fail[op] = down
			if _, err := admin.Stats(ctx); !errors.Is(err, down) {
				t.Errorf("the counts, %s: %v", op, err)
			}
			delete(f.fail, op)
		}
		// An account the log names that cannot be read has no address there.
		f.fail["get record"] = down
		if log, err := admin.AdminLog(ctx, "", 10); err != nil || log.Entries[0].Admin.Address != "" {
			t.Fatalf("the log with the records down: %+v %v", log, err)
		}
	})
	t.Run("what may be acted on cannot be read", func(t *testing.T) {
		w, f, boss, ana := setup(t)
		admin := w.as(boss)
		f.fail["get record"] = down
		if _, err := admin.BlockAccount(ctx, ana.me.Account.ID); !errors.Is(err, down) {
			t.Errorf("blocking: %v", err)
		}
		if _, err := admin.RemoveAdmin(ctx, ana.me.Account.ID); !errors.Is(err, down) {
			t.Errorf("removing an admin: %v", err)
		}
	})
	t.Run("changing", func(t *testing.T) {
		w, f, boss, ana := setup(t)
		admin := w.as(boss)
		// The pointer cannot be written: nothing is.
		f.fail["put admins"] = down
		if _, err := admin.MakeAdmin(ctx, w.account("bea@example.org", true).me.Account.ID); !errors.Is(err, down) {
			t.Fatalf("making with the list down: %v", err)
		}
		delete(f.fail, "put admins")
		// The pointer cannot be deleted: the admin is removed all the same.
		f.fail["delete admins"] = down
		if list, err := admin.RemoveAdmin(ctx, ana.me.Account.ID); err != nil || len(list.Admins) != 1 {
			t.Fatalf("removing with the list down: %+v %v", list, err)
		}
		delete(f.fail, "delete admins")
		if !strings.Contains(w.logs.String(), "the pointer of an admin that was removed could not be deleted") {
			t.Fatal("not logged")
		}
		// The log cannot be written: what was done is done, and logged.
		f.fail["put admin-log"] = down
		if _, err := admin.MakeAdmin(ctx, ana.me.Account.ID); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(w.logs.String(), `msg="accounts: an entry of the admin log could not be written" kind=admin_made`) {
			t.Fatal("not logged")
		}
		delete(f.fail, "put admin-log")
		f.fail["derive"] = down
		if err := w.svc.writeLog(ctx, logDoc{V: 1}); !errors.Is(err, down) {
			t.Fatalf("writing the log without the key: %v", err)
		}
		delete(f.fail, "derive")
		keys := w.svc.cfg.Keys
		w.svc.cfg.Keys = shortKeys{}
		if err := w.svc.writeLog(ctx, logDoc{V: 1}); err == nil {
			t.Fatal("the log signed with a key that is no key")
		}
		w.svc.cfg.Keys = keys
		// The record cannot be written: nothing changed.
		eli := w.account("eli@example.org", true)
		f.fail["put record"] = down
		if _, err := admin.RemoveAdmin(ctx, ana.me.Account.ID); !errors.Is(err, down) || w.role(ana) != contracts.AdminRole {
			t.Fatalf("removing with the records down: %v", err)
		}
		if _, err := admin.MakeAdmin(ctx, eli.me.Account.ID); !errors.Is(err, down) {
			t.Fatalf("making with the records down: %v", err)
		}
		delete(f.fail, "put record")
		f.fail["list admins"] = down
		for what, err := range map[string]error{
			"make":   second2(admin.MakeAdmin(ctx, ana.me.Account.ID)),
			"remove": second2(admin.RemoveAdmin(ctx, ana.me.Account.ID)),
		} {
			if !errors.Is(err, down) {
				t.Errorf("%s with the list down after: %v", what, err)
			}
		}
		delete(f.fail, "list admins")
		who := w.who(ana.cookie)
		f.fail["get record"] = down
		if _, err := w.svc.Admin(ctx, who); !errors.Is(err, down) {
			t.Fatalf("who is an admin with the records down: %v", err)
		}
	})
}

// An admin who may not act on an account is told so before being asked to
// confirm it's them: confirming would be for nothing. Another admin's account
// to an admin who is not the super admin, and the super admin's account to
// anyone who would block or delete it, or remove it as an admin.
func TestAnAdminWhoMayNotActIsToldBeforeConfirming(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	bea := w.account("bea@example.org", true)
	carl, _ := w.signUp("carl@example.org")
	for _, a := range []*adminAccount{ana, bea} {
		if _, err := w.as(boss).MakeAdmin(ctx, a.me.Account.ID); err != nil {
			t.Fatal(err)
		}
	}
	w.later()
	stale := func(a *adminAccount) *Admin {
		t.Helper()
		admin, err := w.svc.Admin(ctx, w.who(a.cookie))
		if err != nil {
			t.Fatal(err)
		}
		return admin
	}
	anaStale, bossStale := stale(ana), stale(boss)
	beaID, bossID := bea.me.Account.ID, boss.me.Account.ID
	for what, err := range map[string]error{
		"ana blocks bea":           second2(anaStale.BlockAccount(ctx, beaID)),
		"ana unblocks bea":         second2(anaStale.UnblockAccount(ctx, beaID)),
		"ana deletes bea":          second2(anaStale.DeleteAccount(ctx, beaID)),
		"ana cancels bea's":        second2(anaStale.KeepAccount(ctx, beaID)),
		"ana signs bea out":        second2(anaStale.CloseAccountSessions(ctx, beaID)),
		"ana blocks the boss":      second2(anaStale.BlockAccount(ctx, bossID)),
		"ana blocks herself":       second2(anaStale.BlockAccount(ctx, ana.me.Account.ID)),
		"the boss blocks himself":  second2(bossStale.BlockAccount(ctx, bossID)),
		"the boss deletes himself": second2(bossStale.DeleteAccount(ctx, bossID)),
		"the boss removes himself": second2(bossStale.RemoveAdmin(ctx, bossID)),
		"ana makes an admin":       second2(anaStale.MakeAdmin(ctx, carl.Account.ID)),
		"ana sets a provider":      second2(anaStale.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "a", ClientSecret: "b"})),
	} {
		if !errors.Is(err, contracts.ErrSuperAdminOnly) {
			t.Errorf("%s: %v", what, err)
		}
	}
	// What they may do still asks for the proof first.
	for what, err := range map[string]error{
		"ana blocks carl":            second2(anaStale.BlockAccount(ctx, carl.Account.ID)),
		"the boss blocks bea":        second2(bossStale.BlockAccount(ctx, beaID)),
		"the boss signs himself out": second2(bossStale.CloseAccountSessions(ctx, bossID)),
		"ana, an account not there":  second2(anaStale.BlockAccount(ctx, newID("acc"))),
	} {
		if !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Errorf("%s: %v", what, err)
		}
	}
}

// The log names the provider whose application an admin set or removed.
func TestTheLogNamesTheProvider(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	if _, err := w.as(boss).SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "a", ClientSecret: "b"}); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Second)
	if err := w.as(boss).RemoveProviderApp(ctx, "github"); err != nil {
		t.Fatal(err)
	}
	log, err := w.svc.AdminLog(ctx, "", 10)
	if err != nil || len(log.Entries) != 2 {
		t.Fatalf("the log: %+v %v", log, err)
	}
	for i, kind := range []string{ActionProviderAppRemove, ActionProviderAppSet} {
		if e := log.Entries[i]; e.Kind != kind || e.Provider != "github" || e.Account != nil {
			t.Errorf("entry %d: %+v", i, e)
		}
	}
}
