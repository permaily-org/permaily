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
	kms := attestation.KMSStatus{KeyARN: "arn:aws:kms:us-east-2:111122223333:key/k", Policy: `{"Statement":[]}`, FetchedAt: time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)}

	t.Run("with a KMS key", func(t *testing.T) {
		rec := call(httpapi.Status(func(context.Context) (attestation.KMSStatus, error) { return kms, nil }, nil), http.MethodGet)
		var got attestation.Status
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if got.KMS == nil || !reflect.DeepEqual(*got.KMS, kms) {
			t.Fatalf("got %+v", got.KMS)
		}
	})

	t.Run("without a KMS key", func(t *testing.T) {
		rec := call(httpapi.Status(nil, nil), http.MethodGet)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kms":null`) {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("KMS unreachable", func(t *testing.T) {
		// The enclave must not answer as if the key had no policy, and must not leak the cause.
		rec := call(httpapi.Status(func(context.Context) (attestation.KMSStatus, error) {
			return attestation.KMSStatus{}, errors.New("secret detail arn:aws:iam::1:role/x")
		}, nil), http.MethodGet)
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "secret detail") {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("TLS identity", func(t *testing.T) {
		tls := &attestation.TLSStatus{SPKISHA256: "ab", KeyCreatedAt: kms.FetchedAt, Sealed: true, ACMEAccount: "https://ca.test/acct/1"}
		rec := call(httpapi.Status(nil, func() *attestation.TLSStatus { return tls }), http.MethodGet)
		var got attestation.Status
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.TLS == nil || *got.TLS != *tls {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		// While the key is not loaded there is no TLS section, not an empty one.
		rec = call(httpapi.Status(nil, func() *attestation.TLSStatus { return nil }), http.MethodGet)
		if strings.Contains(rec.Body.String(), `"tls"`) {
			t.Fatalf("body %s", rec.Body)
		}
	})

	t.Run("other methods", func(t *testing.T) {
		if rec := call(httpapi.Status(nil, nil), http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", rec.Code)
		}
	})
}
