// Package receipts checks the web app's download for checking against
// permaily-verify -receipts (ADR 0041, E18): tests only, never in the image.
//
// export.jsonl is a played export, one page a line, built here from
// pkg/receiptchain (go test ./web/tests/receipts -update writes it again).
// The web test (web/tests/lib/export.test.ts) plays it as the server's
// pages, downloads it whole and in parts, and compares what it saved with
// this folder's files: export.jsonl for the whole file, and parts/ for the
// parts and their manifest. This test runs the real permaily-verify on the
// whole file and on the parts joined in the manifest's order, and requires
// it to fail when a part is left out.
package receipts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/receiptchain"
)

var update = flag.Bool("update", false, "write export.jsonl again")

const account = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

// links and perLink shape the played export: a page a link, so the file
// has several lines for the parts to split between.
const (
	links   = 6
	perLink = 2
)

// played builds the export: links links of perLink receipts each, the
// oldest first, every one naming the one before, then a page a link, the
// newest first, as the service pages it.
func played(t *testing.T) []byte {
	t.Helper()
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	prev := receiptchain.Genesis(account)
	var docs [][]byte
	var receiptDocs [][][]byte
	var refs [][]receiptchain.Ref
	id := 0
	for n := 1; n <= links; n++ {
		l := &receiptchain.Link{V: receiptchain.LinkVersion, N: uint64(n), Kind: receiptchain.LinkReceipts, At: at}
		p := prev
		l.Prev = &p
		var mine [][]byte
		for range perLink {
			id++
			doc := fmt.Appendf(nil, `{"v":2,"id":"played receipt %d","note":"a played receipt of the web test, for permaily-verify"}`, id)
			mine = append(mine, doc)
			l.Receipts = append(l.Receipts, receiptchain.Ref{
				ID:     fmt.Sprintf("%s%016x%0*x", receiptchain.IDPrefix, at.UnixNano(), receiptchain.IDLength-len(receiptchain.IDPrefix)-16, id),
				Digest: receiptchain.DigestOf(doc),
			})
		}
		if err := l.Check(); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, b)
		receiptDocs = append(receiptDocs, mine)
		refs = append(refs, l.Receipts)
		prev = receiptchain.DigestOf(b)
	}
	head := receiptchain.Head{N: links, Verdict: receiptchain.LinkOK, Digest: prev, At: at}
	var file bytes.Buffer
	for n := links; n >= 1; n-- {
		e := receiptchain.ExportedLink{N: uint64(n), Verdict: receiptchain.LinkOK, Doc: docs[n-1]}
		for i := perLink - 1; i >= 0; i-- {
			e.Receipts = append(e.Receipts, receiptchain.ExportedReceipt{Index: i, ID: refs[n-1][i].ID, Verdict: receiptchain.ReceiptOK, Doc: receiptDocs[n-1][i]})
		}
		page := &receiptchain.ExportPage{V: receiptchain.ExportVersion, Account: account, Head: head, Links: []receiptchain.ExportedLink{e}}
		if n == 1 {
			page.Done = true
		} else {
			page.Next = receiptchain.Position{N: uint64(n)}
		}
		if err := page.Check(); err != nil {
			t.Fatalf("a played page off its contract: %v", err)
		}
		// As the route writes a page: json.Encoder, one line.
		if err := json.NewEncoder(&file).Encode(page); err != nil {
			t.Fatal(err)
		}
	}
	return file.Bytes()
}

