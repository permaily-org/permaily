package contracts

import (
	"regexp"
	"time"
)

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
	// cannot be read: its newest version is gone (lost), or its commits were
	// forked (ADR 0026, "An unreadable mailbox"). It accepts no mail. A lost
	// one can be disconnected, which deletes what it holds.
	ErrMailboxUnreadable = &Error{"mailbox_unreadable", "this mailbox can't be read; disconnect it, and write to support@permaily.com"}
	// ErrMailboxForked refuses disconnecting a mailbox whose record was
	// forked: nothing can be written to the record, so it stays in the
	// account's list and counts against the limit, accepting no mail and
	// opened by nobody, for as long as the two commits exist (the lock
	// period). Same code as ErrMailboxUnreadable, other words.
	ErrMailboxForked = &Error{"mailbox_unreadable", "this mailbox can't be disconnected: what Permaily stores for it was changed by someone else. It accepts no mail and nobody can open it. Write to support@permaily.com"}
	// ErrLinkChanged refuses a link of a message's HTML whose token no longer
	// names the link at its number (409): the page was rendered by another
	// release of the sanitiser. The message must be opened again.
	ErrLinkChanged = &Error{"link_changed", "this link changed since the message was opened; open the message again"}
	// ErrBusy says the enclave has no room to render a message's HTML now
	// (503): every render slot is taken and the queue is full or did not move
	// in time. Nothing is wrong with the message.
	ErrBusy = &Error{"busy", "Permaily is busy; try again in a moment"}
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
	// (ErrMailboxUnreadable): it refuses mail and shows nothing of itself
	// but its identifier and when it was added. One whose record was lost
	// can be disconnected; one whose record was forked cannot
	// (ErrMailboxForked) and stays, counted, for the lock period.
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
	// Verified is true only when every check of ADR 0026 ("Gmail's
	// forwarding confirmation") holds: one From, To and Subject each; From
	// exactly Gmail's forwarding service; a passing DKIM signature of
	// google.com over From, To and Subject and the whole body; To this
	// mailbox's address; and a body in Gmail's template that names it. Then
	// Google confirms that Requester asked to forward to this mailbox. It is
	// false for every confirmation until a release turns the check on, once
	// a real confirmation captured in staging passes it.
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
	// HTMLUnavailable is HTML the enclave could not render now: no render
	// slot came in time, or the render passed its deadline. The text is
	// shown, with a way to try again.
	HTMLUnavailable = "unavailable"
)

