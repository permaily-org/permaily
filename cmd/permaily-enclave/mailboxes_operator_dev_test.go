//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

// The operator's routes of mailboxes are mounted on the API host, behind the
// admin token (ADR 0026): without it every one is 401; with it an address
// that leads to no mailbox is an empty list, an identifier that is no
// account or no mailbox is not_found, and every answer is JSON with nosniff.
func TestTheOperatorsMailboxRoutesAreMounted(t *testing.T) {
	const token = "pma_e2e_mailboxes_operator_token"
	sum := sha256.Sum256([]byte(token))
	cfg := config{
		domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), mtaSTSMode: "testing",
		adminTokenSHA256: hex.EncodeToString(sum[:]), superAdmin: defaultSuperAdmin,
	}
	var logs lockedBuffer
	go func() { _ = run(cfg, slog.New(slog.NewJSONHandler(&logs, nil))) }()
	b := &devBrowser{t: t, app: fmt.Sprintf("https://app.permaily.test:%d", cfg.port), client: &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.port))
		},
	}}}
	const api = "https://api.permaily.test"
	none := "/v1/admin/mailboxes/mbx_" + strings.Repeat("a", 26)
	routes := [][2]string{
		{"GET", "/v1/admin/mailboxes?address=nobody%40inbox.permaily.test"}, {"POST", none + "/pause"}, {"POST", none + "/resume"},
		{"GET", "/v1/admin/accounts/acc_" + strings.Repeat("a", 26) + "/mailboxes"},
	}
	for _, r := range routes {
		if code, body := b.call(r[0], api+r[1], "", ""); code != 401 {
			t.Errorf("%s %s without the token: %d %s", r[0], r[1], code, body)
		}
	}
	if code, body := b.call("GET", api+routes[0][1], token, ""); code != 200 || body != `{"mailboxes":[]}`+"\n" {
		t.Fatalf("an address of no mailbox: %d %s", code, body)
	}
	for _, r := range routes[1:] {
		if code, body := b.call(r[0], api+r[1], token, ""); code != 404 || !strings.Contains(body, `"not_found"`) {
			t.Errorf("%s %s: %d %s", r[0], r[1], code, body)
		}
	}
	if strings.Contains(logs.String(), "nobody@inbox") || strings.Contains(logs.String(), "mbx_") {
		t.Fatalf("the log holds what was asked:\n%s", logs.String())
	}
}
