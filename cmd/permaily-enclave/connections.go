package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
	"github.com/permaily-org/permaily/internal/vendors/cimd"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The connections of accounts (ADR 0030 and 0031, pull request 9): the
// authorization server, the connections service with the default rule and
// the record of what left, the API for apps and MCP for agents, composed
// wherever the mailboxes and the model are.

// vendorEgressPort is the vsock port of the host's second proxy, which
// tunnels to any public name on 443 and nothing else (hostproxy.PublicProxy,
// -vendor-egress-port of permaily-host): the only way a vendor's document
// or key set is fetched. It is never the egress port, whose exact list
// keeps every other client of the enclave.
const vendorEgressPort = 8004

// connectionsParts is what the connections are composed from.
type connectionsParts struct {
	oauth oauth.Config
	// domain is the deployment's: a client_id under it is refused.
	domain string
	// records is the one strict Anchored of the connections' records (a
	// connection, an account's list, a token family, a record of
	// assertions), with OnlyNewest: no other Anchored reads or writes their
	// names, and it reads no other.
	records *store.Anchored
	// entries holds the sealed entries (requests, codes, tokens), the
	// receipts and the mailboxes' mail the discloser reads.
	entries connectionsStore
	keys    connections.Keys
	// vendorHTTP is the client the vendors' documents are fetched with:
	// enclave.VendorEgressClient(vendorEgressPort) in the image, a played
	// vendor's in dev mode, and nothing else.
	vendorHTTP *http.Client
	accounts   *accounts.Service
	mailboxes  *mailboxes.Service
	model      *decisions.Service
	now        func() time.Time
}

// connectionsStore is where the connections' entries are, and the mail
// their reads disclose: the deployment's one table.
type connectionsStore interface {
	store.Entries
	inbox.ReadStore
}

// composedConnections is the service and what /status counts of its reads.
type composedConnections struct {
	svc *connections.Service
	// full counts the messages held, undecided, for want of room in their
	// day (inbox.DecisionsFullCounter): /status's decisions_full.
	full inbox.DecisionsFullCounter
	cfg  oauth.Config
	// since is when this enclave started counting.
	since time.Time
}

// errConnectionsRecords refuses records that are not one strict Anchored
// keeping only the newest value.
var errConnectionsRecords = errors.New("the connections' records need a strict Anchored that keeps only the newest value")

// composeConnections is the connections service (ADR 0030, pull request 9):
// its records in one strict Anchored, vendors resolved through the vendor
// egress (cimd), the account's standing and its deletion's fence from
// accounts (which it is given back to), what a connection reads through
// inbox's Discloser over the mailboxes' decisions, with the default rule
// first, the model's gate and Asker, and a receipt written before anything
// leaves.
func composeConnections(c connectionsParts) (*composedConnections, error) {
	if c.records == nil || !c.records.Strict || !c.records.OnlyNewest {
		return nil, errConnectionsRecords
	}
	if c.vendorHTTP == nil || c.accounts == nil || c.mailboxes == nil || c.model == nil {
		return nil, errors.New("the connections need the vendor egress, accounts, mailboxes and the model")
	}
	now := c.now
	if now == nil {
		now = time.Now
	}
	store := inbox.NewDiscloser(c.entries, query.Eval, c.mailboxes.Decisions())
	full, _ := store.(inbox.DecisionsFullCounter)
	svc, err := connections.New(connections.Config{
		OAuth:       c.oauth,
		Records:     c.records,
		Entries:     c.entries,
		Keys:        c.keys,
		Vendors:     cimd.New(c.vendorHTTP, c.domain, now),
		Accounts:    c.accounts,
		Mailboxes:   connectionMailboxes{c.mailboxes},
		Discloser:   store,
		Asker:       c.model,
		Model:       c.model,
		ModelNotice: domain.ModelNoticeVersion,
		Rules:       []rules.Rule{rules.Default()},
		Receipts:    receipts.New(c.entries, c.keys, now, nil),
		Now:         now,
	})
	if err != nil {
		return nil, err
	}
	// An account being deleted fences and ends its connections, and a
	// cancelled deletion reopens them (ADR 0030, finding 6).
	c.accounts.SetConnections(svc)
	return &composedConnections{svc: svc, full: full, cfg: c.oauth, since: now()}, nil
}

