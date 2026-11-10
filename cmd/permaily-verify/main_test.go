package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/smtpd"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/certwatch"
)

const (
	imageA = "e7bcd8dd5d91f78e48a5c686cc775272a59df69b89ec2dc28ccae651d8c0852e8af20ba9ac74632fbaef967bbe3b7f6a"
	imageB = "31e90c2d949962bffe22d3bca58a020e0b100e1fa51a3fc654eceae7233e76c0b8038c5822c3dca9f9971d5db67bbde7"
	admin  = `{"Sid":"KeyAdministration","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":["kms:Describe*","kms:Get*","kms:List*","kms:PutKeyPolicy"],"Resource":"*"}`
)

func keyPolicy(images ...string) string {
	stmts := []string{admin}
	if len(images) > 0 {
		stmts = append(stmts, `{"Sid":"EnclaveDataKeys","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/host"},"Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"*","Condition":{"StringEqualsIgnoreCase":{"kms:RecipientAttestation:ImageSha384":["`+strings.Join(images, `","`)+`"]}}}`)
	}
	return `{"Version":"2012-10-17","Statement":[` + strings.Join(stmts, ",") + `]}`
}

func TestCheckKMS(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	status := func(policy string) *attestation.KMSStatus {
		return &attestation.KMSStatus{KeyARN: "arn:aws:kms:us-east-2:111122223333:key/k", Policy: policy, FetchedAt: now.Add(-time.Minute)}
	}

	t.Run("only the verified image", func(t *testing.T) {
		got, err := checkKMS(status(keyPolicy(imageA)), strings.ToUpper(imageA), nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Images, []string{imageA}) || !got.AdministratorsCanChangePolicy || len(got.PolicySHA256) != 64 || got.KeyARN == "" {
			t.Fatalf("result %+v", got)
		}
	})
	t.Run("no image at all", func(t *testing.T) {
		if got, err := checkKMS(status(keyPolicy()), imageA, nil, now); err != nil || len(got.Images) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("previous release accepted during an upgrade", func(t *testing.T) {
		if _, err := checkKMS(status(keyPolicy(imageA, imageB)), imageA, splitList(" "+strings.ToUpper(imageB)+" ,"), now); err != nil {
			t.Fatal(err)
		}
	})

	fails := []struct {
		name string
		k    *attestation.KMSStatus
		want string
	}{
		{"another image can read the mail", status(keyPolicy(imageA, imageB)), imageB},
		{"only another image", status(keyPolicy(imageB)), "did not verify"},
		{"a statement without the image check", status(`{"Statement":[` + admin + `,{"Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*"}]}`), "without requiring an enclave image"},
		{"a grant exists", func() *attestation.KMSStatus { k := status(keyPolicy(imageA)); k.Grants = 1; return k }(), "1 grant"},
		{"more grants than listed", func() *attestation.KMSStatus { k := status(keyPolicy(imageA)); k.GrantsTruncated = true; return k }(), "grant"},
		{"old reading", func() *attestation.KMSStatus {
			k := status(keyPolicy(imageA))
			k.FetchedAt = now.Add(-time.Hour)
			return k
		}(), "not within"},
		{"reading from the future", func() *attestation.KMSStatus {
			k := status(keyPolicy(imageA))
			k.FetchedAt = now.Add(time.Hour)
			return k
		}(), "not within"},
		{"policy is not JSON", status("nope"), "malformed"},
		{"no policy", status(""), "malformed"},
	}
	for _, c := range fails {
		t.Run(c.name, func(t *testing.T) {
			got, err := checkKMS(c.k, imageA, nil, now)
			if err == nil || got != nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %+v, %v; want an error containing %q", got, err, c.want)
			}
		})
	}
}

func TestPrintKMS(t *testing.T) {
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		r    hostResult
		want []string
		not  string
	}{
		{"skipped", hostResult{KMSSkipped: "not checked (-skip-kms-policy)"}, []string{"KMS key policy: not checked"}, "releases"},
		{"only this image", hostResult{KMS: &kmsResult{KeyARN: "arn:k", Images: []string{imageA}, PolicySHA256: "ab", ReadAt: at, AdministratorsCanChangePolicy: true}},
			[]string{"only to this image", "no grants", "sha256 ab", "2026-11-10T04:00:00Z", "administrators can replace"}, ""},
		{"locked policy", hostResult{KMS: &kmsResult{Images: []string{imageA}, ReadAt: at}}, []string{"only to this image"}, "administrators"},
		{"no image", hostResult{KMS: &kmsResult{ReadAt: at}}, []string{"to no image"}, "only to this image"},
		{"this and an accepted one", hostResult{KMS: &kmsResult{Images: []string{imageB, imageA}, ReadAt: at}}, []string{"this image and 1 more you accepted"}, "only to"},
		{"only an accepted one", hostResult{KMS: &kmsResult{Images: []string{imageB}, ReadAt: at}}, []string{"not to this one"}, "only to"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			printKMS(&b, c.r, imageA)
			for _, w := range c.want {
				if !strings.Contains(b.String(), w) {
					t.Fatalf("output %q lacks %q", b.String(), w)
				}
			}
			if c.not != "" && strings.Contains(b.String(), c.not) {
				t.Fatalf("output %q contains %q", b.String(), c.not)
			}
		})
	}
}

