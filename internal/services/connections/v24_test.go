package connections

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

// approved is a request allowed through s, with its code not exchanged.
func (w *world) approved(s *Service, client string) (*ask, string) {
	w.t.Helper()
	a := w.ask(client)
	p := w.person(accountA)
	return a, w.allow(s, p, w.start(s, p, a))
}

// V24. Codes: exchanged once, with no grace; a mismatching exchange spends
// the code; 60 seconds; the entry deleted, or put back after its use.
func TestV24Codes(t *testing.T) {
	ctx := context.Background()

	t.Run("once; a second exchange ends the family it made", func(t *testing.T) {
		for _, second := range []string{"at once", "through the other enclave"} {
			w := newWorld(t)
			a, b := w.enclave(), w.enclave()
			x, code := w.approved(a, claudeCode)
			tok, err := w.exchange(a, x, code)
			if err != nil {
				t.Fatal(err)
			}
			s := a
			if second != "at once" {
				s = b
			}
			if _, err := w.exchange(s, x, code); oauthCode(err) != oauth.ErrInvalidGrant {
				t.Fatalf("%s: %v", second, err)
			}
			for _, s := range []*Service{a, w.enclave()} {
				if _, err := w.check(s, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("%s: its tokens still work: %v", second, err)
				}
			}
			if _, err := w.refresh(b, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
				t.Fatal(err)
			}
		}
	})

	t.Run("a mismatching exchange spends it", func(t *testing.T) {
		for name, change := range map[string]func(form *ask){
			"verifier": func(x *ask) { x.verifier = strings.Repeat("v", 43) },
			"redirect": func(x *ask) { x.redirect = "http://127.0.0.1/callback" },
			"resource": func(x *ask) { x.resource = apiResource },
		} {
			w := newWorld(t)
			s := w.enclave()
			x, code := w.approved(s, claudeCode)
			wrong := *x
			change(&wrong)
			if _, err := w.exchange(s, &wrong, code); oauthCode(err) != oauth.ErrInvalidGrant {
				t.Fatalf("%s: %v", name, err)
			}
			if _, err := w.exchange(s, x, code); oauthCode(err) != oauth.ErrInvalidGrant {
				t.Fatalf("%s: a correct exchange after it: %v", name, err)
			}
		}
		// Another client: the code is spent too.
		w := newWorld(t)
		s := w.enclave()
		x, code := w.approved(s, claudeCode)
		wrong := *x
		wrong.client = otherApp
		if _, err := w.exchange(s, &wrong, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		if _, err := w.exchange(s, x, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
	})

	t.Run("60 seconds", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x, code := w.approved(s, claudeCode)
		w.clock.add(oauth.CodeTTL)
		if _, err := w.exchange(s, x, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
	})

	t.Run("its entry deleted, or put back after its use", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x, code := w.approved(s, claudeCode)
		partition := PartitionCode + oauth.Hash(oauth.KindCode, code)
		saved, err := w.mem.GetEntry(ctx, partition, entryKey)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.mem.DeleteEntry(ctx, partition, entryKey)
		if _, err := w.exchange(s, x, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		w.mem.SetEntry(*saved)
		tok, err := w.exchange(s, x, code)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.mem.DeleteEntry(ctx, partition, entryKey)
		w.mem.SetEntry(*saved)
		if _, err := w.exchange(w.enclave(), x, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		if _, err := w.check(s, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		for _, form := range []url.Values{
			{"grant_type": {"authorization_code"}, "code": {"nothing"}, "client_id": {claudeCode}},
			{"grant_type": {"authorization_code"}, "code": {"pmc_x", "pmc_y"}, "client_id": {claudeCode}},
			{"grant_type": {"authorization_code", "refresh_token"}},
			{"grant_type": {"password"}},
			{"grant_type": {"refresh_token"}, "refresh_token": {"pma_x"}, "client_id": {claudeCode}},
			{"grant_type": {"authorization_code"}, "code": {"pmc_x"}, "client_id": {claudeCode, claudeCode}},
			{"grant_type": {"authorization_code"}, "code": {"pmc_x"}, "client_id": {claudeCode}, "client_secret": {"s"}},
			{"grant_type": {"authorization_code"}, "code": {"pmc_x"}, "client_id": {"https://nobody.example/c"}},
		} {
			if _, err := s.Token(ctx, form); oauthCode(err) == "" {
				t.Errorf("%v: %v", form, err)
			}
		}
		if _, err := s.Token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {"pmc_unknown"}, "client_id": {claudeCode}}); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Error(err)
		}
	})
}

// V25. Refresh.
func TestV25Refresh(t *testing.T) {
	ctx := context.Background()

	t.Run("rotation, a retry, and another scope within the grace", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		x := w.ask(claudeCode)
		tok, _ := w.connect(a, w.person(accountA), x)
		next, err := w.refresh(a, x, tok.RefreshToken)
		if err != nil || next.RefreshToken == tok.RefreshToken || next.AccessToken == tok.AccessToken {
			t.Fatal(next, err)
		}
		w.clock.add(oauth.RefreshRetryGrace - time.Second)
		again, err := w.refresh(b, x, tok.RefreshToken)
		if err != nil || again.AccessToken != next.AccessToken || again.RefreshToken != next.RefreshToken {
			t.Fatal(again, err)
		}
		if _, err := w.refresh(b, x, tok.RefreshToken, oauth.ScopeSender); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		w.grant(b, x, next.AccessToken) // the family unchanged
		w.clock.add(2 * time.Second)
		// Reuse after the grace ends the device and every token of it.
		if _, err := w.refresh(b, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		for _, s := range []*Service{a, b} {
			if _, err := w.check(s, x, next.AccessToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatal(err)
			}
		}
		if _, err := w.refresh(a, x, next.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		c := w.connectionOf(a, accountA, claudeCode)
		if c.Devices[0].State != contracts.ConnectionEnded || c.Devices[0].EndReason != contracts.EndedByReuse || c.State != contracts.ConnectionActive {
			t.Fatalf("%+v", c)
		}
	})

	t.Run("two enclaves refreshing one token at once", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		x := w.ask(claudeCode)
		tok, _ := w.connect(a, w.person(accountA), x)
		var wg sync.WaitGroup
		out := make([]*TokenAnswer, 2)
		errs := make([]error, 2)
		for i, s := range []*Service{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i], errs[i] = w.refresh(s, x, tok.RefreshToken)
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || out[0].AccessToken != out[1].AccessToken || out[0].RefreshToken != out[1].RefreshToken {
			t.Fatal(errs, out)
		}
	})

	t.Run("a narrower scope, then the family's again; a wider one refused", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok, _ := w.connect(s, w.person(accountA), x)
		narrow, err := w.refresh(s, x, tok.RefreshToken, oauth.ScopeSender)
		if err != nil || narrow.Scope != oauth.ScopeSender {
			t.Fatal(narrow, err)
		}
		g := w.grant(s, x, narrow.AccessToken)
		if !slices.Equal(g.Parts, []contracts.Part{contracts.PartSender}) {
			t.Fatal(g.Parts)
		}
		full, err := w.refresh(s, x, narrow.RefreshToken, oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody)
		if err != nil || full.Scope != "mail.sender mail.subject mail.body" {
			t.Fatal(full, err)
		}
		if _, err := w.refresh(s, x, full.RefreshToken, oauth.ScopeAttachments); oauthCode(err) != oauth.ErrInvalidScope {
			t.Fatal(err)
		}
		if _, err := w.refresh(s, x, full.RefreshToken, "mail.unknown"); oauthCode(err) != oauth.ErrInvalidScope {
			t.Fatal(err)
		}
	})

	t.Run("the connection's end caps expires_in; renewing before it, not after", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(otherApp)
		p := w.person(accountA)
		req := w.pushed(s, p, x, "", url.Values{"ends_in_days": {"1"}})
		tok, err := w.exchange(s, x, w.allow(s, p, req))
		if err != nil {
			t.Fatal(err)
		}
		w.clock.add(24*time.Hour - 30*time.Minute)
		next, err := w.refresh(s, x, tok.RefreshToken)
		if err != nil || next.ExpiresIn > 30*60 || next.ExpiresIn < 29*60 {
			t.Fatal(next, err)
		}
		c := w.connectionOf(s, accountA, otherApp)
		p = w.person(accountA)
		v, _ := s.Get(ctx, p, c.ID)
		renewed, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version})
		if err != nil || !renewed.EndsAt.After(w.clock.now().Add(23*time.Hour)) {
			t.Fatal(renewed, err)
		}
		w.clock.add(25 * time.Hour)
		p = w.person(accountA)
		if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: renewed.Version}); !errors.Is(err, contracts.ErrConnectionEnded) {
			t.Fatal(err)
		}
		if _, err := w.refresh(s, x, next.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		if s.Refused()["connection_ended"] == 0 {
			t.Fatal(s.Refused())
		}
		// Renewing needs a fresh proof and the notice in force.
		stale := p
		stale.ConfirmedUntil = time.Time{}
		if _, err := s.Renew(ctx, stale, c.ID, contracts.ConnectionRenewal{}); !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Fatal(err)
		}
		if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: "old"}); !errors.Is(err, contracts.ErrModelNoticeChanged) {
			t.Fatal(err)
		}
	})

	t.Run("a refresh token past its 30 days, and ten a minute", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok, _ := w.connect(s, w.person(accountA), x)
		cur := tok
		for i := range RefreshesPerFamilyPerMinute {
			next, err := w.refresh(s, x, cur.RefreshToken)
			if err != nil {
				t.Fatal(i, err)
			}
			cur = next
		}
		if _, err := w.refresh(s, x, cur.RefreshToken); !errors.Is(err, contracts.ErrTooManyRequests) {
			t.Fatal(err)
		}
		w.clock.add(oauth.RefreshTokenTTL)
		if _, err := w.refresh(s, x, cur.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
	})

	t.Run("a family moves to its next epoch", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		x := w.ask(claudeCode)
		tok, _ := w.connect(s, w.person(accountA), x)
		c := w.connectionOf(s, accountA, claudeCode)
		fam := c.Devices[0].ID
		f, at, _ := s.readFamily(ctx, fam, 0)
		f.Generation = oauth.FamilyEpochRotations - 1
		if err := s.writeSealed(ctx, familyName(fam, 0), f, at, false); err != nil {
			t.Fatal(err)
		}
		last := oauth.FamilySecret(s.familyKey, oauth.KindRefresh, fam, f.Generation)
		if err := s.putEntry(ctx, PartitionRefresh+oauth.Hash(oauth.KindRefresh, last), refreshEntry{Connection: c.ID, Family: fam, Generation: f.Generation, Account: accountA, Expires: w.clock.now().Add(time.Hour)}, w.clock.now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		next, err := w.refresh(s, x, last)
		if err != nil {
			t.Fatal(err)
		}
		f1, _, err := w.enclave().readFamily(ctx, fam, 0)
		if err != nil || f1.Epoch != 1 || f1.Generation != oauth.FamilyEpochRotations {
			t.Fatal(f1, err)
		}
		if _, err := w.refresh(w.enclave(), x, next.RefreshToken); err != nil {
			t.Fatal(err)
		}
		_ = tok
	})
}

