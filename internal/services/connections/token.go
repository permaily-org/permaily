package connections

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The token, revocation and pushed request endpoints (ADR 0030, "Codes and
// tokens", "private_key_jwt", "Pushed requests").

func oauthErr(code oauth.ErrorCode) error { return &OAuthError{Code: code} }

// single is a form's value, refused when it is sent more than once.
func single(form url.Values, k string) (string, bool) {
	v := form[k]
	if len(v) > 1 {
		return "", false
	}
	return strings.Join(v, ""), true
}

// admit counts a request of the three endpoints before anything is
// authenticated (TokenAdmissionPerMinute, all clients together).
func (s *Service) admit() error {
	if !s.limits.allow("admission", TokenAdmissionPerMinute, time.Minute, s.now()) {
		s.count("rate_limited")
		return contracts.ErrTooManyRequests
	}
	return nil
}

// client resolves and authenticates the client of a request of an
// endpoint: its document as cached, and, for one that declares
// private_key_jwt, a valid assertion for that endpoint whose jti its record
// accepts (C1). A public client's assertion is ignored; a client secret is
// refused for everyone.
func (s *Service) client(ctx context.Context, form url.Values, path string) (*vendors.Client, error) {
	id, ok := single(form, "client_id")
	if !ok || id == "" {
		s.count("invalid_client")
		return nil, oauthErr(oauth.ErrInvalidClient)
	}
	if _, secret := form["client_secret"]; secret {
		s.count("invalid_client")
		return nil, oauthErr(oauth.ErrInvalidClient)
	}
	c, err := s.cfg.Vendors.Client(ctx, id)
	switch {
	case errors.Is(err, vendors.ErrUnreachable):
		s.count("vendor_unreachable")
		return nil, oauthErr(oauth.ErrTemporarily)
	case err != nil:
		s.count("invalid_client")
		return nil, oauthErr(oauth.ErrInvalidClient)
	}
	if c.Document.TokenEndpointAuthMethod != oauth.AuthPrivateKeyJWT {
		return c, nil
	}
	if err := s.assertion(ctx, form, c, path); err != nil {
		return nil, err
	}
	return c, nil
}

// assertion checks a private_key_jwt client's assertion and records its
// use, before the request goes on.
func (s *Service) assertion(ctx context.Context, form url.Values, c *vendors.Client, path string) error {
	typ, ok1 := single(form, "client_assertion_type")
	jwt, ok2 := single(form, "client_assertion")
	if !ok1 || !ok2 || typ != oauth.AssertionType || jwt == "" {
		s.count("assertion")
		return oauthErr(oauth.ErrInvalidClient)
	}
	audiences := []string{s.cfg.OAuth.Issuer + path, s.cfg.OAuth.Issuer}
	now := s.now()
	keys, err := s.cfg.Vendors.Keys(ctx, c, false)
	if err != nil {
		s.count("assertion")
		return oauthErr(oauth.ErrInvalidClient)
	}
	jti, exp, err := oauth.VerifyAssertion(&c.Document, jwsKeys(keys), jwt, audiences, now)
	if errors.Is(err, jws.ErrUnknownKey) {
		if keys, err = s.cfg.Vendors.Keys(ctx, c, true); err == nil {
			jti, exp, err = oauth.VerifyAssertion(&c.Document, jwsKeys(keys), jwt, audiences, now)
		}
	}
	if err != nil {
		s.count("assertion")
		return oauthErr(oauth.ErrInvalidClient)
	}
	name, use := oauth.AssertionRecordName(c.ID, jti, exp)
	if s.usedHere("jti " + use) {
		s.count("assertion_replay")
		return oauthErr(oauth.ErrInvalidClient)
	}
	for range RecordTries {
		var rec oauth.AssertionRecord
		at, found, err := s.readSealed(ctx, name, &rec)
		if err != nil {
			if refusedRecord(err) {
				s.count("assertion")
				return oauthErr(oauth.ErrInvalidClient)
			}
			return ErrUnavailable
		}
		switch rec.Accept(use, exp, s.now(), at.Version()) {
		case oauth.AssertionReplayed:
			s.count("assertion_replay")
			return oauthErr(oauth.ErrInvalidClient)
		case oauth.AssertionExpired:
			s.count("assertion")
			return oauthErr(oauth.ErrInvalidClient)
		case oauth.AssertionFull:
			s.count("assertions_full")
			return oauthErr(oauth.ErrTemporarily)
		}
		rec.V = 1
		if !found {
			s.listAssertions(ctx, name, exp)
		}
		err = s.writeSealed(ctx, name, &rec, at, !found)
		if retry(err) {
			continue
		}
		if err != nil {
			return ErrUnavailable
		}
		s.remember("jti "+use, oauth.AssertionAcceptedUntil(exp))
		return nil
	}
	return ErrUnavailable
}

// assertionHour is the hour a record of assertions holds, as named.
func assertionHour(t time.Time) string { return t.UTC().Format("2006010215") }

// listAssertions keeps, sealed, the name of a record of assertions this
// enclave makes, under its hour, for Sweep to remove once it is dead. It
// decides nothing.
func (s *Service) listAssertions(ctx context.Context, name string, exp time.Time) {
	until := oauth.AssertionAcceptedUntil(exp)
	partition := PartitionJTI + assertionHour(until)
	value, err := envelope.SealText(s.sealKey, []byte(name), entryAAD(partition, name))
	if err != nil {
		return
	}
	_ = s.cfg.Entries.PutEntry(ctx, &store.Entry{Partition: partition, Key: name, Value: value, ExpiresAt: until.Add(72 * time.Hour), UpdatedAt: s.now()})
}

// Push implements API (RFC 9126).
func (s *Service) Push(ctx context.Context, form url.Values) (string, error) {
	if err := s.admit(); err != nil {
		return "", err
	}
	c, err := s.client(ctx, form, oauth.PathPushed)
	if err != nil {
		return "", err
	}
	p, err := oauth.Pushed(form, &c.Document, s.cfg.OAuth)
	if err != nil {
		err = endpointError(err)
		var oe *OAuthError
		if errors.As(err, &oe) {
			s.count(string(oe.Code))
		}
		return "", err
	}
	if _, err := s.parseWebhook(p.Webhook, c.ID); err != nil {
		// What webhook.ParseRequest refuses is refused here, before any
		// screen, with nothing of why (ADR 0039, K23).
		s.count(string(oauth.ErrInvalidRequest))
		return "", oauthErr(oauth.ErrInvalidRequest)
	}
	id, err := oauth.NewSecret(oauth.PrefixPushed, s.cfg.Random)
	if err != nil {
		return "", err
	}
	if err := s.putEntry(ctx, PartitionPushed+oauth.Hash(oauth.KindPushed, id), pushedEntry{ClientID: c.ID, Params: *p}, s.now().Add(oauth.PushedRequestTTL)); err != nil {
		return "", err
	}
	return oauth.RequestURIPrefix + id, nil
}

// Token implements API.
func (s *Service) Token(ctx context.Context, form url.Values) (*TokenAnswer, error) {
	if err := s.admit(); err != nil {
		return nil, err
	}
	grant, ok := single(form, "grant_type")
	if !ok {
		return nil, oauthErr(oauth.ErrInvalidRequest)
	}
	switch grant {
	case "authorization_code":
		return s.exchange(ctx, form)
	case "refresh_token":
		return s.refresh(ctx, form)
	}
	s.count("unsupported_grant_type")
	return nil, oauthErr(oauth.ErrUnsupportedGrantType)
}

// grantRefused counts and answers invalid_grant.
func (s *Service) grantRefused(reason string) error {
	s.count(reason)
	return oauthErr(oauth.ErrInvalidGrant)
}

// exchange is a code's exchange (oauth.CodeState.Exchange).
func (s *Service) exchange(ctx context.Context, form url.Values) (*TokenAnswer, error) {
	code, ok := single(form, "code")
	verifier, ok2 := single(form, "code_verifier")
	redirect, ok3 := single(form, "redirect_uri")
	resource, ok4 := single(form, "resource")
	if !ok || !ok2 || !ok3 || !ok4 || !strings.HasPrefix(code, oauth.PrefixCode) {
		return nil, oauthErr(oauth.ErrInvalidRequest)
	}
	c, err := s.client(ctx, form, oauth.PathToken)
	if err != nil {
		return nil, err
	}
	var e codeEntry
	if err := s.getEntry(ctx, PartitionCode+oauth.Hash(oauth.KindCode, code), &e); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, s.grantRefused("invalid_grant")
		}
		return nil, ErrUnavailable
	}
	matches := e.ClientID == c.ID && e.RedirectURI == redirect && oauth.VerifyPKCE(e.Challenge, verifier) && (resource == "" || resource == e.ResourceURI)
	for range RecordTries {
		d, at, err := s.readConn(ctx, e.Connection)
		if err != nil && !refusedRecord(err) {
			return nil, ErrUnavailable
		}
		if d == nil || d.Account != e.Account {
			return nil, s.grantRefused("invalid_grant")
		}
		dev := d.device(e.Family)
		if dev == nil || dev.Code.Hash != oauth.Hash(oauth.KindCode, code) {
			return nil, s.grantRefused("invalid_grant")
		}
		now := s.now()
		result, changed := dev.Code.Exchange(matches, now)
		switch result {
		case oauth.CodeReused:
			// A second exchange: the family the first made ends (finding 8).
			if err := s.endForReuse(ctx, d.ID, dev.ID); err != nil {
				return nil, ErrUnavailable
			}
			return nil, s.grantRefused("reuse")
		case oauth.CodeRefused:
			if changed {
				if err := s.writeConn(ctx, d, at, false); retry(err) {
					continue
				}
			}
			return nil, s.grantRefused("invalid_grant")
		}
		if !d.live(now) || dev.State != contracts.ConnectionActive {
			return nil, s.grantRefused("invalid_grant")
		}
		if err := s.usable(ctx, d, true); err != nil {
			return nil, err
		}
		if err := s.writeConn(ctx, d, at, false); retry(err) {
			continue
		} else if err != nil {
			return nil, ErrUnavailable
		}
		f := &oauth.FamilyState{
			V: 1, Family: dev.ID, Connection: d.ID, ClientID: d.ClientID, Resource: d.Resource, Scopes: slices.Clone(dev.Scopes),
			ScopeID: e.ScopeID, Scope: e.Scope, Status: oauth.FamilyActive, RotatedAt: now,
		}
		if err := s.writeSealed(ctx, familyName(f.Family, 0), f, store.Mark{}, true); err != nil {
			return nil, ErrUnavailable
		}
		s.count("token_issued")
		return s.issue(ctx, d, f, 0, f.Scopes, now)
	}
	return nil, ErrUnavailable
}