type discard struct{}

func (discard) Accepts(string) bool { return false }
func (discard) Ingest(context.Context, string, []string, []byte) (string, error) {
	return "", errors.New("the check must not send mail")
}

// mailFixture is an enclave-like deployment: one key for the HTTPS policy host
// and the MX, as in the image.
type mailFixture struct {
	client  *http.Client
	key     []byte
	deps    mailDeps
	policy  string
	records []string
	mxHosts []string
}

func newMailFixture(t *testing.T) *mailFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"inbox.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	f := &mailFixture{
		policy:  "version: STSv1\nmode: enforce\nmx: inbox.test\nmax_age: 604800\n",
		records: []string{"v=spf1 -all", "v=STSv1; id=20261110"},
		mxHosts: []string{"Inbox.Test."},
		key:     attestation.SPKIHash(leaf),
	}
	web := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(f.policy)) }))
	web.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	web.StartTLS()
	t.Cleanup(web.Close)
	f.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	mx := smtpd.New(discard{}, smtpd.Config{Domain: "inbox.test", TLS: &tls.Config{Certificates: []tls.Certificate{cert}}}, slog.New(slog.DiscardHandler))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mx.Serve(ln) }()
	t.Cleanup(func() { _ = mx.Close() })

	f.deps = mailDeps{
		roots:     pool,
		policyURL: web.URL,
		smtpAddr:  ln.Addr().String(),
		lookupTXT: func(_ context.Context, name string) ([]string, error) {
			if name != "_mta-sts.inbox.test" {
				t.Errorf("TXT lookup for %q", name)
			}
			if f.records == nil {
				return nil, errors.New("no such host")
			}
			return f.records, nil
		},
		lookupMX: func(context.Context, string) ([]*net.MX, error) {
			var out []*net.MX
			for _, h := range f.mxHosts {
				out = append(out, &net.MX{Host: h, Pref: 10})
			}
			return out, nil
		},
	}
	return f
}

func (f *mailFixture) check(requireSMTP bool) *mailResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return checkMail(ctx, f.client, "inbox.test", f.key, f.deps, requireSMTP)
}

