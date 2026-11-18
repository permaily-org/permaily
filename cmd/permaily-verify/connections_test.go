package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// imageConnections is /status's connections as the image composes them
// (testdata/connections-status.json, which cmd/permaily-enclave's
// TestV56TheConnectionsOnStatus checks is what the image says).
func imageConnections(t *testing.T) *attestation.ConnectionsStatus {
	t.Helper()
	b, err := os.ReadFile("testdata/connections-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var c attestation.ConnectionsStatus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

// goldenNow is a verifier's clock five minutes after the image's own
// section was read (testdata/connections-status.json).
var goldenNow = time.Date(2026, 11, 16, 12, 5, 0, 0, time.UTC)

// V56 (ADR 0030, "What /status and permaily-verify show"): permaily-verify
// passes the image's own section, with no warning (step 5's warning that
// receipts are not chained is gone, ADR 0037), and fails on each case the design names: registration,
// clients that are not their documents, an endpoint or a resource off the
// deployment's hosts, a revocation slower than five seconds or bounding
// less than every byte, assertions or codes not refused strictly, mail
// while the model cannot be asked, decisions that are not strict, and the
// default rule absent, off or not the one the release's commit compiles.
func TestV56PermailyVerifyChecksTheConnections(t *testing.T) {
	r, err := checkConnections(imageConnections(t), "permaily.com", goldenNow)
	if err != nil {
		t.Fatalf("the image's own: %v", err)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings: %q", r.Warnings)
	}
	def := rules.Default()
	for name, c := range map[string]struct {
		change func(*attestation.ConnectionsStatus)
		says   string
	}{
		"dynamic registration": {func(c *attestation.ConnectionsStatus) { c.DynamicRegistration = true }, "register themselves"},
		"no client documents":  {func(c *attestation.ConnectionsStatus) { c.ClientDocuments = false }, "client_documents"},
		"another issuer":       {func(c *attestation.ConnectionsStatus) { c.Issuer = "https://api.evil.example" }, "issuer"},
		"a port on the issuer": {func(c *attestation.ConnectionsStatus) { c.Issuer = "https://api.permaily.com:8443" }, "issuer"},
		"another authorization": {func(c *attestation.ConnectionsStatus) {
			c.AuthorizationEndpoint = "https://api.permaily.com/oauth/authorize"
		}, "authorization endpoint"},
		"another token endpoint": {func(c *attestation.ConnectionsStatus) { c.TokenEndpoint = "http://api.permaily.com/oauth/token" }, "token endpoint"},
		"another revocation": {func(c *attestation.ConnectionsStatus) {
			c.RevocationEndpoint = "https://revoke.permaily.com/oauth/revoke"
		}, "revocation endpoint"},
		"another pushed endpoint": {func(c *attestation.ConnectionsStatus) {
			c.PushedEndpoint = "https://api.permaily.com.evil.example/oauth/par"
		}, "pushed request endpoint"},
		"another resource": {func(c *attestation.ConnectionsStatus) {
			c.Resources = []string{"https://api.permaily.com/v1", "https://mcp.evil.example/mcp"}
		}, "resources"},
		"a resource more":      {func(c *attestation.ConnectionsStatus) { c.Resources = append(c.Resources, "https://app.permaily.com/") }, "resources"},
		"six seconds":          {func(c *attestation.ConnectionsStatus) { c.RevocationSeconds = 6 }, "6 seconds"},
		"the first byte only":  {func(c *attestation.ConnectionsStatus) { c.RevocationCovers = "first_byte" }, "every byte"},
		"assertions by a mark": {func(c *attestation.ConnectionsStatus) { c.AssertionReplay = "mark" }, "strict record"},
		"codes with a grace":   {func(c *attestation.ConnectionsStatus) { c.CodeReuse = "grace" }, "not denied"},
		"cleared mail leaves":  {func(c *attestation.ConnectionsStatus) { c.ModelUnavailable = "cleared_leaves" }, "not nothing"},
		"decisions not strict": {func(c *attestation.ConnectionsStatus) { c.Receipts.Decisions = "cached" }, "not in strict records"},
		"no default rule":      {func(c *attestation.ConnectionsStatus) { c.Rules = nil }, "absent"},
		"another rule only":    {func(c *attestation.ConnectionsStatus) { c.Rules[0].ID = "something-else" }, "absent"},
		"the default rule off": {func(c *attestation.ConnectionsStatus) { c.Rules[0].On = false }, "is off"},
		"another version":      {func(c *attestation.ConnectionsStatus) { c.Rules[0].Version = def.Version + 1 }, "as the release's commit compiles it"},
		"another formula": {func(c *attestation.ConnectionsStatus) {
			c.Rules[0].Formulas[2] = "qry_aaaaaaaaaaaaaaaaaaaaaaaaaa"
		}, "as the release's commit compiles it"},
		"a formula fewer": {func(c *attestation.ConnectionsStatus) { c.Rules[0].Formulas = c.Rules[0].Formulas[:2] }, "as the release's commit compiles it"},
	} {
		st := imageConnections(t)
		c.change(st)
		if _, err := checkConnections(st, "permaily.com", goldenNow); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// permaily.com's section is not another deployment's.
	if _, err := checkConnections(imageConnections(t), "staging.permaily.com", goldenNow); err == nil {
		t.Fatal("permaily.com's endpoints passed for another domain")
	}
	if _, err := checkConnections(imageConnections(t), "", goldenNow); err == nil {
		t.Fatal("no domain")
	}
}

// L19 (ADR 0037, "What /status and permaily-verify show"): permaily-verify
// applies ReceiptsStatus.Check to the image's receipts with its own clock.
// It fails on receipts not sealed, not chained or not strict; on the
// record of incidents absent, old, from the future or holding an entry;
// on no pass of the audit (an image with no audit, or whose answering
// enclave started a minute ago and has not read one), one older than 48
// hours or from the future; and on anything broken, by the audit or the
// enclave that answered, a kind missing or one it does not know. It prints
// the counts and warns, without failing, on receipts recovered, unlinked
// and links expired.
func TestL19PermailyVerifyChecksTheReceipts(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*attestation.ReceiptsStatus)
		says   string
	}{
		"not sealed":          {func(r *attestation.ReceiptsStatus) { r.Sealed = false }, "not sealed"},
		"not chained":         {func(r *attestation.ReceiptsStatus) { r.Chained = false }, "not chained"},
		"not strict":          {func(r *attestation.ReceiptsStatus) { r.Chain = "" }, "not chained"},
		"no record read":      {func(r *attestation.ReceiptsStatus) { r.Incidents = nil }, "could not be read"},
		"a record read stale": {func(r *attestation.ReceiptsStatus) { r.Incidents.Read = goldenNow.Add(-31 * time.Minute) }, "not read in the"},
		"a record from later": {func(r *attestation.ReceiptsStatus) { r.Incidents.Read = goldenNow.Add(time.Minute) }, "not read in the"},
		"an incident":         {func(r *attestation.ReceiptsStatus) { r.Incidents.Recorded = 1 }, "holds 1 entries"},
		"no pass":             {func(r *attestation.ReceiptsStatus) { r.Audit = nil }, "no pass"},
		"finished before it began": {func(r *attestation.ReceiptsStatus) {
			r.Audit.Finished = r.Audit.Began.Add(-time.Second)
		}, "before it began"},
		"an old pass": {func(r *attestation.ReceiptsStatus) {
			r.Audit.Began, r.Audit.Finished = goldenNow.Add(-50*time.Hour), goldenNow.Add(-49*time.Hour)
		}, "more than 48h0m0s old"},
		"a pass from later":   {func(r *attestation.ReceiptsStatus) { r.Audit.Finished = goldenNow.Add(time.Minute) }, "after now"},
		"the audit found":     {func(r *attestation.ReceiptsStatus) { r.Audit.Broken["receipt_missing"] = 1 }, "the audit found"},
		"the enclave found":   {func(r *attestation.ReceiptsStatus) { r.Broken["link_lost"] = 2 }, "link_lost 2"},
		"an unknown kind":     {func(r *attestation.ReceiptsStatus) { r.Broken["receipt_moved"] = 0 }, "does not know"},
		"a kind missing":      {func(r *attestation.ReceiptsStatus) { delete(r.Audit.Broken, "link_forked") }, "does not count link_forked"},
		"a negative count":    {func(r *attestation.ReceiptsStatus) { r.Audit.Unlinked = -1 }, "below zero"},
		"the enclave's empty": {func(r *attestation.ReceiptsStatus) { r.Broken = nil }, "does not count"},
	} {
		st := imageConnections(t)
		c.change(&st.Receipts)
		if _, err := checkConnections(st, "permaily.com", goldenNow); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The image's own, read 31 minutes ago by the verifier's clock.
	if _, err := checkConnections(imageConnections(t), "permaily.com", goldenNow.Add(26*time.Minute)); err == nil {
		t.Fatal("a record of incidents read 31 minutes ago passed")
	}
	// Recovered, unlinked and expired are warnings, printed with the counts.
	st := imageConnections(t)
	st.Receipts.Audit.Chains, st.Receipts.Audit.Catalogue = 4, 3
	st.Receipts.Audit.Recovered, st.Receipts.Audit.Unlinked, st.Receipts.Audit.Expired = 2, 1, 5
	r, err := checkConnections(st, "permaily.com", goldenNow)
	if err != nil || len(r.Warnings) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	var out bytes.Buffer
	printConnections(&out, r)
	for _, want := range []string{
		"receipts sealed true, chained true (strict)",
		"record of incidents: 0 entries, read 2026-11-16T12:00:00Z",
		"audit of receipts' chains: finished 2026-11-16T12:00:00Z (began 2026-11-16T12:00:00Z), catalogue 3, chains 4, removed 0; broken none; recovered 2, unlinked 1, expired 5",
		"found by the enclave that answered and not recorded yet: none",
		"warning: the audit linked 2 receipts late", "warning: 1 receipts without a link", "warning: 5 links' commits",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "not chained") {
		t.Fatalf("step 5's warning is still printed:\n%s", out.String())
	}
	// Counts above zero are printed by kind (as a status that failed would
	// have them; printed only for what passed, here by hand).
	if got := brokenCounts(map[string]int64{"link_lost": 1, "receipt_missing": 2}); got != "receipt_missing 2, link_lost 1" {
		t.Fatalf("%q", got)
	}
}

// The connections' section through checkStatus: checked whatever is
// skipped, as the model's is, on the domain of the URL verified; printed
// with its warning; a failure is the host's.
func TestCheckStatusConnections(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, url := range []string{"https://app.permaily.com", "https://api.permaily.com", "https://mcp.permaily.com", "https://inbox.permaily.com", "https://mta-sts.inbox.permaily.com"} {
		host := hostResult{URL: url, SPKIHash: "ab"}
		for _, skip := range []bool{false, true} {
			r := checkStatus(&attestation.Status{Commit: "abc", Connections: imageConnections(t)}, host, imageA, kmsOptions{now: now, skip: skip})
			if !r.OK || r.Error != "" || r.Connections == nil {
				t.Fatalf("%s, skip %t: %+v", url, skip, r)
			}
			var out bytes.Buffer
			printHuman(&out, "test", map[uint][]byte{0: {1}, 1: {2}, 2: {3}}, []hostResult{r})
			for _, want := range []string{"connections: issuer https://api.permaily.com", "rule default-sign-in-and-security v1 on", "receipts sealed true, chained true (strict)", "record of incidents: 0 entries", "counted by this enclave since 2026-11-16T12:00:00Z: none"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("%s: no %q in\n%s", url, want, out.String())
				}
			}
		}
	}
	bad := imageConnections(t)
	bad.DynamicRegistration = true
	r := checkStatus(&attestation.Status{Commit: "abc", Connections: bad}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: now, skip: true})
	if r.OK || r.Connections != nil || !strings.HasPrefix(r.Error, "connections: ") {
		t.Fatalf("%+v", r)
	}
	// Counts are printed by reason, those not at zero.
	ok := imageConnections(t)
	ok.Refused["invalid_token"] = 3
	r = checkStatus(&attestation.Status{Commit: "abc", Connections: ok}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: now, skip: true})
	var out bytes.Buffer
	printConnections(&out, r.Connections)
	if !strings.Contains(out.String(), ": invalid_token 3\n") {
		t.Fatalf("%s", out.String())
	}
	// An image without the section says nothing of it.
	if r := checkStatus(&attestation.Status{Commit: "abc"}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: now, skip: true}); !r.OK || r.Connections != nil {
		t.Fatalf("%+v", r)
	}
	printConnections(&out, nil)
}

func TestDeploymentOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.permaily.com":           "permaily.com",
		"https://API.permaily.com:443/":      "permaily.com",
		"https://mta-sts.inbox.permaily.com": "permaily.com",
		"https://inbox.staging.permaily.com": "staging.permaily.com",
		"https://localhost":                  "",
		"://not a url":                       "",
	} {
		if got := deploymentOf(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
