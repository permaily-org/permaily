// Command permaily-verify checks that a deployed Permaily enclave runs the
// image built from the public source.
//
//	nix build .#eif                    # reproduce the image, writes result/pcrs.json
//	permaily-verify -pcrs result/pcrs.json
//
// For each host it fetches /attestation with a fresh nonce and checks the AWS
// Nitro signature and certificate chain, freshness, the nonce, that PCR0-2
// are not zero (debug mode), that PCR0-2 equal the reproduced build, and that
// the attestation is bound to the TLS key of the very connection it came over.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

const releaseURL = "https://github.com/permaily-org/permaily/releases/download/%s/pcrs.json"

type hostResult struct {
	URL       string            `json:"url"`
	OK        bool              `json:"ok"`
	Error     string            `json:"error,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	ModuleID  string            `json:"module_id,omitempty"`
	Timestamp time.Time         `json:"timestamp,omitzero"`
	SPKIHash  string            `json:"tls_spki_sha256,omitempty"`
	CertNames []string          `json:"tls_dns_names,omitempty"`
	PCRs      map[string]string `json:"pcrs,omitempty"`
}

func main() {
	var (
		pcrsFile = flag.String("pcrs", "", "pcrs.json from your own reproducible build (recommended)")
		release  = flag.String("release", "", "use pcrs.json published in this GitHub release instead of a local build (weaker: trusts CI)")
		domain   = flag.String("domain", "permaily.com", "parent domain; checks app., api., mcp. and inbox. when no URLs are given")
		asJSON   = flag.Bool("json", false, "print machine-readable JSON")
		timeout  = flag.Duration("timeout", 30*time.Second, "per-host timeout")
		caFile   = flag.String("ca-file", "", "extra PEM root CAs to trust for TLS (e.g. Let's Encrypt staging roots for test deployments)")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: permaily-verify (-pcrs FILE | -release TAG) [flags] [https://host ...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	expected, source, err := loadPCRs(*pcrsFile, *release)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	urls := flag.Args()
	if len(urls) == 0 {
		for _, sub := range []string{"app", "api", "mcp", "inbox"} {
			urls = append(urls, "https://"+sub+"."+*domain)
		}
	}

	client, err := httpClient(*caFile, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	results := make([]hostResult, 0, len(urls))
	allOK := true
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		r := verify(ctx, client, u, expected)
		cancel()
		allOK = allOK && r.OK
		results = append(results, r)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": allOK, "expected_pcrs_source": source, "expected_pcrs": hexPCRs(expected), "hosts": results})
	} else {
		printHuman(os.Stdout, source, expected, results)
	}
	if !allOK {
		os.Exit(1)
	}
}

func verify(ctx context.Context, client *http.Client, url string, expected map[uint][]byte) hostResult {
	res, err := attestation.VerifyRemote(ctx, client, url, attestation.Options{ExpectedPCRs: expected})
	if err != nil {
		return hostResult{URL: url, Error: err.Error()}
	}
	return hostResult{
		URL:       url,
		OK:        true,
		Commit:    res.Commit,
		ModuleID:  res.Document.ModuleID,
		Timestamp: res.Document.Timestamp.UTC(),
		SPKIHash:  hex.EncodeToString(res.SPKIHash),
		CertNames: res.Certificate.DNSNames,
		PCRs:      hexPCRs(pick(res.Document.PCRs, attestation.MeasuredPCRs)),
	}
}

func httpClient(caFile string, timeout time.Duration) (*http.Client, error) {
	if caFile == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // path given by the user on purpose
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no PEM certificates", caFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

func loadPCRs(file, release string) (map[uint][]byte, string, error) {
	switch {
	case file != "" && release != "":
		return nil, "", fmt.Errorf("use either -pcrs or -release, not both")
	case file != "":
		data, err := os.ReadFile(file) //nolint:gosec // path given by the user on purpose
		if err != nil {
			return nil, "", err
		}
		pcrs, err := attestation.ParsePCRFile(data)
		return pcrs, "local build: " + file, err
	case release != "":
		url := fmt.Sprintf(releaseURL, release)
		resp, err := http.Get(url) //nolint:gosec // fixed GitHub URL with the tag given by the user
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if err != nil {
			return nil, "", err
		}
		pcrs, err := attestation.ParsePCRFile(data)
		return pcrs, "GitHub release " + release + " (not rebuilt locally)", err
	default:
		return nil, "", fmt.Errorf("missing -pcrs FILE (from `nix build .#eif`) or -release TAG")
	}
}

func pick(m map[uint][]byte, keys []uint) map[uint][]byte {
	out := map[uint][]byte{}
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func hexPCRs(m map[uint][]byte) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[fmt.Sprintf("PCR%d", k)] = hex.EncodeToString(v)
	}
	return out
}

func printHuman(w io.Writer, source string, expected map[uint][]byte, results []hostResult) {
	fmt.Fprintf(w, "Expected measurements (%s):\n", source)
	for _, i := range attestation.MeasuredPCRs {
		fmt.Fprintf(w, "  PCR%d %s\n", i, hex.EncodeToString(expected[i]))
	}
	fmt.Fprintln(w)
	for _, r := range results {
		if !r.OK {
			fmt.Fprintf(w, "FAIL  %s\n      %s\n", r.URL, r.Error)
			continue
		}
		fmt.Fprintf(w, "PASS  %s\n", r.URL)
		fmt.Fprintf(w, "      commit %s, module %s, attested at %s\n", r.Commit, r.ModuleID, r.Timestamp.Format(time.RFC3339))
		fmt.Fprintf(w, "      TLS key sha256(spki) %s bound in attestation\n", r.SPKIHash)
		fmt.Fprintf(w, "      certificate names: %s\n", strings.Join(r.CertNames, ", "))
		fmt.Fprintln(w, "      AWS Nitro signature and chain OK, nonce OK, not debug mode, PCR0-2 match")
	}
}