func TestCheckMail(t *testing.T) {
	t.Run("senders are told to require TLS and get the attested key", func(t *testing.T) {
		r := newMailFixture(t).check(true)
		if !r.OK || !r.SMTPChecked || r.Mode != "enforce" || r.PolicyID != "20261110" || r.MaxAge != 604800 || r.Warning != "" {
			t.Fatalf("result %+v", r)
		}
	})

	closedPort := func(t *testing.T) string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		return ln.Addr().String()
	}
	t.Run("port 25 blocked on the verifier's network", func(t *testing.T) {
		f := newMailFixture(t)
		f.deps.smtpAddr = closedPort(t)
		if r := f.check(false); !r.OK || r.SMTPChecked || !strings.Contains(r.Warning, "port 25") {
			t.Fatalf("result %+v", r)
		}
		if r := f.check(true); r.OK || !strings.Contains(r.Error, "unreachable") {
			t.Fatalf("with -require-smtp: %+v", r)
		}
	})

	fails := []struct {
		name    string
		breakIt func(t *testing.T, f *mailFixture)
		want    string
	}{
		{"inbox host not verified", func(_ *testing.T, f *mailFixture) { f.key = nil }, "was not verified"},
		{"policy from a server without the attested key", func(_ *testing.T, f *mailFixture) { f.key = make([]byte, 32) }, "attested TLS key"},
		{"mode testing", func(_ *testing.T, f *mailFixture) { f.policy = strings.Replace(f.policy, "enforce", "testing", 1) }, "may deliver without TLS"},
		{"another MX allowed", func(_ *testing.T, f *mailFixture) { f.policy += "mx: mail.attacker.test\n" }, "expected only"},
		{"short max_age", func(_ *testing.T, f *mailFixture) { f.policy = strings.Replace(f.policy, "604800", "60", 1) }, "max_age"},
		{"not a policy", func(_ *testing.T, f *mailFixture) { f.policy = "<html>" }, "malformed"},
		{"no DNS record", func(_ *testing.T, f *mailFixture) { f.records = nil }, "senders do not apply the policy"},
		{"DNS record without id", func(_ *testing.T, f *mailFixture) { f.records = []string{"v=STSv1"} }, "0 MTA-STS records"},
		{"two DNS records", func(_ *testing.T, f *mailFixture) { f.records = []string{"v=STSv1; id=1", "v=STSv1; id=2;"} }, "2 MTA-STS records"},
		{"no MX", func(_ *testing.T, f *mailFixture) { f.mxHosts = nil }, "no records"},
		{"MX elsewhere", func(_ *testing.T, f *mailFixture) { f.mxHosts = []string{"inbox.test.", "mail.attacker.test."} }, "mail.attacker.test"},
		{"MX answers with another key", func(t *testing.T, f *mailFixture) { f.deps.smtpAddr = newMailFixture(t).deps.smtpAddr }, "SMTP"},
	}
	for _, c := range fails {
		t.Run(c.name, func(t *testing.T) {
			f := newMailFixture(t)
			c.breakIt(t, f)
			if r := f.check(true); r.OK || !strings.Contains(r.Error, c.want) {
				t.Fatalf("result %+v, want an error containing %q", r, c.want)
			}
		})
	}
}

func TestPrintMail(t *testing.T) {
	out := func(m *mailResult) string {
		var b bytes.Buffer
		printMail(&b, m)
		return b.String()
	}
	if out(nil) != "" {
		t.Fatal("output for a skipped check")
	}
	if s := out(&mailResult{MX: "inbox.test", Error: "boom"}); !strings.HasPrefix(s, "FAIL  mail to inbox.test") || !strings.Contains(s, "boom") {
		t.Fatal(s)
	}
	if s := out(&mailResult{MX: "inbox.test", OK: true, Mode: "enforce", MaxAge: 604800, PolicyID: "7", SMTPChecked: true}); !strings.HasPrefix(s, "PASS") || !strings.Contains(s, "port 25 answers STARTTLS with the attested TLS key") {
		t.Fatal(s)
	}
	if s := out(&mailResult{MX: "inbox.test", OK: true, Warning: "port 25 blocked"}); !strings.Contains(s, "warning: port 25 blocked") {
		t.Fatal(s)
	}
}

func TestAttestedKey(t *testing.T) {
	results := []hostResult{{URL: "https://a", OK: true, SPKIHash: "0102"}, {URL: "https://b", SPKIHash: "0304"}}
	if got := attestedKey(results, "https://a"); !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("got %x", got)
	}
	if attestedKey(results, "https://b") != nil || attestedKey(results, "https://c") != nil {
		t.Fatal("a failed or missing host gave a key")
	}
}

// certFixture serves CAA records and Certificate Transparency like the real
// services, for a deployment whose records and certificates are all in order.
type certFixture struct {
	caa       map[string][]string
	issuances []certwatch.Issuance
	tls       *attestation.TLSStatus
	deps      certDeps
	hosts     []string
	ctDown    bool
}

const acmeAccount = "https://acme-v02.api.letsencrypt.org/acme/acct/123"

