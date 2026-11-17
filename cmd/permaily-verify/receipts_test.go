package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// permaily-verify -receipts (ADR 0037, "What a person can verify"; pull
// request 3): L24's offline check and L25's offline half, on exports the
// person's route answers over the real chained receipts service, and on
// exports built here in every shape a page can have.

const exportAccount = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

// chainKeys derives keys from a fixed secret.
type chainKeys struct{}

func (chainKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	sum := receipts.DigestOf([]byte("root\x00" + purpose))
	return sum[:], nil
}

// liveAccounts says every account is live, and lists none.
type liveAccounts struct{}

func (liveAccounts) Removing(context.Context, string) (bool, error) { return false, nil }

func (liveAccounts) AccountRecords(context.Context, string, int) (*contracts.AccountList, error) {
	return &contracts.AccountList{Accounts: []contracts.AccountRecord{}}, nil
}

// session plays the session of exportAccount.
type session struct{}

func (session) Authenticate(context.Context, string) (*accounts.Authenticated, error) {
	return &accounts.Authenticated{AccountID: exportAccount}, nil
}

// chained is the real chained receipts service over a storage of its own,
// and the person's routes over it.
type chained struct {
	t     *testing.T
	mem   *store.Memory
	svc   *receipts.Service
	route http.Handler
}

func newChained(t *testing.T) *chained {
	t.Helper()
	mem := store.NewMemory()
	commits := store.NewMemoryCommits()
	commits.Group = receipts.CommitGroup
	svc, err := receipts.NewChained(mem, chainKeys{}, time.Now, &store.Anchored{Records: receipts.ChainTable{Entries: mem}, Commits: commits, Strict: true}, liveAccounts{})
	if err != nil {
		t.Fatal(err)
	}
	return &chained{t: t, mem: mem, svc: svc, route: httpapi.ReceiptsAPI(httpapi.ReceiptsConfig{Chain: svc, Sessions: session{}, Log: slog.New(slog.DiscardHandler)})}
}

// write writes n receipts, each in a link of its own, and returns their
// identifiers, the oldest first.
func (c *chained) write(n int) []string {
	c.t.Helper()
	var ids []string
	for range n {
		r := &receipts.Receipt{Kind: receipts.KindEnded, ClientID: "https://client.example/meta", Vendor: "client.example", Resource: contracts.ResourceMCP, Ended: &receipts.Ended{Reason: contracts.EndedByReplaced}}
		if err := c.svc.Write(context.Background(), exportAccount, r); err != nil {
			c.t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	return ids
}

// export downloads every page of the export through the route, as the app
// does, one line a page, and returns the file and the head the first page
// said, as the person notes it.
func (c *chained) export() (string, contracts.ReceiptsHead) {
	c.t.Helper()
	var file strings.Builder
	var head contracts.ReceiptsHead
	before := ""
	for pages := 0; ; pages++ {
		path := "https://app.permaily.test/api/v1/receipts/export"
		if before != "" {
			path += "?before=" + before
		}
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "x"})
		rec := httptest.NewRecorder()
		c.route.ServeHTTP(rec, req)
		var page contracts.ReceiptsExportPage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			c.t.Fatalf("page %d: %d %s", pages, rec.Code, rec.Body)
		}
		if pages == 0 {
			head = page.Head
		}
		file.Write(rec.Body.Bytes())
		if page.Done {
			return file.String(), head
		}
		before = page.Next.String()
	}
}

// deleteEntry deletes the entry of the account's partition under key.
func (c *chained) deleteEntry(key string) {
	c.t.Helper()
	for _, e := range c.mem.AllEntries() {
		if e.Partition == receipts.Partition(exportAccount) && strings.HasPrefix(e.Key, key) {
			if err := c.mem.DeleteEntry(context.Background(), e.Partition, e.Key); err != nil {
				c.t.Fatal(err)
			}
			return
		}
	}
	c.t.Fatalf("no entry %s", key)
}

// verifyReceipts runs permaily-verify -receipts on file with the noted
// heads, and returns its exit status and what it printed.
func verifyReceipts(t *testing.T, file string, heads ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-receipts", path}
	for _, h := range heads {
		args = append(args, "-receipts-head", h)
	}
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, nil)
	return code, stdout.String() + stderr.String()
}

func noted(h contracts.ReceiptsHead) string { return fmt.Sprintf("%d:%s", h.N, h.SHA256) }

