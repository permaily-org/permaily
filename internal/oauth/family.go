package oauth

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// A token family is one device of a connection (the owner's decision 10):
// the refresh tokens one code started, each rotation the next generation.
// Which generation is current is decided only by the family's record, a
// strict anchored record (ADR 0016, 0026) that cannot be put back or hidden
// (ADR 0030, "Codes and tokens", finding 3). A token's sealed entry only
// says which family and generation it is, for the lookup: deleting it
// refuses the token, and putting back an older one presents an older
// generation, which the record calls a reuse.
//
// One record holds FamilyEpochRotations generations; the rotation that
// reaches the next epoch writes this record Moved and the next one made
// (CreateNew) with the same state. A reader follows Moved to the newest
// epoch before deciding; an epoch that is Moved and whose next was never
// made is made again from it by whoever reads it first.

// FamilyStatus is where a family's record is.
type FamilyStatus string

const (
	FamilyActive FamilyStatus = "active"
	FamilyEnded  FamilyStatus = "ended"
	// FamilyMoved: this epoch is closed; the family goes on in Epoch+1.
	FamilyMoved FamilyStatus = "moved"
)

// FamilyState is a family's record, sealed in the store.
type FamilyState struct {
	V          int                `json:"v"`
	Family     string             `json:"family"`
	Connection string             `json:"connection"`
	ClientID   string             `json:"client_id"`
	Resource   contracts.Resource `json:"resource"`
	// Scopes are what the family was granted, as the person confirmed them:
	// every refresh may ask for these or fewer (RFC 6749 section 6), and the
	// refresh tokens keep these, whatever an access token was narrowed to.
	Scopes []string `json:"scopes"`
	// ScopeID is the canonical identifier of the device's own formulas
	// when its scope is narrower than its connection's ("" when it is the
	// same), as the connection's record lists it for the device; Scope is
	// those formulas, canonical JSON, sealed with this record and copied
	// into each access token's entry, whose reads check them against the
	// identifier the connection's record holds (ADR 0030, N6): the
	// connection's record stays bounded whatever sixteen narrower scopes
	// weigh, and no entry can carry another scope.
	ScopeID string          `json:"scope_id,omitempty"`
	Scope   json.RawMessage `json:"scope,omitempty"`
	Epoch   uint64          `json:"epoch"`
	Status  FamilyStatus    `json:"status"`
	// Generation is the current refresh token's, made at RotatedAt.
	Generation uint64    `json:"generation"`
	RotatedAt  time.Time `json:"rotated_at"`
	// From and FromScope bind the last rotation: the generation it rotated
	// from and the scope it was asked for, normalized (Scopes' order). A
	// second use of From within RefreshRetryGrace asking the same scope gets
	// the same pair; asking another is refused.
	From      uint64    `json:"from"`
	FromScope []string  `json:"from_scope"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	EndReason string    `json:"end_reason,omitempty"`
}

// EpochOf is the epoch whose record holds generation g.
func EpochOf(g uint64) uint64 { return g / FamilyEpochRotations }

// RefreshAsk is one use of a refresh token: who presents it, for which
// resource, the generation its entry names, and the scope asked (nil for
// none: the family's).
type RefreshAsk struct {
	ClientID   string
	Resource   contracts.Resource
	Generation uint64
	Scope      []string
	Now        time.Time
}

// RefreshResult is what one use decides.
type RefreshResult int8

const (
	// RefreshRotated: the next generation is made; write the record at the
	// version read (and, when it Moved, make the next epoch), then issue
	// the pair of Issued.
	RefreshRotated RefreshResult = iota + 1
	// RefreshRetry: the same use again within the grace: issue the pair of
	// Issued again, which is the one the first use made. Nothing written.
	RefreshRetry
	// RefreshReuse: an older generation, or a retry past the grace: the
	// family is ended; write it, and end the family in its connection's
	// record. invalid_grant.
	RefreshReuse
	// RefreshIncompatible: a retry within the grace that asks another scope:
	// refused (invalid_grant), the family unchanged.
	RefreshIncompatible
	// RefreshWider: a scope the family was not granted: invalid_scope, the
	// family unchanged.
	RefreshWider
	// RefreshRefused: another client or resource, a generation the family
	// never made, a family ended, or a record Moved (read the next epoch):
	// invalid_grant, the family unchanged.
	RefreshRefused
)

// Issued is the pair a rotation or a retry hands out: the refresh token's
// generation and the access token's scope (the one the answer's scope
// says). Both tokens' values are the service's to make for that family and
// generation; a retry hands out the same values.
type Issued struct {
	Generation uint64
	Scope      []string
}

// Normalize is a requested scope in the family's order, without repeats;
// ok is false when it holds one the family was not granted. An empty one is
// the family's own.
func (f *FamilyState) Normalize(scope []string) (out []string, ok bool) {
	if len(scope) == 0 {
		return slices.Clone(f.Scopes), true
	}
	for _, s := range scope {
		if !slices.Contains(f.Scopes, s) {
			return nil, false
		}
	}
	for _, s := range f.Scopes {
		if slices.Contains(scope, s) {
			out = append(out, s)
		}
	}
	return out, true
}

// Refresh is the transition of one use of a refresh token on the newest
// epoch's record. It changes f only for RefreshRotated and RefreshReuse.
func (f *FamilyState) Refresh(a RefreshAsk) (RefreshResult, Issued) {
	if f.Status != FamilyActive || a.ClientID != f.ClientID || a.Resource != f.Resource || a.Generation > f.Generation {
		return RefreshRefused, Issued{}
	}
	scope, ok := f.Normalize(a.Scope)
	if !ok {
		return RefreshWider, Issued{}
	}
	switch {
	case a.Generation == f.Generation:
		f.From, f.FromScope = f.Generation, scope
		f.Generation++
		f.RotatedAt = a.Now
		if EpochOf(f.Generation) != f.Epoch {
			f.Status = FamilyMoved
		}
		return RefreshRotated, Issued{Generation: f.Generation, Scope: scope}
	case a.Generation == f.From && f.Generation == f.From+1 && a.Now.Before(f.RotatedAt.Add(RefreshRetryGrace)):
		if !slices.Equal(scope, f.FromScope) {
			return RefreshIncompatible, Issued{}
		}
		return RefreshRetry, Issued{Generation: f.Generation, Scope: f.FromScope}
	default:
		f.Status, f.EndedAt, f.EndReason = FamilyEnded, a.Now, string(contracts.EndedByReuse)
		return RefreshReuse, Issued{}
	}
}

// Next is the record of the next epoch after a rotation that Moved f.
func (f *FamilyState) Next() FamilyState {
	n := *f
	n.Scopes, n.FromScope = slices.Clone(f.Scopes), slices.Clone(f.FromScope)
	n.Scope = slices.Clone(f.Scope)
	n.Epoch, n.Status = EpochOf(f.Generation), FamilyActive
	return n
}

// End ends an active family (a device revoked, its connection ended): its
// tokens are refused from then on.
func (f *FamilyState) End(reason contracts.EndReason, now time.Time) bool {
	if f.Status != FamilyActive {
		return false
	}
	f.Status, f.EndedAt, f.EndReason = FamilyEnded, now, string(reason)
	return true
}
