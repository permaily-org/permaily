package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// connectionsResult is what the verified enclave reports of connections
// (ADR 0030, "What /status and permaily-verify show"), after it passed
// checkConnections, and what the verifier warns of.
type connectionsResult struct {
	*attestation.ConnectionsStatus
	Warnings []string `json:"warnings,omitempty"`
}

// maxRevocationSeconds is the longest another enclave may take to stop
// releasing a revoked connection's mail: the owner's decision 6, made a
// bound on every byte by decision 14.
const maxRevocationSeconds = 5

// deploymentOf is the deployment's domain of a checked URL: the host
// without app., api., mcp., inbox. or mta-sts.inbox.
func deploymentOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if d, ok := strings.CutPrefix(host, "mta-sts.inbox."); ok {
		return d
	}
	_, d, _ := strings.Cut(host, ".")
	return d
}

// checkConnections checks what the enclave reports of connections against
// what the design promises, and its default rule against rules.Default()
// as compiled into this verifier, which is built from the release's
// commit. It fails when clients can register themselves or are not their
// documents, when an endpoint or a resource is not the deployment's
// (domain), when a revoked connection may release mail for more than five
// seconds or the bound does not cover every byte, when a client
// assertion's replay or a code's reuse is not refused by a strict record,
// when anything leaves while the model cannot be asked, when the rules'
// decisions are not strict, and when the default rule is absent, off or
// another; it warns while receipts are not chained (until step 8).
func checkConnections(c *attestation.ConnectionsStatus, domain string) (*connectionsResult, error) {
	switch {
	case c.DynamicRegistration:
		return nil, errors.New("clients may register themselves (dynamic_registration): a client would be whatever it says it is")
	case !c.ClientDocuments:
		return nil, errors.New("clients are not identified by their metadata documents (client_documents is false)")
	case c.RevocationSeconds < 0 || c.RevocationSeconds > maxRevocationSeconds:
		return nil, fmt.Errorf("another enclave may release a revoked connection's mail for %d seconds, more than %d", c.RevocationSeconds, maxRevocationSeconds)
	case c.RevocationCovers != attestation.RevocationEveryByte:
		return nil, fmt.Errorf("the revocation's bound covers %q, not every byte of an answer already being sent", c.RevocationCovers)
	case c.AssertionReplay != "strict":
		return nil, fmt.Errorf("a client assertion used again is refused by %q, not a strict record that cannot be put back", c.AssertionReplay)
	case c.CodeReuse != "denied":
		return nil, fmt.Errorf("an authorization code used again is %q, not denied", c.CodeReuse)
	case c.ModelUnavailable != attestation.ModelUnavailableNothingLeaves:
		return nil, fmt.Errorf("while the model cannot be asked, connections get %q, not nothing", c.ModelUnavailable)
	case c.Receipts.Decisions != "strict":
		return nil, fmt.Errorf("the rules' decisions are kept %q, not in strict records: whoever writes the storage could undo a hold", c.Receipts.Decisions)
	}
	if err := checkEndpoints(c, domain); err != nil {
		return nil, err
	}
	if err := checkDefaultRule(c.Rules); err != nil {
		return nil, err
	}
	r := &connectionsResult{ConnectionsStatus: c}
	if !c.Receipts.Chained {
		r.Warnings = append(r.Warnings, "receipts of what left are sealed but not chained yet (step 8): whoever writes the storage can delete one unseen")
	}
	if !c.Receipts.Sealed {
		r.Warnings = append(r.Warnings, "receipts of what left are not sealed")
	}
	return r, nil
}

