package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	receipts "github.com/permaily-org/permaily/pkg/receiptchain"
)

// permaily-verify -receipts checks, offline, an export of a person's
// receipts (ADR 0037, "What a person can verify"; pull request 3): the
// pages the app downloads from GET /api/v1/receipts/export, one JSON object
// a line. It reads them a page at a time and recomputes every digest: each
// link's against what the link above names (the first against the head the
// page says), every receipt against the digest its link names, link 1
// against the account's genesis. It requires the export to be whole: every
// page passes receiptchain.ExportPage.Check and its head
// receiptchain.Head.CheckShown, and besides, each begins exactly where the
// one before ended, no link or receipt is left out, and only the last says
// it is done, after link 1. Its checks are pkg/receiptchain's, the
// protocol the service uses, which links no storage.
//
// It says three things apart. Consistent: the file is a whole export whose
// digests hold, and every noted head is in it. Intact: consistent, and it
// reports no link or receipt broken (a verdict other than ok, expired, or a
// receipt's ok) and no link naming no link before it. Authenticated: the
// links a head the person noted (-receipts-head <n>:<sha256>, once or
// more) vouches for: link n must have that digest, and every link below it
// down to the first break is then authenticated with every receipt it
// names. A noted head above the export's own fails the check (the file was
// cut short). An export checked alone proves only that it is consistent
// with itself: whoever edits the file can recompute every digest.
//
// It passes (exit 0) only an export both consistent and intact; it fails
// (exit 1) one that is not consistent, and one that reports a break, after
// printing every finding and what is authenticated. A verdict is the
// export's word, not covered by any digest a head vouches for: whoever
// edits the file can remove a receipt and call it missing, as the storage
// would have left it. So a break fails the check whoever made it, and the
// check cannot say which: whether the deployment recorded it as an
// incident is what permaily-verify against the deployment checks.

// maxExportLine bounds one line, a page: WalkBytesMax of documents sealed,
// in base64, with the JSON around them.
const maxExportLine = 16 << 20

type receiptsOptions struct {
	file  string
	heads notedHeads
}

func receiptsFlags(fs *flag.FlagSet) *receiptsOptions {
	o := &receiptsOptions{heads: notedHeads{}}
	fs.StringVar(&o.file, "receipts", "", "instead of checking a deployment: check, offline, an export of your receipts (the file the app downloads, one page a line)")
	fs.Var(o.heads, "receipts-head", "with -receipts: a head you noted from the app, as <n>:<sha256>; give it once or more")
	return o
}

// notedHeads are the heads given with -receipts-head, by number.
type notedHeads map[uint64]receipts.Digest

func (h notedHeads) String() string { return "" }

func (h notedHeads) Set(s string) error {
	n, d, ok := strings.Cut(s, ":")
	num, err := strconv.ParseUint(n, 10, 64)
	if !ok || err != nil || num == 0 {
		return errors.New("a head is <n>:<sha256>, n from 1")
	}
	var digest receipts.Digest
	if err := digest.UnmarshalText([]byte(d)); err != nil {
		return errors.New("a head is <n>:<sha256>, its digest 64 lowercase hexadecimal digits")
	}
	if prev, ok := h[num]; ok && prev != digest {
		return fmt.Errorf("two heads noted at %d", num)
	}
	h[num] = digest
	return nil
}

// span is links From down to To.
type span struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

type brokenOut struct {
	N       uint64 `json:"n"`
	Index   *int   `json:"index,omitempty"`
	ID      string `json:"id,omitempty"`
	Verdict string `json:"verdict"`
}

