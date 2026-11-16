package oauth

import (
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

var familyAt = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func newFamily() *FamilyState {
	return &FamilyState{
		V: 1, Family: "fam_a", Connection: "con_a", ClientID: "https://chatgpt.com/oauth/codex/client.json",
		Resource: contracts.ResourceMCP, Scopes: slices.Clone(DefaultScopes), Status: FamilyActive, RotatedAt: familyAt,
	}
}

func ask(f *FamilyState, g uint64, at time.Duration, scope ...string) RefreshAsk {
	return RefreshAsk{ClientID: f.ClientID, Resource: f.Resource, Generation: g, Scope: scope, Now: familyAt.Add(at)}
}

// V25 and finding 3: the family's record alone decides the current
// generation; an older one is a reuse that ends the family, whatever the
// token entries say.
func TestAFamilyRotatesAndEndsOnReuse(t *testing.T) {
	f := newFamily()
	if r, is := f.Refresh(ask(f, 0, time.Minute)); r != RefreshRotated || is.Generation != 1 || !slices.Equal(is.Scope, DefaultScopes) {
		t.Fatalf("%v %+v", r, is)
	}
	if r, _ := f.Refresh(ask(f, 1, 2*time.Minute)); r != RefreshRotated || f.Generation != 2 {
		t.Fatalf("%v %d", r, f.Generation)
	}
	if r, _ := f.Refresh(ask(f, 0, 3*time.Minute)); r != RefreshReuse || f.Status != FamilyEnded || f.EndReason != string(contracts.EndedByReuse) {
		t.Fatalf("%v %+v", r, f)
	}
	if r, _ := f.Refresh(ask(f, 2, 4*time.Minute)); r != RefreshRefused {
		t.Fatalf("an ended family refreshed: %v", r)
	}
}

// Finding 7: a retry within the grace gets the same pair only for the same
// normalized scope; another scope is refused without ending the family; the
// answer's scope is the access token's.
func TestARetryIsBoundToItsScope(t *testing.T) {
	f := newFamily()
	_, first := f.Refresh(ask(f, 0, 0, ScopeSender))
	if !slices.Equal(first.Scope, []string{ScopeSender}) {
		t.Fatalf("issued %v", first.Scope)
	}
	if r, again := f.Refresh(ask(f, 0, 10*time.Second, ScopeSender, ScopeSender)); r != RefreshRetry || again.Generation != first.Generation || !slices.Equal(again.Scope, first.Scope) {
		t.Fatalf("%v %+v", r, again)
	}
	if r, _ := f.Refresh(ask(f, 0, 10*time.Second)); r != RefreshIncompatible || f.Status != FamilyActive {
		t.Fatalf("a broader retry: %v %v", r, f.Status)
	}
	if r, _ := f.Refresh(ask(f, 0, RefreshRetryGrace)); r != RefreshReuse {
		t.Fatalf("a retry past the grace: %v", r)
	}
}

// The refresh token keeps the family's scope: a narrower access token does
// not narrow what the next refresh may ask; a wider one is refused.
func TestTheFamilyKeepsItsGrantedScope(t *testing.T) {
	f := newFamily()
	f.Refresh(ask(f, 0, 0, ScopeSender))
	if r, is := f.Refresh(ask(f, 1, time.Minute)); r != RefreshRotated || !slices.Equal(is.Scope, DefaultScopes) {
		t.Fatalf("%v %+v", r, is)
	}
	if r, _ := f.Refresh(ask(f, 2, 2*time.Minute, ScopeAttachments)); r != RefreshWider || f.Generation != 2 {
		t.Fatalf("%v", r)
	}
	other := ask(f, 2, 3*time.Minute)
	other.ClientID = "https://claude.ai/oauth/claude-code-client-metadata"
	if r, _ := f.Refresh(other); r != RefreshRefused || f.Status != FamilyActive {
		t.Fatalf("another client: %v", r)
	}
	other = ask(f, 2, 3*time.Minute)
	other.Resource = contracts.ResourceAPI
	if r, _ := f.Refresh(other); r != RefreshRefused {
		t.Fatalf("another resource: %v", r)
	}
	if r, _ := f.Refresh(ask(f, 9, 3*time.Minute)); r != RefreshRefused || f.Status != FamilyActive {
		t.Fatalf("a generation never made: %v", r)
	}
}

// The rotation that reaches the next epoch moves the record; the next
// epoch goes on, a retry of the last generation of the old one included.
func TestAFamilyMovesToItsNextEpoch(t *testing.T) {
	f := newFamily()
	f.ScopeID, f.Scope = "qry_device", []byte(`[{"v":1,"all":[]}]`)
	f.Generation = FamilyEpochRotations - 1
	if r, is := f.Refresh(ask(f, FamilyEpochRotations-1, 0)); r != RefreshRotated || f.Status != FamilyMoved || is.Generation != FamilyEpochRotations {
		t.Fatalf("%v %v %+v", r, f.Status, is)
	}
	if r, _ := f.Refresh(ask(f, FamilyEpochRotations, time.Second)); r != RefreshRefused {
		t.Fatalf("a moved record decided: %v", r)
	}
	n := f.Next()
	if n.Epoch != 1 || n.Status != FamilyActive || EpochOf(n.Generation) != 1 || n.ScopeID != f.ScopeID || string(n.Scope) != string(f.Scope) {
		t.Fatalf("%+v", n)
	}
	n.Scope[0] = 'x'
	if f.Scope[0] == 'x' {
		t.Fatal("the next epoch shares the device's scope")
	}
	if r, _ := n.Refresh(ask(&n, FamilyEpochRotations-1, time.Second)); r != RefreshRetry {
		t.Fatalf("a retry across the epochs: %v", r)
	}
	if r, _ := n.Refresh(ask(&n, 3, time.Second)); r != RefreshReuse {
		t.Fatalf("an old epoch's token: %v", r)
	}
}

// Finding 8: a code works once, with no grace; a second exchange ends what
// the first made; a mismatch spends it.
func TestACodeWorksOnce(t *testing.T) {
	c := CodeState{Hash: "h", Family: "fam_a", ExpiresAt: familyAt.Add(CodeTTL), Status: CodeIssued}
	if r, changed := c.Exchange(true, familyAt); r != CodeOK || !changed {
		t.Fatalf("%v %v", r, changed)
	}
	if r, changed := c.Exchange(true, familyAt.Add(time.Second)); r != CodeReused || changed {
		t.Fatalf("a second use within a second: %v", r)
	}
	spent := CodeState{ExpiresAt: familyAt.Add(CodeTTL), Status: CodeIssued}
	if r, changed := spent.Exchange(false, familyAt); r != CodeRefused || !changed || spent.Status != CodeSpent {
		t.Fatalf("a wrong verifier: %v %v", r, spent.Status)
	}
	if r, _ := spent.Exchange(true, familyAt); r != CodeRefused {
		t.Fatalf("a spent code: %v", r)
	}
	late := CodeState{ExpiresAt: familyAt.Add(CodeTTL), Status: CodeIssued}
	if r, changed := late.Exchange(true, familyAt.Add(CodeTTL)); r != CodeRefused || changed {
		t.Fatalf("at its expiry: %v", r)
	}
}

// Finding 9: an assertion is accepted until exp plus the skew, exclusive,
// and its mark lives exactly as long.
func TestAnAssertionsMarkOutlivesItsAcceptance(t *testing.T) {
	exp := familyAt
	for _, c := range []struct {
		at   time.Duration
		live bool
	}{{-time.Second, true}, {0, true}, {time.Second, true}, {ClockSkew - time.Nanosecond, true}, {ClockSkew, false}, {ClockSkew + time.Second, false}} {
		if got := AssertionLive(exp, exp.Add(c.at)); got != c.live {
			t.Errorf("exp%+v: live %v, want %v", c.at, got, c.live)
		}
	}
	if !AssertionAcceptedUntil(exp).Equal(exp.Add(ClockSkew)) {
		t.Fatal("the mark's expiry")
	}
}

// Finding 5: the grant's deadline is the earliest of the token's expiry,
// the connection's end and the read's freshness.
func TestAGrantsDeadline(t *testing.T) {
	g := Grant{CheckedAt: familyAt, TokenExpiresAt: familyAt.Add(time.Hour), ConnectionEndsAt: familyAt.Add(48 * time.Hour)}
	if !g.Deadline().Equal(familyAt.Add(ConnectionFresh)) {
		t.Fatal(g.Deadline())
	}
	g.TokenExpiresAt = familyAt.Add(time.Second)
	if !g.Deadline().Equal(familyAt.Add(time.Second)) {
		t.Fatal(g.Deadline())
	}
	g.ConnectionEndsAt = familyAt.Add(time.Millisecond)
	if !g.Deadline().Equal(familyAt.Add(time.Millisecond)) {
		t.Fatal(g.Deadline())
	}
}

// N3 (the second design review): the hard end (the token's expiry, the
// connection's end) is told from the freshness of what the check read,
// which is renewed by reading again, so a preparation longer than
// ConnectionFresh reads the authority again instead of failing.
func TestReview2N3TheHardEndIsNotTheFreshness(t *testing.T) {
	g := Grant{CheckedAt: familyAt, TokenExpiresAt: familyAt.Add(time.Hour), ConnectionEndsAt: familyAt.Add(48 * time.Hour)}
	if !g.Expires().Equal(familyAt.Add(time.Hour)) || !g.FreshUntil().Equal(familyAt.Add(ConnectionFresh)) {
		t.Fatal(g.Expires(), g.FreshUntil())
	}
	g.TokenExpiresAt = time.Time{}
	if !g.Expires().Equal(familyAt.Add(48 * time.Hour)) {
		t.Fatal(g.Expires())
	}
	if (&Grant{CheckedAt: familyAt}).Expires() != (time.Time{}) || !(&Grant{CheckedAt: familyAt}).Deadline().Equal(familyAt.Add(ConnectionFresh)) {
		t.Fatal("a grant with no end")
	}
	// A six-second preparation: past the freshness, not past the hard end.
	at := familyAt.Add(6 * time.Second)
	if at.Before(g.FreshUntil()) || !at.Before(g.Expires()) {
		t.Fatal("the review's six seconds")
	}
}
