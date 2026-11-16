package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// A connection's record (ADR 0030, "Its record and the account's list"): a
// strict anchored record, readable and signed, with what the operator must
// not read sealed inside it.

const oauthFresh = oauth.ConnectionFresh

func randomBytes(b []byte) error {
	_, err := rand.Read(b)
	return err
}

// connDoc is a connection's record as stored.
type connDoc struct {
	V           int                       `json:"v"`
	ID          string                    `json:"id"`
	Account     string                    `json:"account"`
	Vendor      string                    `json:"vendor"`
	ClientID    string                    `json:"client_id"`
	ClientName  string                    `json:"client_name"`
	Resource    contracts.Resource        `json:"resource"`
	Parts       []contracts.Part          `json:"parts"`
	Scopes      []string                  `json:"scopes"`
	Mailboxes   []string                  `json:"mailboxes"`
	State       contracts.ConnectionState `json:"state"`
	MadeAt      time.Time                 `json:"made_at"`
	Days        int                       `json:"days"`
	EndsAt      time.Time                 `json:"ends_at"`
	EndedAt     time.Time                 `json:"ended_at,omitzero"`
	EndReason   contracts.EndReason       `json:"end_reason,omitempty"`
	ReplacedBy  string                    `json:"replaced_by,omitempty"`
	ModelNotice string                    `json:"model_notice"`
	// Generation is the list's closing generation the connection was made
	// in (ListMember.Generation).
	Generation uint64      `json:"generation"`
	Devices    []deviceDoc `json:"devices"`
	// Sealed is connPrivate, sealed.
	Sealed []byte `json:"sealed,omitempty"`

	private *connPrivate
}

// deviceDoc is one device of a connection: one token family.
type deviceDoc struct {
	ID        string                    `json:"id"`
	State     contracts.ConnectionState `json:"state"`
	MadeAt    time.Time                 `json:"made_at"`
	EndedAt   time.Time                 `json:"ended_at,omitzero"`
	EndReason contracts.EndReason       `json:"end_reason,omitempty"`
	Parts     []contracts.Part          `json:"parts"`
	Scopes    []string                  `json:"scopes"`
	// ScopeID is the canonical identifier of the device's own narrower
	// formulas, "" when its scope is the connection's (N6).
	ScopeID string          `json:"scope_id,omitempty"`
	Code    oauth.CodeState `json:"code"`
}

// connPrivate is what only the enclave reads of a connection: its query, its
// mail since and what the consent showed.
type connPrivate struct {
	Query *contracts.Formula  `json:"query,omitempty"`
	Since contracts.MailSince `json:"since"`
	Shown consentShown        `json:"shown"`
}

// consentShown is what the screen showed when the person allowed.
type consentShown struct {
	Sentences    []string    `json:"sentences"`
	Rules        []rules.Ref `json:"rules"`
	RedirectHost string      `json:"redirect_host"`
}

func connName(id string) string { return RecordConnection + id }

func privateAAD(id string) []byte { return envelope.AAD("connection private", id) }

// readConn reads a connection's record strictly and now: nil when it was
// never made or was removed.
func (s *Service) readConn(ctx context.Context, id string) (*connDoc, store.Mark, error) {
	d, at, _, err := s.readConnAt(ctx, id)
	return d, at, err
}

// readConnAt is readConn with the instant its read began: what it read is
// that old, however late the read returns (finding 2).
func (s *Service) readConnAt(ctx context.Context, id string) (*connDoc, store.Mark, time.Time, error) {
	began := s.now()
	var d connDoc
	at, found, err := s.readSigned(ctx, connName(id), &d)
	if err != nil || !found {
		s.cacheConn(id, nil, at, err, began)
		return nil, at, began, err
	}
	if d.ID != id || d.V != 1 {
		s.cacheConn(id, nil, at, errDocument, began)
		return nil, at, began, errDocument
	}
	if len(d.Sealed) > 0 {
		raw, err := envelope.Open(s.sealKey, d.Sealed, privateAAD(id))
		var p connPrivate
		if err != nil || json.Unmarshal(raw, &p) != nil {
			return nil, at, began, errDocument
		}
		d.private = &p
	} else {
		d.private = &connPrivate{}
	}
	// The cache keeps a copy of its own: what a caller changes before its
	// write commits is never what another reader sees (finding 3).
	s.cacheConn(id, d.clone(), at, nil, began)
	return &d, at, began, nil
}

