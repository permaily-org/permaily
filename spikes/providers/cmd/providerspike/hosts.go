package main

// imapHost is one entry of ADR 0042's closed list ("The closed list"), as
// documented on 2026-11-27; the spike's item 6 checks each against a test
// account before a release lists it.
type imapHost struct {
	ID, Brand, Host, FailureDomain string
}

var imapHosts = []imapHost{
	{"yahoo", "Yahoo", "imap.mail.yahoo.com", "Yahoo"},
	{"aol", "AOL", "imap.aol.com", "Yahoo"},
	{"icloud", "iCloud", "imap.mail.me.com", "Apple"},
	{"fastmail", "Fastmail", "imap.fastmail.com", "Fastmail"},
	{"gmx-com", "GMX", "imap.gmx.com", "GMX"},
	{"gmx-net", "GMX", "imap.gmx.net", "GMX"},
	{"zoho", "Zoho", "imap.zoho.com", "Zoho US"},
	{"zoho-eu", "Zoho", "imap.zoho.eu", "Zoho EU"},
	{"zoho-pro", "Zoho", "imappro.zoho.com", "Zoho US"},
}

func lookupHost(id string) (imapHost, bool) {
	for _, h := range imapHosts {
		if h.ID == id {
			return h, true
		}
	}
	return imapHost{}, false
}

// httpHosts are the destinations whose ALPN item 8 records: Google's now,
// Microsoft's only before Outlook is switched on (T201).
var (
	googleHTTPHosts    = []string{"accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com", "gmail.googleapis.com"}
	microsoftHTTPHosts = []string{"login.microsoftonline.com", "graph.microsoft.com"}
)
