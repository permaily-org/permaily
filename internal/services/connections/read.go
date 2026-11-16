package connections

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// What a vendor reads (ADR 0030, "What a read gives"), in "Release"'s
// order: the gates, the disclosure prepared in memory, the authority again
// when the grant's freshness passed, the receipt, then the answer under its
// lease.

// reading is one vendor request being prepared.
type reading struct {
	g        *oauth.Grant
	conn     *connDoc
	d        inbox.Disclosure
	formulas []receipts.Formula
}

// prepare checks the gates of a read and makes its disclosure, the search's
// formula (nil for none) evaluated with the connection's.
func (s *Service) prepare(ctx context.Context, g *oauth.Grant, search *query.Canonical) (*reading, error) {
	// The grant is Check's: the request is attributed from here, so every
	// refusal is recorded, those before its disclosure too, with the
	// grant's attribution alone and a fixed reason (PR #136's second
	// review, finding 4).
	r := &reading{g: g, conn: &connDoc{}}
	refused := func(err error) (*reading, error) {
		if errors.Is(err, ErrUnavailable) {
			return nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedUnavailable}, ErrUnavailable)
		}
		return nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedConnectionEnded}, ErrInvalidToken)
	}
	d, _, err := s.authority(ctx, g, false)
	if err != nil {
		return refused(err)
	}
	sc, err := d.scope()
	if err != nil {
		return refused(ErrInvalidToken) // a stored scope that does not read
	}
	own, err := s.deviceScope(ctx, d, g.Family)
	if err != nil {
		return refused(err)
	}
	r.conn = d
	add := func(role receipts.FormulaRole, c *query.Canonical, full bool) {
		r.d.Formulas = append(r.d.Formulas, c)
		f := receipts.Formula{Role: role, ID: c.ID}
		if full {
			cf := c.Formula
			f.Formula = &cf
		}
		r.formulas = append(r.formulas, f)
	}
	if sc.Query != nil {
		add(receipts.RoleConsented, sc.Query, false)
	}
	add(receipts.RoleMailboxes, sc.Mailboxes, false)
	if sc.Since != nil {
		add(receipts.RoleSince, sc.Since, false)
	}
	for _, f := range own {
		add(receipts.RoleConsented, f, false)
	}
	if search != nil {
		add(receipts.RoleSearch, search, true)
	}
	r.d.Rules, r.d.Parts, r.d.Account, r.d.Model = s.cfg.Rules, slices.Clone(g.Parts), g.Account, true
	// Nothing leaves while the model cannot be asked, nor for a connection
	// approved under another notice (the owner's decision 9, finding 18).
	if d.ModelNotice != s.cfg.ModelNotice || s.cfg.Model == nil || s.cfg.Model.Gate(ctx, g.Account) != inbox.AskAnswered {
		s.count("model_unavailable")
		return nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedModelUnavailable}, contracts.ErrModelUnavailable)
	}
	return r, nil
}

// receipt is a receipt of r's request.
func (s *Service) receipt(r *reading, kind receipts.Kind) *receipts.Receipt {
	return &receipts.Receipt{
		V: receipts.Version, Kind: kind, Connection: r.g.Connection, Device: r.g.Family, ClientID: r.g.ClientID,
		Vendor: r.g.Vendor, Resource: r.g.Resource, Formulas: r.formulas, Rules: s.ruleRefs(),
	}
}

// refuse records the refusal of a request a valid token attributed to its
// connection (ADR 0031's choice 7): every way Search, Read and Attachment
// end after prepare without giving mail goes through it, with its fixed
// reason (finding 8). It answers err, or ErrUnavailable when the refusal
// could not be recorded, so the vendor is answered 503 and asks again.
func (s *Service) refuse(ctx context.Context, r *reading, why *receipts.Refused, err error) error {
	rc := s.receipt(r, receipts.KindRefused)
	rc.Refused = why
	return s.refuseWith(ctx, r, rc, err)
}

// refuseWith records rc, a refusal with what it already holds (a page's
// held messages and their evidence), as refuse does. A refusal releases
// nothing, so it is written under a fresh ReceiptWriteMax alone, not the
// grant's hard end nor the request's context: a refusal after the hard end
// or of a request whose client went away is still recorded, with the
// grant's attribution (ruling 6 on PR #136's second review).
func (s *Service) refuseWith(ctx context.Context, r *reading, rc *receipts.Receipt, err error) error {
	if werr := s.writeReceipt(context.WithoutCancel(ctx), r.g.Account, rc, time.Time{}); werr != nil {
		s.count("receipt_failed")
		return ErrUnavailable
	}
	return err
}

