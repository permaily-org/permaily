package connections

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// errDown is storage that could not be read: neither a record refused nor
// one that does not exist.
var errDown = errors.New("the storage did not answer")

// failGets makes h's reads of records whose name starts with prefix fail
// with err.
func failGets(h *failing, prefix string, err error) {
	h.set(func(op, name string) error {
		if op == "get" && strings.HasPrefix(name, prefix) {
			return err
		}
		return nil
	})
}

// V27: a record the service cannot read is ErrUnavailable (503), never a
// refusal of the client or of its token; one it reads and refuses is
// refused.
func TestV27UnreadableRecordsAre503(t *testing.T) {
	ctx := context.Background()

	t.Run("a code's connection", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		code := w.allow(s, w.person(accountA), w.start(s, w.person(accountA), x))
		h, f := w.hooked()
		failGets(f, RecordConnection, errDown)
		if _, err := w.exchange(h, x, code); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})

	t.Run("a record of assertions", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(chatgpt)
		code := w.allow(s, w.person(accountA), w.start(s, w.person(accountA), x))
		h, f := w.hooked()
		failGets(f, oauth.RecordAssertions, errDown)
		if _, err := w.exchange(h, x, code); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		// A record of assertions that is not what was committed refuses
		// the client.
		failGets(f, oauth.RecordAssertions, store.ErrRolledBack)
		if _, err := w.exchange(h, x, code); oauthCode(err) != oauth.ErrInvalidClient {
			t.Fatal(err)
		}
	})

	t.Run("an adapter's refusal naming a message", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok := w.connectAll(s, w.person(accountA), x)
		g := w.grant(s, x, tok.AccessToken)
		h, f := w.hooked()
		r := Refusal{Operation: OperationRead, Reason: receipts.RefusedOutsideScope, Message: mailboxID(1) + ".m001"}
		failGets(f, RecordConnection, errDown)
		before := w.rcpt.count()
		if err := h.Refuse(ctx, g, r); !errors.Is(err, ErrUnavailable) || w.rcpt.count() != before {
			t.Fatal(err)
		}
		// A connection gone: the refusal is recorded without its message.
		failGets(f, RecordConnection, store.ErrNotFound)
		if err := h.Refuse(ctx, g, r); err != nil {
			t.Fatal(err)
		}
		got := w.rcpt.of(receipts.KindRefused)
		if last := got[len(got)-1].Refused; last.Mailbox != "" || last.Message != "" || last.Reason != receipts.RefusedOutsideScope {
			t.Fatalf("%+v", last)
		}
		// Another account's grant never names this connection's mailboxes.
		f.set(nil)
		other := *g
		other.Account = accountB
		if err := h.Refuse(ctx, &other, r); err != nil {
			t.Fatal(err)
		}
		got = w.rcpt.of(receipts.KindRefused)
		if last := got[len(got)-1].Refused; last.Mailbox != "" || last.Message != "" {
			t.Fatalf("%+v", last)
		}
	})
}

// A client assertion signed with a key its set does not hold yet: the set
// is fetched once more, and the assertion verified against it.
func TestPrivateKeyJWTFetchesTheKeySetOnceMoreForAnUnknownKid(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(chatgpt)
	code := w.allow(s, w.person(accountA), w.start(s, w.person(accountA), x))
	w.vendors.mu.Lock()
	var kept []vendors.Key
	for _, k := range w.vendors.keys[chatgpt] {
		if k.ID == "k1" {
			w.vendors.fresh = map[string][]vendors.Key{chatgpt: {k}}
			continue
		}
		kept = append(kept, k)
	}
	w.vendors.keys[chatgpt] = kept
	w.vendors.mu.Unlock()
	if _, err := w.exchange(s, x, code); err != nil {
		t.Fatal(err)
	}
	// A kid in no set, however often it is fetched: invalid_client.
	w.vendors.mu.Lock()
	w.vendors.keys[chatgpt] = kept
	w.vendors.mu.Unlock()
	tok, err := w.exchange(s, x, code)
	if oauthCode(err) != oauth.ErrInvalidClient || tok != nil {
		t.Fatal(tok, err)
	}
}

// V27: a grant whose connection was revoked reads nothing: Info and every
// read refused invalid_token, through the enclave that revoked it at once.
func TestV27RevokedGrantReadsNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	id := w.connectionOf(s, accountA, claudeCode).ID
	if _, err := s.Revoke(ctx, w.person(accountA), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info(ctx, g); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, _, err := s.Read(ctx, g, mailboxID(1)+".m001"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

// V27 and V29: the account's list or a connection's record unreadable,
// through an enclave that has not read them yet: a check is ErrUnavailable,
// a list refused or gone refuses the grant, and the person's list is an
// error, never a list without the connection; a connection gone is left
// out of it.
func TestV29UnreadableListAndConnections(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	p := w.person(accountA)
	for _, tc := range []struct {
		prefix string
		err    error
		info   error
	}{
		{RecordList, errDown, ErrUnavailable},
		{RecordList, store.ErrRolledBack, ErrInvalidToken},
		{RecordConnection, errDown, ErrUnavailable},
	} {
		h, f := w.hooked()
		failGets(f, tc.prefix, tc.err)
		if _, err := h.Info(ctx, g); !errors.Is(err, tc.info) {
			t.Errorf("%s %v: info %v", tc.prefix, tc.err, err)
		}
		if l, err := h.List(ctx, p); err == nil {
			t.Errorf("%s %v: a list %+v", tc.prefix, tc.err, l)
		}
	}
	h, f := w.hooked()
	failGets(f, RecordConnection, store.ErrNotFound)
	if l, err := h.List(ctx, p); err != nil || len(l.Connections) != 0 {
		t.Fatal(l, err)
	}
	f.set(nil)
	id := w.connectionOf(s, accountA, claudeCode).ID
	if _, err := h.RevokeDevice(ctx, p, id, "fam_unknown"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
}