// connFresh is a connection's record from this enclave's read of it if it
// is younger than ConnectionFresh, else read again.
func (s *Service) connFresh(ctx context.Context, id string) (*connDoc, time.Time, error) {
	s.mu.Lock()
	c, ok := s.conns[id]
	s.mu.Unlock()
	if ok && s.now().Sub(c.read) < oauthFresh && c.err == nil {
		return c.doc.clone(), c.read, nil
	}
	d, _, read, err := s.readConnAt(ctx, id)
	return d, read, err
}

// cacheConn keeps a read of a connection's record dated when it began; a
// read that began before the one kept never replaces it.
func (s *Service) cacheConn(id string, d *connDoc, at store.Mark, err error, began time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.conns[id]; ok && old.read.After(began) {
		return
	}
	if len(s.conns) >= cacheMax {
		clear(s.conns)
	}
	s.conns[id] = cachedConn{doc: d, at: at, read: began, err: err}
}

// clone is a copy of d that shares nothing with it: the cache's documents
// are never changed, and a caller may change its own copy.
func (d *connDoc) clone() *connDoc {
	if d == nil {
		return nil
	}
	c := *d
	c.Parts, c.Scopes, c.Mailboxes = slices.Clone(d.Parts), slices.Clone(d.Scopes), slices.Clone(d.Mailboxes)
	c.Sealed = slices.Clone(d.Sealed)
	c.Devices = slices.Clone(d.Devices)
	for i := range c.Devices {
		c.Devices[i].Parts, c.Devices[i].Scopes = slices.Clone(c.Devices[i].Parts), slices.Clone(c.Devices[i].Scopes)
	}
	if d.private != nil {
		p := *d.private
		if p.Query != nil {
			q := copyFormula(*p.Query)
			p.Query = &q
		}
		p.Shown.Sentences, p.Shown.Rules = slices.Clone(p.Shown.Sentences), slices.Clone(p.Shown.Rules)
		c.private = &p
	}
	return &c
}

// copyFormula is f with groups and conditions of its own.
func copyFormula(f contracts.Formula) contracts.Formula {
	f.All = slices.Clone(f.All)
	for i := range f.All {
		f.All[i].Any = slices.Clone(f.All[i].Any)
	}
	return f
}

// writeConn writes a connection's record, its private part sealed, checked
// against ConnectionRecordBytesMax and each device against
// oauth.DeviceEntryBytesMax.
func (s *Service) writeConn(ctx context.Context, d *connDoc, at store.Mark, create bool) error {
	raw, err := json.Marshal(d.private)
	if err != nil {
		return err
	}
	if d.Sealed, err = envelope.Seal(s.sealKey, raw, privateAAD(d.ID)); err != nil {
		return err
	}
	if err := d.fits(); err != nil {
		return err
	}
	if err := s.writeSigned(ctx, connName(d.ID), d, at, create); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.conns, d.ID)
	s.mu.Unlock()
	return nil
}

// fits checks the record's bounds (N6).
func (d *connDoc) fits() error {
	for i := range d.Devices {
		b, _ := json.Marshal(d.Devices[i])
		if len(b) > oauth.DeviceEntryBytesMax {
			return contracts.ErrRequestTooLarge
		}
	}
	b, _ := json.Marshal(d)
	if len(b) > ConnectionRecordBytesMax {
		return contracts.ErrRequestTooLarge
	}
	return nil
}

