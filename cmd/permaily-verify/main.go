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
// When the enclave sends mail, it also checks that Amazon SES was not told to
// keep a copy of it, now or at any time the enclave has watched.
//
// Last, for the default hosts of a domain, it checks how mail gets in: the
// MTA-STS policy (served with the attested key) must tell senders to require
// TLS, the DNS must point senders at it, and the MX on port 25 must answer
// STARTTLS with the attested key.
//
// And it looks for certificates the enclave does not hold: the CAA records
// must let only the enclave's ACME account issue for its hosts and forbid
// wildcards for the domain, and Certificate Transparency must show no
// certificate for those hosts, issued since the enclave's key exists, with
// another key.
//
// With -trust-update it checks no deployment either: it says whether a
// directory holds a trust root of Sigstore's that an enclave of this code
// would take in place of the one compiled in.
//
// With -provenance it checks no deployment. It answers, offline, the question
// a running enclave asks before it hands the root secret to a release: do
// these measurements come from the release workflow, in the public log?
//
//	permaily-verify -provenance bundle.json -pcrs pcrs.json
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
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/certwatch"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
	"github.com/permaily-org/permaily/pkg/provenance"
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
	// Root is what the enclave reported about the key that holds its root
	// secret, after it passed every check; nil when the check was skipped or
	// the image has no root secret.
	Root *rootResult `json:"root,omitempty"`
	// KMS is nil when the check was skipped or the image has no KMS key that
	// wraps data keys directly (images before the root secret did).
	KMS        *kmsResult `json:"kms,omitempty"`
	KMSSkipped string     `json:"kms_skipped,omitempty"`
	// TLS is what the enclave reports about its certificate key and ACME
	// account; nil when /status was not read or the image is older.
	TLS *attestation.TLSStatus `json:"tls,omitempty"`
	// Sigstore is the trust root the enclave checks releases against, after it
	// passed checkSigstore; nil when the image is older or holds no root secret.
	Sigstore *attestation.SigstoreStatus `json:"sigstore,omitempty"`
	// Anchors are the records the enclave refuses an earlier copy of, after
	// they passed checkAnchors; nil when the image is older or has none.
	Anchors *attestation.AnchorStatus `json:"anchors,omitempty"`
	// SentMail is what the enclave reports about the mail it sends through
	// Amazon SES, after it passed checkSentMail; nil when the image is older
	// or sends no mail.
	SentMail *attestation.MailStatus `json:"sent_mail,omitempty"`
}

// rootResult is what the verified enclave reported about the KMS key that
// holds the root secret for its image, after that key passed every check: only
// this image can use it, and nobody can change that.
type rootResult struct {
	KeyARN       string `json:"key_arn"`
	PolicySHA256 string `json:"policy_sha256"`
	// KeyState is Enabled, or PendingDeletion once the key is scheduled for deletion.
	KeyState string `json:"key_state"`
	// Origin is "created" when an enclave of this image created the root
	// secret and "handed_over" when another release's enclave gave it; From is
	// that release, which the enclave accepted only with its public provenance.
	Origin    string                `json:"origin"`
	WrittenAt time.Time             `json:"written_at"`
	From      *attestation.RootFrom `json:"from,omitempty"`
	// ReadAt is when the enclave last read the key's policy. WatchedSince is
	// when it started reading it about once a minute (zero for v0.7.0, which
	// read it once, when it started); Changes counts the other states it saw
	// since then, all of which passed the same check. Gaps are periods in
	// which it could not read.
	ReadAt       time.Time            `json:"read_at"`
	WatchedSince time.Time            `json:"watched_since,omitzero"`
	Changes      int                  `json:"changes_since"`
	Gaps         []attestation.KMSGap `json:"gaps,omitempty"`
}

