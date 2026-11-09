package hostproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/enclave"
)

func fakeIMDS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			_, _ = w.Write([]byte("tok"))
		case r.Header.Get("X-aws-ec2-metadata-token") != "tok":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			_, _ = w.Write([]byte("permaily-enclave-host\n"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/permaily-enclave-host":
			_, _ = w.Write([]byte(`{"Code":"Success","AccessKeyId":"AKIA","SecretAccessKey":"secret","Token":"session","Expiration":"2026-11-09T18:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestCredentialsEndToEnd: the enclave's provider reads the host handler,
// which reads IMDSv2.
func TestCredentialsEndToEnd(t *testing.T) {
	imds := fakeIMDS(t)
	defer imds.Close()
	host := httptest.NewServer(&Credentials{IMDS: imds.URL})
	defer host.Close()

	provider := enclave.HostCredentials{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(host.URL, "http://"))
	}}
	c, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "AKIA" || c.SecretAccessKey != "secret" || c.SessionToken != "session" || !c.CanExpire || c.Expires.IsZero() {
		t.Fatalf("credentials: %+v", c)
	}

	rec := httptest.NewRecorder()
	(&Credentials{IMDS: imds.URL}).ServeHTTP(rec, httptest.NewRequest("GET", "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("other path: %d", rec.Code)
	}
	broken := httptest.NewServer(http.NotFoundHandler())
	defer broken.Close()
	rec = httptest.NewRecorder()
	(&Credentials{IMDS: broken.URL}).ServeHTTP(rec, httptest.NewRequest("GET", "/credentials", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("broken IMDS: %d", rec.Code)
	}
}
