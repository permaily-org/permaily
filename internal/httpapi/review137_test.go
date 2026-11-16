package httpapi_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
)

// PR #137's review, finding 2, through the boundary itself: a storage or
// connections error whose text holds a secret percent-encoded, JSON-escaped
// or in base64, or a URL in any of those, reaches the API's log of internal
// errors as a fixed kind, never as its text.
func TestReview137EncodedSecretsNeverReachTheLog(t *testing.T) {
	value := "PrivateMarkAbZ_x-" + strings.Repeat("Q", 26)
	token := "pma_" + value
	b64 := base64.StdEncoding.EncodeToString([]byte(token))
	for name, text := range map[string]string{
		"percent": url.QueryEscape(token),
		"json":    `{"token":"pma\u005f` + value + `"}`,
		"base64":  b64,
		"url":     "https%3A%2F%2Fprivate-marker.invalid%2Fcallback%3Fstate%3D" + value,
	} {
		t.Run(name, func(t *testing.T) {
			a := newApp(t)
			a.signIn("ana@example.org")
			err := fmt.Errorf("connections: record %s: %w", text, errors.New("down"))
			a.h = httpapi.AccountsAPI(broken{err: err}, slog.New(slog.NewTextHandler(a.logs, nil)))
			if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 500 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			logs := a.logs.String()
			if !strings.Contains(logs, "api: internal error") || !strings.Contains(logs, "error (its text is not logged)") {
				t.Fatalf("not logged as its kind:\n%s", logs)
			}
			for _, secret := range []string{"PrivateMark", "private-marker", text, b64} {
				if strings.Contains(logs, secret) {
					t.Fatalf("the log holds %q:\n%s", secret, logs)
				}
			}
		})
	}
}