// checkRoot decides whether the key the enclave reported keeps the root secret
// to the verified image (pcr0, hex): now, and in every state of the key's
// policy the enclave has seen since it started.
func checkRoot(rs *attestation.RootStatus, pcr0 string, now time.Time) (*rootResult, error) {
	if !strings.EqualFold(rs.Image, pcr0) {
		return nil, fmt.Errorf("the enclave reports a key for image %s, not for the one verified", rs.Image)
	}
	if _, err := kmspolicy.Sealed([]byte(rs.Policy), rs.Grants, rs.GrantsTruncated, rs.Key, pcr0); err != nil {
		return nil, err
	}
	// Nobody in the account can change the policy, but AWS can for the
	// account's owner. So an enclave that watches its key must have read it
	// just now, and every state it saw must pass the same check.
	if watched := !rs.WatchedSince.IsZero(); watched {
		if age := now.Sub(rs.CheckedAt); age > maxPolicyAge || age < -maxPolicyAge {
			return nil, fmt.Errorf("the key's policy was read by the enclave at %s, not within %s of now", rs.CheckedAt.UTC().Format(time.RFC3339), maxPolicyAge)
		}
		if rs.HistoryTruncated || len(rs.History) == 0 {
			return nil, errors.New("the key's policy changed more times than the enclave remembers since it started; its history is incomplete")
		}
		last := rs.History[len(rs.History)-1]
		if last.Policy != rs.Policy || last.Grants != rs.Grants || last.GrantsTruncated != rs.GrantsTruncated {
			return nil, errors.New("the history the enclave reports does not end in the key's current policy")
		}
		for _, past := range rs.History[:len(rs.History)-1] {
			if _, err := kmspolicy.Sealed([]byte(past.Policy), past.Grants, past.GrantsTruncated, rs.Key, pcr0); err != nil {
				return nil, fmt.Errorf("from %s to %s: %w", past.FirstSeen.UTC().Format(time.RFC3339), past.LastSeen.UTC().Format(time.RFC3339), err)
			}
		}
	}
	switch {
	case rs.Origin == attestation.RootCreated && rs.From == nil:
	case rs.Origin == attestation.RootHandedOver && rs.From != nil && rs.From.Image != "" && !strings.EqualFold(rs.From.Image, pcr0):
	default:
		return nil, fmt.Errorf("the enclave does not say where its root secret came from (origin %q)", rs.Origin)
	}
	sum := sha256.Sum256([]byte(rs.Policy))
	res := &rootResult{
		KeyARN: rs.KeyARN, PolicySHA256: hex.EncodeToString(sum[:]), KeyState: rs.Key.State, Origin: rs.Origin, WrittenAt: rs.WrittenAt.UTC(), From: rs.From,
		ReadAt: rs.CheckedAt.UTC(), WatchedSince: rs.WatchedSince.UTC(), Gaps: rs.Gaps,
	}
	if len(rs.History) > 0 {
		res.Changes = len(rs.History) - 1
	}
	if rs.WatchedSince.IsZero() {
		res.WatchedSince = time.Time{}
	}
	return res, nil
}

// kmsResult is what the verified enclave reported about the key that wraps
// mailbox data keys, after the policy passed every check.
type kmsResult struct {
	KeyARN       string    `json:"key_arn"`
	PolicySHA256 string    `json:"policy_sha256"`
	Images       []string  `json:"images"`
	ReadAt       time.Time `json:"read_at"`
	// AdministratorsCanChangePolicy means the result describes the policy only
	// as far as the enclave watched it: account administrators may replace it.
	AdministratorsCanChangePolicy bool `json:"administrators_can_change_policy"`
	// WatchedSince is when the enclave started reading the policy about once a
	// minute; zero for an image that keeps no history. Changes counts the
	// other states it saw since then, all of which passed the same checks.
	WatchedSince time.Time `json:"watched_since,omitzero"`
	Changes      int       `json:"changes_since"`
	// Gaps are periods in which the enclave could not read the policy: a change
	// made and undone inside one would not be seen.
	Gaps []attestation.KMSGap `json:"gaps,omitempty"`
}