// release is steps 3 to 5 of "Release": the authority again when the
// freshness passed during the preparation, the receipt under its own bound
// and the hard end, and the lease the answer is sent under.
func (s *Service) release(ctx context.Context, r *reading, rc *receipts.Receipt) (Release, error) {
	if err := s.again(ctx, r.g); err != nil {
		if errors.Is(err, errRefusedAuthority) {
			s.count("too_late")
			_ = s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedTooLate}, nil)
			return nil, ErrTooLate
		}
		return nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedUnavailable}, ErrUnavailable)
	}
	if err := s.writeReceipt(ctx, r.g.Account, rc, r.g.Expires()); err != nil {
		s.count("receipt_failed")
		return nil, ErrUnavailable
	}
	return s.newLease(r, rc.ID), nil
}

// Info implements API: what the vendor is told of its connection. It gives
// no mail, so it writes no receipt and needs no lease.
func (s *Service) Info(ctx context.Context, g *oauth.Grant) (*contracts.VendorConnection, error) {
	d, _, err := s.authority(ctx, g, false)
	if err != nil {
		return nil, err
	}
	out := &contracts.VendorConnection{Parts: slices.Clone(g.Parts), Since: d.private.Since, EndsAt: d.EndsAt, Delivery: contracts.DeliveryOn}
	if out.Since.New {
		out.Since.At = d.MadeAt
	}
	if d.private.Query != nil {
		q := *d.private.Query
		out.Query = &q
	}
	if s.delivery(ctx, d) != contracts.DeliveryOn {
		out.Delivery = contracts.DeliveryModelUnavailable
	}
	return out, nil
}

// cursorDoc is a vendor's cursor, sealed: bound to the connection, the
// device, the effective parts and every formula of the request.
type cursorDoc struct {
	Connection string           `json:"c"`
	Family     string           `json:"f"`
	Parts      []contracts.Part `json:"p"`
	Formulas   []string         `json:"q"`
	Mailbox    int              `json:"m"`
	Before     string           `json:"b"`
	Expires    time.Time        `json:"e"`
}

var cursorAAD = envelope.AAD("vendor cursor")

// b64 is unpadded base64url, a cursor's encoding.
var b64 = base64.RawURLEncoding

func (r *reading) formulaIDs() []string {
	out := make([]string, len(r.d.Formulas))
	for i, f := range r.d.Formulas {
		out[i] = f.ID
	}
	return out
}

func (s *Service) sealCursor(r *reading, mailbox int, before string) string {
	raw, _ := json.Marshal(cursorDoc{
		Connection: r.g.Connection, Family: r.g.Family, Parts: r.d.Parts, Formulas: r.formulaIDs(),
		Mailbox: mailbox, Before: before, Expires: s.now().Add(CursorTTL),
	})
	sealed, err := envelope.Seal(s.cursorKey, raw, cursorAAD)
	if err != nil {
		return ""
	}
	return oauth.PrefixCursor + b64.EncodeToString(sealed)
}

func (s *Service) openCursor(r *reading, c string) (int, string, error) {
	raw, err := b64.DecodeString(strings.TrimPrefix(c, oauth.PrefixCursor))
	if err != nil || !strings.HasPrefix(c, oauth.PrefixCursor) {
		return 0, "", contracts.ErrInvalidCursor
	}
	plain, err := envelope.Open(s.cursorKey, raw, cursorAAD)
	var doc cursorDoc
	if err != nil || json.Unmarshal(plain, &doc) != nil {
		return 0, "", contracts.ErrInvalidCursor
	}
	if doc.Connection != r.g.Connection || doc.Family != r.g.Family || !slices.Equal(doc.Parts, r.d.Parts) ||
		!slices.Equal(doc.Formulas, r.formulaIDs()) || !s.now().Before(doc.Expires) || doc.Mailbox < 0 {
		return 0, "", contracts.ErrInvalidCursor
	}
	return doc.Mailbox, doc.Before, nil
}

