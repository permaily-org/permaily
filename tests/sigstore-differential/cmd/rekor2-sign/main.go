// Command rekor2-sign makes, today, what GitHub's artifact attestations will
// be once they are recorded in Rekor v2: build provenance for a file, signed
// with a certificate Fulcio issues to this workflow run, recorded in
// Sigstore's public Rekor v2 log, with a timestamp from Sigstore's authority.
//
//	rekor2-sign -trusted-root pkg/provenance/sigstore_trusted_root.json -file pcrs.json -out bundle.json
//
// It runs in GitHub Actions, in a job with `id-token: write`. The workflow
// sigstore-watch uses it so that pkg/provenance is checked every day against
// what the public log really answers, before GitHub's own attestations move
// there (docs/operations.md, "Sigstore changed").
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	fulcioURL = "https://fulcio.sigstore.dev"
	firstLog  = "https://rekor.sigstore.dev"
)

func main() {
	trusted := flag.String("trusted-root", "", "Sigstore's trusted_root.json: names the Rekor v2 log and the timestamp authority to use")
	file := flag.String("file", "", "the file to attest")
	out := flag.String("out", "", "where to write the bundle")
	flag.Parse()
	if err := run(*trusted, *file, *out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(trustedPath, filePath, outPath string) error {
	if trustedPath == "" || filePath == "" || outPath == "" {
		return errors.New("need -trusted-root, -file and -out")
	}
	trustedJSON, err := os.ReadFile(trustedPath)
	if err != nil {
		return err
	}
	logURL, tsaURL, err := services(trustedJSON)
	if err != nil {
		return err
	}
	trusted, err := root.NewTrustedRootFromJSON(trustedJSON)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	statement, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": filePath, "digest": map[string]string{"sha256": hex.EncodeToString(digest[:])}}},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{},
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	token, err := actionsToken(ctx)
	if err != nil {
		return err
	}
	key, err := sign.NewEphemeralKeypair(nil)
	if err != nil {
		return err
	}
	fmt.Printf("log %s, timestamp authority %s\n", logURL, tsaURL)
	bundle, err := sign.Bundle(&sign.DSSEData{Data: statement, PayloadType: "application/vnd.in-toto+json"}, key, sign.BundleOptions{
		Context:                    ctx,
		CertificateProvider:        sign.NewFulcio(&sign.FulcioOptions{BaseURL: fulcioURL, Timeout: 30 * time.Second, Retries: 2}),
		CertificateProviderOptions: &sign.CertificateProviderOptions{IDToken: token},
		TimestampAuthorities:       []*sign.TimestampAuthority{sign.NewTimestampAuthority(&sign.TimestampAuthorityOptions{URL: tsaURL, Timeout: 30 * time.Second, Retries: 2})},
		// Rekor v2 answers once a checkpoint covers the entry, which takes a while.
		TransparencyLogs: []sign.Transparency{sign.NewRekor(&sign.RekorOptions{BaseURL: logURL, Version: 2, Timeout: 90 * time.Second, Retries: 2})},
		TrustedRoot:      trusted,
	})
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	encoded, err := protojson.Marshal(bundle)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, encoded, 0o644) //nolint:gosec // a public bundle
}

// services picks, from the trust root, the newest log that is not the first
// Rekor and still takes entries, and the newest timestamp authority in use.
func services(trustedJSON []byte) (logURL, tsaURL string, err error) {
	type period struct {
		ValidFor struct{ Start, End string }
	}
	var doc struct {
		Tlogs []struct {
			BaseURL   string `json:"baseUrl"`
			PublicKey period
		}
		TimestampAuthorities []struct {
			URI string `json:"uri"`
			period
		}
	}
	if err := json.Unmarshal(trustedJSON, &doc); err != nil {
		return "", "", err
	}
	for _, l := range doc.Tlogs {
		if l.BaseURL != firstLog && l.PublicKey.ValidFor.End == "" {
			logURL = l.BaseURL
		}
	}
	for _, t := range doc.TimestampAuthorities {
		if t.ValidFor.End == "" {
			tsaURL = t.URI
		}
	}
	if logURL == "" || tsaURL == "" {
		return "", "", errors.New("the trust root names no Rekor v2 log or no timestamp authority in use")
	}
	return logURL, tsaURL, nil
}

// actionsToken asks GitHub Actions for an identity token for Sigstore.
func actionsToken(ctx context.Context) (string, error) {
	url, bearer := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if url == "" || bearer == "" {
		return "", errors.New("no identity token: run in GitHub Actions with id-token: write")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"&audience=sigstore", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+bearer)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if err != nil {
		return "", err
	}
	var answer struct{ Value string }
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &answer) != nil || answer.Value == "" {
		return "", fmt.Errorf("identity token: HTTP %d", res.StatusCode)
	}
	return answer.Value, nil
}
