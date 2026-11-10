package attestation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotBound means an answer came over a TLS connection whose key is not the
// one the attestation document vouches for, so it may not come from the enclave.
var ErrNotBound = errors.New("attestation: not served with the attested TLS key")

// Status is the JSON body of GET /status: facts the enclave reports about its
// own environment. It carries no signature of its own; it is trusted only when
// fetched over the TLS key bound in an attestation document (FetchStatus).
type Status struct {
	Commit string `json:"commit"`
	// KMS describes the key that wraps mailbox data keys; nil when the image
	// stores no mail.
	KMS *KMSStatus `json:"kms"`
}

// KMSStatus is what the enclave read from AWS KMS about its pinned key.
type KMSStatus struct {
	KeyARN string `json:"key_arn"`
	// Policy is the key policy document, exactly as KMS returned it.
	Policy string `json:"policy"`
	// Grants counts the grants on the key. Grants give access outside the
	// policy, so a verifier expects none.
	Grants          int  `json:"grants"`
	GrantsTruncated bool `json:"grants_truncated"`
	// FetchedAt is when the enclave read the policy and the grants.
	FetchedAt time.Time `json:"fetched_at"`
}

// FetchStatus gets baseURL/status and checks that the TLS certificate of that
// connection has the key whose SPKI hash is spkiHash, taken from a verified
// attestation (RemoteResult.SPKIHash). The enclave image holds that key, so
// the body was produced by the attested code.
func FetchStatus(ctx context.Context, client *http.Client, baseURL string, spkiHash []byte) (*Status, error) {
	body, err := fetchBound(ctx, client, strings.TrimRight(baseURL, "/")+"/status", spkiHash)
	if err != nil {
		return nil, fmt.Errorf("fetch status: %w", err)
	}
	var st Status
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("%w: status: %w", ErrMalformed, err)
	}
	return &st, nil
}

// fetchBound gets url and returns the body only when the connection's TLS
// certificate has the attested key (spkiHash) and the answer is 200.
func fetchBound(ctx context.Context, client *http.Client, url string, spkiHash []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil, ErrNotTLS
	}
	if !bytes.Equal(SPKIHash(resp.TLS.PeerCertificates[0]), spkiHash) {
		return nil, ErrNotBound
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 512)])))
	}
	return body, nil
}
