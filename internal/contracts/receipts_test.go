package contracts

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

// L23's contract: a position is written as "<n>.<index>" in decimal, and
// nothing else reads as one, so a page asked of the person's route begins
// only where a page before said.
func TestL23APositionIsWrittenAndReadBack(t *testing.T) {
	for _, p := range []ReceiptsPosition{{N: 1}, {N: 12, Index: 3}, {N: math.MaxUint64, Index: 63}} {
		got, err := ParseReceiptsPosition(p.String())
		if err != nil || got != p {
			t.Errorf("%+v: %q read back as %+v, %v", p, p.String(), got, err)
		}
	}
	if s := (ReceiptsPosition{N: 12, Index: 3}).String(); s != "12.3" {
		t.Fatalf("written as %q", s)
	}
	for _, s := range []string{
		"", ".", "1", "1.", ".1", "0.0", "0.1", "01.0", "1.01", "+1.0", "-1.0", "1.-1", "1.2.3", " 1.0", "1.0 ", "1,0", "a.0", "1.a",
		"18446744073709551616.0", "1." + strconv.Itoa(math.MaxInt) + "0", "1.65",
	} {
		if p, err := ParseReceiptsPosition(s); !errors.Is(err, ErrInvalidBefore) {
			t.Errorf("%q read as %+v, %v", s, p, err)
		}
	}
}

// The answers' shapes are the service's, field by field: what the web app
// reads (web/src/lib/api.ts) and what permaily-verify -receipts reads of an
// export, its documents' exact bytes in base64.
func TestL23TheReceiptsAnswersShapes(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	page := ReceiptsPage{
		Head: ReceiptsHead{N: 2, Verdict: "ok", SHA256: "ab", At: at},
		Links: []ReceiptsLink{
			{N: 2, Verdict: "ok", Kind: "receipts", At: at, Prev: "cd", Receipts: []ReceiptsEntry{{Index: 0, ID: "rcp_x", SHA256: "ef", Verdict: "ok", Receipt: json.RawMessage(`{"v":1}`)}}},
			{N: 1, Verdict: "lost"},
		},
		Next: ReceiptsPosition{N: 1},
	}
	b, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"head":{"n":2,"verdict":"ok","sha256":"ab","at":"2026-11-17T12:00:00Z"},"links":[{"n":2,"verdict":"ok","kind":"receipts","at":"2026-11-17T12:00:00Z","prev":"cd","receipts":[{"index":0,"id":"rcp_x","sha256":"ef","verdict":"ok","receipt":{"v":1}}]},{"n":1,"verdict":"lost"}],"next":{"n":1,"index":0}}`
	if string(b) != want {
		t.Fatalf("a page:\n%s\nwant\n%s", b, want)
	}
	export := ReceiptsExportPage{V: 1, Account: "acc_x", Head: ReceiptsHead{}, Links: []ReceiptsExportLink{{N: 1, Verdict: "ok", Doc: []byte("{}"), Receipts: []ReceiptsExportEntry{{Index: 0, ID: "rcp_x", Verdict: "missing"}}}}, Done: true}
	if b, err = json.Marshal(export); err != nil {
		t.Fatal(err)
	}
	const wantExport = `{"v":1,"account":"acc_x","head":{"n":0},"links":[{"n":1,"verdict":"ok","doc":"e30=","receipts":[{"index":0,"id":"rcp_x","verdict":"missing"}]}],"next":{"n":0,"index":0},"done":true}`
	if string(b) != wantExport {
		t.Fatalf("an export's page:\n%s\nwant\n%s", b, wantExport)
	}
	lost := ReceiptsLink{N: 3, Verdict: "forked", Break: "lost"}
	if b, _ = json.Marshal(lost); string(b) != `{"n":3,"verdict":"forked","break":"lost"}` {
		t.Fatalf("a link that cannot be read: %s", b)
	}
	adopted := ReceiptsLink{N: 1, Verdict: "ok", Kind: "adopted", At: at, Prev: "cd", Before: at, More: true}
	if b, _ = json.Marshal(adopted); string(b) != `{"n":1,"verdict":"ok","kind":"adopted","at":"2026-11-17T12:00:00Z","prev":"cd","before":"2026-11-17T12:00:00Z","more":true}` {
		t.Fatalf("an adopted link: %s", b)
	}
}
