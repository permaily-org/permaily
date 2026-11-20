package connections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// Checking a token on every request (ADR 0030, "Checking a token on every
// request"), and reading the authority again (Release).

// Check implements API.
func (s *Service) Check(ctx context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error) {
	if !strings.HasPrefix(bearer, oauth.PrefixAccess) || len(bearer) > 128 {
		s.count("invalid_token")
		return nil, ErrInvalidToken
	}
	var e accessEntry
	if err := s.getEntry(ctx, PartitionAccess+oauth.Hash(oauth.KindAccess, bearer), &e); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.count("invalid_token")
			return nil, ErrInvalidToken
		}
		return nil, ErrUnavailable
	}
	now := s.now()
	if !now.Before(e.Expires) {
		s.count("invalid_token")
		return nil, ErrInvalidToken
	}
	if e.Resource != r {
		s.count("wrong_resource")
		return nil, ErrWrongResource
	}
	g := &oauth.Grant{
		Connection: e.Connection, Family: e.Family, Account: e.Account, ClientID: e.ClientID, Vendor: e.Vendor, Resource: e.Resource,
		Scopes: slices.Clone(e.Scopes), TokenExpiresAt: e.Expires,
	}
	d, checked, err := s.authority(ctx, g, false)
	if err != nil {
		return nil, err
	}
	dev := d.device(e.Family)
	// A narrower device's token carries the formulas its connection's
	// record names by their identifier (N6).
	if dev.ScopeID != e.ScopeID {
		s.count("invalid_token")
		return nil, ErrInvalidToken
	}
	if e.ScopeID != "" {
		fs, err := decodeScope(e.Scope)
		if err != nil || scopeID(fs) != e.ScopeID {
			s.count("invalid_token")
			return nil, ErrInvalidToken
		}
		s.keepScope(e.ScopeID, fs)
	}
	g.Parts = intersect(oauth.Parts(e.Scopes), dev.Parts, d.Parts)
	g.ConnectionScopes = slices.Clone(d.Scopes)
	g.ConnectionEndsAt, g.CheckedAt = d.EndsAt, checked
	return g, nil
}

// errRefusedAuthority is an authority read again that no longer allows the
// grant: revoked, ended, closing, blocked or gone.
var errRefusedAuthority error = &kind{"connections: the connection may no longer be read"}

