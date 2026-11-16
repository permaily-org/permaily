package domain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The secrets' and identifiers' prefixes of connections (ADR 0030, "Codes
// and tokens") and the older identifiers' (accounts, sessions, sign-ins,
// ceremonies, invitations, mailboxes).
var (
	connectionPrefixes = []string{"pma_", "pmr_", "pmc_", "pmq_", "pmp_", "pmk_", "con_", "fam_", "rcp_", "qry_"}
	olderPrefixes      = []string{"acc_", "ses_", "att_", "cer_", "inv_", "mbx_"}
)

// PR #137's review, finding 1: a base64url secret may hold "-acc_a" (or any
// older prefix) after a hyphen; the older identifier's replacement must not
// split the secret before the connection's replacement takes it whole.
// Exactly 43 characters, a canonical 32-byte secret, as the reviewer
// reproduced it.
func TestReview137LiteralTokenIsRemovedWhole(t *testing.T) {
	tail := "Zx_-QyZx_-QyZx_-QyZx_-QyZx_-QyZx_-Q"
	value := "Ab-acc_a" + tail
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if len(value) != 43 || err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != value {
		t.Fatal("not a canonical 32-byte secret", err)
	}
	for _, prefix := range []string{"pma_", "pmr_", "pmc_", "pmq_", "pmp_", "pmk_"} {
		t.Run(prefix, func(t *testing.T) {
			got := Redacted(fmt.Errorf("storage: %w", errors.New("record "+prefix+value)))
			if strings.Contains(got, tail) {
				t.Fatalf("base64url suffix survived: %q", got)
			}
		})
	}
}

// PR #137's review, finding 1, on Redact itself: every prefix of
// connections, with every older prefix after a hyphen inside its value, in
// upper and lower case and with '_' and '-' in what follows, is replaced
// whole, whatever the order of the patterns.
func TestReview137RedactConsumesWholeTokens(t *testing.T) {
	for _, prefix := range connectionPrefixes {
		for _, older := range olderPrefixes {
			for _, tail := range []string{"a0Zx_-Qy", "ZX-_QY9", "zz_x-y_z", "Q-_"} {
				value := prefix + "Ab-" + older + tail
				got := Redact("record " + value + " gone")
				if want := "record " + prefix + "… gone"; got != want {
					t.Errorf("%s: %q, want %q", value, got, want)
				}
			}
		}
	}
}

// PR #137's review, finding 2: what is logged of an error is a fixed kind,
// never its text filtered by patterns, so a secret or a URL written in any
// encoding the patterns do not know (percent, JSON, base64), with no scheme,
// split across wrappers or formatted around a fixed kind with %v, never
// reaches the log.
func TestReview137EncodedSecretsNeverReachTheLog(t *testing.T) {
	value := "PrivateMarkAbZ_x-" + strings.Repeat("Q", 26)
	token := "pma_" + value
	jsonToken, _ := json.Marshal(map[string]string{"token": token})
	jsonEscaped := strings.ReplaceAll(string(jsonToken), "_", `\u005f`)
	b64 := base64.StdEncoding.EncodeToString([]byte(token))
	b64url := base64.RawURLEncoding.EncodeToString([]byte("https://private-marker.invalid/c?state=" + value))
	inputs := map[string]string{
		"percent_prefix": "pma%5F" + value,
		"percent_suffix": "pma_Ab%2DPrivateMarkAbZ_x-",
		"percent_token":  url.QueryEscape(token),
		"json_prefix":    `pma\u005f` + value,
		"json_suffix":    `pma_Ab\u002dPrivateMarkAbZ_x-`,
		"json_object":    jsonEscaped,
		"percent_url":    "https%3A%2F%2Fprivate-marker.invalid%2Fcallback%3Fstate%3DPrivateMark",
		"json_url":       `https:\/\/private-marker.invalid/callback?state=PrivateMark`,
		"scheme_less":    "//private-marker.invalid/c?state=PrivateMark",
		"bare_host":      "private-marker.invalid/c?state=PrivateMark",
		"base64_token":   b64,
		"base64url_url":  b64url,
		"upper_prefix":   "PMA_" + value,
	}
	for name, input := range inputs {
		for form, err := range map[string]error{
			"plain":   errors.New(input),
			"wrapped": fmt.Errorf("storage: record %s: %w", input, errors.New("down")),
			"joined":  errors.Join(errors.New("pma_"), errors.New(input)),
		} {
			t.Run(name+"/"+form, func(t *testing.T) {
				got := Redacted(err)
				for _, secret := range []string{"PrivateMark", "private-marker", input, b64, b64url} {
					if strings.Contains(got, secret) {
						t.Fatalf("%q reached the log: %q", secret, got)
					}
				}
				if got != "error (its text is not logged)" {
					t.Fatalf("not the fixed kind: %q", got)
				}
			})
		}
	}
	// A fixed kind formatted with %v is no longer one: its text is withheld.
	k := contracts.InvalidClient(contracts.ClientIDInvalid)
	//nolint:errorlint // formatted, not wrapped, on purpose: the kind is lost
	for _, err := range []error{fmt.Errorf("state PrivateMark: %v", k), fmt.Errorf("state PrivateMark: %+v", k)} {
		if got := Redacted(err); got != "error (its text is not logged)" {
			t.Fatalf("%q", got)
		}
	}
}

// PR #137's review, finding 2: a transport error is logged as its category
// and an HTTP method, never as an Op someone else wrote.
func TestReview137ATransportErrorsOpIsAMethodOrNothing(t *testing.T) {
	for op, want := range map[string]string{
		"Get":                     "transport error (Get)",
		"Delete":                  "transport error (Delete)",
		"pma_" + "PrivateMarkAbZ": "transport error",
		"https://private-marker":  "transport error",
	} {
		err := fmt.Errorf("store: %w", &url.Error{Op: op, URL: "https://private-marker.invalid/x", Err: errors.New("PrivateMark")})
		if got := Redacted(err); got != want {
			t.Errorf("%q: %q, want %q", op, got, want)
		}
	}
}

// PR #137's review: what the fixed kinds do under any wrapping, and what a
// context's end is logged as; the chain stays whole for callers.
func TestReview137FixedKindsSurviveErrorTrees(t *testing.T) {
	k := contracts.InvalidClient(contracts.ClientIDInvalid)
	forms := []error{k, fmt.Errorf("state PrivateMark: %w", k), fmt.Errorf("state PrivateMark: %+w", k),
		errors.Join(errors.New("PrivateMark"), k), errors.Join(k, errors.New("PrivateMark")),
		fmt.Errorf("many: %w %w", errors.New("PrivateMark"), k),
		&url.Error{Op: "PrivateMark", URL: "https://private-marker.invalid", Err: fmt.Errorf("verifier PrivateMark: %w", k)}}
	for i, e := range forms {
		if got := Redacted(e); got != k.LogKind() {
			t.Errorf("%d: %q", i, got)
		}
		if !errors.Is(e, contracts.ErrInvalidClient) {
			t.Errorf("%d: the chain was cut", i)
		}
	}
	for err, want := range map[error]string{
		fmt.Errorf("record PrivateMark: %w", context.Canceled):         "context canceled",
		fmt.Errorf("record PrivateMark: %w", context.DeadlineExceeded): "context deadline exceeded",
	} {
		if got := Redacted(err); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
