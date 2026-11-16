package connections

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The consent screen's three calls (ADR 0030, "The hand-off", "Asking
// again", "Making one", "Adding a device").

// ErrInvalidBody refuses a draft or a decision that is not one the request
// takes: an empty or unknown mailbox, a mail since the request does not
// allow, a mailbox not the draft's, a decision without its draft or
// snapshot. The routes answer it 400 invalid_body.
var ErrInvalidBody = &contracts.Error{Code: "invalid_body", Message: "that is not a choice this request takes"}

// clientRefusal is contracts.ErrInvalidClient with its fixed reason, for the
// app to show; never redirected.
type clientRefusal struct{ Problem contracts.ClientProblem }

func (e *clientRefusal) Error() string { return contracts.ErrInvalidClient.Error() }
func (e *clientRefusal) Unwrap() error { return contracts.ErrInvalidClient }

// ClientProblemOf is the reason of a contracts.ErrInvalidClient the service
// answered, for the details of its answer ("" for none).
func ClientProblemOf(err error) contracts.ClientProblem {
	var r *clientRefusal
	if errors.As(err, &r) {
		return r.Problem
	}
	return ""
}

// requestEntry is an authorization request waiting for the person, sealed.
type requestEntry struct {
	ClientID    string              `json:"client_id"`
	ClientName  string              `json:"client_name"`
	Vendor      string              `json:"vendor"`
	RedirectURI string              `json:"redirect_uri"`
	State       string              `json:"state"`
	Challenge   string              `json:"challenge"`
	Resource    contracts.Resource  `json:"resource"`
	ResourceURI string              `json:"resource_uri"`
	Scopes      []string            `json:"scopes"`
	Parts       []contracts.Part    `json:"parts"`
	Query       *contracts.Formula  `json:"query,omitempty"`
	Since       contracts.MailSince `json:"since"`
	Days        int                 `json:"days"`
	Session     string              `json:"session"`
	Account     string              `json:"account"`
	Mailboxes   []string            `json:"mailboxes"`
	Expires     time.Time           `json:"expires"`
}

// pushedEntry is a pushed request, sealed, bound to its client.
type pushedEntry struct {
	ClientID string                `json:"client_id"`
	Params   oauth.AuthorizeParams `json:"params"`
}

// codeEntry is a code's sealed entry: what checking an exchange needs.
type codeEntry struct {
	Connection  string             `json:"connection"`
	Family      string             `json:"family"`
	Account     string             `json:"account"`
	ClientID    string             `json:"client_id"`
	RedirectURI string             `json:"redirect_uri"`
	Challenge   string             `json:"challenge"`
	Resource    contracts.Resource `json:"resource"`
	ResourceURI string             `json:"resource_uri"`
	Scopes      []string           `json:"scopes"`
	ScopeID     string             `json:"scope_id,omitempty"`
	Scope       []byte             `json:"scope,omitempty"`
}

// resolve is a client by its client_id, as the person is told of a failure.
func (s *Service) resolve(ctx context.Context, clientID string) (*vendors.Client, error) {
	c, err := s.cfg.Vendors.Client(ctx, clientID)
	switch {
	case errors.Is(err, vendors.ErrClientID):
		s.count(string(contracts.ClientIDInvalid))
		return nil, &clientRefusal{contracts.ClientIDInvalid}
	case errors.Is(err, vendors.ErrDocument):
		s.count(string(contracts.ClientDocumentInvalid))
		return nil, &clientRefusal{contracts.ClientDocumentInvalid}
	case err != nil:
		s.count("vendor_unreachable")
		return nil, contracts.ErrVendorUnreachable
	}
	return c, nil
}

// refusedRedirect is a refusal sent back to the vendor.
func (s *Service) refusedRedirect(p *oauth.AuthorizeParams, code oauth.ErrorCode) *contracts.AuthorizationStarted {
	s.count(string(code))
	v := url.Values{"error": {string(code)}}
	if p.State != "" {
		v.Set("state", p.State)
	}
	return &contracts.AuthorizationStarted{Redirect: redirectWith(p.RedirectURI, v, s.cfg.OAuth.Issuer)}
}

