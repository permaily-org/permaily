package query

import (
	"slices"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Eval is the value of formulas evaluated together (the least of their
// values; each canonical on its own, never merged) on what is known of a
// message, with the model's answers for that message read by Classify in
// mode, and what would change it. Unknown, needing FromItem and FromMeta,
// for a message without both.
//
// A Gmail forwarding confirmation is False whatever the formulas: it never
// leaves. No formula at all, or a nil one, is Unknown: nothing was asked,
// and Unknown delivers nothing. A condition that is not canonical (a
// Canonical built by hand) is Unknown.
func Eval(fs []*Canonical, m *Message, answers Answers, mode Mode) Result {
	if m == nil || m.Have&(FromItem|FromMeta) != FromItem|FromMeta {
		return Result{Value: Unknown, Needs: FromItem | FromMeta}
	}
	if len(fs) == 0 || slices.Contains(fs, nil) {
		return Result{Value: Unknown}
	}
	if m.Confirmation {
		return Result{Value: False}
	}
	v := &view{m: m}
	type evaluated struct {
		c     cond
		value Value
		needs Sources
		atom  *Atom // a model's condition without an answer
	}
	var groups [][]evaluated
	value := True
	for _, f := range fs {
		if f.Never {
			value = False
			continue
		}
		for _, g := range f.Formula.All {
			gv := False
			eg := make([]evaluated, len(g.Any))
			for i, c := range g.Any {
				cc, ok := compile(c)
				e := evaluated{c: cc, value: Unknown}
				if ok {
					e.value, e.needs, e.atom = v.condition(cc, answers, mode)
				}
				eg[i] = e
				gv = max(gv, e.value)
			}
			if gv == Unknown {
				groups = append(groups, eg)
			}
			value = min(value, gv)
		}
	}
	r := Result{Value: value}
	if value != Unknown {
		return r
	}
	// What the Unknown depends on: every Unknown condition of a group whose
	// value is Unknown (a group that is True is settled; none is False).
	for _, g := range groups {
		for _, e := range g {
			if e.value != Unknown {
				continue
			}
			r.Needs |= e.needs
			if e.atom != nil && !slices.Contains(r.Model, *e.atom) {
				r.Model = append(r.Model, *e.atom)
			}
		}
	}
	return r
}

// Resolve says whether a value delivers (ForQuery: only True) or applies
// the rule (ForRule: True or Unknown, since every rule protects). Any other
// mode delivers and applies nothing.
func Resolve(v Value, mode Mode) bool {
	switch mode {
	case ForQuery:
		return v == True
	case ForRule:
		return v == True || v == Unknown
	}
	return false
}

// view is a message's fields normalised as values are, each once, when a
// condition first needs it.
type view struct {
	m *Message

	from, recipients *addresses
	signedBy         *domains
	subject, body    *text
	names            *texts
}

type addresses struct {
	valid   []address
	invalid bool // an entry that is not a valid address
}

type address struct{ local, domain string }

type domains struct {
	valid   []string
	invalid bool
}

type text struct {
	s  string
	ok bool // false: it did not settle within the normaliser's rounds
}

type texts struct {
	valid   []string
	invalid bool
}

func normaliseAddresses(list []string) *addresses {
	out := &addresses{}
	for _, a := range list {
		local, domain, _, reason := normaliseAddress(a)
		if reason != "" {
			out.invalid = true
			continue
		}
		out.valid = append(out.valid, address{local, domain})
	}
	return out
}

func normaliseTextField(s string) *text {
	n, ok := normaliseText(s)
	return &text{n, ok}
}

// condition is the value of one condition on the message, the sources that
// would settle it when it is Unknown, and its atom when a model decides it
// and has not answered. Every value is read before the sign is applied.
func (v *view) condition(c cond, answers Answers, mode Mode) (Value, Sources, *Atom) {
	m := v.m
	if isModel(c.Op) {
		a := Atom{Var: c.Var, Op: c.Op, Topic: c.Value}
		p, ok := answers[a]
		if !ok {
			return Unknown, 0, &a
		}
		return sign(Classify(p, mode), c.Not), 0, nil
	}
	if alwaysKnown(c.Var) {
		return sign(v.item(c), c.Not), 0, nil
	}
	if m.Unparsed {
		return Unknown, 0, nil // the header could not be read: only the item's conditions are known
	}
	var val Value
	var needs Sources
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarFromDomain:
		if v.from == nil {
			v.from = normaliseAddresses(m.From)
		}
		val = v.addressCondition(v.from, c)
	case contracts.VarRecipientAddress, contracts.VarRecipientDomain:
		if v.recipients == nil {
			v.recipients = normaliseAddresses(m.Recipients)
		}
		val = v.addressCondition(v.recipients, c)
	case contracts.VarSignedBy:
		if v.signedBy == nil {
			v.signedBy = &domains{}
			for _, d := range m.SignedBy {
				n, _, reason := normaliseDomain(d)
				if reason != "" {
					v.signedBy.invalid = true
					continue
				}
				v.signedBy.valid = append(v.signedBy.valid, n)
			}
		}
		val = some(slices.ContainsFunc(v.signedBy.valid, func(d string) bool { return underDomain(d, c.domain) }), v.signedBy.invalid)
	case contracts.VarSubject:
		if v.subject == nil {
			v.subject = normaliseTextField(m.Subject)
		}
		val = contained(v.subject, c.Value)
	case contracts.VarBody:
		val, needs = v.bodyCondition(c)
	case contracts.VarAttachments:
		val = some(len(m.Attachments) > 0, m.Partial)
	case contracts.VarAttachmentName:
		if v.names == nil {
			v.names = &texts{}
			for _, a := range m.Attachments {
				n, ok := normaliseText(a.Name)
				if !ok {
					v.names.invalid = true
					continue
				}
				v.names.valid = append(v.names.valid, n)
			}
		}
		val = some(slices.ContainsFunc(v.names.valid, func(n string) bool { return strings.Contains(n, c.Value) }), v.names.invalid || m.Partial)
	case contracts.VarAttachmentType:
		val = some(slices.ContainsFunc(m.Attachments, func(a Attachment) bool { return typeMatches(messageMediaType(a.Type), c.Value) }), m.Partial)
	case contracts.VarList:
		val = v.listCondition(c)
	case contracts.VarSize:
		val = v.sizeCondition(c)
	}
	return sign(val, c.Not), needs, nil
}

// item is a condition on the item: when it was received, the mailbox.
func (v *view) item(c cond) Value {
	switch c.Op {
	case contracts.OpSince:
		return boolValue(!v.m.Received.Before(c.time))
	case contracts.OpBefore:
		return boolValue(v.m.Received.Before(c.time))
	}
	return boolValue(v.m.Mailbox == c.Value) // mailbox is
}

// addressCondition is a condition over a list of addresses: True when one
// valid entry satisfies it; otherwise Unknown when an entry is not valid or
// the list may have been cut (Partial); otherwise False.
func (v *view) addressCondition(list *addresses, c cond) Value {
	found := slices.ContainsFunc(list.valid, func(a address) bool {
		if c.kind == kindAddress {
			return a.local == c.local && a.domain == c.domain
		}
		return underDomain(a.domain, c.domain)
	})
	return some(found, list.invalid || v.m.Partial)
}

// bodyCondition is a body condition: read from the entry's text, or from
// the raw message's when the entry's was cut. No body condition is decided
// from a cut text, for True or for False.
func (v *view) bodyCondition(c cond) (Value, Sources) {
	m := v.m
	switch {
	case m.Have&FromText != 0:
	case m.Have&FromEntry == 0:
		return Unknown, FromEntry
	case m.BodyCut:
		return Unknown, FromText
	}
	if v.body == nil {
		v.body = normaliseTextField(m.Body)
	}
	return contained(v.body, c.Value), 0
}

func (v *view) listCondition(c cond) Value {
	m := v.m
	if !m.ListRecorded {
		return Unknown
	}
	if c.Op == contracts.OpExists {
		return boolValue(len(m.List) > 0)
	}
	return boolValue(slices.ContainsFunc(m.List, func(l string) bool { return strings.ToLower(l) == c.Value }))
}

func (v *view) sizeCondition(c cond) Value {
	m := v.m
	if !m.SizeKnown {
		return Unknown
	}
	if c.Op == contracts.OpAtLeast {
		return boolValue(m.Size >= c.size)
	}
	return boolValue(m.Size < c.size)
}

// contained is a contains condition on one normalised field.
func contained(t *text, value string) Value {
	if !t.ok {
		return Unknown
	}
	return boolValue(strings.Contains(t.s, value))
}

// some is a condition on a list: True when found, otherwise Unknown when
// the list may be missing what was looked for, otherwise False.
func some(found, maybeMissing bool) Value {
	switch {
	case found:
		return True
	case maybeMissing:
		return Unknown
	}
	return False
}

func boolValue(b bool) Value {
	if b {
		return True
	}
	return False
}

// sign applies a condition's sign: True and False swap, Unknown stays.
func sign(v Value, not bool) Value {
	if not {
		return True - v
	}
	return v
}
