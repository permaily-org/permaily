package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestStatus(t *testing.T) {
	call := func(h http.Handler, method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/status", nil))
		return rec
	}
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)

	t.Run("an image that stores nothing", func(t *testing.T) {
		rec := call(httpapi.Status(httpapi.StatusSources{}), http.MethodGet)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), `"root"`) || strings.Contains(rec.Body.String(), `"kms"`) {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("root secret", func(t *testing.T) {
		root := &attestation.RootStatus{
			Image: "aa", KeyARN: "arn:aws:kms:us-east-2:1:key/k", Policy: "{}", Origin: attestation.RootHandedOver, From: &attestation.RootFrom{Image: "bb", Commit: "c1"},
			CheckedAt: at, WatchedSince: at, History: []attestation.KMSObservation{{Policy: "{}", FirstSeen: at, LastSeen: at}},
		}
		rec := call(httpapi.Status(httpapi.StatusSources{Root: func(context.Context) (*attestation.RootStatus, error) { return root, nil }}), http.MethodGet)
		var got attestation.Status
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Root == nil || got.Root.KeyARN != root.KeyARN || got.Root.From == nil || got.Root.From.Image != "bb" || !reflect.DeepEqual(got.Root.History, root.History) {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		// An image that waits for its root secret, or cannot reach KMS, does not
		// answer as if it had none, and does not leak the cause.
		rec = call(httpapi.Status(httpapi.StatusSources{Root: func(context.Context) (*attestation.RootStatus, error) {
			return nil, errors.New("secret detail arn:aws:iam::1:role/x")
		}}), http.MethodGet)
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "secret detail") {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("TLS identity", func(t *testing.T) {
		tls := &attestation.TLSStatus{SPKISHA256: "ab", KeyCreatedAt: at, Sealed: true, ACMEAccount: "https://ca.test/acct/1"}
		rec := call(httpapi.Status(httpapi.StatusSources{TLS: func() *attestation.TLSStatus { return tls }}), http.MethodGet)
		var got attestation.Status
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.TLS == nil || *got.TLS != *tls {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		// While the key is not loaded there is no TLS section, not an empty one.
		rec = call(httpapi.Status(httpapi.StatusSources{TLS: func() *attestation.TLSStatus { return nil }}), http.MethodGet)
		if strings.Contains(rec.Body.String(), `"tls"`) {
			t.Fatalf("body %s", rec.Body)
		}
	})

	t.Run("other methods", func(t *testing.T) {
		if rec := call(httpapi.Status(httpapi.StatusSources{}), http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", rec.Code)
		}
	})
}