// device is the connection's device id, or nil.
func (d *connDoc) device(id string) *deviceDoc {
	for i := range d.Devices {
		if d.Devices[i].ID == id {
			return &d.Devices[i]
		}
	}
	return nil
}

// activeDevices counts the active devices.
func (d *connDoc) activeDevices() int {
	n := 0
	for _, dev := range d.Devices {
		if dev.State == contracts.ConnectionActive {
			n++
		}
	}
	return n
}

// live says the connection may be used at now.
func (d *connDoc) live(now time.Time) bool {
	return d != nil && d.State == contracts.ConnectionActive && now.Before(d.EndsAt)
}

// tidy ends the devices whose code can no longer be exchanged (it expired
// or was spent without making a family), and drops the ended devices past
// oauth.DevicesEndedKept or EndedKept, oldest first: it answers the families
// whose records must be removed before the record is written without them.
func (d *connDoc) tidy(now time.Time) (drop []string) {
	for i := range d.Devices {
		dev := &d.Devices[i]
		if dev.State == contracts.ConnectionActive && dev.Code.Status != oauth.CodeExchanged && !now.Before(dev.Code.ExpiresAt) {
			dev.State, dev.EndedAt, dev.EndReason = contracts.ConnectionEnded, now, contracts.EndedByTime
		}
	}
	var ended []deviceDoc
	for _, dev := range d.Devices {
		if dev.State == contracts.ConnectionEnded {
			ended = append(ended, dev)
		}
	}
	slices.SortStableFunc(ended, func(a, b deviceDoc) int { return a.EndedAt.Compare(b.EndedAt) })
	for i, dev := range ended {
		if i < len(ended)-oauth.DevicesEndedKept || now.Sub(dev.EndedAt) >= EndedKept {
			drop = append(drop, dev.ID)
		}
	}
	return drop
}

// dropDevices removes ended devices from the record.
func (d *connDoc) dropDevices(ids []string) {
	d.Devices = slices.DeleteFunc(d.Devices, func(dev deviceDoc) bool {
		return dev.State == contracts.ConnectionEnded && slices.Contains(ids, dev.ID)
	})
}

// end ends the connection and every device at now.
func (d *connDoc) end(reason contracts.EndReason, replacedBy string, now time.Time) bool {
	if d.State == contracts.ConnectionEnded {
		return false
	}
	d.State, d.EndedAt, d.EndReason, d.ReplacedBy = contracts.ConnectionEnded, now, reason, replacedBy
	for i := range d.Devices {
		if d.Devices[i].State == contracts.ConnectionActive {
			d.Devices[i].State, d.Devices[i].EndedAt, d.Devices[i].EndReason = contracts.ConnectionEnded, now, reason
		}
	}
	return true
}

// ---- its formulas ----

// scopeOf is a scope's formulas: the query (nil for all mail), the
// mailboxes and the mail since, each canonical and evaluated together.
type scopeOf struct {
	Query     *query.Canonical
	Mailboxes *query.Canonical
	Since     *query.Canonical
}

// list is the formulas, the query first when there is one.
func (sc scopeOf) list() []*query.Canonical {
	out := make([]*query.Canonical, 0, 3)
	for _, f := range []*query.Canonical{sc.Query, sc.Mailboxes, sc.Since} {
		if f != nil {
			out = append(out, f)
		}
	}
	return out
}

var errScope error = &kind{"connections: a scope's formulas do not read"}

func everything() *query.Canonical {
	c, _ := query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}})
	return c
}

// mailboxesFormula is "mailbox is" each mailbox.
func mailboxesFormula(ids []string) (*query.Canonical, error) {
	g := contracts.Group{}
	for _, id := range ids {
		g.Any = append(g.Any, contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: id})
	}
	return query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{g}})
}

