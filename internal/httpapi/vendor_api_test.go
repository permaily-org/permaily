package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// countedLease is connectionstest.Lease, counting its Finish calls and what
// each said.
type countedLease struct {
	*connectionstest.Lease
	mu       sync.Mutex
	finishes []bool
}

func newLease() *countedLease { return &countedLease{Lease: &connectionstest.Lease{}} }

func (l *countedLease) Finish(sent bool) {
	l.mu.Lock()
	l.finishes = append(l.finishes, sent)
	l.mu.Unlock()
	l.Lease.Finish(sent)
}

// finished says Finish was called exactly once, with sent.
func (l *countedLease) finished(t *testing.T, sent bool) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Equal(l.finishes, []bool{sent}) {
		t.Fatalf("Finish called %v, want once with %v", l.finishes, sent)
	}
}

const (
	apiToken = "pma_api"
	mcpToken = "pma_mcp"
)

var apiGrant = &oauth.Grant{
	Connection: "con_1", Family: "fam_1", Account: "acc_1", ClientID: "https://vendor.example/c", Resource: contracts.ResourceAPI,
	Parts:            []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments},
	Scopes:           []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments},
	ConnectionScopes: []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments},
}

// grants sets the fake's Check: the API's token is apiGrant (or g), the
// MCP's another resource's, anything else not valid.
func grants(f *connectionstest.Fake, g *oauth.Grant) *[]string {
	var asked []string
	var mu sync.Mutex
	f.CheckFunc = func(_ context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error) {
		mu.Lock()
		asked = append(asked, string(r)+" "+bearer)
		mu.Unlock()
		switch bearer {
		case apiToken:
			return g, nil
		case mcpToken:
			return nil, connections.ErrWrongResource
		}
		return nil, connections.ErrInvalidToken
	}
	return &asked
}

const challengeNone = `Bearer resource_metadata="https://api.permaily.test/.well-known/oauth-protected-resource/v1", scope="mail.sender mail.subject mail.body"`

var vendorRoutes = []consentRoute{
	{"GET", "/v1/connection", ""},
	{"POST", "/v1/messages/search", `{"limit":10}`},
	{"GET", "/v1/messages/mbx_1.18e7", ""},
	{"GET", "/v1/messages/mbx_1.18e7/attachments/0", ""},
}

// V45: every route of the API takes a bearer token of the API's resource,
// in its header alone: without one 401 with the challenge, the service not
// asked; one of MCP's, or one not valid, 401 invalid_token; a storage that
// could not be read 503, never 401.
func TestV45TheAPIsRoutesTakeTheAPIsBearerAlone(t *testing.T) {
	a := newConnApp(t)
	asked := grants(a.fake, apiGrant)
	for _, c := range vendorRoutes {
		for _, set := range []func(*http.Request){
			func(*http.Request) {},
			func(r *http.Request) { r.URL.RawQuery = "access_token=" + apiToken },
			func(r *http.Request) { r.Header.Set("Authorization", "Basic "+apiToken) },
			func(r *http.Request) { r.Header.Set("Cookie", httpapi.SessionCookie+"=fresh") },
		} {
			r := httptest.NewRequest(c.method, "https://"+apiHost+c.path, strings.NewReader(c.body))
			set(r)
			rec := a.serve(r)
			want(t, rec, 401, "unauthorized")
			if rec.Header().Get("WWW-Authenticate") != challengeNone || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: %v", c.path, rec.Header())
			}
		}
		for _, token := range []string{mcpToken, "pma_unknown", " "} {
			rec := a.vendor(token, c.method, c.path, c.body)
			want(t, rec, 401, "invalid_token")
			if rec.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token", `+challengeNone[len("Bearer "):] {
				t.Fatalf("%s: %v", c.path, rec.Header())
			}
		}
		r := httptest.NewRequest(c.method, "https://"+apiHost+c.path, strings.NewReader(c.body))
		r.Header.Add("Authorization", "Bearer "+apiToken)
		r.Header.Add("Authorization", "Bearer "+apiToken)
		want(t, a.serve(r), 401, "invalid_token")
	}
	// Each asked of the service as the API's resource, the token as sent.
	if len(*asked) == 0 || slices.ContainsFunc(*asked, func(s string) bool { return !strings.HasPrefix(s, "api ") }) {
		t.Fatalf("asked %v", *asked)
	}
	if slices.ContainsFunc(a.fake.Calls(), func(s string) bool { return s != "Check" }) {
		t.Fatalf("calls %v", a.fake.Calls())
	}
	// The scheme in any case.
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/connection", nil)
	r.Header.Set("Authorization", "bearer "+apiToken)
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		return &contracts.VendorConnection{}, nil
	}
	want(t, a.serve(r), 200, "")
	// The storage failing: 503, never 401.
	a.fake.CheckFunc = func(context.Context, contracts.Resource, string) (*oauth.Grant, error) {
		return nil, connections.ErrUnavailable
	}
	for _, c := range vendorRoutes {
		rec := a.vendor(apiToken, c.method, c.path, c.body)
		want(t, rec, 503, "unavailable")
		if rec.Header().Get("Retry-After") != "5" || rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("%v", rec.Header())
		}
	}
	// Methods, paths and a browser's preflight.
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/connection", 405}, {"GET", "/v1/messages/search", 405}, {"POST", "/v1/messages/mbx_1.18e7", 405},
		{"DELETE", "/v1/messages/mbx_1.18e7/attachments/0", 405}, {"GET", "/v1/messages/", 404}, {"GET", "/v1/messages/a/b", 404},
	} {
		if rec := a.vendor(apiToken, c.method, c.path, ""); rec.Code != c.status || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s %s: %d %v", c.method, c.path, rec.Code, rec.Header())
		}
	}
	pre := a.serve(httptest.NewRequest(http.MethodOptions, "https://"+apiHost+"/v1/messages/search", nil))
	if h := pre.Header(); pre.Code != 204 || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
		h.Get("Access-Control-Allow-Credentials") != "" || h.Get("Access-Control-Expose-Headers") != "WWW-Authenticate, Retry-After" {
		t.Fatalf("preflight: %d %v", pre.Code, h)
	}
}

