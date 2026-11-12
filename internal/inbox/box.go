package inbox

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The seam of ADR 0026 between delivery (pull request 1, which writes
// messages in the format below) and reading (pull request 2, which
// implements Reader on it). The F1 service in inbox.go and messages.go is
// deleted by pull request 1; nothing below depends on it.

// Box is one mailbox opened for one request: its identifier and its data key,
// unwrapped from the record that request just read. mailboxes.Service.Open
// makes it after checking the owner, the mailbox's state and what the
// account may do now. It lives as long as the request and is never kept: no
// enclave holds a key another may have taken out of the record.
type Box struct {
	MailboxID string
	Key       []byte
}

// Reader is what the inbox's routes do with an opened mailbox; pull request 2
// implements it on the store (store.Store for messages and raw objects,
// store.Entries and store.Scanner for txt# entries). Every method fails with
// contracts.ErrNotFound for a message that is not in the box, has expired, or
// does not open under its key. It checks nothing of who asks: Open did.
//
// Every method runs under the request's context, which ends
// mailboxes.RequestDeadline after the request started, and checks it
// between items. What reads a raw message (Get, Raw, Attachment) reads it
// with store.MessageParts.ReadRaw bounded by the size the caller took from
// the budget of mail open for reading, which the adapter takes before the
// call and gives back when the response's buffer is released; a message
// larger than the bound is ErrTooLarge, never read further. List and Search
// read message items with store.Scanner and store.MessageParts.Item, never
// a raw object (ADR 0026, "The inbox").
type Reader interface {
	// List is a page of the box's messages, the newest first, of at most
	// limit (1 to 100) below the message before ("" for the newest). It reads
	// at most ListMaxRead message items: a page may hold fewer than limit,
	// or none, and still have Next (contracts.MessagePage). Next is the last
	// message it put on the page when it stopped inside a scan's batch, and
	// the scan's Last otherwise; it is absent only when the scan said no
	// More.
	List(ctx context.Context, b Box, before string, limit int) (*contracts.MessagePage, error)
	// Search is a page of what q finds (ADR 0026, "Searching"): it opens the
	// box's search entries from below before, the newest first, until it has
	// limit matches or has read SearchMaxRead entries, and says how far it
	// looked (SearchedTo) and where to go on (Next).
	Search(ctx context.Context, b Box, q, before string, limit int) (*contracts.MessagePage, error)
	// Get is one message: its meta, its text (at most MessageTextMax), and
	// what the frame needs of its HTML (Message.HTML: its status and counts,
	// rendered under a render slot, WithRenderSlots, and RenderDeadline;
	// absent when it has no HTML part).
	Get(ctx context.Context, b Box, id string) (*contracts.Message, error)
	// Raw is the message as it was received, unsealed.
	Raw(ctx context.Context, b Box, id string) ([]byte, error)
	// Attachment is the attachment at index, its name as the message gave it
	// (the adapter cleans it with domain.AttachmentFilename) and its decoded
	// bytes. contracts.ErrNotFound for an index that is not one.
	Attachment(ctx context.Context, b Box, id string, index int) (name string, data []byte, err error)
	// Delete deletes the messages named, at most MaxDelete, and says how many
	// were there: for each the raw object, the message item, its txt# and
	// its confirm# entry. One that is not there counts as nothing.
	Delete(ctx context.Context, b Box, ids []string) (int, error)
}

// What reading is bounded by (ADR 0026, "The inbox").
const (
	// ListMaxRead is how many message items a page of the list reads at most.
	ListMaxRead = 500
	// SearchMaxRead is how many search entries a page of a search reads at
	// most, expired ones and ones that do not open included.
	SearchMaxRead = 2000
	// SearchTextMax is how much of a message's text its search entry keeps.
	SearchTextMax = 16 << 10
	// MessageTextMax is how much text Get returns (TextTruncated past it).
	MessageTextMax = 256 << 10
	// MaxDelete is how many messages one request deletes.
	MaxDelete = 100
)

// The stored format of a message (ADR 0026, "Records and layout"). Every
// sealed value is bound to its mailbox and message with envelope.AAD(purpose,
// mailbox id, message id); the purposes below are part of the format.
const (
	// PurposeMeta seals StoredMeta, the message item's meta (as F1).
	PurposeMeta = "message-meta"
	// PurposeRaw seals the raw message in the bucket (as F1).
	PurposeRaw = "message-raw"
	// PurposeSearch seals a SearchEntry with envelope.SealText.
	PurposeSearch = "message-search"
	// PurposeConfirm seals a ConfirmEntry with envelope.SealText.
	PurposeConfirm = "message-confirm"
	// PurposeSeen seals a SeenEntry with envelope.SealText, bound to the
	// mailbox and the entry's whole key (seen# and the digest) instead of a
	// message.
	PurposeSeen = "message-seen"
)

// The keys derived from a mailbox's key, each for one purpose: the names of
// its seen# entries (seenPurpose, "permaily seen v1", deliver.go) and the
// tags of its links (LinkPurpose, "permaily link v1", htmlpart.go).

// The keys of a mailbox's partition (pk=mailbox#<id>) besides its messages
// (sk=msg#<id>, written with store.Store.PutMessage).
const (
	KeySearch   = "txt#"     // + message id: SearchEntry
	KeyConfirm  = "confirm#" // + message id: ConfirmEntry
	KeySeen     = "seen#"    // + hex digest: SeenEntry
	KeySettings = "settings" // the mailbox's label and retention
)

// StoredMeta is what a message item's meta holds, sealed.
type StoredMeta struct {
	EnvelopeFrom string                  `json:"envelope_from"`
	MessageID    string                  `json:"message_id,omitempty"`
	From         []contracts.MailAddress `json:"from"`
	To           []contracts.MailAddress `json:"to"`
	Cc           []contracts.MailAddress `json:"cc"`
	ReplyTo      []contracts.MailAddress `json:"reply_to"`
	Subject      string                  `json:"subject"`
	Date         string                  `json:"date"`
	Snippet      string                  `json:"snippet"`
	// Attachments lists at most MaxAttachmentsListed, by index.
	Attachments  []contracts.MessageAttachment `json:"attachments"`
	Auth         []contracts.AuthCheck         `json:"auth"`
	Confirmation bool                          `json:"confirmation,omitempty"`
	// Unparsed: the header could not be read as mail; only Raw has it.
	Unparsed bool `json:"unparsed,omitempty"`
	// Partial: a parse limit was reached (ADR 0026, "What a message may
	// hold"); what is here is what was read before it.
	Partial bool `json:"partial,omitempty"`
}

// SearchEntry is what search reads of a message, sealed in its txt# entry.
type SearchEntry struct {
	From    []contracts.MailAddress `json:"from"`
	Subject string                  `json:"subject"`
	// Text is the start of the message's text, at most SearchTextMax bytes.
	Text string `json:"text"`
	// ReceivedAt is the message's: what SearchedTo says.
	ReceivedAt time.Time `json:"received_at"`
}

// ConfirmEntry is a Gmail forwarding confirmation, sealed in its confirm#
// entry. Verified and ForAnotherAddress are what ADR 0026 ("Gmail's
// forwarding confirmation") lets the enclave say of it.
type ConfirmEntry struct {
	Code              string    `json:"code"`
	Requester         string    `json:"requester,omitempty"`
	ReceivedAt        time.Time `json:"received_at"`
	Verified          bool      `json:"verified"`
	ForAnotherAddress bool      `json:"for_another_address,omitempty"`
	// Ends is when it stops being shown: read from here, never from the
	// entry's own expiry, so one put back after it ended shows nothing.
	Ends time.Time `json:"ends"`
}

// SeenEntry is a delivery's claim on a message for one mailbox, and then the
// mark that it is stored, sealed in its seen# entry (ADR 0026, "The claim").
// The entry's key is the hex HMAC, under a key derived from the mailbox's,
// of the envelope sender and the SHA-256 of the raw message. The entry
// expires mailboxes.SeenFor after ReceivedAt, and nothing renews it.
type SeenEntry struct {
	// Stored is false while a delivery is storing it (a claim), true once
	// every part of it is stored.
	Stored bool `json:"stored"`
	// Message, ReceivedAt, ExpiresAt and RetentionDays are fixed by the
	// delivery that first claims the message, from the settings it read
	// then, and every later try writes under them, whatever the settings say
	// by then: the same keys, the same received time, the same expiry for
	// the item, its search entry and the raw object's tag.
	Message       string    `json:"message"`
	ReceivedAt    time.Time `json:"received_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	RetentionDays int       `json:"retention_days"`
	// ClaimedAt is when the claim was made or last taken over (on the wall
	// clock of the enclave that did it): a claim not stored whose ClaimedAt
	// is more than mailboxes.ClaimStale ago is taken over by the next
	// delivery, at the version read, which sets ClaimedAt and nothing else.
	ClaimedAt time.Time `json:"claimed_at"`
	// StoredAt is when it was marked stored; zero while it is a claim.
	StoredAt time.Time `json:"stored_at,omitzero"`
}

// What a message may hold before parsing stops reading it (ADR 0026, "What a
// message may hold"). Past one, the message is still stored whole, with
// StoredMeta.Partial.
const (
	MaxHeaderBytes       = 256 << 10
	MaxHeaderFieldBytes  = 16 << 10
	MaxAddressesPerField = 100
	MaxSubjectBytes      = 2 << 10
	MaxMIMEDepth         = 16
	MaxMIMEParts         = 500
	MaxAttachmentsListed = 100
	MaxAttachmentName    = 255
)
