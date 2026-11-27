package graphsim

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests below are graphtest's scripts for spike items 4, 5 and 8: each
// plays one documented (or, where marked, assumed) behaviour against the
// production URLs and checks what an adapter built to ADR 0042 would see.

const (
	clientID  = "00000000-0000-0000-0000-00000000c1d0"
	workTID   = "11111111-2222-3333-4444-555555555555"
	adrScopes = "openid email offline_access https://graph.microsoft.com/Mail.Read"
	loginURL  = "https://" + LoginHost
	graphURL  = "https://" + GraphHost + "/v1.0"
)

func newSim(t *testing.T) *Sim {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func workAccount(s *Sim) *Account {
	return s.AddAccount(&Account{OID: "aaaaaaaa-0000-0000-0000-000000000001", TID: workTID, Sub: "pairwise-sub-work", Email: "work.user@example.test", PreferredUsername: "work.user@example.test"})
}

func personalAccount(s *Sim, locale string) *Account {
	return s.AddAccount(&Account{OID: "00000000-0000-0000-0000-0000000000p1", TID: PersonalTenant, Sub: "pairwise-sub-personal", Email: "personal.user@example.test", PreferredUsername: "personal.user@example.test", Locale: locale})
}

type answer struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
	proto  string
}

func do(t *testing.T, c *http.Client, method, u string, form url.Values, header map[string]string) answer {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header.Add(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	a := answer{status: resp.StatusCode, header: resp.Header, raw: raw, proto: resp.Proto}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &a.body); err != nil {
			t.Fatalf("%s %s: %v", method, u, err)
		}
	}
	return a
}

func redeem(t *testing.T, s *Sim, a *Account, scopes, tenant string) answer {
	t.Helper()
	code, rerr := s.Authorize(a, AuthorizeRequest{ClientID: clientID, Scopes: strings.Fields(scopes), Nonce: "n-0S6_WzA2Mj"})
	if rerr != nil {
		t.Fatalf("authorize: %v", rerr)
	}
	return do(t, s.Client(), http.MethodPost, loginURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"client_id": {clientID}, "client_secret": {"sim-secret"}, "grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {"http://localhost:8443/api/v1/mailboxes/connect/callback"}, "scope": {scopes},
	}, nil)
}

func refresh(t *testing.T, s *Sim, rt, scopes, tenant string) answer {
	t.Helper()
	return do(t, s.Client(), http.MethodPost, loginURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"client_id": {clientID}, "client_secret": {"sim-secret"}, "grant_type": {"refresh_token"},
		"refresh_token": {rt}, "scope": {scopes},
	}, nil)
}

// claims verifies an ID token against the simulator's published keys and
// returns its claims.
func claims(t *testing.T, s *Sim, tenant, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token has %d parts", len(parts))
	}
	keys := do(t, s.Client(), http.MethodGet, loginURL+"/"+tenant+"/discovery/v2.0/keys", nil, nil)
	k := keys.body["keys"].([]any)[0].(map[string]any)
	n, _ := base64.RawURLEncoding.DecodeString(k["n"].(string))
	e, _ := base64.RawURLEncoding.DecodeString(k["e"].(string))
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("id_token signature: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEveryScriptIsCited(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range Scripts {
		if seen[sc.ID] {
			t.Errorf("duplicate script %s", sc.ID)
		}
		seen[sc.ID] = true
		if !strings.HasPrefix(sc.Source, "https://learn.microsoft.com/") {
			t.Errorf("%s: source %q is not Microsoft's documentation", sc.ID, sc.Source)
		}
		if sc.Status != Documented && sc.Status != Undocumented {
			t.Errorf("%s: status %q", sc.ID, sc.Status)
		}
	}
}

