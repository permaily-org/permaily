package oauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The vendors' documents and ChatGPT's key set as the spike fetched them on
// 2026-11-16 (ADR 0030, "The clients, observed"), byte for byte: pull request
// 1's checks of a document run on them (V2). A new capture is a new file.
var vendorDocuments = map[string]string{
	"cimd-chatgpt-apps.json":      "88ed18b7cd8d099afceb43258fa9010d3fc72f99ce6370a51fd785d876fe2c6b",
	"cimd-chatgpt-connector.json": "f4a154ea53ac927715b19ec043ec899f31fda759919bb47118de667c15831feb",
	"cimd-claude-ai.json":         "049f8cf53fbaaa902e71402620e96269a8973917b90e69c98b0edccfb9b07f79",
	"cimd-claude-code.json":       "844b69e632732c325e3bae21ab324fa3d5353ba2e15890a384e8885e9837e5a4",
	"cimd-codex.json":             "88c1677cc7dc907c07184571bba9ecaa342904424823d883270f9c8f62940004",
	"jwks-chatgpt.json":           "d6e3f50d10230a5238732ae2d73d98c4f4721a171d22dbfc79a2cff0c2ebc114",
}

func TestTheVendorsDocumentsAreTheSpikes(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "vendors"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(vendorDocuments) {
		t.Fatalf("%d files, want %d", len(entries), len(vendorDocuments))
	}
	methods := map[string]string{}
	for name, want := range vendorDocuments {
		b, err := os.ReadFile(filepath.Join("testdata", "vendors", name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s changed: %s", name, got)
		}
		if name == "jwks-chatgpt.json" {
			continue
		}
		var d ClientDocument
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		methods[d.ClientID] = d.TokenEndpointAuthMethod
	}
	// What the design rests on: Claude's clients and Codex are public,
	// ChatGPT's two documents declare private_key_jwt.
	want := map[string]string{
		"https://claude.ai/oauth/claude-code-client-metadata": AuthNone,
		"https://claude.ai/oauth/mcp-oauth-client-metadata":   AuthNone,
		"https://chatgpt.com/oauth/codex/client.json":         AuthNone,
		"https://chatgpt.com/oauth/client.json":               AuthPrivateKeyJWT,
		"https://chatgpt.com/oauth/chatgpt/client.json":       AuthPrivateKeyJWT,
	}
	for id, m := range want {
		if methods[id] != m {
			t.Errorf("%s: %q, want %q", id, methods[id], m)
		}
	}
}

// Every part has one scope and the default scopes are the parts but
// attachments, sender first (ADR 0030, "Scopes are the parts").
func TestScopesAreTheParts(t *testing.T) {
	parts := map[contracts.Part]bool{}
	scopes := map[string]bool{}
	for _, sp := range ScopeParts {
		if parts[sp.Part] || scopes[sp.Scope] {
			t.Fatalf("%s or %s twice", sp.Part, sp.Scope)
		}
		parts[sp.Part], scopes[sp.Scope] = true, true
	}
	if len(parts) != 4 || ScopeParts[0].Part != contracts.PartSender {
		t.Fatalf("parts %v", ScopeParts)
	}
	if len(DefaultScopes) != 3 || DefaultScopes[0] != ScopeSender || contains(DefaultScopes, ScopeAttachments) {
		t.Fatalf("default scopes %v", DefaultScopes)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