// Start implements API.
func (s *Service) Start(ctx context.Context, p Person, raw string) (*contracts.AuthorizationStarted, error) {
	q, err := url.ParseQuery(raw)
	if err != nil || len(q["client_id"]) != 1 {
		s.count(string(contracts.ClientIDInvalid))
		return nil, &clientRefusal{contracts.ClientIDInvalid}
	}
	clientID := q.Get("client_id")
	client, err := s.resolve(ctx, clientID)
	if err != nil {
		return nil, err
	}
	var params *oauth.AuthorizeParams
	if uri, ok := q["request_uri"]; ok {
		if params, err = s.takePushed(ctx, clientID, uri); err != nil {
			return nil, err
		}
		if !redirectAllowed(client.Document.RedirectURIs, params.RedirectURI) {
			return nil, &clientRefusal{contracts.RedirectURIInvalid}
		}
		if len(q) != 2 {
			return s.refusedRedirect(params, oauth.ErrInvalidRequest), nil
		}
	} else {
		var proven bool
		params, proven, err = authorizeParams(q, &client.Document, s.cfg.OAuth)
		if !proven {
			s.count(string(contracts.RedirectURIInvalid))
			return nil, err
		}
		var oe *OAuthError
		if errors.As(err, &oe) {
			return s.refusedRedirect(params, oe.Code), nil
		}
	}
	if err := s.openForApproval(ctx, p.Account); err != nil {
		return nil, err
	}
	catalogue, err := s.cfg.Mailboxes.Catalogue(ctx, p.Account)
	if err != nil {
		return nil, err
	}
	now := s.now()
	req := &requestEntry{
		ClientID: clientID, ClientName: clientName(client), Vendor: client.Domain, RedirectURI: params.RedirectURI,
		State: params.State, Challenge: params.CodeChallenge, ResourceURI: params.Resource, Scopes: params.Scope,
		Parts: partsOf(params.Scope), Session: p.Session, Account: p.Account, Mailboxes: slices.Clone(catalogue),
		Days: oauth.ConnectionDefaultDays, Since: contracts.MailSince{New: true}, Expires: now.Add(oauth.RequestTTL),
	}
	req.Resource, _ = resourceOf(s.cfg.OAuth, params.Resource)
	if params.EndsInDays > 0 {
		req.Days = params.EndsInDays
	}
	if params.Since != "" && params.Since != "new" {
		at, _ := time.Parse(time.RFC3339, params.Since)
		if !at.Before(now) {
			return s.refusedRedirect(params, oauth.ErrInvalidRequest), nil
		}
		req.Since = contracts.MailSince{At: at.UTC()}
	}
	if params.Query != nil {
		c, err := query.Parse(params.Query, catalogue)
		if err != nil {
			return s.refusedRedirect(params, oauth.ErrInvalidRequest), nil
		}
		req.Query = &c.Formula
	}
	if err := s.requestFits(req); err != nil {
		s.count("request_too_large")
		return nil, err
	}
	id, err := newSecret(oauth.PrefixRequest, s.cfg.Random)
	if err != nil {
		return nil, err
	}
	if err := s.putEntry(ctx, PartitionRequest+hashOf(oauth.KindRequest, id), req, req.Expires); err != nil {
		return nil, err
	}
	out, err := s.screen(ctx, id, req)
	if err != nil {
		return nil, err
	}
	return &contracts.AuthorizationStarted{Request: out}, nil
}

// clientName is what the document calls the client, cut, or its domain.
func clientName(c *vendors.Client) string {
	name := []rune(strings.TrimSpace(c.Document.ClientName))
	if len(name) == 0 {
		return c.Domain
	}
	if len(name) > oauth.ClientNameMaxRunes {
		name = name[:oauth.ClientNameMaxRunes]
	}
	return string(name)
}

// takePushed is the pushed request a request_uri names, ended once.
func (s *Service) takePushed(ctx context.Context, clientID string, uri []string) (*oauth.AuthorizeParams, error) {
	if len(uri) != 1 || !strings.HasPrefix(uri[0], oauth.RequestURIPrefix+oauth.PrefixPushed) {
		return nil, contracts.ErrRequestEnded
	}
	partition := PartitionPushed + hashOf(oauth.KindPushed, strings.TrimPrefix(uri[0], oauth.RequestURIPrefix))
	var e pushedEntry
	if err := s.getEntry(ctx, partition, &e); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, contracts.ErrRequestEnded
		}
		return nil, err
	}
	if e.ClientID != clientID {
		return nil, contracts.ErrRequestEnded
	}
	first, err := s.endOnce(ctx, partition, s.now().Add(oauth.PushedRequestTTL))
	if err != nil {
		return nil, err
	}
	if !first {
		return nil, contracts.ErrRequestEnded
	}
	return &e.Params, nil
}