// sinceFormula is the mail since: from approved for new mail only, from At,
// or nil for all the mail kept.
func sinceFormula(m contracts.MailSince, approved time.Time) (*query.Canonical, error) {
	at := m.At
	if m.New {
		at = approved
	}
	if at.IsZero() {
		return nil, nil
	}
	c := contracts.Condition{Var: contracts.VarReceived, Op: contracts.OpSince, Value: at.UTC().Format(time.RFC3339)}
	return query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{c}}}})
}

// makeScope is the formulas of a query, mailboxes and mail since.
func makeScope(q *contracts.Formula, mailboxes []string, since contracts.MailSince, approved time.Time) (scopeOf, error) {
	var sc scopeOf
	var err error
	if q != nil {
		if sc.Query, err = query.Canonicalise(*q); err != nil {
			return sc, err
		}
	}
	if sc.Mailboxes, err = mailboxesFormula(mailboxes); err != nil {
		return sc, err
	}
	if sc.Since, err = sinceFormula(since, approved); err != nil {
		return sc, err
	}
	return sc, nil
}

// scope is the connection's own formulas.
func (d *connDoc) scope() (scopeOf, error) {
	return makeScope(d.private.Query, d.Mailboxes, d.private.Since, d.MadeAt)
}

// contained says inner fits inside outer: each formula of outer contains the
// one of inner (an absent one is all mail).
func contained(outer, inner scopeOf) bool {
	or := func(c *query.Canonical) *query.Canonical {
		if c == nil {
			return everything()
		}
		return c
	}
	return query.Contains(or(outer.Query), or(inner.Query)) &&
		query.Contains(or(outer.Mailboxes), or(inner.Mailboxes)) &&
		query.Contains(or(outer.Since), or(inner.Since))
}

// narrower is the device's own formulas: those of draft that are not the
// connection's, and their canonical identifier ("" for none).
func narrower(conn, draft scopeOf) ([]*query.Canonical, string) {
	id := func(c *query.Canonical) string {
		if c == nil {
			return ""
		}
		return c.ID
	}
	var own []*query.Canonical
	for _, p := range [][2]*query.Canonical{{conn.Query, draft.Query}, {conn.Mailboxes, draft.Mailboxes}, {conn.Since, draft.Since}} {
		if p[1] != nil && id(p[0]) != id(p[1]) {
			own = append(own, p[1])
		}
	}
	return own, scopeID(own)
}

