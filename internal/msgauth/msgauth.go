// Package msgauth checks DKIM signatures of incoming mail. DNS lookups go over
// DNS-over-HTTPS from inside the enclave (through the egress proxy, TLS end to
// end), so the untrusted parent instance cannot forge the answers.
package msgauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/permaily-org/permaily/internal/inbox"
)

// MaxSignatures bounds the work per message.
const MaxSignatures = 5

// LookupTXT resolves TXT records.
type LookupTXT func(ctx context.Context, name string) ([]string, error)

// DKIM returns an inbox.Authenticator that verifies DKIM signatures.
func DKIM(lookup LookupTXT) inbox.Authenticator {
	return func(ctx context.Context, raw []byte) []inbox.AuthResult {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		verifications, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
			LookupTXT:        func(name string) ([]string, error) { return lookup(ctx, name) },
			MaxVerifications: MaxSignatures,
		})
		if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
			return []inbox.AuthResult{{Method: "dkim", Result: "fail: " + err.Error()}}
		}
		out := make([]inbox.AuthResult, 0, len(verifications))
		for _, v := range verifications {
			r := inbox.AuthResult{Method: "dkim", Domain: strings.ToLower(v.Domain), Result: "pass"}
			for _, k := range v.HeaderKeys {
				r.Headers = append(r.Headers, strings.ToLower(strings.TrimSpace(k)))
			}
			if v.Err != nil {
				r.Result = "fail: " + v.Err.Error()
				// go-msgauth refuses a signature with l= (it signs only part
				// of the body); the check of Gmail's confirmation is told so.
				r.BodyLimited = strings.Contains(v.Err.Error(), "body length")
			}
			out = append(out, r)
		}
		return out
	}
}

// DoH resolves TXT records with DNS-over-HTTPS (JSON API, RFC 8484 companion
// format used by Cloudflare and Google).
type DoH struct {
	Client   *http.Client
	Endpoint string // e.g. https://cloudflare-dns.com/dns-query
}

type dohAnswer struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// LookupTXT implements LookupTXT.
func (d DoH) LookupTXT(ctx context.Context, name string) ([]string, error) {
	u := d.Endpoint + "?type=TXT&name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	res, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh: HTTP %d", res.StatusCode)
	}
	var a dohAnswer
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&a); err != nil {
		return nil, fmt.Errorf("doh: %w", err)
	}
	if a.Status == 3 { // NXDOMAIN
		return nil, fmt.Errorf("doh: %s: no such domain", name)
	}
	if a.Status != 0 {
		return nil, fmt.Errorf("doh: %s: rcode %d", name, a.Status)
	}
	var out []string
	for _, ans := range a.Answer {
		if ans.Type == 16 { // TXT
			out = append(out, joinTXT(ans.Data))
		}
	}
	return out, nil
}

// joinTXT turns `"part one" "part two"` into `part onepart two`.
func joinTXT(data string) string {
	if !strings.HasPrefix(data, `"`) {
		return data
	}
	var b strings.Builder
	in := false
	escaped := false
	for _, r := range data {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && in:
			escaped = true
		case r == '"':
			in = !in
		case in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
