package contracts

import "time"

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
	// header's addresses and names, and the time Permaily received it. Every
	// connection has it.
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
	// identifier; the sentence names them.
	Mailboxes []string `json:"mailboxes"`
	// Rules are the sentences of the rules that apply to every connection:
	// today the default rule (ADR 0031).
	Rules []RuleSummary `json:"rules"`
	// Replaces is the connection this vendor's client has now, which an
	// approval ends; absent when it has none. Narrower says the request asks
	// for no more than that connection's scope (query.Contains), so the
	// screen says it asks for less or the same, never more.
	Replaces *Connection `json:"replaces,omitempty"`
	Narrower bool        `json:"narrower,omitempty"`
	// ModelNotice is the version of the model's notice the screen shows: an
	// approval agrees to it for this connection (ADR 0030, "Allowing").
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

// ConsentPreview asks for a page of what a request would read in one of the
// account's mailboxes: its formulas evaluated together (the vendor's query,
// the mailboxes, the mail since), newest first, as MatchRequest pages do.
// With AskModel the model is asked about the messages that need it, the
// default rule included, under the account's agreement to the model's notice
// (ErrModelNoticeNeeded without it); the answer is a MatchPage whose Held
// lists what a rule holds back.
type ConsentPreview struct {
	Mailbox  string `json:"mailbox"`
	After    string `json:"after,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	AskModel bool   `json:"ask_model,omitempty"`
}

// AuthorizationDecision is the body of POST
// /api/v1/oauth/requests/{id}/decision. Allow needs a fresh proof (ADR 0022);
// Deny needs none. For a request with a query, Since may only narrow what was
// asked (new mail only when a date was asked); for one without a query the
// person chooses it (new mail only when absent) and the Mailboxes (all of
// them when absent), which must be the account's.
type AuthorizationDecision struct {
	Allow     bool       `json:"allow"`
	Since     *MailSince `json:"since,omitempty"`
	Mailboxes []string   `json:"mailboxes,omitempty"`
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
}

// VendorSearch is a search of the connection's mail: Query, evaluated
// together with the connection's own formulas and never merged with them
// (absent: everything the connection reads), at most Limit messages (1 to
// 50; 20 when 0), after Cursor (the cursor of the page before; "" for the
// newest).
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
	// ErrConnectionEnded refuses renewing a connection that ended.
	ErrConnectionEnded = &Error{"connection_ended", "that connection ended"}
	// ErrPartNotApproved refuses an attachment to a connection without
	// PartAttachments (the API's answer; MCP steps up instead).
	ErrPartNotApproved = &Error{"part_not_approved", "this connection does not read attachments"}
	// ErrAttachmentTooLarge refuses an attachment past VendorAttachmentMax.
	ErrAttachmentTooLarge = &Error{"attachment_too_large", "that attachment is larger than a connection can read"}
)

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
