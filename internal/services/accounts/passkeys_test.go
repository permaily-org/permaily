package accounts

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// Who the passkeys of a world are for, and the page that asks for them.
const (
	testRP     = "app.permaily.test"
	testOrigin = "https://app.permaily.test"
)

func (w *world) who(cookie string) *Authenticated {
	w.t.Helper()
	who, err := w.svc.Authenticate(context.Background(), cookie)
	if err != nil {
		w.t.Fatal(err)
	}
	return who
}

// create asks to make a passkey and has the authenticator answer.
func (w *world) create(who *Authenticated, auth *webauthntest.Authenticator, name string) contracts.PasskeyFinish {
	w.t.Helper()
	started, err := w.svc.StartPasskey(context.Background(), w.fresh(who))
	if err != nil {
		w.t.Fatal(err)
	}
	in := contracts.PasskeyFinish{Ceremony: started.Ceremony, Name: name}
	id, clientData, attestation := auth.Create(started.Options.RP.ID, testOrigin, started.Options.Challenge, started.Options.User.ID)
	in.Credential.RawID, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject = id, clientData, attestation
	return in
}

// addPasskey makes a passkey for the account behind a session.
func (w *world) addPasskey(who *Authenticated, auth *webauthntest.Authenticator, name string) *contracts.Passkey {
	w.t.Helper()
	made, err := w.svc.FinishPasskey(context.Background(), who, w.create(who, auth, name))
	if err != nil {
		w.t.Fatal(err)
	}
	return made
}

// assert asks to sign in with a passkey and has the authenticator answer
// with the credential id.
func (w *world) assert(auth *webauthntest.Authenticator, id []byte) contracts.PasskeySignIn {
	w.t.Helper()
	in, err := w.asserting(auth, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return in
}

// asserting is assert, with the failure to start the ceremony.
func (w *world) asserting(auth *webauthntest.Authenticator, id []byte) (contracts.PasskeySignIn, error) {
	started, err := w.svc.StartPasskeySignIn(context.Background())
	if err != nil {
		return contracts.PasskeySignIn{}, err
	}
	in := contracts.PasskeySignIn{Ceremony: started.Ceremony}
	in.Credential.RawID = id
	r := &in.Credential.Response
	r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(started.Options.RPID, testOrigin, started.Options.Challenge, id)
	return in, nil
}

func (w *world) signInWith(auth *webauthntest.Authenticator, id []byte) (*contracts.Me, string, error) {
	w.t.Helper()
	in, err := w.asserting(auth, id)
	if err != nil {
		return nil, "", err
	}
	return w.svc.FinishPasskeySignIn(context.Background(), in, Browser{UserAgent: "passkey device"})
}

// passkeyOnly turns "passkey only" on and returns the recovery codes.
func (w *world) passkeyOnly(who *Authenticated) []string {
	w.t.Helper()
	state, err := w.svc.SetPasskeyOnly(context.Background(), w.fresh(who), contracts.PasskeyOnly{On: true})
	if err != nil || !state.PasskeyOnly || len(state.RecoveryCodes) != domain.RecoveryCodes {
		w.t.Fatalf("turning passkey only on: %+v %v", state, err)
	}
	return state.RecoveryCodes
}

func (w *world) recover(address, code string) (*contracts.Me, string, error) {
	return w.svc.Recover(context.Background(), contracts.Recovery{Address: address, Code: code}, Browser{UserAgent: "recovered device"})
}

// newest returns the name the newest version of an account's record is
// stored under: what whoever runs the storage would write to.
func (w *world) newest(accountID string) string {
	w.t.Helper()
	name := ""
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "account-"+accountID+"@") {
			name = n // the names are in order, and the version is part of them
		}
	}
	if name == "" {
		w.t.Fatal("no record of the account")
	}
	return name
}

func (w *world) record(name string) store.Record {
	w.t.Helper()
	rec, err := w.mem.GetRecord(context.Background(), name)
	if err != nil {
		w.t.Fatal(err)
	}
	return *rec
}