// Item 4: the token response for exactly the scopes ADR 0042 requests.
func TestTripWithTheADRsScopesHasNoOID(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	r := redeem(t, s, a, adrScopes, "common")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.raw)
	}
	for _, f := range []string{"access_token", "token_type", "expires_in", "scope", "refresh_token", "id_token"} {
		if _, ok := r.body[f]; !ok {
			t.Errorf("token response lacks %s", f)
		}
	}
	c := claims(t, s, "common", r.body["id_token"].(string))
	if c["tid"] != workTID || c["sub"] == nil || c["nonce"] != "n-0S6_WzA2Mj" {
		t.Errorf("claims %v", c)
	}
	// id-token-oid-needs-profile: without `profile`, as documented, no oid
	// and no preferred_username. ADR 0042 requires tid and oid and refuses
	// the trip otherwise: ADR change needed (request `profile`).
	if _, ok := c["oid"]; ok {
		t.Error("oid present without the profile scope")
	}
	if _, ok := c["preferred_username"]; ok {
		t.Error("preferred_username present without the profile scope")
	}
	if c["email"] != "work.user@example.test" {
		t.Errorf("email %v", c["email"])
	}

	r = redeem(t, s, a, adrScopes+" profile", "common")
	c = claims(t, s, "common", r.body["id_token"].(string))
	if c["oid"] != a.OID || c["preferred_username"] == nil {
		t.Errorf("with profile: claims %v", c)
	}
}

func TestPersonalAccountTenant(t *testing.T) {
	s := newSim(t)
	a := personalAccount(s, "en")
	r := redeem(t, s, a, adrScopes+" profile", "common")
	c := claims(t, s, "common", r.body["id_token"].(string))
	if c["tid"] != PersonalTenant || c["iss"] != "https://"+LoginHost+"/"+PersonalTenant+"/v2.0" {
		t.Errorf("claims %v", c)
	}
}

func TestNoOfflineAccessNoRefreshToken(t *testing.T) {
	s := newSim(t)
	r := redeem(t, s, workAccount(s), "openid email https://graph.microsoft.com/Mail.Read", "common")
	if _, ok := r.body["refresh_token"]; ok {
		t.Error("refresh_token without offline_access")
	}
}

// adrScopeCheck is ADR 0042's Microsoft row of the scope table, read
// literally: `Mail.Read` and its URL form are one, the OIDC scopes are
// dropped, `Mail.Read` must be held, `User.Read` is allowed, anything else
// is refused. tolerant also folds case and percent-decoding.
func adrScopeCheck(scope string, present bool, requested string, tolerant bool) error {
	if !present {
		scope = requested
	}
	if tolerant {
		if d, err := url.QueryUnescape(scope); err == nil {
			scope = d
		}
	}
	held := false
	for _, s := range strings.Fields(scope) {
		short := strings.TrimPrefix(s, "https://graph.microsoft.com/")
		if tolerant {
			short = strings.TrimPrefix(strings.ToLower(s), "https://graph.microsoft.com/")
		}
		switch {
		case oidcScopes[strings.ToLower(short)]:
		case short == "Mail.Read" || (tolerant && short == "mail.read"):
			held = true
		case short == "User.Read" || (tolerant && short == "user.read"):
		default:
			return fmt.Errorf("unexpected scope %q", s)
		}
	}
	if !held {
		return errors.New("Mail.Read not granted")
	}
	return nil
}

func TestScopeRenderingsAgainstTheADRsComparison(t *testing.T) {
	cases := []struct {
		mode           ScopeMode
		strict, tolera bool // accepted by the literal and the tolerant check
	}{
		{ScopeResourceOnly, true, true},
		{ScopeWithOIDC, true, true},
		{ScopeOmitted, true, true},
		// token-scope-example-form: the documented example's form breaks a
		// case-sensitive comparison: ADR change needed (compare
		// case-insensitively and decide on percent-encoding; T201 confirms
		// the real form).
		{ScopeDocExample, false, true},
	}
	for _, tc := range cases {
		s := newSim(t)
		s.ScopeMode = tc.mode
		r := redeem(t, s, workAccount(s), adrScopes, "common")
		sc, present := r.body["scope"].(string)
		if got := adrScopeCheck(sc, present, adrScopes, false) == nil; got != tc.strict {
			t.Errorf("mode %d scope %q: literal check accepted=%v, want %v", tc.mode, sc, got, tc.strict)
		}
		if got := adrScopeCheck(sc, present, adrScopes, true) == nil; got != tc.tolera {
			t.Errorf("mode %d scope %q: tolerant check accepted=%v, want %v", tc.mode, sc, got, tc.tolera)
		}
	}
}