// V45: the four routes answer the contract, each search, read and
// attachment under its lease, started once and finished once with sent.
func TestV45TheAPIsRoutesAnswerTheContract(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	subject, text := "Hello", "the text"
	msg := contracts.VendorMessage{ID: "mbx_1.18e7", ReceivedAt: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC),
		From: []contracts.MailAddress{{Address: "ana@example.org"}}, Subject: &subject, Text: &text}
	conn := contracts.VendorConnection{Parts: apiGrant.Parts, Since: contracts.MailSince{New: true}, EndsAt: time.Date(2027, 2, 14, 0, 0, 0, 0, time.UTC),
		Delivery: contracts.DeliveryOn}
	var searched contracts.VendorSearch
	var read struct {
		id    string
		index int
	}
	leases := map[string]*countedLease{"search": newLease(), "read": newLease(), "attachment": newLease()}
	a.fake.InfoFunc = func(_ context.Context, g *oauth.Grant) (*contracts.VendorConnection, error) {
		if g != apiGrant {
			t.Fatalf("info was given %v", g)
		}
		return &conn, nil
	}
	a.fake.SearchFunc = func(_ context.Context, _ *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		searched = in
		return &contracts.VendorPage{Messages: []contracts.VendorMessage{msg}, Cursor: "pmk_c"}, leases["search"], nil
	}
	a.fake.ReadFunc = func(_ context.Context, _ *oauth.Grant, id string) (*contracts.VendorMessage, connections.Release, error) {
		read.id = id
		return &msg, leases["read"], nil
	}
	data := []byte("%PDF-1.7 not a document a browser renders")
	a.fake.AttachmentFunc = func(_ context.Context, _ *oauth.Grant, id string, index int) (*inbox.Attachment, connections.Release, error) {
		read.id, read.index = id, index
		return &inbox.Attachment{Name: "a.pdf", Type: "application/pdf", Data: data}, leases["attachment"], nil
	}
	answer := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		h := rec.Header()
		if rec.Code != 200 || h.Get("Cache-Control") != "no-store" || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Set-Cookie") != "" ||
			h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%d %v %s", rec.Code, h, rec.Body)
		}
		if v != nil {
			want, _ := json.Marshal(v)
			if strings.TrimSpace(rec.Body.String()) != string(want) || h.Get("Content-Type") != "application/json" {
				t.Fatalf("%s, want %s", rec.Body, want)
			}
		}
	}
	answer(a.vendor(apiToken, "GET", "/v1/connection", ""), conn)
	formula := `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"invoice"},{"var":"from_domain","op":"is","value":"example.org"}]}]}`
	answer(a.vendor(apiToken, "POST", "/v1/messages/search", `{"query":`+formula+`,"limit":20,"cursor":"pmk_b"}`),
		contracts.VendorPage{Messages: []contracts.VendorMessage{msg}, Cursor: "pmk_c"})
	got, _ := json.Marshal(searched.Query)
	if string(got) != formula || searched.Limit != 20 || searched.Cursor != "pmk_b" {
		t.Fatalf("search was given %s %+v", got, searched)
	}
	leases["search"].finished(t, true)
	answer(a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7", ""), msg)
	if read.id != "mbx_1.18e7" {
		t.Fatalf("read was given %q", read.id)
	}
	leases["read"].finished(t, true)
	rec := a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/3", "")
	answer(rec, nil)
	if h := rec.Header(); rec.Body.String() != string(data) || h.Get("Content-Type") != "application/octet-stream" ||
		h.Get("Content-Disposition") != "attachment" || h.Get("Content-Length") != fmt.Sprint(len(data)) {
		t.Fatalf("%v %q", h, rec.Body)
	}
	if read.id != "mbx_1.18e7" || read.index != 3 {
		t.Fatalf("attachment was given %+v", read)
	}
	leases["attachment"].finished(t, true)
	for _, l := range leases {
		if started, finished, sent, tooLate := l.State(); !started || !finished || !sent || tooLate != 0 {
			t.Fatalf("lease %v %v %v %d", started, finished, sent, tooLate)
		}
	}
	// A search without a query, or with null, asks for everything.
	for _, body := range []string{`{}`, `{"query":null}`} {
		a.fake.SearchFunc = func(_ context.Context, _ *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
			searched = in
			return &contracts.VendorPage{Messages: []contracts.VendorMessage{}}, newLease(), nil
		}
		answer(a.vendor(apiToken, "POST", "/v1/messages/search", body), contracts.VendorPage{Messages: []contracts.VendorMessage{}})
		if searched.Query != nil || searched.Limit != 0 || searched.Cursor != "" {
			t.Fatalf("%s: %+v", body, searched)
		}
	}
}