// scopeID is the canonical identifier of a device's own formulas.
func scopeID(fs []*query.Canonical) string {
	if len(fs) == 0 {
		return ""
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	h := sha256.Sum256([]byte("permaily device scope v1\x00" + strings.Join(ids, "\x00")))
	return "scp_" + idEncoding.EncodeToString(h[:16])
}

// encodeScope and decodeScope are a device's own formulas as its family's
// record and its tokens' entries carry them.
func encodeScope(fs []*query.Canonical) json.RawMessage {
	if len(fs) == 0 {
		return nil
	}
	out := make([]contracts.Formula, len(fs))
	for i, f := range fs {
		out[i] = f.Formula
	}
	b, _ := json.Marshal(out)
	return b
}

func decodeScope(raw json.RawMessage) ([]*query.Canonical, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fs []contracts.Formula
	if json.Unmarshal(raw, &fs) != nil {
		return nil, errScope
	}
	out := make([]*query.Canonical, len(fs))
	for i, f := range fs {
		c, err := query.Canonicalise(f)
		if err != nil {
			return nil, errScope
		}
		out[i] = c
	}
	return out, nil
}

// ---- what the person is shown ----

// view is the connection as the app shows it.
func (s *Service) view(ctx context.Context, d *connDoc, version uint64) contracts.Connection {
	c := contracts.Connection{
		ID: d.ID, ClientID: d.ClientID, ClientName: d.ClientName, Resource: d.Resource, State: d.State,
		Parts: slices.Clone(d.Parts), Mailboxes: slices.Clone(d.Mailboxes), Since: d.private.Since,
		MadeAt: d.MadeAt, EndsAt: d.EndsAt, EndedAt: d.EndedAt, EndReason: d.EndReason, ReplacedBy: d.ReplacedBy,
		ModelNotice: d.ModelNotice, Version: version, Devices: []contracts.ConnectionDevice{},
	}
	c.Vendor = vendorOf(d.Vendor)
	if c.Since.New {
		c.Since.At = d.MadeAt
	}
	if d.State == contracts.ConnectionActive && !s.now().Before(d.EndsAt) {
		c.State, c.EndedAt, c.EndReason = contracts.ConnectionEnded, d.EndsAt, contracts.EndedByTime
	}
	if d.private.Query != nil {
		if q, err := query.Canonicalise(*d.private.Query); err == nil {
			fc := check(q, d.Mailboxes)
			c.Query = &fc
		}
	}
	for _, dev := range d.Devices {
		c.Devices = append(c.Devices, contracts.ConnectionDevice{
			ID: dev.ID, MadeAt: dev.MadeAt, State: dev.State, Parts: slices.Clone(dev.Parts),
			Narrower: dev.ScopeID != "", EndedAt: dev.EndedAt, EndReason: dev.EndReason,
		})
	}
	c.Delivery = s.delivery(ctx, d)
	return c
}

// delivery says whether a connection gets mail now.
func (s *Service) delivery(ctx context.Context, d *connDoc) contracts.ConnectionDelivery {
	if d.ModelNotice != s.cfg.ModelNotice {
		return contracts.DeliveryNoticeChanged
	}
	if s.cfg.Model == nil || s.cfg.Model.Gate(ctx, d.Account) != inbox.AskAnswered {
		return contracts.DeliveryModelUnavailable
	}
	return contracts.DeliveryOn
}

func vendorOf(domain string) contracts.Vendor {
	v := contracts.Vendor{Domain: domain}
	if owner, ok := vendors.Verified(domain); ok {
		v.Verified = owner
	}
	return v
}

// check is what the screen shows of a canonical formula.
func check(c *query.Canonical, mailboxes []string) contracts.FormulaCheck {
	names := map[string]query.MailboxName{}
	for _, m := range mailboxes {
		names[m] = query.MailboxName{Address: m}
	}
	return contracts.FormulaCheck{Formula: c.Formula, ID: c.ID, Sentence: query.Render(c, query.Context{Lang: "en", Mailboxes: names}), Model: modelDecides(c)}
}

// modelDecides says a model decides a condition of the formula.
func modelDecides(c *query.Canonical) bool {
	for _, g := range c.Formula.All {
		for _, cond := range g.Any {
			if cond.Op == contracts.OpAbout || cond.Op == contracts.OpMentions {
				return true
			}
		}
	}
	return false
}

// ruleSummaries are the rules every connection applies first, as shown.
func (s *Service) ruleSummaries() []contracts.RuleSummary {
	out := []contracts.RuleSummary{}
	for _, r := range s.cfg.Rules {
		out = append(out, contracts.RuleSummary{ID: r.ID, Version: r.Version, Sentence: query.RenderRule(r.Formulas, query.Context{Lang: "en"}).Formulas})
	}
	return out
}

func (s *Service) ruleRefs() []rules.Ref {
	out := make([]rules.Ref, 0, len(s.cfg.Rules))
	for _, r := range s.cfg.Rules {
		out = append(out, r.Ref())
	}
	return out
}

// intersect is the parts in every one of the lists, in a's order.
func intersect(a []contracts.Part, others ...[]contracts.Part) []contracts.Part {
	out := []contracts.Part{}
	for _, p := range a {
		in := true
		for _, o := range others {
			in = in && slices.Contains(o, p)
		}
		if in {
			out = append(out, p)
		}
	}
	return out
}

// subset says every part of inner is in outer.
func subset(outer, inner []contracts.Part) bool {
	for _, p := range inner {
		if !slices.Contains(outer, p) {
			return false
		}
	}
	return true
}
