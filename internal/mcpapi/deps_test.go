package mcpapi_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	module = "github.com/permaily-org/permaily"
	sdk    = "github.com/modelcontextprotocol/go-sdk"
	self   = module + "/internal/mcpapi"
)

// V53. Only internal/mcpapi links the MCP SDK (depguard's
// mcp-sdk-only-in-mcpapi, checked here on what is built): no other package
// of the module imports it, and none links it but through internal/mcpapi;
// in both builds, with and without the dev tag. internal/mcpapi itself does.
func TestV53OnlyMCPAPILinksTheSDK(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is needed to list what is built: %v", err)
	}
	for _, tags := range []string{"", "dev"} {
		out, err := exec.Command(gobin, "list", "-tags", tags, "-f", "{{.ImportPath}}|{{join .Imports \",\"}}|{{join .Deps \",\"}}", module+"/...").Output()
		if err != nil {
			t.Fatalf("go list: %v", err)
		}
		seen := false
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), "|")
			pkg, imports, deps := fields[0], strings.Split(fields[1], ","), strings.Split(fields[2], ",")
			fromSDK := func(p string) bool { return p == sdk || strings.HasPrefix(p, sdk+"/") }
			if pkg == self {
				seen = true
				if !slices.ContainsFunc(imports, fromSDK) {
					t.Fatal("internal/mcpapi does not speak MCP through the SDK")
				}
				continue
			}
			if slices.ContainsFunc(imports, fromSDK) {
				t.Fatalf("%s imports the MCP SDK (tags %q)", pkg, tags)
			}
			if slices.ContainsFunc(deps, fromSDK) && !slices.Contains(deps, self) {
				t.Fatalf("%s links the MCP SDK other than through internal/mcpapi (tags %q)", pkg, tags)
			}
		}
		if !seen {
			t.Fatal("internal/mcpapi not listed")
		}
	}
}