func main() {
	var (
		pcrsFile = flag.String("pcrs", "", "pcrs.json from your own reproducible build (recommended)")
		release  = flag.String("release", "", "use pcrs.json published in this GitHub release instead of a local build (weaker: trusts CI)")
		domain   = flag.String("domain", "permaily.com", "parent domain; checks app., api., mcp., inbox. and mta-sts.inbox., and the mail transport, when no URLs are given")
		asJSON   = flag.Bool("json", false, "print machine-readable JSON")
		timeout  = flag.Duration("timeout", 30*time.Second, "per-host timeout")
		caFile   = flag.String("ca-file", "", "extra PEM root CAs to trust for TLS (e.g. Let's Encrypt staging roots for test deployments)")
		kmsAllow = flag.String("kms-also-allow", "", "comma-separated PCR0 values of other images you accept in the KMS key policy (e.g. the previous release during an upgrade)")
		skipKMS  = flag.Bool("skip-kms-policy", false, "do not check the KMS key policy (releases before v0.3.0 do not report it)")
		skipMail = flag.Bool("skip-mail", false, "do not check how mail gets in (MTA-STS, DNS and SMTP on port 25)")
		needSMTP = flag.Bool("require-smtp", false, "fail when port 25 cannot be reached from this network, instead of warning")
		skipCert = flag.Bool("skip-certificates", false, "do not check CAA records and Certificate Transparency (releases before v0.4.0 do not report their TLS key)")
		resolver = flag.String("doh-resolver", "https://cloudflare-dns.com/dns-query", "DNS-over-HTTPS resolver (JSON API) for CAA records")
		ctAPI    = flag.String("ct-api", "https://api.certspotter.com/v1/issuances", "Certificate Transparency search API (Cert Spotter compatible)")
	)
	prov := provenanceFlags(flag.CommandLine)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: permaily-verify (-pcrs FILE | -release TAG) [flags] [https://host ...]\n       permaily-verify -provenance BUNDLE -pcrs FILE [-release-... flags]\n       permaily-verify -trust-update DIR\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if prov.bundle != "" {
		if !checkProvenance(os.Stdout, prov, *pcrsFile, *asJSON) {
			os.Exit(1)
		}
		return
	}
	// Sigstore's trust root as compiled into this binary: what an enclave of the
	// same release starts with.
	trust, err := provenance.PublicGood()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if prov.trustUpdate != "" {
		if !checkTrustUpdate(os.Stdout, trust, prov.trustUpdate, time.Now(), *asJSON) {
			os.Exit(1)
		}
		return
	}

	expected, source, err := loadPCRs(*pcrsFile, *release)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	urls := flag.Args()
	wholeDomain := len(urls) == 0
	if wholeDomain {
		for _, sub := range []string{"app", "api", "mcp", "inbox", "mta-sts.inbox"} {
			urls = append(urls, "https://"+sub+"."+*domain)
		}
	}

	client, roots, err := httpClient(*caFile, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	results := make([]hostResult, 0, len(urls))
	allOK := true
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		r := verify(ctx, client, u, expected, kmsOptions{skip: *skipKMS, noStatus: *skipKMS && *skipCert, alsoAllow: splitList(*kmsAllow), now: time.Now(), trust: trust})
		cancel()
		allOK = allOK && r.OK
		results = append(results, r)
	}

	// Mail is checked once per domain, with the key attested on the inbox host.
	var mail *mailResult
	if wholeDomain && !*skipMail {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		mail = checkMail(ctx, client, "inbox."+*domain, attestedKey(results, "https://inbox."+*domain), mailDeps{
			lookupTXT: net.DefaultResolver.LookupTXT,
			lookupMX:  net.DefaultResolver.LookupMX,
			roots:     roots,
		}, *needSMTP)
		cancel()
		allOK = allOK && mail.OK
	}

	// Certificates are checked once per domain too, against the key and the
	// ACME account the enclave reports on the app host.
	var certs *certResult
	if wholeDomain && !*skipCert {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		certs = checkCertificates(ctx, *domain, hostsOf(urls), reportedTLS(results, "https://app."+*domain), certDeps{
			client: &http.Client{Timeout: *timeout}, resolver: *resolver, ctAPI: *ctAPI, now: time.Now(),
		})
		cancel()
		allOK = allOK && certs.OK
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": allOK, "expected_pcrs_source": source, "expected_pcrs": hexPCRs(expected), "hosts": results, "mail": mail, "certificates": certs})
	} else {
		printHuman(os.Stdout, source, expected, results)
		printMail(os.Stdout, mail)
		printCertificates(os.Stdout, certs)
	}
	if !allOK {
		os.Exit(1)
	}
}

