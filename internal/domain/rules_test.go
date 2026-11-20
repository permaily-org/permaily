package domain

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The rules' notice the screen shows is the one the server records (ADR
// 0040): the app and the server ship in one image.
func TestRulesNoticeVersionMatchesTheWebApp(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "rules-notice.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^export const RULES_NOTICE_VERSION = '([^']+)'$`).FindStringSubmatch(string(src))
	if m == nil || m[1] != RulesNoticeVersion {
		t.Fatalf("web/src/lib/rules-notice.ts says %v, the server %s", m, RulesNoticeVersion)
	}
	if RulesNoticeVersion == ModelNoticeVersion {
		t.Fatal("the rules' notice shares the model's notice's version: a change of one would read as a change of the other")
	}
}
