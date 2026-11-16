package connections

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// A device's token family (oauth.FamilyState) in its strict records, one
// per epoch of oauth.FamilyEpochRotations rotations, and the entries of the
// tokens it hands out, which are for the lookup and decide nothing.

func familyName(family string, epoch uint64) string {
	return RecordFamily + family + "-" + strconv.FormatUint(epoch, 10)
}

// readFamily is the newest epoch of a family's record, from at least epoch
// from, following Moved and making an epoch a crash left unmade: nil when
// the family was never made or was removed.
func (s *Service) readFamily(ctx context.Context, family string, from uint64) (*oauth.FamilyState, store.Mark, error) {
	s.mu.Lock()
	e := max(s.epochs["family "+family], from)
	s.mu.Unlock()
	for range 8 {
		var f oauth.FamilyState
		at, found, err := s.readSealed(ctx, familyName(family, e), &f)
		if err != nil {
			return nil, at, err
		}
		if !found {
			if e == 0 {
				return nil, at, nil
			}
			var prev oauth.FamilyState
			_, ok, err := s.readSealed(ctx, familyName(family, e-1), &prev)
			if err != nil {
				return nil, at, err
			}
			if !ok || prev.Status != oauth.FamilyMoved {
				return nil, at, nil
			}
			next := prev.Next()
			if err := s.writeSealed(ctx, familyName(family, e), &next, store.Mark{}, true); err != nil && !retry(err) {
				return nil, at, err
			}
			continue
		}
		if f.Family != family || f.Epoch != e {
			return nil, at, errDocument
		}
		if f.Status == oauth.FamilyMoved {
			e++
			continue
		}
		s.mu.Lock()
		if e > s.epochs["family "+family] {
			s.epochs["family "+family] = e
		}
		s.mu.Unlock()
		return &f, at, nil
	}
	return nil, store.Mark{}, errBusy
}

// endFamily ends a family's record, if it is active.
func (s *Service) endFamily(ctx context.Context, family string, reason contracts.EndReason) error {
	for range RecordTries {
		f, at, err := s.readFamily(ctx, family, 0)
		if err != nil || f == nil {
			return err
		}
		if !f.End(reason, s.now()) {
			return nil
		}
		if err := s.writeSealed(ctx, familyName(family, f.Epoch), f, at, false); !retry(err) {
			return err
		}
	}
	return errBusy
}

// removeFamily removes every epoch of a family's records, oldest first.
func (s *Service) removeFamily(ctx context.Context, family string) error {
	for e := uint64(0); ; e++ {
		var f oauth.FamilyState
		_, found, err := s.readSealed(ctx, familyName(family, e), &f)
		if err != nil && !refusedRecord(err) {
			return err
		}
		if err := s.cfg.Records.RemoveRecord(ctx, familyName(family, e)); err != nil {
			return err
		}
		if !found || f.Status != oauth.FamilyMoved {
			return nil
		}
	}
}

// accessEntry is an access token's sealed entry.
type accessEntry struct {
	Connection string             `json:"connection"`
	Family     string             `json:"family"`
	Generation uint64             `json:"generation"`
	Account    string             `json:"account"`
	ClientID   string             `json:"client_id"`
	Vendor     string             `json:"vendor"`
	Resource   contracts.Resource `json:"resource"`
	Scopes     []string           `json:"scopes"`
	Issued     time.Time          `json:"issued"`
	Expires    time.Time          `json:"expires"`
	ScopeID    string             `json:"scope_id,omitempty"`
	Scope      json.RawMessage    `json:"scope,omitempty"`
}

// refreshEntry is a refresh token's sealed entry.
type refreshEntry struct {
	Connection string    `json:"connection"`
	Family     string    `json:"family"`
	Generation uint64    `json:"generation"`
	Account    string    `json:"account"`
	Expires    time.Time `json:"expires"`
}

// issue hands out the pair of a family's generation, issued at issued with
// scope: the same values for the same generation in every enclave
// (familySecret), so a retry answers what the first use answered. Its
// entries are made once; one there already is the same.
func (s *Service) issue(ctx context.Context, conn *connDoc, f *oauth.FamilyState, generation uint64, scope []string, issued time.Time) (*TokenAnswer, error) {
	access := familySecret(s.familyKey, oauth.KindAccess, f.Family, generation)
	refresh := familySecret(s.familyKey, oauth.KindRefresh, f.Family, generation)
	expires := issued.Add(oauth.AccessTokenTTL)
	if conn.EndsAt.Before(expires) {
		expires = conn.EndsAt
	}
	a := accessEntry{
		Connection: conn.ID, Family: f.Family, Generation: generation, Account: conn.Account, ClientID: conn.ClientID,
		Vendor: conn.Vendor, Resource: conn.Resource, Scopes: scope, Issued: issued, Expires: expires, ScopeID: f.ScopeID, Scope: f.Scope,
	}
	if err := s.putEntry(ctx, PartitionAccess+hashOf(oauth.KindAccess, access), a, expires); err != nil && !errors.Is(err, store.ErrExists) {
		return nil, err
	}
	r := refreshEntry{Connection: conn.ID, Family: f.Family, Generation: generation, Account: conn.Account, Expires: issued.Add(oauth.RefreshTokenTTL)}
	if err := s.putEntry(ctx, PartitionRefresh+hashOf(oauth.KindRefresh, refresh), r, r.Expires); err != nil && !errors.Is(err, store.ErrExists) {
		return nil, err
	}
	left := int(expires.Sub(s.now()) / time.Second)
	if left < 0 {
		left = 0
	}
	return &TokenAnswer{AccessToken: access, TokenType: "Bearer", ExpiresIn: left, RefreshToken: refresh, Scope: strings.Join(scope, " ")}, nil
}
