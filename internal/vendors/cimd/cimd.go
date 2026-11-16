// Package cimd fetches and caches vendors' client metadata documents and key
// sets (ADR 0030, "Clients" and "The egress for vendors' documents"): the
// implementation of vendors.Resolver over the vendor egress, the host's
// second CONNECT proxy (vsock 8004), through enclave.VendorEgressClient.
//
// A fetch is a bodiless GET of the URL exactly, with Accept:
// application/json and nothing else of the enclave's (no cookie, no
// credential, no Referer, no compression asked), over HTTPS and HTTP/2 only,
// following no redirect, within FetchTimeout, taking at most
// oauth.DocumentMaxBytes of a document and oauth.JWKSMaxBytes of a key set
// after any content decoding. What is fetched is kept in memory only, as
// vendors.CachePolicy says. Nothing of a URL, a host or a vendor's answer is
// in an error: each error is one of vendors' kinds with a fixed reason.
package cimd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

// FetchTimeout bounds one fetch, from the connection to the last byte.
const FetchTimeout = 5 * time.Second

// KeysRefreshEvery bounds how often a key set is fetched again for a kid it
// did not have.
const KeysRefreshEvery = time.Minute

// HeaderMaxBytes bounds a response's headers.
const HeaderMaxBytes = 16 << 10

// Reason is why a fetch failed, a fixed word a log line may carry. It is
// the text of the error after the vendors' kind.
type Reason string

// The reasons.
const (
	ReasonStatus      Reason = "status"       // an answer other than 200 or 304 (a 3xx included: never followed)
	ReasonNotHeld     Reason = "not_held"     // a 304 for a document the enclave does not hold
	ReasonTransport   Reason = "transport"    // no answer: the egress, DNS, TCP, TLS, HTTP/2 or the time
	ReasonEncoding    Reason = "encoding"     // a content coding other than gzip, or a gzip that does not decode
	ReasonNotObject   Reason = "not_object"   // a 200 whose body is not a JSON object
	ReasonTooLarge    Reason = "too_large"    // more than the bound, after decoding
	ReasonRateLimited Reason = "rate_limited" // past FetchesPerMinute for a client not held
	ReasonRules       Reason = "rules"        // the URL or the document breaks oauth's rules
	ReasonNoKeys      Reason = "no_keys"      // a document that declares no key set
)

// Error is a failure of the resolver: Kind is vendors.ErrClientID,
// ErrDocument or ErrUnreachable (errors.Is matches it), Reason why.
type Error struct {
	Kind   error
	Reason Reason
}

func (e *Error) Error() string { return fmt.Sprintf("%v (%s)", e.Kind, e.Reason) }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, r Reason) error { return &Error{Kind: kind, Reason: r} }

// Resolver implements vendors.Resolver.
type Resolver struct {
	hc         http.Client
	deployment string
	now        func() time.Time
	timeout    time.Duration

	slots chan struct{}

	mu      sync.Mutex
	docs    *lru
	keys    *lru
	flights map[string]*flight
	window  time.Time // when the current minute of FetchesPerMinute began
	fetched int       // fetches of clients not held in it
	forced  map[string]time.Time
}

var _ vendors.Resolver = (*Resolver)(nil)

// New is a resolver that fetches through hc (enclave.VendorEgressClient),
// refusing the names of deployment and under it, at now. It copies hc, and
// the copy follows no redirect, keeps no cookie, asks no compression,
// speaks only HTTP/2, takes headers of at most HeaderMaxBytes and gives a
// fetch FetchTimeout in all; hc is left as it is.
func New(hc *http.Client, deployment string, now func() time.Time) *Resolver {
	r := &Resolver{
		deployment: deployment,
		now:        now,
		timeout:    FetchTimeout,
		slots:      make(chan struct{}, vendors.FetchesInFlight),
		docs:       newLRU(vendors.CacheMax),
		keys:       newLRU(vendors.CacheMax),
		flights:    map[string]*flight{},
		forced:     map[string]time.Time{},
	}
	if hc != nil {
		r.hc = *hc
	}
	r.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r.hc.Jar = nil
	r.hc.Timeout = FetchTimeout
	r.hc.Transport = Harden(r.hc.Transport)
	return r
}