// searchFormula reads a vendor's search: well formed, and looking only at
// what the request's effective parts give (finding 4).
func searchFormula(q *contracts.Formula, parts []contracts.Part) (*query.Canonical, error) {
	if q == nil {
		return nil, nil
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return nil, contracts.ErrInvalidFormula
	}
	c, err := query.Parse(raw, nil)
	if err != nil {
		return nil, err
	}
	for i, g := range q.All {
		for j, cond := range g.Any {
			need, ok := contracts.ConditionParts(cond.Var, cond.Op)
			if !ok || !subset(parts, need) {
				at := "all[" + strconv.Itoa(i) + "].any[" + strconv.Itoa(j) + "].var"
				return nil, &query.Problem{FormulaProblem: contracts.FormulaProblem{At: at, Reason: contracts.FormulaPartNotApproved}}
			}
		}
	}
	return c, nil
}

// vendorMessage is a message as it leaves: its identifier for the vendor,
// and only the effective parts.
func vendorMessage(mailbox string, dis *inbox.Disclosed, parts []contracts.Part, text bool) contracts.VendorMessage {
	m := dis.Message
	m.ID = mailbox + "." + dis.ID
	if !slices.Contains(parts, contracts.PartSubject) {
		m.Subject = nil
	}
	if !text || !slices.Contains(parts, contracts.PartBody) {
		m.Text, m.TextCut = nil, false
	}
	if !slices.Contains(parts, contracts.PartAttachments) {
		m.Attachments = nil
	}
	return m
}

// Search implements API.
func (s *Service) Search(ctx context.Context, g *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, Release, error) {
	search, ferr := searchFormula(in.Query, g.Parts)
	r, err := s.prepare(ctx, g, search)
	if err != nil {
		return nil, nil, err
	}
	refuse := func(reason receipts.RefusedReason, err error) (*contracts.VendorPage, Release, error) {
		return nil, nil, s.refuse(ctx, r, &receipts.Refused{Reason: reason}, err)
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > inbox.DiscloseMaxLimit {
		s.count("invalid_limit")
		return refuse(receipts.RefusedInvalidLimit, contracts.ErrInvalidLimit)
	}
	if ferr != nil {
		reason := receipts.RefusedFormula
		var p *query.Problem
		if errors.As(ferr, &p) && p.Reason == contracts.FormulaPartNotApproved {
			reason = receipts.RefusedFormulaPart
		}
		s.count("invalid_formula")
		return refuse(reason, ferr)
	}
	if !s.limits.allow("search "+g.Connection, SearchesPerMinute, time.Minute, s.now()) {
		s.count("rate_limited")
		return refuse(receipts.RefusedRateLimited, contracts.ErrTooManyRequests)
	}
	index, before := 0, ""
	if in.Cursor != "" {
		if index, before, err = s.openCursor(r, in.Cursor); err != nil {
			s.count("invalid_cursor")
			return refuse(receipts.RefusedInvalidCursor, err)
		}
	}
	// The receipt is built message by message as Write will store it, its
	// lists' framing and the writer's own fields counted (receipts.Page,
	// finding 9): the page ends before the message that would pass its bound.
	rp, err := receipts.NewPage(s.receipt(r, receipts.KindSearch))
	if err != nil {
		return refuse(receipts.RefusedUnavailable, ErrUnavailable)
	}
	out := &contracts.VendorPage{Messages: []contracts.VendorMessage{}}
	considered, modelShort := 0, false
	mailboxes := r.conn.Mailboxes
pages:
	for index < len(mailboxes) {
		mbx := mailboxes[index]
		box, err := s.cfg.Mailboxes.OpenForConnection(ctx, g.Account, mbx)
		if errors.Is(err, contracts.ErrNotFound) {
			index, before = index+1, "" // a mailbox no longer there is skipped, never read
			continue
		}
		if err != nil {
			return refuse(receipts.RefusedUnavailable, err)
		}
		page, err := s.cfg.Discloser.Page(ctx, box, r.d, before, limit-considered)
		if err != nil {
			return refuse(receipts.RefusedUnavailable, err)
		}
		if page.Model != "" && page.Model != contracts.MatchModelAsked {
			modelShort = true
		}
		for _, it := range page.Items {
			if (it.Disclosed == nil) == (it.Withheld == nil) {
				continue
			}
			fit, err := rp.Add(mbx, r.d.Parts, it)
			if err != nil {
				return refuse(receipts.RefusedUnavailable, ErrUnavailable)
			}
			if !fit {
				// The page ends before the message that would take its
				// receipt past its bound; the cursor continues from the last
				// one recorded (ADR 0031, finding 10).
				out.Cursor = s.sealCursor(r, index, before)
				break pages
			}
			considered++
			before = it.ID()
			if it.Disclosed != nil {
				out.Messages = append(out.Messages, vendorMessage(mbx, it.Disclosed, r.d.Parts, false))
			}
		}
		if page.Next == "" {
			index, before = index+1, ""
			if considered >= limit && index < len(mailboxes) {
				out.Cursor = s.sealCursor(r, index, "")
				break
			}
			continue
		}
		// The mailbox has more: the page ends here (its budgets spent, or its
		// limit reached), and goes on below what it considered.
		before = page.Next
		out.Cursor = s.sealCursor(r, index, before)
		break
	}
	if len(out.Messages) == 0 && modelShort {
		// The refusal keeps what the page held, with its evidence.
		rc := rp.Receipt()
		rc.Kind, rc.Refused = receipts.KindRefused, &receipts.Refused{Reason: receipts.RefusedModelUnavailable}
		s.count("model_unavailable")
		return nil, nil, s.refuseWith(ctx, r, rc, contracts.ErrModelUnavailable)
	}
	rel, err := s.release(ctx, r, rp.Receipt())
	if err != nil {
		return nil, nil, err
	}
	return out, rel, nil
}

// message is a vendor's message identifier split, checked against the
// connection's mailboxes before anything is read.
func message(r *reading, id string) (string, string, bool) {
	mbx, msg, ok := strings.Cut(id, ".")
	if !ok || len(id) > 256 || msg == "" || !slices.Contains(r.conn.Mailboxes, mbx) {
		return "", "", false
	}
	return mbx, msg, true
}

// refuseRead records a refusal of a read or an attachment and answers it.
func (s *Service) refuseRead(ctx context.Context, r *reading, mbx, msg string, o inbox.Outcome, err error) error {
	switch {
	case o.Withheld != nil:
		return s.refuse(ctx, r, receipts.RefusedHeldOf(mbx, o.Withheld), contracts.ErrNotFound)
	case errors.Is(err, contracts.ErrNotFound) || err == nil:
		return s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedOutsideScope, Mailbox: mbx, Message: msg}, contracts.ErrNotFound)
	case errors.Is(err, contracts.ErrAttachmentTooLarge):
		return s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedTooLarge, Mailbox: mbx, Message: msg}, err)
	case errors.Is(err, contracts.ErrPartNotApproved):
		return s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedPartNotGiven, Mailbox: mbx, Message: msg}, err)
	case errors.Is(err, contracts.ErrModelUnavailable):
		return s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedModelUnavailable, Mailbox: mbx, Message: msg}, err)
	}
	return s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedUnavailable, Mailbox: mbx, Message: msg}, err)
}