// endForReuse ends a device whose code or refresh token was used again
// (EndedByReuse) and, once its end is written here, tells of it (ADR 0041,
// E33): a device another reuse or enclave ended first is told of by that
// one, and the key, the connection and the device, makes it one notice.
func (s *Service) endForReuse(ctx context.Context, id, device string) error {
	d, err := s.endDeviceHere(ctx, id, device, contracts.EndedByReuse)
	if d != nil {
		at := s.now()
		if dev := d.device(device); dev != nil {
			at = dev.EndedAt
		}
		s.notifyEndedReuse(ctx, d, device, at)
	}
	return err
}

// usable checks what a refresh or an exchange needs of the account: its
// standing (open or not agreed) and its list not closing, read now when
// fresh.
func (s *Service) usable(ctx context.Context, d *connDoc, fresh bool) error {
	st, _, err := s.standing(ctx, d.Account, fresh)
	if err != nil {
		return ErrUnavailable
	}
	if st != StandingOpen && st != StandingNotAgreed {
		return s.grantRefused("invalid_grant")
	}
	closing, _, err := s.listClosing(ctx, d.Account, fresh)
	if err != nil {
		return ErrUnavailable
	}
	if closing {
		return s.grantRefused("invalid_grant")
	}
	return nil
}

// refresh is a refresh token's use (oauth.FamilyState.Refresh).
func (s *Service) refresh(ctx context.Context, form url.Values) (*TokenAnswer, error) {
	token, ok := single(form, "refresh_token")
	resource, ok2 := single(form, "resource")
	scopeRaw, ok3 := single(form, "scope")
	if !ok || !ok2 || !ok3 || !strings.HasPrefix(token, oauth.PrefixRefresh) {
		return nil, oauthErr(oauth.ErrInvalidRequest)
	}
	c, err := s.client(ctx, form, oauth.PathToken)
	if err != nil {
		return nil, err
	}
	var e refreshEntry
	if err := s.getEntry(ctx, PartitionRefresh+oauth.Hash(oauth.KindRefresh, token), &e); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, s.grantRefused("invalid_grant")
		}
		return nil, ErrUnavailable
	}
	if !s.now().Before(e.Expires) {
		return nil, s.grantRefused("invalid_grant")
	}
	if !s.limits.allow("refresh "+e.Family, RefreshesPerFamilyPerMinute, time.Minute, s.now()) {
		s.count("rate_limited")
		return nil, contracts.ErrTooManyRequests
	}
	var asked []string
	if _, sent := form["scope"]; sent {
		// mail.sender is every connection's, asked or not.
		if asked, err = oauth.Scopes(scopeRaw, true); err != nil {
			return nil, oauthErr(oauth.ErrInvalidScope)
		}
	}
	d, err := s.liveConn(ctx, e.Connection, e.Family)
	if err != nil {
		return nil, err
	}
	if err := s.usable(ctx, d, false); err != nil {
		return nil, err
	}
	res, okRes := resourceOf(s.cfg.OAuth, resource)
	if resource != "" && (!okRes || res != d.Resource) {
		return nil, s.grantRefused("invalid_grant")
	}
	for range RecordTries {
		f, at, err := s.readFamily(ctx, e.Family, oauth.EpochOf(e.Generation))
		if err != nil {
			if refusedRecord(err) {
				return nil, s.grantRefused("invalid_grant")
			}
			return nil, ErrUnavailable
		}
		if f == nil || f.Connection != d.ID {
			return nil, s.grantRefused("invalid_grant")
		}
		now := s.now()
		result, issued := f.Refresh(oauth.RefreshAsk{ClientID: c.ID, Resource: d.Resource, Generation: e.Generation, Scope: asked, Now: now})
		switch result {
		case oauth.RefreshRetry:
			s.count("refresh_retry")
			return s.issue(ctx, d, f, issued.Generation, issued.Scope, f.RotatedAt)
		case oauth.RefreshReuse:
			if err := s.endForReuse(ctx, d.ID, f.Family); err != nil {
				return nil, ErrUnavailable
			}
			return nil, s.grantRefused("reuse")
		case oauth.RefreshWider:
			s.count("invalid_scope")
			return nil, oauthErr(oauth.ErrInvalidScope)
		case oauth.RefreshIncompatible, oauth.RefreshRefused:
			return nil, s.grantRefused("invalid_grant")
		}
		// Rotated: written at the version read before the pair is issued.
		moved := f.Status == oauth.FamilyMoved
		if err := s.writeSealed(ctx, familyName(f.Family, f.Epoch), f, at, false); retry(err) {
			continue
		} else if err != nil {
			return nil, ErrUnavailable
		}
		if moved {
			next := f.Next()
			if err := s.writeSealed(ctx, familyName(f.Family, next.Epoch), &next, store.Mark{}, true); err != nil && !errors.Is(err, store.ErrExists) {
				return nil, ErrUnavailable
			}
		}
		s.count("token_issued")
		return s.issue(ctx, d, f, issued.Generation, issued.Scope, now)
	}
	return nil, ErrUnavailable
}

