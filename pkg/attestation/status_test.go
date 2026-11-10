package attestation_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestFetchStatus(t *testing.T) {
	want := attestation.Status{Commit: "abc", KMS: &attestation.KMSStatus{
		KeyARN: "arn:aws:kms:us-east-2:111122223333:key/k", Policy: `{"Statement":[]}`, Grants: 2,
		FetchedAt:    time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC),
		WatchedSince: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC),
		History:      []attestation.KMSObservation{{Policy: `{"Statement":[]}`, Grants: 2, FirstSeen: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)}},
		Gaps:         []attestation.KMSGap{{From: time.Date(2026, 11, 10, 3, 10, 0, 0, time.UTC), To: time.Date(2026, 11, 10, 3, 12, 0, 0, time.UTC)}},
	}}
	var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(want)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(w, r) }))
	defer srv.Close()
	client := clientTrusting(srv.Certificate())
	bound := attestation.SPKIHash(srv.Certificate())

	t.Run("served with the attested key", func(t *testing.T) {
		got, err := attestation.FetchStatus(context.Background(), client, srv.URL+"/", bound)
		if err != nil {
			t.Fatal(err)
		}
		if got.Commit != want.Commit || got.KMS == nil || !reflect.DeepEqual(*got.KMS, *want.KMS) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("served with another key", func(t *testing.T) {
		// Someone else answers for the name with a certificate the client trusts:
		// the status must not be accepted on the strength of an attestation it is not bound to.
		cert := selfSigned(t)
		other := httptest.NewUnstartedServer(handler)
		other.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		other.StartTLS()
		defer other.Close()
		_, err := attestation.FetchStatus(context.Background(), clientTrusting(cert.Leaf), other.URL, bound)
		if !errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want ErrNotBound", err)
		}
	})

	t.Run("no attested key given", func(t *testing.T) {
		if _, err := attestation.FetchStatus(context.Background(), client, srv.URL, nil); !errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want ErrNotBound", err)
		}
	})

	t.Run("without TLS", func(t *testing.T) {
		plain := httptest.NewServer(handler)
		defer plain.Close()
		if _, err := attestation.FetchStatus(context.Background(), plain.Client(), plain.URL, bound); !errors.Is(err, attestation.ErrNotTLS) {
			t.Fatalf("got %v, want ErrNotTLS", err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		if _, err := attestation.FetchStatus(context.Background(), client, "https://127.0.0.1:1", bound); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("bad URL", func(t *testing.T) {
		if _, err := attestation.FetchStatus(context.Background(), client, "https://bad host", bound); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("error status", func(t *testing.T) {
		handler = func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "KMS key policy unavailable", http.StatusServiceUnavailable)
		}
		_, err := attestation.FetchStatus(context.Background(), client, srv.URL, bound)
		if err == nil || errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want an HTTP error", err)
		}
	})

	t.Run("not JSON", func(t *testing.T) {
		handler = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }
		if _, err := attestation.FetchStatus(context.Background(), client, srv.URL, bound); !errors.Is(err, attestation.ErrMalformed) {
			t.Fatalf("got %v, want ErrMalformed", err)
		}
	})
}