// open opens the mailbox of a read, as not found when it is no longer there.
func (s *Service) open(ctx context.Context, r *reading, mbx, msg string) (inbox.Box, error) {
	box, err := s.cfg.Mailboxes.OpenForConnection(ctx, r.g.Account, mbx)
	if err != nil {
		return box, s.refuseRead(ctx, r, mbx, msg, inbox.Outcome{}, err)
	}
	return box, nil
}

// Read implements API.
func (s *Service) Read(ctx context.Context, g *oauth.Grant, id string) (*contracts.VendorMessage, Release, error) {
	r, err := s.prepare(ctx, g, nil)
	if err != nil {
		return nil, nil, err
	}
	mbx, msg, ok := message(r, id)
	if !ok {
		return nil, nil, s.refuseRead(ctx, r, "", "", inbox.Outcome{}, nil)
	}
	if !s.limits.allow("read "+g.Connection, ReadsPerMinute, time.Minute, s.now()) {
		s.count("rate_limited")
		return nil, nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedRateLimited, Mailbox: mbx, Message: msg}, contracts.ErrTooManyRequests)
	}
	box, err := s.open(ctx, r, mbx, msg)
	if err != nil {
		return nil, nil, err
	}
	o, err := s.cfg.Discloser.Message(ctx, box, r.d, msg)
	if err != nil || o.Disclosed == nil {
		return nil, nil, s.refuseRead(ctx, r, mbx, msg, o, err)
	}
	rc := s.receipt(r, receipts.KindRead)
	rc.Delivered = []receipts.Delivered{receipts.DeliveredOf(mbx, o.Disclosed, r.d.Parts)}
	rel, err := s.release(ctx, r, rc)
	if err != nil {
		return nil, nil, err
	}
	m := vendorMessage(mbx, o.Disclosed, r.d.Parts, true)
	return &m, rel, nil
}