type kmsOptions struct {
	skip      bool // do not check the KMS key policy
	noStatus  bool // do not read /status at all
	alsoAllow []string
	now       time.Time
	// trust is Sigstore's trust root compiled into this binary, which the one
	// the enclave reports is compared with; nil skips that.
	trust *provenance.TrustRoot
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
		r.KMSSkipped = "not checked (-skip-kms-policy)"
	}
	if kms.noStatus {
		r.OK = true
		return r
	}
	st, err := attestation.FetchStatus(ctx, client, url, res.SPKIHash)
	if err != nil {
		if kms.skip && strings.Contains(err.Error(), "HTTP 404") {
			r.OK = true // an image from before /status existed
			return r
		}
		r.Error = "status: " + err.Error()
		if strings.Contains(err.Error(), "HTTP 404") {
			r.Error += " (this image does not report it; releases before v0.3.0 need -skip-kms-policy)"
		}
		return r
	}
	return checkStatus(st, r, hex.EncodeToString(expected[0]), kms)
}

// checkStatus applies the checks to what a verified enclave reports about
// itself on /status, and returns r with what passed. pcr0 is the verified
// image, in hex.
func checkStatus(st *attestation.Status, r hostResult, pcr0 string, kms kmsOptions) hostResult {
	var err error
	if st.TLS != nil {
		// The enclave must describe the key it is being verified with.
		if st.TLS.SPKISHA256 != r.SPKIHash {
			r.Error = "status: the enclave reports TLS key " + st.TLS.SPKISHA256 + ", not the attested one"
			return r
		}
		r.TLS = st.TLS
	}
	if kms.skip {
		r.OK = true
		return r
	}
	if st.Sigstore != nil && kms.trust != nil {
		if err := checkSigstore(st.Sigstore, kms.trust); err != nil {
			r.Error = "Sigstore trust root: " + err.Error()
			return r
		}
		r.Sigstore = st.Sigstore
	}
	if st.Anchors != nil {
		if err := checkAnchors(st.Anchors, kms.now); err != nil {
			r.Error = "records that must not go back: " + err.Error()
			return r
		}
		r.Anchors = st.Anchors
	}
	if st.Mail != nil {
		if err := checkSentMail(st.Mail, kms.now); err != nil {
			r.Error = "mail the enclave sends: " + err.Error()
			return r
		}
		r.SentMail = st.Mail
	}
	if st.Root != nil {
		if r.Root, err = checkRoot(st.Root, pcr0, kms.now); err != nil {
			r.Root = nil
			r.Error = "root secret: " + err.Error()
			return r
		}
	}
	if st.KMS == nil {
		r.OK = true
		if st.Root == nil {
			r.KMSSkipped = "this image uses no KMS key"
		}
		return r
	}
	if r.KMS, err = checkKMS(st.KMS, pcr0, kms.alsoAllow, kms.now); err != nil {
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
	current := attestation.KMSObservation{Policy: k.Policy, Grants: k.Grants, GrantsTruncated: k.GrantsTruncated}
	rep, err := checkPolicy(current, pcr0, alsoAllow)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(k.Policy))
	res := &kmsResult{
		KeyARN:                        k.KeyARN,
		PolicySHA256:                  hex.EncodeToString(sum[:]),
		Images:                        rep.Images,
		ReadAt:                        k.FetchedAt.UTC(),
		AdministratorsCanChangePolicy: rep.AdminCanChangePolicy,
	}
	if len(k.History) == 0 {
		return res, nil // an image that keeps no history
	}

	// Every state the enclave saw since it started must pass the same checks:
	// a policy opened for a moment and closed again is still a way in.
	if k.HistoryTruncated {
		return nil, errors.New("changed more times than the enclave remembers since it started; its history is incomplete")
	}
	last := k.History[len(k.History)-1]
	if last.Policy != k.Policy || last.Grants != k.Grants || last.GrantsTruncated != k.GrantsTruncated {
		return nil, errors.New("the history the enclave reports does not end in the current policy")
	}
	for _, past := range k.History[:len(k.History)-1] {
		if _, err := checkPolicy(past, pcr0, alsoAllow); err != nil {
			return nil, fmt.Errorf("from %s to %s: %w", past.FirstSeen.UTC().Format(time.RFC3339), past.LastSeen.UTC().Format(time.RFC3339), err)
		}
	}
	res.WatchedSince, res.Changes, res.Gaps = k.WatchedSince.UTC(), len(k.History)-1, k.Gaps
	return res, nil
}