func TestRefreshRotatesAndOmitsTheIDToken(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	first := redeem(t, s, a, adrScopes+" profile", "common")
	rt1 := first.body["refresh_token"].(string)
	// ADR 0042's refresh scope, at the tenant's own endpoint.
	r := refresh(t, s, rt1, "https://graph.microsoft.com/Mail.Read offline_access", workTID)
	if r.status != 200 {
		t.Fatalf("refresh: %d %s", r.status, r.raw)
	}
	rt2 := r.body["refresh_token"].(string)
	if rt2 == rt1 {
		t.Error("refresh token not rotated")
	}
	// id-token-needs-openid: no `openid` in the refresh, no ID token, so the
	// ADR's tid/oid cross-check on a refresh never has a token to read.
	if _, ok := r.body["id_token"]; ok {
		t.Error("id_token on a refresh without openid")
	}
	// refresh-rotates: the old token is not revoked.
	if r := refresh(t, s, rt1, "https://graph.microsoft.com/Mail.Read offline_access", workTID); r.status != 200 {
		t.Errorf("old refresh token refused: %d", r.status)
	}
	// With openid the ID token comes back, without a nonce.
	r = refresh(t, s, rt2, "openid https://graph.microsoft.com/Mail.Read offline_access", workTID)
	c := claims(t, s, workTID, r.body["id_token"].(string))
	if c["oid"] != a.OID || c["nonce"] != nil {
		t.Errorf("refresh id_token claims %v", c)
	}
	// Another tenant's endpoint is refused (assumed).
	if r := refresh(t, s, rt2, "https://graph.microsoft.com/Mail.Read offline_access", PersonalTenant); r.status != 400 || r.body["error"] != "invalid_grant" {
		t.Errorf("refresh at another tenant: %d %v", r.status, r.body["error"])
	}
}

func TestPersonalRefreshAtItsTenantPath(t *testing.T) {
	s := newSim(t)
	r := redeem(t, s, personalAccount(s, "en"), adrScopes, "common")
	// refresh-tenant-path: not documented; the simulator accepts it.
	if r := refresh(t, s, r.body["refresh_token"].(string), "https://graph.microsoft.com/Mail.Read offline_access", PersonalTenant); r.status != 200 {
		t.Errorf("personal refresh at /%s: %d", PersonalTenant, r.status)
	}
}

func aadsts(r answer) float64 {
	codes, _ := r.body["error_codes"].([]any)
	if len(codes) == 0 {
		return 0
	}
	return codes[0].(float64)
}

func TestRefreshLifetime(t *testing.T) {
	s := newSim(t)
	r := redeem(t, s, workAccount(s), adrScopes, "common")
	rt := r.body["refresh_token"].(string)
	s.Advance(89 * 24 * time.Hour)
	r = refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID)
	if r.status != 200 {
		t.Fatalf("day 89: %d", r.status)
	}
	rt = r.body["refresh_token"].(string)
	s.Advance(89 * 24 * time.Hour) // renewed by the use on day 89
	if r := refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID); r.status != 200 {
		t.Fatalf("day 178: %d", r.status)
	}
	s.Advance(91 * 24 * time.Hour)
	r = refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID)
	if r.body["error"] != "invalid_grant" || aadsts(r) != 700082 {
		t.Errorf("after 91 idle days: %v %v", r.body["error"], aadsts(r))
	}
}