func TestPasskeys(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)

	// What the browser is told to make: a passkey for this app alone, that
	// knows which account it is for and asks for the person.
	started, err := w.svc.StartPasskey(ctx, w.fresh(who))
	if err != nil {
		t.Fatal(err)
	}
	o := started.Options
	if !validID("cer", started.Ceremony) || o.RP != (contracts.RelyingParty{ID: testRP, Name: "Permaily"}) ||
		string(o.User.ID) != me.Account.ID || o.User.Name != "ana@example.org" || o.User.DisplayName != "ana@example.org" ||
		len(o.Challenge) != 32 || o.Timeout != 300000 || o.Attestation != "none" || len(o.ExcludeCredentials) != 0 ||
		o.AuthenticatorSelection != (contracts.AuthenticatorSelection{ResidentKey: "required", UserVerification: "required"}) {
		t.Fatalf("creation options: %+v", started)
	}
	var algs []int
	for _, p := range o.PubKeyCredParams {
		if p.Type != "public-key" {
			t.Fatalf("parameters: %+v", o.PubKeyCredParams)
		}
		algs = append(algs, p.Alg)
	}
	if !slices.Equal(algs, webauthn.Algorithms) {
		t.Fatalf("algorithms offered: %v", algs)
	}
	// In the shape PublicKeyCredential.parseCreationOptionsFromJSON reads.
	raw, _ := json.Marshal(started)
	for _, part := range []string{
		`{"ceremony":"cer_`, `"options":{"rp":{"id":"app.permaily.test","name":"Permaily"}`, `"challenge":"` + o.Challenge.String() + `"`,
		`"pubKeyCredParams":[{"type":"public-key","alg":-8},{"type":"public-key","alg":-7},{"type":"public-key","alg":-257}]`,
		`"excludeCredentials":[]`, `"authenticatorSelection":{"residentKey":"required","userVerification":"required"}`, `"attestation":"none"`,
		`"user":{"id":"` + contracts.Bytes(me.Account.ID).String() + `","name":"ana@example.org","displayName":"ana@example.org"}`,
	} {
		if !strings.Contains(string(raw), part) {
			t.Errorf("creation options lack %s:\n%s", part, raw)
		}
	}
	// A name the account has is shown instead of the address.
	if _, err := w.svc.change(ctx, me.Account.ID, func(doc *accountDoc) error { doc.Name = "Ana"; return nil }); err != nil {
		t.Fatal(err)
	}
	if named, _ := w.svc.StartPasskey(ctx, w.fresh(who)); named.Options.User.DisplayName != "Ana" || named.Options.User.Name != "ana@example.org" {
		t.Fatalf("user of a named account: %+v", named.Options.User)
	}

	// A passkey of each kind of key is made, listed, and signs in.
	var auths []*webauthntest.Authenticator
	var made []*contracts.Passkey
	for i, alg := range webauthn.Algorithms {
		auth := webauthntest.New(alg)
		auth.Synced = i == 0
		w.now = w.now.Add(time.Minute)
		p := w.addPasskey(who, auth, fmt.Sprintf("  key %d ", i))
		if len(p.ID) == 0 || p.Name != fmt.Sprintf("key %d", i) || !p.CreatedAt.Equal(w.now) || p.Synced != (i == 0) {
			t.Fatalf("passkey %d: %+v", i, p)
		}
		auths, made = append(auths, auth), append(made, p)
	}
	list, err := w.svc.Passkeys(ctx, who)
	if err != nil || len(list.Passkeys) != 3 || list.PasskeyOnly || list.RecoveryCodesLeft != 0 {
		t.Fatalf("passkeys: %+v %v", list, err)
	}
	for i, p := range list.Passkeys {
		if !bytes.Equal(p.ID, made[i].ID) || p.Name != made[i].Name || !p.CreatedAt.Equal(made[i].CreatedAt) || p.Synced != made[i].Synced {
			t.Fatalf("listed %d: %+v, made %+v", i, p, made[i])
		}
	}
	// The ones the account has are not made twice by their authenticator.
	again, _ := w.svc.StartPasskey(ctx, w.fresh(who))
	if ex := again.Options.ExcludeCredentials; len(ex) != 3 || ex[0].Type != "public-key" || !bytes.Equal(ex[2].ID, made[2].ID) {
		t.Fatalf("excluded: %+v", ex)
	}

	// Signing in asks for no address and names no passkey: the answer says
	// which account it is for.
	request, err := w.svc.StartPasskeySignIn(ctx)
	if err != nil || !validID("cer", request.Ceremony) || len(request.Options.Challenge) != 32 {
		t.Fatalf("request: %+v %v", request, err)
	}
	raw, _ = json.Marshal(request)
	if want := `{"ceremony":"` + request.Ceremony + `","options":{"challenge":"` + request.Options.Challenge.String() + `","timeout":300000,"rpId":"app.permaily.test","userVerification":"required"}}`; string(raw) != want {
		t.Fatalf("request options:\n%s\nwant\n%s", raw, want)
	}
	for i, auth := range auths {
		in := w.assert(auth, made[i].ID)
		signed, passkeyCookie, err := w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: "passkey device"})
		if err != nil || signed.Account.ID != me.Account.ID || !signed.Session.Current || signed.Session.Device != "passkey device" {
			t.Fatalf("signing in with passkey %d: %+v %v", i, signed, err)
		}
		// A session like any other.
		if got := w.who(passkeyCookie); got.AccountID != me.Account.ID || got.Session.ID != signed.Session.ID {
			t.Fatalf("the session of passkey %d: %+v", i, got)
		}
		// The answer worked once.
		if _, _, err := w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidPasskey) {
			t.Fatalf("the same answer again: %v", err)
		}
	}

	// Renamed and removed by the identifier the list shows.
	id := made[1].ID.String()
	renamed, err := w.svc.RenamePasskey(ctx, who, id, contracts.PasskeyRename{Name: " Work laptop "})
	if err != nil || renamed.Name != "Work laptop" || !bytes.Equal(renamed.ID, made[1].ID) || !renamed.CreatedAt.Equal(made[1].CreatedAt) {
		t.Fatalf("renamed: %+v %v", renamed, err)
	}
	if list, _ := w.svc.Passkeys(ctx, who); list.Passkeys[1].Name != "Work laptop" || list.Passkeys[0].Name != "key 0" {
		t.Fatalf("after renaming: %+v", list.Passkeys)
	}
	for _, name := range []string{"", "  ", strings.Repeat("a", 65), "two\nlines"} {
		if _, err := w.svc.RenamePasskey(ctx, who, id, contracts.PasskeyRename{Name: name}); !errors.Is(err, contracts.ErrInvalidName) {
			t.Errorf("renamed to %q: %v", name, err)
		}
		in := w.create(who, webauthntest.New(webauthntest.ES256), name)
		if _, err := w.svc.FinishPasskey(ctx, who, in); !errors.Is(err, contracts.ErrInvalidName) {
			t.Errorf("made with the name %q: %v", name, err)
		}
		// A name asked again does not end the ceremony.
		in.Name = "ok"
		if _, err := w.svc.FinishPasskey(ctx, who, in); err != nil {
			t.Fatalf("after a name that was refused: %v", err)
		}
		if err := w.svc.RemovePasskey(ctx, w.fresh(who), in.Credential.RawID.String()); err != nil {
			t.Fatal(err)
		}
	}
	other := contracts.Bytes("no such passkey").String()
	for _, unknown := range []string{"", other, "not base64url!", made[1].ID.String() + "="} {
		if _, err := w.svc.RenamePasskey(ctx, who, unknown, contracts.PasskeyRename{Name: "x"}); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("renaming %q: %v", unknown, err)
		}
		if err := w.svc.RemovePasskey(ctx, w.fresh(who), unknown); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("removing %q: %v", unknown, err)
		}
	}
	// Nobody renames or removes a passkey of another account.
	_, eveCookie := w.signUp("eve@example.org")
	eve := w.who(eveCookie)
	if err := w.svc.RemovePasskey(ctx, w.fresh(eve), id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("removing another account's passkey: %v", err)
	}
	if _, err := w.svc.RenamePasskey(ctx, eve, id, contracts.PasskeyRename{Name: "mine"}); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("renaming another account's passkey: %v", err)
	}

	// A passkey that was removed signs nobody in; the others still do.
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.signInWith(auths[1], made[1].ID); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("a removed passkey: %v", err)
	}
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("removing it again: %v", err)
	}
	if _, _, err := w.signInWith(auths[2], made[2].ID); err != nil {
		t.Fatalf("the passkey next to a removed one: %v", err)
	}
	// Without "passkey only" the last one can go too: mail still signs in.
	for _, i := range []int{0, 2} {
		if err := w.svc.RemovePasskey(ctx, w.fresh(who), made[i].ID.String()); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := w.svc.Passkeys(ctx, who); len(list.Passkeys) != 0 || list.Passkeys == nil {
		t.Fatalf("after removing all: %+v", list)
	}

	// Nothing logged names the person, the account, a passkey or a ceremony.
	logs := w.logs.String()
	if !strings.Contains(logs, "accounts: passkey made") || !strings.Contains(logs, "accounts: passkey removed") {
		t.Fatalf("the logs do not say a passkey was made and removed:\n%s", logs)
	}
	secrets := []string{"ana@", "example.org", me.Account.ID, started.Ceremony, request.Ceremony, "Work laptop", "key 0"}
	for _, p := range made {
		secrets = append(secrets, p.ID.String(), hex.EncodeToString(p.ID))
	}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, logs)
		}
	}
}

