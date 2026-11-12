package contracts

import "time"

// Mailboxes and the inbox (ADR 0018, how in ADR 0026). What the web app's API
// under /api/v1/mailboxes sends and receives, and what the operator's tool
// and the admin panel are shown of a mailbox: never its address, its label or
// the Gmail account behind it.

// The failures of mailboxes and the inbox. Use errors.Is. A mailbox or a
// message of another account is ErrNotFound, as one that does not exist.
var (
	// ErrTooManyMailboxes refuses a mailbox more than an account may have.
	ErrTooManyMailboxes = &Error{"too_many_mailboxes", "this account has as many mailboxes as one can have; disconnect one first"}
	// ErrInvalidLabel refuses a label that is not one printable line of at
	// most 64 characters.
	ErrInvalidLabel = &Error{"invalid_label", "a label is one line of at most 64 characters"}
	// ErrInvalidRetention refuses a period mail is kept for that is not one
	// of MailboxRetentionDays. The code is the one the F1 mailbox API used.
	ErrInvalidRetention = &Error{"invalid_retention", "keep mail for 7, 30, 90, 180 or 365 days"}
	// ErrInvalidQuery refuses a search that is empty or longer than 200
	// characters.
	ErrInvalidQuery = &Error{"invalid_query", "a search is 1 to 200 characters"}
	// ErrTooManyMessages refuses deleting more messages at once than one
	// request deletes.
	ErrTooManyMessages = &Error{"too_many_messages", "delete at most 100 messages at a time"}
	// ErrMailboxUnreadable refuses opening or changing a mailbox whose record
	// cannot be read: its newest version is gone, or two versions were
	// committed in its place (ADR 0026). It accepts no mail; it can only be
	// disconnected, which deletes what it holds.
	ErrMailboxUnreadable = &Error{"mailbox_unreadable", "this mailbox can't be read; disconnect it, and write to support@permaily.com"}
)

// MailboxRetentionDays are the periods a mailbox keeps mail for, the default
// first among the choices it is shown with (MailboxRetentionDefault).
var MailboxRetentionDays = []int{7, 30, 90, 180, 365}

// MailboxRetentionDefault is how long a new mailbox keeps mail.
const MailboxRetentionDefault = 90

// MailboxState says what a mailbox does with mail.
type MailboxState string

const (
	// MailboxWaiting is a mailbox that has received nothing but Gmail's
	// forwarding confirmations: forwarding is not on yet.
	MailboxWaiting MailboxState = "waiting"
	// MailboxReceiving is a mailbox that has received mail.
	MailboxReceiving MailboxState = "receiving"
	// MailboxPaused is a mailbox the operator paused: it refuses new mail
	// until the operator resumes it. What it holds stays.
	MailboxPaused MailboxState = "paused"
	// MailboxUnreadable is a mailbox whose record cannot be read
	// (ErrMailboxUnreadable): it refuses mail, shows nothing of itself but
	// its identifier and when it was added, and can only be disconnected.
	MailboxUnreadable MailboxState = "unreadable"
)

// Mailbox is what a person sees of one of their mailboxes.
type Mailbox struct {
	ID string `json:"id"`
	// Address is where the person forwards their mail: random, at
	// inbox.permaily.com. Empty for a mailbox that is MailboxUnreadable, as
	// are the label, the retention's choices and every date but CreatedAt
	// (when it was added to the account).
	Address string `json:"address"`
	// Label is the person's own name for the mailbox; empty when they gave
	// none.
	Label     string       `json:"label"`
	State     MailboxState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
	// RetentionDays is how long mail received from now on is kept;
	// RetentionOptions are the choices, MailboxRetentionDays.
	RetentionDays    int   `json:"retention_days"`
	RetentionOptions []int `json:"retention_options"`
	// ReceivingSince is when the first message that was not a forwarding
	// confirmation arrived: nothing from before it is here. Absent while
	// waiting.
	ReceivingSince time.Time `json:"receiving_since,omitzero"`
	// PausedAt is when the operator paused the mailbox; absent otherwise.
	PausedAt time.Time `json:"paused_at,omitzero"`
	// Source is the first Gmail account Google confirmed asked to forward to
	// this mailbox, while it was waiting; absent when none did. Without it
	// the app shows the mailbox as not verified.
	Source *MailboxSource `json:"source,omitempty"`
	// Confirmations are the forwarding confirmations received, the newest
	// first, ten at most, for thirty days each. Always a list.
	Confirmations []ForwardingConfirmation `json:"confirmations"`
}

// MailboxSource is what a provider confirmed in a message it signed: that
// this account asked to forward its mail to the mailbox. Not that forwarding
// is on, and not that later mail comes from it.
type MailboxSource struct {
	// Provider is "gmail", the only one that says so in a signed message.
	Provider string `json:"provider"`
	// Address is the account at the provider that asked to forward here.
	Address    string    `json:"address"`
	VerifiedAt time.Time `json:"verified_at"`
}

