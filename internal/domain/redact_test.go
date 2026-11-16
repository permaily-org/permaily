package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// PR #91's review, finding 2: one redactor knows every identifier the
// deployment makes and addresses, and logs a transport error as its category.
func TestRedactKnowsEveryIdentifierAndAddresses(t *testing.T) {
	secrets := []string{
		"acc_abcdefghijklmnopqrstuvwxyz", "ses_abcdefghijklmnopqrstuvwxyz", "att_abcdefghijklmnopqrstuvwxyz",
		"cer_abcdefghijklmnopqrstuvwxyz", "inv_abcdefghijklmnopqrstuvwxyz", "mbx_abcdefghijklmnopqrstuvwxyz",
		"mbx_00000000000000000000000001", "0123456789abcdef0123456789abcdef", strings.Repeat("ab", 32),
		"ana@example.org", "someone.else+tag@inbox.permaily.test",
	}
	text := "store: put mailbox-" + strings.Join(secrets, " x/") + ": down"
	got := Redact(text)
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Errorf("%q kept: %s", s, got)
		}
	}
	if !strings.HasPrefix(got, "store: put mailbox-acc_… x/ses_…") || !strings.Contains(got, "…@…") || !strings.HasSuffix(got, ": down") {
		t.Fatalf("%s", got)
	}
	if Redact("version 12 of 3 at 2026-11-12") != "version 12 of 3 at 2026-11-12" {
		t.Fatal("what names nobody changed")
	}
}

func TestRedactedErrors(t *testing.T) {
	if Redacted(nil) != "" {
		t.Fatal("nil")
	}
	transport := fmt.Errorf("mailboxes: %w", &url.Error{Op: "Put", URL: "https://b.s3.amazonaws.com/commits/mailbox-mbx_abcdefghijklmnopqrstuvwxyz/0123456789abcdef0123456789abcdef", Err: errors.New("reset")})
	if got := Redacted(transport); got != "transport error (Put)" {
		t.Fatalf("%s", got)
	}
	if got := Redacted(errors.New("commits of account-acc_abcdefghijklmnopqrstuvwxyz: down")); got != "commits of account-acc_…: down" {
		t.Fatalf("%s", got)
	}
}

// PR #131's review, seam gap 2: the identifiers and secrets of connections
// (ADR 0030, "Codes and tokens"), whole base64url values included, and
// every URL are taken out of a text before it is logged, wrapped or not.
func TestRedactKnowsConnectionsIdentifiersAndURLs(t *testing.T) {
	b64 := "Ab-_9zQ" + strings.Repeat("xY_-", 9) // 43 characters of base64url: upper case, '_' and '-'
	secrets := []string{
		"pma_" + b64, "pmr_" + b64, "pmc_" + b64, "pmq_" + b64, "pmp_" + b64, "pmk_" + b64,
		"con_abcdefghijklmnopqrstuvwxyz", "fam_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "rcp_01JD0000000000000000000000", "qry_" + b64,
		"https://trip-app.example/oauth/client.json", "http://localhost:58047/callback?code=pmc_x&state=s",
		"https://private-marker.invalid/c#frag", "urn:ietf:params:oauth:request_uri:pmp_" + b64,
	}
	text := "connections: record connection-" + strings.Join(secrets, " oauth-family-") + ": changed"
	for _, err := range []error{errors.New(text), fmt.Errorf("refresh: %w", errors.New(text))} {
		got := Redacted(err)
		for _, s := range secrets {
			if strings.Contains(got, s) {
				t.Errorf("%q kept: %s", s, got)
			}
		}
		for _, frag := range []string{b64, "abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "trip-app", "localhost", "private-marker", "callback"} {
			if strings.Contains(got, frag) {
				t.Errorf("%q kept: %s", frag, got)
			}
		}
		if !strings.Contains(got, "connection-pma_…") || !strings.Contains(got, "con_…") || !strings.Contains(got, "https://…") || !strings.HasSuffix(got, ": changed") {
			t.Errorf("%s", got)
		}
	}
}

// fixedKind stands for an error of connections that is a fixed kind
// (contracts.ClientError, connections.OAuthError): its text is the kind.
type fixedKind string

func (k fixedKind) Error() string   { return string(k) + " (whatever the error says)" }
func (k fixedKind) LogKind() string { return string(k) }

// PR #131's review, seam gap 2: a value no pattern recognises (a PKCE
// verifier, a state, a client assertion, a jti) is never logged, because
// an error that may carry one is a fixed kind, and what wraps a fixed kind
// is logged as that kind alone, whatever the wrapping text says.
func TestAFixedKindIsLoggedAsItsKindAlone(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk" // RFC 7636's example, no prefix
	assertion := "eyJhbGciOiJSUzI1NiJ9.eyJqdGkiOiJ1bmlxdWUifQ.c2lnbmF0dXJl"
	err := fmt.Errorf("token for state %s, verifier %s, assertion %s, jti unique-0001: %w", "xyzzy-state", verifier, assertion, fixedKind("invalid_grant"))
	if got := Redacted(err); got != "invalid_grant" {
		t.Fatalf("%q", got)
	}
	// Wrapped in a transport error, the fixed kind still decides.
	wrapped := &url.Error{Op: "Post", URL: "https://private-marker.invalid/token", Err: fixedKind("invalid_client")}
	if got := Redacted(fmt.Errorf("x %s: %w", verifier, wrapped)); got != "invalid_client" {
		t.Fatalf("%q", got)
	}
}
