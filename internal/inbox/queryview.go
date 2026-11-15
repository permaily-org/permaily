package inbox

import (
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// What evaluation knows of a stored message (ADR 0027, "Evaluation over
// stored mail"): QueryMessage turns what a page of matches has read of a
// message into query.Message, each source only when it was read.

// searchCutSlack is how close to SearchTextMax the text of a search entry
// written before ADR 0027 (which does not say whether it was cut) ends when
// it was cut: cutText ends a cut text on a whole character, at most three
// bytes before the bound, so an entry without Cut whose text is within four
// bytes of it is taken as cut.
const searchCutSlack = 4

// RawText is what a raw read gives evaluation: the message's text as a
// connection receives it (MessageText, at most MessageTextMax bytes) and the
// length of the raw message, which is its size when the meta has none.
type RawText struct {
	Text string
	Size int64
}

// entryCut reports whether a search entry's text may be shorter than the
// message's: it says it was cut, or it was written before entries said so
// and ends where a cut text ends.
func entryCut(e *SearchEntry) bool {
	return e.Cut || len(e.Text) >= SearchTextMax-searchCutSlack
}

// QueryMessage is what evaluation knows of a message from what was read of
// it: its item (FromItem: the mailbox it is in, and when it was received,
// from the time in its identifier, never the item's plain fields), its
// opened meta (FromMeta: senders, recipients, the domains of the DKIM
// signatures that passed, the subject, the attachments, the flags; the list
// and the size only from a meta of MetaFormat or later, and the list not
// when the meta says its List-Id could not be read, or is Partial and holds
// none: the header may have been cut before it), its opened search
// entry (FromEntry: the first SearchTextMax bytes of the text, and whether
// they were cut), and its raw message read and opened (FromText: the whole
// text, which supersedes the entry's, and the raw length, which is the size
// of a message whose meta has none). Any of them may be nil. The size the
// item stores beside the meta is never read: whoever writes the table sets
// it.
//
// A page reads the entry for a message whose value depends on its body, and
// when the entry is missing or does not open it passes a SearchEntry with
// Cut and no text, so the body is read from the raw message as a cut text's
// is.
func QueryMessage(item *store.Message, meta *StoredMeta, entry *SearchEntry, text *RawText) *query.Message {
	m := &query.Message{}
	if item != nil {
		m.Have |= query.FromItem
		m.Mailbox = item.MailboxID
		m.Received = timeOfMessageID(item.ID)
	}
	if meta != nil {
		m.Have |= query.FromMeta
		for _, a := range meta.From {
			m.From = append(m.From, a.Address)
		}
		for _, a := range meta.To {
			m.Recipients = append(m.Recipients, a.Address)
		}
		for _, a := range meta.Cc {
			m.Recipients = append(m.Recipients, a.Address)
		}
		for _, a := range meta.Auth {
			if a.Method == "dkim" && a.Result == "pass" {
				m.SignedBy = append(m.SignedBy, a.Domain)
			}
		}
		m.Subject = meta.Subject
		for _, a := range meta.Attachments {
			m.Attachments = append(m.Attachments, query.Attachment{Name: a.Name, Type: a.Type})
		}
		if meta.Format >= MetaFormat {
			// No List-Id is a known absence only when the header was read
			// whole (not Partial) and any List-Id in it was read.
			m.ListRecorded = !meta.ListUnknown && (meta.List != "" || !meta.Partial)
			if m.ListRecorded && meta.List != "" {
				m.List = []string{meta.List}
			}
			m.Size, m.SizeKnown = meta.Size, true
		}
		m.Partial, m.Unparsed, m.Confirmation = meta.Partial, meta.Unparsed, meta.Confirmation
	}
	if entry != nil {
		m.Have |= query.FromEntry
		m.Body, _ = cutText(entry.Text, SearchTextMax)
		m.BodyCut = entryCut(entry)
	}
	if text != nil {
		// The whole text: what the entry held is a prefix of it, and nothing
		// of it is cut any more.
		m.Have |= query.FromEntry | query.FromText
		m.Body, m.BodyCut = text.Text, false
		if !m.SizeKnown {
			m.Size, m.SizeKnown = text.Size, true
		}
	}
	return m
}