// MailboxSourceGmail is the provider of a source vouched for by Google.
const MailboxSourceGmail = "gmail"

// ForwardingConfirmation is a message Gmail sends to a forwarding address
// when an account asks to forward to it, with the code that turns forwarding
// on. Anyone can send a message that looks like one.
type ForwardingConfirmation struct {
	// Code is empty when ForAnotherAddress: it confirms nothing here.
	Code string `json:"code"`
	// Requester is the Gmail account that asked, as the message says.
	Requester  string    `json:"requester,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	// Verified is true only when google.com signed the message (DKIM, over
	// its From, To and Subject and its whole body) and the address it names
	// as where mail is forwarded is this mailbox's: Google confirms that
	// Requester asked to forward to this mailbox.
	Verified bool `json:"verified"`
	// ForAnotherAddress is a message Google signed for another forwarding
	// address, sent here by someone else: it says nothing of this mailbox.
	ForAnotherAddress bool `json:"for_another_address,omitempty"`
	// Message is the message that carried it, in the mailbox's inbox.
	Message string `json:"message,omitempty"`
}

// MailboxList is the mailboxes of an account, the oldest first, and how many
// it may have.
type MailboxList struct {
	Mailboxes []Mailbox `json:"mailboxes"`
	Limit     int       `json:"limit"`
}

// MailboxCreate makes a mailbox. Label may be empty.
type MailboxCreate struct {
	Label string `json:"label"`
}

// MailboxUpdate changes what a person may change of a mailbox. What is
// absent stays as it is. A new period applies to mail received afterwards.
type MailboxUpdate struct {
	Label         *string `json:"label"`
	RetentionDays *int    `json:"retention_days"`
}

// MailAddress is an address of a message, with the name it was given there.
type MailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// MessageSummary is a message in a list.
type MessageSummary struct {
	// ID is time-ordered: a later message has a greater one.
	ID         string        `json:"id"`
	ReceivedAt time.Time     `json:"received_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	Size       int           `json:"size"`
	From       []MailAddress `json:"from"`
	Subject    string        `json:"subject"`
	// Snippet is the start of the message's text, at most 200 characters.
	Snippet string `json:"snippet"`
	// Attachments is how many attachments the message has.
	Attachments int `json:"attachments"`
	// Confirmation says the message is a forwarding confirmation.
	Confirmation bool `json:"confirmation"`
}

// MessagePage is a page of a mailbox's messages, the newest first, or of the
// ones a search found. A page reads a bounded number of stored items (500
// for a list, 2,000 for a search, expired ones included), so it may hold
// fewer messages than were asked for, or none, and still have a Next.
type MessagePage struct {
	Messages []MessageSummary `json:"messages"`
	// Next is what to ask the following page with (after): the identifier of
	// the last item the page read, found or not. Absent when nothing older
	// is left to list or to search.
	Next string `json:"next,omitempty"`
	// SearchedTo is, for a search, when the oldest message this page looked
	// at arrived; absent when it looked at none.
	SearchedTo time.Time `json:"searched_to,omitzero"`
}

// The parts a message's text can come from (Message.TextFrom).
const (
	// TextFromPlain is the message's text/plain part.
	TextFromPlain = "plain"
	// TextFromHTML is text the enclave took out of its text/html part, for
	// a message with no text/plain part. The HTML itself is shown only
	// through the frame (Message.HTML).
	TextFromHTML = "html"
	// TextFromNone is a message with no text to show.
	TextFromNone = "none"
)

// MessageAttachment is an attachment of a message, as the message names it.
// Index is where it is among the message's attachments, from 0: what it is
// downloaded by.
type MessageAttachment struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
}

// AuthCheck is the outcome of one check of who sent a message: a DKIM
// signature, with its domain, and "pass" or "fail: <reason>".
type AuthCheck struct {
	Method string `json:"method"`
	Domain string `json:"domain"`
	Result string `json:"result"`
}

