// Package certwatch answers two questions about the certificates of a set of
// host names: who is allowed to issue them (CAA records), and which ones have
// been issued (Certificate Transparency).
//
// Attestation proves that one TLS connection ends inside the enclave. A
// browser cannot check that, so it accepts any valid certificate for the name.
// These checks look for certificates the enclave does not hold: every one
// issued since the enclave's key exists must carry that key.
package certwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrMalformed means an answer from DNS or the Certificate Transparency
// service could not be understood.
var ErrMalformed = errors.New("certwatch: malformed answer")

// CAA is one Certification Authority Authorization record (RFC 8659).
type CAA struct {
	Tag string // "issue", "issuewild", "iodef"
	// Issuer is the CA domain of an issue or issuewild record; empty means
	// nobody may issue (the value ";").
	Issuer string
	// Params are the CA's parameters, e.g. accounturi and validationmethods.
	Params map[string]string
}

// ParseCAA parses a record in presentation format: 0 issue "letsencrypt.org; accounturi=...".
func ParseCAA(data string) (CAA, error) {
	fields := strings.SplitN(strings.TrimSpace(data), " ", 3)
	if len(fields) != 3 {
		return CAA{}, fmt.Errorf("%w: CAA record %q", ErrMalformed, data)
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		return CAA{}, fmt.Errorf("%w: CAA flags in %q", ErrMalformed, data)
	}
	value, err := strconv.Unquote(fields[2])
	if err != nil {
		return CAA{}, fmt.Errorf("%w: CAA value in %q", ErrMalformed, data)
	}
	rec := CAA{Tag: strings.ToLower(fields[1]), Params: map[string]string{}}
	parts := strings.Split(value, ";")
	rec.Issuer = strings.ToLower(strings.TrimSpace(parts[0]))
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok {
			rec.Params[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return rec, nil
}

// LookupCAA asks a DNS-over-HTTPS resolver (JSON API, e.g.
// https://cloudflare-dns.com/dns-query) for the CAA records of name itself; it
// does not climb to the parent.
func LookupCAA(ctx context.Context, client *http.Client, resolver, name string) ([]CAA, error) {
	var answer struct {
		Status int
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}
	}
	u := resolver + "?name=" + url.QueryEscape(name) + "&type=CAA"
	if err := getJSON(ctx, client, u, "application/dns-json", &answer); err != nil {
		return nil, fmt.Errorf("CAA of %s: %w", name, err)
	}
	if answer.Status != 0 && answer.Status != 3 { // 3: the name does not exist
		return nil, fmt.Errorf("CAA of %s: DNS status %d", name, answer.Status)
	}
	var out []CAA
	for _, a := range answer.Answer {
		if a.Type != 257 {
			continue // a CNAME on the way
		}
		rec, err := ParseCAA(a.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// CheckPinned reports whether records let only issuer issue for the name, only
// for the ACME account accountURI and only with the validation method: the
// CAA records of a host whose certificates must come from one account.
func CheckPinned(records []CAA, issuer, accountURI, method string) error {
	issue := 0
	for _, r := range records {
		if r.Tag != "issue" {
			continue
		}
		issue++
		switch {
		case r.Issuer != issuer:
			return fmt.Errorf("lets %q issue, expected only %s", r.Issuer, issuer)
		case r.Params["accounturi"] != accountURI:
			return fmt.Errorf("does not limit %s to the enclave's ACME account (accounturi %q)", issuer, r.Params["accounturi"])
		case r.Params["validationmethods"] != method:
			return fmt.Errorf("does not limit validation to %s (validationmethods %q)", method, r.Params["validationmethods"])
		}
	}
	if issue == 0 {
		return errors.New("has no issue record")
	}
	return nil
}

// CheckNoWildcard reports whether records forbid wildcard certificates for the
// name: an issuewild record with the value ";" and no other.
func CheckNoWildcard(records []CAA) error {
	wild := 0
	for _, r := range records {
		if r.Tag != "issuewild" {
			continue
		}
		wild++
		if r.Issuer != "" {
			return fmt.Errorf("lets %q issue wildcards", r.Issuer)
		}
	}
	if wild == 0 {
		return errors.New("has no issuewild record, so whoever may issue for it may issue wildcards")
	}
	return nil
}

// Issuance is one certificate (or precertificate) seen in Certificate Transparency.
type Issuance struct {
	ID           string    `json:"id"`
	DNSNames     []string  `json:"dns_names"`
	PubkeySHA256 string    `json:"pubkey_sha256"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	Revoked      bool      `json:"revoked"`
	Issuer       struct {
		FriendlyName string `json:"friendly_name"`
		Name         string `json:"name"`
	} `json:"issuer"`
}

// maxPages bounds how many pages of issuances are read.
const maxPages = 50

// Issuances lists what Certificate Transparency has for domain and its
// subdomains, through a Cert Spotter compatible API (api is the issuances
// endpoint, e.g. https://api.certspotter.com/v1/issuances). The answer is only
// as complete as that service's view of the logs.
func Issuances(ctx context.Context, client *http.Client, api, domain string) ([]Issuance, error) {
	var all []Issuance
	after := ""
	for range maxPages {
		q := url.Values{"domain": {domain}, "include_subdomains": {"true"}, "expand": {"dns_names", "issuer"}}
		if after != "" {
			q.Set("after", after)
		}
		var page []Issuance
		if err := getJSON(ctx, client, api+"?"+q.Encode(), "application/json", &page); err != nil {
			return nil, fmt.Errorf("certificate transparency: %w", err)
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		after = page[len(page)-1].ID
		if after == "" {
			return nil, fmt.Errorf("%w: issuance without id", ErrMalformed)
		}
	}
	return nil, errors.New("certificate transparency: too many pages")
}

// Covers reports whether a certificate name (possibly a wildcard) is valid for host.
func Covers(name, host string) bool {
	name, host = strings.ToLower(name), strings.ToLower(host)
	if name == host {
		return true
	}
	rest, ok := strings.CutPrefix(name, "*.")
	if !ok {
		return false
	}
	_, parent, ok := strings.Cut(host, ".")
	return ok && parent == rest
}

// Report sorts the unexpired, unrevoked certificates that are valid for the hosts.
type Report struct {
	// Own have the expected key.
	Own []Issuance
	// Foreign have another key and were issued once the expected key existed:
	// someone other than the enclave holds a certificate for its names.
	Foreign []Issuance
	// Older have another key but were issued before the expected key existed.
	// They are still valid, and nothing here can tell who holds them.
	Older []Issuance
}

// Classify sorts issuances for hosts. key is the hex SHA-256 of the expected
// certificate key and since is when that key was created.
func Classify(issuances []Issuance, hosts []string, key string, since, now time.Time) Report {
	var r Report
	for _, i := range issuances {
		if i.Revoked || !now.Before(i.NotAfter) {
			continue
		}
		if !slices.ContainsFunc(i.DNSNames, func(n string) bool {
			return slices.ContainsFunc(hosts, func(h string) bool { return Covers(n, h) })
		}) {
			continue
		}
		switch {
		case strings.EqualFold(i.PubkeySHA256, key):
			r.Own = append(r.Own, i)
		case i.NotBefore.Before(since):
			r.Older = append(r.Older, i)
		default:
			r.Foreign = append(r.Foreign, i)
		}
	}
	return r
}

func getJSON(ctx context.Context, client *http.Client, u, accept string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return nil
}