// connectionMailboxes is what the connections service reads of mailboxes
// (connections.Mailboxes): the identifiers of the account's mailboxes a
// formula may name, as the person's own formulas are checked against, and
// a mailbox opened for a connection's read.
type connectionMailboxes struct{ m *mailboxes.Service }

func (c connectionMailboxes) Catalogue(ctx context.Context, account string) ([]string, error) {
	named, err := c.m.Catalogue(ctx, mailboxes.Caller{Account: account}, false)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(named))
	for _, n := range named {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

func (c connectionMailboxes) OpenForConnection(ctx context.Context, account, mailbox string) (inbox.Box, error) {
	return c.m.OpenForConnection(ctx, account, mailbox)
}

var _ connections.Mailboxes = connectionMailboxes{}

// oauthConfig is the authorization server's hosts, read from the image's
// domain as the hosts are; in dev mode every one carries the dev port.
func oauthConfig(hosts router.Hosts, port uint32) oauth.Config {
	origin := func(host string) string {
		if port == 443 {
			return "https://" + host
		}
		return "https://" + host + ":" + strconv.FormatUint(uint64(port), 10)
	}
	return oauth.Config{
		Issuer:      origin(hosts.API),
		AppOrigin:   origin(hosts.App),
		APIResource: origin(hosts.API) + oauth.PathAPI,
		MCPResource: origin(hosts.MCP) + oauth.PathMCP,
	}
}

// sweepEvery is how often an enclave runs the connections' pass: past-end
// connections ended, pending ones fenced, ended ones removed after 30 days,
// unfinished deletions' fences finished, dead records of assertions removed.
const sweepEvery = time.Hour

// runConnections runs the hourly pass on its ticker (of tick when a test
// gives one), once when it starts, until ctx ends. A pass that fails is
// logged by its kind and the next tries again: nothing it does is what
// refuses a token (a past-end connection is refused whether or not it ran).
//
// It also runs the refusals' pass (ADR 0031, "Refusals past a window's
// bound") refusalPassDelay after each minute of the clock ends, whenever
// the enclave started: it writes the summaries of the windows that closed
// and the writes due again. It returns what is closed once that pass has
// stopped, which stopConnections waits for before the last writes.
func runConnections(ctx context.Context, svc *connections.Service, log *slog.Logger, tick <-chan time.Time, clock refusalClock) <-chan struct{} {
	go every(ctx, sweepEvery, tick, func() {
		if err := svc.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Warn("connections: the hourly pass did not finish", "kind", "sweep")
		}
	})
	if clock.now == nil {
		clock.now = time.Now
	}
	if clock.after == nil {
		clock.after = time.After
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-clock.after(untilRefusalPass(clock.now())):
			}
			if err := svc.FlushRefusals(ctx, false); err != nil && ctx.Err() == nil {
				log.Warn("connections: a summary of refusals was not written", "kind", "refusals")
			}
		}
	}()
	return done
}