// V45: a search's body is read strictly: a formula refused says where in it
// as sent and why, before the service is asked; anything else that is not
// the body is invalid_body. The service's own refusals of a search (a
// condition on a part not approved among them) keep their details.
func TestV45ASearchIsReadStrictly(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	for body, code := range map[string]string{
		`{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"nope","value":"x"}]}]}}`: "invalid_formula",
		`{"query":{"v":2,"all":[]}}`: "invalid_formula",
		`{"query":"subject:x"}`:      "invalid_formula",
		`{"limit":"ten"}`:            "invalid_body",
		`{"cursor":3}`:               "invalid_body",
		`{"other":1}`:                "invalid_body",
		`{"limit":1,"limit":2}`:      "invalid_body",
		`[]`:                         "invalid_body",
		`{"query":` + strings.Repeat(" ", contracts.MaxFormulaBytes+4<<10) + `{}}`: "invalid_body",
	} {
		want(t, a.vendor(apiToken, "POST", "/v1/messages/search", body), 400, code)
	}
	var e httpapi.APIError
	rec := a.vendor(apiToken, "POST", "/v1/messages/search", `{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"nope","value":"x"}]}]}}`)
	if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Details.At != "all[0].any[0].op" || strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("%s", rec.Body)
	}
	if slices.Contains(a.fake.Calls(), "Search") {
		t.Fatal("the service was asked")
	}
	a.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		return nil, nil, &query.Problem{FormulaProblem: contracts.FormulaProblem{At: "all[0].any[0].var", Reason: contracts.FormulaPartNotApproved}}
	}
	rec = a.vendor(apiToken, "POST", "/v1/messages/search", `{"query":{"v":1,"all":[{"any":[{"var":"body","op":"contains","value":"x"}]}]}}`)
	want(t, rec, 400, "invalid_formula")
	if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Details.Reason != contracts.FormulaPartNotApproved || e.Error.Details.At != "all[0].any[0].var" {
		t.Fatalf("%s", rec.Body)
	}
}

