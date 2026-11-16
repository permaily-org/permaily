package connections

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// Renewing: a connection not the person's, ended, under unagreed terms, or
// written meanwhile.
func TestRenewRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	c := w.connectionOf(s, accountA, claudeCode)
	v, _ := s.Get(ctx, p, c.ID)
	if _, err := s.Renew(ctx, w.person(accountB), c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	w.accts.set(accountA, StandingNotAgreed)
	if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); !errors.Is(err, contracts.ErrTermsChanged) {
		t.Fatal(err)
	}
	// Revoking still works under unagreed terms (finding 17).
	w.accts.set(accountA, StandingOpen)
	// The record written meanwhile by another enclave.
	f.set(func(op, name string) error {
		if op == "put" && name == connName(c.ID) {
			return store.ErrChanged
		}
		return nil
	})
	if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); !errors.Is(err, contracts.ErrConsentChanged) {
		t.Fatal(err)
	}
	f.set(nil)
	// A receipt that cannot be written fails the renewal's answer.
	w.rcpt.mu.Lock()
	w.rcpt.fail = errors.New("down")
	w.rcpt.mu.Unlock()
	if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); err == nil {
		t.Fatal("renewed without its receipt")
	}
	if after, err := w.enclave().Get(ctx, p, c.ID); err != nil || after.Version != v.Version || !after.EndsAt.Equal(v.EndsAt) {
		t.Fatal("a renewal without its receipt was made", err)
	}
	w.rcpt.mu.Lock()
	w.rcpt.fail = nil
	w.rcpt.mu.Unlock()
	w.accts.set(accountA, StandingNotAgreed)
	if _, err := s.Revoke(ctx, p, c.ID); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Get(ctx, p, c.ID)
	w.accts.set(accountA, StandingOpen)
	if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); !errors.Is(err, contracts.ErrConnectionEnded) {
		t.Fatal(err)
	}
}

// A pushed request: only its client may use it, once, by a request_uri of
// its shape; a refused pushed request is its JSON answer.
func TestPushedRequests(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	x := w.ask(otherApp)
	form := url.Values{"client_id": {otherApp}, "redirect_uri": {x.redirect}, "response_type": {"code"}, "code_challenge": {x.challenge},
		"code_challenge_method": {"S256"}, "resource": {mcpResource}}
	uri, err := s.Push(ctx, form)
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]url.Values{
		"another client":   {"client_id": {claudeCode}, "request_uri": {uri}},
		"another shape":    {"client_id": {otherApp}, "request_uri": {"urn:x:pmp_y"}},
		"two request uris": {"client_id": {otherApp}, "request_uri": {uri, uri}},
	} {
		if _, err := s.Start(ctx, p, q.Encode()); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Error(name, err)
		}
	}
	// With other parameters: the request is the pushed one, refused and
	// sent back.
	got, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}, "state": {"x"}}.Encode())
	if err != nil || !strings.Contains(got.Redirect, "error=invalid_request") {
		t.Fatal(got, err)
	}
	// Expired.
	uri, _ = s.Push(ctx, form)
	w.clock.add(oauth.PushedRequestTTL)
	if _, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode()); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Fatal(err)
	}
	// A query the account's mailboxes refuse, and a date not in the past.
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("query", `{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"`+mailboxID(9)+`"}]}]}`)
	uri, _ = s.Push(ctx, bad)
	if got, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode()); err != nil || !strings.Contains(got.Redirect, "invalid_request") {
		t.Fatal(got, err)
	}
	bad.Del("query")
	bad.Set("mail_since", w.clock.now().Add(time.Hour).Format(time.RFC3339))
	uri, _ = s.Push(ctx, bad)
	if got, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode()); err != nil || !strings.Contains(got.Redirect, "invalid_request") {
		t.Fatal(got, err)
	}
	// A redirect URI the document does not list is the client's problem
	// (oauth.Pushed: invalid_client).
	bad.Set("redirect_uri", "https://evil.example/x")
	if _, err := s.Push(ctx, bad); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	// An unknown client.
	if _, err := s.Push(ctx, url.Values{"client_id": {"https://nobody.example/c"}}); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	// A redirect URI the document no longer has when the request starts.
	uri, _ = s.Push(ctx, form)
	w.vendors.mu.Lock()
	saved := w.vendors.clients[otherApp].Document.RedirectURIs
	w.vendors.clients[otherApp].Document.RedirectURIs = []string{"https://tools.example.org/new"}
	w.vendors.mu.Unlock()
	if _, err := s.Start(ctx, p, url.Values{"client_id": {otherApp}, "request_uri": {uri}}.Encode()); ClientProblemOf(err) != contracts.RedirectURIInvalid {
		t.Fatal(err)
	}
	w.vendors.mu.Lock()
	w.vendors.clients[otherApp].Document.RedirectURIs = saved
	w.vendors.mu.Unlock()
}

// Adding a device refused: the connection ended, replaced or the account's
// list closing between the screen and the commit.
func TestAddingADeviceRefused(t *testing.T) {
	ctx := context.Background()
	for _, meanwhile := range []string{"revoked", "closing"} {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		w.connect(s, p, w.ask(claudeCode))
		req := w.start(s, p, w.ask(claudeCode))
		c := w.connectionOf(s, accountA, claudeCode)
		switch meanwhile {
		case "revoked":
			if _, err := s.Revoke(ctx, p, c.ID); err != nil {
				t.Fatal(err)
			}
		case "closing":
			if err := s.EndAll(ctx, accountA); err != nil {
				t.Fatal(err)
			}
		}
		d := req.Draft
		if _, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); err == nil {
			t.Fatal(meanwhile, "a device added")
		}
	}
}

// Ending what has ended ends nothing more; a family already ended is not
// written again.
func TestEndingTwice(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, p, x)
	c := w.connectionOf(s, accountA, claudeCode)
	dev := c.Devices[0].ID
	for range 2 {
		if err := s.endDevice(ctx, c.ID, dev, contracts.EndedByPerson); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.endFamily(ctx, dev, contracts.EndedByPerson); err != nil {
		t.Fatal(err)
	}
	if err := s.endDevice(ctx, "con_"+strings.Repeat("z", 26), dev, contracts.EndedByPerson); err != nil {
		t.Fatal(err)
	}
	if err := s.endConnection(ctx, "con_"+strings.Repeat("z", 26), contracts.EndedByPerson, "", false); err != nil {
		t.Fatal(err)
	}
	_ = tok
	// A vendor revoking a token of a device the person ended: nothing more.
	if err := s.RevokeToken(ctx, url.Values{"token": {tok.RefreshToken}, "client_id": {claudeCode}}); err != nil {
		t.Fatal(err)
	}
	// The ended receipts failing do not undo an end.
	w.rcpt.mu.Lock()
	w.rcpt.fail = errors.New("down")
	w.rcpt.mu.Unlock()
	if _, err := s.Revoke(ctx, p, c.ID); err != nil {
		t.Fatal(err)
	}
	if s.Refused()["receipt_failed"] == 0 {
		t.Fatal(s.Refused())
	}
}