// refusalClock is the refusals' pass's clock: time.Now and time.After
// unless a test gives its own.
type refusalClock struct {
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// refusalPassDelay is how long after a minute of the clock ends the
// refusals' pass runs: the window is closed on every enclave's clock by
// then, and its summary is not left to wait for a pass a minute later.
const refusalPassDelay = 2 * time.Second

// untilRefusalPass is how long from now to the next refusals' pass:
// refusalPassDelay after the end of the minute, this one's when that is
// still ahead.
func untilRefusalPass(now time.Time) time.Duration {
	at := now.Truncate(connections.RefusalWindow).Add(refusalPassDelay)
	if !now.Before(at) {
		at = at.Add(connections.RefusalWindow)
	}
	return at.Sub(now)
}

// stopMax bounds a stop's last writes, once the server stopped taking
// requests: the requests of connections still under way, the refusals'
// pass, and every summary of refusals still in memory.
const stopMax = 20 * time.Second

// stopConnections ends the connections' work at a stop, after the server
// stopped taking requests: it waits for the refusals' pass to stop (passed,
// what runConnections returned), then for the attributed requests still
// under way, admitting none more, and writes every connection's refusals
// still counted in memory, the open windows' included (connections.Stop).
// It returns an error when any of that did not finish within stopMax: a
// stop that loses counts says so (a crash loses the ones not yet written).
func stopConnections(svc *connections.Service, passed <-chan struct{}, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), stopMax)
	defer cancel()
	select {
	case <-passed:
	case <-ctx.Done():
		log.Error("connections: the refusals' pass did not stop", "kind", "refusals")
		return errors.New("connections: the refusals' pass did not stop")
	}
	if err := svc.Stop(ctx); err != nil {
		log.Error("connections: refusals were not written at the stop", "kind", "refusals")
		return err
	}
	return nil
}

// mountConnections puts the connections' routes on their hosts (ADR 0030,
// "Hosts, issuer and endpoints"), with what internal/oauth writes of the
// metadata and the challenges, and MCP's endpoint on the mcp host.
func mountConnections(c *composedConnections, app *httpapi.AppMux, api, mcp *http.ServeMux, spa http.Handler, sessions httpapi.Sessions, log *slog.Logger) {
	httpapi.MountConnections(httpapi.ConnectionsMounts{App: app, API: api, MCP: mcp, SPA: spa}, httpapi.ConnectionsConfig{
		Service:          c.svc,
		Sessions:         sessions,
		OAuth:            c.cfg,
		Metadata:         oauth.Metadata,
		ResourceMetadata: oauth.ResourceMetadataFor,
		Challenge:        oauth.Challenge,
		Log:              log,
	})
	mcp.Handle(oauth.PathMCP, mcpapi.New(c.svc, c.cfg, mcpapi.WithLog(log)))
}

// connectionsStatus is /status's connections (ADR 0030, "What /status and
// permaily-verify show"): what the image compiles in, and this enclave's
// refusals by fixed reason since it started, never a token, a client that
// is not verified, an account, a connection, or a count of connections or
// consents.
func connectionsStatus(c *composedConnections) func() *attestation.ConnectionsStatus {
	return func() *attestation.ConnectionsStatus {
		md := oauth.Metadata(c.cfg)
		st := &attestation.ConnectionsStatus{
			Issuer:                md.Issuer,
			AuthorizationEndpoint: md.AuthorizationEndpoint,
			TokenEndpoint:         md.TokenEndpoint,
			RevocationEndpoint:    md.RevocationEndpoint,
			PushedEndpoint:        md.PushedAuthorizationRequestEndpoint,
			Resources:             []string{c.cfg.APIResource, c.cfg.MCPResource},
			Grants:                md.GrantTypesSupported,
			ClientAuth:            md.TokenEndpointAuthMethodsSupported,
			ClientDocuments:       md.ClientIDMetadataDocumentSupported,
			// The server's metadata has no registration endpoint, and no
			// route takes one.
			DynamicRegistration:   false,
			AccessTokenSeconds:    seconds(oauth.AccessTokenTTL),
			RefreshTokenSeconds:   seconds(oauth.RefreshTokenTTL),
			RefreshRetrySeconds:   seconds(oauth.RefreshRetryGrace),
			CodeSeconds:           seconds(oauth.CodeTTL),
			CodeReuse:             "denied",
			RevocationSeconds:     seconds(oauth.ConnectionFresh),
			ConnectionDefaultDays: oauth.ConnectionDefaultDays,
			ConnectionMaxDays:     oauth.ConnectionMaxDays,
			RevocationCovers:      attestation.RevocationEveryByte,
			AssertionReplay:       "strict",
			VendorTransport:       "h2",
			DevicesPerConnection:  oauth.FamiliesMax,
			ModelUnavailable:      attestation.ModelUnavailableNothingLeaves,
			Receipts: attestation.ReceiptsStatus{
				Sealed: true, Chained: false, Decisions: "strict", DecisionsPerDay: inbox.DecisionsPerDay,
			},
			Since:   c.since,
			Refused: map[string]int64{},
		}
		for _, v := range vendors.List() {
			st.Verified = append(st.Verified, attestation.VerifiedVendor{Domain: v.Domain, Owner: v.Name})
		}
		def := rules.Default()
		rs := attestation.RuleStatus{ID: def.ID, Version: def.Version, On: true, Formulas: []string{}}
		for _, f := range def.Formulas {
			rs.Formulas = append(rs.Formulas, f.ID)
		}
		st.Rules = []attestation.RuleStatus{rs}
		for _, reason := range connectionsReasons {
			st.Refused[reason] = 0
		}
		for reason, n := range c.svc.Refused() {
			st.Refused[reason] = n
		}
		if c.full != nil {
			st.Refused["decisions_full"] = c.full.DecisionsFull()
		}
		return st
	}
}