// V45: the service's refusals: not found alike, the cursor, the limit,
// too many requests, the model unavailable (503 with Retry-After, the
// connection itself then saying model_unavailable), an attachment not
// approved (403 with the step-up challenge naming every scope then needed,
// before the service is asked), too large (413), a token revoked meanwhile
// (401), and a failure of the service's own (500, logged by its kind
// alone). A lease answered with a refusal is finished, unsent.
func TestV45TheAPIsRefusals(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	var refusal error
	var lease *countedLease
	a.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		return nil, lease, refusal
	}
	a.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		return nil, nil, refusal
	}
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return nil, nil, refusal
	}
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) { return nil, refusal }
	for _, c := range []struct {
		err          error
		status       int
		code, header string
	}{
		{contracts.ErrNotFound, 404, "not_found", ""},
		{contracts.ErrInvalidCursor, 400, "invalid_cursor", ""},
		{contracts.ErrInvalidLimit, 400, "invalid_limit", ""},
		{contracts.ErrTooManyRequests, 429, "too_many_requests", ""},
		{contracts.ErrModelUnavailable, 503, "model_unavailable", "Retry-After: 60"},
		{fmt.Errorf("x: %w", contracts.ErrModelUnavailable), 503, "model_unavailable", "Retry-After: 60"},
		{contracts.ErrAttachmentTooLarge, 413, "attachment_too_large", ""},
		{connections.ErrInvalidToken, 401, "invalid_token", `WWW-Authenticate: Bearer error="invalid_token", ` + challengeNone[len("Bearer "):]},
		{connections.ErrTooLate, 503, "unavailable", "Retry-After: 5"},
		{connections.ErrUnavailable, 503, "unavailable", "Retry-After: 5"},
		{errors.New("con_secret fam_secret pma_secret https://vendor.example/c"), 500, "internal", ""},
		{context.DeadlineExceeded, 500, "internal", ""},
	} {
		refusal = c.err
		for _, r := range vendorRoutes {
			lease = newLease()
			rec := a.vendor(apiToken, r.method, r.path, r.body)
			want(t, rec, c.status, c.code)
			if name, value, ok := strings.Cut(c.header, ": "); ok && rec.Header().Get(name) != value {
				t.Fatalf("%s %v: %v", r.path, c.err, rec.Header())
			}
			if r.path == "/v1/messages/search" {
				lease.finished(t, false)
				if started, _, _, _ := lease.State(); started {
					t.Fatal("a refused answer's lease was started")
				}
			}
		}
	}
	if out := a.logs.String(); strings.Count(out, "vendor api: internal error") != 8 || !strings.Contains(out, `"kind":"deadline"`) ||
		strings.Contains(out, "secret") || strings.Contains(out, "vendor.example") {
		t.Fatalf("log: %s", out)
	}
	// While the model cannot be asked, the connection says so.
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		return &contracts.VendorConnection{Delivery: contracts.DeliveryModelUnavailable}, nil
	}
	var info contracts.VendorConnection
	if rec := a.vendor(apiToken, "GET", "/v1/connection", ""); json.Unmarshal(rec.Body.Bytes(), &info) != nil || info.Delivery != contracts.DeliveryModelUnavailable {
		t.Fatalf("%s", rec.Body)
	}
	// An attachment's index is a number as written; any other is not found.
	refusal = nil
	before := len(a.fake.Calls())
	for _, index := range []string{"-1", "01", "+1", "x", "99999999999999999999"} {
		want(t, a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/"+index, ""), 404, "not_found")
	}
	if calls := a.fake.Calls()[before:]; slices.Contains(calls, "Attachment") {
		t.Fatalf("calls %v", calls)
	}
}

