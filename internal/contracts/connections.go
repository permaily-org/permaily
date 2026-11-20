package contracts

import (
	"encoding/json"
	"errors"
	"time"
)

// Connections (ADR 0019, how in ADR 0030): what a vendor asks for, what the
// person sees and decides on the consent screen, the connection that results,
// and what the vendor reads through the API and MCP. The web app reads the
// first three in the person's session; vendors read the last with a token.

// Part is a part of a message a connection may read (ADR 0019, "Read only, by
// parts"). Every connection reads PartSender; the others are asked for one by
// one, as OAuth scopes (ADR 0030, "Scopes are the parts").
type Part string

const (
	// PartSender is who sent the message and when it arrived: the From
	// header's addresses and their display names, and the time Permaily
	// received it. Every connection has it.
	PartSender Part = "sender"
	// PartSubject is the subject as stored.
	PartSubject Part = "subject"
	// PartBody is the message's text as a connection receives it: at most
	// 256 KiB of it, never its HTML.
	PartBody Part = "body"
	// PartAttachments is the names, types and sizes of its attachments, and
	// their contents. Off unless asked for and approved.
	PartAttachments Part = "attachments"
)

// Resource is what a connection's tokens are for (RFC 8707): the API for apps,
// or MCP for agents. A token for one is refused by the other.
type Resource string

const (
	ResourceAPI Resource = "api"
	ResourceMCP Resource = "mcp"
)

// Vendor is who asks, as the person is shown it: the host of its client
// metadata document's URL (its client_id), and, when that host is on the list
// compiled into the image, who it belongs to. Nothing else of it is
// believed.
type Vendor struct {
	// Domain is the host of the client_id URL, in lower case, as an A-label.
	Domain string `json:"domain"`
	// Verified is the owner the image's list names for Domain ("Anthropic",
	// "OpenAI"); absent for every other domain.
	Verified string `json:"verified,omitempty"`
}

// MailSince says from when a connection reads mail: new mail only (from the
// moment it was approved), mail received since a date, or, with neither New
// nor At, all the mail Permaily keeps (only by the person's choice, for a
// request without a query).
type MailSince struct {
	// New is true for new mail only; At is then when the connection was
	// approved (in an answer about a connection) or absent (in a request).
	New bool      `json:"new"`
	At  time.Time `json:"at,omitzero"`
}

// AuthorizationStart is the body of POST /api/v1/oauth/requests: the query
// string the vendor sent the browser to https://app.permaily.com/oauth/authorize
// with, exactly as the address bar had it after "?", which the web app takes
// out of the address bar before it renders anything.
type AuthorizationStart struct {
	Query string `json:"query"`
}

// AuthorizationStarted is what the app is answered: a request to show
// (Request), or, when the vendor's request was refused after its redirect URI
// was proven, where to send the browser back to the vendor with the error
// (Redirect). Exactly one is set. A request refused before its redirect URI
// is proven is an error answer instead (ErrInvalidClient,
// ErrVendorUnreachable), shown by the app and never redirected.
type AuthorizationStarted struct {
	Request  *AuthorizationRequest `json:"request,omitempty"`
	Redirect string                `json:"redirect,omitempty"`
}

// ConsentDraft is the scope the person is about to approve (ADR 0030, "The
// consent screen", finding 13): the mailboxes, by identifier, and the mail
// since. The screen sends the whole draft with every preview and with the
// decision, and the server validates it each time against the request and
// the account; the preview and the decision describe exactly the draft sent.
// Mailboxes is never empty and never means "all": it lists every mailbox
// chosen, each the account's (an empty or absent list is invalid_body). For
// a request with a query, Since may only narrow what was asked (new mail only
// when a date was asked) and Mailboxes must be the request's. New mail only
// means mail received at or after the instant of the approval, so its
// preview is always empty.
type ConsentDraft struct {
	Mailboxes []string  `json:"mailboxes"`
	Since     MailSince `json:"since"`
}

