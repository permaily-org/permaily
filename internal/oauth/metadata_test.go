package oauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// golden compares v's JSON with testdata/golden/name, which -update writes.
func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil { //nolint:gosec // G306: test data
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s:\n got %s\nwant %s", name, got, want)
	}
}

// V9. The metadata documents, golden: every field of ADR 0030's
// "Metadata", no registration, no device flow, nothing of OpenID.
func TestV9TheMetadataDocuments(t *testing.T) {
	m := Metadata(config)
	golden(t, "server-metadata.json", m)
	golden(t, "resource-mcp.json", ResourceMetadataFor(config, contracts.ResourceMCP))
	golden(t, "resource-api.json", ResourceMetadataFor(config, contracts.ResourceAPI))
	b, _ := json.Marshal(m)
	for _, absent := range []string{"registration_endpoint", "device_authorization_endpoint", "userinfo", "id_token", "openid", "jwks_uri", "client_secret"} {
		if strings.Contains(string(b), absent) {
			t.Errorf("the metadata says %s", absent)
		}
	}
	// Each document is its own: changing one changes no other.
	m.TokenEndpointAuthMethodsSupported[0] = "changed"
	if Metadata(config).RevocationEndpointAuthMethods[0] != AuthNone || Metadata(config).TokenEndpointAuthMethodsSupported[0] != AuthNone {
		t.Fatal("shared slices")
	}
	r := ResourceMetadataFor(config, contracts.ResourceMCP)
	r.ScopesSupported[0] = "changed"
	if DefaultScopes[0] != ScopeSender {
		t.Fatal("the default scopes were handed out")
	}
}

// V10. The challenges of a 401 and a 403, golden.
func TestV10Challenge(t *testing.T) {
	for _, c := range []struct {
		r      contracts.Resource
		code   ErrorCode
		scopes []string
		want   string
	}{
		{contracts.ResourceMCP, "", nil, `Bearer resource_metadata="https://mcp.permaily.com/.well-known/oauth-protected-resource/mcp", scope="mail.sender mail.subject mail.body"`},
		{contracts.ResourceAPI, "", nil, `Bearer resource_metadata="https://api.permaily.com/.well-known/oauth-protected-resource/v1", scope="mail.sender mail.subject mail.body"`},
		{contracts.ResourceMCP, ErrInvalidToken, nil, `Bearer error="invalid_token", resource_metadata="https://mcp.permaily.com/.well-known/oauth-protected-resource/mcp", scope="mail.sender mail.subject mail.body"`},
		{contracts.ResourceMCP, ErrInsufficientScope, []string{ScopeAttachments, ScopeBody, ScopeSender, ScopeSubject, ScopeBody},
			`Bearer error="insufficient_scope", resource_metadata="https://mcp.permaily.com/.well-known/oauth-protected-resource/mcp", scope="mail.sender mail.subject mail.body mail.attachments"`},
		{contracts.ResourceAPI, ErrInsufficientScope, []string{ScopeSender, ScopeAttachments, `x", error="forged`},
			`Bearer error="insufficient_scope", resource_metadata="https://api.permaily.com/.well-known/oauth-protected-resource/v1", scope="mail.sender mail.attachments"`},
	} {
		if got := Challenge(config, c.r, c.code, c.scopes); got != c.want {
			t.Errorf("\n got %s\nwant %s", got, c.want)
		}
	}
	// In dev mode the hosts carry a port.
	dev := Config{Issuer: "https://api.permaily.localhost:8443", AppOrigin: "https://app.permaily.localhost:8443", APIResource: "https://api.permaily.localhost:8443/v1", MCPResource: "https://mcp.permaily.localhost:8443/mcp"}
	if got := ResourceMetadataURL(dev, contracts.ResourceMCP); got != "https://mcp.permaily.localhost:8443/.well-known/oauth-protected-resource/mcp" {
		t.Fatal(got)
	}
}
