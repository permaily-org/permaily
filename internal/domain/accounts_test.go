package domain

import (
	"errors"
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