// Every way an answer about a passkey can be wrong is the same error, and
// what is logged of it holds nothing of the answer.
func TestPasskeyRefusals(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	_, eveCookie := w.signUp("eve@example.org")
	eve := w.who(eveCookie)
	auth := webauthntest.New(webauthntest.ES256)
	mine := w.addPasskey(who, auth, "mine")
	evil := webauthntest.New(webauthntest.ES256)

	// Making one.
	creations := map[string]func() (*Authenticated, contracts.PasskeyFinish){
		"an unknown ceremony": func() (*Authenticated, contracts.PasskeyFinish) {
			in := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			in.Ceremony = "cer_" + strings.Repeat("a", 26)
			return who, in
		},
		"no ceremony": func() (*Authenticated, contracts.PasskeyFinish) {
			in := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			in.Ceremony = ""
			return who, in
		},
		"a ceremony that ended": func() (*Authenticated, contracts.PasskeyFinish) {
			in := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			w.now = w.now.Add(domain.PasskeyCeremonyLifetime)
			return who, in
		},
		"the ceremony of another account": func() (*Authenticated, contracts.PasskeyFinish) {
			return eve, w.create(who, webauthntest.New(webauthntest.ES256), "x")
		},
		"the ceremony of a sign-in": func() (*Authenticated, contracts.PasskeyFinish) {
			in := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			request, _ := w.svc.StartPasskeySignIn(ctx)
			a := webauthntest.New(webauthntest.ES256)
			in.Ceremony = request.Ceremony
			in.Credential.RawID, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject = a.Create(testRP, testOrigin, request.Options.Challenge, []byte(me.Account.ID))
			return who, in
		},
		"another page": func() (*Authenticated, contracts.PasskeyFinish) {
			started, _ := w.svc.StartPasskey(ctx, w.fresh(who))
			in := contracts.PasskeyFinish{Ceremony: started.Ceremony, Name: "x"}
			in.Credential.RawID, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject = evil.Create(testRP, "https://permaily.test", started.Options.Challenge, started.Options.User.ID)
			return who, in
		},
		"another relying party": func() (*Authenticated, contracts.PasskeyFinish) {
			a := webauthntest.New(webauthntest.ES256)
			a.RPID = "permaily.test"
			return who, w.create(who, a, "x")
		},
		"nobody was asked": func() (*Authenticated, contracts.PasskeyFinish) {
			a := webauthntest.New(webauthntest.ES256)
			a.NoVerification = true
			return who, w.create(who, a, "x")
		},
		"another challenge": func() (*Authenticated, contracts.PasskeyFinish) {
			first := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			second := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			second.Ceremony = first.Ceremony
			return who, second
		},
		"an answer that is nothing": func() (*Authenticated, contracts.PasskeyFinish) {
			started, _ := w.svc.StartPasskey(ctx, w.fresh(who))
			return who, contracts.PasskeyFinish{Ceremony: started.Ceremony, Name: "x"}
		},
		"another identifier than the one proved": func() (*Authenticated, contracts.PasskeyFinish) {
			in := w.create(who, webauthntest.New(webauthntest.ES256), "x")
			in.Credential.RawID = mine.ID
			return who, in
		},
		"an identifier the account has": func() (*Authenticated, contracts.PasskeyFinish) {
			// The authenticator data of a creation is not signed: the
			// identifier in it is replaced by the one of the passkey made.
			a := webauthntest.New(webauthntest.ES256)
			a.EditAuthData = func(data []byte) []byte {
				copy(data[55:], mine.ID) // after the hash, the flags, the counter, the model and the length
				return data
			}
			in := w.create(who, a, "x")
			in.Credential.RawID = mine.ID
			return who, in
		},
	}
	for name, make := range creations {
		as, in := make()
		got, err := w.svc.FinishPasskey(ctx, as, in)
		if got != nil || !errors.Is(err, contracts.ErrInvalidPasskey) || err.Error() != contracts.ErrInvalidPasskey.Error() {
			t.Errorf("making a passkey with %s: %+v, %v", name, got, err)
		}
		// The ceremony is over, right or wrong.
		if _, ok, _ := w.svc.end(ctx, in.Ceremony, as.AccountID); ok {
			t.Errorf("%s: the ceremony is still running", name)
		}
	}
	if list, _ := w.svc.Passkeys(ctx, who); len(list.Passkeys) != 1 {
		t.Fatalf("a refused passkey was kept: %+v", list.Passkeys)
	}
	if list, _ := w.svc.Passkeys(ctx, eve); len(list.Passkeys) != 0 {
		t.Fatalf("a passkey in another account: %+v", list.Passkeys)
	}
	// A wrong answer ends the ceremony for the right one too.
	right := w.create(who, webauthntest.New(webauthntest.ES256), "x")
	wrong := right
	wrong.Credential.Response.AttestationObject = nil
	for _, in := range []contracts.PasskeyFinish{wrong, right} {
		if _, err := w.svc.FinishPasskey(ctx, who, in); !errors.Is(err, contracts.ErrInvalidPasskey) {
			t.Fatalf("a ceremony tried twice: %v", err)
		}
	}

	// Using one.
	stranger, _, _ := evil.Create(testRP, testOrigin, make([]byte, 32), []byte(me.Account.ID))
	assertions := map[string]func() contracts.PasskeySignIn{
		"an unknown ceremony": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Ceremony = "cer_" + strings.Repeat("a", 26)
			return in
		},
		"a ceremony that ended": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			w.now = w.now.Add(domain.PasskeyCeremonyLifetime)
			return in
		},
		"the ceremony of making one": func() contracts.PasskeySignIn {
			started, _ := w.svc.StartPasskey(ctx, w.fresh(who))
			in := contracts.PasskeySignIn{Ceremony: started.Ceremony}
			in.Credential.RawID = mine.ID
			r := &in.Credential.Response
			r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(testRP, testOrigin, started.Options.Challenge, mine.ID)
			return in
		},
		"another challenge": func() contracts.PasskeySignIn {
			first, second := w.assert(auth, mine.ID), w.assert(auth, mine.ID)
			second.Ceremony = first.Ceremony
			return second
		},
		"an account that is not there": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.UserHandle = []byte("acc_" + strings.Repeat("a", 26))
			return in
		},
		"no account": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.UserHandle = nil
			return in
		},
		"an address for an account": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.UserHandle = []byte("ana@example.org")
			return in
		},
		"a passkey of another account": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.UserHandle = []byte(eve.AccountID)
			return in
		},
		// Someone's own authenticator, told it holds a passkey of the account.
		"a passkey the account does not have": func() contracts.PasskeySignIn {
			return w.assert(evil, stranger)
		},
		"another key under the identifier of a passkey": func() contracts.PasskeySignIn {
			in := w.assert(evil, stranger)
			in.Credential.RawID = mine.ID
			return in
		},
		"a signature that was changed": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.Signature[len(in.Credential.Response.Signature)-1] ^= 1
			return in
		},
		"no signature": func() contracts.PasskeySignIn {
			in := w.assert(auth, mine.ID)
			in.Credential.Response.Signature = nil
			return in
		},
		"another page": func() contracts.PasskeySignIn {
			started, _ := w.svc.StartPasskeySignIn(ctx)
			in := contracts.PasskeySignIn{Ceremony: started.Ceremony}
			in.Credential.RawID = mine.ID
			r := &in.Credential.Response
			r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(testRP, "https://evil.example", started.Options.Challenge, mine.ID)
			return in
		},
		"nobody was asked": func() contracts.PasskeySignIn {
			auth.NoVerification = true
			defer func() { auth.NoVerification = false }()
			return w.assert(auth, mine.ID)
		},
	}
	sessions, _ := w.svc.sessions(ctx, me.Account.ID, nil)
	for name, make := range assertions {
		in := make()
		got, cookie, err := w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: ""})
		if got != nil || cookie != "" || !errors.Is(err, contracts.ErrInvalidPasskey) || err.Error() != contracts.ErrInvalidPasskey.Error() {
			t.Errorf("signing in with %s: %+v, %v", name, got, err)
		}
		if _, ok, _ := w.svc.end(ctx, in.Ceremony, ""); ok {
			t.Errorf("%s: the ceremony is still running", name)
		}
	}
	if after, _ := w.svc.sessions(ctx, me.Account.ID, nil); len(after) != len(sessions) {
		t.Fatalf("a refused sign-in left a session: %d, before %d", len(after), len(sessions))
	}
	right2 := w.assert(auth, mine.ID)
	wrong2 := right2
	wrong2.Credential.Response.Signature = []byte("nope")
	for _, in := range []contracts.PasskeySignIn{wrong2, right2} {
		if _, _, err := w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidPasskey) {
			t.Fatalf("a sign-in tried twice: %v", err)
		}
	}
	// After all of it the passkey still signs in.
	if _, _, err := w.signInWith(auth, mine.ID); err != nil {
		t.Fatalf("the passkey after the refusals: %v", err)
	}

	// What the checks refused is logged in their own fixed words, and in
	// nothing else: no address, account, passkey, ceremony or page.
	logs := w.logs.String()
	if !strings.Contains(logs, "a passkey that was made was refused") || !strings.Contains(logs, "a sign-in with a passkey was refused") || !strings.Contains(logs, `why="webauthn: answer refused: `) {
		t.Fatalf("the logs do not say why an answer was refused:\n%s", logs)
	}
	line := regexp.MustCompile(`^time=\S+ level=INFO msg="accounts: [a-z -]+"( why="webauthn: answer refused: [a-zA-Z ,'-]+"| kind=[a-z_]+ count=\d+)?$`)
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		if !line.MatchString(l) {
			t.Errorf("a log line with more than fixed words: %s", l)
		}
	}
	for _, secret := range []string{"ana@", "example", me.Account.ID, eve.AccountID, mine.ID.String(), contracts.Bytes(stranger).String(), "evil", "cer_"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, logs)
		}
	}
}

