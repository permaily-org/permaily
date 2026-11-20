package accounts

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain/mail"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The notices about connections and the record of what left (ADR 0041,
// "Which notices"): a connection approved, a device added, a connection
// renewed, one about to end, a device ended because a token of it was used
// twice, receipts found deleted or changed, and every device signed out by
// the operator or an admin. They are queued (internal/services/notices) and
// the queue hands each to Deliver, which writes it from fixed words and sends
// it to the account's contact address as it is when it sends.
//
// What a notice may say is ADR 0017's, applied to connections: fixed words;
// no code, link, token or identifier; nothing a person typed; and nothing a
// vendor chose. A vendor is named only when the image's verified list names
// it (vendors.Verified); any other is "An app Permaily hasn't verified",
// never its domain, which a vendor chooses. The parts, the resource and who
// signed devices out are worded from fixed lists: one Deliver does not know
// is notices.ErrInvalid, never interpolated.
//
// The words are a first draft, to review with the designer (ADR 0041).

var _ notices.Deliverer = (*Service)(nil)

// Why a notice about a connection was not sent this time: each is tried
// again while the notice is sendable (notices.Deliverer). None holds the
// address, the account or a word of the storage's or the mail system's
// error, which can carry them.
var (
	errNoticeUnread = errors.New("accounts: the account of a notice could not be read")
	errNoticeState  = errors.New("accounts: the account of a notice is in a state its notices do not know")
	errNoticeSend   = errors.New("accounts: a notice could not be sent")
)

// Deliver implements notices.Deliverer. It checks the notice and words it
// before it reads anything: a notice that is not one of a kind it words, with
// its facts, is notices.ErrInvalid. It then reads the account, with its
// record's whole history, within s.deliverFor at most (and ctx's deadline):
// an account it read and found removed or due to be, closing or blocked is
// notices.ErrSkip; an open one, its terms agreed or not, is told at its
// contact address. Anything else (a record never made, lost, forked or that
// does not open as the account's, a storage that fails, refuses or does not
// answer in time, a state it does not know) is an error tried again, and
// nothing is sent while it lasts (ADR 0041, "Changed after the design
// review", 1).
func (s *Service) Deliver(ctx context.Context, n notices.Notice) error {
	if n.Check() != nil {
		return notices.ErrInvalid
	}
	subject, blocks, err := connectionNotice(s.rp.ID, n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.deliverFor)
	defer cancel()
	to, err := s.reachable(ctx, n.Account)
	if err != nil {
		return err
	}
	// Not the error: a mail system's errors carry the address.
	if s.cfg.Mailer.Send(ctx, compose(to, subject, blocks...)) != nil {
		return errNoticeSend
	}
	return nil
}

// reachable reads the account a notice is for and returns its contact
// address, or why it is not told now (Deliver).
func (s *Service) reachable(ctx context.Context, id string) (string, error) {
	checker, ok := s.cfg.Records.(historyChecker)
	if !ok || checker.CheckHistory(ctx, recordName(id)) != nil {
		return "", errNoticeUnread
	}
	doc, at, err := s.readAt(ctx, id)
	switch {
	case err != nil:
		return "", errNoticeUnread
	case doc == nil && at.Version() > 0:
		return "", notices.ErrSkip // removed by its terminal commit
	case doc == nil:
		// Never made: nothing says it is gone, and a security notice is
		// not given up on what cannot be told apart from a lost record.
		return "", errNoticeUnread
	case doc.due(s.now()), doc.blocked(), doc.State == contracts.AccountClosing:
		return "", notices.ErrSkip
	case doc.State != contracts.AccountActive || doc.Address == "":
		return "", errNoticeState
	}
	return doc.Address, nil
}

// connectionNoticeMail is a notice about a connection as it is sent to to
// (Deliver; the samples of the mail).
func connectionNoticeMail(to, app string, n notices.Notice) (Mail, error) {
	subject, blocks, err := connectionNotice(app, n)
	if err != nil {
		return Mail{}, err
	}
	return compose(to, subject, blocks...), nil
}