// ConnectionChange is what approving a request does to the client's
// connection (the owner's decisions 7 and 10).
type ConnectionChange string

const (
	// ChangeNew: the client has no active connection; approving makes one.
	ChangeNew ConnectionChange = "new"
	// ChangeAddDevice: the client has one, and the draft's scope is the
	// same as, or fits inside, the connection's (the resource the same, the
	// parts a subset, query.Contains true for every formula): approving
	// adds this request as another device of that connection, with the
	// draft's scope as its own, and its other devices' tokens keep working.
	// The connection's end does not change.
	ChangeAddDevice ConnectionChange = "add_device"
	// ChangeReplace: the client has one and the draft asks for anything
	// else (another resource, a part more, a formula that is not
	// contained): approving replaces it, and its tokens stop working;
	// denying leaves it as it was.
	ChangeReplace ConnectionChange = "replace"
)

// AuthorizationRequest is what the consent screen shows of a vendor's request
// (ADR 0030, "The consent screen").
type AuthorizationRequest struct {
	ID     string `json:"id"`
	Vendor Vendor `json:"vendor"`
	// ClientName is what the vendor's document calls it ("Claude Code"):
	// the vendor's own claim, shown as such.
	ClientName string `json:"client_name"`
	// RedirectHost is where the browser goes once the person decides: the
	// host of the redirect URI, without a port. Loopback says it is the
	// person's own computer (localhost, 127.0.0.1 or [::1]): then any
	// program on it can use this vendor's name.
	RedirectHost string   `json:"redirect_host"`
	Loopback     bool     `json:"loopback"`
	Resource     Resource `json:"resource"`
	Parts        []Part   `json:"parts"`
	// Query is the vendor's formula, checked, with its sentence; absent for
	// a request without one (every MCP client today): the person then
	// chooses the mailboxes, and the connection reads all their mail but
	// what rules hold.
	Query *FormulaCheck `json:"query,omitempty"`
	// Since is what the vendor asked: new mail only unless it asked for a
	// date (only through a pushed request).
	Since MailSince `json:"since"`
	// EndsInDays is how long the connection lasts once approved: 90 unless
	// the vendor asked for fewer; 365 at most.
	EndsInDays int `json:"ends_in_days"`
	// Mailboxes are the account's mailboxes the request can reach, by
	// identifier: what the picker offers; the sentence names them.
	Mailboxes []string `json:"mailboxes"`
	// Draft is where the screen starts: every mailbox and new mail only,
	// or, when the client has a connection, that connection's mailboxes and
	// mail since, so asking again never quietly narrows or widens it.
	Draft ConsentDraft `json:"draft"`
	// Rules are the sentences of the rules that apply to every connection:
	// today the default rule (ADR 0031).
	Rules []RuleSummary `json:"rules"`
	// Connection is the client's active connection, absent when it has
	// none; Change is what approving the starting draft does to it (each
	// preview answers it again for the draft it was given).
	Connection *Connection      `json:"connection,omitempty"`
	Change     ConnectionChange `json:"change"`
	// Snapshot is what the starting draft's approval would do, as the
	// screen shows it; each preview answers it again for its draft, and the
	// approval sends back the one the person saw (N5).
	Snapshot ConsentSnapshot `json:"snapshot"`
	// ModelNotice is the version of the model's notice the screen shows: an
	// approval sends it back, and is refused (ErrModelNoticeChanged) when
	// another is in force by then (finding 18).
	ModelNotice string    `json:"model_notice"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// RuleSummary is a rule as the consent screen and a connection's page show
// it: which rule, and its sentence.
type RuleSummary struct {
	ID       string     `json:"id"`
	Version  int        `json:"version"`
	Sentence []Sentence `json:"sentence"`
}

// ConsentPreview asks for a page of what a request would read, under Draft,
// in Mailbox (one of Draft's): its formulas evaluated together (the
// vendor's query, the draft's mailboxes and mail since), newest first, as
// MatchRequest pages do. After continues a page of the same draft: DraftID
// must then be the one the page before answered, else ErrInvalidCursor (a
// changed draft starts again from the newest). With AskModel the model is
// asked about the messages that need it, the default rule included, through
// the same discloser a connection reads with, under the account's agreement
// to the model's notice (ErrModelNoticeNeeded without it).
type ConsentPreview struct {
	Draft    ConsentDraft `json:"draft"`
	DraftID  string       `json:"draft_id,omitempty"`
	Mailbox  string       `json:"mailbox"`
	After    string       `json:"after,omitempty"`
	Limit    int          `json:"limit,omitempty"`
	AskModel bool         `json:"ask_model,omitempty"`
}

// ConsentPreviewPage is a preview's answer: the draft's identifier (the
// canonical identifier of its formulas together with the request's), what
// approving that draft does to the client's connection, as a snapshot the
// approval sends back, and the page, whose Held lists what a rule holds
// back.
type ConsentPreviewPage struct {
	DraftID  string           `json:"draft_id"`
	Change   ConnectionChange `json:"change"`
	Snapshot ConsentSnapshot  `json:"snapshot"`
	Page     MatchPage        `json:"page"`
}

// ConsentSnapshot is what the screen showed approving would do (ADR 0030,
// "Asking again"; the second design review's N5): the client's connection
// it would change, at which version of its record, the change and the end
// shown, for the draft shown. An approval sends it back; the decision is
// committed only when, worked out again at the commit, every field is still
// the same, else ErrConsentChanged and nothing is committed: the screen
// loads the request again and shows what approving now does, for a new
// confirmation. So a stale screen never adds a device where it promised
// one, replaces where it promised to add, or approves an end it did not
// show.
type ConsentSnapshot struct {
	// Connection is the client's active connection when it was shown, ""
	// for none (ChangeNew); Version is its record's version then.
	Connection string `json:"connection,omitempty"`
	Version    uint64 `json:"version,omitempty"`
	// Change is what approving does, and EndsAt the end the connection
	// would have: the connection's own for ChangeAddDevice, the new one's
	// otherwise (from the approval's instant, at most a minute later than
	// shown: a later end is a change).
	Change  ConnectionChange `json:"change"`
	EndsAt  time.Time        `json:"ends_at"`
	DraftID string           `json:"draft_id"`
}

// AuthorizationDecision is the body of POST
// /api/v1/oauth/requests/{id}/decision. Allow needs a fresh proof (ADR 0022),
// checked again when the decision is committed, the Draft the person
// approves and the ModelNotice the screen showed; Deny needs none of them.
// What it does to the client's connection is Change for that draft, worked
// out again as it is committed and refused (ErrConsentChanged) when it is
// not the Snapshot the screen showed.
type AuthorizationDecision struct {
	Allow       bool          `json:"allow"`
	Draft       *ConsentDraft `json:"draft,omitempty"`
	ModelNotice string        `json:"model_notice,omitempty"`
	// Snapshot is the one the screen showed for Draft, required to allow
	// (N5).
	Snapshot *ConsentSnapshot `json:"snapshot,omitempty"`
}

// ConnectionRenewal is the body of POST /api/v1/connections/{id}/renew: the
// model's notice the screen showed (ErrModelNoticeChanged when another is
// in force). Renewing needs a fresh proof, checked again when committed,
// and works only while the connection is active and before its end: past
// it, the connection has ended, whether or not the hourly pass wrote it so
// (ErrConnectionEnded).
//
// Version is the connection's record's version the screen showed: a
// renewal of a connection changed since (replaced, a device added or
// revoked, renewed by another tab) is refused (ErrConsentChanged), and the
// page shows it again (N5).
type ConnectionRenewal struct {
	ModelNotice string `json:"model_notice"`
	Version     uint64 `json:"version"`
}

// AuthorizationAnswer is where the app sends the browser back to the vendor:
// the redirect URI with the code (or access_denied), the state and the issuer
// (RFC 9207). The app navigates to it and to nothing else.
type AuthorizationAnswer struct {
	Redirect string `json:"redirect"`
}

// ConnectionState is where a connection is in its life.
type ConnectionState string

const (
	ConnectionActive ConnectionState = "active"
	ConnectionEnded  ConnectionState = "ended"
)

// EndReason is why a connection ended.
type EndReason string

const (
	EndedByPerson   EndReason = "person"   // revoked in the app
	EndedByVendor   EndReason = "vendor"   // the vendor revoked one of its tokens (RFC 7009)
	EndedByReuse    EndReason = "reuse"    // a refresh token or a code was used again after its grace
	EndedByReplaced EndReason = "replaced" // the person approved a new request of the same client
	EndedByAccount  EndReason = "account"  // the account is being deleted
	EndedByTime     EndReason = "expired"  // it reached its end without being renewed
)

// ConnectionDelivery says whether a connection gets mail now.
type ConnectionDelivery string

const (
	DeliveryOn ConnectionDelivery = "delivering"
	// DeliveryModelUnavailable: the model cannot be asked (no key, a spend
	// limit reached): nothing leaves until it can (the owner's decision 9).
	DeliveryModelUnavailable ConnectionDelivery = "model_unavailable"
	// DeliveryNoticeChanged: the model's notice changed since the person
	// approved the connection: nothing leaves until they renew it, agreeing
	// to the one in force (finding 18). Vendors are told
	// DeliveryModelUnavailable.
	DeliveryNoticeChanged ConnectionDelivery = "model_notice_changed"
)

// ConnectionDevice is one device of a connection: the tokens one approval
// started (a token family, the owner's decision 10). Its scope is the one
// the person confirmed for it, the same as the connection's or narrower.
type ConnectionDevice struct {
	ID     string          `json:"id"`
	MadeAt time.Time       `json:"made_at"`
	State  ConnectionState `json:"state"`
	Parts  []Part          `json:"parts"`
	// Narrower says its scope is narrower than the connection's: its own
	// mailboxes, mail since or query, which its reads also evaluate.
	Narrower  bool      `json:"narrower,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	EndReason EndReason `json:"end_reason,omitempty"`
}

