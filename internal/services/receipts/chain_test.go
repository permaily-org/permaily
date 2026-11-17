package receipts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// L1: a link's record name says its account and number and nothing else
// parses as one; each stored version of it is kept in the account's own
// partition, under a key whose order is the numbers' and that never meets a
// receipt's; a link's plain name has no key, so nothing written there is
// read.
func TestALinksNameIsItsAccountAndNumber(t *testing.T) {
	const account = "acc_abcdefghijklmnopqrstuvwxyz"
	for _, n := range []uint64{1, 2, 63, 64, 1 << 20, 1<<64 - 1} {
		name := LinkName(account, n)
		got, m, ok := ParseLinkName(name)
		if !ok || got != account || m != n {
			t.Fatalf("%q parsed as %q %d %t", name, got, m, ok)
		}
		if _, _, ok := RecordKey(name); ok {
			t.Fatalf("the plain name %q has a key", name)
		}
		partition, key, ok := RecordKey(name + "@00000000000000000001-0123456789abcdef")
		if !ok || partition != Partition(account) || key != fmt.Sprintf("lnk_%016x@00000000000000000001-0123456789abcdef", n) {
			t.Fatalf("%d is kept at %q %q %t", n, partition, key, ok)
		}
		if !strings.HasPrefix(key, LinkKeyPrefix(n)+"@") || strings.HasPrefix(key, IDPrefix) {
			t.Fatalf("the key %q", key)
		}
	}
	if LinkKeyPrefix(9) >= LinkKeyPrefix(10) || LinkKeyPrefix(255) >= LinkKeyPrefix(256) {
		t.Fatal("the keys' order is not the numbers'")
	}
	for _, name := range []string{
		"", "receipt-link-", "receipt-link-acc", "receipt-link--1", "receipt-link-acc-0", "receipt-link-acc-01",
		"receipt-link-acc-+1", "receipt-link-acc-1x", "receipt-link-acc-18446744073709551616", "connections-acc-1", "mailbox-acc-1", "receipt-chains-0", "receipt-audit-01", "receipt-chains", "receipt-audit-x",
	} {
		if _, _, ok := ParseLinkName(name); ok {
			t.Fatalf("%q parsed as a link's name", name)
		}
		if _, _, ok := RecordKey(name + "@1-a"); ok {
			t.Fatalf("%q has a link's key", name)
		}
		if _, _, ok := CommitGroup(name); ok {
			t.Fatalf("%q has a group of commits", name)
		}
	}
	// An account's identifier with a hyphen is still the whole of it.
	if got, n, ok := ParseLinkName(LinkName("acc-with-hyphens", 7)); !ok || got != "acc-with-hyphens" || n != 7 {
		t.Fatalf("parsed as %q %d %t", got, n, ok)
	}
}

// L1: the genesis is the account's own and what the design says: the
// SHA-256 of the purpose, a zero byte and the identifier; a digest is
// lowercase hexadecimal in JSON and nothing else is read as one.
func TestTheGenesisIsTheAccounts(t *testing.T) {
	if Genesis("acc_a") == Genesis("acc_b") {
		t.Fatal("two accounts share a genesis")
	}
	want := sha256.Sum256([]byte("permaily receipts chain v1\x00acc_a"))
	if Genesis("acc_a") != Digest(want) {
		t.Fatal("the genesis is not the design's")
	}
	if DigestOf([]byte(`{"v":1}`)) != Digest(sha256.Sum256([]byte(`{"v":1}`))) {
		t.Fatal("a digest is not the SHA-256 of the bytes")
	}
	d := Genesis("acc_a")
	b, err := json.Marshal(d)
	if err != nil || string(b) != `"`+d.String()+`"` || len(d.String()) != 64 {
		t.Fatalf("%s %v", b, err)
	}
	var back Digest
	if err := json.Unmarshal(b, &back); err != nil || back != d {
		t.Fatalf("%v", err)
	}
	for _, bad := range []string{`""`, `"` + strings.ToUpper(d.String()) + `"`, `"` + d.String()[:62] + `"`, `"` + d.String() + `00"`, `"` + strings.Repeat("g", 64) + `"`} {
		if json.Unmarshal([]byte(bad), &back) == nil {
			t.Fatalf("%s read as a digest", bad)
		}
	}
}

