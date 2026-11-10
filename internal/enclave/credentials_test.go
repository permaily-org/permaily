package enclave

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHostCredentials(t *testing.T) {
	body, status := "", http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/credentials" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	creds := HostCredentials{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}
	ctx := context.Background()

	body = `{"AccessKeyId":"AKIAEXAMPLE","SecretAccessKey":"secret","Token":"session","Expiration":"2026-11-10T12:00:00Z"}`
	got, err := creds.Retrieve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if got.AccessKeyID != "AKIAEXAMPLE" || got.SecretAccessKey != "secret" || got.SessionToken != "session" || !got.CanExpire || !got.Expires.Equal(want) {
		t.Fatalf("credentials %+v", got)
	}

	body = `{"AccessKeyId":"AKIAEXAMPLE","SecretAccessKey":"secret"}`
	if got, err := creds.Retrieve(ctx); err != nil || got.CanExpire {
		t.Fatalf("credentials without expiration: %+v, %v", got, err)
	}

	// Whatever the host answers, the enclave never ends up with half a credential.
	for name, c := range map[string]struct {
		body   string
		status int
	}{
		"no secret":    {`{"AccessKeyId":"AKIAEXAMPLE"}`, http.StatusOK},
		"no key id":    {`{"SecretAccessKey":"secret"}`, http.StatusOK},
		"not JSON":     {`<html>`, http.StatusOK},
		"host error":   {`metadata unavailable`, http.StatusBadGateway},
		"empty answer": {``, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			body, status = c.body, c.status
			if got, err := creds.Retrieve(ctx); err == nil || got.AccessKeyID != "" {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}

	down := HostCredentials{Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("connection reset by peer") }}
	if _, err := down.Retrieve(ctx); err == nil {
		t.Fatal("no error when the host does not answer")
	}
}