// V45: an attachment to a grant without mail.attachments is the service's
// to refuse (it records it): 403 part_not_approved, with the step-up
// challenge naming the connection's scopes and mail.attachments in one; an
// attachment past the bound is 413 and its lease finished unsent.
func TestV45AnAttachmentNeedsItsScope(t *testing.T) {
	a := newConnApp(t)
	narrow := *apiGrant
	narrow.Parts = []contracts.Part{contracts.PartSender, contracts.PartBody}
	narrow.Scopes = []string{oauth.ScopeSender, oauth.ScopeBody}
	narrow.ConnectionScopes = narrow.Scopes
	grants(a.fake, &narrow)
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return nil, nil, contracts.ErrPartNotApproved
	}
	const step = `Bearer error="insufficient_scope", resource_metadata="https://api.permaily.test/.well-known/oauth-protected-resource/v1", ` +
		`scope="mail.sender mail.body mail.attachments"`
	rec := a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", "")
	want(t, rec, 403, "part_not_approved")
	if rec.Header().Get("WWW-Authenticate") != step {
		t.Fatalf("%v", rec.Header())
	}
	if !slices.Contains(a.fake.Calls(), "Attachment") {
		t.Fatal("the service was not asked")
	}
	grants(a.fake, apiGrant)
	rec = a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", "")
	want(t, rec, 403, "part_not_approved")
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `scope="mail.sender mail.subject mail.body mail.attachments"`) {
		t.Fatalf("%v", rec.Header())
	}
	lease := newLease()
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: make([]byte, contracts.VendorAttachmentMax+1)}, lease, nil
	}
	want(t, a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", ""), 413, "attachment_too_large")
	lease.finished(t, false)
	// The largest is given whole.
	lease = newLease()
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: make([]byte, contracts.VendorAttachmentMax)}, lease, nil
	}
	if rec := a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", ""); rec.Code != 200 || rec.Body.Len() != contracts.VendorAttachmentMax {
		t.Fatalf("%d %d", rec.Code, rec.Body.Len())
	}
	lease.finished(t, true)
}

// V45 (N2): a lease cut after the service returned and before the first
// byte (a revocation in this enclave), or that refuses to start (the
// authority read again), gives a 503 and nothing of the answer; a request
// whose client left before its first byte is finished without starting.
func TestV45ALeaseCutBeforeTheFirstByte(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	const secret = "the subject of a message"
	subject := secret
	var lease *countedLease
	prepare := func() connections.Release {
		lease = newLease()
		lease.Cut() // revoked after the service returned
		return lease
	}
	a.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		return &contracts.VendorPage{Messages: []contracts.VendorMessage{{ID: "mbx_1.1", Subject: &subject}}}, prepare(), nil
	}
	a.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		return &contracts.VendorMessage{ID: "mbx_1.1", Subject: &subject}, prepare(), nil
	}
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: []byte(secret)}, prepare(), nil
	}
	for _, r := range vendorRoutes[1:] {
		rec := a.vendor(apiToken, r.method, r.path, r.body)
		want(t, rec, 503, "unavailable")
		if strings.Contains(rec.Body.String(), secret) || rec.Header().Get("Retry-After") != "5" || rec.Header().Get("Content-Disposition") != "" {
			t.Fatalf("%s: %v %s", r.path, rec.Header(), rec.Body)
		}
		lease.finished(t, false)
		if started, _, _, tooLate := lease.State(); started || tooLate != 1 {
			t.Fatalf("%s: started %v, %d too late", r.path, started, tooLate)
		}
	}
	// Refused to start.
	prepare = func() connections.Release {
		lease = newLease()
		lease.TooLate = true
		return lease
	}
	rec := a.vendor(apiToken, "GET", "/v1/messages/mbx_1.1", "")
	want(t, rec, 503, "unavailable")
	lease.finished(t, false)
	// The client left before the first byte: nothing started.
	ctx, cancel := context.WithCancel(context.Background())
	prepare = func() connections.Release {
		cancel()
		lease = newLease()
		return lease
	}
	r := httptest.NewRequestWithContext(ctx, "GET", "https://"+apiHost+"/v1/messages/mbx_1.1", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	if rec := a.serve(r); strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("%s", rec.Body)
	}
	lease.finished(t, false)
	if started, _, _, _ := lease.State(); started {
		t.Fatal("started for a client that left")
	}
}

// leaseWriter is the server's ResponseWriter as the handler sees it, counting
// the writes that started, and that returned without error, after the cut;
// ResponseController reaches the server's own through Unwrap.
type leaseWriter struct {
	http.ResponseWriter
	cut      *atomic.Bool
	writing  atomic.Int64 // when the write in progress started (UnixNano), 0 when none
	after    atomic.Int32 // writes that returned without error after the cut
	returned chan struct{}
}

