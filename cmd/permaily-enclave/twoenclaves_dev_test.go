//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/domain"
)

// Two enclaves of one deployment, over one storage: a sign-up by mail started
// through one ends through the other, whose session the first then knows.
func TestASignInStartedThroughOneEnclaveEndsThroughTheOther(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	const admin = "pma_two_enclaves"
	sum := sha256.Sum256([]byte(admin))
	ports := []uint32{freePort(t), freePort(t)}
	for _, port := range ports {
		cfg := config{
			domain: "permaily.test", acmeDirectory: "unused", port: port, mtaSTSMode: "testing",
			adminTokenSHA256: hex.EncodeToString(sum[:]),
		}
		go func() { _ = run(cfg, slog.New(slog.DiscardHandler)) }()
	}
	// One browser, whose requests go to one enclave or the other.
	jar, _ := cookiejar.New(nil)
	call := func(enclave int, method, url, auth, body string) (int, string) {
		t.Helper()
		client := &http.Client{Jar: jar, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", ports[enclave]))
			},
		}}
		var res *http.Response
		for range 50 {
			req, _ := http.NewRequest(method, url, strings.NewReader(body))
			if auth != "" {
				req.Header.Set("Authorization", "Bearer "+auth)
			}
			if strings.HasPrefix(url, "https://app.") && method != http.MethodGet {
				req.Header.Set("Origin", "https://app.permaily.test")
				req.Header.Set("Content-Type", "application/json")
			}
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	// Both answer before anything starts.
	for i := range ports {
		call(i, "GET", "https://app.permaily.test/api/v1/auth/providers", "", "")
	}
	code, body := call(0, "POST", "https://api.permaily.test/v1/admin/invitations", admin, `{"count":1}`)
	var made struct {
		Invitations []string `json:"invitations"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Invitations) != 1 {
		t.Fatalf("invitations: %d %s", code, body)
	}
	code, body = call(0, "POST", "https://app.permaily.test/api/v1/auth/email/start", "", `{"address":"ana@example.org","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`)
	var started struct {
		Attempt string `json:"attempt"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &started) != nil || started.Attempt == "" {
		t.Fatalf("start: %d %s", code, body)
	}
	// The message is in the outbox of the enclave that sent it.
	var signInCode string
	for range 50 {
		_, outbox := call(0, "GET", "https://app.permaily.test/dev/outbox", "", "")
		if g := regexp.MustCompile(`\\n    (\d{6})\\n`).FindStringSubmatch(outbox); g != nil && strings.Contains(outbox, started.Attempt) {
			signInCode = g[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if signInCode == "" {
		t.Fatal("no sign-in message in the outbox")
	}
	code, body = call(1, "POST", "https://app.permaily.test/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+signInCode+`"}`)
	if code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) {
		t.Fatalf("finished through the other enclave: %d %s", code, body)
	}
	// The agreement to the terms given through the first was kept with the
	// sign-in, and the account made through the second records it.
	if code, body := call(0, "GET", "https://app.permaily.test/api/v1/me", "", ""); code != 200 || !strings.Contains(body, `"address":"ana@example.org"`) || !strings.Contains(body, `"terms":"`+domain.TermsVersion+`"`) {
		t.Fatalf("the session through the first enclave: %d %s", code, body)
	}
	// It worked once: the same code through the first is refused.
	if code, _ := call(0, "POST", "https://app.permaily.test/api/v1/auth/email/finish", "", `{"attempt":"`+started.Attempt+`","code":"`+signInCode+`"}`); code == 200 {
		t.Fatal("a sign-in ended twice")
	}
}