// liveConn is a connection, read now, with its device active and its end
// to come: invalid_grant otherwise (connection_ended past its end).
func (s *Service) liveConn(ctx context.Context, id, device string) (*connDoc, error) {
	if s.revokedHere(id, device) {
		return nil, s.grantRefused("invalid_grant")
	}
	d, _, err := s.readConn(ctx, id)
	if err != nil && !refusedRecord(err) {
		return nil, ErrUnavailable
	}
	if !d.live(s.now()) {
		return nil, s.grantRefused("connection_ended")
	}
	dev := d.device(device)
	if dev == nil || dev.State != contracts.ConnectionActive {
		return nil, s.grantRefused("invalid_grant")
	}
	return d, nil
}

// RevokeToken implements API (RFC 7009): a token of the authenticated
// client ends its device; any other is answered as if it had been.
func (s *Service) RevokeToken(ctx context.Context, form url.Values) error {
	if err := s.admit(); err != nil {
		return err
	}
	token, ok := single(form, "token")
	if !ok {
		return oauthErr(oauth.ErrInvalidRequest)
	}
	c, err := s.client(ctx, form, oauth.PathRevoke)
	if err != nil {
		return err
	}
	var conn, family string
	switch {
	case strings.HasPrefix(token, oauth.PrefixAccess):
		var e accessEntry
		if err := s.getEntry(ctx, PartitionAccess+oauth.Hash(oauth.KindAccess, token), &e); err != nil {
			return s.unknownToken(err)
		}
		if e.ClientID != c.ID {
			return nil
		}
		conn, family = e.Connection, e.Family
	case strings.HasPrefix(token, oauth.PrefixRefresh):
		var e refreshEntry
		if err := s.getEntry(ctx, PartitionRefresh+oauth.Hash(oauth.KindRefresh, token), &e); err != nil {
			return s.unknownToken(err)
		}
		d, _, err := s.readConn(ctx, e.Connection)
		if err != nil && !refusedRecord(err) {
			return ErrUnavailable
		}
		if d == nil || d.ClientID != c.ID {
			return nil
		}
		conn, family = e.Connection, e.Family
	default:
		return nil
	}
	if err := s.endDevice(ctx, conn, family, contracts.EndedByVendor); err != nil {
		return ErrUnavailable
	}
	s.count("revoked_vendor")
	return nil
}

// unknownToken is the answer for a token that is not found: nothing, as RFC
// 7009 says, unless the storage could not be read.
func (s *Service) unknownToken(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return ErrUnavailable
}