// requestFits checks that a connection made from req could hold every
// device it may have, each at its largest (N6), within
// ConnectionRecordBytesMax.
func (s *Service) requestFits(req *requestEntry) error {
	d := &connDoc{
		V: 1, ID: "con_" + strings.Repeat("a", 26), Account: req.Account, Vendor: req.Vendor, ClientID: req.ClientID,
		ClientName: req.ClientName, Resource: req.Resource, Parts: req.Parts, Scopes: req.Scopes, Mailboxes: req.Mailboxes,
		State: contracts.ConnectionActive, ModelNotice: s.cfg.ModelNotice, EndReason: contracts.EndedByReplaced,
		ReplacedBy: "con_" + strings.Repeat("a", 26),
		private:    &connPrivate{Query: req.Query, Since: req.Since, Shown: s.shown(req)},
	}
	raw, err := jsonSize(d.private)
	if err != nil {
		return err
	}
	// The private part as sealed and signed: a third more, and the seal's own
	// bytes.
	d.Sealed = make([]byte, raw+64)
	base, err := jsonSize(d)
	if err != nil {
		return err
	}
	if base+(oauth.FamiliesMax+oauth.DevicesEndedKept)*(oauth.DeviceEntryBytesMax+1) > ConnectionRecordBytesMax {
		return contracts.ErrRequestTooLarge
	}
	return nil
}

// shown is what the screen shows of a request, kept with the consent.
func (s *Service) shown(req *requestEntry) consentShown {
	host, _ := redirectHost(req.RedirectURI)
	sh := consentShown{Rules: s.ruleRefs(), RedirectHost: host, Sentences: []string{}}
	if req.Query != nil {
		if c, err := query.Canonicalise(*req.Query); err == nil {
			sh.Sentences = append(sh.Sentences, check(c, req.Mailboxes).Sentence.Text)
		}
	}
	for _, r := range s.ruleSummaries() {
		for _, sen := range r.Sentence {
			sh.Sentences = append(sh.Sentences, sen.Text)
		}
	}
	return sh
}

// request is a request waiting for the person, if it is this session's.
func (s *Service) request(ctx context.Context, p Person, id string) (*requestEntry, error) {
	if !strings.HasPrefix(id, oauth.PrefixRequest) || len(id) > 64 {
		return nil, contracts.ErrRequestEnded
	}
	var req requestEntry
	if err := s.getEntry(ctx, PartitionRequest+hashOf(oauth.KindRequest, id), &req); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, contracts.ErrRequestEnded
		}
		return nil, err
	}
	if req.Session != p.Session || req.Account != p.Account || !s.now().Before(req.Expires) {
		return nil, contracts.ErrRequestEnded
	}
	return &req, nil
}

// current is the client's current connection in the account's list: its
// identifier as the list names it (raw), and the connection when it is
// active and before its end, with its record's mark.
func (s *Service) current(ctx context.Context, account, client string) (raw string, cur *connDoc, at store.Mark, err error) {
	l, _, err := s.readList(ctx, account)
	if err != nil || l == nil {
		return "", nil, at, err
	}
	raw = l.Current[client]
	if raw == "" {
		return "", nil, at, nil
	}
	d, at, err := s.readConn(ctx, raw)
	if err != nil && !refusedRecord(err) {
		return raw, nil, at, err
	}
	if !d.live(s.now()) {
		return raw, nil, at, nil
	}
	return raw, d, at, nil
}