// Harden is a copy of rt that speaks only HTTP/2, asks no compression and
// takes headers of at most HeaderMaxBytes, when rt is an *http.Transport
// (nil is http.DefaultTransport); rt itself otherwise. Go's HTTP/1.1
// transport reads an idle connection and logs what it finds there, so a
// vendor's bytes would reach the process's log; its HTTP/2 transport logs
// none (ADR 0030, "Logging", finding 12; the owner's decision 11). A host
// that negotiates no protocol, or only HTTP/1.1, is refused at the
// handshake, before a byte of HTTP is written.
func Harden(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	tr, ok := rt.(*http.Transport)
	if !ok {
		return rt
	}
	tr = tr.Clone()
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	tr.DisableCompression = true
	tr.MaxResponseHeaderBytes = HeaderMaxBytes
	if tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		tr.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	verify := tr.TLSClientConfig.VerifyConnection
	tr.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
		if verify != nil {
			if err := verify(cs); err != nil {
				return err
			}
		}
		if cs.NegotiatedProtocol != "h2" {
			return errNotHTTP2
		}
		return nil
	}
	return tr
}

var errNotHTTP2 = errors.New("cimd: the vendor did not negotiate HTTP/2")

// Client resolves a client_id URL to its checked document.
func (r *Resolver) Client(ctx context.Context, clientID string) (*vendors.Client, error) {
	host, err := checkClientID(clientID, r.deployment)
	if err != nil {
		return nil, fail(vendors.ErrClientID, ReasonRules)
	}
	parse := func(body []byte) (any, error) {
		doc, err := checkDocument(clientID, body)
		if err != nil {
			return nil, fail(vendors.ErrDocument, ReasonRules)
		}
		return doc, nil
	}
	v, at, err := r.resolve(ctx, r.docs, "doc "+clientID, clientID, oauth.DocumentMaxBytes, false, parse)
	if err != nil {
		return nil, err
	}
	return &vendors.Client{ID: clientID, Domain: host, Document: *v.(*oauth.ClientDocument), FetchedAt: at}, nil
}

// Keys returns the key set at c's jwks_uri; with refresh, it revalidates it
// first, at most once every KeysRefreshEvery for one URL.
func (r *Resolver) Keys(ctx context.Context, c *vendors.Client, refresh bool) ([]vendors.Key, error) {
	if c == nil || c.Document.TokenEndpointAuthMethod != oauth.AuthPrivateKeyJWT || c.Document.JWKSURI == "" {
		return nil, fail(vendors.ErrDocument, ReasonNoKeys)
	}
	uri := c.Document.JWKSURI
	if _, err := checkClientID(uri, r.deployment); err != nil {
		return nil, fail(vendors.ErrDocument, ReasonRules)
	}
	force := false
	if refresh {
		r.mu.Lock()
		now := r.now()
		if last, ok := r.forced[uri]; !ok || now.Sub(last) >= KeysRefreshEvery {
			r.forced[uri] = now
			force = true
		}
		// What a vendor's URLs leave here is bounded: past CacheMax, the
		// refreshes older than a minute, which decide nothing, are dropped.
		if len(r.forced) > vendors.CacheMax {
			for u, at := range r.forced {
				if now.Sub(at) >= KeysRefreshEvery {
					delete(r.forced, u)
				}
			}
		}
		r.mu.Unlock()
	}
	parse := func(body []byte) (any, error) { return parseKeys(body) }
	v, _, err := r.resolve(ctx, r.keys, "keys "+uri, uri, oauth.JWKSMaxBytes, force, parse)
	if err != nil {
		return nil, err
	}
	return append([]vendors.Key(nil), v.([]vendors.Key)...), nil
}

// flight is one fetch of one URL that every caller asking for it at once
// waits on.
type flight struct {
	done  chan struct{}
	value any
	at    time.Time
	err   error
}

// resolve answers what the cache c holds for key when it is fresh (and not
// force), and otherwise fetches uri, one fetch of it at once, as "Caching"
// says.
func (r *Resolver) resolve(ctx context.Context, c *lru, key, uri string, limit int64, force bool, parse func([]byte) (any, error)) (any, time.Time, error) {
	r.mu.Lock()
	now := r.now()
	e := c.get(key)
	if e != nil && !force && e.policy.Fresh(now.Sub(e.validated)) {
		r.mu.Unlock()
		return e.value, e.validated, nil
	}
	f := r.flights[key]
	if f == nil {
		if e == nil && !r.admit(now) {
			r.mu.Unlock()
			return nil, time.Time{}, fail(vendors.ErrUnreachable, ReasonRateLimited)
		}
		f = &flight{done: make(chan struct{})}
		r.flights[key] = f
		var held *entry
		if e != nil {
			copied := *e
			held = &copied
		}
		go r.fly(ctx, c, key, uri, limit, held, parse, f)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return f.value, f.at, f.err
	case <-ctx.Done():
		return nil, time.Time{}, fail(vendors.ErrUnreachable, ReasonTransport)
	}
}