func TestPasskeysAreBounded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	// Twenty passkeys, and a ceremony for one more that was started in time.
	late := w.create(who, webauthntest.New(webauthntest.EdDSA), "one too many")
	for i := range domain.MaxPasskeys {
		w.addPasskey(who, webauthntest.New(webauthntest.EdDSA), fmt.Sprintf("key %d", i))
	}
	if _, err := w.svc.StartPasskey(ctx, w.fresh(who)); !errors.Is(err, contracts.ErrTooManyPasskeys) {
		t.Fatalf("starting passkey twenty-one: %v", err)
	}
	if _, err := w.svc.FinishPasskey(ctx, who, late); !errors.Is(err, contracts.ErrTooManyPasskeys) {
		t.Fatalf("finishing passkey twenty-one: %v", err)
	}
	if list, _ := w.svc.Passkeys(ctx, who); len(list.Passkeys) != domain.MaxPasskeys {
		t.Fatalf("%d passkeys kept", len(list.Passkeys))
	}

	// A ceremony ends once, and not after its five minutes; one the storage
	// could not keep is not started.
	first, _ := w.svc.StartPasskeySignIn(ctx)
	second, _ := w.svc.StartPasskeySignIn(ctx)
	if _, ok, err := w.svc.end(ctx, first.Ceremony, ""); !ok || err != nil {
		t.Fatalf("a ceremony that runs: %v", err)
	}
	if _, ok, _ := w.svc.end(ctx, first.Ceremony, ""); ok {
		t.Fatal("a ceremony ended twice")
	}
	w.now = w.now.Add(domain.PasskeyCeremonyLifetime)
	if _, ok, _ := w.svc.end(ctx, second.Ceremony, ""); ok || w.pending(kindCeremony) != 0 {
		t.Fatalf("a ceremony ended after its time: %d kept", w.pending(kindCeremony))
	}
	if _, ok, _ := w.svc.end(ctx, "not a ceremony", ""); ok {
		t.Fatal("a ceremony that is not one")
	}
	w.svc.cfg.Entries = &failing{Entries: w.mem, fail: map[string]error{"put " + pendingKey: errors.New("down")}}
	if started, err := w.svc.StartPasskeySignIn(ctx); started != nil || err == nil {
		t.Fatalf("a ceremony the storage did not keep: %+v %v", started, err)
	}
	w.svc.cfg.Entries = w.mem
}

var recoveryCodeRE = regexp.MustCompile(`^[2-9A-Z]{4}-[2-9A-Z]{4}-[2-9A-Z]{4}-[2-9A-Z]{4}$`)

func TestPasskeyOnly(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)

	// It needs a passkey, and without it there are no recovery codes.
	if _, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true}); !errors.Is(err, contracts.ErrPasskeyNeeded) {
		t.Fatalf("passkey only without a passkey: %v", err)
	}
	if _, err := w.svc.NewRecoveryCodes(ctx, w.fresh(who)); !errors.Is(err, contracts.ErrPasskeyOnlyOff) {
		t.Fatalf("recovery codes without passkey only: %v", err)
	}
	if state, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: false}); err != nil || state.PasskeyOnly || state.RecoveryCodes == nil || len(state.RecoveryCodes) != 0 {
		t.Fatalf("turning off what is off: %+v %v", state, err)
	}
	auth := webauthntest.New(webauthntest.ES256)
	first := w.addPasskey(who, auth, "first")

	// A sign-in by mail that was started before.
	attempt, mail := w.start("ana@example.org", "")
	if mail == nil {
		t.Fatal("no message before passkey only")
	}

	codes := w.passkeyOnly(who)
	seen := map[string]bool{}
	for _, c := range codes {
		if !recoveryCodeRE.MatchString(c) || seen[c] {
			t.Fatalf("recovery codes: %v", codes)
		}
		seen[c] = true
	}
	if list, _ := w.svc.Passkeys(ctx, who); !list.PasskeyOnly || list.RecoveryCodesLeft != domain.RecoveryCodes {
		t.Fatalf("after turning it on: %+v", list)
	}
	// Asked again it stays on, and the codes just shown keep working.
	if state, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true}); err != nil || !state.PasskeyOnly || len(state.RecoveryCodes) != 0 {
		t.Fatalf("turning on what is on: %+v %v", state, err)
	}

	// The sign-in that was waiting is refused, with its right code and link.
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a sign-in by mail started before passkey only: %v", err)
	}
	// A new one answers as always and sends nothing, with an invitation or without.
	sent := len(w.out.Sent())
	for _, invitation := range []string{"", w.invite()} {
		if _, mail := w.start("ana@example.org", invitation); mail != nil {
			t.Fatalf("a sign-in message for an account that signs in with passkeys only (invitation %q)", invitation)
		}
	}
	if len(w.out.Sent()) != sent || w.pending(kindAttempt) != 0 {
		t.Fatalf("a sign-in by mail is waiting: %d", w.pending(kindAttempt))
	}
	// The passkey signs in.
	if signed, _, err := w.signInWith(auth, first.ID); err != nil || signed.Account.ID != me.Account.ID {
		t.Fatalf("the passkey: %v", err)
	}

	// The last passkey stays; with a second one, either goes.
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), first.ID.String()); !errors.Is(err, contracts.ErrLastPasskey) {
		t.Fatalf("removing the last passkey: %v", err)
	}
	second := w.addPasskey(who, webauthntest.New(webauthntest.EdDSA), "second")
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), first.ID.String()); err != nil {
		t.Fatalf("removing one of two: %v", err)
	}
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), second.ID.String()); !errors.Is(err, contracts.ErrLastPasskey) {
		t.Fatalf("removing the one left: %v", err)
	}
	if list, _ := w.svc.Passkeys(ctx, who); len(list.Passkeys) != 1 || !list.PasskeyOnly {
		t.Fatalf("after the refusals: %+v", list)
	}

	// New codes: ten, and the ones before them stop working.
	fresh, err := w.svc.NewRecoveryCodes(ctx, w.fresh(who))
	if err != nil || len(fresh.RecoveryCodes) != domain.RecoveryCodes || slices.Contains(fresh.RecoveryCodes, codes[0]) {
		t.Fatalf("new codes: %+v %v", fresh, err)
	}
	if _, _, err := w.recover("ana@example.org", codes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code from before the new ones: %v", err)
	}
	if _, _, err := w.recover("ana@example.org", fresh.RecoveryCodes[0]); err != nil {
		t.Fatalf("a new code: %v", err)
	}

	// Turned off, the codes are dropped and mail signs in again.
	if state, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: false}); err != nil || state.PasskeyOnly || len(state.RecoveryCodes) != 0 {
		t.Fatalf("turning it off: %+v %v", state, err)
	}
	if list, _ := w.svc.Passkeys(ctx, who); list.PasskeyOnly || list.RecoveryCodesLeft != 0 || len(list.Passkeys) != 1 {
		t.Fatalf("after turning it off: %+v", list)
	}
	if _, _, err := w.recover("ana@example.org", fresh.RecoveryCodes[1]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code after passkey only was turned off: %v", err)
	}
	attempt, mail = w.start("ana@example.org", "")
	if mail == nil {
		t.Fatal("no message after passkey only was turned off")
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); err != nil {
		t.Fatalf("signing in by mail again: %v", err)
	}
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), second.ID.String()); err != nil {
		t.Fatalf("removing the last passkey with passkey only off: %v", err)
	}
	// Turned on again, the codes are new ones.
	w.addPasskey(who, auth, "again")
	if again := w.passkeyOnly(who); slices.Contains(again, fresh.RecoveryCodes[1]) || slices.Contains(again, codes[1]) {
		t.Fatal("codes from before came back")
	}
}