// screen is the request as the consent screen shows it, with the starting
// draft and what approving it does.
func (s *Service) screen(ctx context.Context, id string, req *requestEntry) (*contracts.AuthorizationRequest, error) {
	host, loopback := redirectHost(req.RedirectURI)
	out := &contracts.AuthorizationRequest{
		ID: id, Vendor: vendorOf(req.Vendor), ClientName: req.ClientName, RedirectHost: host, Loopback: loopback,
		Resource: req.Resource, Parts: slices.Clone(req.Parts), Since: req.Since, EndsInDays: req.Days,
		Mailboxes: slices.Clone(req.Mailboxes), Rules: s.ruleSummaries(), ModelNotice: s.cfg.ModelNotice, ExpiresAt: req.Expires,
	}
	if req.Query != nil {
		c, err := query.Canonicalise(*req.Query)
		if err != nil {
			return nil, err
		}
		fc := check(c, req.Mailboxes)
		out.Query = &fc
	}
	out.Draft = contracts.ConsentDraft{Mailboxes: slices.Clone(req.Mailboxes), Since: req.Since}
	_, cur, _, err := s.current(ctx, req.Account, req.ClientID)
	if err != nil {
		return nil, err
	}
	if cur != nil && req.Query == nil {
		// Asking again starts from what the person had approved, so it never
		// quietly narrows or widens it.
		var mbx []string
		for _, m := range cur.Mailboxes {
			if slices.Contains(req.Mailboxes, m) {
				mbx = append(mbx, m)
			}
		}
		if len(mbx) > 0 {
			out.Draft.Mailboxes = mbx
		}
		out.Draft.Since = cur.private.Since
	}
	if len(out.Draft.Mailboxes) == 0 {
		return out, nil // nothing to approve until the account has a mailbox
	}
	snap, cur, at, _, err := s.changeFor(ctx, req, out.Draft)
	if err != nil {
		return nil, err
	}
	out.Change, out.Snapshot = snap.Change, snap
	if cur != nil {
		v := s.view(ctx, cur, at.Version())
		out.Connection = &v
	}
	return out, nil
}

// validDraft checks a draft against its request and the account.
func validDraft(req *requestEntry, d contracts.ConsentDraft, now time.Time) error {
	if len(d.Mailboxes) == 0 || len(d.Mailboxes) > contracts.MaxGroupConditions {
		return ErrInvalidBody
	}
	seen := map[string]bool{}
	for _, m := range d.Mailboxes {
		if seen[m] || !slices.Contains(req.Mailboxes, m) {
			return ErrInvalidBody
		}
		seen[m] = true
	}
	if d.Since.New && !d.Since.At.IsZero() || d.Since.At.After(now) {
		return ErrInvalidBody
	}
	if req.Query != nil {
		if len(d.Mailboxes) != len(req.Mailboxes) {
			return ErrInvalidBody
		}
		switch {
		case d.Since.New:
		case req.Since.At.IsZero() || !d.Since.At.Equal(req.Since.At):
			return ErrInvalidBody
		}
	}
	return nil
}

// draftID is the canonical identifier of a draft's formulas with the
// request's (the mail since as asked: new mail only is the approval's
// instant, which a draft does not know yet).
func draftID(req *requestEntry, d contracts.ConsentDraft) (string, error) {
	sc, err := makeScope(req.Query, d.Mailboxes, contracts.MailSince{}, time.Time{})
	if err != nil {
		return "", err
	}
	parts := []string{string(req.Resource), strings.Join(req.Scopes, " "), sc.Mailboxes.ID}
	if sc.Query != nil {
		parts = append(parts, sc.Query.ID)
	}
	switch {
	case d.Since.New:
		parts = append(parts, "new")
	case !d.Since.At.IsZero():
		parts = append(parts, d.Since.At.UTC().Format(time.RFC3339Nano))
	default:
		parts = append(parts, "all")
	}
	h := sha256.Sum256([]byte("permaily consent draft v1\x00" + strings.Join(parts, "\x00")))
	return "drf_" + idEncoding.EncodeToString(h[:16]), nil
}

// changeFor is what approving draft does now (ADR 0030, "Asking again"),
// as its snapshot, the client's connection it would change and the draft's
// formulas.
func (s *Service) changeFor(ctx context.Context, req *requestEntry, d contracts.ConsentDraft) (contracts.ConsentSnapshot, *connDoc, store.Mark, scopeOf, error) {
	now := s.now()
	did, err := draftID(req, d)
	if err != nil {
		return contracts.ConsentSnapshot{}, nil, store.Mark{}, scopeOf{}, ErrInvalidBody
	}
	ds, err := makeScope(req.Query, d.Mailboxes, d.Since, now)
	if err != nil {
		return contracts.ConsentSnapshot{}, nil, store.Mark{}, scopeOf{}, ErrInvalidBody
	}
	_, cur, at, err := s.current(ctx, req.Account, req.ClientID)
	if err != nil {
		return contracts.ConsentSnapshot{}, nil, store.Mark{}, scopeOf{}, err
	}
	snap := contracts.ConsentSnapshot{Change: contracts.ChangeNew, EndsAt: endsAt(now, req.Days), DraftID: did}
	if cur == nil {
		return snap, nil, at, ds, nil
	}
	snap.Connection, snap.Version, snap.Change = cur.ID, at.Version(), contracts.ChangeReplace
	cs, err := cur.scope()
	if err == nil && cur.Resource == req.Resource && subset(cur.Parts, req.Parts) && contained(cs, ds) {
		snap.Change, snap.EndsAt = contracts.ChangeAddDevice, cur.EndsAt
	}
	return snap, cur, at, ds, nil
}