func newCertFixture(t *testing.T) *certFixture {
	t.Helper()
	created := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	now := created.Add(20 * 24 * time.Hour)
	pinned := `0 issue "letsencrypt.org; accounturi=` + acmeAccount + `; validationmethods=tls-alpn-01"`
	issued := func(id, key string, notBefore time.Time, names ...string) certwatch.Issuance {
		i := certwatch.Issuance{ID: id, DNSNames: names, PubkeySHA256: key, NotBefore: notBefore, NotAfter: notBefore.Add(90 * 24 * time.Hour)}
		i.Issuer.FriendlyName = "Let's Encrypt"
		return i
	}
	f := &certFixture{
		hosts: []string{"app.permaily.test", "inbox.permaily.test"},
		caa: map[string][]string{
			"app.permaily.test":   {pinned},
			"inbox.permaily.test": {pinned},
			"permaily.test":       {`0 issue "amazon.com"`, `0 issuewild ";"`},
		},
		issuances: []certwatch.Issuance{
			issued("1", "aa11", created.Add(-48*time.Hour), "*.permaily.test", "permaily.test"),
			issued("2", "bb22", created.Add(-24*time.Hour), "app.permaily.test", "inbox.permaily.test"),
			issued("3", "cafe", created.Add(time.Hour), "app.permaily.test", "inbox.permaily.test"),
			issued("4", "dd44", created.Add(time.Hour), "permaily.test", "www.permaily.test"),
		},
		tls: &attestation.TLSStatus{SPKISHA256: "cafe", KeyCreatedAt: created, Sealed: true, ACMEAccount: acmeAccount},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns":
			records, ok := f.caa[r.URL.Query().Get("name")]
			if !ok {
				http.Error(w, "resolver down", http.StatusBadGateway)
				return
			}
			type answer struct {
				Type int    `json:"type"`
				Data string `json:"data"`
			}
			out := struct {
				Status int
				Answer []answer
			}{}
			for _, rec := range records {
				out.Answer = append(out.Answer, answer{257, rec})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/ct":
			if f.ctDown {
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			if r.URL.Query().Get("after") != "" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode(f.issuances)
		}
	}))
	t.Cleanup(srv.Close)
	f.deps = certDeps{client: srv.Client(), resolver: srv.URL + "/dns", ctAPI: srv.URL + "/ct", now: now}
	return f
}

func (f *certFixture) check() *certResult {
	return checkCertificates(context.Background(), "permaily.test", f.hosts, f.tls, f.deps)
}

func TestCheckCertificates(t *testing.T) {
	t.Run("only the enclave can get and holds certificates for its hosts", func(t *testing.T) {
		r := newCertFixture(t).check()
		if !r.OK || r.Own != 1 || len(r.Older) != 2 || len(r.Foreign) != 0 || r.Account != acmeAccount || r.Warning != "" {
			t.Fatalf("result %+v", r)
		}
		if want := time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC).Add(90 * 24 * time.Hour); !r.OlderUntil.Equal(want) {
			t.Fatalf("older certificates valid until %s, want %s", r.OlderUntil, want)
		}
	})
	t.Run("the enclave's certificate is not in the logs yet", func(t *testing.T) {
		f := newCertFixture(t)
		f.issuances = f.issuances[:2]
		if r := f.check(); !r.OK || r.Own != 0 || !strings.Contains(r.Warning, "does not list") {
			t.Fatalf("result %+v", r)
		}
	})

	after := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	rogue := func(names ...string) certwatch.Issuance {
		i := certwatch.Issuance{ID: "9", DNSNames: names, PubkeySHA256: "0bad0bad0bad0bad0bad", NotBefore: after, NotAfter: after.Add(90 * 24 * time.Hour)}
		i.Issuer.FriendlyName = "Some CA"
		return i
	}
	fails := []struct {
		name    string
		breakIt func(f *certFixture)
		want    string
	}{
		{"image does not report its TLS key", func(f *certFixture) { f.tls = nil }, "did not report its TLS key"},
		{"key only in memory", func(f *certFixture) { f.tls.Sealed = false }, "not sealed"},
		{"no ACME account yet", func(f *certFixture) { f.tls.ACMEAccount = "" }, "ACME account"},
		{"any Let's Encrypt account may issue", func(f *certFixture) { f.caa["inbox.permaily.test"] = []string{`0 issue "letsencrypt.org"`} }, "CAA of inbox.permaily.test"},
		{"another CA may issue", func(f *certFixture) {
			f.caa["app.permaily.test"] = append(f.caa["app.permaily.test"], `0 issue "amazon.com"`)
		}, "amazon.com"},
		{"host without CAA records", func(f *certFixture) { f.caa["app.permaily.test"] = nil }, "no issue record"},
		{"wildcards allowed for the domain", func(f *certFixture) { f.caa["permaily.test"] = []string{`0 issue "amazon.com"`} }, "wildcard"},
		{"a CA may issue wildcards", func(f *certFixture) { f.caa["permaily.test"] = []string{`0 issuewild "amazon.com"`} }, "wildcard"},
		{"resolver fails for a host", func(f *certFixture) { delete(f.caa, "app.permaily.test") }, "CAA of app.permaily.test"},
		{"resolver fails for the domain", func(f *certFixture) { delete(f.caa, "permaily.test") }, "CAA of permaily.test"},
		{"transparency service fails", func(f *certFixture) { f.ctDown = true }, "certificate transparency"},
		{"new certificate with another key", func(f *certFixture) { f.issuances = append(f.issuances, rogue("app.permaily.test")) }, "Some CA for app.permaily.test, issued 2026-11-15, key 0bad0bad0bad0bad"},
		{"new wildcard", func(f *certFixture) { f.issuances = append(f.issuances, rogue("*.permaily.test")) }, "*.permaily.test"},
	}
	for _, c := range fails {
		t.Run(c.name, func(t *testing.T) {
			f := newCertFixture(t)
			c.breakIt(f)
			if r := f.check(); r.OK || !strings.Contains(r.Error, c.want) {
				t.Fatalf("result %+v, want an error containing %q", r, c.want)
			}
		})
	}
}