// admit counts a fetch of a client not held against FetchesPerMinute.
func (r *Resolver) admit(now time.Time) bool {
	if now.Sub(r.window) >= time.Minute || now.Before(r.window) {
		r.window, r.fetched = now, 0
	}
	if r.fetched >= vendors.FetchesPerMinute {
		return false
	}
	r.fetched++
	return true
}

// fly fetches uri for f and keeps what it may in c. It runs apart from the
// caller that started it, which may give up, so that the others waiting on
// f are answered.
func (r *Resolver) fly(ctx context.Context, c *lru, key, uri string, limit int64, held *entry, parse func([]byte) (any, error), f *flight) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.timeout)
	defer cancel()
	res, err := r.fetch(ctx, uri, held, limit)

	r.mu.Lock()
	defer func() {
		delete(r.flights, key)
		r.mu.Unlock()
		close(f.done)
	}()
	now := r.now()
	switch {
	case err == nil && res.status == http.StatusOK:
		value, perr := parse(res.body)
		if perr != nil {
			c.remove(key)
			f.err = perr
			return
		}
		e := &entry{key: key, value: value, policy: res.policy, validated: now, etag: res.etag, modified: res.modified}
		if res.policy.NoStore {
			c.remove(key)
		} else {
			c.put(e)
		}
		f.value, f.at = value, now
	case err == nil && res.status == http.StatusNotModified && held != nil:
		e := c.get(key)
		if e == nil {
			e = held
		}
		if res.hasPolicy {
			e.policy = res.policy
		}
		if res.etag != "" {
			e.etag = res.etag
		}
		if res.modified != "" {
			e.modified = res.modified
		}
		e.validated = now
		c.put(e)
		f.value, f.at = e.value, now
	default:
		if err == nil {
			err = fail(vendors.ErrUnreachable, ReasonStatus)
			if res.status == http.StatusNotModified {
				err = fail(vendors.ErrUnreachable, ReasonNotHeld)
			}
		}
		if errors.Is(err, vendors.ErrDocument) {
			c.remove(key)
		} else if held != nil && held.policy.StaleOnError(now.Sub(held.validated)) {
			f.value, f.at = held.value, held.validated
			return
		}
		f.err = err
	}
}

// response is what a fetch read: its status, and for a 200 its body.
type response struct {
	status    int
	body      []byte
	policy    vendors.CachePolicy
	hasPolicy bool
	etag      string
	modified  string
}

// fetch makes the GET, conditional on held's validators.
func (r *Resolver) fetch(ctx context.Context, uri string, held *entry, limit int64) (*response, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return nil, fail(vendors.ErrUnreachable, ReasonTransport)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, http.NoBody)
	if err != nil {
		return nil, fail(vendors.ErrClientID, ReasonRules)
	}
	req.Header = http.Header{"Accept": {"application/json"}}
	if held != nil {
		if held.etag != "" {
			req.Header.Set("If-None-Match", held.etag)
		}
		if held.modified != "" {
			req.Header.Set("If-Modified-Since", held.modified)
		}
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fail(vendors.ErrUnreachable, ReasonTransport)
	}
	defer resp.Body.Close()
	res := &response{
		status:    resp.StatusCode,
		etag:      resp.Header.Get("ETag"),
		modified:  resp.Header.Get("Last-Modified"),
		hasPolicy: len(resp.Header.Values("Cache-Control")) > 0,
		policy:    vendors.ParseCacheControl(strings.Join(resp.Header.Values("Cache-Control"), ",")),
	}
	if resp.StatusCode != http.StatusOK {
		return res, nil
	}
	if resp.ContentLength > limit && resp.Header.Get("Content-Encoding") == "" {
		return nil, fail(vendors.ErrDocument, ReasonTooLarge)
	}
	var body io.Reader = resp.Body
	gz := false
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip", "x-gzip":
		// Not asked for, but read as HTTP allows: the bound holds after
		// decoding, so a small answer that decodes large is refused.
		zr, err := gzip.NewReader(io.LimitReader(resp.Body, 2*limit))
		if err != nil {
			return nil, fail(vendors.ErrUnreachable, ReasonEncoding)
		}
		body, gz = zr, true
	default:
		return nil, fail(vendors.ErrUnreachable, ReasonEncoding)
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		if gz && ctx.Err() == nil {
			return nil, fail(vendors.ErrUnreachable, ReasonEncoding)
		}
		return nil, fail(vendors.ErrUnreachable, ReasonTransport)
	}
	if int64(len(b)) > limit {
		return nil, fail(vendors.ErrDocument, ReasonTooLarge)
	}
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) == 0 || t[0] != '{' || !json.Valid(b) {
		return nil, fail(vendors.ErrUnreachable, ReasonNotObject)
	}
	res.body = b
	return res, nil
}
