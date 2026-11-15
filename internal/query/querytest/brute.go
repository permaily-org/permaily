package querytest

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// Eval is the value of formulas as sent (not canonical, not simplified) on
// a message, by brute force from ADR 0027's definitions ("What a formula
// means", "The table", "Which reads"): the least over formulas of the least
// over groups of the greatest over conditions, each condition read straight
// from the message. A formula is read in its version 1 meaning; a value
// outside the universe's spellings is not guaranteed to normalise as
// internal/query normalises it.
func Eval(fs []contracts.Formula, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	if m.Have&(query.FromItem|query.FromMeta) != query.FromItem|query.FromMeta {
		return query.Unknown
	}
	if m.Confirmation {
		return query.False
	}
	v := query.True
	for _, f := range fs {
		for _, g := range f.All {
			gv := query.False
			for _, c := range g.Any {
				gv = max(gv, Condition(c, m, a, mode))
			}
			v = min(v, gv)
		}
	}
	return v
}

// Condition is one condition's value on a message whose item and meta were
// read.
func Condition(c contracts.Condition, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	v := positive(c, m, a, mode)
	if c.Not {
		return query.True - v
	}
	return v
}

func positive(c contracts.Condition, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	switch c.Var {
	case contracts.VarReceived:
		t, _ := time.Parse(time.RFC3339, c.Value)
		if c.Op == contracts.OpSince {
			return of(!m.Received.Before(t))
		}
		return of(m.Received.Before(t))
	case contracts.VarMailbox:
		return of(m.Mailbox == strings.ToLower(c.Value))
	}
	if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
		p, ok := a[query.Atom{Var: c.Var, Op: c.Op, Topic: text(c.Value)}]
		if !ok {
			return query.Unknown
		}
		return query.Classify(p, mode)
	}
	if m.Unparsed {
		return query.Unknown
	}
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarFromDomain:
		return overAddresses(c, m.From, m.Partial)
	case contracts.VarRecipientAddress, contracts.VarRecipientDomain:
		return overAddresses(c, m.Recipients, m.Partial)
	case contracts.VarSignedBy:
		want, _ := domain(c.Value)
		found, bad := false, false
		for _, s := range m.SignedBy {
			d, ok := domain(s)
			bad = bad || !ok
			found = found || (ok && under(d, want))
		}
		return listValue(found, bad)
	case contracts.VarSubject:
		return of(strings.Contains(text(m.Subject), text(c.Value)))
	case contracts.VarBody:
		if m.Have&query.FromText == 0 && (m.Have&query.FromEntry == 0 || m.BodyCut) {
			return query.Unknown
		}
		return of(strings.Contains(text(m.Body), text(c.Value)))
	case contracts.VarAttachments:
		return listValue(len(m.Attachments) > 0, m.Partial)
	case contracts.VarAttachmentName:
		found := slices.ContainsFunc(m.Attachments, func(x query.Attachment) bool { return strings.Contains(text(x.Name), text(c.Value)) })
		return listValue(found, m.Partial)
	case contracts.VarAttachmentType:
		want := strings.ToLower(c.Value)
		found := slices.ContainsFunc(m.Attachments, func(x query.Attachment) bool {
			t, _, _ := strings.Cut(x.Type, ";")
			t = strings.ToLower(strings.TrimSpace(t))
			if t == want {
				return true
			}
			top, sub, ok := strings.Cut(t, "/")
			wtop, wsub, _ := strings.Cut(want, "/")
			listed := slices.Contains([]string{"application", "audio", "font", "image", "message", "model", "multipart", "text", "video"}, top)
			return ok && listed && sub != "" && wsub == "*" && top == wtop
		})
		return listValue(found, m.Partial)
	case contracts.VarList:
		if !m.ListRecorded {
			return query.Unknown
		}
		if c.Op == contracts.OpExists {
			return of(len(m.List) > 0)
		}
		return of(slices.ContainsFunc(m.List, func(l string) bool { return strings.EqualFold(l, c.Value) }))
	case contracts.VarSize:
		if !m.SizeKnown {
			return query.Unknown
		}
		n, _ := strconv.ParseInt(c.Value, 10, 64)
		if c.Op == contracts.OpAtLeast {
			return of(m.Size >= n)
		}
		return of(m.Size < n)
	}
	panic("querytest: a condition outside the universe: " + string(c.Var))
}

// overAddresses is a condition on a list of addresses: one decision per
// address, whole; True when a valid one satisfies it, otherwise Unknown
// when one is not valid or the list may be cut, otherwise False.
func overAddresses(c contracts.Condition, list []string, partial bool) query.Value {
	found, bad := false, false
	for _, s := range list {
		local, d, ok := address(s)
		if !ok {
			bad = true
			continue
		}
		switch c.Var {
		case contracts.VarFromAddress, contracts.VarRecipientAddress:
			wl, wd, _ := address(c.Value)
			found = found || (local == wl && d == wd)
		default:
			want, _ := domain(c.Value)
			found = found || under(d, want)
		}
	}
	return listValue(found, bad || partial)
}

func listValue(found, maybeMissing bool) query.Value {
	switch {
	case found:
		return query.True
	case maybeMissing:
		return query.Unknown
	}
	return query.False
}

func of(b bool) query.Value {
	if b {
		return query.True
	}
	return query.False
}

var invisible = strings.NewReplacer("\u00ad", "", "\u200b", "")

// text is the brute force's normalisation of a text: NFKC, soft hyphens
// and zero-width spaces removed, lower case, white space made one.
func text(s string) string {
	s = norm.NFKC.String(s)
	s = invisible.Replace(s)
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// domain is a domain in ASCII, lower case, without its trailing dot.
func domain(s string) (string, bool) {
	d, err := idna.Lookup.ToASCII(strings.TrimSuffix(s, "."))
	return strings.ToLower(d), err == nil && strings.Contains(d, ".")
}

// address is an address's local part (unquoted, lower case) and domain.
func address(s string) (string, string, bool) {
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return "", "", false
	}
	local := strings.Trim(s[:at], `"`)
	d, ok := domain(s[at+1:])
	return strings.ToLower(local), d, ok && local != "" && !strings.ContainsAny(local, " \"\\")
}

func under(d, parent string) bool {
	return d == parent || strings.HasSuffix(d, "."+parent)
}

// Contained reports whether inner ≤ outer on every message of ms with each
// message's answers in both modes: brute-force containment in three values.
func Contained(outer, inner contracts.Formula, ms []*query.Message, as []query.Answers) bool {
	for i, m := range ms {
		for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
			if Eval([]contracts.Formula{inner}, m, as[i], mode) > Eval([]contracts.Formula{outer}, m, as[i], mode) {
				return false
			}
		}
	}
	return true
}
