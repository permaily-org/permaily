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