// checkPolicy applies the checks to one state of the policy and grants.
func checkPolicy(o attestation.KMSObservation, pcr0 string, alsoAllow []string) (kmspolicy.Report, error) {
	rep, err := kmspolicy.Analyze([]byte(o.Policy))
	if err != nil {
		return rep, err
	}
	if len(rep.Problems) > 0 {
		return rep, errors.New(strings.Join(rep.Problems, "; "))
	}
	if o.Grants > 0 || o.GrantsTruncated {
		return rep, fmt.Errorf("the key has %d grant(s); grants give access outside the policy", o.Grants)
	}
	var others []string
	for _, img := range rep.Images {
		if img != strings.ToLower(pcr0) && !slices.Contains(alsoAllow, img) {
			others = append(others, img)
		}
	}
	if len(others) > 0 {
		return rep, fmt.Errorf("also releases data keys to image(s) you did not verify: %s (accept them with -kms-also-allow)", strings.Join(others, ", "))
	}
	return rep, nil
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

// httpClient returns the client and the root CAs it trusts (nil means the
// system roots), which the SMTP check uses too.
func httpClient(caFile string, timeout time.Duration) (*http.Client, *x509.CertPool, error) {
	if caFile == "" {
		return &http.Client{Timeout: timeout}, nil, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // path given by the user on purpose
	if err != nil {
		return nil, nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, nil, fmt.Errorf("%s: no PEM certificates", caFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	return &http.Client{Timeout: timeout, Transport: tr}, pool, nil
}

// minMTASTSMaxAge is the shortest policy lifetime accepted: with less, a sender
// forgets the policy quickly and whoever controls DNS can switch it off.
const minMTASTSMaxAge = 86400

// mailResult is the outcome of checking how mail reaches the enclave.
type mailResult struct {
	MX      string `json:"mx"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
	// Mode, MaxAge and PolicyID describe the MTA-STS policy senders will apply.
	Mode     string `json:"mta_sts_mode,omitempty"`
	MaxAge   int    `json:"mta_sts_max_age,omitempty"`
	PolicyID string `json:"mta_sts_id,omitempty"`
	// SMTPChecked is true when port 25 answered STARTTLS with the attested key.
	SMTPChecked bool `json:"smtp_checked"`
}

// mailDeps are the network operations of the mail check, replaceable in tests.
type mailDeps struct {
	lookupTXT func(ctx context.Context, name string) ([]string, error)
	lookupMX  func(ctx context.Context, name string) ([]*net.MX, error)
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	roots     *x509.CertPool
	policyURL string // default https://mta-sts.<mx>
	smtpAddr  string // default <mx>:25
}

// attestedKey returns the TLS key hash attested on url, or nil when that host failed.
func attestedKey(results []hostResult, url string) []byte {
	for _, r := range results {
		if r.URL == url && r.OK {
			key, _ := hex.DecodeString(r.SPKIHash)
			return key
		}
	}
	return nil
}

// checkMail verifies that a sender is told to require TLS to the enclave and
// gets it: the MTA-STS policy comes from the enclave (attested key spki) and
// says enforce for this MX only, DNS publishes the policy and the MX, and
// port 25 answers STARTTLS with the attested key.
func checkMail(ctx context.Context, client *http.Client, mx string, spki []byte, deps mailDeps, requireSMTP bool) *mailResult {
	r := &mailResult{MX: mx}
	fail := func(format string, args ...any) *mailResult {
		r.Error = fmt.Sprintf(format, args...)
		return r
	}
	if len(spki) == 0 {
		return fail("https://%s was not verified, so there is no attested key to compare with", mx)
	}
	policyURL := deps.policyURL
	if policyURL == "" {
		policyURL = "https://mta-sts." + mx
	}
	policy, err := attestation.FetchMTASTS(ctx, client, policyURL, spki)
	if err != nil {
		return fail("%v", err)
	}
	r.Mode, r.MaxAge = policy.Mode, policy.MaxAge
	if policy.Mode != "enforce" {
		return fail("MTA-STS mode is %q, not enforce: senders may deliver without TLS", policy.Mode)
	}
	if !slices.Equal(policy.MX, []string{mx}) {
		return fail("MTA-STS policy allows MX %v, expected only %s", policy.MX, mx)
	}
	if policy.MaxAge < minMTASTSMaxAge {
		return fail("MTA-STS max_age is %d seconds, less than %d", policy.MaxAge, minMTASTSMaxAge)
	}

	txts, err := deps.lookupTXT(ctx, "_mta-sts."+mx)
	if err != nil {
		return fail("DNS _mta-sts.%s: %v (without this record senders do not apply the policy)", mx, err)
	}
	var ids []string
	for _, txt := range txts {
		if id, ok := mtaSTSRecordID(txt); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return fail("DNS _mta-sts.%s has %d MTA-STS records, expected one", mx, len(ids))
	}
	r.PolicyID = ids[0]

	mxs, err := deps.lookupMX(ctx, mx)
	if err != nil || len(mxs) == 0 {
		return fail("DNS MX of %s: no records (%v)", mx, err)
	}
	for _, m := range mxs {
		if host := strings.ToLower(strings.TrimSuffix(m.Host, ".")); host != mx {
			return fail("DNS MX of %s points to %s, which the policy does not allow", mx, host)
		}
	}

	addr := deps.smtpAddr
	if addr == "" {
		addr = net.JoinHostPort(mx, "25")
	}
	switch err := attestation.VerifySMTP(ctx, deps.dial, addr, mx, deps.roots, spki); {
	case err == nil:
		r.SMTPChecked = true
	case errors.Is(err, attestation.ErrSMTPUnreachable) && !requireSMTP:
		r.Warning = "port 25 could not be reached from this network (many providers block it), so the SMTP certificate was not checked; run from a server, or pass -require-smtp to fail instead"
	default:
		return fail("SMTP %s: %v", addr, err)
	}
	r.OK = true
	return r
}

// mtaSTSRecordID returns the id of an MTA-STS TXT record ("v=STSv1; id=...").
func mtaSTSRecordID(txt string) (string, bool) {
	fields := strings.Split(txt, ";")
	if strings.TrimSpace(fields[0]) != "v=STSv1" {
		return "", false
	}
	for _, f := range fields[1:] {
		if id, ok := strings.CutPrefix(strings.TrimSpace(f), "id="); ok && id != "" {
			return id, true
		}
	}
	return "", false
}

// certResult is the outcome of looking for certificates outside the enclave.
type certResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
	// Account is the only ACME account the CAA records let issue for the hosts.
	Account string `json:"acme_account,omitempty"`
	// KeyCreatedAt is the time from which every certificate must carry the enclave's key.
	KeyCreatedAt time.Time `json:"key_created_at,omitzero"`
	// Own counts valid certificates with the enclave's key.
	Own int `json:"with_attested_key"`
	// Older are valid certificates with other keys, issued before the enclave's key existed.
	Older      []certwatch.Issuance `json:"older_with_other_keys,omitempty"`
	OlderUntil time.Time            `json:"older_valid_until,omitzero"`
	// Foreign are valid certificates with other keys issued since: the failure.
	Foreign []certwatch.Issuance `json:"with_other_keys,omitempty"`
}

// certDeps are the network services of the certificate check.
type certDeps struct {
	client   *http.Client
	resolver string
	ctAPI    string
	now      time.Time
}

// reportedTLS returns what the enclave reported about its TLS key on url, or
// nil when that host failed or reported nothing.
func reportedTLS(results []hostResult, url string) *attestation.TLSStatus {
	for _, r := range results {
		if r.URL == url && r.OK {
			return r.TLS
		}
	}
	return nil
}

func hostsOf(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, strings.TrimPrefix(u, "https://"))
	}
	return out
}

// caIssuer is the CAA name of the CA the enclave uses; method is the only
// validation it can pass, since it answers TLS-ALPN-01 from inside the enclave.
const (
	caIssuer = "letsencrypt.org"
	caMethod = "tls-alpn-01"
)

// checkCertificates looks for ways someone else could hold a certificate for
// the enclave hosts: CAA records that let another account or CA issue, or a
// wildcard for the domain, and certificates in Certificate Transparency with
// another key issued since the enclave's key was created.
func checkCertificates(ctx context.Context, domain string, hosts []string, tls *attestation.TLSStatus, deps certDeps) *certResult {
	r := &certResult{}
	fail := func(format string, args ...any) *certResult {
		r.Error = fmt.Sprintf(format, args...)
		return r
	}
	switch {
	case tls == nil:
		return fail("the enclave did not report its TLS key on https://app.%s (releases before v0.4.0 need -skip-certificates)", domain)
	case !tls.Sealed:
		return fail("the TLS key is not sealed: it changes at every restart, so no certificate history can be checked")
	case tls.ACMEAccount == "":
		return fail("the enclave has not registered an ACME account yet")
	}
	r.Account, r.KeyCreatedAt = tls.ACMEAccount, tls.KeyCreatedAt

	for _, h := range hosts {
		records, err := certwatch.LookupCAA(ctx, deps.client, deps.resolver, h)
		if err != nil {
			return fail("%v", err)
		}
		if err := certwatch.CheckPinned(records, caIssuer, tls.ACMEAccount, caMethod); err != nil {
			return fail("CAA of %s %v", h, err)
		}
	}
	records, err := certwatch.LookupCAA(ctx, deps.client, deps.resolver, domain)
	if err != nil {
		return fail("%v", err)
	}
	if err := certwatch.CheckNoWildcard(records); err != nil {
		return fail("CAA of %s %v (a wildcard would be valid for the enclave hosts)", domain, err)
	}

	issuances, err := certwatch.Issuances(ctx, deps.client, deps.ctAPI, domain)
	if err != nil {
		return fail("%v", err)
	}
	rep := certwatch.Classify(issuances, hosts, tls.SPKISHA256, tls.KeyCreatedAt, deps.now)
	r.Own, r.Older, r.Foreign = len(rep.Own), rep.Older, rep.Foreign
	for _, o := range rep.Older {
		if o.NotAfter.After(r.OlderUntil) {
			r.OlderUntil = o.NotAfter
		}
	}
	if len(rep.Foreign) > 0 {
		var list []string
		for _, f := range rep.Foreign {
			list = append(list, fmt.Sprintf("%s for %s, issued %s, key %.16s", f.Issuer.FriendlyName, strings.Join(f.DNSNames, ","), f.NotBefore.Format("2006-01-02"), f.PubkeySHA256))
		}
		return fail("%d certificate(s) valid for the enclave hosts have a key the enclave does not hold: %s", len(rep.Foreign), strings.Join(list, "; "))
	}
	if r.Own == 0 {
		r.Warning = "Certificate Transparency does not list a certificate with the attested key yet (it can lag, and test CAs do not log)"
	}
	r.OK = true
	return r
}

func printCertificates(w io.Writer, c *certResult) {
	if c == nil {
		return
	}
	if !c.OK {
		fmt.Fprintf(w, "FAIL  certificates outside the enclave\n      %s\n", c.Error)
		return
	}
	fmt.Fprintln(w, "PASS  certificates outside the enclave")
	fmt.Fprintf(w, "      CAA: only %s account %s with %s may issue for the hosts; no wildcards for the domain\n", caIssuer, c.Account, caMethod)
	fmt.Fprintf(w, "      Certificate Transparency: %d valid certificate(s) with the attested key, none with another key since %s\n", c.Own, c.KeyCreatedAt.UTC().Format(time.RFC3339))
	if len(c.Older) > 0 {
		fmt.Fprintf(w, "      note: %d older certificate(s) with other keys, issued before the enclave's key existed, stay valid until %s\n", len(c.Older), c.OlderUntil.UTC().Format("2006-01-02"))
	}
	if c.Warning != "" {
		fmt.Fprintf(w, "      warning: %s\n", c.Warning)
	}
}

func printMail(w io.Writer, m *mailResult) {
	if m == nil {
		return
	}
	if !m.OK {
		fmt.Fprintf(w, "FAIL  mail to %s\n      %s\n", m.MX, m.Error)
		return
	}
	fmt.Fprintf(w, "PASS  mail to %s\n", m.MX)
	fmt.Fprintf(w, "      MTA-STS policy served with the attested key: mode %s, only this MX, max_age %d (DNS record id %s)\n", m.Mode, m.MaxAge, m.PolicyID)
	if m.SMTPChecked {
		fmt.Fprintln(w, "      port 25 answers STARTTLS with the attested TLS key")
	} else {
		fmt.Fprintf(w, "      warning: %s\n", m.Warning)
	}
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
		printSigstore(w, r.Sigstore)
		printAnchors(w, r.Anchors, time.Now())
		printSentMail(w, r.SentMail)
		if r.TLS != nil && r.TLS.Sealed {
			fmt.Fprintf(w, "      TLS key created %s, kept sealed with KMS across restarts\n", r.TLS.KeyCreatedAt.UTC().Format(time.RFC3339))
		}
	}
}

// printRoot describes the key that holds the root secret.
func printRoot(w io.Writer, root *rootResult) {
	if root == nil {
		return
	}
	fmt.Fprintf(w, "      root secret locked by KMS key %s: only this image can open it, and nobody in the account can change that\n", root.KeyARN)
	fmt.Fprintf(w, "      that key's policy sha256 %s gives nobody a way to replace it or to add grants; no grants; key material never left KMS\n", root.PolicySHA256)
	if root.WatchedSince.IsZero() {
		fmt.Fprintf(w, "      that policy was read by the enclave once, at %s; AWS can change it for the account's owner, and this release would not show it\n", root.ReadAt.Format(time.RFC3339))
	} else {
		what := "unchanged"
		if root.Changes > 0 {
			what = fmt.Sprintf("%d other state(s), each open to this image alone and without grants", root.Changes)
		}
		fmt.Fprintf(w, "      that policy read by the enclave every minute since %s: %s\n", root.WatchedSince.Format(time.RFC3339), what)
		for _, g := range root.Gaps {
			fmt.Fprintf(w, "      warning: the enclave could not read the policy from %s to %s; a change made and undone in that time would not be seen\n", g.From.UTC().Format(time.RFC3339), g.To.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(w, "      note: AWS can change that policy for the account's owner; it would show here while this enclave runs, not before it started or after a restart")
	}
	at := root.WrittenAt.Format(time.RFC3339)
	if root.Origin == attestation.RootHandedOver {
		fmt.Fprintf(w, "      root secret handed to this image at %s by the enclave of release %s (image %s, published %s), which this image accepted only with that release's public build provenance\n",
			at, root.From.Commit, root.From.Image, root.From.PublishedAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(w, "      root secret created inside an enclave of this image at %s\n", at)
	}
	if root.KeyState != "Enabled" {
		fmt.Fprintf(w, "      warning: that key is in state %s; once it is gone this image cannot start again\n", root.KeyState)
	}
}

func printKMS(w io.Writer, r hostResult, pcr0 string) {
	printRoot(w, r.Root)
	if r.KMS == nil {
		if r.Root == nil {
			fmt.Fprintf(w, "      KMS key policy: %s\n", r.KMSSkipped)
		}
		return
	}
	if r.Root != nil {
		fmt.Fprintln(w, "      this image also opens data keys that releases before the root secret wrapped with another KMS key, to move them:")
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
	watched := !r.KMS.WatchedSince.IsZero()
	if watched {
		what := "unchanged"
		if r.KMS.Changes > 0 {
			what = fmt.Sprintf("%d other state(s), each limited to images you accepted and without grants", r.KMS.Changes)
		}
		fmt.Fprintf(w, "      KMS key policy read by the enclave every minute since %s: %s\n", r.KMS.WatchedSince.Format(time.RFC3339), what)
		for _, g := range r.KMS.Gaps {
			fmt.Fprintf(w, "      warning: the enclave could not read the policy from %s to %s; a change made and undone in that time would not be seen\n", g.From.UTC().Format(time.RFC3339), g.To.UTC().Format(time.RFC3339))
		}
	}
	switch {
	case r.KMS.AdministratorsCanChangePolicy && watched:
		fmt.Fprintln(w, "      note: account administrators can replace that policy; before the enclave started, or after a restart, nothing here shows it")
	case r.KMS.AdministratorsCanChangePolicy:
		fmt.Fprintln(w, "      note: account administrators can replace that policy; this is its state at that time")
	}
}
