package mcpapi_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// refusals is a world whose Check hands out one grant per token, kept, and
// whose Refuse records which grant each refusal came with and answers err.
type refusals struct {
	*world
	mu     sync.Mutex
	grants map[string]*oauth.Grant
	given  []*oauth.Grant
	err    error
}

func newRefusals() *refusals {
	r := &refusals{world: newWorld(), grants: map[string]*oauth.Grant{}}
	check := r.fake.CheckFunc
	r.fake.CheckFunc = func(ctx context.Context, res contracts.Resource, bearer string) (*oauth.Grant, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if g, ok := r.grants[bearer]; ok {
			return g, nil
		}
		g, err := check(ctx, res, bearer)
		if err == nil {
			r.grants[bearer] = g
		}
		return g, err
	}
	r.fake.RefuseFunc = func(_ context.Context, g *oauth.Grant, _ connections.Refusal) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.given = append(r.given, g)
		return r.err
	}
	return r
}

// one checks that exactly want was refused, with the very grant Check
// returned for token, and nothing of the mail was asked for.
func (r *refusals) one(t *testing.T, token string, want connections.Refusal) {
	t.Helper()
	got := r.fake.Refusals()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("refusals %+v, want exactly %+v", got, want)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.given) != 1 || r.given[0] != r.grants[token] || r.given[0] == nil {
		t.Fatal("Refuse was not given the grant Check returned")
	}
	for _, m := range []string{"Search", "Read", "Attachment"} {
		if slices.Contains(r.fake.Calls(), m) {
			t.Fatalf("the service was asked %s", m)
		}
	}
}