// pagesOf reads an export's lines.
func pagesOf(t *testing.T, file string) []*receipts.ExportPage {
	t.Helper()
	var out []*receipts.ExportPage
	for _, line := range strings.Split(strings.TrimSuffix(file, "\n"), "\n") {
		p := &receipts.ExportPage{}
		if err := json.Unmarshal([]byte(line), p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// fileOf writes pages as an export's lines.
func fileOf(t *testing.T, pages []*receipts.ExportPage) string {
	t.Helper()
	var b strings.Builder
	for _, p := range pages {
		line, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// linkOf is the document of an exported link.
func linkOf(t *testing.T, l *receipts.ExportedLink) *receipts.Link {
	t.Helper()
	doc := &receipts.Link{}
	if err := json.Unmarshal(l.Doc, doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// exported finds link n in pages, every time it is there.
func exported(pages []*receipts.ExportPage, n uint64) []*receipts.ExportedLink {
	var out []*receipts.ExportedLink
	for _, p := range pages {
		for i := range p.Links {
			if p.Links[i].N == n {
				out = append(out, &p.Links[i])
			}
		}
	}
	return out
}

// recompute rewrites every link from n up to the top so that each names the
// one below as it now is, and every page's head the new top, as whoever
// edits a file and recomputes every digest above what they changed would;
// genesis, when set, is what link 1 names.
func recompute(t *testing.T, pages []*receipts.ExportPage, n uint64, genesis *receipts.Digest) {
	t.Helper()
	top := pages[0].Head.N
	var below *receipts.Digest
	if n > 1 {
		d := receipts.DigestOf(exported(pages, n-1)[0].Doc)
		below = &d
	} else if genesis != nil {
		below = genesis
	}
	for m := n; m <= top; m++ {
		ls := exported(pages, m)
		doc := linkOf(t, ls[0])
		for _, l := range ls {
			for _, r := range l.Receipts {
				if r.Doc != nil {
					doc.Receipts[r.Index].Digest = receipts.DigestOf(r.Doc)
				}
			}
		}
		if below != nil {
			doc.Prev = below
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range ls {
			l.Doc = b
		}
		d := receipts.DigestOf(b)
		below = &d
	}
	for _, p := range pages {
		if p.Head.N == top {
			p.Head.Digest = *below
		}
	}
}

// L24: an untouched export passes from its head to its genesis; with the
// head noted when it was made, all of it is authenticated; without one,
// none of it is, and it says so.
func TestL24AnUntouchedExportPasses(t *testing.T) {
	c := newChained(t)
	c.write(20)
	file, head := c.export()
	if head.N != 20 || head.Verdict != "ok" {
		t.Fatalf("the head: %+v", head)
	}
	code, out := verifyReceipts(t, file, noted(head))
	if code != 0 || !strings.Contains(out, "PASS  receipts") || !strings.Contains(out, "authenticated by a noted head: links 20 to 1") || strings.Contains(out, "not authenticated") {
		t.Fatalf("an untouched export with its head: %d\n%s", code, out)
	}
	code, out = verifyReceipts(t, file)
	if code != 0 || !strings.Contains(out, "not authenticated by a noted head: links 20 to 1") || !strings.Contains(out, "no head noted") {
		t.Fatalf("an untouched export with no head noted: %d\n%s", code, out)
	}
	// The JSON says the same.
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-json", "-receipts", path, "-receipts-head", noted(head)}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("-json: %d %s", code, stderr.String())
	}
	var res receiptsResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil || !res.OK || res.Links != 20 || res.Receipts != 20 || len(res.Authenticated) != 1 || res.Authenticated[0] != (span{From: 20, To: 1}) || len(res.Heads) != 1 || res.Heads[0].SHA256 != head.SHA256 {
		t.Fatalf("-json: %v %s", err, stdout.String())
	}
	// An empty chain is an export too.
	code, out = verifyReceipts(t, fileOf(t, []*receipts.ExportPage{{V: receipts.ExportVersion, Account: exportAccount, Links: []receipts.ExportedLink{}, Done: true}}))
	if code != 0 || !strings.Contains(out, "no link") {
		t.Fatalf("an empty chain: %d\n%s", code, out)
	}
}

// L24: a byte of any document changed without recomputing fails, wherever
// it is.
func TestL24AByteChangedFails(t *testing.T) {
	c := newChained(t)
	c.write(20)
	file, head := c.export()
	for name, change := range map[string]func([]*receipts.ExportPage){
		"the head's link": func(p []*receipts.ExportPage) {
			exported(p, 20)[0].Doc = bytes.Replace(exported(p, 20)[0].Doc, []byte(`"v":1`), []byte(`"v": 1`), 1)
		},
		"a link inside": func(p []*receipts.ExportPage) {
			exported(p, 11)[0].Doc = bytes.Replace(exported(p, 11)[0].Doc, []byte(`"v":1`), []byte(`"v": 1`), 1)
		},
		"link 1": func(p []*receipts.ExportPage) {
			exported(p, 1)[0].Doc = bytes.Replace(exported(p, 1)[0].Doc, []byte(`"v":1`), []byte(`"v": 1`), 1)
		},
		"a receipt": func(p []*receipts.ExportPage) {
			r := &exported(p, 11)[0].Receipts[0]
			r.Doc = bytes.Replace(r.Doc, []byte(`"ended"`), []byte(`"ended" `), 1)
		},
		"the head": func(p []*receipts.ExportPage) { p[0].Head.Digest[0] ^= 1 },
	} {
		pages := pagesOf(t, file)
		change(pages)
		for _, heads := range [][]string{{noted(head)}, nil} {
			if code, out := verifyReceipts(t, fileOf(t, pages), heads...); code != 1 || !strings.Contains(out, "FAIL  receipts") {
				t.Errorf("%s changed, heads %v: %d\n%s", name, heads, code, out)
			}
		}
	}
}

// L24: a receipt changed in link 11, link 11 and every link above
// recomputed and the export's head replaced: consistent, and not the head
// of 20 noted at the export. With only the head of 10 noted, it passes, and
// what is above 10 is printed as not authenticated.
func TestL24ARecomputedSuffix(t *testing.T) {
	c := newChained(t)
	c.write(20)
	file, head := c.export()
	pages := pagesOf(t, file)
	ten := receipts.DigestOf(exported(pages, 10)[0].Doc)
	r := &exported(pages, 11)[0].Receipts[0]
	r.Doc = bytes.Replace(r.Doc, []byte(`"ended"`), []byte(`"ended" `), 1)
	recompute(t, pages, 11, nil)
	tampered := fileOf(t, pages)
	if code, out := verifyReceipts(t, tampered, noted(head)); code != 1 || !strings.Contains(out, "not the head you noted") {
		t.Fatalf("with the head of 20 noted: %d\n%s", code, out)
	}
	code, out := verifyReceipts(t, tampered, "10:"+ten.String())
	if code != 0 || !strings.Contains(out, "authenticated by a noted head: links 10 to 1") || !strings.Contains(out, "not authenticated by a noted head: links 20 to 11") {
		t.Fatalf("with the head of 10 noted: %d\n%s", code, out)
	}
	// Both noted: the newest decides, and fails.
	if code, out := verifyReceipts(t, tampered, "10:"+ten.String(), noted(head)); code != 1 {
		t.Fatalf("with both noted: %d\n%s", code, out)
	}
}

// L24: the whole chain recomputed fails on the noted head; recomputed from
// another genesis, it fails on the genesis whatever is noted.
func TestL24TheWholeChainRecomputed(t *testing.T) {
	c := newChained(t)
	c.write(20)
	file, head := c.export()
	pages := pagesOf(t, file)
	r := &exported(pages, 1)[0].Receipts[0]
	r.Doc = bytes.Replace(r.Doc, []byte(`"ended"`), []byte(`"ended" `), 1)
	recompute(t, pages, 1, nil)
	if code, out := verifyReceipts(t, fileOf(t, pages), noted(head)); code != 1 || !strings.Contains(out, "not the head you noted") {
		t.Fatalf("the whole chain recomputed: %d\n%s", code, out)
	}
	other := receipts.Genesis("acc_bbbbbbbbbbbbbbbbbbbbbbbbbb")
	pages = pagesOf(t, file)
	recompute(t, pages, 1, &other)
	for _, heads := range [][]string{nil, {fmt.Sprintf("20:%s", pages[0].Head.Digest)}} {
		if code, out := verifyReceipts(t, fileOf(t, pages), heads...); code != 1 || !strings.Contains(out, "genesis") {
			t.Fatalf("another genesis, %v: %d\n%s", heads, code, out)
		}
	}
}

// L24: a noted head above the export's own fails: the file was cut short,
// or made before the head was noted.
func TestL24ANotedHeadAboveTheExports(t *testing.T) {
	c := newChained(t)
	c.write(20)
	file, head := c.export()
	if code, out := verifyReceipts(t, file, noted(head), "21:"+head.SHA256); code != 1 || !strings.Contains(out, "above the export's") {
		t.Fatalf("a noted head above: %d\n%s", code, out)
	}
	c.write(1)
	later, _ := c.export()
	h, _ := c.svc.Head(context.Background(), exportAccount)
	if code, out := verifyReceipts(t, file, fmt.Sprintf("21:%s", h.Digest)); code != 1 || !strings.Contains(out, "above the export's") {
		t.Fatalf("an older export against a newer head: %d\n%s", code, out)
	}
	// The newer export holds the older head: deleting nothing adds no link,
	// and a head noted is on every later chain.
	if code, out := verifyReceipts(t, later, noted(head), fmt.Sprintf("21:%s", h.Digest)); code != 0 || !strings.Contains(out, "authenticated by a noted head: links 21 to 1") {
		t.Fatalf("a newer export against both heads: %d\n%s", code, out)
	}
}

// L24: links below a break are printed as not authenticated, down to the
// next noted head.
func TestL24BelowABreakNothingIsAuthenticated(t *testing.T) {
	c := newChained(t)
	c.write(20)
	c.deleteEntry(receipts.LinkKeyPrefix(8))
	file, head := c.export()
	code, out := verifyReceipts(t, file, noted(head))
	if code != 0 || !strings.Contains(out, "authenticated by a noted head: links 20 to 9") || !strings.Contains(out, "not authenticated by a noted head: links 8 to 1") || !strings.Contains(out, "BREAK link 8 lost") {
		t.Fatalf("a break: %d\n%s", code, out)
	}
	pages := pagesOf(t, file)
	five := receipts.DigestOf(exported(pages, 5)[0].Doc)
	code, out = verifyReceipts(t, file, noted(head), "5:"+five.String())
	if code != 0 || !strings.Contains(out, "not authenticated by a noted head: links 8 to 6") || !strings.Contains(out, "authenticated by a noted head: links 20 to 9, 5 to 1") {
		t.Fatalf("a break and a head noted below it: %d\n%s", code, out)
	}
	// A head noted at the break has nothing to match.
	if code, out := verifyReceipts(t, file, "8:"+five.String()); code != 1 || !strings.Contains(out, "link 8") {
		t.Fatalf("a head noted at a lost link: %d\n%s", code, out)
	}
}

// L25's offline half: one receipt deleted from the table between two
// exports. The second's walk names it missing, its offline check prints it,
// and the first's head is on the second, since deleting adds no link. The
// same with a link's value deleted, and with both: the broken number is
// printed, and no receipt's identifier is invented from a digest.
func TestL25ADeletionBetweenTwoExports(t *testing.T) {
	for name, del := range map[string][]string{
		"a receipt": {"receipt"},
		"a link":    {"link"},
		"both":      {"receipt", "link"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newChained(t)
			ids := c.write(12)
			first, head := c.export()
			if code, out := verifyReceipts(t, first, noted(head)); code != 0 || strings.Contains(out, "BREAK") {
				t.Fatalf("the first export: %d\n%s", code, out)
			}
			for _, d := range del {
				switch d {
				case "receipt":
					c.deleteEntry(ids[6]) // link 7's
				case "link":
					c.deleteEntry(receipts.LinkKeyPrefix(5))
				}
			}
			second, again := c.export()
			if again != head {
				t.Fatalf("the head moved: %+v, %+v", again, head)
			}
			code, out := verifyReceipts(t, second, noted(head))
			if code != 0 || !strings.Contains(out, "authenticated by a noted head: links 12 to") {
				t.Fatalf("the second export with the first's head: %d\n%s", code, out)
			}
			for _, d := range del {
				switch d {
				case "receipt":
					if !strings.Contains(out, "BREAK receipt 7.0 "+ids[6]+" missing") {
						t.Fatalf("the deleted receipt is not printed:\n%s", out)
					}
				case "link":
					if !strings.Contains(out, "BREAK link 5 lost") || strings.Contains(out, ids[4]) {
						t.Fatalf("the lost link, or a receipt's identifier made up:\n%s", out)
					}
				}
			}
		})
	}
}

// The builder of exports in every shape a page can have: links of every
// kind and verdict, receipts of any size, pages cut by a number of
// receipts as the service cuts them by bytes.

type synthLink struct {
	kind    receipts.LinkKind
	verdict receipts.LinkVerdict // "" is LinkOK
	br      receipts.BreakReason
	docs    [][]byte
	// receipts' verdicts, ok when unset
	verdicts map[int]receipts.ReceiptVerdict
}

// synth builds the links' documents, the oldest first, each naming the one
// before (or its break), and returns their bytes.
func synth(t *testing.T, links []synthLink) [][]byte {
	t.Helper()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var out [][]byte
	prev := receipts.Genesis(exportAccount)
	id := 0
	for i, s := range links {
		l := &receipts.Link{V: receipts.LinkVersion, N: uint64(i + 1), Kind: s.kind, At: at}
		if l.Kind == "" {
			l.Kind = receipts.LinkReceipts
		}
		if l.Kind == receipts.LinkAdopted {
			l.Before = at
		}
		if s.br != "" && i > 0 {
			l.Break = s.br
		} else {
			p := prev
			l.Prev = &p
		}
		for _, d := range s.docs {
			id++
			l.Receipts = append(l.Receipts, receipts.Ref{ID: fmt.Sprintf("%s%016x%0*x", receipts.IDPrefix, at.UnixNano(), receipts.IDLength-len(receipts.IDPrefix)-16, id), Digest: receipts.DigestOf(d)})
		}
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Check(); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
		prev = receipts.DigestOf(b)
	}
	return out
}

// paginate is the export of links, every page at most perPage receipts, as
// the service pages it: newest first, ending inside a link or at one.
func paginate(t *testing.T, links []synthLink, perPage int) []*receipts.ExportPage {
	t.Helper()
	docs := synth(t, links)
	top := uint64(len(links))
	head := receipts.Head{N: top}
	if top > 0 {
		head.Verdict = receipts.LinkOK
		if v := links[top-1].verdict; v != "" {
			head.Verdict = v
		}
		if head.Verdict == receipts.LinkOK {
			head.Digest, head.At = receipts.DigestOf(docs[top-1]), time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
		}
	}
	var pages []*receipts.ExportPage
	n, from := top, -1
	for {
		p := &receipts.ExportPage{V: receipts.ExportVersion, Account: exportAccount, Head: head, Links: []receipts.ExportedLink{}}
		count := 0
		ended := false
		for ; n >= 1 && !ended; n-- {
			if count == perPage {
				p.Next, ended = receipts.Position{N: n + 1}, true
				break
			}
			s := links[n-1]
			e := receipts.ExportedLink{N: n, Verdict: s.verdict}
			if e.Verdict == "" {
				e.Verdict = receipts.LinkOK
			}
			if !e.Verdict.Readable() {
				p.Links = append(p.Links, e)
				continue
			}
			e.Doc = docs[n-1]
			doc := &receipts.Link{}
			_ = json.Unmarshal(e.Doc, doc)
			i := len(s.docs) - 1
			if from >= 0 {
				i = from - 1
			}
			from = -1
			for ; i >= 0; i-- {
				if count == perPage {
					p.Next, ended = receipts.Position{N: n, Index: i + 1}, true
					break
				}
				r := receipts.ExportedReceipt{Index: i, ID: doc.Receipts[i].ID, Verdict: receipts.ReceiptOK, Doc: s.docs[i]}
				if v, ok := s.verdicts[i]; ok {
					r.Verdict = v
					if v == receipts.ReceiptMissing || v == receipts.ReceiptUnreadable {
						r.Doc = nil
					}
				}
				e.Receipts = append(e.Receipts, r)
				count++
			}
			if len(e.Receipts) > 0 || !ended {
				p.Links = append(p.Links, e)
			} else {
				p.Next = receipts.Position{N: n + 1}
			}
			if ended {
				break
			}
		}
		if ended {
			if p.Next.Index > 0 {
				n, from = p.Next.N, p.Next.Index
			} else {
				n = p.Next.N - 1
			}
		} else {
			p.Done = true
		}
		if err := p.Check(); err != nil {
			t.Fatalf("a built page off its contract: %v", err)
		}
		pages = append(pages, p)
		if p.Done {
			return pages
		}
	}
}

func docs(n int, prefix string) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = fmt.Appendf(nil, `{"v":1,"note":"%s %d"}`, prefix, i)
	}
	return out
}

// shapes is a chain of every kind: adopted links first, a link of many
// receipts, a recovered link, a link changed and one whose receipt is
// missing, one expired, a break and its link before lost.
func shapes() []synthLink {
	return []synthLink{
		{kind: receipts.LinkAdopted, docs: docs(3, "adopted")},
		{kind: receipts.LinkAdopted, docs: docs(2, "adopted again")},
		{verdict: receipts.LinkExpired, docs: docs(1, "expired")},
		{docs: docs(7, "many")},
		{verdict: receipts.LinkLost, docs: docs(1, "lost")},
		{br: receipts.BreakLost, docs: docs(2, "after the break")},
		{kind: receipts.LinkRecovered, docs: docs(2, "recovered")},
		{docs: docs(3, "one missing"), verdicts: map[int]receipts.ReceiptVerdict{1: receipts.ReceiptMissing, 2: receipts.ReceiptUnreadable}},
		{docs: docs(5, "last")},
	}
}

// L24: exports of every shape pass at every page size, and say what they
// hold: the breaks, the recovered and adopted links, every page's head.
func TestL24ExportsOfEveryShapePass(t *testing.T) {
	links := shapes()
	digests := synth(t, links)
	top := fmt.Sprintf("%d:%s", len(links), receipts.DigestOf(digests[len(digests)-1]))
	for _, per := range []int{1, 2, 3, 4, 7, 100} {
		pages := paginate(t, links, per)
		code, out := verifyReceipts(t, fileOf(t, pages), top)
		for _, want := range []string{
			"PASS  receipts", "authenticated by a noted head: links 9 to 6", "not authenticated by a noted head: links 5 to 1",
			"BREAK link 5 lost", "BREAK receipt 8.1", "missing", "BREAK receipt 8.2", "unreadable",
			"NOTE  link 7 recovered", "NOTE  links 2 to 1 adopted", "NOTE  link 3 expired", "NOTE  link 6 names no link before it (lost)",
			fmt.Sprintf("head 9 ok sha256 %s", receipts.DigestOf(digests[8])),
		} {
			if code != 0 || !strings.Contains(out, want) {
				t.Fatalf("%d a page: %d, no %q in\n%s", per, code, want, out)
			}
		}
	}
	// A changed receipt the enclave reported is a break, and its digest
	// must not match.
	links[3].verdicts = map[int]receipts.ReceiptVerdict{2: receipts.ReceiptChanged}
	pages := paginate(t, links, 3)
	for _, l := range exported(pages, 4) {
		for i := range l.Receipts {
			if l.Receipts[i].Index == 2 {
				l.Receipts[i].Doc = []byte(`{"changed":true}`)
			}
		}
	}
	if code, out := verifyReceipts(t, fileOf(t, pages), top); code != 0 || !strings.Contains(out, "BREAK receipt 4.2") || !strings.Contains(out, "changed") {
		t.Fatalf("a changed receipt: %d\n%s", code, out)
	}
	for _, l := range exported(pages, 4) {
		for i := range l.Receipts {
			if l.Receipts[i].Index == 2 {
				l.Receipts[i].Doc = docs(7, "many")[2]
			}
		}
	}
	if code, out := verifyReceipts(t, fileOf(t, pages), top); code != 1 {
		t.Fatalf("a receipt said changed that is not: %d\n%s", code, out)
	}
	// A link the enclave found replaced by another that opens (prev) is a
	// break: its digest is not what the link above names, and the chain
	// below goes on from what it names.
	pages = paginate(t, shapes(), 3)
	for _, l := range exported(pages, 2) {
		l.Verdict, l.Doc = receipts.LinkPrev, bytes.Replace(l.Doc, []byte(`"n":2`), []byte(`"n":2 `), 1)
	}
	if code, out := verifyReceipts(t, fileOf(t, pages), top); code != 0 || !strings.Contains(out, "BREAK link 2 prev") {
		t.Fatalf("a link replaced: %d\n%s", code, out)
	}
}

// A noted head authenticates down to the first break only: a link that
// names no link before it, or one found replaced (prev), ends what it
// authenticates, even when the links below it read well.
func TestL24ANotedHeadStopsAtABreak(t *testing.T) {
	for name, c := range map[string]struct {
		links  []synthLink
		change func([]*receipts.ExportPage)
		want   []string
	}{
		"a link naming no link before it": {
			links: []synthLink{{docs: docs(1, "a")}, {docs: docs(1, "b")}, {br: receipts.BreakForked, docs: docs(1, "c")}, {docs: docs(1, "d")}},
			want:  []string{"authenticated by a noted head: links 4 to 3", "not authenticated by a noted head: links 2 to 1", "NOTE  link 3 names no link before it (forked)"},
		},
		"a link replaced": {
			links: []synthLink{{docs: docs(1, "a")}, {docs: docs(1, "b")}, {docs: docs(1, "c")}, {docs: docs(1, "d")}},
			change: func(p []*receipts.ExportPage) {
				for _, l := range exported(p, 2) {
					l.Verdict, l.Doc = receipts.LinkPrev, bytes.Replace(l.Doc, []byte(`"n":2`), []byte(`"n":2 `), 1)
				}
			},
			want: []string{"authenticated by a noted head: links 4 to 3", "not authenticated by a noted head: links 2 to 1", "BREAK link 2 prev"},
		},
	} {
		docs := synth(t, c.links)
		top := fmt.Sprintf("4:%s", receipts.DigestOf(docs[3]))
		for _, per := range []int{1, 100} {
			pages := paginate(t, c.links, per)
			if c.change != nil {
				c.change(pages)
			}
			code, out := verifyReceipts(t, fileOf(t, pages), top)
			for _, want := range c.want {
				if code != 0 || !strings.Contains(out, want) {
					t.Fatalf("%s, %d a page: %d, no %q in\n%s", name, per, code, want, out)
				}
			}
		}
	}
}

// A file that cannot be read to its end fails.
func TestL24AnExportReadHalfFails(t *testing.T) {
	first, _, _ := strings.Cut(fileOf(t, paginate(t, shapes(), 3)), "\n")
	r := io.MultiReader(strings.NewReader(first+"\n"), iotest.ErrReader(errors.New("the disk failed")))
	if res, err := checkExport(r, notedHeads{}); err == nil || err.Error() != "the disk failed" || res.OK {
		t.Fatalf("%+v, %v", res, err)
	}
}

// ExportPage.Check checks one page's structure; the export as a whole must
// also be complete (the third design review's P2 for pull request 3): no
// ref or link left out, no page dropped, repeated or out of order, no Done
// before link 1, no page past the last, every page of the same account,
// each beginning where the one before ended.
func TestL24TheWholeExportIsComplete(t *testing.T) {
	links := shapes()
	digests := synth(t, links)
	top := fmt.Sprintf("%d:%s", len(links), receipts.DigestOf(digests[len(digests)-1]))
	for name, c := range map[string]struct {
		change func([]*receipts.ExportPage) []*receipts.ExportPage
		says   string
	}{
		"a receipt left out of a link": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			l := exported(p, 4)[0]
			l.Receipts = append(l.Receipts[:1:1], l.Receipts[2:]...)
			return p
		}, "left out"},
		"a link's last receipts left out": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			l := exported(p, 9)[0]
			l.Receipts = l.Receipts[:len(l.Receipts)-1]
			return p
		}, "left out"},
		"a link left out": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			// The seventh page holds links 4, 3 and 2.
			p[6].Links = append(p[6].Links[:1:1], p[6].Links[2:]...)
			return p
		}, "left out"},
		"a page left out":          {func(p []*receipts.ExportPage) []*receipts.ExportPage { return append(p[:1:1], p[2:]...) }, "left out"},
		"the first page left out":  {func(p []*receipts.ExportPage) []*receipts.ExportPage { return p[1:] }, "left out"},
		"the first pages left out": {func(p []*receipts.ExportPage) []*receipts.ExportPage { return p[3:] }, "does not begin at its head"},
		"a link's receipts left out, not the page's last": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[6].Links[1].Receipts = nil
			return p
		}, "left out"},
		"the last page left out": {func(p []*receipts.ExportPage) []*receipts.ExportPage { return p[:len(p)-1] }, "ends before"},
		"done too soon": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[1].Done, p[1].Next = true, receipts.Position{}
			return p[:2]
		}, "done before"},
		"a page after the last": {func(p []*receipts.ExportPage) []*receipts.ExportPage { return append(p, p[len(p)-1]) }, "after the last"},
		"a page repeated":       {func(p []*receipts.ExportPage) []*receipts.ExportPage { return append(p[:2:2], p[1:]...) }, "where"},
		"two pages swapped": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[1], p[2] = p[2], p[1]
			return p
		}, "where"},
		"a page's next moved down": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[0].Next.N--
			return p
		}, "next"},
		"another account's page": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[1].Account = "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"
			return p
		}, "account"},
		"a receipt named as another": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			exported(p, 9)[0].Receipts[0].ID = exported(p, 4)[0].Receipts[0].ID
			return p
		}, "not the one"},
		"a link continued as another": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			for _, l := range exported(p, 4)[1:] {
				l.Doc = exported(p, 9)[0].Doc
			}
			return p
		}, "changed between"},
		"the head going back": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[2].Head = receipts.Head{N: 8, Verdict: receipts.LinkOK, Digest: receipts.DigestOf(digests[7])}
			return p
		}, "head"},
		"the head of a page changed": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[2].Head.Digest[0] ^= 1
			return p
		}, "head"},
		"a link said prev that is not": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			exported(p, 4)[0].Verdict = receipts.LinkPrev
			return p
		}, "prev"},
		"a receipt said ok that is not": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			for _, l := range exported(p, 8) {
				for i := range l.Receipts {
					if l.Receipts[i].Index == 1 {
						l.Receipts[i].Verdict, l.Receipts[i].Doc = receipts.ReceiptOK, []byte(`{}`)
					}
				}
			}
			return p
		}, "receipt 8.1"},
		"a document that is not a link": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			exported(p, 9)[0].Doc = []byte(`{"v":1,"note":"last 0"}`)
			return p
		}, "not a link"},
		"a link of another number": {func(p []*receipts.ExportPage) []*receipts.ExportPage {
			p[0].Links[0].Doc = digests[7]
			return p
		}, "not a link"},
	} {
		pages := c.change(paginate(t, links, 3))
		if code, out := verifyReceipts(t, fileOf(t, pages), top); code != 1 || !strings.Contains(out, "FAIL  receipts") || !strings.Contains(out, c.says) {
			t.Errorf("%s: %d, want %q in\n%s", name, code, c.says, out)
		}
	}
}

