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
	// TLS describes the certificate key and the ACME account; nil in images
	// older than this field.
	TLS *TLSStatus `json:"tls,omitempty"`
}

// TLSStatus is what the enclave reports about its own TLS identity.
type TLSStatus struct {
	// SPKISHA256 is the hex SHA-256 of the certificate key, the same value
	// bound in attestation documents.
	SPKISHA256 string `json:"spki_sha256"`
	// KeyCreatedAt is when the key was generated. A certificate for the
	// enclave's names issued after this time with another key was not issued
	// to the enclave.
	KeyCreatedAt time.Time `json:"key_created_at"`
	// Sealed is true when the key is stored sealed with KMS and so survives
	// restarts; false means it lives only in this enclave's memory.
	Sealed bool `json:"sealed"`
	// ACMEAccount is the URL of the ACME account that asks for certificates,
	// empty until the first one is issued. CAA records should name it.
	ACMEAccount string `json:"acme_account,omitempty"`
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

	// WatchedSince is when this enclave started reading the policy, about
	// once a minute. Zero in images that do not keep a history.
	WatchedSince time.Time `json:"watched_since,omitzero"`
	// History is every different state of the policy and the grants the
	// enclave has read since then, oldest first; the last one is the current
	// state. A policy that was changed and changed back shows up here.
	History []KMSObservation `json:"history,omitempty"`
	// HistoryTruncated means older states were dropped to bound the answer.
	HistoryTruncated bool `json:"history_truncated,omitempty"`
	// Gaps are the periods in which the enclave could not read from KMS. A
	// change made and undone inside a gap is not in History.
	Gaps          []KMSGap `json:"gaps,omitempty"`
	GapsTruncated bool     `json:"gaps_truncated,omitempty"`
}

// KMSObservation is one state of the key policy and grants, and the first and
// last time the enclave read it without reading a different one in between.
type KMSObservation struct {
	Policy          string    `json:"policy"`
	Grants          int       `json:"grants"`
	GrantsTruncated bool      `json:"grants_truncated,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

// KMSGap is a period between two successful readings with failed ones in between.
type KMSGap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
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