func TestPasswordChangesAndRevocation(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	rt := redeem(t, s, a, adrScopes, "common").body["refresh_token"].(string)
	// refresh-revocation-table: the person's own password change leaves a
	// confidential client's refresh token alive. ADR 0042 says Microsoft's
	// token "ends at a password reset": ADR change needed (only an admin
	// reset in the Entra or M365 admin center, or a revocation, ends it).
	s.UserChangesPassword(a)
	if r := refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID); r.status != 200 {
		t.Fatalf("after the user's own password change: %d", r.status)
	}
	s.AdminResetsPassword(a)
	r := refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID)
	if r.body["error"] != "invalid_grant" || aadsts(r) != 50173 {
		t.Errorf("after an admin reset: %v %v", r.body["error"], aadsts(r))
	}
}

func TestErrorShapes(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)

	// authorize-error-shape: the redirect carries `error` and the text only.
	_, q := s.Authorize(a, AuthorizeRequest{ClientID: clientID, Scopes: strings.Fields(adrScopes), Decline: true})
	if q.Get("error") != "access_denied" || q.Has("error_codes") {
		t.Errorf("decline redirect %v", q)
	}
	// admin-approval-redirect (not documented): ADR 0042 maps AADSTS65001
	// and AADSTS90094 to admin_approval_needed, but at the redirect the
	// number is only inside error_description, which Microsoft says never to
	// react to; the mapping is best-effort and must fail to provider_failed.
	a.AdminApprovalNeeded = true
	_, q = s.Authorize(a, AuthorizeRequest{ClientID: clientID, Scopes: strings.Fields(adrScopes)})
	if q.Get("error") != "access_denied" || !strings.HasPrefix(q.Get("error_description"), "AADSTS90094") {
		t.Errorf("admin approval redirect %v", q)
	}
	a.AdminApprovalNeeded = false

	// code-reused.
	code, _ := s.Authorize(a, AuthorizeRequest{ClientID: clientID, Scopes: strings.Fields(adrScopes)})
	form := url.Values{"client_id": {clientID}, "client_secret": {"x"}, "grant_type": {"authorization_code"}, "code": {code}}
	do(t, s.Client(), http.MethodPost, loginURL+"/common/oauth2/v2.0/token", form, nil)
	r := do(t, s.Client(), http.MethodPost, loginURL+"/common/oauth2/v2.0/token", form, nil)
	if r.body["error"] != "invalid_grant" || aadsts(r) != 54005 {
		t.Errorf("reused code: %v", r.body)
	}
	for _, f := range []string{"error", "error_description", "error_codes", "timestamp", "trace_id", "correlation_id"} {
		if _, ok := r.body[f]; !ok {
			t.Errorf("token error lacks %s", f)
		}
	}

	// interaction-required at a refresh.
	rt := redeem(t, s, a, adrScopes, "common").body["refresh_token"].(string)
	a.RequireMFA = true
	r = refresh(t, s, rt, "https://graph.microsoft.com/Mail.Read offline_access", workTID)
	if r.body["error"] != "interaction_required" || aadsts(r) != 50076 {
		t.Errorf("MFA: %v", r.body)
	}
}

func token(t *testing.T, s *Sim, a *Account) string {
	t.Helper()
	return redeem(t, s, a, adrScopes, "common").body["access_token"].(string)
}

func get(t *testing.T, s *Sim, at, path string, immutable bool, extra ...string) answer {
	t.Helper()
	h := map[string]string{"Authorization": "Bearer " + at}
	prefer := []string{}
	if immutable {
		prefer = append(prefer, `IdType="ImmutableId"`)
	}
	prefer = append(prefer, extra...)
	if len(prefer) > 0 {
		h["Prefer"] = strings.Join(prefer, ", ")
	}
	u := path
	if !strings.HasPrefix(u, "https://") {
		u = graphURL + path
	}
	return do(t, s.Client(), http.MethodGet, u, nil, h)
}

