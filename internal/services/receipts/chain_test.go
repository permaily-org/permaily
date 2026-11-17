package receipts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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
		if _, _, ok := LinkKey(name); ok {
			t.Fatalf("the plain name %q has a key", name)
		}
		partition, key, ok := LinkKey(name + "@00000000000000000001-0123456789abcdef")
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
		"receipt-link-acc-+1", "receipt-link-acc-1x", "receipt-link-acc-18446744073709551616", "connections-acc-1", "mailbox-acc-1",
	} {
		if _, _, ok := ParseLinkName(name); ok {
			t.Fatalf("%q parsed as a link's name", name)
		}
		if _, _, ok := LinkKey(name + "@1-a"); ok {
			t.Fatalf("%q has a link's key", name)
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