// counting counts what the storage is asked.
type counting struct {
	Records
	store.Entries
	mu       sync.Mutex
	gets     int
	getDelay time.Duration
}

func (c *counting) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	rec, err := c.Records.GetRecord(ctx, name)
	time.Sleep(c.getDelay)
	return rec, err
}

func (c *counting) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	return c.Entries.GetEntry(ctx, partition, key)
}

func TestRecovery(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
	codes := w.passkeyOnly(who)

	// Eve signs in with passkeys only too; Bob does not.
	_, eveCookie := w.signUp("eve@example.org")
	eve := w.who(eveCookie)
	w.addPasskey(eve, webauthntest.New(webauthntest.ES256), "key")
	eveCodes := w.passkeyOnly(eve)
	w.signUp("bob@example.org")

	// Every refusal is the error of a wrong code, in the same words.
	wrongCode := "2222-2222-2222-2222"
	if slices.Contains(codes, wrongCode) {
		wrongCode = "3333-3333-3333-3333"
	}
	refusals := map[string][2]string{
		"a wrong code":                       {"ana@example.org", wrongCode},
		"another account's code":             {"ana@example.org", eveCodes[0]},
		"the code with another address":      {"eve@example.org", codes[0]},
		"an address without an account":      {"nobody@example.org", codes[0]},
		"an account without passkey only":    {"bob@example.org", codes[0]},
		"something that is not an address":   {"ana", codes[0]},
		"no address":                         {"", codes[0]},
		"something that is not a code":       {"ana@example.org", "123456"},
		"no code":                            {"ana@example.org", ""},
		"an invitation":                      {"ana@example.org", w.invite()},
		"a code cut short":                   {"ana@example.org", codes[0][:14]},
		"a code with a character more":       {"ana@example.org", codes[0] + "2"},
		"nothing":                            {"", ""},
		"the address of the account's owner": {"ana@example.org.", codes[0]},
	}
	for name, in := range refusals {
		got, cookie, err := w.recover(in[0], in[1])
		if got != nil || cookie != "" || !errors.Is(err, contracts.ErrInvalidCode) || err.Error() != contracts.ErrInvalidCode.Error() {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	// Only the wrong codes for an account that takes them were counted.
	if len(w.svc.recoveries) != 2 || len(w.svc.recoveries["ana@example.org"]) != 2 || len(w.svc.recoveries["eve@example.org"]) != 1 {
		t.Fatalf("wrong codes counted: %v", w.svc.recoveries)
	}

	// A code signs in, typed as a person types it, and works once.
	signed, recovered, err := w.recover(" Ana@Example.org ", " "+strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))+" ")
	if err != nil || signed.Account.ID != me.Account.ID || !signed.Session.Current || signed.Session.Device != "recovered device" {
		t.Fatalf("recovery: %+v %v", signed, err)
	}
	if got := w.who(recovered); got.AccountID != me.Account.ID {
		t.Fatalf("the session of a recovery: %+v", got)
	}
	if list, _ := w.svc.Passkeys(ctx, who); list.RecoveryCodesLeft != domain.RecoveryCodes-1 || !list.PasskeyOnly {
		t.Fatalf("after one recovery: %+v", list)
	}
	if _, _, err := w.recover("ana@example.org", codes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the same code again: %v", err)
	}
	// The one recovered can make a passkey and stay in.
	w.addPasskey(w.who(recovered), webauthntest.New(webauthntest.ES256), "new")

	// Five wrong codes in an hour (three so far), and the right one is
	// refused too, for that address alone.
	for range 2 {
		_, _, _ = w.recover("ana@example.org", wrongCode)
	}
	if _, _, err := w.recover("ana@example.org", codes[1]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a right code after five wrong ones: %v", err)
	}
	if _, _, err := w.recover("eve@example.org", eveCodes[0]); err != nil {
		t.Fatalf("another address meanwhile: %v", err)
	}
	// Trying on does not make the wait longer.
	w.now = w.now.Add(59 * time.Minute)
	_, _, _ = w.recover("ana@example.org", wrongCode)
	w.now = w.now.Add(2 * time.Minute)
	if _, _, err := w.recover("ana@example.org", codes[1]); err != nil {
		t.Fatalf("the right code an hour later: %v", err)
	}

	// The count is in memory, and memory is bounded: when it is full, an
	// address not counted yet is refused, never let to guess without count.
	w.now = w.now.Add(2 * time.Hour)
	w.svc.mu.Lock()
	w.svc.swept = time.Time{}
	w.svc.sweep(w.now)
	w.svc.mu.Unlock()
	if len(w.svc.recoveries) != 0 {
		t.Fatalf("wrong codes kept after their hour: %v", w.svc.recoveries)
	}
	for i := range maxRecoveryCounts {
		w.svc.recoveries[fmt.Sprint(i)] = []time.Time{w.now}
	}
	if _, _, err := w.recover("ana@example.org", codes[2]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a recovery beyond what is counted: %v", err)
	}
	w.now = w.now.Add(time.Hour + time.Minute)
	if _, _, err := w.recover("ana@example.org", codes[2]); err != nil {
		t.Fatalf("after the others ended: %v", err)
	}

	// The storage is asked as much for an address without an account as for
	// one with: how long the answer takes does not tell them apart.
	c := &counting{Records: w.svc.cfg.Records, Entries: w.svc.cfg.Entries}
	w.svc.cfg.Records, w.svc.cfg.Entries = c, c
	asked := map[string]int{}
	for _, address := range []string{"ana@example.org", "bob@example.org", "nobody@example.org"} {
		before := c.gets
		_, _, _ = w.recover(address, wrongCode)
		asked[address] = c.gets - before
	}
	if asked["ana@example.org"] == 0 || asked["nobody@example.org"] != asked["ana@example.org"] || asked["bob@example.org"] != asked["ana@example.org"] {
		t.Fatalf("what the storage was asked: %v", asked)
	}

	// Nothing logged names the address, the account or a code.
	logs := w.logs.String()
	if !strings.Contains(logs, "accounts: recovery code used") {
		t.Fatalf("the logs do not say a code was used:\n%s", logs)
	}
	secrets := append([]string{"ana@", "example.org", me.Account.ID, wrongCode}, codes...)
	for _, c := range codes {
		normalized, _ := domain.NormalizeRecoveryCode(c)
		secrets = append(secrets, normalized, strings.ToUpper(normalized), hex.EncodeToString(recoveryHash(me.Account.ID, normalized)))
	}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, logs)
		}
	}
}

