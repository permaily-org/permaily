package redact

import "testing"

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