type headOut struct {
	N       uint64 `json:"n"`
	Verdict string `json:"verdict,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// Pages are the pages that said it, from the first.
	Pages span `json:"pages"`
}

type receiptsResult struct {
	// OK is the exit status: consistent and intact.
	OK bool `json:"ok"`
	// Consistent says the file is a whole export whose digests hold, with
	// every noted head in it; Error says why it is not. Intact says it is
	// consistent and reports no break (Broken and Breaks empty).
	Consistent bool   `json:"consistent"`
	Intact     bool   `json:"intact"`
	Error      string `json:"error,omitempty"`
	// Authentication is "whole" when a noted head authenticates every link
	// of a consistent export, "partial" when it authenticates some, "none"
	// otherwise (no head noted, an empty chain, or a file not consistent).
	Authentication string `json:"authentication"`
	Account        string `json:"account,omitempty"`
	// Links and Receipts are how many the export holds.
	Links    uint64 `json:"links"`
	Receipts int    `json:"receipts"`
	// Authenticated are the links a noted head authenticates, the newest
	// first; NotAuthenticated the others.
	Authenticated    []span `json:"authenticated"`
	NotAuthenticated []span `json:"not_authenticated"`
	// Broken are the links and receipts the export reports broken; Breaks
	// the links whose document names no link before it.
	Broken    []brokenOut `json:"broken"`
	Breaks    []brokenOut `json:"breaks"`
	Recovered []span      `json:"recovered"`
	Adopted   []span      `json:"adopted"`
	Expired   []span      `json:"expired"`
	Heads     []headOut   `json:"heads"`
}

// checkReceipts runs -receipts and prints what it found; it returns
// whether the export is consistent and intact.
func checkReceipts(w io.Writer, o *receiptsOptions, asJSON bool) bool {
	res := &receiptsResult{}
	f, err := os.Open(o.file) //nolint:gosec // path given by the user on purpose
	if err == nil {
		res, err = checkExport(f, o.heads)
		_ = f.Close()
	}
	if err != nil {
		res.Consistent, res.Error = false, err.Error()
	}
	res.judge()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return res.OK
	}
	if !res.Consistent {
		fmt.Fprintf(w, "FAIL  receipts: not a consistent export\n      %s\n", res.Error)
		return false
	}
	breaks := len(res.Broken) + len(res.Breaks)
	switch {
	case res.Links == 0:
		fmt.Fprintf(w, "PASS  receipts of %s: an empty chain, no link\n", res.Account)
	case breaks == 0:
		fmt.Fprintf(w, "PASS  receipts of %s: an export of %d links and %d receipts, consistent and intact from link %d to the account's genesis\n", res.Account, res.Links, res.Receipts, res.Links)
	default:
		fmt.Fprintf(w, "FAIL  receipts of %s: an export of %d links and %d receipts, consistent with itself and not intact: it reports %d %s\n", res.Account, res.Links, res.Receipts, breaks, plural(breaks, "break", "breaks"))
	}
	if len(res.Authenticated) > 0 {
		fmt.Fprintf(w, "      authenticated by a noted head: links %s\n", spans(res.Authenticated))
	}
	if len(res.NotAuthenticated) > 0 {
		fmt.Fprintf(w, "WARN  not authenticated by a noted head: links %s\n", spans(res.NotAuthenticated))
	}
	if len(o.heads) == 0 {
		fmt.Fprintln(w, "WARN  no head noted: whoever has the file could have recomputed all of it; note the head the app shows when you export, and pass it with -receipts-head <n>:<sha256>")
	}
	for _, b := range res.Broken {
		switch {
		case b.Index != nil && b.Verdict == string(receipts.ReceiptChanged):
			fmt.Fprintf(w, "BREAK receipt %d.%d %s changed: the export reports it, and its document is not what link %d names\n", b.N, *b.Index, b.ID, b.N)
		case b.Index != nil:
			fmt.Fprintf(w, "BREAK receipt %d.%d %s %s: the export reports it, and has no document of it\n", b.N, *b.Index, b.ID, b.Verdict)
		case b.Verdict == string(receipts.LinkPrev):
			fmt.Fprintf(w, "BREAK link %d prev: the export reports it, and its document is not what link %d names\n", b.N, b.N+1)
		default:
			fmt.Fprintf(w, "BREAK link %d %s: the export reports it, and has no document of it; which receipts it named is not known\n", b.N, b.Verdict)
		}
	}
	for _, b := range res.Breaks {
		fmt.Fprintf(w, "BREAK link %d names no link before it (%s): the link below it is not checked by this chain\n", b.N, b.Verdict)
	}
	for _, s := range res.Recovered {
		fmt.Fprintf(w, "NOTE  %s recovered: its receipts were found without a link and linked late, so whether their answers left is not known\n", linksWord(s))
	}
	for _, s := range res.Adopted {
		fmt.Fprintf(w, "NOTE  %s adopted: receipts written before the chain began; what was deleted before it is not known\n", linksWord(s))
	}
	for _, s := range res.Expired {
		fmt.Fprintf(w, "NOTE  %s expired: their commits' lock ended; their bytes are checked, a fork there no longer shows\n", linksWord(s))
	}
	for _, h := range res.Heads {
		fmt.Fprintf(w, "      head %d %s sha256 %s on pages %d to %d\n", h.N, h.Verdict, h.SHA256, h.Pages.From, h.Pages.To)
	}
	if breaks > 0 {
		fmt.Fprintln(w, "      a break is the export's word, which no head vouches for: the storage made it (then the deployment recorded it as an incident, which permaily-verify against it shows) or whoever edited the file did; this check cannot tell which")
	}
	return res.OK
}

// judge sets what the result says as a whole from what was found.
func (r *receiptsResult) judge() {
	r.Intact = r.Consistent && len(r.Broken) == 0 && len(r.Breaks) == 0
	r.OK = r.Intact
	switch {
	case !r.Consistent || len(r.Authenticated) == 0:
		r.Authentication = "none"
	case len(r.NotAuthenticated) == 0:
		r.Authentication = "whole"
	default:
		r.Authentication = "partial"
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func spans(s []span) string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = fmt.Sprintf("%d to %d", x.From, x.To)
	}
	return strings.Join(out, ", ")
}

func linksWord(s span) string {
	if s.From == s.To {
		return fmt.Sprintf("link %d", s.From)
	}
	return fmt.Sprintf("links %d to %d", s.From, s.To)
}

// addLink adds link n to the spans, links coming the newest first.
func addLink(s []span, n uint64) []span {
	if len(s) > 0 && s[len(s)-1].To == n+1 {
		s[len(s)-1].To = n
		return s
	}
	return append(s, span{From: n, To: n})
}

// exportCheck is where the check of an export is: the link expected next,
// and what authenticates it.
type exportCheck struct {
	noted notedHeads
	res   *receiptsResult
	page  uint64
	top   receipts.Head
	last  receipts.Head
	// n is the link expected next: from its last receipt when from is -1,
	// or continuing before receipt from, the page before having ended
	// inside it (cont, its document and verdict, and link what it says).
	n    uint64
	from int
	cont *receipts.ExportedLink
	link *receipts.Link
	// above is what the link above names of link n, nil after a break or
	// at a head that is not ok; authed says that link above was
	// authenticated by a noted head.
	above  *receipts.Digest
	authed bool
	done   bool
}

// checkExport reads an export from r, a page a line, and checks it whole.
func checkExport(r io.Reader, noted notedHeads) (*receiptsResult, error) {
	c := &exportCheck{noted: noted, res: &receiptsResult{
		Authenticated: []span{}, NotAuthenticated: []span{}, Broken: []brokenOut{}, Breaks: []brokenOut{},
		Recovered: []span{}, Adopted: []span{}, Expired: []span{}, Heads: []headOut{},
	}}
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64<<10), maxExportLine)
	for lines.Scan() {
		c.page++
		if c.done {
			return c.res, fmt.Errorf("page %d is a page after the last, which said it was done", c.page)
		}
		p, err := readPage(lines.Bytes())
		if err != nil {
			return c.res, fmt.Errorf("page %d: %w", c.page, err)
		}
		if err := c.check(p); err != nil {
			return c.res, fmt.Errorf("page %d: %w", c.page, err)
		}
	}
	switch err := lines.Err(); {
	case errors.Is(err, bufio.ErrTooLong):
		return c.res, fmt.Errorf("page %d is larger than a page can be", c.page+1)
	case err != nil:
		return c.res, err
	case c.page == 0:
		return c.res, errors.New("the export is empty")
	case !c.done:
		return c.res, errors.New("the export ends before its last page: no page says it is done; pages or links left out")
	}
	c.res.Consistent = true
	return c.res, nil
}

// readPage reads one line as a page: one JSON object of a page's fields
// and nothing else, which keeps the contract of a page.
func readPage(line []byte) (*receipts.ExportPage, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	p := &receipts.ExportPage{}
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("not a page of an export: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("not a page of an export: more after it on its line")
	}
	if err := p.Check(); err != nil {
		return nil, err
	}
	if err := p.Head.CheckShown(); err != nil {
		return nil, err
	}
	return p, nil
}

func (c *exportCheck) check(p *receipts.ExportPage) error {
	if err := c.heads(p); err != nil {
		return err
	}
	for i := range p.Links {
		if err := c.visit(&p.Links[i], i == 0, i == len(p.Links)-1); err != nil {
			return err
		}
	}
	if p.Done {
		if c.n != 0 || c.from >= 0 {
			return fmt.Errorf("the export says it is done before link 1 (link %d is next): receipts or links left out", c.n)
		}
		c.done = true
		return nil
	}
	// Where the page ended, which is where the next begins.
	want := receipts.Position{N: c.n + 1}
	if c.from >= 0 {
		want = receipts.Position{N: c.n, Index: c.from}
	}
	if p.Next != want {
		return fmt.Errorf("the page says the next begins at %d.%d, not where it ended (%d.%d): receipts or links left out", p.Next.N, p.Next.Index, want.N, want.Index)
	}
	return nil
}

// heads checks the page's account and head against the export's: the first
// page's head is where the export begins, and every noted head must be at
// or below it; a later page's head never goes back, nor says another
// digest at the same number.
func (c *exportCheck) heads(p *receipts.ExportPage) error {
	h := p.Head
	if c.page == 1 {
		c.res.Account, c.top, c.n, c.from = p.Account, h, h.N, -1
		if h.Verdict == receipts.LinkOK {
			d := h.Digest
			c.above = &d
		}
		for n := range c.noted {
			if n > h.N {
				return fmt.Errorf("a noted head (%d) is above the export's head (%d): the file was cut short, or made before you noted it", n, h.N)
			}
		}
	} else {
		switch {
		case p.Account != c.res.Account:
			return errors.New("a page of another account")
		case h.N < c.last.N:
			return fmt.Errorf("the head went back, from %d to %d", c.last.N, h.N)
		case h.N == c.last.N && (h.Verdict != c.last.Verdict || h.Digest != c.last.Digest):
			return fmt.Errorf("the head of this page says another link %d than the page before", h.N)
		}
	}
	c.last = h
	hs := c.res.Heads
	if len(hs) > 0 && hs[len(hs)-1].N == h.N {
		hs[len(hs)-1].Pages.To = c.page
		return nil
	}
	out := headOut{N: h.N, Verdict: string(h.Verdict), Pages: span{From: c.page, To: c.page}}
	if h.Verdict == receipts.LinkOK {
		out.SHA256 = h.Digest.String()
	}
	c.res.Heads = append(hs, out)
	return nil
}

// visit checks one link of a page, and the receipts of it the page holds.
func (c *exportCheck) visit(l *receipts.ExportedLink, first, last bool) error {
	switch {
	case c.n == 0 || l.N > c.n:
		return fmt.Errorf("link %d where link %d was expected: the page does not begin where the one before ended; pages repeated or out of order", l.N, c.n)
	case l.N < c.n && first && c.page == 1:
		return fmt.Errorf("the export does not begin at its head (link %d, not %d): pages left out", l.N, c.n)
	case l.N < c.n && first:
		return fmt.Errorf("link %d where link %d was expected: the page does not begin where the one before ended; pages left out", l.N, c.n)
	case l.N < c.n:
		return fmt.Errorf("link %d follows link %d: links left out", l.N, c.n+1)
	}
	if c.from >= 0 {
		// Link n again, on the page after the one that ended inside it.
		if !first || l.Verdict != c.cont.Verdict || !bytes.Equal(l.Doc, c.cont.Doc) {
			return fmt.Errorf("link %d changed between the pages that hold it", l.N)
		}
	} else if err := c.judge(l); err != nil {
		return err
	}
	start := c.from - 1
	if c.from < 0 && c.link != nil {
		start = len(c.link.Receipts) - 1
	}
	if c.link == nil {
		start = -1
	}
	next := start
	for _, r := range l.Receipts {
		if r.Index != next {
			return fmt.Errorf("receipt %d.%d where %d.%d was expected: receipts left out", l.N, r.Index, l.N, next)
		}
		if err := c.receipt(l.N, &r); err != nil {
			return err
		}
		next--
	}
	if next >= 0 {
		if !last {
			return fmt.Errorf("link %d's receipts below %d.%d left out", l.N, l.N, next+1)
		}
		c.from, c.cont = next+1, l
		return nil
	}
	c.n, c.from, c.cont, c.link = l.N-1, -1, nil, nil
	return nil
}

// judge checks link n the first time the export holds it: its document is a
// link of its number, its digest what the link above names (or the head, or
// a noted head), link 1's names the genesis; and what it means for the link
// below.
func (c *exportCheck) judge(l *receipts.ExportedLink) error {
	c.res.Links++
	c.link = nil
	note, matched := c.noted[l.N]
	if !l.Verdict.Readable() {
		if matched {
			return fmt.Errorf("link %d has no document to match the head you noted: the export says it is %s", l.N, l.Verdict)
		}
		c.res.Broken = append(c.res.Broken, brokenOut{N: l.N, Verdict: string(l.Verdict)})
		c.res.NotAuthenticated = addLink(c.res.NotAuthenticated, l.N)
		c.above, c.authed = nil, false
		return nil
	}
	doc, err := receipts.ParseLink(l.Doc, l.N)
	if err != nil {
		return fmt.Errorf("link %d's document is not a link of number %d", l.N, l.N)
	}
	d := receipts.DigestOf(l.Doc)
	named := c.above != nil && d == *c.above
	switch {
	case c.above != nil && !named && l.Verdict != receipts.LinkPrev && l.N == c.top.N && c.res.Links == 1:
		return fmt.Errorf("link %d is not what the export's head names: a byte changed", l.N)
	case c.above != nil && !named && l.Verdict != receipts.LinkPrev:
		return fmt.Errorf("link %d is not what link %d names: a byte changed", l.N, l.N+1)
	case l.Verdict == receipts.LinkPrev && (c.above == nil || named):
		return fmt.Errorf("link %d is said prev, and nothing above it names another", l.N)
	case matched && d != note:
		return fmt.Errorf("link %d is not the head you noted: the file was changed and its digests recomputed, or it is of another deployment", l.N)
	case l.N == 1 && *doc.Prev != receipts.Genesis(c.res.Account):
		return errors.New("link 1 does not name the account's genesis: the chain was recomputed, or it is not this account's")
	}
	authed := matched || (c.authed && named)
	if authed {
		c.res.Authenticated = addLink(c.res.Authenticated, l.N)
	} else {
		c.res.NotAuthenticated = addLink(c.res.NotAuthenticated, l.N)
	}
	if l.Verdict == receipts.LinkPrev {
		c.res.Broken = append(c.res.Broken, brokenOut{N: l.N, Verdict: string(l.Verdict)})
	}
	switch doc.Kind {
	case receipts.LinkRecovered:
		c.res.Recovered = addLink(c.res.Recovered, l.N)
	case receipts.LinkAdopted:
		c.res.Adopted = addLink(c.res.Adopted, l.N)
	}
	if l.Verdict == receipts.LinkExpired {
		c.res.Expired = addLink(c.res.Expired, l.N)
	}
	c.above, c.authed, c.link = doc.Prev, authed, doc
	if doc.Prev == nil {
		c.res.Breaks = append(c.res.Breaks, brokenOut{N: l.N, Verdict: string(doc.Break)})
	}
	return nil
}

// receipt checks one receipt of link n against the ref its link names.
func (c *exportCheck) receipt(n uint64, r *receipts.ExportedReceipt) error {
	ref := c.link.Receipts[r.Index]
	if r.ID != ref.ID {
		return fmt.Errorf("receipt %d.%d is not the one link %d names", n, r.Index, n)
	}
	c.res.Receipts++
	switch r.Verdict {
	case receipts.ReceiptOK:
		if receipts.DigestOf(r.Doc) != ref.Digest {
			return fmt.Errorf("receipt %d.%d is not what link %d names: a byte changed", n, r.Index, n)
		}
		return nil
	case receipts.ReceiptChanged:
		if receipts.DigestOf(r.Doc) == ref.Digest {
			return fmt.Errorf("receipt %d.%d is said changed, and is what link %d names", n, r.Index, n)
		}
	}
	i := r.Index
	c.res.Broken = append(c.res.Broken, brokenOut{N: n, Index: &i, ID: r.ID, Verdict: string(r.Verdict)})
	return nil
}