// Message is one message, as the inbox shows it: its text always, and what
// the formatted view needs to know of its HTML.
type Message struct {
	ID         string        `json:"id"`
	ReceivedAt time.Time     `json:"received_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	Size       int           `json:"size"`
	From       []MailAddress `json:"from"`
	To         []MailAddress `json:"to"`
	Cc         []MailAddress `json:"cc"`
	ReplyTo    []MailAddress `json:"reply_to"`
	Subject    string        `json:"subject"`
	// Date is the Date header as the sender wrote it.
	Date string `json:"date"`
	// Text is the message's text, 256 KiB at most (TextTruncated when there
	// was more), from TextFrom.
	Text          string              `json:"text"`
	TextFrom      string              `json:"text_from"`
	TextTruncated bool                `json:"text_truncated"`
	Attachments   []MessageAttachment `json:"attachments"`
	Auth          []AuthCheck         `json:"auth"`
	Confirmation  bool                `json:"confirmation"`
	// Unparsed says the message could not be read as mail: only the raw
	// message can be downloaded.
	Unparsed bool `json:"unparsed"`
	// HTML says whether the message is shown formatted and what was taken
	// out of it. Absent when it has no text/html part (or is unparsed): the
	// app shows the text and no switch.
	HTML *MessageHTML `json:"html,omitempty"`
}

// The states of a message's HTML (MessageHTML.Status).
const (
	// HTMLShown is HTML the frame shows: GET .../messages/{message}/html
	// answers the cleaned document.
	HTMLShown = "shown"
	// HTMLTooLarge is HTML over one of the limits below: the text is shown,
	// and the original can be downloaded.
	HTMLTooLarge = "too_large"
	// HTMLUnreadable is HTML whose charset cannot be decoded, or that the
	// sanitiser could not make into a document that parses back to itself:
	// the text is shown.
	HTMLUnreadable = "unreadable"
)

// The limits of showing a message's HTML (ADR 0026, "Showing a message's
// HTML"). Past the first three the HTML is HTMLTooLarge; past the others
// what is over is left out and the rest is shown.
const (
	// MessageHTMLMaxBytes bounds the HTML part, decoded.
	MessageHTMLMaxBytes = 2 << 20
	// MessageHTMLMaxElements bounds the elements it has.
	MessageHTMLMaxElements = 50_000
	// MessageHTMLMaxDepth bounds how deep they nest.
	MessageHTMLMaxDepth = 256
	// MessageHTMLMaxLinks is how many links are numbered; the rest are text.
	MessageHTMLMaxLinks = 1000
	// MessageInlineImagesMax, MessageInlineImageMaxBytes and
	// MessageInlineImagesMaxBytes bound the images that travel in the
	// message (cid: parts and data: URIs), decoded.
	MessageInlineImagesMax      = 50
	MessageInlineImageMaxBytes  = 2 << 20
	MessageInlineImagesMaxBytes = 5 << 20
)

// MessageHTML is what the app is told of a message's HTML. The HTML itself
// is never in it: the frame loads it from its own route, sanitised on read.
type MessageHTML struct {
	// Status is HTMLShown, HTMLTooLarge or HTMLUnreadable.
	Status string `json:"status"`
	// RemoteImages counts the images the message would have fetched from
	// elsewhere (img, background, CSS url() in image properties), none of
	// which is ever loaded: "3 remote images were not loaded".
	RemoteImages int `json:"remote_images"`
	// RemoteOther counts the other remote references taken out: stylesheets,
	// @import, fonts, frames, objects, media, prefetch hints.
	RemoteOther int `json:"remote_other"`
	// InlineImages counts the images in the message that are shown (PNG,
	// JPEG, GIF, WebP, checked by their bytes); InlineImagesOmitted the ones
	// that are not: SVG, a type that does not match its bytes, or over the
	// limits.
	InlineImages        int `json:"inline_images"`
	InlineImagesOmitted int `json:"inline_images_omitted"`
	// Links counts the links that open the link page, at most
	// MessageHTMLMaxLinks.
	Links int `json:"links"`
}

// MessageLink is where a link of a message's HTML leads, for the page the
// app shows before the person follows it (GET .../links/{n}).
type MessageLink struct {
	// URL is the link's address, http or https, as the server parsed and
	// wrote it again.
	URL string `json:"url"`
	// Host is its host in ASCII: a host with other characters is shown in
	// its xn-- form, so a look-alike is seen.
	Host string `json:"host"`
	// Shown is the host the link's text named when it named another one
	// (the text says one address and the link leads to another); absent
	// otherwise.
	Shown string `json:"shown,omitempty"`
}

// MessagesDelete deletes messages of a mailbox, 100 at most.
type MessagesDelete struct {
	IDs []string `json:"ids"`
}

// MessagesDeleted says how many of them were there and are deleted.
type MessagesDeleted struct {
	Deleted int `json:"deleted"`
}

// MailboxRecord is what the operator's tool and the admin panel show of a
// mailbox: what the enclave vouches for, read from its signed record (the
// retention from its signed settings). Never its address, its label or the
// account at the provider behind it. State is the one the person sees,
// MailboxUnreadable included (then only ID, Account and CreatedAt are set).
type MailboxRecord struct {
	ID             string       `json:"id"`
	Account        string       `json:"account"`
	State          MailboxState `json:"state"`
	CreatedAt      time.Time    `json:"created_at"`
	RetentionDays  int          `json:"retention_days"`
	ReceivingSince time.Time    `json:"receiving_since,omitzero"`
	PausedAt       time.Time    `json:"paused_at,omitzero"`
	// Verified says a provider vouched for the account that forwards here.
	Verified bool `json:"verified"`
}

// MailboxRecordList is mailboxes as the operator sees them.
type MailboxRecordList struct {
	Mailboxes []MailboxRecord `json:"mailboxes"`
}

// MailboxCounts counts the mailboxes for the admin panel's first page.
type MailboxCounts struct {
	Total int `json:"total"`
}