func endsAt(now time.Time, days int) time.Time {
	return now.Add(time.Duration(days) * 24 * time.Hour).UTC().Truncate(time.Second)
}

// sameSnapshot says the commit does what the screen showed (N5): the same
// connection at the same version, the same change and draft, and no end
// later than a minute past the one shown.
func sameSnapshot(shown, now contracts.ConsentSnapshot) bool {
	if shown.Connection != now.Connection || shown.Version != now.Version || shown.Change != now.Change || shown.DraftID != now.DraftID {
		return false
	}
	if now.Change == contracts.ChangeAddDevice {
		return now.EndsAt.Equal(shown.EndsAt)
	}
	return !now.EndsAt.After(shown.EndsAt.Add(time.Minute))
}

// Preview implements API.
func (s *Service) Preview(ctx context.Context, p Person, id string, in contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error) {
	req, err := s.request(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err := validDraft(req, in.Draft, s.now()); err != nil || !slices.Contains(in.Draft.Mailboxes, in.Mailbox) {
		return nil, ErrInvalidBody
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > inbox.DiscloseMaxLimit {
		return nil, contracts.ErrInvalidLimit
	}
	snap, _, _, ds, err := s.changeFor(ctx, req, in.Draft)
	if err != nil {
		return nil, err
	}
	if in.After != "" && in.DraftID != snap.DraftID {
		return nil, contracts.ErrInvalidCursor
	}
	box, err := s.cfg.Mailboxes.OpenForConnection(ctx, p.Account, in.Mailbox)
	if err != nil {
		return nil, err
	}
	d := inbox.Disclosure{Formulas: ds.list(), Parts: req.Parts, Account: p.Account}
	if in.AskModel {
		d.Rules = s.cfg.Rules
		d.Model = s.cfg.Model != nil && s.cfg.Model.Gate(ctx, p.Account) == inbox.AskAnswered
	}
	page, err := s.cfg.Discloser.Page(ctx, box, d, in.After, limit)
	if err != nil {
		return nil, err
	}
	out := &contracts.ConsentPreviewPage{DraftID: snap.DraftID, Change: snap.Change, Snapshot: snap, Page: contracts.MatchPage{
		Messages: []contracts.MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}, Next: page.Next,
	}}
	for _, it := range page.Items {
		switch {
		case it.Disclosed != nil:
			m := it.Disclosed.Message
			sum := contracts.MessageSummary{ID: it.Disclosed.ID, ReceivedAt: m.ReceivedAt, From: m.From, Attachments: len(m.Attachments)}
			if m.Subject != nil {
				sum.Subject = *m.Subject
			}
			out.Page.Messages = append(out.Page.Messages, sum)
		case it.Withheld != nil && in.AskModel:
			out.Page.Held = append(out.Page.Held, it.Withheld.ID)
		}
	}
	if in.AskModel {
		out.Page.Model = page.Model
	}
	return out, nil
}

// Decide implements API.
func (s *Service) Decide(ctx context.Context, p Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error) {
	req, err := s.request(ctx, p, id)
	if err != nil {
		return nil, err
	}
	reqHash := hashOf(oauth.KindRequest, id)
	partition := PartitionRequest + reqHash
	if s.usedHere(partition) || s.decided(ctx, req.Account, reqHash) {
		return nil, contracts.ErrRequestEnded
	}
	if !in.Allow {
		return s.deny(ctx, req, reqHash, partition)
	}
	if !s.now().Before(p.ConfirmedUntil) {
		return nil, contracts.ErrConfirmationNeeded
	}
	if in.ModelNotice != s.cfg.ModelNotice {
		s.count("model_notice_changed")
		return nil, contracts.ErrModelNoticeChanged
	}
	if in.Draft == nil || in.Snapshot == nil {
		return nil, ErrInvalidBody
	}
	if err := s.openForApproval(ctx, p.Account); err != nil {
		return nil, err
	}
	if err := validDraft(req, *in.Draft, s.now()); err != nil {
		return nil, err
	}
	// The document is read again: the redirect URI must still be one of it.
	client, err := s.resolve(ctx, req.ClientID)
	if err != nil {
		return nil, err
	}
	if !redirectAllowed(client.Document.RedirectURIs, req.RedirectURI) {
		return nil, &clientRefusal{contracts.RedirectURIInvalid}
	}
	snap, cur, _, ds, err := s.changeFor(ctx, req, *in.Draft)
	if err != nil {
		return nil, err
	}
	// The proof must still be fresh now, after the document and the storage
	// it waited for (choice 15); it is checked again by the commit's writes'
	// own order, which follow at once.
	if !s.now().Before(p.ConfirmedUntil) {
		return nil, contracts.ErrConfirmationNeeded
	}
	if !sameSnapshot(*in.Snapshot, snap) {
		s.count("consent_changed")
		return nil, contracts.ErrConsentChanged
	}
	var made *made
	if snap.Change == contracts.ChangeAddDevice {
		made, err = s.addDevice(ctx, req, reqHash, cur, snap, ds)
	} else {
		made, err = s.makeConnection(ctx, req, reqHash, snap, ds, *in.Draft)
	}
	if err != nil {
		if errors.Is(err, contracts.ErrConsentChanged) {
			s.count("consent_changed")
		}
		return nil, err
	}
	_, _ = s.endOnce(ctx, partition, req.Expires)
	if err := s.consentReceipt(ctx, req, made, *in.Draft, ds, true); err != nil {
		return nil, err
	}
	code, err := s.issueCode(ctx, req, made)
	if err != nil {
		return nil, err
	}
	v := url.Values{"code": {code}}
	if req.State != "" {
		v.Set("state", req.State)
	}
	s.count("allowed")
	return &contracts.AuthorizationAnswer{Redirect: redirectWith(req.RedirectURI, v, s.cfg.OAuth.Issuer)}, nil
}

// decided says the account's list has the request decided (a request is
// decided once, also when its mark was deleted); the list's own write
// decides it again at the commit.
func (s *Service) decided(ctx context.Context, account, reqHash string) bool {
	l, _, err := s.readList(ctx, account)
	return err == nil && l != nil && slices.ContainsFunc(l.Decided, func(d ListDecision) bool { return d.Request == reqHash })
}

// deny records the request as decided and sends the vendor access_denied.
func (s *Service) deny(ctx context.Context, req *requestEntry, reqHash, partition string) (*contracts.AuthorizationAnswer, error) {
	now := s.now()
	if _, err := s.updateList(ctx, req.Account, func(l *ListState) error { return l.Decide(reqHash, now) }); err != nil {
		return nil, err
	}
	_, _ = s.endOnce(ctx, partition, req.Expires)
	ds, _ := makeScope(req.Query, req.Mailboxes, req.Since, now)
	_ = s.consentReceipt(ctx, req, nil, contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: req.Since}, ds, false)
	v := url.Values{"error": {string(oauth.ErrAccessDenied)}}
	if req.State != "" {
		v.Set("state", req.State)
	}
	s.count("denied")
	return &contracts.AuthorizationAnswer{Redirect: redirectWith(req.RedirectURI, v, s.cfg.OAuth.Issuer)}, nil
}

