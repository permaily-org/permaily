package contracts

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The web app reads these names (web/src/lib/api.ts), and vendors read the
// last ones through the API and MCP: a change here is a change of ADR 0030.
func TestConnectionShapes(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	subject, text := "Your booking", "Hello"
	all := Formula{V: FormulaVersion, All: []Group{}}
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"a start", AuthorizationStart{Query: "response_type=code&client_id=x"}, `{"query":"response_type=code\u0026client_id=x"}`},
		{"refused after the redirect was proven", AuthorizationStarted{Redirect: "https://claude.ai/api/mcp/auth_callback?error=invalid_scope&iss=x"},
			`{"redirect":"https://claude.ai/api/mcp/auth_callback?error=invalid_scope\u0026iss=x"}`},
		{"a request without a query", AuthorizationStarted{Request: &AuthorizationRequest{
			ID: "pmq_x", Vendor: Vendor{Domain: "claude.ai", Verified: "Anthropic"}, ClientName: "Claude Code",
			RedirectHost: "localhost", Loopback: true, Resource: ResourceMCP, Parts: []Part{PartSender, PartSubject, PartBody},
			Since: MailSince{New: true}, EndsInDays: 90, Mailboxes: []string{"mbx_a"},
			Rules:       []RuleSummary{{ID: "default-sign-in-and-security", Version: 1, Sentence: []Sentence{}}},
			ModelNotice: "2026-11-15", ExpiresAt: at,
		}}, `{"request":{"id":"pmq_x","vendor":{"domain":"claude.ai","verified":"Anthropic"},"client_name":"Claude Code","redirect_host":"localhost","loopback":true,"resource":"mcp","parts":["sender","subject","body"],"since":{"new":true},"ends_in_days":90,"mailboxes":["mbx_a"],"rules":[{"id":"default-sign-in-and-security","version":1,"sentence":[]}],"model_notice":"2026-11-15","expires_at":"2026-11-16T10:00:00Z"}}`},
		{"an unknown vendor", Vendor{Domain: "trip-app.com"}, `{"domain":"trip-app.com"}`},
		{"mail since a date", MailSince{At: at}, `{"new":false,"at":"2026-11-16T10:00:00Z"}`},
		{"a preview", ConsentPreview{Mailbox: "mbx_a", Limit: 50, AskModel: true}, `{"mailbox":"mbx_a","limit":50,"ask_model":true}`},
		{"a page with held mail", MatchPage{Messages: []MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}, Held: []string{"m1"}, Model: MatchModelAsked},
			`{"messages":[],"needs_model":[],"unknown":[],"held":["m1"],"model":"asked"}`},
		{"an approval", AuthorizationDecision{Allow: true, Since: &MailSince{New: true}, Mailboxes: []string{"mbx_a"}},
			`{"allow":true,"since":{"new":true},"mailboxes":["mbx_a"]}`},
		{"a refusal", AuthorizationDecision{}, `{"allow":false}`},
		{"the way back", AuthorizationAnswer{Redirect: "http://localhost:58047/callback?code=c&state=s&iss=i"},
			`{"redirect":"http://localhost:58047/callback?code=c\u0026state=s\u0026iss=i"}`},
		{"a connection that ended", Connection{
			ID: "con_x", Vendor: Vendor{Domain: "chatgpt.com", Verified: "OpenAI"}, ClientID: "https://chatgpt.com/oauth/codex/client.json",
			ClientName: "Codex", Resource: ResourceMCP, State: ConnectionEnded, Parts: []Part{PartSender},
			Mailboxes: []string{"mbx_a"}, Since: MailSince{New: true, At: at}, MadeAt: at, EndsAt: at,
			EndedAt: at, EndReason: EndedByReplaced, ReplacedBy: "con_y",
		}, `{"id":"con_x","vendor":{"domain":"chatgpt.com","verified":"OpenAI"},"client_id":"https://chatgpt.com/oauth/codex/client.json","client_name":"Codex","resource":"mcp","state":"ended","parts":["sender"],"mailboxes":["mbx_a"],"since":{"new":true,"at":"2026-11-16T10:00:00Z"},"made_at":"2026-11-16T10:00:00Z","ends_at":"2026-11-16T10:00:00Z","ended_at":"2026-11-16T10:00:00Z","end_reason":"replaced","replaced_by":"con_y"}`},
		{"no connection", ConnectionList{Connections: []Connection{}}, `{"connections":[]}`},
		{"what the vendor is told", VendorConnection{Parts: []Part{PartSender, PartAttachments}, Since: MailSince{At: at}, EndsAt: at, Query: &all},
			`{"parts":["sender","attachments"],"since":{"new":false,"at":"2026-11-16T10:00:00Z"},"ends_at":"2026-11-16T10:00:00Z","query":{"v":1,"all":[]}}`},
		{"a search", VendorSearch{Query: &all, Limit: 20, Cursor: "pmk_x"}, `{"query":{"v":1,"all":[]},"limit":20,"cursor":"pmk_x"}`},
		{"everything, newest first", VendorSearch{}, `{}`},
		{"a page that left", VendorPage{Messages: []VendorMessage{{
			ID: "mbx_a.m1", ReceivedAt: at, From: []MailAddress{{Address: "a@example.org"}}, Subject: &subject,
			Attachments: []VendorAttachment{{Index: 0, Name: "a.pdf", Type: "application/pdf", Size: 10}},
		}}, Cursor: "pmk_y"}, `{"messages":[{"id":"mbx_a.m1","received_at":"2026-11-16T10:00:00Z","from":[{"address":"a@example.org"}],"subject":"Your booking","attachments":[{"index":0,"name":"a.pdf","type":"application/pdf","size":10}]}],"cursor":"pmk_y"}`},
		{"a read with the text", VendorMessage{ID: "mbx_a.m1", ReceivedAt: at, From: []MailAddress{}, Text: &text, TextCut: true},
			`{"id":"mbx_a.m1","received_at":"2026-11-16T10:00:00Z","from":[],"text":"Hello","text_cut":true}`},
		{"the last page", VendorPage{Messages: []VendorMessage{}}, `{"messages":[]}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestConnectionErrorsAreTheirCodes(t *testing.T) {
	for _, e := range []*Error{ErrInvalidClient, ErrVendorUnreachable, ErrRequestEnded, ErrConnectionEnded, ErrPartNotApproved, ErrAttachmentTooLarge} {
		if !errors.Is(&Error{e.Code, "other words"}, e) {
			t.Errorf("%s is not its code", e.Code)
		}
	}
}
