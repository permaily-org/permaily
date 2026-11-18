package receiptchain

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const account = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

var at = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func id(i int) string {
	return IDPrefix + strings.Repeat("0", 16) + strings.Repeat("0", 2*IDRandomBytes-1) + string(rune('0'+i))
}

// The digests a person recomputes: SHA-256 of the bytes as they are, the
// genesis of ChainPurpose, a zero byte and the account, in lowercase
// hexadecimal both ways and nothing else.
func TestDigests(t *testing.T) {
	if DigestOf([]byte("x")) != sha256.Sum256([]byte("x")) {
		t.Fatal("DigestOf")
	}
	if Genesis(account) != sha256.Sum256([]byte("permaily receipts chain v1\x00"+account)) || Genesis(account) == Genesis("acc_bbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatal("Genesis")
	}
	d := DigestOf([]byte("x"))
	text, err := d.MarshalText()
	if err != nil || string(text) != d.String() || len(text) != 64 {
		t.Fatalf("%s %v", text, err)
	}
	var back Digest
	if err := back.UnmarshalText(text); err != nil || back != d {
		t.Fatalf("%v %v", back, err)
	}
	for _, bad := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 65)} {
		if back.UnmarshalText([]byte(bad)) == nil {
			t.Errorf("%q read as a digest", bad)
		}
	}
	if SealedBytes(0) != len(`{"sealed":""}`)+84 || SealedBytes(3) != SealedBytes(0)+4 {
		t.Fatal(SealedBytes(0))
	}
}

func TestValidID(t *testing.T) {
	if IDLength != 40 || !ValidID(id(1)) {
		t.Fatal(id(1))
	}
	for _, bad := range []string{"", id(1)[:IDLength-1], "rcq_" + id(1)[4:], strings.ToUpper(id(1)[:4]) + id(1)[4:], id(1)[:IDLength-1] + "g", id(1) + "0"} {
		if ValidID(bad) {
			t.Errorf("%q valid", bad)
		}
	}
}

func goodLink() *Link {
	g := Genesis(account)
	return &Link{V: LinkVersion, N: 1, Kind: LinkReceipts, At: at, Prev: &g, Receipts: []Ref{{ID: id(1)}, {ID: id(2)}}}
}

