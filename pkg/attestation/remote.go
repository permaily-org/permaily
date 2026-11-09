package attestation

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotTLS is returned when the attestation endpoint is not reached over TLS.
var ErrNotTLS = errors.New("attestation: endpoint not reached over TLS")

// RemoteResult is the outcome of verifying a live enclave.
type RemoteResult struct {
	URL         string
	Commit      string
	Certificate *x509.Certificate
	SPKIHash    []byte
	Document    *Document
}

type remoteResponse struct {
	Document       string `json:"document"`
	UserDataScheme string `json:"user_data_scheme"`
	Commit         string `json:"commit"`
}

// VerifyRemote fetches an attestation document from baseURL/attestation with
// a fresh random nonce and verifies it. The user data must equal the SPKI hash
// of the TLS certificate presented on the same connection, which proves that
// the TLS session terminates inside the attested enclave. opts.Nonce and
// opts.UserData are set by this function; set opts.ExpectedPCRs.
//
// client must verify TLS certificates normally (the default client does).
func VerifyRemote(ctx context.Context, client *http.Client, baseURL string, opts Options) (*RemoteResult, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	url := strings.TrimRight(baseURL, "/") + "/attestation?nonce=" + hex.EncodeToString(nonce)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch attestation: %w", err)
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil, ErrNotTLS
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetch attestation: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var body remoteResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: response: %v", ErrMalformed, err)
	}
	if body.UserDataScheme != UserDataScheme {
		return nil, fmt.Errorf("%w: unsupported user_data_scheme %q", ErrMalformed, body.UserDataScheme)
	}
	raw, err := base64.StdEncoding.DecodeString(body.Document)
	if err != nil {
		return nil, fmt.Errorf("%w: document encoding: %v", ErrMalformed, err)
	}

	leaf := resp.TLS.PeerCertificates[0]
	spki := SPKIHash(leaf)
	opts.Nonce = nonce
	opts.UserData = spki
	doc, err := Verify(raw, opts)
	if err != nil {
		return nil, err
	}
	return &RemoteResult{URL: baseURL, Commit: body.Commit, Certificate: leaf, SPKIHash: spki, Document: doc}, nil
}