// connectionsReasons are the fixed reasons /status lists even at zero: the
// refusals ADR 0030's "Logging" names, the events the service counts, and
// decisions_full (ADR 0031, "Bounds"). Any other the service counts is a
// fixed key of the image too, and is listed when it is not zero.
var connectionsReasons = []string{
	"client_id_invalid", "client_document_invalid", "vendor_unreachable", "redirect_uri_invalid",
	"invalid_request", "invalid_target", "invalid_scope", "invalid_grant", "invalid_client", "unsupported_grant_type",
	"assertion", "assertion_replay", "assertions_full", "reuse", "refresh_retry", "invalid_token", "wrong_resource",
	"insufficient_scope", "rate_limited", "consent_changed", "too_many_devices", "too_many_connections",
	"request_too_large", "model_notice_changed", "model_unavailable", "invalid_formula", "invalid_limit",
	"invalid_cursor", "too_late", "cut", "receipt_failed", "allowed", "denied", "token_issued",
	"revoked_person", "revoked_vendor", "decisions_full",
}

func seconds(d time.Duration) int { return int(d / time.Second) }

// serverTLS is the HTTPS server's TLS: base, which offers h2 and http/1.1
// (and answers ACME's challenges), except on the api and mcp hosts, which
// offer http/1.1 alone (ADR 0030, "Changed after the second review of pull
// request 6"): Go's HTTP/2 server gives no honest signal of when an
// answer's last bytes reached the socket, so an answer of mail over HTTP/2
// is refused there (503 no_abort), and a client that offers h2 to those
// hosts is given http/1.1 instead and gets its mail. The app's host keeps
// h2. A client that names another host by SNI than by Host is still
// dispatched by Host, and its mail over HTTP/2 refused by the routes.
func serverTLS(base *tls.Config, hosts router.Hosts) *tls.Config {
	only := base.Clone()
	only.NextProtos = []string{"http/1.1", acme.ALPNProto}
	cfg := base.Clone()
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		switch strings.TrimSuffix(strings.ToLower(hello.ServerName), ".") {
		case hosts.API, hosts.MCP:
			return only, nil
		}
		return nil, nil
	}
	return cfg
}

// connContext is the server's ConnContext: the connection kept in its
// requests' context for both the API's and MCP's transfers, so a lease that
// ends while an answer is sent closes the connection (httpapi.ConnContext,
// mcpapi.ConnContext). Without it every answer of mail is refused.
func connContext(ctx context.Context, c net.Conn) context.Context {
	return mcpapi.ConnContext(httpapi.ConnContext(ctx, c), c)
}
