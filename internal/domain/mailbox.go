package domain

import (
	"crypto/rand"
	"math/big"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Mailboxes (ADR 0026, "The address").

// MailboxAddressAlphabet is what a mailbox's address is made of: lower-case
// letters and digits without l, o, 0 and 1, which are read for one another.
const MailboxAddressAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// MailboxAddressLength is how many characters of MailboxAddressAlphabet a
// mailbox's local part has: 20 of 32 symbols, 100 bits.
const MailboxAddressLength = 20

// MaxLocalPart is the longest local part compared (RFC 5321).
const MaxLocalPart = 64

// NewMailboxAddress draws a new mailbox address at host, with crypto/rand.
func NewMailboxAddress(host string) string {
	b := make([]byte, MailboxAddressLength)
	n := big.NewInt(int64(len(MailboxAddressAlphabet)))
	for i := range b {
		k, _ := rand.Int(rand.Reader, n) // crypto/rand does not fail
		b[i] = MailboxAddressAlphabet[k.Int64()]
	}
	return string(b) + "@" + strings.ToLower(host)
}

// MailboxLocalPart is the part of a recipient that names a mailbox at host:
// the local part lowercased, when the address is at host (compared without
// case), is at most MaxLocalPart bytes and carries no +tag (Gmail forwards to
// the exact address it was given). ok is false for anything else, which is
// refused like an address never made.
func MailboxLocalPart(address, host string) (local string, ok bool) {
	address = strings.TrimSpace(address)
	address = strings.TrimSuffix(strings.TrimPrefix(address, "<"), ">")
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || host == "" || !strings.EqualFold(address[at+1:], host) {
		return "", false
	}
	local = strings.ToLower(address[:at])
	if len(local) > MaxLocalPart || strings.ContainsAny(local, "+\"\\ ") {
		return "", false
	}
	return local, true
}

// MailboxLabelMax is how many characters a mailbox's label has at most.
const MailboxLabelMax = 64

// MailboxLabel reports whether s can be a mailbox's label: one printable line
// of at most MailboxLabelMax characters, or nothing.
func MailboxLabel(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MailboxLabelMax {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// AttachmentFilenameMax is how many characters a downloaded attachment's
// name keeps.
const AttachmentFilenameMax = 120

// AttachmentFilename is the name an attachment is downloaded as: the name the
// message gave it, which anyone who sends mail chooses, without any path,
// control, format (bidirectional overrides among them) or line and paragraph
// separator characters, quotes or the characters file systems refuse
// (<>:|?*), trimmed of spaces and dots, at most AttachmentFilenameMax
// characters, and attachment-<index> when nothing is left (ADR 0026, "What a
// message returns"). A name that Windows reserves for a device (CON, PRN,
// AUX, NUL, COM1 to COM9 and LPT1 to LPT9, superscript digits included, in
// any case, with or without an extension) is prefixed with "_". An
// executable or a double extension ("invoice.pdf.exe") is kept as it is:
// the name is what the sender wrote, and the download is a file, never
// opened by the app.
func AttachmentFilename(name string, index int) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == utf8.RuneError || strings.ContainsRune(`"/<>:|?*`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, " .\t")
	if r := []rune(name); len(r) > AttachmentFilenameMax {
		name = strings.Trim(string(r[:AttachmentFilenameMax]), " .\t")
	}
	if windowsDevice(name) {
		// After the cut, which can leave a device's name; the "_" stays
		// when the end is cut again.
		name = "_" + name
		if r := []rune(name); len(r) > AttachmentFilenameMax {
			name = strings.Trim(string(r[:AttachmentFilenameMax]), " .\t")
		}
	}
	if name == "" {
		return "attachment-" + strconv.Itoa(index)
	}
	return name
}

// windowsDevice reports whether Windows reads name as a device: its part
// before the first dot, trailing spaces ignored, is a reserved device name.
func windowsDevice(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if r := []rune(stem); len(r) == 4 && (string(r[:3]) == "COM" || string(r[:3]) == "LPT") {
		return r[3] >= '1' && r[3] <= '9' || r[3] == '¹' || r[3] == '²' || r[3] == '³'
	}
	return false
}