// Two uses of one recovery code at the same time: one signs in.
func TestRecoveryCodeUsedTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
	codes := w.passkeyOnly(who)
	// A storage slow enough for every use to read the record before any of
	// them writes it.
	c := &counting{Records: w.svc.cfg.Records, Entries: w.svc.cfg.Entries, getDelay: 5 * time.Millisecond}
	w.svc.cfg.Records = c

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, results[i] = w.recover("ana@example.org", codes[0])
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, contracts.ErrInvalidCode):
			t.Errorf("a use that lost: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("one code signed in %d times", won)
	}
	if list, _ := w.svc.Passkeys(context.Background(), who); list.RecoveryCodesLeft != domain.RecoveryCodes-1 {
		t.Fatalf("codes left: %d", list.RecoveryCodesLeft)
	}
	if len(w.svc.recoveries) != 0 {
		t.Fatalf("the uses that lost were counted as wrong codes: %v", w.svc.recoveries)
	}
}

// Whoever runs the storage cannot add a passkey, bring back one that was
// removed or a recovery code that was used, or turn "passkey only" off.
func TestPasskeysWrittenByHand(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	id := ana.Account.ID
	sessions := func() int {
		list, err := w.mem.ListEntries(ctx, accountPartition(id), sessionPrefix, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	// refused checks that nobody got in, and that the account was not taken
	// for one that could be signed up again.
	refused := func(what string, me *contracts.Me, cookie string, err error, before int) {
		t.Helper()
		if me != nil || cookie != "" || err == nil || sessions() != before {
			t.Fatalf("%s: signed in (%v)", what, err)
		}
	}

	// A passkey of the operator's own written into the record, as the
	// service would have written it.
	theirs := webauthntest.New(webauthntest.ES256)
	challenge := make([]byte, 32)
	credential, clientData, attestation := theirs.Create(testRP, testOrigin, challenge, []byte(id))
	made, err := webauthn.RelyingParty{ID: testRP, Origins: []string{testOrigin}}.Registered(challenge, clientData, attestation)
	if err != nil {
		t.Fatal(err)
	}
	planted, _ := json.Marshal([]passkeyDoc{{ID: credential, PublicKey: made.PublicKey, Name: "planted", CreatedAt: w.now}})
	clean := w.record(w.newest(id))
	forged := clean
	forged.Value = bytes.Replace(clean.Value, []byte(`"language":"en"`), []byte(`"language":"en","passkeys":`+string(planted)), 1)
	if bytes.Equal(forged.Value, clean.Value) {
		t.Fatal("the record was not changed")
	}
	before := sessions()
	w.mem.SetRecord(forged)
	me, got, err := w.signInWith(theirs, credential)
	refused("a passkey written into the record", me, got, err, before)
	if errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatal("a record changed by hand was taken for an account without that passkey")
	}
	// Signed with nothing at all, and as a record of its own beside the others.
	unsigned := clean
	doc, _ := json.Marshal(accountDoc{V: 1, ID: id, State: contracts.AccountActive, Address: "ana@example.org", Passkeys: []passkeyDoc{{ID: credential, PublicKey: made.PublicKey}}})
	unsigned.Value = []byte(`{"doc":` + string(doc) + `,"mac":""}`)
	w.mem.SetRecord(unsigned)
	me, got, err = w.signInWith(theirs, credential)
	refused("a record written without the key", me, got, err, before)
	later := unsigned
	later.Name = strings.Replace(clean.Name, "@0000", "@9999", 1)
	w.mem.SetRecord(clean)
	w.mem.SetRecord(later)
	if me, got, err = w.signInWith(theirs, credential); !errors.Is(err, contracts.ErrInvalidPasskey) || me != nil || got != "" {
		t.Fatalf("a later version written beside the record: %v", err)
	}
	_ = w.mem.DeleteRecord(ctx, later.Name)

	// A passkey that was removed, brought back with the copy of the record
	// that had it: an earlier copy that verifies.
	auth := webauthntest.New(webauthntest.ES256)
	removed := w.addPasskey(who, auth, "removed")
	kept := w.addPasskey(who, webauthntest.New(webauthntest.ES256), "kept")
	withIt := w.record(w.newest(id))
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), removed.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.signInWith(auth, removed.ID); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("a removed passkey: %v", err)
	}
	without := w.record(w.newest(id))
	back := without
	back.Value = withIt.Value
	before = sessions()
	w.mem.SetRecord(back)
	me, got, err = w.signInWith(auth, removed.ID)
	refused("a removed passkey put back over the newest record", me, got, err, before)
	// The answer names the earlier copy itself, by the name it is stored
	// under, instead of the account: only an account's identifier is read.
	w.mem.SetRecord(without)
	earlier := w.assert(auth, removed.ID)
	earlier.Credential.Response.UserHandle = []byte(strings.TrimPrefix(withIt.Name, "account-"))
	names := len(w.mem.RecordNames())
	if me, got, err = w.svc.FinishPasskeySignIn(ctx, earlier, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidPasskey) || me != nil || got != "" || len(w.mem.RecordNames()) != names {
		t.Fatalf("an earlier copy of the record named as the account: %v", err)
	}
	// With the newest version deleted, the one before it is the last in the table.
	_ = w.mem.DeleteRecord(ctx, without.Name)
	me, got, err = w.signInWith(auth, removed.ID)
	refused("a removed passkey with the newest record deleted", me, got, err, before)
	if !errors.Is(err, store.ErrLost) {
		t.Fatalf("the newest record deleted: %v", err)
	}
	w.mem.SetRecord(without)

	// A recovery code that was used, brought back the same way.
	codes := w.passkeyOnly(who)
	unused := w.record(w.newest(id))
	if _, _, err := w.recover("ana@example.org", codes[0]); err != nil {
		t.Fatal(err)
	}
	used := w.record(w.newest(id))
	back = used
	back.Value = unused.Value
	before = sessions()
	w.mem.SetRecord(back)
	me, got, err = w.recover("ana@example.org", codes[0])
	refused("a used recovery code put back", me, got, err, before)
	_ = w.mem.DeleteRecord(ctx, used.Name)
	me, got, err = w.recover("ana@example.org", codes[0])
	refused("a used recovery code with the newest record deleted", me, got, err, before)
	w.mem.SetRecord(used)
	if _, _, err := w.recover("ana@example.org", codes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the used code, with the storage as it was: %v", err)
	}
	// The hash of a code of another account, which its record shows, is of
	// no code here: the account is part of what is hashed.
	if bytes.Equal(recoveryHash(id, "abcd2345wxyz6789"), recoveryHash("acc_"+strings.Repeat("a", 26), "abcd2345wxyz6789")) {
		t.Fatal("a code hashes the same in two accounts")
	}

	// "Passkey only" turned off: by changing the word, and with the copy
	// from before it was turned on. No sign-in by mail is sent either way.
	on := w.record(w.newest(id))
	if !bytes.Contains(on.Value, []byte(`"passkey_only":true`)) {
		t.Fatalf("the record does not say passkey only is on: %s", on.Value)
	}
	for what, value := range map[string][]byte{
		"changed by hand":       bytes.Replace(on.Value, []byte(`"passkey_only":true`), []byte(`"passkey_only":false`), 1),
		"left out by hand":      bytes.Replace(on.Value, []byte(`"passkey_only":true,`), nil, 1),
		"the copy from before":  without.Value,
		"the very first record": clean.Value,
	} {
		off := on
		off.Value = value
		w.mem.SetRecord(off)
		w.now = w.now.Add(2 * time.Hour)
		if _, mail := w.start("ana@example.org", ""); mail != nil {
			t.Fatalf("passkey only turned off (%s): a sign-in message was sent", what)
		}
		if _, mail := w.start("ana@example.org", w.invite()); mail != nil {
			t.Fatalf("passkey only turned off (%s): the address was let to sign up again", what)
		}
	}
	// With the storage as the enclave left it, everything is as it was.
	w.mem.SetRecord(on)
	if list, err := w.svc.Passkeys(ctx, who); err != nil || !list.PasskeyOnly || len(list.Passkeys) != 1 || !bytes.Equal(list.Passkeys[0].ID, kept.ID) || list.RecoveryCodesLeft != domain.RecoveryCodes-1 {
		t.Fatalf("after all of it: %+v %v", list, err)
	}
}

