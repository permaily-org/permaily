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
// the family was never made or was removed. Past a Moved epoch it finds the
// newest one made in a number of reads that grows with the logarithm of the
// epochs passed (newestFamilyEpoch), so an old token reaches the current
// epoch however many there are, and what it found is kept for the next
// read (finding 10).
func (s *Service) readFamily(ctx context.Context, family string, from uint64) (*oauth.FamilyState, store.Mark, error) {
	s.mu.Lock()
	e := max(s.epochs["family "+family], from)
	s.mu.Unlock()
	for range familyReadsMax {
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
			if e, err = s.newestFamilyEpoch(ctx, family, e+1); err != nil {
				return nil, store.Mark{}, err
			}
			s.sawFamilyEpoch(family, e)
			continue
		}
		s.sawFamilyEpoch(family, e)
		return &f, at, nil
	}
	return nil, store.Mark{}, errBusy
}

// familyReadsMax bounds the passes of one readFamily: each either makes an
// unmade epoch or jumps to the newest made.
const familyReadsMax = 8

func (s *Service) sawFamilyEpoch(family string, e uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e > s.epochs["family "+family] {
		s.epochs["family "+family] = e
	}
}

// newestFamilyEpoch is the newest epoch of a family made from lo on, lo-1
// having been written Moved: lo itself when it is not made (a crash, which
// readFamily makes again). An epoch is made only after the one before it was
// written Moved, and a strict read refuses one made and then lost, so the
// epochs made are lo to some n: it doubles its step until one is not made,
// then halves between the last made and the first not.
func (s *Service) newestFamilyEpoch(ctx context.Context, family string, lo uint64) (uint64, error) {
	made := func(e uint64) (bool, error) {
		var f oauth.FamilyState
		_, found, err := s.readSealed(ctx, familyName(family, e), &f)
		if err != nil {
			return false, err
		}
		if found && (f.Family != family || f.Epoch != e) {
			return false, errDocument
		}
		return found, nil
	}
	ok, err := made(lo)
	if err != nil || !ok {
		return lo, err
	}
	good, bad := lo, uint64(0)
	for step := uint64(1); ; step *= 2 {
		ok, err := made(good + step)
		if err != nil {
			return 0, err
		}
		if !ok {
			bad = good + step
			break
		}
		good += step
	}
	for bad-good > 1 {
		mid := good + (bad-good)/2
		ok, err := made(mid)
		if err != nil {
			return 0, err
		}
		if ok {
			good = mid
		} else {
			bad = mid
		}
	}
	return good, nil
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