func testLink(n uint64, refs int) Link {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	prev := Genesis("acc_a")
	l := Link{V: LinkVersion, N: n, Kind: LinkReceipts, At: at, Prev: &prev}
	for i := range refs {
		l.Receipts = append(l.Receipts, Ref{ID: fmt.Sprintf("rcp_%016x%020x", at.UnixNano(), i), Digest: Genesis(fmt.Sprint(i))})
	}
	return l
}

// L2: a link is checked as it is written: its version, number, kind and
// time; the link before named by a digest or a break, never both, and link
// 1 by its genesis; adoption's fields only on adopted links; one to
// LinkReceiptsMax receipts, each a receipt's identifier, none twice.
func TestALinkIsChecked(t *testing.T) {
	ok := testLink(2, 3)
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	adopted := testLink(1, 2)
	adopted.Kind, adopted.Before, adopted.More = LinkAdopted, adopted.At.Add(-AdoptAfter), true
	if err := adopted.Check(); err != nil {
		t.Fatal(err)
	}
	broken := testLink(5, 1)
	broken.Prev, broken.Break = nil, BreakLost
	if err := broken.Check(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Link){
		"version":          func(l *Link) { l.V = 2 },
		"number":           func(l *Link) { l.N = 0 },
		"kind":             func(l *Link) { l.Kind = "summary" },
		"time":             func(l *Link) { l.At = time.Time{} },
		"neither":          func(l *Link) { l.Prev = nil },
		"both":             func(l *Link) { l.Break = BreakForked },
		"break reason":     func(l *Link) { l.Prev, l.Break = nil, "gone" },
		"link 1 broken":    func(l *Link) { l.N, l.Prev, l.Break = 1, nil, BreakLost },
		"before on linked": func(l *Link) { l.Before = l.At },
		"more on linked":   func(l *Link) { l.More = true },
		"adopted unbound":  func(l *Link) { l.Kind = LinkAdopted },
		"empty":            func(l *Link) { l.Receipts = nil },
		"too many":         func(l *Link) { *l = testLink(l.N, LinkReceiptsMax+1) },
		"not a receipt":    func(l *Link) { l.Receipts[0].ID = "lnk_0000000000000001" },
		"twice":            func(l *Link) { l.Receipts[1].ID = l.Receipts[0].ID },
	} {
		l := testLink(2, 3)
		edit(&l)
		if l.Check() == nil {
			t.Errorf("%s: a link that is not one passed", name)
		}
	}
}