// Item 5: every excluded well-known folder, in a Spanish mailbox.
func TestWellKnownFoldersInALocalisedMailbox(t *testing.T) {
	s := newSim(t)
	a := personalAccount(s, "es")
	at := token(t, s, a)
	for _, wk := range []string{"junkemail", "deleteditems", "sentitems", "drafts", "outbox", "conversationhistory"} {
		r := get(t, s, at, "/me/mailFolders/"+wk, false)
		if r.status != 200 || r.body["id"] != "fold-"+wk {
			t.Errorf("%s: %d %v", wk, r.status, r.body)
		}
		if r.body["displayName"] == displayNames["en"][wk] {
			t.Errorf("%s: display name not localised", wk)
		}
	}
	// Moving a well-known folder: refused by assumption only.
	if err := s.MoveFolder(a, "fold-junkemail", "inbox"); !errors.Is(err, ErrWellKnown) {
		t.Errorf("move junkemail: %v", err)
	}
}

func TestFolderListingPagesAndHidesHidden(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	s.CreateFolder(a, "msgfolderroot", "Hidden one", true)
	r := get(t, s, at, "/me/mailFolders?includeHiddenFolders=false&$top=3", false)
	seen := 0
	for {
		for _, v := range r.body["value"].([]any) {
			seen++
			if v.(map[string]any)["isHidden"] == true {
				t.Error("hidden folder listed")
			}
		}
		next, ok := r.body["@odata.nextLink"].(string)
		if !ok {
			break
		}
		r = get(t, s, at, next, false)
	}
	if seen != len(wellKnown)-1 {
		t.Errorf("listed %d folders, want %d", seen, len(wellKnown)-1)
	}
}

type round struct {
	items    []map[string]any
	pages    int
	link     string
	statuses []int
}

func runRound(t *testing.T, s *Sim, at, first string, page int) round {
	t.Helper()
	var rd round
	u := first
	for {
		r := get(t, s, at, u, true, fmt.Sprintf("odata.maxpagesize=%d", page))
		rd.statuses = append(rd.statuses, r.status)
		if r.status != 200 {
			return rd
		}
		rd.pages++
		for _, v := range r.body["value"].([]any) {
			rd.items = append(rd.items, v.(map[string]any))
		}
		next, hasNext := r.body["@odata.nextLink"].(string)
		dl, hasDelta := r.body["@odata.deltaLink"].(string)
		if hasNext == hasDelta {
			t.Fatalf("page with nextLink=%v deltaLink=%v (delta-links)", hasNext, hasDelta)
		}
		if hasDelta {
			rd.link = dl
			return rd
		}
		u = next
	}
}

func TestDeltaRoundsMovesAndRemovals(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 25 {
		ids = append(ids, s.Deliver(a, "inbox", t0.Add(time.Duration(i)*time.Hour), []byte("synthetic")))
	}
	first := runRound(t, s, at, "/me/mailFolders/inbox/messages/delta?$select=id,receivedDateTime,parentFolderId", 10)
	if first.pages != 3 || len(first.items) != 25 {
		t.Fatalf("first round: %d pages, %d items", first.pages, len(first.items))
	}
	if !strings.Contains(first.link, "$deltatoken=") {
		t.Fatalf("delta link %q", first.link)
	}

	s.MoveMessage(a, ids[0], "junkemail")
	s.PurgeMessage(a, ids[1])
	s.DeleteMessage(a, ids[2])
	s.MarkRead(a, ids[3])
	newID := s.Deliver(a, "inbox", t0.Add(48*time.Hour), []byte("synthetic"))

	second := runRound(t, s, at, first.link, 10)
	got := map[string]string{}
	for _, it := range second.items {
		kind := "changed"
		if rm, ok := it["@removed"].(map[string]any); ok {
			kind = "removed:" + rm["reason"].(string)
		}
		got[it["id"].(string)] = kind
	}
	want := map[string]string{ids[0]: "removed:deleted", ids[1]: "removed:deleted", ids[2]: "removed:deleted", ids[3]: "changed", newID: "changed"}
	for id, k := range want {
		if got[id] != k {
			t.Errorf("%s: %q, want %q", id, got[id], k)
		}
	}
	// The removal says nothing of where the message went: only a read of
	// its parentFolderId does (the ADR's evidence rule).
	r := get(t, s, at, "/me/messages/"+ids[0]+"?$select=parentFolderId", true)
	if r.body["parentFolderId"] != "fold-junkemail" {
		t.Errorf("moved message's parent %v", r.body)
	}
}

