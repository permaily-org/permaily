package mailhtml

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Test 22: golden outputs of real messages (a receipt, a newsletter with
// @media and floats, Gmail's forwarding confirmation), so a change of
// output is seen in review. go test ./internal/mailhtml -run TestGolden
// -update writes them again.
func TestGolden22(t *testing.T) {
	files, _ := filepath.Glob("testdata/golden/*.html")
	if len(files) != 3 {
		t.Fatal(files)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			in := input(string(b))
			in.Inline = map[string]InlinePart{"map@trails": {Type: "image/png", Data: pngImage(t, 300, 200, false)}}
			st, out := renderOK(t, in)
			if st.Status != contracts.HTMLShown {
				t.Fatalf("%+v", st)
			}
			stats, _ := json.MarshalIndent(st, "", "  ")
			got := out + "\n" + string(stats) + "\n"
			golden := strings.TrimSuffix(f, ".html") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, []byte(got)) {
				t.Errorf("the output changed; run with -update and review the diff:\n%s", got)
			}
		})
	}
}

// The tree bound accepts real mail: the golden newsletter repeated to
// 2 MiB, a newsletter far longer than real ones, is shown.
func TestTreeBoundAcceptsRealMail(t *testing.T) {
	src := realLookingNewsletter(t, contracts.MessageHTMLMaxBytes)
	bound, err := treeBound(context.Background(), []byte(src))
	if err != nil {
		t.Fatalf("the bound refuses a real-looking newsletter of %d bytes: %v", len(src), err)
	}
	st, _ := renderOK(t, input(src))
	if st.Status != contracts.HTMLShown {
		t.Fatalf("%+v", st)
	}
	t.Logf("%d bytes, tree bound %d, %d links", len(src), bound, st.Links)
}

// realLookingNewsletter is the golden newsletter's body repeated to at most
// size bytes, with distinct links.
func realLookingNewsletter(t testing.TB, size int) string {
	t.Helper()
	b, err := os.ReadFile("testdata/golden/newsletter.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	head, body := s[:strings.Index(s, "<body>")+6], s[strings.Index(s, "<body>")+6:strings.Index(s, "</body>")]
	var out strings.Builder
	out.WriteString(head)
	for i := 0; out.Len()+len(body)+20 < size; i++ {
		out.WriteString(strings.ReplaceAll(body, "week-46", "week-"+string(rune('a'+i%26))))
	}
	out.WriteString("</body></html>")
	return out.String()
}
