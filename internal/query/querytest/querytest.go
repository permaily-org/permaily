// Package querytest generates formulas and messages over a small universe of
// values chosen to collide, and evaluates a formula as sent by brute force,
// from ADR 0027's definitions and nothing of internal/query's rewrites,
// implication table or containment. It is for tests only (depguard's
// no-fakes-in-production keeps it out of the image).
//
// The brute force normalises values with its own plain code: enough for the
// universe here (ASCII, a Unicode domain, case, full-width letters, white
// space, soft hyphens and zero-width spaces), not for every string. What
// internal/query does with every string is tested by its own tables and
// fuzz targets; what the brute force checks is the logic.
package querytest

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The universe. Every value a generated formula holds is one of these,
// possibly respelled; every field of a generated message is built from them.
var (
	// Domains: two, a subdomain of one, and a Unicode one.
	Domains = []string{"example.com", "sub.example.com", "other.example", "bücher.example"}
	// Addresses: valid ones, under those domains.
	Addresses = []string{"alice@example.com", "bob@sub.example.com", "carol@other.example", "ann@bücher.example"}
	// BadAddress is an address that is not valid (its local part has a
	// space once unquoted), as net/mail stores "bad local"@example.com.
	BadAddress = "bad local@example.com"
	// Texts are values of contains, substrings of each other.
	Texts = []string{"invoice", "invoices", "car", "scar", "e 2", "invoice 2026"}
	// Fields are subjects, bodies and attachment names that hold them or
	// not, respelled.
	Fields = []string{
		"Your INVOICES for 2026", "invoice 2026", "Ｉｎｖｏｉｃｅ", "re-invoiced", "scar tissue", "careful",
		"in­voice", "nothing here", "the  car", "", "e\n2",
	}
	// Topics are what a model decides on.
	Topics = []string{"invoices", "travel"}
	// Times are four instants, a month apart.
	Times = []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	// Sizes are three values of size conditions.
	Sizes = []int64{10, 1000, 1048576}
	// Mailboxes are the account's two mailboxes, and Other one it does not
	// have.
	Mailboxes    = []string{"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa", "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"}
	OtherMailbox = "mbx_cccccccccccccccccccccccccc"
	// Types are media types of conditions; MessageTypes those of messages.
	Types        = []string{"application/pdf", "application/*", "image/png", "image/*"}
	MessageTypes = []string{"application/pdf", "Application/PDF; name=x", "image/png", "text/plain", "foo/bar", "application"}
	// Lists are list identifiers.
	Lists = []string{"news.example.com", "deals.other.example"}
	// AttachmentNames are names of attachments of messages.
	AttachmentNames = []string{"invoice.pdf", "scar.png", "CAR", "notes.txt"}
)

// ModelOps are the conditions a model decides, as variable and operator.
var ModelOps = [][2]string{{"subject", "about"}, {"message", "about"}, {"message", "mentions"}}

// Atoms are every atom of the universe.
func Atoms() []query.Atom {
	var out []query.Atom
	for _, t := range Topics {
		for _, o := range ModelOps {
			out = append(out, query.Atom{Var: contracts.Variable(o[0]), Op: contracts.Operator(o[1]), Topic: t})
		}
	}
	return out
}

// Rand is a generator of the universe, seeded so a failure can be
// reproduced.
type Rand struct{ *rand.Rand }

