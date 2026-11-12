package domain

import (
	"strings"
	"testing"
)

func TestNewMailboxAddress(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		a := NewMailboxAddress("Inbox.Permaily.com")
		local, ok := MailboxLocalPart(a, "inbox.permaily.com")
		if !ok || len(local) != MailboxAddressLength || seen[a] || strings.Trim(local, MailboxAddressAlphabet) != "" || !strings.HasSuffix(a, "@inbox.permaily.com") {
			t.Fatal(a)
		}
		seen[a] = true
	}
}

func TestMailboxLocalPart(t *testing.T) {
	for in, want := range map[string]string{
		"Abc@INBOX.permaily.com":                        "abc",
		"<abc@inbox.permaily.com>":                      "abc",
		" abc@inbox.permaily.com ":                      "abc",
		"a@b@inbox.permaily.com":                        "a@b",
		"abc@other.com":                                 "",
		"abc+tag@inbox.permaily.com":                    "",
		"@inbox.permaily.com":                           "",
		"abc":                                           "",
		`"a b"@inbox.permaily.com`:                      "",
		strings.Repeat("a", 65) + "@inbox.permaily.com": "",
		strings.Repeat("a", 64) + "@inbox.permaily.com": strings.Repeat("a", 64),
	} {
		got, ok := MailboxLocalPart(in, "inbox.permaily.com")
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	if _, ok := MailboxLocalPart("abc@", ""); ok {
		t.Fatal("no host")
	}
}

func TestMailboxLabel(t *testing.T) {
	for in, want := range map[string]bool{
		"": true, "Work": true, "Trabajo é 日本": true, strings.Repeat("é", 64): true,
		strings.Repeat("a", 65): false, "two\nlines": false, "tab\t": false, "\xff": false,
	} {
		if MailboxLabel(in) != want {
			t.Errorf("%q", in)
		}
	}
}

func TestAttachmentFilename(t *testing.T) {
	for in, want := range map[string]string{
		"ticket.pdf":             "ticket.pdf",
		"../../etc/passwd":       "passwd",
		`C:\Users\x\evil.exe`:    "evil.exe",
		"a\x00b\u202ec.pdf":      "abc.pdf",
		`quo"te.txt`:             "quote.txt",
		"":                       "attachment-3",
		"..":                     "attachment-3",
		"dir/":                   "dir",
		"  ":                     "attachment-3",
		strings.Repeat("n", 200): strings.Repeat("n", AttachmentFilenameMax),
		"bad\xffutf8.txt":        "badutf8.txt",
	} {
		if got := AttachmentFilename(in, 3); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
