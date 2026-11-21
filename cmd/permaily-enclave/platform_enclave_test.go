//go:build !dev

package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/store"
)

// /status says that one of the model's records went back, and never which
// one or at which version: the versions of a month's spend say how much
// was spent (ADR 0028, "What /status and permaily-verify show").
func TestAnchorStatusSaysTheModelsRecordsWentBackWithoutTheirVersions(t *testing.T) {
	ctx := context.Background()
	mem, commits := store.NewMemory(), store.NewMemoryCommits()
	tls := &store.Anchored{Records: mem, Commits: commits}
	model := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true}
	if err := model.CreateNew(ctx, &store.Record{Name: "model-spend-2026-11", Value: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	names, err := mem.ListRecords(ctx, "model-spend-2026-11", 10)
	if err != nil || len(names) == 0 {
		t.Fatalf("%v %v", names, err)
	}
	for _, name := range names {
		if err := mem.DeleteRecord(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := model.GetRecordAt(ctx, "model-spend-2026-11"); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a record whose value was deleted: %v", err)
	}
	st := anchorStatus(tls, nil, nil, model, nil, nil, "anchors", 3650)
	if len(st.Records) != 0 || len(st.Regressions) != 1 {
		t.Fatalf("%+v", st)
	}
	if r := st.Regressions[0]; r.Name != modelRecord || !r.Lost || r.Had != 0 || r.Found != 0 || r.At.IsZero() {
		t.Fatalf("%+v", r)
	}
	if st := anchorStatus(tls, nil, nil, nil, nil, nil, "anchors", 3650); len(st.Regressions) != 0 {
		t.Fatalf("an image without the model: %+v", st)
	}
}

// V57 (ADR 0030, "The image"): the image fetches vendors' documents through
// the host's vendor egress on vsock port 8004 and nothing else: with the
// configuration main reads (newConfig, its defaults and flags, given no
// flag, as the entry point gives neither port), the image's composition
// asks for that client alone, on the port the host listens on by default
// and never on the egress port; the deployment's entry point names no
// other port; and no file of the image but the composition makes a vendor
// client.
func TestV57TheVendorsAreFetchedThroughTheVendorEgress(t *testing.T) {
	var ports []uint32
	old := vendorEgress
	vendorEgress = func(port uint32) *http.Client {
		ports = append(ports, port)
		return old(port)
	}
	t.Cleanup(func() { vendorEgress = old })
	w := newConnectionsWorld(t)
	fs := flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	cfg := newConfig(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	p := &platform{accounts: w.parts.accounts, mailboxes: w.parts.mailboxes, model: w.parts.model, receipts: w.parts.receipts, rulesRecords: w.parts.rulesRecords}
	c, err := enclaveConnections(*cfg, router.HostsForDomain(cfg.domain), w.records, w.table, w.keys, p, slog.New(slog.DiscardHandler))
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0] != 8004 || ports[0] == cfg.egressPort || cfg.egressPort != 8000 {
		t.Fatalf("the vendor egress was asked on %v (the egress is on %d)", ports, cfg.egressPort)
	}
	// The flag, when an operator gives it, is the port the composition asks.
	ports = nil
	fs = flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	cfg = newConfig(fs)
	if err := fs.Parse([]string{"-vendor-egress-port", "9004"}); err != nil {
		t.Fatal(err)
	}
	if _, err := enclaveConnections(*cfg, router.HostsForDomain(cfg.domain), w.records, w.table, w.keys, p, slog.New(slog.DiscardHandler)); err != nil || len(ports) != 1 || ports[0] != 9004 {
		t.Fatalf("-vendor-egress-port 9004: asked on %v (%v)", ports, err)
	}
	// The host listens there by default, and the entry point changes
	// neither port.
	host, err := os.ReadFile("../permaily-host/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(host), "vendorPort = uint32(8004)") {
		t.Fatal("the host's vendor egress is not on 8004 by default")
	}
	eif, err := os.ReadFile("../../nix/eif.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-vendor-egress-port", "-egress-port"} {
		if strings.Contains(string(eif), `"`+flag+`"`) {
			t.Fatalf("the entry point sets %s", flag)
		}
	}
	// Only the composition makes a vendor client: cimd.New once, over the
	// client it is given, and enclave.VendorEgressClient only as
	// vendorEgress.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var code []string // the file without its comments
		for _, line := range strings.Split(string(raw), "\n") {
			if c, _, _ := strings.Cut(line, "//"); strings.TrimSpace(c) != "" {
				code = append(code, c)
			}
		}
		src := strings.Join(code, "\n")
		for _, call := range []string{"cimd.New(", "enclave.VendorEgressClient", "vendorEgress(", "Vendors:", "enclaveConnections(cfg, "} {
			count[call] += strings.Count(src, call)
		}
	}
	// newPlatform hands enclaveConnections the configuration run was given,
	// as it is: the one place the image composes its connections.
	if count["cimd.New("] != 1 || count["enclave.VendorEgressClient"] != 1 || count["vendorEgress("] != 1 || count["Vendors:"] != 1 || count["enclaveConnections(cfg, "] != 1 {
		t.Fatalf("vendor clients made: %v", count)
	}
}

// /status says that one of the connections' records went back, and never
// which one or at which version: their names hold identifiers, and their
// versions how often a connection was used.
func TestAnchorStatusSaysTheConnectionsRecordsWentBackWithoutTheirVersions(t *testing.T) {
	ctx := context.Background()
	mem, commits := store.NewMemory(), store.NewMemoryCommits()
	tls := &store.Anchored{Records: mem, Commits: commits}
	conns := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true}
	const name = "connection-con_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := conns.CreateNew(ctx, &store.Record{Name: name, Value: []byte("signed")}); err != nil {
		t.Fatal(err)
	}
	names, err := mem.ListRecords(ctx, name, 10)
	if err != nil || len(names) == 0 {
		t.Fatalf("%v %v", names, err)
	}
	for _, n := range names {
		if err := mem.DeleteRecord(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := conns.GetRecordAt(ctx, name); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a record whose value was deleted: %v", err)
	}
	st := anchorStatus(tls, nil, nil, nil, conns, nil, "anchors", 3650)
	if len(st.Records) != 0 || len(st.Regressions) != 1 {
		t.Fatalf("%+v", st)
	}
	if r := st.Regressions[0]; r.Name != connectionRecord || !r.Lost || r.Had != 0 || r.Found != 0 || r.At.IsZero() || strings.Contains(r.Name, "con_") {
		t.Fatalf("%+v", r)
	}
}