func (w *leaseWriter) Write(b []byte) (int, error) {
	w.writing.Store(time.Now().UnixNano())
	n, err := w.ResponseWriter.Write(b)
	w.writing.Store(0)
	if err == nil && w.cut.Load() {
		w.after.Add(1)
	}
	return n, err
}

func (w *leaseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// V45 (N2, the owner's decision 14): a lease cut during a transfer whose
// client reads nothing, so the server's write is blocked: the write
// returns, the response is aborted, no write succeeds after the cut, the
// client gets less than the whole, and the lease is finished once, unsent.
func TestV45ALeaseCutDuringABlockedTransfer(t *testing.T) {
	fake := &connectionstest.Fake{}
	grants(fake, apiGrant)
	lease := newLease()
	data := make([]byte, contracts.VendorAttachmentMax)
	fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: data}, lease, nil
	}
	api := httpapi.VendorAPI(httpapi.ConnectionsConfig{
		Service: fake, OAuth: testOAuth, Challenge: testChallenge, Log: slog.New(slog.DiscardHandler),
	})
	var cut atomic.Bool
	w := &leaseWriter{cut: &cut, returned: make(chan struct{})}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer close(w.returned)
		w.ResponseWriter = rw
		api.ServeHTTP(w, r)
	}))
	srv.Listener = smallBuffers{srv.Listener}
	srv.Config.ErrorLog = nil
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET /v1/messages/mbx_1.1/attachments/0 HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", apiHost, apiToken)
	// The client reads nothing until the server's write has been blocked
	// for a while.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if started := w.writing.Load(); started != 0 && time.Since(time.Unix(0, started)) > 200*time.Millisecond {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server's write never blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cut.Store(true)
	lease.Cut()
	select {
	case <-w.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked write did not return")
	}
	if n := w.after.Load(); n != 0 {
		t.Fatalf("%d writes after the cut", n)
	}
	lease.finished(t, false)
	if started, _, sent, _ := lease.State(); !started || sent {
		t.Fatalf("started %v, sent %v", started, sent)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	if n >= int64(len(data)) {
		t.Fatalf("the client got all %d bytes", n)
	}
}

// smallBuffers makes the server's sockets' send buffers small, so a client
// that reads nothing blocks the server's write soon.
type smallBuffers struct{ net.Listener }

func (l smallBuffers) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(4096)
	}
	return c, err
}

// V46: the consent preview, the only answer that holds a MatchPage, is
// answered by POST /api/v1/oauth/requests/{id}/matches alone, in the
// person's session from the app: every route of every host of connections,
// by GET and by POST, with a session, with a vendor's bearer token and with
// neither, asks the service's Preview from that one request, and no vendor
// route answers a page of matches.
func TestV46NoVendorRouteAnswersAMatchPage(t *testing.T) {
	a := newConnApp(t)
	answerEverything(a.fake)
	grants(a.fake, apiGrant)
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		return &contracts.VendorConnection{}, nil
	}
	a.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		return &contracts.VendorPage{Messages: []contracts.VendorMessage{}}, newLease(), nil
	}
	a.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		return &contracts.VendorMessage{ID: "mbx_1.1"}, newLease(), nil
	}
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: []byte("x")}, newLease(), nil
	}
	a.fake.TokenFunc = func(context.Context, url.Values) (*connections.TokenAnswer, error) {
		return &connections.TokenAnswer{}, nil
	}
	a.fake.RevokeTokenFunc = func(context.Context, url.Values) error { return nil }
	a.fake.PushFunc = func(context.Context, url.Values) (string, error) { return "urn:x", nil }
	a.fake.PreviewFunc = func(context.Context, connections.Person, string, contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error) {
		return &contracts.ConsentPreviewPage{DraftID: "qry_1", Page: contracts.MatchPage{Messages: []contracts.MessageSummary{}}}, nil
	}
	paths := []string{oauth.PathAuthorize, oauth.PathServerMetadata, oauth.PathResourceMetadata, oauth.PathResourceMetadata + oauth.PathAPI,
		oauth.PathResourceMetadata + oauth.PathMCP, oauth.PathToken, oauth.PathRevoke, oauth.PathPushed}
	for _, r := range append(slices.Clone(consentRoutes), vendorRoutes...) {
		paths = append(paths, r.path)
	}
	const matches = "/api/v1/oauth/requests/pmq_1/matches"
	body := `{"draft":{"mailboxes":["mbx_1"],"since":{"new":true}},"mailbox":"mbx_1"}`
	type ask struct{ host, method, path, auth string }
	var reached []ask
	for _, path := range paths {
		for _, host := range []string{appHost, apiHost, mcpHost} {
			for _, method := range []string{"GET", "POST"} {
				for _, auth := range []string{"none", "session", "bearer"} {
					r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					switch auth {
					case "session":
						r.Header.Set("Origin", "https://"+host)
						r.Header.Set("Sec-Fetch-Site", "same-origin")
						r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
					case "bearer":
						r.Header.Set("Authorization", "Bearer "+apiToken)
					}
					before := strings.Count(strings.Join(a.fake.Calls(), " "), "Preview")
					rec := a.serve(r)
					if strings.Count(strings.Join(a.fake.Calls(), " "), "Preview") != before {
						reached = append(reached, ask{host, method, path, auth})
					}
					if strings.Contains(rec.Body.String(), `"page"`) && (host != appHost || path != matches) {
						t.Errorf("%s %s%s (%s) answered a page: %s", method, host, path, auth, rec.Body)
					}
				}
			}
		}
	}
	if !slices.Equal(reached, []ask{{appHost, "POST", matches, "session"}}) {
		t.Fatalf("the preview was asked by %v", reached)
	}
}