func TestPrintCertificates(t *testing.T) {
	out := func(c *certResult) string {
		var b bytes.Buffer
		printCertificates(&b, c)
		return b.String()
	}
	if out(nil) != "" {
		t.Fatal("output for a skipped check")
	}
	if s := out(&certResult{Error: "boom"}); !strings.HasPrefix(s, "FAIL  certificates outside the enclave") || !strings.Contains(s, "boom") {
		t.Fatal(s)
	}
	created := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	s := out(&certResult{OK: true, Account: acmeAccount, KeyCreatedAt: created, Own: 2, Older: make([]certwatch.Issuance, 4), OlderUntil: created.Add(24 * time.Hour), Warning: "lagging"})
	for _, want := range []string{"PASS", acmeAccount, "tls-alpn-01", "2 valid certificate(s) with the attested key", "since 2026-11-10T06:00:00Z", "4 older certificate(s)", "until 2026-11-11", "warning: lagging"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output %q lacks %q", s, want)
		}
	}
	if s := out(&certResult{OK: true}); strings.Contains(s, "note:") || strings.Contains(s, "warning:") {
		t.Fatal(s)
	}
}

func TestReportedTLS(t *testing.T) {
	tls := &attestation.TLSStatus{SPKISHA256: "ab"}
	results := []hostResult{{URL: "https://app.x", OK: true, TLS: tls}, {URL: "https://api.x", TLS: tls}}
	if reportedTLS(results, "https://app.x") != tls || reportedTLS(results, "https://api.x") != nil || reportedTLS(results, "https://mcp.x") != nil {
		t.Fatal("only a verified host's report counts")
	}
	if got := hostsOf([]string{"https://app.x", "https://inbox.x"}); !slices.Equal(got, []string{"app.x", "inbox.x"}) {
		t.Fatalf("hosts %v", got)
	}
}