// A link's document as one is written, and every way it is not one.
func TestALinkIsChecked(t *testing.T) {
	if err := goodLink().Check(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(l *Link){
		func(l *Link) { l.Kind = LinkAdopted; l.Before, l.More = at, true },
		func(l *Link) { l.Kind = LinkRecovered },
		func(l *Link) { l.N, l.Prev, l.Break = 2, nil, BreakForked },
	} {
		l := goodLink()
		edit(l)
		if err := l.Check(); err != nil {
			t.Errorf("%+v: %v", l, err)
		}
	}
	for name, edit := range map[string]func(l *Link){
		"version":        func(l *Link) { l.V = 2 },
		"number 0":       func(l *Link) { l.N = 0 },
		"kind":           func(l *Link) { l.Kind = "other" },
		"no time":        func(l *Link) { l.At = time.Time{} },
		"prev and break": func(l *Link) { l.Break = BreakLost },
		"neither":        func(l *Link) { l.Prev = nil },
		"link 1 broken":  func(l *Link) { l.Prev, l.Break = nil, BreakLost },
		"a reason":       func(l *Link) { l.N, l.Prev, l.Break = 2, nil, "other" },
		"before":         func(l *Link) { l.Before = at },
		"more":           func(l *Link) { l.More = true },
		"adopted":        func(l *Link) { l.Kind = LinkAdopted },
		"no receipt":     func(l *Link) { l.Receipts = nil },
		"too many":       func(l *Link) { l.Receipts = make([]Ref, LinkReceiptsMax+1) },
		"not a receipt":  func(l *Link) { l.Receipts[1].ID = "x" },
		"twice":          func(l *Link) { l.Receipts[1].ID = l.Receipts[0].ID },
	} {
		l := goodLink()
		edit(l)
		if l.Check() == nil {
			t.Errorf("%s: a link that is not one passed", name)
		}
	}
}

// ParseLink reads one link's document strictly: one object of its fields,
// nothing after it, that passes Check, of its number (the review of pull
// request 3, F3).
func TestParseLink(t *testing.T) {
	doc, err := json.Marshal(goodLink())
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseLink(doc, 1)
	if err != nil || l.N != 1 || len(l.Receipts) != 2 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := ParseLink(append(append([]byte{}, doc...), " \n"...), 1); err != nil {
		t.Fatalf("white space after it: %v", err)
	}
	for name, bad := range map[string][]byte{
		"not JSON":        []byte("{"),
		"a field unknown": []byte(strings.Replace(string(doc), `{`, `{"extra":1,`, 1)),
		"a bracket after": append(append([]byte{}, doc...), ']'),
		"a brace after":   append(append([]byte{}, doc...), '}'),
		"a token cut":     append(append([]byte{}, doc...), `{"v`...),
		"another value":   append(append([]byte{}, doc...), "{}"...),
		"not a link":      []byte(`{"v":1}`),
	} {
		if _, err := ParseLink(bad, 1); err == nil {
			t.Errorf("%s: read as a link", name)
		}
	}
	if _, err := ParseLink(doc, 2); err == nil || !strings.Contains(err.Error(), "says it is link 1") {
		t.Fatalf("another number: %v", err)
	}
}

// The verdicts the protocol has, and no other.
func TestVerdicts(t *testing.T) {
	for v, want := range map[LinkVerdict][2]bool{
		LinkOK: {true, true}, LinkPrev: {true, true}, LinkExpired: {true, true},
		LinkLost: {false, true}, LinkForked: {false, true}, LinkUnreadable: {false, true},
		"": {false, false}, "OK": {false, false}, "NOT_A_VERDICT": {false, false},
	} {
		if v.Readable() != want[0] || v.Known() != want[1] {
			t.Errorf("link %q: readable %t, known %t", v, v.Readable(), v.Known())
		}
	}
	for v, want := range map[ReceiptVerdict][2]bool{
		ReceiptOK: {true, true}, ReceiptChanged: {true, true},
		ReceiptMissing: {false, true}, ReceiptUnreadable: {false, true},
		"": {false, false}, "NOT_A_VERDICT": {false, false},
	} {
		if v.HasDocument() != want[0] || v.Known() != want[1] {
			t.Errorf("receipt %q: document %t, known %t", v, v.HasDocument(), v.Known())
		}
	}
}

// A head as the person's routes show it: number 0 and nothing else, or a
// verdict a head can have, its digest and time when ok and only then.
func TestAHeadShown(t *testing.T) {
	d := DigestOf([]byte("x"))
	for _, h := range []Head{{}, {N: 2, Verdict: LinkOK, Digest: d, At: at}, {N: 2, Verdict: LinkLost}, {N: 2, Verdict: LinkForked}, {N: 2, Verdict: LinkUnreadable}} {
		if err := h.CheckShown(); err != nil {
			t.Errorf("%+v: %v", h, err)
		}
	}
	for _, h := range []Head{
		{Verdict: LinkOK}, {Digest: d}, {At: at},
		{N: 2}, {N: 2, Verdict: "NOT_A_VERDICT"}, {N: 2, Verdict: LinkPrev, Digest: d, At: at}, {N: 2, Verdict: LinkExpired, Digest: d, At: at},
		{N: 2, Verdict: LinkOK, At: at}, {N: 2, Verdict: LinkOK, Digest: d},
		{N: 2, Verdict: LinkLost, Digest: d}, {N: 2, Verdict: LinkLost, At: at},
	} {
		if h.CheckShown() == nil {
			t.Errorf("%+v shown", h)
		}
	}
}

// testPage is a page of a chain of two links, the head and link 1, as an
// export holds it whole.
func testPage() *ExportPage {
	return &ExportPage{V: ExportVersion, Account: account, Head: Head{N: 2, Verdict: LinkOK}, Done: true, Links: []ExportedLink{
		{N: 2, Verdict: LinkOK, Doc: []byte(`{}`), Receipts: []ExportedReceipt{{Index: 0, ID: "rcp_c", Verdict: ReceiptOK, Doc: []byte(`{}`)}}},
		{N: 1, Verdict: LinkExpired, Doc: []byte(`{}`), Receipts: []ExportedReceipt{
			{Index: 2, ID: "rcp_b", Verdict: ReceiptChanged, Doc: []byte(`{}`)},
			{Index: 1, ID: "rcp_a", Verdict: ReceiptMissing},
			{Index: 0, ID: "rcp_9", Verdict: ReceiptUnreadable},
		}},
	}}
}

// The contract of an export's page (L17, L24; R7; the review of pull
// request 3, F2): its version, its bounds, the head's verdict, links in
// descending number below the head and receipts in descending index, each
// of a verdict the protocol has, a document only where its verdict says one
// was read, Done or a Next no higher than where the page ended.
func TestAnExportPageIsChecked(t *testing.T) {
	if err := testPage().Check(); err != nil {
		t.Fatal(err)
	}
	if b := testPage().Bytes(); b != 4*SealedBytes(2) {
		t.Fatalf("a page of four documents charges %d", b)
	}
	empty := &ExportPage{V: ExportVersion, Account: account, Done: true}
	if err := empty.Check(); err != nil {
		t.Fatalf("an empty chain: %v", err)
	}
	for name, edit := range map[string]func(p *ExportPage){
		"version":               func(p *ExportPage) { p.V = 2 },
		"done and next":         func(p *ExportPage) { p.Next = Position{N: 1} },
		"not done":              func(p *ExportPage) { p.Done = false },
		"a head of no verdict":  func(p *ExportPage) { p.Head.Verdict = "" },
		"a head of another":     func(p *ExportPage) { p.Head.Verdict = "NOT_A_VERDICT" },
		"a head 0 of a verdict": func(p *ExportPage) { p.Head, p.Links = Head{Verdict: LinkOK}, nil },
		"above head":            func(p *ExportPage) { p.Links[0].N = 3 },
		"link 0":                func(p *ExportPage) { p.Links[1].N = 0 },
		"order":                 func(p *ExportPage) { p.Links[0], p.Links[1] = p.Links[1], p.Links[0] },
		"no doc":                func(p *ExportPage) { p.Links[0].Doc = nil },
		"lost doc":              func(p *ExportPage) { p.Links[0].Verdict = LinkLost },
		"lost receipts":         func(p *ExportPage) { p.Links[1].Verdict, p.Links[1].Doc = LinkForked, nil },
		"a link of another": func(p *ExportPage) {
			p.Links[1].Verdict, p.Links[1].Doc, p.Links[1].Receipts = "NOT_A_VERDICT", nil, nil
		},
		"receipt order":            func(p *ExportPage) { p.Links[1].Receipts[1].Index = 2 },
		"receipt below 0":          func(p *ExportPage) { p.Links[1].Receipts[2].Index = -1 },
		"receipt too high":         func(p *ExportPage) { p.Links[0].Receipts[0].Index = LinkReceiptsMax },
		"missing doc":              func(p *ExportPage) { p.Links[1].Receipts[1].Doc = []byte(`{}`) },
		"ok without doc":           func(p *ExportPage) { p.Links[0].Receipts[0].Doc = nil },
		"a receipt of another":     func(p *ExportPage) { p.Links[1].Receipts[1].Verdict = "NOT_A_VERDICT" },
		"a receipt of none":        func(p *ExportPage) { p.Links[1].Receipts[1].Verdict = "" },
		"too many":                 func(p *ExportPage) { p.Links = make([]ExportedLink, WalkLinksMax+1) },
		"too large":                func(p *ExportPage) { p.Links[0].Receipts[0].Doc = make([]byte, WalkBytesMax) },
		"next above a link":        func(p *ExportPage) { p.Done, p.Next, p.Links = false, Position{N: 2, Index: 1}, p.Links[:1] },
		"next inside above":        func(p *ExportPage) { p.Done, p.Next = false, Position{N: 1, Index: 1} },
		"no progress":              func(p *ExportPage) { p.Done, p.Next, p.Links = false, Position{N: 3}, nil },
		"a receipt of a link lost": func(p *ExportPage) { p.Links[1].Verdict, p.Links[1].Doc = LinkLost, nil },
	} {
		p := testPage()
		edit(p)
		if p.Check() == nil {
			t.Errorf("%s: a page that is not one passed", name)
		}
	}
	for name, edit := range map[string]func(p *ExportPage){
		"inside": func(p *ExportPage) {
			p.Done, p.Next, p.Links[1].Receipts = false, Position{N: 1, Index: 2}, p.Links[1].Receipts[:1]
		},
		"at a link": func(p *ExportPage) { p.Done, p.Next, p.Links = false, Position{N: 2}, p.Links[:1] },
		"after lost": func(p *ExportPage) {
			p.Done, p.Next, p.Links[1] = false, Position{N: 1}, ExportedLink{N: 1, Verdict: LinkLost}
		},
		"a head lost": func(p *ExportPage) { p.Head.Verdict, p.Links[0] = LinkLost, ExportedLink{N: 2, Verdict: LinkLost} },
	} {
		p := testPage()
		edit(p)
		if err := p.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A head at the last number a chain can have never wraps to 0.
	p := &ExportPage{V: ExportVersion, Account: account, Head: Head{N: math.MaxUint64, Verdict: LinkOK}, Next: Position{N: math.MaxUint64}, Links: []ExportedLink{
		{N: math.MaxUint64, Verdict: LinkOK, Doc: []byte(`{}`), Receipts: []ExportedReceipt{{Index: 0, ID: "rcp_c", Verdict: ReceiptOK, Doc: []byte(`{}`)}}},
	}}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	p.Head.N = math.MaxUint64 - 1
	if p.Check() == nil {
		t.Fatal("a link above the page's head passed")
	}
}
