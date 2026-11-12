package domain

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

func TestNormalizeAddress(t *testing.T) {
	good := map[string]string{
		"ana@example.org":          "ana@example.org",
		"  Ana.Perez@Example.ORG ": "ana.perez@example.org",
		"a+tag@mail.example.co.uk": "a+tag@mail.example.co.uk",
		"o'neil@ex-ample.org":      "o'neil@ex-ample.org",
	}
	for in, want := range good {
		if got, err := NormalizeAddress(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "ana", "@example.org", "ana@", "ana@localhost", "ana@example..org", "ana@-example.org", "ana@example-.org",
		"ana@exa_mple.org", ".ana@example.org", "ana.@example.org", "a..b@example.org", "ana perez@example.org",
		"Ana <ana@example.org>", "ana@example.org,eve@example.org", "ana@example.org\r\nBcc: eve@example.org",
		"aña@example.org", "ana@exámple.org", "a@b@example.org", `"ana"@example.org`,
		strings.Repeat("a", 65) + "@example.org", "ana@" + strings.Repeat("a", 64) + ".org",
		"ana@" + strings.Repeat("a.", 130) + "org",
	}
	for _, in := range bad {
		if got, err := NormalizeAddress(in); !errors.Is(err, contracts.ErrInvalidAddress) {
			t.Errorf("%q: accepted as %q (%v)", in, got, err)
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	created := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		lastUsed time.Duration
		want     time.Duration
	}{
		{0, 7 * day},         // never used again: seven days
		{3 * day, 10 * day},  // each use gives seven more
		{25 * day, 30 * day}, // but never past thirty
		{30 * day, 30 * day},
	}
	for _, c := range cases {
		if got := SessionExpiry(created, created.Add(c.lastUsed)); !got.Equal(created.Add(c.want)) {
			t.Errorf("last used after %v: ends after %v, want %v", c.lastUsed, got.Sub(created), c.want)
		}
	}
}

func TestConfirmedUntil(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if got := ConfirmedUntil(at); !got.Equal(at.Add(10 * time.Minute)) {
		t.Fatalf("a proof lasts %v", got.Sub(at))
	}
	if !ConfirmedUntil(time.Time{}).IsZero() {
		t.Fatal("no proof is fresh until some time")
	}
}

func TestInvitations(t *testing.T) {
	if got := FormatInvitation("abcd2345wxyz"); got != "ABCD-2345-WXYZ" {
		t.Errorf("format: %q", got)
	}
	for _, in := range []string{"abcd2345wxyz", "ABCD-2345-WXYZ", " abcd 2345 wxyz "} {
		if got, ok := NormalizeInvitation(in); !ok || got != "abcd2345wxyz" {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	// Too short, too long, and characters the alphabet leaves out (0, 1, i, l, o).
	for _, in := range []string{"", "abcd2345wxy", "abcd2345wxyzz", "abcd2345wxy0", "abcd2345wxy1", "abcd2345wxyi", "abcd2345wxyl", "abcd2345wxyo", "abcd2345wxy!"} {
		if got, ok := NormalizeInvitation(in); ok {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
}

func TestDevice(t *testing.T) {
	if got := Device("  Mozilla/5.0 (X11; Linux)\r\nX: é\x00 "); got != "Mozilla/5.0 (X11; Linux)X:" {
		t.Errorf("device: %q", got)
	}
	if got := Device(strings.Repeat("a", 500)); len(got) != 200 {
		t.Errorf("a long one is kept at %d", len(got))
	}
}

func TestPasskeyName(t *testing.T) {
	for in, want := range map[string]string{
		"Laptop":                      "Laptop",
		"  MacBook de Ana  ":          "MacBook de Ana",
		"Teléfono 📱":                  "Teléfono 📱",
		strings.Repeat("ñ", 64):       strings.Repeat("ñ", 64),
		" " + strings.Repeat("a", 64): strings.Repeat("a", 64),
	} {
		if got, err := PasskeyName(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	// Nothing, too long, more than one line, and what cannot be shown.
	for _, in := range []string{"", "   ", strings.Repeat("a", 65), "two\nlines", "tab\there", "nul\x00", "bell\a", "\xff\xfe", "right\u202eto left"} {
		if got, err := PasskeyName(in); !errors.Is(err, contracts.ErrInvalidName) {
			t.Errorf("%q: accepted as %q (%v)", in, got, err)
		}
	}
}

func TestAccountName(t *testing.T) {
	for in, want := range map[string]string{
		"Ana":                         "Ana",
		"  Ana Pérez  ":               "Ana Pérez",
		"小林 📬":                        "小林 📬",
		strings.Repeat("ñ", 80):       strings.Repeat("ñ", 80),
		" " + strings.Repeat("a", 80): strings.Repeat("a", 80),
		// A person may have no name here.
		"":   "",
		" 	": "",
	} {
		if got, err := AccountName(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	// Too long, more than one line, and what cannot be shown.
	for _, in := range []string{strings.Repeat("a", 81), "two\nlines", "tab\there", "nul\x00", "bell\a", "\xff\xfe", "right\u202eto left"} {
		got, err := AccountName(in)
		if !errors.Is(err, contracts.ErrInvalidAccountName) || !errors.Is(err, contracts.ErrInvalidName) {
			t.Errorf("%q: accepted as %q (%v)", in, got, err)
		}
	}
	if AccountClosing != 7*24*time.Hour || DefaultAccountsPage > MaxAccountsPage {
		t.Errorf("closing takes %v, a page is %d of at most %d", AccountClosing, DefaultAccountsPage, MaxAccountsPage)
	}
}

func TestRecoveryCodes(t *testing.T) {
	if got := FormatRecoveryCode("abcd2345wxyz6789"); got != "ABCD-2345-WXYZ-6789" {
		t.Errorf("format: %q", got)
	}
	for _, in := range []string{"abcd2345wxyz6789", "ABCD-2345-WXYZ-6789", " abcd 2345 wxyz 6789 "} {
		if got, ok := NormalizeRecoveryCode(in); !ok || got != "abcd2345wxyz6789" {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	// An invitation is not one, nor is a code with a character left out of the alphabet.
	for _, in := range []string{"", "abcd2345wxyz", "abcd2345wxyz67890", "abcd2345wxyz678o", "abcd2345wxyz678!"} {
		if got, ok := NormalizeRecoveryCode(in); ok {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
	// What a code is worth against whoever reads its hash: more than 2^79 guesses.
	if bits := float64(RecoveryCodeLength) * math.Log2(float64(len(InvitationAlphabet))); bits < 79 {
		t.Errorf("a recovery code is %f bits", bits)
	}
}

// Where a sign-in with a provider comes back to is a path of the app, and
// nothing a browser could take for another site.
func TestAppPath(t *testing.T) {
	for _, in := range []string{"/", "/connections", "/settings?tab=sign-in#providers", "/a/b/c", "/apis", "/" + strings.Repeat("a", 511)} {
		if got := AppPath(in); got != in {
			t.Errorf("%q: %q", in, got)
		}
	}
	for _, in := range []string{
		"", "connections", "https://evil.example", "https://evil.example/", "//evil.example", "///evil.example", `/\evil.example`, `\/evil.example`, `\\evil.example`,
		`/ok\..\evil`, "javascript:alert(1)", " /connections", "/connections ", "/two words", "/line\nbreak", "/line\r\nLocation: https://evil.example",
		"/tab\there", "/nul\x00", "/ñ", "/api", "/api/", "/api/v1/auth/provider/callback?state=x", "/" + strings.Repeat("a", 512),
		"http:/evil.example", "evil.example", "@evil.example", ".", "..",
	} {
		if got := AppPath(in); got != "" {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
}

func TestProviderWords(t *testing.T) {
	for _, in := range []string{"1", "108204268033311374519", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", "00000000-0000-0000-66f3-3332eca7ea81", strings.Repeat("a", 255)} {
		if !ProviderSubject(in) {
			t.Errorf("subject %q refused", in)
		}
	}
	for _, in := range []string{"", " ", "1 2", "1\n", "a#b\x00", "ñ", "\x7f", strings.Repeat("a", 256)} {
		if ProviderSubject(in) {
			t.Errorf("subject %q accepted", in)
		}
	}
	for _, in := range []string{"1234-abc.apps.googleusercontent.com", "GOCSPX-a_b-c", "Iv1.0123456789abcdef", "a~b.c_d-e", strings.Repeat("a", 512)} {
		if !ProviderCredential(in) {
			t.Errorf("credential %q refused", in)
		}
	}
	for _, in := range []string{"", " secret", "secret\n", "two words", "tab\t", "ñ", strings.Repeat("a", 513)} {
		if ProviderCredential(in) {
			t.Errorf("credential %q accepted", in)
		}
	}
	if ProviderLifetime != 10*time.Minute || ProviderConfirmLifetime != 20*time.Minute {
		t.Errorf("a trip to a provider takes %v, confirming %v", ProviderLifetime, ProviderConfirmLifetime)
	}
}

func TestDevices(t *testing.T) {
	if DeviceLifetime != 365*24*time.Hour || MaxDevices != 50 {
		t.Errorf("a device is known for %v, and an account keeps %d", DeviceLifetime, MaxDevices)
	}
	for _, in := range []string{strings.Repeat("a", 43), "AZaz09-_" + strings.Repeat("0", 35)} {
		if !DeviceToken(in) {
			t.Errorf("token %q refused", in)
		}
	}
	for _, in := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + ".", strings.Repeat("a", 42) + " ", strings.Repeat("a", 42) + "\x00", strings.Repeat("a", 42) + "{"} {
		if DeviceToken(in) {
			t.Errorf("token %q accepted", in)
		}
	}
}

// A browser is named from fixed words: whatever else it says of itself, which
// whoever signs in chooses, is not repeated.
func TestDeviceName(t *testing.T) {
	for in, want := range map[string]string{
		// Every row of the two tables.
		"Edge/1":              "Edge",
		"Edg/142":             "Edge",
		"EdgA/142":            "Edge",
		"EdgiOS/142":          "Edge",
		"OPR/120":             "Opera",
		"OPT/5":               "Opera",
		"SamsungBrowser/28.0": "Samsung Internet",
		"Firefox/145.0":       "Firefox",
		"FxiOS/145.0":         "Firefox",
		"Chrome/142.0.0.0":    "Chrome",
		"CriOS/142.0":         "Chrome",
		"Chromium/142.0":      "Chrome",
		"Safari/605.1.15":     "Safari",
		"(iPhone)":            "iPhone",
		"(iPad)":              "iPad",
		"(Android 14)":        "Android",
		"(Windows NT 10.0)":   "Windows",
		"(CrOS x86_64)":       "ChromeOS",
		"(Macintosh)":         "Mac",
		"Mac OS X 10_15_7":    "Mac",
		"(Linux x86_64)":      "Linux",
		"(X11)":               "Linux",
		// Browsers as they say it: the first row that matches is the one.
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36":                            "Chrome on Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 OPR/120.0":            "Opera on Mac",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15":                      "Safari on Mac",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1":    "Safari on iPhone",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/142.0 Mobile/15E148 Safari/604.1":     "Chrome on iPhone",
		"Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/145.0 Mobile/15E148 Safari/605.1.15":           "Firefox on iPad",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36": "Samsung Internet on Android",
		"Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0":                                                                     "Firefox on Linux",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36":                             "Chrome on ChromeOS",
		// The browser alone, the system alone, neither.
		"Firefox/145.0 (on a computer of its own)": "Firefox",
		"curl/8.5.0 (Windows)":                     "Windows",
		"":                                         "", "test browser": "", "curl/8.5.0": "",
		"Your account is at risk, call 555 0100":          "",
		"Visit https://evil.example/now, Firefox/1":       "Firefox",
		"write to help@evil.example from your iPhone":     "iPhone",
		"Chromebook/1 Firefoxy/1 MacintoshX iPhones Edge": "",
	} {
		if got := DeviceName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The web app names a session's browser with the same tables, so the screen
// and a notice say the same thing: they are read from its source and compared.
func TestDeviceNameMatchesTheWebApp(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "device.ts"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`^\[/(.+)/, '([^']+)'\],$`)
	table := func(name string) []namePattern {
		_, rest, ok := strings.Cut(string(src), "\nconst "+name+": Array<[RegExp, string]> = [\n")
		body, _, closed := strings.Cut(rest, "\n]\n")
		if !ok || !closed {
			t.Fatalf("no table %s in web/src/lib/device.ts", name)
		}
		var out []namePattern
		for _, line := range strings.Split(body, "\n") {
			m := row.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				t.Fatalf("a row of %s that is not read: %q", name, line)
			}
			out = append(out, namePattern{m[1], m[2]})
		}
		return out
	}
	for name, ours := range map[string][]namePattern{"browsers": browserNames, "systems": systemNames} {
		if theirs := table(name); !slices.Equal(theirs, ours) {
			t.Errorf("%s differ:\nweb %v\ngo  %v", name, theirs, ours)
		}
	}
}