func TestCheckKMSHistory(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	backdoor := `{"Statement":[` + admin + `,{"Sid":"Backdoor","Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*"}]}`
	// status builds what an enclave reports: the states it saw, oldest first;
	// the last one is the current policy.
	status := func(states ...attestation.KMSObservation) *attestation.KMSStatus {
		for i := range states {
			states[i].FirstSeen, states[i].LastSeen = at(5, i*10), at(5, i*10+9)
		}
		last := states[len(states)-1]
		return &attestation.KMSStatus{
			KeyARN: "arn:k", Policy: last.Policy, Grants: last.Grants, GrantsTruncated: last.GrantsTruncated,
			FetchedAt: now.Add(-time.Minute), WatchedSince: at(5, 0), History: states,
		}
	}
	state := func(policy string) attestation.KMSObservation { return attestation.KMSObservation{Policy: policy} }

	t.Run("unchanged since the enclave started", func(t *testing.T) {
		got, err := checkKMS(status(state(keyPolicy(imageA))), imageA, nil, now)
		if err != nil || got.Changes != 0 || !got.WatchedSince.Equal(at(5, 0)) || len(got.Gaps) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("changed between states that all pass", func(t *testing.T) {
		k := status(state(keyPolicy(imageA)), state(keyPolicy()), state(keyPolicy(imageA)))
		k.Gaps = []attestation.KMSGap{{From: at(6, 0), To: at(6, 5)}}
		got, err := checkKMS(k, imageA, nil, now)
		if err != nil || got.Changes != 2 || len(got.Gaps) != 1 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("an upgrade seen by the new enclave, with the old release accepted", func(t *testing.T) {
		k := status(state(keyPolicy(imageB, imageA)), state(keyPolicy(imageA)))
		if _, err := checkKMS(k, imageA, []string{imageB}, now); err != nil {
			t.Fatal(err)
		}
	})

	// The current policy is clean in every one of these; only the history gives it away.
	fails := []struct {
		name string
		k    *attestation.KMSStatus
		want []string
	}{
		{"opened to another image for a while", status(state(keyPolicy(imageA)), state(keyPolicy(imageA, imageB)), state(keyPolicy(imageA))),
			[]string{"from 2026-11-10T05:10:00Z to 2026-11-10T05:19:00Z", imageB}},
		{"a statement without the image check for a while", status(state(keyPolicy(imageA)), state(backdoor), state(keyPolicy(imageA))),
			[]string{"from 2026-11-10T05:10:00Z", "Backdoor", "without requiring an enclave image"}},
		{"a grant that was later revoked", status(state(keyPolicy(imageA)), attestation.KMSObservation{Policy: keyPolicy(imageA), Grants: 1}, state(keyPolicy(imageA))),
			[]string{"from 2026-11-10T05:10:00Z", "1 grant"}},
		{"a past policy that is not a policy", status(state("nope"), state(keyPolicy(imageA))), []string{"from 2026-11-10T05:00:00Z", "malformed"}},
		{"more changes than the enclave remembers", func() *attestation.KMSStatus {
			k := status(state(keyPolicy(imageA)))
			k.HistoryTruncated = true
			return k
		}(), []string{"history is incomplete"}},
		{"a history that does not end in the current policy", func() *attestation.KMSStatus {
			k := status(state(keyPolicy(imageA)), state(keyPolicy()))
			k.Policy = keyPolicy(imageA)
			return k
		}(), []string{"does not end in the current policy"}},
	}
	for _, c := range fails {
		t.Run(c.name, func(t *testing.T) {
			got, err := checkKMS(c.k, imageA, nil, now)
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v; want an error", got, err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestPrintKMSHistory(t *testing.T) {
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	out := func(k *kmsResult) string {
		var b bytes.Buffer
		printKMS(&b, hostResult{KMS: k}, imageA)
		return b.String()
	}
	s := out(&kmsResult{Images: []string{imageA}, ReadAt: at, WatchedSince: at.Add(-time.Hour), AdministratorsCanChangePolicy: true})
	for _, want := range []string{"every minute since 2026-11-10T03:00:00Z: unchanged", "before the enclave started, or after a restart"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output %q lacks %q", s, want)
		}
	}
	if strings.Contains(s, "warning") || strings.Contains(s, "state at that time") {
		t.Fatal(s)
	}
	s = out(&kmsResult{Images: []string{imageA}, ReadAt: at, WatchedSince: at.Add(-time.Hour), Changes: 2,
		Gaps: []attestation.KMSGap{{From: at.Add(-30 * time.Minute), To: at.Add(-25 * time.Minute)}}})
	for _, want := range []string{"2 other state(s)", "warning: the enclave could not read the policy from 2026-11-10T03:30:00Z to 2026-11-10T03:35:00Z"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output %q lacks %q", s, want)
		}
	}
	if strings.Contains(s, "note:") {
		t.Fatal("a locked policy needs no note about administrators: " + s)
	}
}