// L24: a malformed page, a page past its bounds (ExportPage.Check), a file
// that is not an export, and flags that are not heads fail.
func TestL24AMalformedExportFails(t *testing.T) {
	good := fileOf(t, paginate(t, shapes(), 100))
	line := strings.TrimSuffix(good, "\n")
	tooMany := paginate(t, []synthLink{{docs: docs(1, "x")}}, 100)
	for range receipts.WalkLinksMax {
		tooMany[0].Links = append(tooMany[0].Links, receipts.ExportedLink{N: 1, Verdict: receipts.LinkLost})
	}
	for name, file := range map[string]string{
		"empty":                    "",
		"not JSON":                 "{\n",
		"two values a line":        line + " {}\n",
		"a field it does not know": strings.Replace(line, `"v":1,`, `"v":1,"extra":1,`, 1) + "\n",
		"a page of version 2":      strings.Replace(line, `"v":1,"account"`, `"v":2,"account"`, 1) + "\n",
		"a blank line":             good + "\n" + good,
		"a page past its bounds":   fileOf(t, tooMany),
		"a line past a page":       `{"v":1,"account":"` + strings.Repeat("a", maxExportLine) + `"}` + "\n",
	} {
		if code, out := verifyReceipts(t, file); code != 1 || !strings.Contains(out, "FAIL  receipts") {
			t.Errorf("%s: %d\n%s", name, code, out)
		}
	}
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		{"-receipts-head", "1:" + strings.Repeat("a", 64)},
		{"-receipts", "x", "-receipts-head", "1"},
		{"-receipts", "x", "-receipts-head", "0:" + strings.Repeat("a", 64)},
		{"-receipts", "x", "-receipts-head", "1:" + strings.Repeat("A", 64)},
		{"-receipts", "x", "-receipts-head", "a:" + strings.Repeat("a", 64)},
		{"-receipts", "x", "-receipts-head", "1:" + strings.Repeat("a", 64), "-receipts-head", "1:" + strings.Repeat("b", 64)},
	} {
		if code := run(args, &stdout, &stderr, nil); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if code := run([]string{"-receipts", filepath.Join(t.TempDir(), "none")}, &stdout, &stderr, nil); code != 1 {
		t.Errorf("a file that is not there: %d", code)
	}
}