// New is a generator seeded with seed.
func New(seed uint64) *Rand {
	return &Rand{rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func pick[T any](r *Rand, s []T) T { return s[r.IntN(len(s))] }

// Condition is a random condition over the universe, as a vendor could send
// it (possibly negated, never respelled).
func (r *Rand) Condition() contracts.Condition {
	c := contracts.Condition{Not: r.IntN(3) == 0}
	switch r.IntN(17) {
	case 0:
		c.Var, c.Op, c.Value = contracts.VarFromAddress, contracts.OpIs, pick(r, Addresses)
	case 1:
		c.Var, c.Op, c.Value = contracts.VarFromDomain, contracts.OpIs, pick(r, Domains)
	case 2:
		c.Var, c.Op, c.Value = contracts.VarRecipientAddress, contracts.OpIs, pick(r, Addresses)
	case 3:
		c.Var, c.Op, c.Value = contracts.VarRecipientDomain, contracts.OpIs, pick(r, Domains)
	case 4:
		c.Var, c.Op, c.Value = contracts.VarSignedBy, contracts.OpIs, pick(r, Domains)
	case 5:
		c.Var, c.Op, c.Value = contracts.VarSubject, contracts.OpContains, pick(r, Texts)
	case 6:
		c.Var, c.Op, c.Value = contracts.VarBody, contracts.OpContains, pick(r, Texts)
	case 7:
		o := pick(r, ModelOps)
		c.Var, c.Op, c.Value = contracts.Variable(o[0]), contracts.Operator(o[1]), pick(r, Topics)
	case 8:
		c.Var, c.Op = contracts.VarAttachments, contracts.OpExists
	case 9:
		c.Var, c.Op, c.Value = contracts.VarAttachmentName, contracts.OpContains, pick(r, Texts)
	case 10:
		c.Var, c.Op, c.Value = contracts.VarAttachmentType, contracts.OpIs, pick(r, Types)
	case 11:
		c.Var, c.Op = contracts.VarList, contracts.OpExists
	case 12:
		c.Var, c.Op, c.Value = contracts.VarList, contracts.OpIs, pick(r, Lists)
	case 13, 14:
		c.Var, c.Op, c.Value = contracts.VarReceived, pick(r, []contracts.Operator{contracts.OpSince, contracts.OpBefore}), pick(r, Times).Format(time.RFC3339)
	case 15:
		c.Var, c.Op, c.Value = contracts.VarSize, pick(r, []contracts.Operator{contracts.OpAtLeast, contracts.OpBelow}), strconv.FormatInt(pick(r, Sizes), 10)
	default:
		c.Var, c.Op, c.Value = contracts.VarMailbox, contracts.OpIs, pick(r, Mailboxes)
	}
	return c
}

// Formula is a random formula of up to maxGroups groups of up to maxConds
// conditions each, with at most contracts.MaxModelConditions a model
// decides.
func (r *Rand) Formula(maxGroups, maxConds int) contracts.Formula {
	f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}
	model := 0
	for range r.IntN(maxGroups + 1) {
		var g contracts.Group
		for range 1 + r.IntN(maxConds) {
			c := r.Condition()
			for IsModel(c) && model >= contracts.MaxModelConditions {
				c = r.Condition()
			}
			if IsModel(c) {
				model++
			}
			g.Any = append(g.Any, c)
		}
		f.All = append(f.All, g)
	}
	return f
}

// IsModel reports a condition a model decides.
func IsModel(c contracts.Condition) bool {
	return c.Op == contracts.OpAbout || c.Op == contracts.OpMentions
}

// Respell gives f again with its conditions and groups shuffled, some
// repeated, and its values spelled another way that normalises the same:
// case, white space, invisible characters, a domain's Unicode or ASCII form
// and a trailing dot, a quoted local part, a time with an offset, a negated
// time or size made the other operator.
func (r *Rand) Respell(f contracts.Formula) contracts.Formula {
	out := contracts.Formula{V: f.V, All: []contracts.Group{}}
	model := 0
	for _, g := range f.All {
		for _, c := range g.Any {
			if IsModel(c) {
				model++
			}
		}
	}
	for _, g := range f.All {
		var ng contracts.Group
		for _, c := range g.Any {
			ng.Any = append(ng.Any, r.respell(c))
			if r.IntN(5) == 0 && (!IsModel(c) || model < contracts.MaxModelConditions) {
				ng.Any = append(ng.Any, r.respell(c))
				if IsModel(c) {
					model++
				}
			}
		}
		r.Shuffle(len(ng.Any), func(i, j int) { ng.Any[i], ng.Any[j] = ng.Any[j], ng.Any[i] })
		out.All = append(out.All, ng)
		if r.IntN(5) == 0 && !slices.ContainsFunc(ng.Any, IsModel) {
			out.All = append(out.All, ng)
		}
	}
	r.Shuffle(len(out.All), func(i, j int) { out.All[i], out.All[j] = out.All[j], out.All[i] })
	return out
}

func (r *Rand) respell(c contracts.Condition) contracts.Condition {
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarRecipientAddress:
		local, domain, _ := strings.Cut(c.Value, "@")
		if r.IntN(2) == 0 {
			local = `"` + local + `"`
		}
		c.Value = r.caseOf(local) + "@" + r.domain(domain)
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		c.Value = r.domain(c.Value)
	case contracts.VarSubject, contracts.VarBody, contracts.VarAttachmentName, contracts.VarMessage:
		c.Value = r.text(c.Value)
	case contracts.VarAttachmentType, contracts.VarList, contracts.VarMailbox:
		c.Value = r.caseOf(c.Value)
	case contracts.VarReceived:
		t, _ := time.Parse(time.RFC3339, c.Value)
		if r.IntN(2) == 0 {
			c.Value = t.In(time.FixedZone("", -5*3600-30*60)).Format(time.RFC3339)
		}
		if r.IntN(2) == 0 {
			c.Not, c.Op = !c.Not, map[contracts.Operator]contracts.Operator{contracts.OpSince: contracts.OpBefore, contracts.OpBefore: contracts.OpSince}[c.Op]
		}
	case contracts.VarSize:
		if r.IntN(2) == 0 {
			c.Not, c.Op = !c.Not, map[contracts.Operator]contracts.Operator{contracts.OpAtLeast: contracts.OpBelow, contracts.OpBelow: contracts.OpAtLeast}[c.Op]
		}
	}
	return c
}

