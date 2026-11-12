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

// The enclave sends on this and a verifier judges with it: every state but
// the ones known to be harmless must count as not clean.
func TestMailStateClean(t *testing.T) {
	const none, set, unverified, absent = attestation.MailNoConfigurationSet, attestation.MailConfigurationSet, attestation.MailNotVerified, attestation.MailAbsent
	for _, c := range []struct {
		domain, address string
		want            bool
	}{
		{none, absent, true},
		{none, none, true},
		{none, unverified, true},
		// A configuration set anywhere applies to what is sent.
		{set, absent, false},
		{none, set, false},
		{set, set, false},
		{set, none, false},
		// Without a domain identity that may send, another identity's settings apply.
		{absent, absent, false},
		{absent, none, false},
		{unverified, absent, false},
		{unverified, none, false},
		// What this code does not know is not taken for good.
		{"", "", false},
		{none, "", false},
		{"", absent, false},
		{none, "archived", false},
		{"NO_CONFIGURATION_SET", absent, false},
	} {
		if got := (attestation.MailState{DomainIdentity: c.domain, AddressIdentity: c.address}).Clean(); got != c.want {
			t.Errorf("domain %q, address %q: clean %v, want %v", c.domain, c.address, got, c.want)
		}
	}
}

// The names in the JSON are what verifiers in other languages read.
func TestMailStatusJSON(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	state := attestation.MailState{DomainIdentity: attestation.MailNoConfigurationSet, AddressIdentity: attestation.MailAbsent}
	got, err := json.Marshal(attestation.Status{Commit: "abc", Mail: &attestation.MailStatus{
		Domain: "mail.example.org", Address: "no-reply@mail.example.org", CheckedAt: at.Add(time.Minute), State: &state, WatchedSince: at,
		History: []attestation.MailObservation{{MailState: state, FirstSeen: at, LastSeen: at.Add(time.Minute)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"commit":"abc","mail":{"domain":"mail.example.org","address":"no-reply@mail.example.org","checked_at":"2026-11-11T12:01:00Z",` +
		`"state":{"domain_identity":"no_configuration_set","address_identity":"absent"},"watched_since":"2026-11-11T12:00:00Z",` +
		`"history":[{"domain_identity":"no_configuration_set","address_identity":"absent","first_seen":"2026-11-11T12:00:00Z","last_seen":"2026-11-11T12:01:00Z"}]}}`
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// Before the first reading there is no state, and nothing made up for it.
	got, _ = json.Marshal(attestation.MailStatus{Domain: "mail.example.org", Address: "no-reply@mail.example.org", Failing: true})
	if string(got) != `{"domain":"mail.example.org","address":"no-reply@mail.example.org","failing":true}` {
		t.Fatalf("got %s", got)
	}
	// An image that sends no mail has no section.
	if got, _ = json.Marshal(attestation.Status{Commit: "abc"}); string(got) != `{"commit":"abc"}` {
		t.Fatalf("got %s", got)
	}
}

func TestAdminStatusJSON(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	got, err := json.Marshal(attestation.Status{Commit: "abc", Admin: &attestation.AdminStatus{
		Since: at, Actions: map[string]int{attestation.AdminBlock: 2, attestation.AdminInvitationsMade: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"commit":"abc","admin":{"since":"2026-11-11T12:00:00Z","actions":{"block":2,"invitations_made":0}}}`; string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// A kind this package does not know is kept: a later image may count more.
	var st attestation.Status
	if err := json.Unmarshal([]byte(`{"commit":"abc","admin":{"since":"2026-11-11T12:00:00Z","actions":{"block":1,"something_new":3}}}`), &st); err != nil || st.Admin.Actions["something_new"] != 3 || !st.Admin.Since.Equal(at) {
		t.Fatalf("%+v %v", st.Admin, err)
	}
	if len(attestation.AdminActions) != 11 {
		t.Fatalf("%d kinds", len(attestation.AdminActions))
	}
}
