package attestation

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Errors of the mail transport checks.
var (
	// ErrSMTPUnreachable means the MX could not be reached at all. Many networks
	// block outbound port 25, so this says nothing about the MX itself.
	ErrSMTPUnreachable = errors.New("attestation: SMTP server unreachable")
	// ErrNoSTARTTLS means the MX does not offer TLS, so mail would travel in clear text.
	ErrNoSTARTTLS = errors.New("attestation: SMTP server does not offer STARTTLS")
)

// MTASTSPolicy is an MTA-STS policy (RFC 8461): it tells senders whether they
// must refuse to deliver mail to an MX that does not present a valid certificate.
type MTASTSPolicy struct {
	Mode   string   // "enforce", "testing" or "none"
	MX     []string // host patterns allowed as MX
	MaxAge int      // seconds a sender may cache the policy
}

// ParseMTASTS parses the text of /.well-known/mta-sts.txt.
func ParseMTASTS(text string) (*MTASTSPolicy, error) {
	var p MTASTSPolicy
	version := ""
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "version":
			version = value
		case "mode":
			p.Mode = value
		case "mx":
			p.MX = append(p.MX, strings.ToLower(value))
		case "max_age":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%w: MTA-STS max_age %q", ErrMalformed, value)
			}
			p.MaxAge = n
		}
	}
	if version != "STSv1" || p.Mode == "" {
		return nil, fmt.Errorf("%w: not an MTA-STS policy", ErrMalformed)
	}
	return &p, nil
}

// FetchMTASTS gets the MTA-STS policy from baseURL and accepts it only when it
// is served with the attested TLS key (spkiHash), like FetchStatus.
func FetchMTASTS(ctx context.Context, client *http.Client, baseURL string, spkiHash []byte) (*MTASTSPolicy, error) {
	body, err := fetchBound(ctx, client, strings.TrimRight(baseURL, "/")+"/.well-known/mta-sts.txt", spkiHash)
	if err != nil {
		return nil, fmt.Errorf("fetch MTA-STS policy: %w", err)
	}
	return ParseMTASTS(string(body))
}

// VerifySMTP connects to the MX at addr, upgrades with STARTTLS as a sending
// server would (certificate valid for serverName under roots; nil means the
// system roots) and checks that the certificate has the attested key
// (spkiHash). It sends no mail. dial opens the TCP connection; nil uses the
// default dialer.
func VerifySMTP(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), addr, serverName string, roots *x509.CertPool, spkiHash []byte) error {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSMTPUnreachable, err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, serverName)
	if err != nil {
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello("permaily-verify.invalid"); err != nil {
		return fmt.Errorf("SMTP EHLO: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return ErrNoSTARTTLS
	}
	if err := c.StartTLS(&tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("SMTP STARTTLS: %w", err)
	}
	state, ok := c.TLSConnectionState()
	if !ok || len(state.PeerCertificates) == 0 {
		return ErrNotTLS
	}
	if !bytes.Equal(SPKIHash(state.PeerCertificates[0]), spkiHash) {
		return ErrNotBound
	}
	_ = c.Quit()
	return nil
}