// L2: a link naming LinkReceiptsMax receipts, written after a break, with
// adoption's fields, is within two thirds of LinkBytesMax, and sealed as text within
// one entry: a group the appender makes always fits one link.
func TestTheLargestLinkFits(t *testing.T) {
	l := testLink(1<<64-1, LinkReceiptsMax)
	l.Kind, l.Before, l.More = LinkAdopted, time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true
	l.At = l.Before
	if err := l.Check(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(&l)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > LinkBytesMax*2/3 { // about 8.1 KiB
		t.Fatalf("the largest link is %d bytes", len(b))
	}
	if sealed := (LinkBytesMax+64)*4/3 + 1024; sealed > 400<<10 {
		t.Fatalf("a link seals to %d bytes", sealed)
	}
}

// L1: the commits of an account's links are kept together, apart from
// another account's, and so are the catalogue's and the audit's passes, each
// member in its number's order, so one listing reads the history of a
// range; their values are kept where RecordKey says, and nothing else has a
// group or a key.
func TestTheChainsRecordsAreGroupedAndKept(t *testing.T) {
	const a, b = "acc_abcdefghijklmnopqrstuvwxyz", "acc_bbcdefghijklmnopqrstuvwxyz"
	for _, c := range []struct{ name, group, member, partition, key string }{
		{LinkName(a, 1), "receipt-link-" + a, "0000000000000001", Partition(a), "lnk_0000000000000001@v"},
		{LinkName(b, 255), "receipt-link-" + b, "00000000000000ff", Partition(b), "lnk_00000000000000ff@v"},
		{CatalogueName(16), "receipt-chains", "0000000000000010", CataloguePartition, "cat_0000000000000010@v"},
		{AuditName(1<<64 - 1), "receipt-audit", "ffffffffffffffff", AuditPartition, "pass_ffffffffffffffff@v"},
	} {
		group, member, ok := CommitGroup(c.name)
		if !ok || group != c.group || member != c.member {
			t.Fatalf("%q is grouped as %q %q %t", c.name, group, member, ok)
		}
		if _, _, ok := CommitGroup(c.name + "@v"); ok {
			t.Fatalf("a stored version of %q has a group of commits", c.name)
		}
		if _, _, ok := RecordKey(c.name); ok {
			t.Fatalf("the plain name %q has a key", c.name)
		}
		partition, key, ok := RecordKey(c.name + "@v")
		if !ok || partition != c.partition || key != c.key {
			t.Fatalf("%q is kept at %q %q %t", c.name, partition, key, ok)
		}
	}
	if Partition(a) == CataloguePartition || Partition(a) == AuditPartition || CataloguePartition == AuditPartition {
		t.Fatal("two series share a partition")
	}
	if CatalogueGenesis() == Genesis("catalogue") || CatalogueGenesis() == Genesis("") {
		t.Fatal("the catalogue's genesis is an account's")
	}
	if CatalogueGenesis() != Digest(sha256.Sum256([]byte("permaily receipts chain v1\x01catalogue"))) {
		t.Fatal("the catalogue's genesis is not the design's")
	}
	for _, name := range []string{"receipt-chains-1-1", "receipt-audit", "receipt-chainsx-1", "x-receipt-chains-1", "receipt-chains-1x"} {
		if _, _, ok := CommitGroup(name); ok {
			t.Fatalf("%q has a group of commits", name)
		}
		if _, _, ok := RecordKey(name + "@v"); ok {
			t.Fatalf("%q has a key", name)
		}
	}
}

// L2: a link of kind recovered is checked as one of kind receipts: it
// names the link before, and says nothing of adoption.
func TestARecoveredLinkIsChecked(t *testing.T) {
	l := testLink(9, 2)
	l.Kind = LinkRecovered
	if err := l.Check(); err != nil {
		t.Fatal(err)
	}
	l.Before = l.At
	if l.Check() == nil {
		t.Fatal("a recovered link said what it adopts")
	}
}

const testAccount = "acc_abcdefghijklmnopqrstuvwxyz"

func testCatalogue(k uint64, accounts int) Catalogue {
	prev := CatalogueGenesis()
	c := Catalogue{V: CatalogueVersion, K: k, At: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), Prev: &prev}
	for i := range accounts {
		const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
		c.Accounts = append(c.Accounts, "acc_"+strings.Repeat("a", 24)+string(alphabet[i/32])+string(alphabet[i%32]))
	}
	return c
}