// Connection is a connection as the person's app shows it.
type Connection struct {
	ID         string          `json:"id"`
	Vendor     Vendor          `json:"vendor"`
	ClientID   string          `json:"client_id"`
	ClientName string          `json:"client_name"`
	Resource   Resource        `json:"resource"`
	State      ConnectionState `json:"state"`
	Parts      []Part          `json:"parts"`
	// Query is the consented formula and its sentence; absent for a
	// connection that reads all mail of its mailboxes.
	Query     *FormulaCheck `json:"query,omitempty"`
	Mailboxes []string      `json:"mailboxes"`
	Since     MailSince     `json:"since"`
	MadeAt    time.Time     `json:"made_at"`
	EndsAt    time.Time     `json:"ends_at"`
	EndedAt   time.Time     `json:"ended_at,omitzero"`
	EndReason EndReason     `json:"end_reason,omitempty"`
	// ReplacedBy is the connection that replaced it, for EndedByReplaced.
	ReplacedBy string `json:"replaced_by,omitempty"`
	// Devices are its devices, active and ended in the last 30 days.
	Devices []ConnectionDevice `json:"devices"`
	// ModelNotice is the version of the model's notice the person agreed
	// to for it; Delivery says whether it gets mail now.
	ModelNotice string             `json:"model_notice"`
	Delivery    ConnectionDelivery `json:"delivery"`
	// Version is its record's version, which a renewal sends back.
	Version uint64 `json:"version"`
}