// V26. Check.
func TestV26Check(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, w.person(accountA), x)

	g := w.grant(s, x, tok.AccessToken)
	c := w.connectionOf(s, accountA, claudeCode)
	if g.Connection != c.ID || g.Family != c.Devices[0].ID || g.Account != accountA || g.ClientID != claudeCode || g.Vendor != "claude.ai" ||
		!g.TokenExpiresAt.Equal(start.Add(oauth.AccessTokenTTL)) || !g.ConnectionEndsAt.Equal(c.EndsAt) || g.CheckedAt.IsZero() ||
		!slices.Equal(g.Parts, []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody}) {
		t.Fatalf("%+v", g)
	}
	if _, err := s.Check(ctx, contracts.ResourceAPI, tok.AccessToken); !errors.Is(err, ErrWrongResource) {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "pmr_x", tok.RefreshToken, "pma_unknown", "pma_" + strings.Repeat("x", 200)} {
		if _, err := s.Check(ctx, contracts.ResourceMCP, bad); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(bad, err)
		}
	}
	// The storage failing is ErrUnavailable, never ErrInvalidToken.
	failing, f := w.hooked()
	f.set(func(op, name string) error { return errors.New("the storage is down") })
	if _, err := failing.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	f.set(nil)
	w.accts.mu.Lock()
	w.accts.fail = errors.New("down")
	w.accts.mu.Unlock()
	if _, err := w.enclave().Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	w.accts.mu.Lock()
	w.accts.fail = nil
	w.accts.mu.Unlock()

	// The account's standing and list.
	for _, st := range []Standing{StandingBlocked, StandingGone, StandingClosing} {
		w.accts.set(accountA, st)
		if _, err := w.enclave().Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(st, err)
		}
	}
	w.accts.set(accountA, StandingNotAgreed)
	w.grant(w.enclave(), x, tok.AccessToken)
	w.accts.set(accountA, StandingOpen)

	// An expired token.
	w.clock.add(oauth.AccessTokenTTL)
	if _, err := s.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