// made is what an approval made: the connection, its new device and its
// code's state, and what it replaced.
type made struct {
	conn     *connDoc
	device   string
	code     string
	scopeID  string
	scope    []*query.Canonical
	change   contracts.ConnectionChange
	replaced string
}

// errRaced is the client's current connection changed since the list was
// read: decided again.
var errRaced = errors.New("connections: the client's current connection changed")

// makeConnection makes a connection for the request, replacing the
// client's (ADR 0030, "Making one").
func (s *Service) makeConnection(ctx context.Context, req *requestEntry, reqHash string, snap contracts.ConsentSnapshot, ds scopeOf, draft contracts.ConsentDraft) (*made, error) {
	now := s.now()
	id, err := s.newID("con")
	if err != nil {
		return nil, err
	}
	family, err := s.newID("fam")
	if err != nil {
		return nil, err
	}
	code, err := newSecret(oauth.PrefixCode, s.cfg.Random)
	if err != nil {
		return nil, err
	}
	var curRaw string
	var generation uint64
	if _, err := s.updateList(ctx, req.Account, func(l *ListState) error {
		curRaw = l.Current[req.ClientID]
		if cur := snap.Connection; cur != "" && curRaw != cur {
			return contracts.ErrConsentChanged
		}
		if err := l.Decide(reqHash, now); err != nil {
			return err
		}
		generation = l.Generation
		return l.AddPending(id, req.ClientID, now)
	}); err != nil {
		return nil, err
	}
	since := draft.Since
	doc := &connDoc{
		V: 1, ID: id, Account: req.Account, Vendor: req.Vendor, ClientID: req.ClientID, ClientName: req.ClientName,
		Resource: req.Resource, Parts: slices.Clone(req.Parts), Scopes: slices.Clone(req.Scopes), Mailboxes: slices.Clone(draft.Mailboxes),
		State: contracts.ConnectionActive, MadeAt: now, Days: req.Days, EndsAt: snap.EndsAt, ModelNotice: s.cfg.ModelNotice,
		Generation: generation,
		Devices: []deviceDoc{{
			ID: family, State: contracts.ConnectionActive, MadeAt: now, Parts: slices.Clone(req.Parts), Scopes: slices.Clone(req.Scopes),
			Code: oauth.CodeState{Hash: hashOf(oauth.KindCode, code), Family: family, ExpiresAt: now.Add(oauth.CodeTTL), Status: oauth.CodeIssued},
		}},
		private: &connPrivate{Query: req.Query, Since: since, Shown: s.shown(req)},
	}
	if err := s.writeConn(ctx, doc, store.Mark{}, true); err != nil {
		return nil, err
	}
	replaced := curRaw
	for range RecordTries {
		if curRaw != "" {
			if err := s.endConnection(ctx, curRaw, contracts.EndedByReplaced, id, true); err != nil {
				return nil, err
			}
		}
		_, err := s.updateList(ctx, req.Account, func(l *ListState) error {
			if l.Closing {
				return contracts.ErrAccountClosing
			}
			if l.Current[req.ClientID] != curRaw {
				curRaw = l.Current[req.ClientID]
				return errRaced
			}
			if curRaw != "" {
				s.dropEnded(ctx, l, l.End(curRaw, now))
			}
			for i := range l.Members {
				if l.Members[i].ID == id {
					l.Members[i].Pending = false
				}
			}
			l.Current[req.ClientID] = id
			return nil
		})
		switch {
		case errors.Is(err, errRaced):
			continue
		case errors.Is(err, contracts.ErrAccountClosing):
			// The account began closing meanwhile: what was made is ended,
			// and no code is issued.
			_ = s.endConnection(ctx, id, contracts.EndedByAccount, "", true)
			_, _ = s.updateList(ctx, req.Account, func(l *ListState) error {
				s.dropEnded(ctx, l, l.End(id, now))
				return nil
			})
			return nil, err
		case err != nil:
			return nil, err
		}
		change := contracts.ChangeNew
		if replaced != "" {
			change = contracts.ChangeReplace
		}
		return &made{conn: doc, device: family, code: code, change: change, replaced: replaced}, nil
	}
	return nil, errBusy
}