// What the storage holds of passkeys and recovery codes: the public key,
// readable, and of each code its hash alone.
func TestWhatIsStoredOfPasskeys(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	made := w.addPasskey(who, webauthntest.New(webauthntest.ES256), "Laptop")
	codes := w.passkeyOnly(who)
	more, err := w.svc.NewRecoveryCodes(ctx, w.fresh(who))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.recover("ana@example.org", more.RecoveryCodes[0]); err != nil {
		t.Fatal(err)
	}
	var hidden []string
	for _, c := range append(codes, more.RecoveryCodes...) {
		normalized, _ := domain.NormalizeRecoveryCode(c)
		hidden = append(hidden, c, strings.ToLower(c), normalized, strings.ToUpper(normalized))
	}
	for _, name := range w.mem.RecordNames() {
		rec := w.record(name)
		for _, code := range hidden {
			if bytes.Contains(rec.Value, []byte(code)) {
				t.Fatalf("record %s holds a recovery code", name)
			}
		}
	}
	for _, partition := range []string{accountPartition(me.Account.ID), loginPartition("ana@example.org")} {
		entries, _ := w.mem.ListEntries(ctx, partition, "", "", 100)
		for _, e := range entries {
			for _, code := range hidden {
				if bytes.Contains(e.Value, []byte(code)) || strings.Contains(e.Key, code) {
					t.Fatalf("entry %s holds a recovery code", e.Key)
				}
			}
		}
	}
	// The newest record, as support reads it.
	rec := w.record(w.newest(me.Account.ID))
	var stored struct {
		Doc struct {
			Passkeys []struct {
				ID        string    `json:"id"`
				PublicKey string    `json:"public_key"`
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"created_at"`
				Synced    bool      `json:"synced"`
			} `json:"passkeys"`
			PasskeyOnly   bool     `json:"passkey_only"`
			RecoveryCodes [][]byte `json:"recovery_codes"`
		} `json:"doc"`
	}
	if !rec.Plain || json.Unmarshal(rec.Value, &stored) != nil {
		t.Fatalf("the record is not readable: %s", rec.Value)
	}
	d := stored.Doc
	if len(d.Passkeys) != 1 || d.Passkeys[0].ID != made.ID.String() || d.Passkeys[0].PublicKey == "" || d.Passkeys[0].Name != "Laptop" || !d.PasskeyOnly {
		t.Fatalf("stored: %s", rec.Value)
	}
	if strings.Contains(string(rec.Value), "counter") {
		t.Fatalf("a signature counter is kept: %s", rec.Value)
	}
	// Nine hashes: of the nine new codes not used, and of nothing else.
	if len(d.RecoveryCodes) != domain.RecoveryCodes-1 {
		t.Fatalf("%d hashes kept", len(d.RecoveryCodes))
	}
	for i, c := range more.RecoveryCodes[1:] {
		normalized, _ := domain.NormalizeRecoveryCode(c)
		if !bytes.Equal(d.RecoveryCodes[i], recoveryHash(me.Account.ID, normalized)) || len(d.RecoveryCodes[i]) != 32 {
			t.Fatalf("hash %d is not of code %d", i, i+1)
		}
	}
}

// Storage or keys that fail never sign anyone in with a passkey or a
// recovery code, and never leave a change half made.
func TestPasskeyFailures(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()
	type env struct {
		w     *world
		f     *failing
		who   *Authenticated
		auth  *webauthntest.Authenticator
		made  *contracts.Passkey
		codes []string
	}
	setup := func(t *testing.T, passkeyOnly bool) *env {
		w := newWorld(t)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		_, cookie := w.signUp("ana@example.org")
		e := &env{w: w, f: f, who: w.who(cookie), auth: webauthntest.New(webauthntest.ES256)}
		e.made = w.addPasskey(e.who, e.auth, "key")
		if passkeyOnly {
			e.codes = w.passkeyOnly(e.who)
		}
		return e
	}
	sessions := func(e *env) int {
		list, _ := e.w.mem.ListEntries(ctx, accountPartition(e.who.AccountID), sessionPrefix, "", 1000)
		return len(list)
	}

	// Each step of signing in with a passkey.
	for _, op := range []string{"get record", "derive", "list entries", "put entry"} {
		e := setup(t, false)
		in, before := e.w.assert(e.auth, e.made.ID), sessions(e)
		e.f.fail[op] = down
		if op == "derive" {
			e.f.skip = map[string]int{op: 1} // the ceremony is read, and ended, before
		}
		me, cookie, err := e.w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: ""})
		if me != nil || cookie != "" || !errors.Is(err, down) || sessions(e) != before {
			t.Errorf("%s down: signing in with a passkey: %+v %v", op, me, err)
		}
		// The ceremony ended with the failure: the same answer is not tried again.
		e.f.fail = map[string]error{}
		if _, _, err := e.w.svc.FinishPasskeySignIn(ctx, in, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidPasskey) {
			t.Errorf("%s down: the answer again: %v", op, err)
		}
	}

	// Each step of a recovery. Until the record is written the code is
	// still good; once it is, the code is used, session or not.
	for _, op := range []string{"get entry", "get record", "derive", "put record", "list entries", "put entry"} {
		e := setup(t, true)
		before := sessions(e)
		e.f.fail[op] = down
		me, cookie, err := e.w.recover("ana@example.org", e.codes[0])
		if me != nil || cookie != "" || !errors.Is(err, down) || sessions(e) != before {
			t.Errorf("%s down: recovery: %+v %v", op, me, err)
		}
		if len(e.w.svc.recoveries) != 0 {
			t.Errorf("%s down: a failure was counted as a wrong code", op)
		}
		e.f.fail = map[string]error{}
		list, _ := e.w.svc.Passkeys(ctx, e.who)
		_, _, err = e.w.recover("ana@example.org", e.codes[0])
		if used := op == "list entries" || op == "put entry"; used != (list.RecoveryCodesLeft == domain.RecoveryCodes-1) || used != errors.Is(err, contracts.ErrInvalidCode) || (!used && err != nil) {
			t.Errorf("%s down: afterwards %d codes left, and the code again: %v", op, list.RecoveryCodesLeft, err)
		}
	}
	// The same for an address without an account: a failure, not "wrong code".
	e := setup(t, true)
	e.f.fail["get record"] = down
	if _, _, err := e.w.recover("nobody@example.org", e.codes[0]); !errors.Is(err, down) {
		t.Errorf("get record down: recovery of an address without an account: %v", err)
	}

	// Each change of the record: nothing is half made.
	for _, op := range []string{"get record", "derive", "put record"} {
		e := setup(t, false)
		in := e.w.create(e.who, webauthntest.New(webauthntest.ES256), "second")
		e.f.fail[op] = down
		if _, err := e.w.svc.FinishPasskey(ctx, e.who, in); !errors.Is(err, down) {
			t.Errorf("%s down: making a passkey: %v", op, err)
		}
		if _, err := e.w.svc.StartPasskey(ctx, e.w.fresh(e.who)); !errors.Is(err, down) && op != "put record" {
			t.Errorf("%s down: starting a passkey: %v", op, err)
		}
		if _, err := e.w.svc.Passkeys(ctx, e.who); !errors.Is(err, down) && op != "put record" {
			t.Errorf("%s down: passkeys: %v", op, err)
		}
		if _, err := e.w.svc.RenamePasskey(ctx, e.who, e.made.ID.String(), contracts.PasskeyRename{Name: "renamed"}); !errors.Is(err, down) {
			t.Errorf("%s down: renaming: %v", op, err)
		}
		if err := e.w.svc.RemovePasskey(ctx, e.w.fresh(e.who), e.made.ID.String()); !errors.Is(err, down) {
			t.Errorf("%s down: removing: %v", op, err)
		}
		if _, err := e.w.svc.SetPasskeyOnly(ctx, e.w.fresh(e.who), contracts.PasskeyOnly{On: true}); !errors.Is(err, down) {
			t.Errorf("%s down: passkey only: %v", op, err)
		}
		if _, err := e.w.svc.NewRecoveryCodes(ctx, e.w.fresh(e.who)); !errors.Is(err, down) && op != "put record" {
			t.Errorf("%s down: recovery codes: %v", op, err)
		}
		e.f.fail = map[string]error{}
		list, err := e.w.svc.Passkeys(ctx, e.who)
		if err != nil || len(list.Passkeys) != 1 || list.Passkeys[0].Name != "key" || list.PasskeyOnly || list.RecoveryCodesLeft != 0 {
			t.Errorf("%s down: afterwards: %+v %v", op, list, err)
		}
	}
	e = setup(t, true)
	e.f.fail["put record"] = down
	if _, err := e.w.svc.NewRecoveryCodes(ctx, e.w.fresh(e.who)); !errors.Is(err, down) {
		t.Errorf("put record down: recovery codes: %v", err)
	}
	if _, err := e.w.svc.SetPasskeyOnly(ctx, e.w.fresh(e.who), contracts.PasskeyOnly{On: false}); !errors.Is(err, down) {
		t.Errorf("put record down: turning passkey only off: %v", err)
	}
	e.f.fail = map[string]error{}
	if _, _, err := e.w.recover("ana@example.org", e.codes[0]); err != nil {
		t.Errorf("a code after new ones could not be stored: %v", err)
	}
	// A sign-in by mail that ends when the account cannot be read.
	e = setup(t, false)
	attempt, mail := e.w.start("ana@example.org", "")
	e.f.fail["get record"] = down
	if _, _, err := e.w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, down) {
		t.Errorf("get record down: finishing a sign-in by mail: %v", err)
	}

	// A session of an account that is not there does nothing.
	w := newWorld(t)
	ghost := w.ghost()
	id := contracts.Bytes("x").String()
	for what, err := range map[string]error{
		"passkeys":       second(w.svc.Passkeys(ctx, ghost)),
		"start":          second(w.svc.StartPasskey(ctx, w.fresh(ghost))),
		"rename":         second(w.svc.RenamePasskey(ctx, ghost, id, contracts.PasskeyRename{Name: "x"})),
		"remove":         w.svc.RemovePasskey(ctx, w.fresh(ghost), id),
		"passkey only":   second(w.svc.SetPasskeyOnly(ctx, w.fresh(ghost), contracts.PasskeyOnly{On: true})),
		"recovery codes": second(w.svc.NewRecoveryCodes(ctx, w.fresh(ghost))),
	} {
		if !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("%s without an account: %v", what, err)
		}
	}
	// Who the passkeys are for comes from the app's URL: one without a host
	// is no service.
	for _, bad := range []string{"app.permaily.test", "https://", "%zz", "/sign-in"} {
		if _, err := New(Config{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, Mailer: w.out, AppURL: bad}); err == nil {
			t.Errorf("a service at %q", bad)
		}
	}
	// The port is part of the page's origin, and not of who they are for.
	dev, err := New(Config{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, Mailer: w.out, AppURL: "https://app.permaily.test:8443"})
	if err != nil || dev.rp.ID != "app.permaily.test" || !slices.Equal(dev.rp.Origins, []string{"https://app.permaily.test:8443"}) {
		t.Fatalf("relying party of a service on a port: %+v %v", dev, err)
	}
}

func second[T any](_ T, err error) error { return err }