// V26: a token of a connection past its end, removed, rolled back or forked,
// of an ended device, of an account whose list is closing.
func TestV26CheckRefusesWhatItsRecordsRefuse(t *testing.T) {
	ctx := context.Background()
	for _, what := range []string{"past its end", "removed", "rolled back", "forked", "device ended", "list closing", "ended"} {
		t.Run(what, func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			x := w.ask(claudeCode)
			tok, _ := w.connect(s, w.person(accountA), x)
			c := w.connectionOf(s, accountA, claudeCode)
			name := connName(c.ID)
			switch what {
			case "past its end":
				w.clock.add(oauth.ConnectionDefaultDays * 24 * time.Hour)
			case "removed":
				if err := s.cfg.Records.RemoveRecord(ctx, name); err != nil {
					t.Fatal(err)
				}
			case "rolled back":
				// Ended, then the ended commit hidden: the active one shows again.
				if _, err := s.Revoke(ctx, w.person(accountA), c.ID); err != nil {
					t.Fatal(err)
				}
				head, _, _ := w.commits.Head(ctx, name)
				w.commits.Copy(name, head.Version, true)
				w.commits.Forget(name)
			case "forked":
				head, _, _ := w.commits.Head(ctx, name)
				w.commits.Fork(name, head.Version, sha256.Sum256([]byte("another")))
			case "device ended":
				if _, err := s.RevokeDevice(ctx, w.person(accountA), c.ID, c.Devices[0].ID); err != nil {
					t.Fatal(err)
				}
			case "list closing":
				if err := s.EndAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
			case "ended":
				if err := s.endConnection(ctx, c.ID, contracts.EndedByTime, "", false); err != nil {
					t.Fatal(err)
				}
			}
			if what == "removed" || what == "forked" {
				// Done by whoever writes the table, not through the service:
				// this enclave sees it once its read is ConnectionFresh old.
				w.clock.add(oauth.ConnectionFresh)
			}
			for _, e := range []*Service{s, w.enclave()} {
				if _, err := e.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("%v", err)
				}
			}
		})
	}
}

