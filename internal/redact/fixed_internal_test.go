package redact

import (
	"testing"
	"unicode"
)

// The punycode a top-level domain is looked up in the public suffix list
// by: RFC 3492's samples and the list's own internationalised TLDs.
func TestPunycodeIsRFC3492s(t *testing.T) {
	for in, want := range map[string]string{
		"com":      "com",
		"中国":       "xn--fiqs8s",
		"рф":       "xn--p1ai",
		"みんな":      "xn--q9jyb4c",
		"españa":   "xn--espaa-rta",
		"ελ":       "xn--qxam",
		"भारत":     "xn--h2brj9c",
		"السعودية": "xn--mgberp4a5d4ar",
	} {
		if got, ok := punycode(in); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
	long := ""
	for range 64 {
		long += "ü"
	}
	if _, ok := punycode(long); ok {
		t.Fatal("a label of 64 runes was encoded")
	}
	for label, want := range map[string]bool{"com": true, "ＣＯＭ": true, "中国": true, "ck": true, "js": false, "": false, "⒈": false} {
		if icannTLD(label) != want {
			t.Errorf("%q: %v", label, !want)
		}
	}
}

// Finding 1: transparent is Unicode's Default_Ignorable_Code_Point (Cf,
// Other_Default_Ignorable_Code_Point and the variation selectors, less white
// space), over every rune, so no invisible rune can break a piece.
func TestFinding1TransparentIsTheDefaultIgnorableProperty(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		di := unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) && !unicode.IsSpace(r)
		if transparent(r) != di {
			t.Fatalf("%U: transparent %v, default-ignorable %v", r, transparent(r), di)
		}
	}
	for _, r := range []rune{0x17B4, 0x17B5, 0x115F, 0x1160, 0x3164, 0xFFA0, 0x034F, 0x180B, 0x180F, 0x200B, 0x2065, 0xFFF0, 0xE0000, 0xE0FFF, 0xE0100} {
		if !transparent(r) {
			t.Errorf("%U is not transparent", r)
		}
	}
}