// L21 (ADR 0037, "Where a link is kept"): the image's commits in the
// anchors bucket group the chain's records as receipts.CommitGroup says
// and no other name, so the chain's strict Anchored can list an account's
// links together and no Anchored reads a grouped name's commits anywhere
// else; every commit is kept for the image's retention; and newPlatform
// builds the one store of commits it shares with anchorCommits and gives
// the chain's records their own strict Anchored over the chain's table.
func TestL21TheImagesCommitsGroupTheChain(t *testing.T) {
	key := func(context.Context) ([]byte, error) { return []byte("k"), nil }
	c := anchorCommits(nil, "anchors", key, 3650)
	if c.Bucket != "anchors" || c.Retention.Hours() != 3650*24 || !chainGrouped(c) {
		t.Fatalf("%+v", c)
	}
	if _, _, ok := c.Group("account-acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"); ok {
		t.Fatal("an account's record is grouped")
	}
	src, err := os.ReadFile("platform_enclave.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"commits := anchorCommits(s3Client, cfg.anchorBucket, root.CommitKey, cfg.anchorRetentionDays)",
		"chainRecords = &store.Anchored{Records: receipts.ChainTable{Entries: awsStore}, Commits: commits, Strict: true}",
		"composeReceipts(receiptsParts{chain: chainRecords,",
		"st.Regressions = append(st.Regressions, receiptRegressions(chainRecords)...)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("newPlatform does not say %q", want)
		}
	}
	if strings.Count(string(src), "&store.S3Commits{") != 1 {
		t.Error("the image makes more than one store of commits")
	}
}

// ADR 0041, pull request 7: the image composes one queue of notices over
// the deployment's table and the root secret's keys, before accounts, which
// it gives the queue and then makes its Deliverer; receipts and connections
// queue through it; every enclave runs its runner, and /status reports its
// counts in mail.notices.
func TestTheImageComposesTheQueueOfNotices(t *testing.T) {
	src, err := os.ReadFile("platform_enclave.go")
	if err != nil {
		t.Fatal(err)
	}
	platform := string(src)
	for _, want := range []string{
		"composeNotices(noticesParts{entries: awsStore, keys: root, log: log})",
		"Notices: p.notices.svc,",
		"p.notices.deliverBy(p.accounts)",
		"accounts: p.accounts, notices: p.notices, log: log})",
		"receipts: p.receipts, notices: p.notices, log: log,",
	} {
		if strings.Count(platform, want) != 1 {
			t.Errorf("newPlatform does not say %q once", want)
		}
	}
	if strings.Count(platform, "composeNotices(") != 1 || strings.Index(platform, "composeNotices(") > strings.Index(platform, "p.signIn(") {
		t.Error("the image does not compose one queue before accounts")
	}
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"runNotices(ctx, p.notices)", "stopNotices(p.notices, log)", "status.Notices = p.notices.status"} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.go does not say %q", want)
		}
	}
}