// addDevice adds the request to the client's connection as another device
// (ADR 0030, "Adding a device").
func (s *Service) addDevice(ctx context.Context, req *requestEntry, reqHash string, cur *connDoc, snap contracts.ConsentSnapshot, ds scopeOf) (*made, error) {
	now := s.now()
	d, at, err := s.readConn(ctx, cur.ID)
	if err != nil || at.Version() != snap.Version || !d.live(now) {
		if err != nil && !refusedRecord(err) {
			return nil, err
		}
		return nil, contracts.ErrConsentChanged
	}
	drop := d.tidy(now)
	if d.activeDevices() >= oauth.FamiliesMax {
		s.count("too_many_devices")
		return nil, contracts.ErrTooManyDevices
	}
	if _, err := s.updateList(ctx, req.Account, func(l *ListState) error {
		if l.Current[req.ClientID] != cur.ID {
			return contracts.ErrConsentChanged
		}
		if l.Closing {
			return contracts.ErrAccountClosing
		}
		return l.Decide(reqHash, now)
	}); err != nil {
		return nil, err
	}
	family, err := s.newID("fam")
	if err != nil {
		return nil, err
	}
	code, err := newSecret(oauth.PrefixCode, s.cfg.Random)
	if err != nil {
		return nil, err
	}
	cs, err := d.scope()
	if err != nil {
		return nil, err
	}
	own, sid := narrower(cs, ds)
	for _, f := range drop {
		if err := s.removeFamily(ctx, f); err != nil {
			return nil, err
		}
	}
	d.dropDevices(drop)
	d.Devices = append(d.Devices, deviceDoc{
		ID: family, State: contracts.ConnectionActive, MadeAt: now, Parts: slices.Clone(req.Parts), Scopes: slices.Clone(req.Scopes), ScopeID: sid,
		Code: oauth.CodeState{Hash: hashOf(oauth.KindCode, code), Family: family, ExpiresAt: now.Add(oauth.CodeTTL), Status: oauth.CodeIssued},
	})
	if err := s.writeConn(ctx, d, at, false); err != nil {
		if retry(err) {
			return nil, contracts.ErrConsentChanged
		}
		return nil, err
	}
	s.keepScope(sid, own)
	return &made{conn: d, device: family, code: code, scopeID: sid, scope: own, change: contracts.ChangeAddDevice}, nil
}