// L2: an entry of the catalogue is checked as it is written: its version,
// number and time, the entry before named as a link names the link before,
// one to CatalogueAccountsMax accounts of an account's shape, none twice;
// the largest fits LinkBytesMax.
func TestACatalogueEntryIsChecked(t *testing.T) {
	ok := testCatalogue(3, 3)
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	broken := testCatalogue(4, 1)
	broken.Prev, broken.Break = nil, BreakForked
	if err := broken.Check(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Catalogue){
		"version":        func(c *Catalogue) { c.V = 2 },
		"number":         func(c *Catalogue) { c.K = 0 },
		"time":           func(c *Catalogue) { c.At = time.Time{} },
		"neither":        func(c *Catalogue) { c.Prev = nil },
		"both":           func(c *Catalogue) { c.Break = BreakLost },
		"entry 1 broken": func(c *Catalogue) { c.K, c.Prev, c.Break = 1, nil, BreakLost },
		"break reason":   func(c *Catalogue) { c.Prev, c.Break = nil, "gone" },
		"empty":          func(c *Catalogue) { c.Accounts = nil },
		"too many":       func(c *Catalogue) { *c = testCatalogue(c.K, CatalogueAccountsMax+1) },
		"prefix":         func(c *Catalogue) { c.Accounts[0] = "ses_abcdefghijklmnopqrstuvwxyz" },
		"length":         func(c *Catalogue) { c.Accounts[0] = testAccount + "a" },
		"alphabet":       func(c *Catalogue) { c.Accounts[0] = "acc_abcdefghijklmnopqrstuvwxy1" },
		"case":           func(c *Catalogue) { c.Accounts[0] = "acc_Abcdefghijklmnopqrstuvwxyz" },
		"twice":          func(c *Catalogue) { c.Accounts[1] = c.Accounts[0] },
	} {
		c := testCatalogue(3, 3)
		edit(&c)
		if c.Check() == nil {
			t.Errorf("%s: an entry that is not one passed", name)
		}
	}
	largest := testCatalogue(1<<64-1, CatalogueAccountsMax)
	largest.At = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	if err := largest.Check(); err != nil {
		t.Fatal(err)
	}
	if b, err := json.Marshal(&largest); err != nil || len(b) > LinkBytesMax/2 {
		t.Fatalf("the largest entry is %d bytes (%v)", len(b), err)
	}
}

func testPass() AuditPass {
	at := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	p := AuditPass{V: AuditVersion, P: 7, Audit: attestation.ReceiptsAudit{Began: at, Finished: at.Add(time.Minute), Catalogue: 2, Chains: 3, Broken: map[string]int64{}}}
	for _, k := range attestation.ReceiptsBrokenKinds {
		p.Audit.Broken[k] = 0
	}
	return p
}

// L2: a pass of the audit is checked as it is written: its version and
// number, when it began and finished, in that order, every kind counted and
// no other, nothing below zero; the largest fits LinkBytesMax.
func TestAnAuditPassIsChecked(t *testing.T) {
	ok := testPass()
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*AuditPass){
		"version":   func(p *AuditPass) { p.V = 2 },
		"number":    func(p *AuditPass) { p.P = 0 },
		"began":     func(p *AuditPass) { p.Audit.Began = time.Time{} },
		"order":     func(p *AuditPass) { p.Audit.Finished = p.Audit.Began.Add(-time.Second) },
		"chains":    func(p *AuditPass) { p.Audit.Chains = -1 },
		"removed":   func(p *AuditPass) { p.Audit.Removed = -1 },
		"recovered": func(p *AuditPass) { p.Audit.Recovered = -1 },
		"missing":   func(p *AuditPass) { delete(p.Audit.Broken, "link_lost") },
		"other":     func(p *AuditPass) { delete(p.Audit.Broken, "link_lost"); p.Audit.Broken["unlinked"] = 0 },
		"negative":  func(p *AuditPass) { p.Audit.Broken["receipt_missing"] = -1 },
	} {
		p := testPass()
		edit(&p)
		if p.Check() == nil {
			t.Errorf("%s: a pass that is not one passed", name)
		}
	}
	largest := testPass()
	largest.P, largest.Audit.Catalogue, largest.Audit.Chains, largest.Audit.Removed, largest.Audit.Recovered = 1<<64-1, 1<<64-1, 1<<63-1, 1<<63-1, 1<<63-1
	for k := range largest.Audit.Broken {
		largest.Audit.Broken[k] = 1<<63 - 1
	}
	if b, err := json.Marshal(&largest); err != nil || len(b) > LinkBytesMax/8 {
		t.Fatalf("the largest pass is %d bytes (%v)", len(b), err)
	}
}

// L8: a chain not begun is a head of number 0 with nothing else; a head
// says its verdict, and its digest unless it is forked.
func TestAHeadSaysWhatWasFound(t *testing.T) {
	b, err := json.Marshal(Head{})
	if err != nil || string(b) != `{"n":0}` {
		t.Fatalf("%s %v", b, err)
	}
	b, err = json.Marshal(Head{N: 3, Verdict: LinkForked})
	if err != nil || string(b) != `{"n":3,"verdict":"forked"}` {
		t.Fatalf("%s %v", b, err)
	}
}