// endedLease starts with a context that has ended already: the authority
// was found ended between Start and the first byte.
type endedLease struct{ *countedLease }

func (l endedLease) Start(ctx context.Context) (context.Context, error) {
	c, cancel := context.WithCancel(ctx)
	cancel()
	return c, nil
}

// cutOnWrite is a ResponseWriter whose first Write cuts the lease, as a
// revocation while that chunk is sent.
type cutOnWrite struct {
	*httptest.ResponseRecorder
	lease  *countedLease
	writes int
}

func (w *cutOnWrite) Write(b []byte) (int, error) {
	w.writes++
	n, err := w.ResponseRecorder.Write(b)
	w.lease.Cut()
	return n, err
}

// abortedBy runs h and says it aborted the response (http.ErrAbortHandler).
func abortedBy(h http.Handler, w http.ResponseWriter, r *http.Request) (aborted bool) {
	defer func() {
		err, _ := recover().(error)
		aborted = errors.Is(err, http.ErrAbortHandler)
	}()
	h.ServeHTTP(w, r)
	return false
}

// V45 (N2, review 1): a lease whose context has ended by the first byte
// writes nothing, not even the header; one cut while a chunk is sent writes
// no chunk after it and does not flush that one; both abort the response and finish the lease once,
// unsent.
func TestV45ALeaseEndedWritesNothingMore(t *testing.T) {
	fake := &connectionstest.Fake{}
	grants(fake, apiGrant)
	var lease connections.Release
	fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: make([]byte, 100<<10)}, lease, nil
	}
	api := httpapi.VendorAPI(httpapi.ConnectionsConfig{Service: fake, OAuth: testOAuth, Challenge: testChallenge, Log: slog.New(slog.DiscardHandler)})
	request := func() *http.Request {
		r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/messages/mbx_1.1/attachments/0", nil)
		r.Header.Set("Authorization", "Bearer "+apiToken)
		return r
	}
	ended := endedLease{newLease()}
	lease = ended
	rec := httptest.NewRecorder()
	if !abortedBy(api, rec, request()) || rec.Body.Len() != 0 || rec.Flushed {
		t.Fatalf("aborted with %d bytes, flushed %v", rec.Body.Len(), rec.Flushed)
	}
	ended.finished(t, false)
	cut := newLease()
	lease = cut
	w := &cutOnWrite{ResponseRecorder: httptest.NewRecorder(), lease: cut}
	if !abortedBy(api, w, request()) || w.writes != 1 || w.Body.Len() != 32<<10 || w.Flushed {
		t.Fatalf("%d writes, %d bytes, flushed %v", w.writes, w.Body.Len(), w.Flushed)
	}
	cut.finished(t, false)
}