// argumentRefusals are mail tool calls whose arguments the SDK's schema or
// the tool's own reading refuses, and the refusal each records: a fixed
// reason, never an identifier the request named.
var argumentRefusals = []struct {
	name, token, tool, args string
	want                    connections.Refusal
}{
	// read_message and get_attachment: the SDK checks their schema before
	// the tool runs.
	{"read_message with a numeric id", tokenMCP, "read_message", `{"id":123}`, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}},
	{"read_message without an id", tokenMCP, "read_message", `{}`, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}},
	{"read_message with an unknown argument", tokenMCP, "read_message", `{"id":"mbx_A.msg_1","sender-supplied":1}`, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}},
	{"read_message with arguments not an object", tokenMCP, "read_message", `["mbx_A.msg_1"]`, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}},
	{"get_attachment with an index as text", tokenMCPAttached, "get_attachment", `{"id":"mbx_A.msg_1","index":"0"}`, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedInvalidBody}},
	{"get_attachment with a fractional index", tokenMCPAttached, "get_attachment", `{"id":"mbx_A.msg_1","index":0.5}`, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedInvalidBody}},
	{"get_attachment without an index", tokenMCPAttached, "get_attachment", `{"id":"mbx_A.msg_1"}`, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedInvalidBody}},
	{"get_attachment with a numeric id", tokenMCPAttached, "get_attachment", `{"id":7,"index":0}`, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedInvalidBody}},
	// search_mail: the tool reads its arguments itself (searchArgs).
	{"search_mail with a limit as text", tokenMCP, "search_mail", `{"limit":"sender-supplied"}`, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
	{"search_mail with a cursor as a number", tokenMCP, "search_mail", `{"cursor":42}`, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
	{"search_mail with an unknown argument", tokenMCP, "search_mail", `{"sender-supplied":1}`, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
	{"search_mail with arguments not an object", tokenMCP, "search_mail", `[1]`, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
	{"search_mail with a formula the parser refuses", tokenMCP, "search_mail", `{"query":{"v":1,"all":[{"any":[{"var":"secret_variable","op":"is","value":"x"}]}]}}`, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedFormula}},
}

func argumentCall(token, tool, args string) request {
	q := toolCall(1, tool, args)
	q.token = token
	return q
}

// ADR 0030 ("Seams added while building", API.Refuse) and ADR 0031: a mail
// tool call whose arguments are refused, by the SDK's schema or by the
// tool's own reading, after Check returned a grant, records exactly one
// refusal, with that grant, a fixed reason and no message identifier,
// before it is answered; the service is asked for no mail.
func TestArgumentsRefusedAreRecordedOnce(t *testing.T) {
	for _, c := range argumentRefusals {
		t.Run(c.name, func(t *testing.T) {
			r := newRefusals()
			res := callTool(t, r.handler(), argumentCall(c.token, c.tool, c.args))
			if !res.IsError || len(res.StructuredContent) != 0 {
				t.Fatalf("not refused: %+v", res)
			}
			r.one(t, c.token, c.want)
		})
	}
}

// A refusal that cannot be recorded is answered 503 with Retry-After and
// the generic words, as the API answers it: never the refusal itself, never
// a challenge that would make a client drop its tokens.
func TestArgumentsRefusedWhenTheRecordFailsAre503(t *testing.T) {
	for _, c := range argumentRefusals {
		t.Run(c.name, func(t *testing.T) {
			r := newRefusals()
			r.err = errors.New("the receipt was not written")
			rec := do(r.handler(), argumentCall(c.token, c.tool, c.args))
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || rec.Header().Get("WWW-Authenticate") != "" ||
				!strings.Contains(rec.Body.String(), `"temporarily_unavailable"`) || strings.Contains(rec.Body.String(), "sender-supplied") {
				t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
			}
			if n := len(r.fake.Refusals()); n != 1 {
				t.Fatalf("%d refusals", n)
			}
		})
	}
}

// The step-up of get_attachment, refused before the SDK runs, records its
// refusal (part_not_given, MCP's alone) before its 403; when that cannot be
// recorded the call is 503 instead.
func TestTheStepUpIsRecordedOnce(t *testing.T) {
	r := newRefusals()
	rec := do(r.handler(), attachmentCall(tokenMCP))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	r.one(t, tokenMCP, connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedPartNotGiven})

	r = newRefusals()
	r.err = errors.New("the receipt was not written")
	rec = do(r.handler(), attachmentCall(tokenMCP))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if n := len(r.fake.Refusals()); n != 1 {
		t.Fatalf("%d refusals", n)
	}
}

// What the service refuses it records itself: the adapter never turns a
// service's error into a Refuse. Nor does it record a call that was
// answered, nor one naming no mail operation (connection_info, an unknown
// tool), whose arguments may be refused all the same.
func TestNoRefusalIsRecordedForTheServicesOwnOrForNoMailOperation(t *testing.T) {
	r := newRefusals()
	r.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		return nil, nil, contracts.ErrNotFound
	}
	r.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		return nil, nil, contracts.ErrInvalidLimit
	}
	r.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return nil, nil, contracts.ErrPartNotApproved
	}
	h := r.handler()
	for _, q := range []request{
		argumentCall(tokenMCP, "read_message", `{"id":"mbx_A.msg_unknown"}`),
		argumentCall(tokenMCP, "search_mail", `{"limit":500}`),
		argumentCall(tokenMCPAttached, "get_attachment", `{"id":"mbx_A.msg_1","index":9}`),
		argumentCall(tokenMCP, "connection_info", `{"sender-supplied":1}`),
		argumentCall(tokenMCP, "delete_everything", `{"id":123}`),
	} {
		_ = do(h, q)
	}
	if got := r.fake.Refusals(); len(got) != 0 {
		t.Fatalf("refused %+v", got)
	}
	w := newRefusals()
	h = w.handler()
	for _, q := range []request{
		argumentCall(tokenMCP, "read_message", `{"id":"mbx_A.msg_1"}`),
		argumentCall(tokenMCP, "search_mail", `{"limit":5}`),
		argumentCall(tokenMCPAttached, "get_attachment", `{"id":"mbx_A.msg_1","index":0}`),
	} {
		if res := callTool(t, h, q); res.IsError {
			t.Fatalf("%+v", res)
		}
	}
	if got := w.fake.Refusals(); len(got) != 0 {
		t.Fatalf("refused %+v", got)
	}
}
