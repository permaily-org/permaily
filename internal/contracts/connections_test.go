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
			Draft:  ConsentDraft{Mailboxes: []string{"mbx_a"}, Since: MailSince{New: true}},
			Rules:  []RuleSummary{{ID: "default-sign-in-and-security", Version: 1, Sentence: []Sentence{}}},
			Change: ChangeNew, Snapshot: ConsentSnapshot{Change: ChangeNew, EndsAt: at, DraftID: "qry_d"}, ModelNotice: "2026-11-15", ExpiresAt: at,
		}}, `{"request":{"id":"pmq_x","vendor":{"domain":"claude.ai","verified":"Anthropic"},"client_name":"Claude Code","redirect_host":"localhost","loopback":true,"resource":"mcp","parts":["sender","subject","body"],"since":{"new":true},"ends_in_days":90,"mailboxes":["mbx_a"],"draft":{"mailboxes":["mbx_a"],"since":{"new":true}},"rules":[{"id":"default-sign-in-and-security","version":1,"sentence":[]}],"change":"new","snapshot":{"change":"new","ends_at":"2026-11-16T10:00:00Z","draft_id":"qry_d"},"model_notice":"2026-11-15","expires_at":"2026-11-16T10:00:00Z"}}`},
		{"an unknown vendor", Vendor{Domain: "trip-app.com"}, `{"domain":"trip-app.com"}`},
		{"mail since a date", MailSince{At: at}, `{"new":false,"at":"2026-11-16T10:00:00Z"}`},
		{"a preview of the last year", ConsentPreview{Draft: ConsentDraft{Mailboxes: []string{"mbx_a", "mbx_b"}, Since: MailSince{At: at}}, DraftID: "qry_d", Mailbox: "mbx_a", After: "m1", Limit: 50, AskModel: true},
			`{"draft":{"mailboxes":["mbx_a","mbx_b"],"since":{"new":false,"at":"2026-11-16T10:00:00Z"}},"draft_id":"qry_d","mailbox":"mbx_a","after":"m1","limit":50,"ask_model":true}`},
		{"a preview's answer", ConsentPreviewPage{DraftID: "qry_d", Change: ChangeAddDevice, Snapshot: ConsentSnapshot{Connection: "con_x", Version: 7, Change: ChangeAddDevice, EndsAt: at, DraftID: "qry_d"}, Page: MatchPage{Messages: []MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}}},
			`{"draft_id":"qry_d","change":"add_device","snapshot":{"connection":"con_x","version":7,"change":"add_device","ends_at":"2026-11-16T10:00:00Z","draft_id":"qry_d"},"page":{"messages":[],"needs_model":[],"unknown":[]}}`},
		{"a page with held mail", MatchPage{Messages: []MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}, Held: []string{"m1"}, Model: MatchModelAsked},
			`{"messages":[],"needs_model":[],"unknown":[],"held":["m1"],"model":"asked"}`},
		{"an approval", AuthorizationDecision{Allow: true, Draft: &ConsentDraft{Mailboxes: []string{"mbx_a"}, Since: MailSince{New: true}}, ModelNotice: "2026-11-15", Snapshot: &ConsentSnapshot{Connection: "con_x", Version: 7, Change: ChangeAddDevice, EndsAt: at, DraftID: "qry_d"}},
			`{"allow":true,"draft":{"mailboxes":["mbx_a"],"since":{"new":true}},"model_notice":"2026-11-15","snapshot":{"connection":"con_x","version":7,"change":"add_device","ends_at":"2026-11-16T10:00:00Z","draft_id":"qry_d"}}`},
		{"a renewal", ConnectionRenewal{ModelNotice: "2026-11-15", Version: 7}, `{"model_notice":"2026-11-15","version":7}`},
		{"a refusal", AuthorizationDecision{}, `{"allow":false}`},
		{"the way back", AuthorizationAnswer{Redirect: "http://localhost:58047/callback?code=c&state=s&iss=i"},
			`{"redirect":"http://localhost:58047/callback?code=c\u0026state=s\u0026iss=i"}`},
		{"a connection that ended", Connection{
			ID: "con_x", Vendor: Vendor{Domain: "chatgpt.com", Verified: "OpenAI"}, ClientID: "https://chatgpt.com/oauth/codex/client.json",
			ClientName: "Codex", Resource: ResourceMCP, State: ConnectionEnded, Parts: []Part{PartSender},
			Mailboxes: []string{"mbx_a"}, Since: MailSince{New: true, At: at}, MadeAt: at, EndsAt: at,
			EndedAt: at, EndReason: EndedByReplaced, ReplacedBy: "con_y",
			Devices:     []ConnectionDevice{{ID: "fam_a", MadeAt: at, State: ConnectionEnded, Parts: []Part{PartSender}, Narrower: true, EndedAt: at, EndReason: EndedByVendor}},
			ModelNotice: "2026-11-15", Delivery: DeliveryNoticeChanged, Version: 9,
		}, `{"id":"con_x","vendor":{"domain":"chatgpt.com","verified":"OpenAI"},"client_id":"https://chatgpt.com/oauth/codex/client.json","client_name":"Codex","resource":"mcp","state":"ended","parts":["sender"],"mailboxes":["mbx_a"],"since":{"new":true,"at":"2026-11-16T10:00:00Z"},"made_at":"2026-11-16T10:00:00Z","ends_at":"2026-11-16T10:00:00Z","ended_at":"2026-11-16T10:00:00Z","end_reason":"replaced","replaced_by":"con_y","devices":[{"id":"fam_a","made_at":"2026-11-16T10:00:00Z","state":"ended","parts":["sender"],"narrower":true,"ended_at":"2026-11-16T10:00:00Z","end_reason":"vendor"}],"model_notice":"2026-11-15","delivery":"model_notice_changed","version":9}`},
		{"no connection", ConnectionList{Connections: []Connection{}}, `{"connections":[]}`},
		{"what the vendor is told", VendorConnection{Parts: []Part{PartSender, PartAttachments}, Since: MailSince{At: at}, EndsAt: at, Query: &all, Delivery: DeliveryModelUnavailable},
			`{"parts":["sender","attachments"],"since":{"new":false,"at":"2026-11-16T10:00:00Z"},"ends_at":"2026-11-16T10:00:00Z","query":{"v":1,"all":[]},"delivery":"model_unavailable"}`},
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
	for _, e := range []*Error{ErrInvalidClient, ErrVendorUnreachable, ErrRequestEnded, ErrConnectionEnded, ErrPartNotApproved, ErrAttachmentTooLarge, ErrModelUnavailable, ErrModelNoticeChanged, ErrTooManyDevices, ErrTooManyConnections, ErrConsentChanged, ErrRequestTooLarge} {
		if !errors.Is(&Error{e.Code, "other words"}, e) {
			t.Errorf("%s is not its code", e.Code)
		}
	}
}

// Finding 4: a vendor's search may look only at what its parts give; the
// variables no part gives are never searchable by a vendor.
func TestWhatASearchConditionLooksAt(t *testing.T) {
	want := map[Variable][]Part{
		VarFromAddress: {PartSender}, VarFromDomain: {PartSender}, VarReceived: {PartSender},
		VarSubject: {PartSubject}, VarBody: {PartBody}, VarMessage: {PartSubject, PartBody},
		VarAttachments: {PartAttachments}, VarAttachmentName: {PartAttachments}, VarAttachmentType: {PartAttachments},
	}
	for _, v := range []Variable{VarFromAddress, VarFromDomain, VarRecipientAddress, VarRecipientDomain, VarSignedBy, VarSubject, VarBody, VarMessage, VarAttachments, VarAttachmentName, VarAttachmentType, VarList, VarReceived, VarSize, VarMailbox} {
		got, ok := ConditionParts(v, OpContains)
		w, searchable := want[v]
		if ok != searchable || len(got) != len(w) {
			t.Errorf("%s: %v %v, want %v", v, got, ok, w)
			continue
		}
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%s: %v, want %v", v, got, w)
			}
		}
	}
}
