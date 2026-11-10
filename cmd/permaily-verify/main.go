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
//
// Then it reads /status over that same TLS key and checks the policy of the KMS
// key that protects stored mail: data keys must be released only to the image
// being verified, with no grants and no statement that skips the image check.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
)

// maxPolicyAge bounds how old the enclave's reading of the KMS policy may be.
const maxPolicyAge = 5 * time.Minute

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
	// KMS is nil when the check was skipped or the image has no KMS key.
	KMS        *kmsResult `json:"kms,omitempty"`
	KMSSkipped string     `json:"kms_skipped,omitempty"`
}

// kmsResult is what the verified enclave reported about the key that wraps
// mailbox data keys, after the policy passed every check.
type kmsResult struct {
	KeyARN       string    `json:"key_arn"`
	PolicySHA256 string    `json:"policy_sha256"`
	Images       []string  `json:"images"`
	ReadAt       time.Time `json:"read_at"`
	// AdministratorsCanChangePolicy means the result describes the policy only
	// at ReadAt: account administrators may replace it later.
	AdministratorsCanChangePolicy bool `json:"administrators_can_change_policy"`
}

func main() {
	var (
		pcrsFile = flag.String("pcrs", "", "pcrs.json from your own reproducible build (recommended)")
		release  = flag.String("release", "", "use pcrs.json published in this GitHub release instead of a local build (weaker: trusts CI)")
		domain   = flag.String("domain", "permaily.com", "parent domain; checks app., api., mcp. and inbox. when no URLs are given")
		asJSON   = flag.Bool("json", false, "print machine-readable JSON")
		timeout  = flag.Duration("timeout", 30*time.Second, "per-host timeout")
		caFile   = flag.String("ca-file", "", "extra PEM root CAs to trust for TLS (e.g. Let's Encrypt staging roots for test deployments)")
		kmsAllow = flag.String("kms-also-allow", "", "comma-separated PCR0 values of other images you accept in the KMS key policy (e.g. the previous release during an upgrade)")
		skipKMS  = flag.Bool("skip-kms-policy", false, "do not check the KMS key policy (releases before v0.3.0 do not report it)")
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
		r := verify(ctx, client, u, expected, kmsOptions{skip: *skipKMS, alsoAllow: splitList(*kmsAllow), now: time.Now()})
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

type kmsOptions struct {
	skip      bool
	alsoAllow []string
	now       time.Time
}

func verify(ctx context.Context, client *http.Client, url string, expected map[uint][]byte, kms kmsOptions) hostResult {
	res, err := attestation.VerifyRemote(ctx, client, url, attestation.Options{ExpectedPCRs: expected})
	if err != nil {
		return hostResult{URL: url, Error: err.Error()}
	}
	r := hostResult{
		URL:       url,
		Commit:    res.Commit,
		ModuleID:  res.Document.ModuleID,
		Timestamp: res.Document.Timestamp.UTC(),
		SPKIHash:  hex.EncodeToString(res.SPKIHash),
		CertNames: res.Certificate.DNSNames,
		PCRs:      hexPCRs(pick(res.Document.PCRs, attestation.MeasuredPCRs)),
	}
	if kms.skip {
		r.OK, r.KMSSkipped = true, "not checked (-skip-kms-policy)"
		return r
	}
	st, err := attestation.FetchStatus(ctx, client, url, res.SPKIHash)
	if err != nil {
		r.Error = "KMS key policy: " + err.Error()
		if strings.Contains(err.Error(), "HTTP 404") {
			r.Error += " (this image does not report it; releases before v0.3.0 need -skip-kms-policy)"
		}
		return r
	}
	if st.KMS == nil {
		r.OK, r.KMSSkipped = true, "this image uses no KMS key"
		return r
	}
	if r.KMS, err = checkKMS(st.KMS, hex.EncodeToString(expected[0]), kms.alsoAllow, kms.now); err != nil {
		r.KMS = nil
		r.Error = "KMS key policy: " + err.Error()
		return r
	}
	r.OK = true
	return r
}

// checkKMS decides whether the key policy the enclave reported protects stored
// mail: only the verified image (pcr0, hex) and the images in alsoAllow may get
// data keys, nothing skips the image check, and no grant exists.
func checkKMS(k *attestation.KMSStatus, pcr0 string, alsoAllow []string, now time.Time) (*kmsResult, error) {
	if age := now.Sub(k.FetchedAt); age > maxPolicyAge || age < -maxPolicyAge {
		return nil, fmt.Errorf("read by the enclave at %s, not within %s of now", k.FetchedAt.UTC().Format(time.RFC3339), maxPolicyAge)
	}
	rep, err := kmspolicy.Analyze([]byte(k.Policy))
	if err != nil {
		return nil, err
	}
	if len(rep.Problems) > 0 {
		return nil, errors.New(strings.Join(rep.Problems, "; "))
	}
	if k.Grants > 0 || k.GrantsTruncated {
		return nil, fmt.Errorf("the key has %d grant(s); grants give access outside the policy", k.Grants)
	}
	var others []string
	for _, img := range rep.Images {
		if img != strings.ToLower(pcr0) && !slices.Contains(alsoAllow, img) {
			others = append(others, img)
		}
	}
	if len(others) > 0 {
		return nil, fmt.Errorf("also releases data keys to image(s) you did not verify: %s (accept them with -kms-also-allow)", strings.Join(others, ", "))
	}
	sum := sha256.Sum256([]byte(k.Policy))
	return &kmsResult{
		KeyARN:                        k.KeyARN,
		PolicySHA256:                  hex.EncodeToString(sum[:]),
		Images:                        rep.Images,
		ReadAt:                        k.FetchedAt.UTC(),
		AdministratorsCanChangePolicy: rep.AdminCanChangePolicy,
	}, nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
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
		printKMS(w, r, hex.EncodeToString(expected[0]))
	}
}

func printKMS(w io.Writer, r hostResult, pcr0 string) {
	if r.KMS == nil {
		fmt.Fprintf(w, "      KMS key policy: %s\n", r.KMSSkipped)
		return
	}
	who := "only to this image"
	switch {
	case len(r.KMS.Images) == 0:
		who = "to no image"
	case !slices.Contains(r.KMS.Images, pcr0):
		who = fmt.Sprintf("to %d image(s) you accepted, not to this one", len(r.KMS.Images))
	case len(r.KMS.Images) > 1:
		who = fmt.Sprintf("to this image and %d more you accepted", len(r.KMS.Images)-1)
	}
	fmt.Fprintf(w, "      KMS key %s releases data keys %s; no grants\n", r.KMS.KeyARN, who)
	fmt.Fprintf(w, "      KMS key policy sha256 %s, read by the enclave at %s\n", r.KMS.PolicySHA256, r.KMS.ReadAt.Format(time.RFC3339))
	if r.KMS.AdministratorsCanChangePolicy {
		fmt.Fprintln(w, "      note: account administrators can replace that policy; this is its state at that time")
	}
}