// connectionNotice writes a notice about a connection: its subject and its
// blocks, a lead, its facts, a paragraph and, but for an ending, a note. app
// is the app's host, as every notice names it.
func connectionNotice(app string, n notices.Notice) (string, []mail.Block, error) {
	f := n.Facts
	when := at("When", n.At)
	who := mail.Fact{Label: "App", Value: appName(f.Vendor)}
	ends := at("Ends", f.EndsAt)
	seeIt := "You can see what it reads, and revoke it, in Connections at " + app + "."
	ifNotYou := "If this wasn't you, sign in at " + app + ", revoke it in Connections, sign out the devices you don't know, and write to " + supportAddress + "."
	switch n.Kind {
	case notices.KindConnectionApproved:
		reads, ok := readsOf(f.Parts)
		through, known := throughOf[f.Resource]
		if !ok || !known {
			return "", nil, notices.ErrInvalid
		}
		lead := "A connection to your mail was approved on your Permaily account. The app below can read the mail it allows until it ends."
		if f.Replaced {
			lead += " It replaces the connection that app had before."
		}
		return "A connection to your mail was approved", connectionBlocks(lead,
			[]mail.Fact{when, who, {Label: "Reads", Value: reads}, {Label: "Through", Value: through}, ends}, seeIt, ifNotYou), nil
	case notices.KindDeviceAdded:
		reads, ok := readsOf(f.Parts)
		if !ok {
			return "", nil, notices.ErrInvalid
		}
		return "A device was added to one of your connections", connectionBlocks(
			"A new device was added to one of your connections. It reads the same mail the connection reads.",
			[]mail.Fact{when, who, {Label: "Reads", Value: reads}, ends}, seeIt, ifNotYou), nil
	case notices.KindConnectionRenewed:
		return "One of your connections was renewed", connectionBlocks(
			"One of your connections was renewed. It keeps reading the mail it reads now until its new end.",
			[]mail.Fact{when, who, ends}, seeIt, ifNotYou), nil
	case notices.KindConnectionEnding:
		return "One of your connections ends in a week", connectionBlocks(
			"One of your connections ends in a week. After that, the app below can no longer read your mail.",
			[]mail.Fact{who, ends}, "To keep it, sign in at "+app+", open Connections and renew it. To let it end, do nothing.", ""), nil
	case notices.KindDeviceEndedReuse:
		return "Permaily ended a device of one of your connections", connectionBlocks(
			"Permaily ended a device of one of your connections, because a key that device reads with was used twice. That can mean someone copied it.",
			[]mail.Fact{when, who}, "The app may ask you to connect again: approve it only if you asked for it.",
			"If you don't know why this happened, revoke the connection in Connections at "+app+", and write to "+supportAddress+"."), nil
	case notices.KindReceiptsIncident:
		return "Some of your receipts were deleted or changed", connectionBlocks(
			"Permaily found that some of the receipts that record what left your mailboxes were deleted or changed in its storage.",
			[]mail.Fact{when},
			"Open Activity at "+app+" to see which. Your receipts after them are still recorded, and Permaily's public check now fails until this is looked into.",
			"To ask about it, write to "+supportAddress+"."), nil
	case notices.KindDevicesSignedOut:
		by, known := signedOutBy[f.By]
		if !known {
			return "", nil, notices.ErrInvalid
		}
		return "Every device was signed out of your Permaily account", connectionBlocks(
			by+" signed every device out of your Permaily account.",
			[]mail.Fact{when}, "You can sign in again at "+app+".", "To ask why, write to "+supportAddress+"."), nil
	}
	return "", nil, notices.ErrInvalid
}

// connectionBlocks are a notice's blocks: a lead, its facts, a paragraph and
// a note, when there is one.
func connectionBlocks(lead string, facts []mail.Fact, paragraph, note string) []mail.Block {
	blocks := []mail.Block{mail.Lead(lead), mail.Facts(facts...), mail.Paragraph(paragraph)}
	if note != "" {
		blocks = append(blocks, mail.Note(note))
	}
	return blocks
}

// appName names a connection's vendor: by the owner the image's verified list
// gives its domain, and otherwise as an app Permaily has not verified, never
// by its domain or by what its client calls itself, which a vendor chooses.
func appName(domain string) string {
	if owner, ok := vendors.Verified(domain); ok {
		return "An app by " + owner + " (" + domain + ")"
	}
	return "An app Permaily hasn't verified"
}

// partNames are the parts a connection reads, in the order a notice lists
// them.
var partNames = []struct {
	part contracts.Part
	name string
}{
	{contracts.PartSender, "sender and date"},
	{contracts.PartSubject, "subject"},
	{contracts.PartBody, "body text"},
	{contracts.PartAttachments, "attachments"},
}

// readsOf words the parts a connection reads, in partNames' order whatever
// theirs: false when one is not a part it knows, or is given twice.
func readsOf(parts []contracts.Part) (string, bool) {
	given := map[contracts.Part]bool{}
	for _, p := range parts {
		if given[p] {
			return "", false
		}
		given[p] = true
	}
	var words []string
	for _, p := range partNames {
		if given[p.part] {
			words = append(words, p.name)
			delete(given, p.part)
		}
	}
	if len(words) == 0 || len(given) > 0 {
		return "", false
	}
	out := strings.Join(words, ", ")
	return strings.ToUpper(out[:1]) + out[1:], true
}

// throughOf words the resource a connection reads through.
var throughOf = map[contracts.Resource]string{
	contracts.ResourceAPI: "API",
	contracts.ResourceMCP: "MCP",
}

// signedOutBy names who signed every device out, at the start of a sentence.
var signedOutBy = map[string]string{
	notices.ByOperator: "Permaily's operator",
	notices.ByAdmin:    "An admin of Permaily",
}

// noticeSignedOut queues the notice that every device of an account was
// signed out, by the operator (by nil) or an admin, its key the account's
// count of sign-outs after it (ADR 0041, "Which notices"). It is called after
// the change is stored, fails nothing, and holds its caller no longer than
// notices.EnqueueMax: one that cannot be queued is logged by its kind alone.
func (s *Service) noticeSignedOut(ctx context.Context, by *acting, doc *accountDoc) {
	if s.cfg.Notices == nil {
		return
	}
	signer := notices.ByOperator
	if by != nil {
		signer = notices.ByAdmin
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notices.EnqueueMax)
	defer cancel()
	n := notices.Notice{
		Account: doc.ID, Kind: notices.KindDevicesSignedOut, At: s.now(),
		Key:   notices.Key(doc.ID, notices.KindDevicesSignedOut, strconv.Itoa(doc.SignOuts)),
		Facts: notices.Facts{By: signer},
	}
	if s.cfg.Notices.Enqueue(ctx, n) != nil {
		s.cfg.Log.Warn("accounts: a notice could not be queued", "kind", string(notices.KindDevicesSignedOut))
	}
}