func (r *Rand) caseOf(s string) string {
	b := []rune(s)
	for i := range b {
		if r.IntN(2) == 0 {
			b[i] = []rune(strings.ToUpper(string(b[i])))[0]
		}
	}
	return string(b)
}

func (r *Rand) domain(d string) string {
	if d == "bücher.example" && r.IntN(2) == 0 {
		d = "xn--bcher-kva.example"
	}
	if r.IntN(3) == 0 {
		d += "."
	}
	return r.caseOf(d)
}

func (r *Rand) text(s string) string {
	s = r.caseOf(s)
	switch r.IntN(5) {
	case 0:
		s = "  " + s + "\t"
	case 1:
		s = strings.ReplaceAll(s, " ", " \n ")
	case 2:
		if len(s) > 1 {
			s = s[:1] + "­" + s[1:] // a soft hyphen, removed
		}
	case 3:
		s = "​" + s // a zero-width space, removed
	}
	return s
}

// Message is a random message over the universe, with every source read
// or not, Partial, Unparsed, cut texts, sizes recorded or not, addresses
// that are not valid, and now and then a Gmail confirmation.
func (r *Rand) Message() *query.Message {
	m := &query.Message{Have: query.FromItem | query.FromMeta}
	switch r.IntN(20) {
	case 0:
		m.Have = query.FromItem
	case 1:
		m.Have = 0
	}
	if r.IntN(2) == 0 {
		m.Have |= query.FromEntry
	}
	if r.IntN(4) == 0 {
		m.Have |= query.FromText
	}
	m.Mailbox = pick(r, append([]string{OtherMailbox}, Mailboxes...))
	t := pick(r, Times)
	switch r.IntN(3) {
	case 0:
		t = t.Add(-time.Nanosecond)
	case 1:
		t = t.Add(time.Hour)
	}
	m.Received = t
	if r.IntN(2) == 0 {
		m.Size, m.SizeKnown = pick(r, []int64{5, 10, 999, 1000, 1048575, 2000000}), true
	}
	m.From = r.addresses()
	m.Recipients = r.addresses()
	for range r.IntN(3) {
		m.SignedBy = append(m.SignedBy, pick(r, append([]string{"bad_domain!", "SUB.Example.COM."}, Domains...)))
	}
	m.Subject = pick(r, Fields)
	m.Body = pick(r, Fields)
	m.BodyCut = r.IntN(3) == 0
	for range r.IntN(3) {
		m.Attachments = append(m.Attachments, query.Attachment{Name: pick(r, AttachmentNames), Type: pick(r, MessageTypes)})
	}
	m.ListRecorded = r.IntN(4) != 0
	if r.IntN(2) == 0 {
		m.List = []string{pick(r, Lists)}
		if r.IntN(2) == 0 {
			m.List[0] = strings.ToUpper(m.List[0])
		}
	}
	m.Partial = r.IntN(6) == 0
	m.Unparsed = r.IntN(15) == 0
	m.Confirmation = r.IntN(40) == 0
	return m
}

func (r *Rand) addresses() []string {
	var out []string
	for range r.IntN(3) {
		out = append(out, pick(r, append([]string{BadAddress, "ALICE@Example.COM", "x@bad_domain!"}, Addresses...)))
	}
	return out
}

// Answers are a model's answers for one message: each atom missing, or in
// each band of both modes.
func (r *Rand) Answers() query.Answers {
	a := query.Answers{}
	for _, atom := range Atoms() {
		if r.IntN(4) == 0 {
			continue
		}
		a[atom] = pick(r, []float64{0.1, 0.2, 0.25, 0.5, 0.75, 0.8, 0.9})
	}
	return a
}

// Stronger is a formula that implies f by construction (every message it
// matches f matches, in three values), so that containment is often
// provable: a condition replaced by one that implies it, a text lengthened,
// a group added, a condition taken out of a group.
func (r *Rand) Stronger(f contracts.Formula) contracts.Formula {
	out := contracts.Formula{V: f.V, All: []contracts.Group{}}
	for _, g := range f.All {
		out.All = append(out.All, contracts.Group{Any: slices.Clone(g.Any)})
	}
	for range 1 + r.IntN(3) {
		switch r.IntN(3) {
		case 0:
			out.All = append(out.All, contracts.Group{Any: []contracts.Condition{r.Condition()}})
		case 1:
			if len(out.All) == 0 {
				continue
			}
			g := &out.All[r.IntN(len(out.All))]
			if len(g.Any) > 1 {
				i := r.IntN(len(g.Any))
				g.Any = slices.Delete(g.Any, i, i+1)
			}
		default:
			if len(out.All) == 0 {
				continue
			}
			g := &out.All[r.IntN(len(out.All))]
			i := r.IntN(len(g.Any))
			g.Any[i] = r.stronger(g.Any[i])
		}
	}
	return out
}