func TestImmutableAndRegularIDs(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	imm := s.Deliver(a, "inbox", time.Now(), []byte("synthetic"))
	reg := get(t, s, at, "/me/messages/"+imm, false).body["id"].(string)
	for _, to := range []string{"junkemail", "deleteditems", "archive"} {
		s.MoveMessage(a, imm, to)
		r := get(t, s, at, "/me/messages/"+imm+"?$select=parentFolderId", true)
		if r.status != 200 || r.body["id"] != imm || r.body["parentFolderId"] != "fold-"+to {
			t.Errorf("after a move to %s: %d %v", to, r.status, r.body)
		}
	}
	// The regular id from before the moves no longer resolves (assumed 404).
	if r := get(t, s, at, "/me/messages/"+reg, false); r.status != 404 {
		t.Errorf("stale regular id: %d", r.status)
	}
	// Without the header the id is a regular one.
	if r := get(t, s, at, "/me/messages/"+imm, false); r.body["id"] == imm {
		t.Error("immutable id without the Prefer header")
	}
	s.PurgeMessage(a, imm)
	r := get(t, s, at, "/me/messages/"+imm+"?$select=parentFolderId", true)
	if r.status != 404 || r.body["error"].(map[string]any)["code"] != "ErrorItemNotFound" {
		t.Errorf("purged: %d %v", r.status, r.body)
	}
	r = get(t, s, at, "/me/messages/not-an-id", true)
	if r.status != 400 || r.body["error"].(map[string]any)["code"] != "ErrorInvalidIdMalformed" {
		t.Errorf("malformed: %d %v", r.status, r.body)
	}
}

func TestDeletedFolderAndEmptiedDeletedItems(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	proj := s.CreateFolder(a, "inbox", "Projects", false)
	imm := s.Deliver(a, proj, time.Now(), []byte("synthetic"))
	if err := s.DeleteFolder(a, proj); err != nil {
		t.Fatal(err)
	}
	f := get(t, s, at, "/me/mailFolders/"+proj, false)
	if f.body["parentFolderId"] != "fold-deleteditems" {
		t.Errorf("deleted folder's parent %v", f.body["parentFolderId"])
	}
	// Assumed: the message's parent is still the folder, not deleted items,
	// so its own parentFolderId is no evidence (the ADR's I2).
	if r := get(t, s, at, "/me/messages/"+imm+"?$select=parentFolderId", true); r.body["parentFolderId"] != proj {
		t.Errorf("message's parent %v", r.body["parentFolderId"])
	}
	s.EmptyDeletedItems(a)
	if r := get(t, s, at, "/me/messages/"+imm, true); r.status != 404 {
		t.Errorf("after emptying deleted items: %d", r.status)
	}
	if r := get(t, s, at, "/me/mailFolders/"+proj, false); r.status != 404 {
		t.Errorf("folder after emptying: %d", r.status)
	}
	if err := s.DeleteFolder(a, "fold-inbox"); !errors.Is(err, ErrWellKnown) {
		t.Errorf("delete inbox: %v", err)
	}
}

func TestDeltaTokenExpiryAndReset(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	s.Deliver(a, "inbox", time.Now(), []byte("synthetic"))
	link := runRound(t, s, at, "/me/mailFolders/inbox/messages/delta", 10).link

	s.ResetSync(a)
	r := get(t, s, at, link, true)
	if r.status != http.StatusGone || !strings.HasSuffix(r.header.Get("Location"), "$deltatoken=") {
		t.Errorf("reset: %d Location=%q", r.status, r.header.Get("Location"))
	}
	link = runRound(t, s, at, "/me/mailFolders/inbox/messages/delta", 10).link
	s.ExpireDeltaTokens(a)
	r = get(t, s, at, link, true)
	if r.status/100 != 4 || r.body["error"].(map[string]any)["code"] != "syncStateNotFound" {
		t.Errorf("expired: %d %v", r.status, r.body)
	}
}