// checkEndpoints says every endpoint and resource is the deployment's:
// the issuer and its endpoints on api., the authorization page on app.,
// the resources api.'s /v1 and mcp.'s /mcp, over HTTPS on 443.
func checkEndpoints(c *attestation.ConnectionsStatus, domain string) error {
	if domain == "" {
		return errors.New("the deployment's domain is not known, so its endpoints cannot be checked")
	}
	api, app, mcp := "https://api."+domain, "https://app."+domain, "https://mcp."+domain
	want := map[string][2]string{
		"issuer":                  {c.Issuer, api},
		"authorization endpoint":  {c.AuthorizationEndpoint, app + "/oauth/authorize"},
		"token endpoint":          {c.TokenEndpoint, api + "/oauth/token"},
		"revocation endpoint":     {c.RevocationEndpoint, api + "/oauth/revoke"},
		"pushed request endpoint": {c.PushedEndpoint, api + "/oauth/par"},
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if got := want[name]; got[0] != got[1] {
			return fmt.Errorf("the %s is %q, not %q: it is not on the deployment's hosts", name, got[0], got[1])
		}
	}
	if resources := []string{api + "/v1", mcp + "/mcp"}; !slices.Equal(c.Resources, resources) {
		return fmt.Errorf("the resources are %q, not %q", c.Resources, resources)
	}
	return nil
}

// checkDefaultRule says the default rule is there, on, and the one this
// verifier compiles: its version and the canonical identifiers of its
// formulas, in order.
func checkDefaultRule(rs []attestation.RuleStatus) error {
	def := rules.Default()
	want := make([]string, 0, len(def.Formulas))
	for _, f := range def.Formulas {
		want = append(want, f.ID)
	}
	for _, r := range rs {
		if r.ID != def.ID {
			continue
		}
		switch {
		case !r.On:
			return errors.New("the default rule, which holds sign-in codes and password resets back, is off")
		case r.Version != def.Version || !slices.Equal(r.Formulas, want):
			return fmt.Errorf("the default rule is version %d with formulas %q, not version %d with %q as the release's commit compiles it", r.Version, r.Formulas, def.Version, want)
		}
		return nil
	}
	return errors.New("the default rule, which holds sign-in codes and password resets back, is absent")
}

func printConnections(w io.Writer, c *connectionsResult) {
	if c == nil {
		return
	}
	fmt.Fprintf(w, "      connections: issuer %s, resources %s; grants %s; clients by their documents, no registration; client authentication %s\n",
		c.Issuer, strings.Join(c.Resources, " and "), strings.Join(c.Grants, ", "), strings.Join(c.ClientAuth, ", "))
	fmt.Fprintf(w, "      access tokens %ds, refresh tokens %ds (a retry within %ds), codes %ds and %s on reuse; a connection %d days by default, %d at most, %d devices\n",
		c.AccessTokenSeconds, c.RefreshTokenSeconds, c.RefreshRetrySeconds, c.CodeSeconds, c.CodeReuse, c.ConnectionDefaultDays, c.ConnectionMaxDays, c.DevicesPerConnection)
	fmt.Fprintf(w, "      a revocation stops %s within %ds in another enclave; client assertions %s; vendors' documents over %s; while the model cannot be asked: %s\n",
		c.RevocationCovers, c.RevocationSeconds, c.AssertionReplay, c.VendorTransport, strings.ReplaceAll(c.ModelUnavailable, "_", " "))
	var verified []string
	for _, v := range c.Verified {
		verified = append(verified, v.Domain+" ("+v.Owner+")")
	}
	fmt.Fprintf(w, "      verified vendors: %s\n", strings.Join(verified, ", "))
	for _, r := range c.Rules {
		state := "off"
		if r.On {
			state = "on"
		}
		fmt.Fprintf(w, "      rule %s v%d %s, formulas %s (the release's own)\n", r.ID, r.Version, state, strings.Join(r.Formulas, ", "))
	}
	fmt.Fprintf(w, "      receipts sealed %t, chained %t; decisions %s, at least %d a mailbox's day\n", c.Receipts.Sealed, c.Receipts.Chained, c.Receipts.Decisions, c.Receipts.DecisionsPerDay)
	reasons := make([]string, 0, len(c.Refused))
	for reason := range c.Refused {
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)
	var counts []string
	for _, reason := range reasons {
		if n := c.Refused[reason]; n != 0 {
			counts = append(counts, fmt.Sprintf("%s %d", reason, n))
		}
	}
	if len(counts) == 0 {
		counts = []string{"none"}
	}
	fmt.Fprintf(w, "      counted by this enclave since %s: %s\n", c.Since.UTC().Format(time.RFC3339), strings.Join(counts, ", "))
	for _, warning := range c.Warnings {
		fmt.Fprintf(w, "      warning: %s\n", warning)
	}
}