// keepScope remembers a device's narrower formulas by their identifier.
func (s *Service) keepScope(id string, fs []*query.Canonical) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.scopes) >= cacheMax {
		clear(s.scopes)
	}
	s.scopes[id] = fs
}

// issueCode writes the code's entry: from then on the vendor may exchange
// it.
func (s *Service) issueCode(ctx context.Context, req *requestEntry, m *made) (string, error) {
	e := codeEntry{
		Connection: m.conn.ID, Family: m.device, Account: req.Account, ClientID: req.ClientID, RedirectURI: req.RedirectURI,
		Challenge: req.Challenge, Resource: req.Resource, ResourceURI: req.ResourceURI, Scopes: req.Scopes,
		ScopeID: m.scopeID, Scope: encodeScope(m.scope),
	}
	if err := s.putEntry(ctx, PartitionCode+hashOf(oauth.KindCode, m.code), e, s.now().Add(oauth.CodeTTL)); err != nil {
		return "", err
	}
	return m.code, nil
}

// consentReceipt records what the person was shown and decided.
func (s *Service) consentReceipt(ctx context.Context, req *requestEntry, m *made, d contracts.ConsentDraft, ds scopeOf, allowed bool) error {
	sh := s.shown(req)
	r := &receipts.Receipt{
		V: receipts.Version, Kind: receipts.KindConsent, ClientID: req.ClientID, Vendor: req.Vendor, Resource: req.Resource,
		Formulas: consentedFormulas(ds), Rules: s.ruleRefs(),
		Consent: &receipts.Consent{
			Allowed: allowed, Sentences: sh.Sentences, Parts: req.Parts, Since: d.Since, Mailboxes: d.Mailboxes, ModelNotice: s.cfg.ModelNotice,
		},
	}
	if m != nil {
		r.Connection, r.Device = m.conn.ID, m.device
		r.Consent.Change, r.Consent.EndsAt, r.Consent.Replaced = m.change, m.conn.EndsAt, m.replaced
	}
	return s.writeReceipt(ctx, req.Account, r, time.Time{})
}

// consentedFormulas are a scope's formulas in full, for a consent receipt.
func consentedFormulas(sc scopeOf) []receipts.Formula {
	var out []receipts.Formula
	add := func(role receipts.FormulaRole, c *query.Canonical) {
		if c != nil {
			f := c.Formula
			out = append(out, receipts.Formula{Role: role, ID: c.ID, Formula: &f})
		}
	}
	add(receipts.RoleConsented, sc.Query)
	add(receipts.RoleMailboxes, sc.Mailboxes)
	add(receipts.RoleSince, sc.Since)
	return out
}

// writeReceipt writes a receipt under ReceiptWriteMax and, when hard is
// set, the grant's hard end.
func (s *Service) writeReceipt(ctx context.Context, account string, r *receipts.Receipt, hard time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, ReceiptWriteMax)
	defer cancel()
	if !hard.IsZero() {
		var c2 context.CancelFunc
		ctx, c2 = context.WithDeadline(ctx, hard)
		defer c2()
	}
	return s.cfg.Receipts.Write(ctx, account, r)
}