// stronger is a condition that implies c.
func (r *Rand) stronger(c contracts.Condition) contracts.Condition {
	pos := c
	pos.Not = false
	var cands []contracts.Condition
	if !c.Not {
		cands = strongerPositive(pos)
	} else {
		cands = weakerPositive(pos)
	}
	if len(cands) == 0 {
		return c
	}
	out := pick(r, cands)
	out.Not = c.Not
	return out
}

// strongerPositive lists positive conditions that imply c.
func strongerPositive(c contracts.Condition) []contracts.Condition {
	var out []contracts.Condition
	switch c.Var {
	case contracts.VarFromDomain, contracts.VarRecipientDomain:
		addr := contracts.VarFromAddress
		if c.Var == contracts.VarRecipientDomain {
			addr = contracts.VarRecipientAddress
		}
		for _, a := range Addresses {
			if under(a[strings.LastIndexByte(a, '@')+1:], c.Value) {
				out = append(out, contracts.Condition{Var: addr, Op: contracts.OpIs, Value: a})
			}
		}
		fallthrough
	case contracts.VarSignedBy:
		out = append(out, contracts.Condition{Var: c.Var, Op: contracts.OpIs, Value: "deep." + c.Value})
	case contracts.VarSubject, contracts.VarBody, contracts.VarAttachmentName:
		if c.Op == contracts.OpContains {
			out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: c.Value + "s"}, contracts.Condition{Var: c.Var, Op: c.Op, Value: "x" + c.Value})
		}
	case contracts.VarAttachments:
		out = append(out, contracts.Condition{Var: contracts.VarAttachmentName, Op: contracts.OpContains, Value: "car"},
			contracts.Condition{Var: contracts.VarAttachmentType, Op: contracts.OpIs, Value: "image/png"})
	case contracts.VarAttachmentType:
		if t, ok := strings.CutSuffix(c.Value, "/*"); ok {
			out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: t + "/x-test"})
		}
	case contracts.VarList:
		if c.Op == contracts.OpExists {
			out = append(out, contracts.Condition{Var: c.Var, Op: contracts.OpIs, Value: Lists[0]})
		}
	case contracts.VarReceived:
		t, _ := time.Parse(time.RFC3339, c.Value)
		d := 24 * time.Hour
		if c.Op == contracts.OpBefore {
			d = -d
		}
		out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: t.Add(d).Format(time.RFC3339)})
	case contracts.VarSize:
		n, _ := strconv.ParseInt(c.Value, 10, 64)
		if c.Op == contracts.OpAtLeast {
			n++
		} else if n > 1 {
			n--
		}
		out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: strconv.FormatInt(n, 10)})
	}
	return out
}

// weakerPositive lists positive conditions that c implies.
func weakerPositive(c contracts.Condition) []contracts.Condition {
	var out []contracts.Condition
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarRecipientAddress:
		dom := contracts.VarFromDomain
		if c.Var == contracts.VarRecipientAddress {
			dom = contracts.VarRecipientDomain
		}
		d := c.Value[strings.LastIndexByte(c.Value, '@')+1:]
		out = append(out, contracts.Condition{Var: dom, Op: contracts.OpIs, Value: d})
		if p, ok := parent(d); ok {
			out = append(out, contracts.Condition{Var: dom, Op: contracts.OpIs, Value: p})
		}
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		if p, ok := parent(c.Value); ok {
			out = append(out, contracts.Condition{Var: c.Var, Op: contracts.OpIs, Value: p})
		}
	case contracts.VarSubject, contracts.VarBody, contracts.VarAttachmentName:
		if c.Op == contracts.OpContains && len(c.Value) > 1 {
			out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: c.Value[1:]}, contracts.Condition{Var: c.Var, Op: c.Op, Value: c.Value[:len(c.Value)-1]})
		}
		if c.Var == contracts.VarAttachmentName {
			out = append(out, contracts.Condition{Var: contracts.VarAttachments, Op: contracts.OpExists})
		}
	case contracts.VarAttachmentType:
		t, _, _ := strings.Cut(c.Value, "/")
		out = append(out, contracts.Condition{Var: c.Var, Op: c.Op, Value: t + "/*"}, contracts.Condition{Var: contracts.VarAttachments, Op: contracts.OpExists})
	case contracts.VarList:
		out = append(out, contracts.Condition{Var: c.Var, Op: contracts.OpExists})
	}
	return out
}

func parent(d string) (string, bool) {
	_, p, ok := strings.Cut(d, ".")
	return p, ok && strings.Contains(p, ".")
}