// ConnectionList is the account's connections: the active ones, and the ones
// that ended in the last 30 days.
type ConnectionList struct {
	Connections []Connection `json:"connections"`
}

// What the vendor reads, with a token (ADR 0030, "The API for apps", "MCP for
// agents"). The same shapes are the API's answers and the MCP tools'
// structured results.

// VendorConnection is what a vendor is told of its own connection: what it
// may read, from when and until when, and its query. Nothing of the
// account, its mailboxes or its rules: only that rules may hold mail back
// without saying which.
type VendorConnection struct {
	Parts  []Part    `json:"parts"`
	Since  MailSince `json:"since"`
	EndsAt time.Time `json:"ends_at"`
	// Query is the canonical form of the query the person consented to;
	// absent when it reads all mail of the mailboxes the person chose.
	Query *Formula `json:"query,omitempty"`
	// Delivery is DeliveryOn, or DeliveryModelUnavailable while nothing
	// leaves because the model cannot be asked (the owner's decision 9);
	// never why, and never how much mail is waiting.
	Delivery ConnectionDelivery `json:"delivery"`
}

// VendorSearch is a search of the connection's mail: Query, evaluated
// together with the connection's own formulas and never merged with them
// (absent: everything the connection reads), at most Limit messages (1 to
// 50; 20 when 0), after Cursor (the cursor of the page before; "" for the
// newest). Query may only look at what the request's effective parts give
// (ConditionParts, finding 4): a condition on anything else is refused
// before anything is evaluated, ErrInvalidFormula with
// FormulaPartNotApproved at its path.
type VendorSearch struct {
	Query  *Formula `json:"query,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
}

// VendorPage is a page of what a search delivered, newest first. It names
// only messages that left: true on every formula, held by no rule. Cursor is
// opaque, sealed and bound to the connection and the search; absent when
// nothing older can match. A page may hold fewer messages than asked, or
// none, and still have a cursor.
type VendorPage struct {
	Messages []VendorMessage `json:"messages"`
	Cursor   string          `json:"cursor,omitempty"`
}

// VendorMessage is one message as it left, with the parts the connection
// reads and no other: a field of a part not approved is absent. ID is the
// same for the same message in every page and read of this connection.
type VendorMessage struct {
	ID         string        `json:"id"`
	ReceivedAt time.Time     `json:"received_at"`
	From       []MailAddress `json:"from"`
	// Subject is present with PartSubject.
	Subject *string `json:"subject,omitempty"`
	// Text is present with PartBody, in a read only; TextCut says it was cut
	// at 256 KiB.
	Text    *string `json:"text,omitempty"`
	TextCut bool    `json:"text_cut,omitempty"`
	// Attachments are present with PartAttachments.
	Attachments []VendorAttachment `json:"attachments,omitempty"`
}

// VendorAttachment is one attachment of a message as it left.
type VendorAttachment struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int    `json:"size"`
}

// Errors of connections, besides the common ones (ErrNotFound for a message a
// connection does not read, whether it exists or not; ErrTooManyRequests;
// ErrConfirmationNeeded for an approval or a renewal without a fresh proof).
var (
	// ErrInvalidClient refuses a vendor's request whose client_id, document
	// or redirect URI cannot be used; shown, never redirected (ADR 0030,
	// "The authorization request"). Its details hold a fixed reason.
	ErrInvalidClient = &Error{"invalid_client", "that app's request can't be used"}
	// ErrVendorUnreachable is a vendor's document that could not be fetched.
	ErrVendorUnreachable = &Error{"vendor_unreachable", "that app's details could not be read; try again later"}
	// ErrRequestEnded is an authorization request that ended, was decided
	// already, or is not this session's.
	ErrRequestEnded = &Error{"request_ended", "that request ended; start again from the app"}
	// ErrConnectionEnded refuses renewing a connection that ended or is past
	// its end, and revoking a device of one.
	ErrConnectionEnded = &Error{"connection_ended", "that connection ended"}
	// ErrPartNotApproved refuses an attachment to a connection without
	// PartAttachments (the API's answer; MCP steps up instead).
	ErrPartNotApproved = &Error{"part_not_approved", "this connection does not read attachments"}
	// ErrAttachmentTooLarge refuses an attachment past VendorAttachmentMax.
	ErrAttachmentTooLarge = &Error{"attachment_too_large", "that attachment is larger than a connection can read"}
	// ErrModelUnavailable answers a vendor's search, read or attachment
	// while the model cannot be asked for its connection (no key, a spend
	// limit reached, a notice the person has not agreed to): nothing leaves
	// until it can (the owner's decision 9). 503 with Retry-After; MCP's
	// tool error says the same.
	ErrModelUnavailable = &Error{"model_unavailable", "Permaily cannot ask its model now, so no mail leaves until it can; try again later"}
	// ErrModelNoticeChanged refuses an approval or a renewal made on a
	// screen that showed a notice no longer in force: the screen starts
	// again with the one in force (finding 18).
	ErrModelNoticeChanged = &Error{"model_notice_changed", "what Permaily sends to its model changed; read it again before you allow"}
	// ErrTooManyDevices refuses adding a device to a connection with
	// oauth.FamiliesMax active ones (the owner's decision 13): the person
	// removes one on the connection's page first; none is ever removed
	// for them.
	ErrTooManyDevices = &Error{"too_many_devices", "that connection is on 16 devices, as many as it can be; remove one you no longer use on its page, then allow again"}
	// ErrTooManyConnections refuses approving a new connection for an
	// account with as many active ones as it may have (ADR 0030, C2).
	ErrTooManyConnections = &Error{"too_many_connections", "you have as many connections as an account can; revoke one you no longer use first"}
	// ErrConsentChanged refuses an approval or a renewal whose screen no
	// longer says what it would do (N5): the client's connection, its
	// version, the change or the end changed since it was shown. The
	// screen loads it again.
	ErrConsentChanged = &Error{"consent_changed", "what allowing does changed since this screen was shown; read it again"}
	// ErrRequestTooLarge refuses a request whose connection could not be
	// kept within its record's bound with every device it may have (N6);
	// shown before anything is approved, never redirected.
	ErrRequestTooLarge = &Error{"request_too_large", "that app asks for more than a connection can hold"}
)

// ConditionParts is the parts a vendor's search condition looks at (finding
// 4): a search may hold a condition only when the request's effective parts
// include every one of them. The variables no part gives (recipients, DKIM
// signatures, lists, sizes, mailboxes) are never searchable by a vendor: ok
// is false. The consented query is not checked here: the person read its
// sentence and agreed to it.
func ConditionParts(v Variable, op Operator) (parts []Part, ok bool) {
	switch v {
	case VarFromAddress, VarFromDomain, VarReceived:
		return []Part{PartSender}, true
	case VarSubject:
		return []Part{PartSubject}, true
	case VarBody:
		return []Part{PartBody}, true
	case VarMessage:
		return []Part{PartSubject, PartBody}, true
	case VarAttachments, VarAttachmentName, VarAttachmentType:
		return []Part{PartAttachments}, true
	}
	return nil, false
}

// VendorAttachmentMax bounds an attachment a connection reads, through the
// API and MCP alike.
const VendorAttachmentMax = 10 << 20

// ClientProblem is the reason in the details of ErrInvalidClient, one of a
// fixed set: never a value of the vendor's request or document.
type ClientProblem string

const (
	ClientIDInvalid       ClientProblem = "client_id_invalid"       // not a URL the rules of ADR 0030 accept
	ClientDocumentInvalid ClientProblem = "client_document_invalid" // not a document those rules accept
	RedirectURIInvalid    ClientProblem = "redirect_uri_invalid"    // not one of the document's
)

// Valid says whether p is one of the three reasons, the only values a
// ClientProblem may hold anywhere it is shown, sent or logged.
func (p ClientProblem) Valid() bool {
	switch p {
	case ClientIDInvalid, ClientDocumentInvalid, RedirectURIInvalid:
		return true
	}
	return false
}

// ClientError is ErrInvalidClient with its fixed reason (PR #131's review,
// finding 5; ADR 0030, "Seams added while building"): what the connections
// service answers Start (or Push) with for a client_id, a document or a
// redirect URI it cannot use. errors.Is(err, ErrInvalidClient) holds, and
// errors.As to *Error finds ErrInvalidClient's code and words; the routes
// write Details() as the error's details, and the web app reads the reason.
// Problem is one of the three constants and never the vendor's text, URL
// or anything of its request: an unknown one is neither in the text nor
// written as details. The issuer's own refusals (token, revocation, pushed
// requests) stay OAuth codes alone (connections.OAuthError).
type ClientError struct {
	Problem ClientProblem
}

// InvalidClient is ErrInvalidClient for reason p.
func InvalidClient(p ClientProblem) *ClientError { return &ClientError{Problem: p} }

func (e *ClientError) Error() string {
	if !e.Problem.Valid() {
		return ErrInvalidClient.Error()
	}
	return ErrInvalidClient.Error() + " (" + string(e.Problem) + ")"
}

// Unwrap makes it ErrInvalidClient to errors.Is and errors.As.
func (e *ClientError) Unwrap() error { return ErrInvalidClient }

// LogKind is what a log says of it (domain.FixedKind): its code and reason,
// fixed words alone.
func (e *ClientError) LogKind() string { return e.Error() }

// Details is the error's details in an API answer.
func (e *ClientError) Details() ErrorDetails { return ErrorDetails{Client: e.Problem} }

// ErrorDetails is the details of an API error (internal/httpapi's
// APIError; web/src/lib/api.ts's ErrorDetails), one of three shapes, or
// none: with invalid_formula, a FormulaProblem, written exactly as it always
// was ({"at","reason"}, "at" always present); with invalid_client, its
// ClientProblem alone ({"reason"}, no "at"); with invalid_rule (ADR 0040), its
// RuleProblem alone ({"rule"}). No two go together, and reading tells them
// apart by "at" and "rule".
type ErrorDetails struct {
	FormulaProblem
	Client ClientProblem
	Rule   RuleProblem
}

// IsZero says there are no details (omitzero).
func (d ErrorDetails) IsZero() bool { return d == ErrorDetails{} }

func (d ErrorDetails) MarshalJSON() ([]byte, error) {
	switch {
	case d.Rule != "" && (d.Client != "" || d.FormulaProblem != (FormulaProblem{})):
		return nil, errors.New("contracts: details of a rule with another's at once")
	case d.Rule != "":
		if !d.Rule.Valid() {
			return nil, errors.New("contracts: an invalid rule's details hold one of its reasons")
		}
		return json.Marshal(struct {
			Rule RuleProblem `json:"rule"`
		}{d.Rule})
	case d.Client != "" && d.FormulaProblem != (FormulaProblem{}):
		return nil, errors.New("contracts: details of a formula and a client at once")
	case d.Client != "":
		if !d.Client.Valid() {
			return nil, errors.New("contracts: an invalid client's details hold one of three reasons")
		}
		return json.Marshal(struct {
			Reason ClientProblem `json:"reason"`
		}{d.Client})
	}
	return json.Marshal(d.FormulaProblem)
}

func (d *ErrorDetails) UnmarshalJSON(b []byte) error {
	var v struct {
		At     *string `json:"at"`
		Reason string  `json:"reason"`
		Rule   *string `json:"rule"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.Rule != nil {
		if v.At != nil || v.Reason != "" || !RuleProblem(*v.Rule).Valid() {
			return errors.New("contracts: an invalid rule's details that are not one of its reasons alone")
		}
		*d = ErrorDetails{Rule: RuleProblem(*v.Rule)}
		return nil
	}
	if v.At != nil {
		*d = ErrorDetails{FormulaProblem: FormulaProblem{At: *v.At, Reason: FormulaReason(v.Reason)}}
		return nil
	}
	if !ClientProblem(v.Reason).Valid() {
		return errors.New("contracts: details that are neither a formula's nor an invalid client's")
	}
	*d = ErrorDetails{Client: ClientProblem(v.Reason)}
	return nil
}