func TestE18PlayedExportIsCurrent(t *testing.T) {
	got := played(t)
	if *update {
		if err := os.WriteFile("export.jsonl", got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("export.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("export.jsonl is not the played export: go test ./web/tests/receipts -update")
	}
}

var (
	verifyOnce sync.Once
	verifyPath string
	verifyErr  error
)

// verifier builds permaily-verify once, from this checkout.
func verifier(t *testing.T) string {
	t.Helper()
	verifyOnce.Do(func() {
		dir, err := os.MkdirTemp("", "permaily-verify-")
		if err != nil {
			verifyErr = err
			return
		}
		verifyPath = filepath.Join(dir, "permaily-verify")
		// The toolchain running this test, else the one on the path.
		gobin := "go"
		if root := runtime.GOROOT(); root != "" { //nolint:staticcheck // the toolchain that built this test
			gobin = filepath.Join(root, "bin", "go")
		}
		cmd := exec.Command(gobin, "build", "-o", verifyPath, "../../../cmd/permaily-verify")
		if out, err := cmd.CombinedOutput(); err != nil {
			verifyErr = fmt.Errorf("go build: %w: %s", err, out)
		}
	})
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	return verifyPath
}

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if verifyPath != "" {
		_ = os.RemoveAll(filepath.Dir(verifyPath))
	}
	os.Exit(code)
}

// check runs permaily-verify -receipts on file with the head noted, and
// says whether it passed and what it printed.
func check(t *testing.T, file []byte, head string) (bool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(verifier(t), "-receipts", path, "-receipts-head", head).CombinedOutput() //nolint:gosec // the binary built above
	if err != nil {
		if exited := (*exec.ExitError)(nil); !errors.As(err, &exited) {
			t.Fatal(err)
		}
		return false, string(out)
	}
	return true, string(out)
}

// manifest is what the web app writes after a download's last part.
type manifest struct {
	Complete bool   `json:"complete"`
	File     string `json:"file"`
	Date     string `json:"date"`
	Head     string `json:"head"`
	Parts    []struct {
		Name   string `json:"name"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"parts"`
}

// parts reads the parts the web test saved, each checked against the
// manifest's bytes and digest, in the manifest's order.
func parts(t *testing.T) (*manifest, [][]byte) {
	t.Helper()
	names, err := filepath.Glob("parts/*-jsonl-manifest.json")
	if err != nil || len(names) != 1 {
		t.Fatalf("one manifest in parts/: %v %v", names, err)
	}
	b, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	m := &manifest{}
	if err := json.Unmarshal(b, m); err != nil {
		t.Fatal(err)
	}
	if !m.Complete || m.File != "jsonl" || len(m.Parts) < 2 {
		t.Fatalf("not the manifest of a download in parts: %+v", m)
	}
	var out [][]byte
	for _, p := range m.Parts {
		b, err := os.ReadFile(filepath.Join("parts", p.Name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if len(b) != p.Bytes || hex.EncodeToString(sum[:]) != p.SHA256 {
			t.Fatalf("%s is not the part its manifest names", p.Name)
		}
		out = append(out, b)
	}
	return m, out
}

func TestE18VerifierPassesOnTheDownload(t *testing.T) {
	file, err := os.ReadFile("export.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parts(t)
	if ok, out := check(t, file, m.Head); !ok {
		t.Fatalf("permaily-verify refuses the played export:\n%s", out)
	}
}

func TestE18VerifierPassesOnThePartsJoinedInOrder(t *testing.T) {
	m, ps := parts(t)
	joined := bytes.Join(ps, nil)
	whole, err := os.ReadFile("export.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(joined, whole) {
		t.Fatal("the parts joined are not the whole download")
	}
	for i, p := range ps {
		if len(p) == 0 || p[len(p)-1] != '\n' || !json.Valid(bytes.SplitN(p, []byte("\n"), 2)[0]) {
			t.Fatalf("part %d is not whole lines of whole pages", i+1)
		}
	}
	if ok, out := check(t, joined, m.Head); !ok {
		t.Fatalf("permaily-verify refuses the parts joined:\n%s", out)
	}
}

func TestE18VerifierFailsOnAPartLeftOut(t *testing.T) {
	m, ps := parts(t)
	for left := range ps {
		var joined [][]byte
		for i, p := range ps {
			if i != left {
				joined = append(joined, p)
			}
		}
		if ok, out := check(t, bytes.Join(joined, nil), m.Head); ok {
			t.Fatalf("permaily-verify passes the parts without part %d:\n%s", left+1, out)
		} else if strings.TrimSpace(out) == "" {
			t.Fatalf("permaily-verify said nothing of the parts without part %d", left+1)
		}
	}
}