// authority checks a grant's connection, device, account's list and
// standing: from this enclave's reads younger than ConnectionFresh, or, when
// fresh, read again now (strictly). It answers the connection and the
// oldest instant what it used was read at. ErrInvalidToken when it refuses
// the grant; ErrUnavailable when the storage could not be read.
func (s *Service) authority(ctx context.Context, g *oauth.Grant, fresh bool) (*connDoc, time.Time, error) {
	if s.revokedHere(g.Connection, g.Family) {
		s.count("invalid_token")
		return nil, time.Time{}, ErrInvalidToken
	}
	var d *connDoc
	var read time.Time
	var err error
	if fresh {
		d, _, read, err = s.readConnAt(ctx, g.Connection)
	} else {
		d, read, err = s.connFresh(ctx, g.Connection)
	}
	if err != nil && !refusedRecord(err) {
		return nil, time.Time{}, ErrUnavailable
	}
	now := s.now()
	if err != nil || !d.live(now) || d.Account != g.Account {
		s.count("invalid_token")
		return nil, time.Time{}, ErrInvalidToken
	}
	if dev := d.device(g.Family); dev == nil || dev.State != contracts.ConnectionActive {
		if dev == nil && !fresh {
			// A device added since this enclave's read: read it again.
			return s.authority(ctx, g, true)
		}
		s.count("invalid_token")
		return nil, time.Time{}, ErrInvalidToken
	}
	accountRead, err := s.accountAuthority(ctx, d.Account, fresh)
	if errors.Is(err, ErrInvalidToken) {
		s.count("invalid_token")
		return nil, time.Time{}, ErrInvalidToken
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	// What it used is as old as the oldest of its reads, each dated when it
	// began (finding 2).
	read = earliest(read, accountRead)
	return d, read, nil
}

// accountAuthority is what of a connection's authority is its account's,
// the same for a vendor's read (authority) and a webhook's call
// (webhookAuthority): the account's list, read strictly (refused when it
// is closing, a fence committed whatever became of its members, or when
// its record is refused), and its standing (open or not agreed), each from
// this enclave's reads younger than ConnectionFresh, or read again now
// when fresh. It answers the oldest instant what it used was read at.
// ErrInvalidToken when it refuses, wrapping the list's error when its
// record was refused; ErrUnavailable when the storage could not be read.
func (s *Service) accountAuthority(ctx context.Context, account string, fresh bool) (time.Time, error) {
	closing, listRead, err := s.listClosing(ctx, account, fresh)
	if err != nil {
		if refusedRecord(err) {
			return time.Time{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
		}
		return time.Time{}, ErrUnavailable
	}
	if closing {
		return time.Time{}, ErrInvalidToken
	}
	st, stRead, err := s.standing(ctx, account, fresh)
	if err != nil {
		return time.Time{}, ErrUnavailable
	}
	if st != StandingOpen && st != StandingNotAgreed {
		return time.Time{}, ErrInvalidToken
	}
	return earliest(listRead, stRead), nil
}

// earliest is the earliest of ts.
func earliest(t time.Time, ts ...time.Time) time.Time {
	for _, u := range ts {
		if u.Before(t) {
			t = u
		}
	}
	return t
}

// again reads the authority of g again, strictly and now, when its
// freshness has passed, and refuses past its hard end: nil when what was
// prepared may still be recorded and released, extending g's freshness.
func (s *Service) again(ctx context.Context, g *oauth.Grant) error {
	now := s.now()
	if e := g.Expires(); !e.IsZero() && !now.Before(e) {
		return errRefusedAuthority
	}
	if s.revokedHere(g.Connection, g.Family) {
		return errRefusedAuthority
	}
	if now.Before(g.FreshUntil()) {
		return nil
	}
	checked, err := s.verify(ctx, g)
	if err != nil {
		return err
	}
	g.CheckedAt = checked
	return nil
}

// verify reads the authority of g strictly and now, and answers when that
// read began: errRefusedAuthority when it no longer allows g, ErrUnavailable
// when it could not be read or returned older than its freshness.
func (s *Service) verify(ctx context.Context, g *oauth.Grant) (time.Time, error) {
	if s.revokedHere(g.Connection, g.Family) {
		return time.Time{}, errRefusedAuthority
	}
	_, checked, err := s.authority(ctx, g, true)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return time.Time{}, errRefusedAuthority
	case err != nil:
		return time.Time{}, err
	}
	// A read that took longer than the freshness renews nothing: what it
	// read is already too old to release by (finding 2).
	now := s.now()
	if !now.Before(checked.Add(oauthFresh)) {
		s.count("authority_stale")
		return time.Time{}, ErrUnavailable
	}
	if e := g.Expires(); !e.IsZero() && !now.Before(e) {
		return time.Time{}, errRefusedAuthority
	}
	return checked, nil
}

// deviceScope is a device's own narrower formulas, from memory (by their
// identifier) or its family's record: errScope when that record is gone or
// refused, or its formulas are not the ones the device names;
// ErrUnavailable when it could not be read.
func (s *Service) deviceScope(ctx context.Context, d *connDoc, family string) ([]*query.Canonical, error) {
	dev := d.device(family)
	if dev == nil || dev.ScopeID == "" {
		return nil, nil
	}
	s.mu.Lock()
	fs, ok := s.scopes[dev.ScopeID]
	s.mu.Unlock()
	if ok {
		return fs, nil
	}
	f, _, err := s.readFamily(ctx, family, 0)
	if err != nil && !refusedRecord(err) {
		return nil, ErrUnavailable // storage that could not be read, never a refusal (V27)
	}
	if err != nil || f == nil {
		return nil, errScope
	}
	fs, err = decodeScope(f.Scope)
	if err != nil || scopeID(fs) != dev.ScopeID {
		return nil, errScope
	}
	s.keepScope(dev.ScopeID, fs)
	return fs, nil
}