// The limits of showing a message's HTML (ADR 0026, "Showing a message's
// HTML", "Limits", where each has its reason). Past the ones of the part,
// the tokens, the tree and the output the HTML is HTMLTooLarge; past the
// others what is over is left out and the rest is shown. The render budgets
// were checked against what the sanitiser measured in an enclave's size
// (ADR 0026, "Limits").
const (
	// MessageHTMLMaxBytes bounds the HTML part, before and after it is
	// transcoded to UTF-8.
	MessageHTMLMaxBytes = 2 << 20
	// MessageHTMLMaxStartTags bounds the start tags the tokeniser pass
	// counts, before any tree is built.
	MessageHTMLMaxStartTags = 50_000
	// MessageHTMLMaxAttributesPerElement and MessageHTMLMaxAttributes bound
	// the attributes of one element and of the part.
	MessageHTMLMaxAttributesPerElement = 64
	MessageHTMLMaxAttributes           = 200_000
	// MessageHTMLMaxAttributeBytes bounds one attribute's value, except an
	// img src, which the inline image limits bound.
	MessageHTMLMaxAttributeBytes = 8 << 10
	// MessageHTMLMaxTreeBound bounds the tokeniser pass's upper bound of the
	// tree the parser would build (amplification by reconstructed formatting
	// elements included).
	MessageHTMLMaxTreeBound = 200_000
	// MessageHTMLMaxElements and MessageHTMLMaxDepth bound the output's
	// elements and how deep they nest.
	MessageHTMLMaxElements = 50_000
	MessageHTMLMaxDepth    = 256
	// MessageHTMLMaxMarkupBytes bounds the output without its images,
	// counted as it is written.
	MessageHTMLMaxMarkupBytes = 4 << 20
	// MessageHTMLMaxStyleBytes and MessageHTMLMaxStyleAttributeBytes bound
	// the CSS of the <style> blocks together and of one style attribute.
	MessageHTMLMaxStyleBytes          = 256 << 10
	MessageHTMLMaxStyleAttributeBytes = 8 << 10
	// MessageHTMLMaxCSSRules, MessageHTMLMaxCSSDeclarations and
	// MessageHTMLMaxSelectorBytes bound the message's CSS rules, its
	// declarations in all, and one selector.
	MessageHTMLMaxCSSRules        = 5_000
	MessageHTMLMaxCSSDeclarations = 20_000
	MessageHTMLMaxSelectorBytes   = 1 << 10
	// MessageHTMLMaxLinks is how many links are numbered; the rest are text.
	// MessageHTMLMaxURLBytes bounds one link's URL; a longer one is text.
	MessageHTMLMaxLinks    = 1000
	MessageHTMLMaxURLBytes = 4096
	// MessageInlineImagesMax, MessageInlineImageMaxBytes and
	// MessageInlineImagesMaxBytes bound the images that travel in the
	// message (cid: parts and data: URIs), decoded.
	MessageInlineImagesMax      = 50
	MessageInlineImageMaxBytes  = 2 << 20
	MessageInlineImagesMaxBytes = 5 << 20
	// MessageInlineImageMaxSide, MessageInlineImageMaxPixels and
	// MessageInlineImagesMaxPixels bound what an image costs the browser to
	// decode, read from its header without decoding it: a side, one image's
	// pixels (64 MiB at four bytes a pixel), all of a message's (128 MiB).
	MessageInlineImageMaxSide    = 8192
	MessageInlineImageMaxPixels  = 1 << 24
	MessageInlineImagesMaxPixels = 1 << 25
	// MessageHTMLRenders is how many renders run at once in one enclave;
	// MessageHTMLRenderQueue how many may wait, for at most
	// MessageHTMLRenderWait.
	MessageHTMLRenders     = 2
	MessageHTMLRenderQueue = 16
	MessageHTMLRenderWait  = 5 * time.Second
	// MessageHTMLRendersPerAccount and MessageHTMLQueuedPerAccount are one
	// account's share: past it, too_many_requests.
	MessageHTMLRendersPerAccount = 1
	MessageHTMLQueuedPerAccount  = 2
	// MessageHTMLRenderDeadline stops a render that runs longer: the
	// slowest input the limits accept took 0.32 s (ADR 0026, "Limits").
	MessageHTMLRenderDeadline = 2 * time.Second
	// MessageHTMLRenderMemory is the most one render holds at its peak, the
	// heap above where it started, garbage not yet collected included: the
	// largest sample measured is 110.6 MiB on one CPU, where the collector
	// runs late behind a render that allocates 532 MB (ADR 0026, "Limits");
	// 64 MiB until the second review of pull request 5 added the prepared
	// stream and the plain parse of the output after the bounded one.
	MessageHTMLRenderMemory = 128 << 20
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
	// JPEG, GIF or WebP by their bytes, static, within the limits);
	// InlineImagesOmitted the ones that are not, and InlineImagesOmittedBy
	// why, so the app can say it. Remote images are RemoteImages, never
	// omitted ones.
	InlineImages          int                  `json:"inline_images"`
	InlineImagesOmitted   int                  `json:"inline_images_omitted"`
	InlineImagesOmittedBy MessageImagesOmitted `json:"inline_images_omitted_by"`
	// Links counts the links that open the link page, at most
	// MessageHTMLMaxLinks.
	Links int `json:"links"`
}

// MessageImagesOmitted says why images that travel in a message were not
// shown; the three add up to MessageHTML.InlineImagesOmitted.
type MessageImagesOmitted struct {
	// TooLarge is over a limit of bytes, of a side, of pixels, or of how
	// many a message shows.
	TooLarge int `json:"too_large"`
	// Animated is an animated GIF, PNG (APNG) or WebP: none is shown in
	// this version.
	Animated int `json:"animated"`
	// Unsupported is not PNG, JPEG, GIF or WebP by its bytes (SVG
	// included), a cid: that names no part, or a structure that does not
	// read.
	Unsupported int `json:"unsupported"`
}

// MessageLink is where a link of a message's HTML leads, for the page the
// app shows before the person follows it (GET .../links/{link}). The page
// parses URL with the browser's own URL parser, shows and goes where that
// parse says, and offers nothing when its host is not Host.
type MessageLink struct {
	// Link is the link's token as the route names it, "<n>.<tag>": its
	// number in the message and a tag bound to its address
	// (MessageLinkToken), so a link renumbered by another release answers
	// ErrLinkChanged instead of another address.
	Link string `json:"link"`
	// URL is the link's address, http or https, as the server wrote it
	// again: ASCII, its host a DNS name in ASCII (xn-- for other
	// characters, so a look-alike is seen), no userinfo, no default port.
	URL string `json:"url"`
	// Host is its host and, when it is not the scheme's default, ":port",
	// as the WHATWG URL's host attribute writes it.
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

// The token of a link of a message's HTML: its number, a dot, and a tag of
// MessageLinkTagBytes bytes in unpadded base64url (an HMAC under a key of
// the mailbox, ADR 0026, "The link's token").
const MessageLinkTagBytes = 12

var messageLinkToken = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})\.[A-Za-z0-9_-]{16}$`)

// MessageLinkToken says whether s has the shape of a link's token: a number
// below MessageHTMLMaxLinks without leading zeros, a dot, and 16 base64url
// characters. One that does not is not_found; one that does but no longer
// names the link is ErrLinkChanged.
func MessageLinkToken(s string) bool {
	return messageLinkToken.MatchString(s)
}
