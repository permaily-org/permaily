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
	"strconv"
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
	flights map[string]*flight // by URL, whatever it is fetched as
	window  time.Time          // when the current minute of FetchesPerMinute began
	fetched int                // fetches of clients not held in it
	forced  *refreshes
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
		forced:     newRefreshes(vendors.CacheMax),
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
	if tr.TLSClientConfig == nil {
		// Clone fills one in only where it sets HTTP/2 up itself, which it
		// does not for a transport with its own dialer.
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
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
	host, err := oauth.ClientID(clientID, r.deployment)
	if err != nil {
		return nil, fail(vendors.ErrClientID, ReasonRules)
	}
	parse := func(body []byte) (any, error) {
		doc, err := oauth.Document(clientID, body)
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
// first, at most once every KeysRefreshEvery for one URL. Only a refresh
// that is fetched is remembered, of at most CacheMax URLs at once: past
// them, a refresh is refused and the key set held is answered.
func (r *Resolver) Keys(ctx context.Context, c *vendors.Client, refresh bool) ([]vendors.Key, error) {
	if c == nil || c.Document.TokenEndpointAuthMethod != oauth.AuthPrivateKeyJWT || c.Document.JWKSURI == "" {
		return nil, fail(vendors.ErrDocument, ReasonNoKeys)
	}
	uri := c.Document.JWKSURI
	if _, err := oauth.ClientID(uri, r.deployment); err != nil {
		return nil, fail(vendors.ErrDocument, ReasonRules)
	}
	parse := func(body []byte) (any, error) { return parseKeys(body) }
	v, _, err := r.resolve(ctx, r.keys, "keys "+uri, uri, oauth.JWKSMaxBytes, refresh, parse)
	if err != nil {
		return nil, err
	}
	return append([]vendors.Key(nil), v.([]vendors.Key)...), nil
}

// flight is one fetch of one URL that every caller asking for it at once
// waits on: as key (a document or a key set) says.
type flight struct {
	key   string
	done  chan struct{}
	value any
	at    time.Time
	err   error
}

// resolve answers what the cache c holds for key when it is fresh, and
// otherwise fetches uri, one fetch of it at once, as "Caching" says. With
// refresh, it fetches a held key fresh or not, when r.forced allows it. A
// URL is fetched once at a time whatever it is fetched as: a fetch of it as
// the other kind (a jwks_uri may be the document's own URL) is waited for,
// and the cache looked at again.
func (r *Resolver) resolve(ctx context.Context, c *lru, key, uri string, limit int64, refresh bool, parse func([]byte) (any, error)) (any, time.Time, error) {
	for {
		r.mu.Lock()
		now := r.now()
		force := refresh && r.forced.may(uri, now)
		e := c.get(key)
		if e != nil && !force && e.policy.Fresh(e.currentAge(now)) {
			r.mu.Unlock()
			return e.value, e.validated, nil
		}
		f := r.flights[uri]
		if f != nil && f.key != key {
			r.mu.Unlock()
			select {
			case <-f.done:
				continue
			case <-ctx.Done():
				return nil, time.Time{}, fail(vendors.ErrUnreachable, ReasonTransport)
			}
		}
		if f == nil {
			if e == nil && !r.admit(now) {
				r.mu.Unlock()
				return nil, time.Time{}, fail(vendors.ErrUnreachable, ReasonRateLimited)
			}
			f = &flight{key: key, done: make(chan struct{})}
			r.flights[uri] = f
			var held *entry
			if e != nil {
				copied := *e
				held = &copied
			}
			go r.fly(ctx, c, uri, limit, held, parse, f)
		}
		if force {
			r.forced.mark(uri, now)
		}
		r.mu.Unlock()
		select {
		case <-f.done:
			return f.value, f.at, f.err
		case <-ctx.Done():
			return nil, time.Time{}, fail(vendors.ErrUnreachable, ReasonTransport)
		}
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
func (r *Resolver) fly(ctx context.Context, c *lru, uri string, limit int64, held *entry, parse func([]byte) (any, error), f *flight) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.timeout)
	defer cancel()
	res, err := r.fetch(ctx, uri, held, limit)

	key := f.key
	r.mu.Lock()
	defer func() {
		delete(r.flights, uri)
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
		e := &entry{key: key, value: value, policy: res.policy, validated: res.received, age: res.age, etag: res.etag, modified: res.modified}
		if res.policy.NoStore {
			c.remove(key)
		} else {
			c.put(e)
		}
		f.value, f.at = value, res.received
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
		e.validated, e.age = res.received, res.age
		// A 304 that now says no-store answers this request and drops
		// the copy and its validators.
		if e.policy.NoStore {
			c.remove(key)
		} else {
			c.put(e)
		}
		f.value, f.at = e.value, res.received
	default:
		if err == nil {
			err = fail(vendors.ErrUnreachable, ReasonStatus)
			if res.status == http.StatusNotModified {
				err = fail(vendors.ErrUnreachable, ReasonNotHeld)
			}
		}
		if errors.Is(err, vendors.ErrDocument) {
			c.remove(key)
		} else if held != nil && held.policy.StaleOnError(held.currentAge(now)) {
			f.value, f.at = held.value, held.validated
			return
		}
		f.err = err
	}
}

// response is what a fetch read: its status, and for a 200 its body; when
// it was received and how old it was then.
type response struct {
	status    int
	body      []byte
	received  time.Time
	age       time.Duration
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
	requested := r.now()
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fail(vendors.ErrUnreachable, ReasonTransport)
	}
	defer resp.Body.Close()
	received := r.now()
	res := &response{
		status:    resp.StatusCode,
		received:  received,
		age:       initialAge(resp.Header, requested, received),
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

// ageUnreadable is how old a response whose Age cannot be read is taken to
// be: RFC 9111's 2^31 seconds, past any freshness and any stale hour.
const ageUnreadable = (1 << 31) * time.Second

// initialAge is a response's corrected initial age (RFC 9111 section
// 4.2.3): the larger of the time since its Date and its Age plus the time
// it took to come, so that an answer a CDN kept does not start its max-age
// again. A Date that cannot be read, or that is later than received, adds
// nothing.
func initialAge(h http.Header, requested, received time.Time) time.Duration {
	apparent := time.Duration(0)
	if date, err := http.ParseTime(h.Get("Date")); err == nil && received.After(date) {
		apparent = capAge(received.Sub(date))
	}
	delay := capAge(max(received.Sub(requested), 0))
	return capAge(max(apparent, ageValue(h.Values("Age"))+delay))
}

// capAge bounds an age at ageUnreadable, already past any freshness and any
// stale hour, so that adding two ages never overflows into a negative one
// (RFC 9111 section 1.2.2: an overflow stays a large positive age). A
// vendor's Date of 1600 would otherwise make time.Time.Sub saturate, and the
// time kept since wrap the sum below zero, keeping a copy fresh for ever.
func capAge(d time.Duration) time.Duration {
	return min(max(d, 0), ageUnreadable)
}

// ageValue is the one delta-seconds of an Age header: 0 for none,
// ageUnreadable for one that is not a non-negative integer, two that
// differ, or one past 2^31.
func ageValue(values []string) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var n int64
	for i, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || strings.Trim(v, "0123456789") != "" {
			return ageUnreadable
		}
		m, err := strconv.ParseInt(v, 10, 64)
		if err != nil || m >= 1<<31 || (i > 0 && m != n) {
			return ageUnreadable
		}
		n = m
	}
	return time.Duration(n) * time.Second
}
