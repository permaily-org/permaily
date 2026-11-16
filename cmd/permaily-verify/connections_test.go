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

// V56 (ADR 0030, "What /status and permaily-verify show"): permaily-verify
// passes the image's own section, with the warning that receipts are not
// chained yet, and fails on each case the design names: registration,
// clients that are not their documents, an endpoint or a resource off the
// deployment's hosts, a revocation slower than five seconds or bounding
// less than every byte, assertions or codes not refused strictly, mail
// while the model cannot be asked, decisions that are not strict, and the
// default rule absent, off or not the one the release's commit compiles.
func TestV56PermailyVerifyChecksTheConnections(t *testing.T) {
	r, err := checkConnections(imageConnections(t), "permaily.com")
	if err != nil {
		t.Fatalf("the image's own: %v", err)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "not chained") {
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
		if _, err := checkConnections(st, "permaily.com"); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// permaily.com's section is not another deployment's.
	if _, err := checkConnections(imageConnections(t), "staging.permaily.com"); err == nil {
		t.Fatal("permaily.com's endpoints passed for another domain")
	}
	if _, err := checkConnections(imageConnections(t), ""); err == nil {
		t.Fatal("no domain")
	}
	// Chained receipts: no warning.
	st := imageConnections(t)
	st.Receipts.Chained = true
	if r, err := checkConnections(st, "permaily.com"); err != nil || len(r.Warnings) != 0 {
		t.Fatalf("%+v %v", r, err)
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
			for _, want := range []string{"connections: issuer https://api.permaily.com", "rule default-sign-in-and-security v1 on", "warning: receipts of what left are sealed but not chained", "counted by this enclave since 2026-11-16T12:00:00Z: none"} {
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