func TestDeltaFilterCapsAt5000(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5001 {
		s.Deliver(a, "inbox", t0.Add(time.Duration(i)*time.Minute), nil)
	}
	filtered := runRound(t, s, at, "/me/mailFolders/inbox/messages/delta?$filter=receivedDateTime+ge+2025-12-01T00:00:00Z", 1000)
	if len(filtered.items) != 5000 {
		t.Errorf("filtered round: %d items, want 5000 (delta-filter-5000)", len(filtered.items))
	}
	unfiltered := runRound(t, s, at, "/me/mailFolders/inbox/messages/delta", 1000)
	if len(unfiltered.items) != 5001 {
		t.Errorf("unfiltered round: %d items", len(unfiltered.items))
	}
}

func TestThrottling(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	s.Throttle(1, http.StatusTooManyRequests, 7*time.Second)
	r := get(t, s, at, "/me/mailFolders/inbox", false)
	if r.status != 429 || r.header.Get("Retry-After") != "7" || r.body["error"].(map[string]any)["code"] != "TooManyRequests" {
		t.Errorf("429: %d %q %v", r.status, r.header.Get("Retry-After"), r.body)
	}
	s.Throttle(1, http.StatusServiceUnavailable, 30*time.Second)
	if r := get(t, s, at, "/me/mailFolders/inbox", false); r.status != 503 || r.header.Get("Retry-After") != "30" {
		t.Errorf("503: %d %q", r.status, r.header.Get("Retry-After"))
	}
	if r := get(t, s, at, "/me/mailFolders/inbox", false); r.status != 200 {
		t.Errorf("after throttling: %d", r.status)
	}
}

func TestFifthConcurrentRequest(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	release := make(chan struct{})
	s.mu.Lock()
	s.Hold = func() { <-release }
	s.mu.Unlock()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { get(t, s, at, "/me/mailFolders/inbox", false) })
	}
	for {
		s.mu.Lock()
		n := s.inFlight[a.OID]
		s.mu.Unlock()
		if n == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r := get(t, s, at, "/me/mailFolders/inbox", false)
	close(release)
	wg.Wait()
	if r.status != 429 || r.header.Get("Retry-After") == "" {
		t.Errorf("fifth concurrent request: %d", r.status)
	}
}

func TestMIMEValueLeavesReadStateAlone(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	body := []byte(strings.Repeat("x", 1<<20))
	imm := s.Deliver(a, "inbox", time.Now(), body)
	r := get(t, s, at, "/me/messages/"+imm+"/$value", true)
	if r.status != 200 || len(r.raw) != len(body) {
		t.Errorf("$value: %d, %d bytes", r.status, len(r.raw))
	}
	if s.IsRead(a, imm) {
		t.Error("$value marked the message read (assumed it does not)")
	}
}

func TestConditionalAccessAndQuota(t *testing.T) {
	s := newSim(t)
	a := workAccount(s)
	at := token(t, s, a)
	s.OverQuota(a, true)
	r := get(t, s, at, "/me/mailFolders/delta", false)
	if r.status != 403 || r.body["error"].(map[string]any)["code"] != "ErrorQuotaExceeded" {
		t.Errorf("folder delta over quota: %d %v", r.status, r.body)
	}
	a.RequireClaims = true
	r = get(t, s, at, "/me/mailFolders/inbox", false)
	if r.status != 403 || !strings.Contains(r.header.Get("WWW-Authenticate"), "insufficient_claims") {
		t.Errorf("CA challenge: %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}
}

// Item 8: the simulator offers h2; whether the real hosts do is T201's.
func TestHTTP2IsNegotiated(t *testing.T) {
	s := newSim(t)
	r := do(t, s.Client(), http.MethodGet, loginURL+"/common/v2.0/.well-known/openid-configuration", nil, nil)
	if r.proto != "HTTP/2.0" {
		t.Errorf("proto %s", r.proto)
	}
}