// V27. Revocation (the owner's decision 6), with a clock.
func TestV27Revocation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	tok, _ := w.connect(a, p, x)
	second := w.ask(claudeCode)
	tok2, err := w.exchange(a, second, w.allow(a, p, w.start(a, p, second)))
	if err != nil {
		t.Fatal(err)
	}
	c := w.connectionOf(a, accountA, claudeCode)

	// A device revoked: its tokens refused the same way, the others working.
	w.grant(b, x, tok.AccessToken)
	if _, err := a.RevokeDevice(ctx, p, c.ID, c.Devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(a, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	w.grant(b, x, tok.AccessToken) // within its five seconds
	w.grant(a, second, tok2.AccessToken)
	w.clock.add(oauth.ConnectionFresh)
	if _, err := w.check(b, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	w.grant(b, second, tok2.AccessToken)

	// The connection revoked through A.
	if _, err := a.Revoke(ctx, p, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(a, second, tok2.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	w.clock.add(oauth.ConnectionFresh - time.Millisecond)
	w.grant(b, second, tok2.AccessToken)
	if _, err := w.check(w.enclave(), second, tok2.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("a restarted enclave accepted it", err)
	}
	w.clock.add(time.Millisecond)
	if _, err := w.check(b, second, tok2.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	// Revoking again changes nothing; another account's is not found.
	if v, err := a.Revoke(ctx, p, c.ID); err != nil || v.State != contracts.ConnectionEnded || v.EndReason != contracts.EndedByPerson {
		t.Fatal(v, err)
	}
	if _, err := a.Revoke(ctx, w.person(accountB), c.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := a.RevokeDevice(ctx, p, c.ID, c.Devices[1].ID); !errors.Is(err, contracts.ErrConnectionEnded) {
		t.Fatal(err)
	}
	if _, err := a.RevokeDevice(ctx, p, c.ID, "fam_nothing"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}

	// An earlier copy of the record put back, in the table: refused by both
	// and after a restart.
	name := connName(c.ID)
	head, _, _ := w.commits.Head(ctx, name)
	w.commits.Copy(name, head.Version, true)
	w.commits.Forget(name)
	for _, s := range []*Service{a, b, w.enclave()} {
		if _, err := w.check(s, second, tok2.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
	}
	if len(w.rcpt.of("ended")) < 2 {
		t.Fatal("no receipt of what ended")
	}
}

// V28. The vendor's revocation (RFC 7009).
func TestV28VendorRevocation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, p, x)
	revoke := func(token, client string) error {
		form := url.Values{"token": {token}, "client_id": {client}}
		w.sign(form, client, oauth.PathRevoke)
		return s.RevokeToken(ctx, form)
	}
	if err := revoke("pma_unknown", claudeCode); err != nil {
		t.Fatal(err)
	}
	if err := revoke("something else", claudeCode); err != nil {
		t.Fatal(err)
	}
	// Another client's revocation of this token: 200, and nothing ends.
	if err := revoke(tok.AccessToken, otherApp); err != nil {
		t.Fatal(err)
	}
	if err := revoke(tok.RefreshToken, otherApp); err != nil {
		t.Fatal(err)
	}
	w.grant(s, x, tok.AccessToken)
	// Claude Code revokes its tokens before it signs in again: its device
	// ends, its connection stays with no active device.
	if err := revoke(tok.RefreshToken, claudeCode); err != nil {
		t.Fatal(err)
	}
	if err := revoke(tok.AccessToken, claudeCode); err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(s, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	c := w.connectionOf(s, accountA, claudeCode)
	if c.State != contracts.ConnectionActive || c.activeDevices() != 0 || c.Devices[0].EndReason != contracts.EndedByVendor {
		t.Fatalf("%+v", c)
	}
	// The new consent adds a device to it.
	again := w.ask(claudeCode)
	req := w.start(s, p, again)
	if req.Change != contracts.ChangeAddDevice {
		t.Fatal(req.Change)
	}
	tok2, err := w.exchange(s, again, w.allow(s, p, req))
	if err != nil {
		t.Fatal(err)
	}
	// An access token of the device revokes it too.
	if err := revoke(tok2.AccessToken, claudeCode); err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(s, again, tok2.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	// A private_key_jwt client must authenticate to revoke.
	cx := w.ask(chatgpt)
	ctok, _ := w.connect(s, p, cx)
	if err := s.RevokeToken(ctx, url.Values{"token": {ctok.AccessToken}, "client_id": {chatgpt}}); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	w.grant(s, cx, ctok.AccessToken)
	if err := s.RevokeToken(ctx, url.Values{"client_id": {claudeCode}, "token": {"a", "b"}}); oauthCode(err) != oauth.ErrInvalidRequest {
		t.Fatal(err)
	}
}

// V29. private_key_jwt at the token endpoint (C1, finding 9, choice 7).
func TestV29PrivateKeyJWT(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	p := w.person(accountA)
	x := w.ask(chatgpt)
	req := w.start(a, p, x)
	code := w.allow(a, p, req)

	// None: invalid_client, no fallback to a public client.
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {x.verifier}, "redirect_uri": {x.redirect}, "client_id": {chatgpt}}
	if _, err := a.Token(ctx, form); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	tok, err := w.exchange(a, x, code)
	if err != nil {
		t.Fatal(err)
	}

	// J authenticates a refresh through A; replayed through B, through a
	// restarted A, with the marks and the list deleted, with a current
	// credential of the same device and of another device of the client:
	// invalid_client, nothing issued, at all three endpoints.
	exp := w.clock.now().Add(5 * time.Minute)
	j := w.assertion(chatgpt, issuer+oauth.PathToken, "the-jti", exp)
	refreshWith := func(s *Service, token, assertion string) (*TokenAnswer, error) {
		return s.Token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {chatgpt},
			"client_assertion_type": {oauth.AssertionType}, "client_assertion": {assertion}})
	}
	next, err := refreshWith(a, tok.RefreshToken, j)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, PartitionJTI) || e.Key == markKey {
			_ = w.mem.DeleteEntry(ctx, e.Partition, e.Key)
		}
	}
	other := w.ask(chatgpt)
	otherTok, err := w.exchange(a, other, w.allow(a, p, w.start(a, p, other)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Service{b, w.enclave()} {
		for _, token := range []string{next.RefreshToken, otherTok.RefreshToken} {
			for _, at := range []time.Duration{-time.Second, 0, time.Second, oauth.ClockSkew - time.Second} {
				saved := w.clock.now()
				w.clock.t = exp.Add(at)
				_, err := refreshWith(s, token, j)
				w.clock.t = saved
				if oauthCode(err) != oauth.ErrInvalidClient {
					t.Fatalf("a replay at exp%+v: %v", at, err)
				}
			}
		}
		jr := w.assertion(chatgpt, issuer+oauth.PathRevoke, newJTI(), exp)
		if err := s.RevokeToken(ctx, url.Values{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {jr}}); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeToken(ctx, url.Values{"token": {next.AccessToken}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {jr}}); oauthCode(err) != oauth.ErrInvalidClient {
			t.Fatal(err)
		}
		jp := w.assertion(chatgpt, issuer, newJTI(), exp)
		pushForm := url.Values{"client_id": {chatgpt}, "redirect_uri": {x.redirect}, "response_type": {"code"}, "code_challenge": {x.challenge},
			"code_challenge_method": {"S256"}, "resource": {mcpResource}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {jp}}
		if _, err := s.Push(ctx, pushForm); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Push(ctx, pushForm); oauthCode(err) != oauth.ErrInvalidClient {
			t.Fatal(err)
		}
	}
	w.grant(a, x, next.AccessToken) // nothing was ended by the replays
	if b.Refused()["assertion_replay"] == 0 {
		t.Fatal(b.Refused())
	}

	t.Run("two enclaves presenting one assertion at once", func(t *testing.T) {
		j := w.assertion(chatgpt, issuer+oauth.PathRevoke, "at-once", w.clock.now().Add(time.Minute))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, s := range []*Service{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.RevokeToken(ctx, url.Values{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j}})
			}()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatal(errs)
		}
	})

	t.Run("a full record", func(t *testing.T) {
		exp := w.clock.now().Add(2 * time.Minute)
		name, _ := oauth.AssertionRecordName(chatgpt, "the-full-one", exp)
		var rec oauth.AssertionRecord
		at, found, err := a.readSealed(ctx, name, &rec)
		if err != nil {
			t.Fatal(err)
		}
		rec.V = 1
		for i := 0; len(rec.Uses) < oauth.AssertionsPerRecord; i++ {
			rec.Uses = append(rec.Uses, oauth.AssertionUse{Hash: strings.Repeat("0", 60) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)), Until: exp})
		}
		if err := a.writeSealed(ctx, name, &rec, at, !found); err != nil {
			t.Fatal(err)
		}
		j := w.assertion(chatgpt, issuer+oauth.PathRevoke, "the-full-one", exp)
		if err := b.RevokeToken(ctx, url.Values{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j}}); oauthCode(err) != oauth.ErrTemporarily {
			t.Fatal(err)
		}
		if b.Refused()["assertions_full"] != 1 {
			t.Fatal(b.Refused())
		}
	})

	t.Run("an assertion stands for nothing but the client", func(t *testing.T) {
		j := w.assertion(chatgpt, issuer+oauth.PathToken, "bearer", w.clock.now().Add(time.Minute))
		if _, err := a.Token(ctx, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {j}, "client_id": {chatgpt}}); oauthCode(err) != oauth.ErrUnsupportedGrantType {
			t.Fatal(err)
		}
		if _, err := a.Check(ctx, contracts.ResourceMCP, j); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
	})

	t.Run("assertions refused", func(t *testing.T) {
		now := w.clock.now()
		for name, j := range map[string]string{
			"another audience": w.assertion(chatgpt, "https://elsewhere.example/token", newJTI(), now.Add(time.Minute)),
			"expired":          w.assertion(chatgpt, issuer, newJTI(), now.Add(-2*time.Minute)),
			"too far":          w.assertion(chatgpt, issuer, newJTI(), now.Add(20*time.Minute)),
			"another client":   w.assertion(otherApp, issuer, newJTI(), now.Add(time.Minute)),
			"not a JWS":        "a.b",
			"garbage":          "!!.!!.!!",
		} {
			err := a.RevokeToken(ctx, url.Values{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j}})
			if oauthCode(err) != oauth.ErrInvalidClient {
				t.Errorf("%s: %v", name, err)
			}
		}
		// A key set that cannot be fetched.
		w.vendors.mu.Lock()
		saved := w.vendors.keys[chatgpt]
		delete(w.vendors.keys, chatgpt)
		w.vendors.mu.Unlock()
		j := w.assertion(chatgpt, issuer, newJTI(), now.Add(time.Minute))
		if err := a.RevokeToken(ctx, url.Values{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j}}); oauthCode(err) != oauth.ErrInvalidClient {
			t.Error(err)
		}
		w.vendors.mu.Lock()
		w.vendors.keys[chatgpt] = saved
		w.vendors.mu.Unlock()
	})
}
