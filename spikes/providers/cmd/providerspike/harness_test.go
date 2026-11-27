package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

// These tests touch no provider: they check the harness's own guards.

func TestCredentialFilesMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PERMAILY_SPIKE_CONFIG", dir)
	t.Setenv("PERMAILY_SPIKE_GOOGLE_CLIENT_ID", "")
	t.Setenv("PERMAILY_SPIKE_GOOGLE_CLIENT_SECRET", "")
	path := filepath.Join(dir, "spike-google-client.json")

	if _, err := loadGoogleClient(); !errors.Is(err, errNoCredentials) {
		t.Fatalf("missing file: %v", err)
	}
	const secret = "made-up-secret-value"
	if err := os.WriteFile(path, []byte(`{"client_id":"id","client_secret":"`+secret+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadGoogleClient()
	if err == nil || !strings.Contains(err.Error(), "must be 0600") {
		t.Fatalf("world-readable file accepted: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error echoes the secret")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadGoogleClient()
	if err != nil || c.ClientSecret != secret {
		t.Fatalf("private file: %v", err)
	}
	// Bad JSON is reported without its content.
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGoogleClient(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("bad JSON: %v", err)
	}
}

func TestGrantIsWrittenPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PERMAILY_SPIKE_CONFIG", dir)
	if err := saveGoogleGrant(googleGrant{RefreshToken: "made-up"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "spike-google-grant.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("grant file mode %o", fi.Mode().Perm())
	}
	g, err := loadGoogleGrant()
	if err != nil || g.RefreshToken != "made-up" {
		t.Fatalf("grant: %v", err)
	}
}

func TestResultsRefuseAddressesAndTokens(t *testing.T) {
	res := newResults(t.TempDir())
	for _, bad := range []map[string]any{
		{"who": "person@example.test"},
		{"nested": map[string]any{"token": "ya29.a0made-up"}},
		{"list": []string{"ok", "1//0made-up-refresh"}},
		{"jwt": "eyJhbGciOiJub25lIn0.e30."},
		{"header": "Bearer made-up"},
	} {
		if err := res.record("guard", bad); err == nil {
			t.Errorf("recorded %v", bad)
		}
	}
	if err := res.record("guard", map[string]any{"scope": "openid https://www.googleapis.com/auth/gmail.readonly", "status": 200}); err != nil {
		t.Fatalf("a safe record refused: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(res.dir, "*-guard.jsonl"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("expected exactly the safe record, got:\n%s", b)
	}
	fi, _ := os.Stat(files[0])
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("results file mode %o", fi.Mode().Perm())
	}
}

func TestPseudonymsAreStableAndOpaque(t *testing.T) {
	t.Setenv("PERMAILY_SPIKE_CONFIG", t.TempDir())
	p1, err := loadPseudonym()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := loadPseudonym()
	if err != nil {
		t.Fatal(err)
	}
	a, b := p1.of("msg", "18c0ffee"), p2.of("msg", "18c0ffee")
	if a != b || strings.Contains(a, "18c0ffee") || p1.of("folder", "18c0ffee") == a {
		t.Errorf("pseudonyms %q %q", a, b)
	}
}

// A 24 MiB message in format=raw is decoded as it arrives, through a reader
// that hands over a byte at a time in places, with the other members kept.
func TestStreamRawDecodesWithoutBuffering(t *testing.T) {
	msg := make([]byte, 24<<20)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}
	enc := base64.URLEncoding.EncodeToString(msg) // padded, as either form may come
	doc := fmt.Sprintf(`{"id": "18c0ffee", "threadId":"18c0ffee", "labelIds": ["INBOX", "UNREAD"], "snippet": "a \"quoted\" {snippet}", "sizeEstimate": %d, "historyId": "4242", "internalDate": "1764244800000", "raw": "%s"}`, len(msg), enc)

	h := sha256.New()
	counter := &countingWriter{w: h}
	reader := iotest.HalfReader(strings.NewReader(doc))
	others, n, err := streamRaw(reader, counter, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(msg)) {
		t.Fatalf("decoded %d bytes, want %d", n, len(msg))
	}
	want := sha256.Sum256(msg)
	if !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatal("decoded content differs")
	}
	for _, k := range []string{"id", "threadId", "labelIds", "snippet", "sizeEstimate", "historyId", "internalDate"} {
		if _, ok := others[k]; !ok {
			t.Errorf("member %s lost", k)
		}
	}
	var snippet string
	if err := json.Unmarshal(others["snippet"], &snippet); err != nil || snippet != `a "quoted" {snippet}` {
		t.Errorf("snippet %q %v", snippet, err)
	}
	if counter.maxWrite > 1<<20 {
		t.Errorf("a single write of %d bytes: the decoder buffered", counter.maxWrite)
	}
}

func TestStreamRawRefusesMalformed(t *testing.T) {
	for _, doc := range []string{
		`{"raw": 12}`,
		`{"raw": "abc\u0041"}`, // an escape inside base64url
		`{"raw": "abc`,
		`["raw"]`,
		`{"id": "` + strings.Repeat("x", 2<<20) + `"}`,
	} {
		if _, _, err := streamRaw(strings.NewReader(doc), io.Discard, 1<<20); err == nil {
			t.Errorf("accepted %.40q", doc)
		}
	}
}

type countingWriter struct {
	w        io.Writer
	maxWrite int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.maxWrite = max(c.maxWrite, len(p))
	return c.w.Write(p)
}

func TestEveryListedHostIsOn993Names(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range imapHosts {
		if seen[h.ID] || !strings.Contains(h.Host, ".") || h.FailureDomain == "" {
			t.Errorf("host entry %+v", h)
		}
		seen[h.ID] = true
	}
	if len(imapHosts) != 9 {
		t.Errorf("%d hosts, the ADR lists nine", len(imapHosts))
	}
}