// Attachment implements API.
func (s *Service) Attachment(ctx context.Context, g *oauth.Grant, id string, index int) (*inbox.Attachment, Release, error) {
	r, err := s.prepare(ctx, g, nil)
	if err != nil {
		return nil, nil, err
	}
	mbx, msg, ok := message(r, id)
	if !slices.Contains(r.d.Parts, contracts.PartAttachments) {
		return nil, nil, s.refuseRead(ctx, r, mbx, msg, inbox.Outcome{}, contracts.ErrPartNotApproved)
	}
	if !ok || index < 0 {
		return nil, nil, s.refuseRead(ctx, r, "", "", inbox.Outcome{}, nil)
	}
	if !s.limits.allow("read "+g.Connection, ReadsPerMinute, time.Minute, s.now()) {
		s.count("rate_limited")
		return nil, nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedRateLimited, Mailbox: mbx, Message: msg}, contracts.ErrTooManyRequests)
	}
	box, err := s.open(ctx, r, mbx, msg)
	if err != nil {
		return nil, nil, err
	}
	o, att, err := s.cfg.Discloser.Attachment(ctx, box, r.d, msg, index)
	if err != nil || o.Disclosed == nil || att == nil {
		return nil, nil, s.refuseRead(ctx, r, mbx, msg, o, err)
	}
	if len(att.Data) > contracts.VendorAttachmentMax {
		return nil, nil, s.refuseRead(ctx, r, mbx, msg, o, contracts.ErrAttachmentTooLarge)
	}
	if !s.limits.allowN("bytes "+g.Connection, len(att.Data), AttachmentBytesPerHour, time.Hour, s.now()) {
		s.count("rate_limited")
		return nil, nil, s.refuse(ctx, r, &receipts.Refused{Reason: receipts.RefusedRateLimited, Mailbox: mbx, Message: msg}, contracts.ErrTooManyRequests)
	}
	rc := s.receipt(r, receipts.KindAttachment)
	dl := receipts.DeliveredOf(mbx, o.Disclosed, r.d.Parts)
	dl.Attachment = &index
	rc.Delivered = []receipts.Delivered{dl}
	rel, err := s.release(ctx, r, rc)
	if err != nil {
		return nil, nil, err
	}
	return att, rel, nil
}

// Refuse implements API: the refusal an adapter made of the attributed
// request of g, recorded as the service records its own (refuseWith)
// before the adapter answers: under a fresh ReceiptWriteMax, detached from
// the request's cancellation, and not bound by the grant's hard end, which
// bounds only what releases mail (ruling 6 on PR #136's second review). Its
// message is recorded only when it names one of the connection's
// mailboxes, as Read and Attachment check it (message); anything else of
// what the vendor sent is not. ErrInvalidRefusal for no grant or a refusal
// that is not ValidOn its resource, with nothing recorded; ErrUnavailable
// when the connection could not be read or the receipt not written.
func (s *Service) Refuse(ctx context.Context, g *oauth.Grant, rf Refusal) error {
	if g == nil || !rf.ValidOn(g.Resource) {
		return ErrInvalidRefusal
	}
	ctx = context.WithoutCancel(ctx)
	r := &reading{g: g, conn: &connDoc{}}
	if rf.Message != "" {
		rctx, cancel := context.WithTimeout(ctx, ReceiptWriteMax)
		d, _, err := s.connFresh(rctx, g.Connection)
		cancel()
		switch {
		case err == nil && d != nil && d.Account == g.Account:
			r.conn = d
		case err != nil && !refusedRecord(err):
			return ErrUnavailable
		}
	}
	why := &receipts.Refused{Reason: rf.Reason}
	if mbx, msg, ok := message(r, rf.Message); ok {
		why.Mailbox, why.Message = mbx, msg
	}
	rc := s.receipt(r, receipts.KindRefused)
	rc.Refused = why
	if err := s.refuseWith(ctx, r, rc, nil); err != nil {
		return err
	}
	s.count(string(rf.Reason))
	return nil
}
