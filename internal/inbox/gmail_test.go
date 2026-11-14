package inbox_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	_ "unsafe" // go:linkname, for the DKIM verifier's clock

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/msgauth"
)

// Gmail's forwarding confirmation as Google sent it (ADR 0026, "Gmail's
// forwarding confirmation"): two messages received in production on
// 2026-11-14, the bytes Permaily stored and its raw route served, asked for by
// a Gmail account made only for this (permailycheck@gmail.com) for the mailbox
// yf2kwd4jkcs2e8y9yez9@inbox.permaily.com; the request was removed in Gmail
// afterwards, so their links confirm nothing. testdata/gmail holds them:
// confirmation-es.eml (Gmail in Spanish, SHA-256 5f17ad20…18a8),
// confirmation-en.eml (Gmail in English, 0c6c6e26…d444), and dkim-dns.txt, the
// TXT records of their signing keys at capture time from 1.1.1.1 and 8.8.8.8.
const (
	gmailMailbox   = "yf2kwd4jkcs2e8y9yez9@inbox.permaily.com"
	gmailRequester = "permailycheck@gmail.com"
	// gmailSigned is when Google signed them (t=); their signatures expire a
	// week later (x=), so the verifier is told it is this time.
	gmailSigned = 1794686434
)

//go:linkname dkimNow github.com/emersion/go-msgauth/dkim.now
var dkimNow func() time.Time

// atCapture runs the DKIM verifier at the time the captured messages were
// signed: their signatures carry x=, and a test must not start failing a week
// after the capture.
func atCapture(t *testing.T) {
	t.Helper()
	was := dkimNow
	dkimNow = func() time.Time { return time.Unix(gmailSigned+60, 0) }
	t.Cleanup(func() { dkimNow = was })
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/gmail/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// googleDNS answers the TXT records dkim-dns.txt holds, as DNS did when the
// messages were captured: each name's answer, the same from both resolvers.
func googleDNS(t *testing.T) msgauth.LookupTXT {
	t.Helper()
	records := map[string]string{}
	name := ""
	sc := bufio.NewScanner(strings.NewReader(fixture(t, "dkim-dns.txt")))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "$ dig +short TXT "):
			name = strings.Fields(line)[4]
		case strings.HasPrefix(line, `"`) && name != "":
			// "part one" "part two": one record, its strings joined.
			record := strings.ReplaceAll(strings.Trim(line, `"`), `" "`, "")
			if was, ok := records[name]; ok && was != record {
				t.Fatalf("two answers for %s", name)
			}
			records[name] = record
			name = ""
		}
	}
	for _, want := range []string{"20260707._domainkey.google.com", "20260707._domainkey.1e100.net"} {
		if !strings.HasPrefix(records[want], "v=DKIM1; k=rsa; p=") {
			t.Fatalf("no record for %s", want)
		}
	}
	return func(_ context.Context, name string) ([]string, error) {
		if r, ok := records[name]; ok {
			return []string{r}, nil
		}
		return nil, fmt.Errorf("no such record %s", name)
	}
}

type gmailVerdict struct {
	recognised, verified, forAnother bool
	confirmation                     *inbox.Confirmation
}

func judge(t *testing.T, dns msgauth.LookupTXT, raw, mailbox string) gmailVerdict {
	t.Helper()
	auth := msgauth.DKIM(dns)(context.Background(), []byte(raw))
	p := inbox.Parse(context.Background(), []byte(raw))
	if p.Confirmation == nil {
		if p.Meta.Confirmation {
			t.Fatal("marked a confirmation without one")
		}
		return gmailVerdict{}
	}
	if !p.Meta.Confirmation {
		t.Fatal("a confirmation not marked")
	}
	verified, other := p.Confirmation.Verdict(auth, mailbox)
	return gmailVerdict{true, verified, other, p.Confirmation}
}

var gmailFixtures = map[string]struct{ file, link string }{
	"English": {"confirmation-en.eml", "https://mail-settings.google.com/mail/vf-%5BANGjdJ8Jeg5FHoqQyZzVMdKtVq8qWqnuUVeaMhAMt6ezTaoGyN-l5g1YiBq-B9avyGs3FHu-nmW_Pq3_CHVMEJscV3OKL1Mv_RtxSZAo9VPuhme2P6b9FbjE5z4wQZs%5D-QD553HeKe4DopLZ5ESMRUQrzKZ8"},
	"Spanish": {"confirmation-es.eml", "https://mail-settings.google.com/mail/vf-%5BANGjdJ-2QCN0V3cF29b0jEg97lpixYkaS4glGDOp3LkzMrxcIRWWg5oIPgjV1nbSBDR9w_1qvExaOYa8NQwgPg0sHKBJcb2hyQFXPIu5wEKMRcQy2TKZvwXBpbL8atA%5D-p6WIW6DiI-shTnWro2LqRP_i_J4"},
}

// Google's messages as sent are recognised, with no code, their requester
// from the subject and their confirmation link from the body, and Google's
// mark is on in the release.
func TestGmailConfirmationAsGoogleSendsIt(t *testing.T) {
	atCapture(t)
	if !inbox.VerifyGmail {
		t.Error("the mark is off")
	}
	dns := googleDNS(t)
	for lang, f := range gmailFixtures {
		raw := fixture(t, f.file)
		auth := msgauth.DKIM(dns)(context.Background(), []byte(raw))
		passed := map[string]bool{}
		for _, a := range auth {
			passed[a.Domain] = a.Result == "pass"
		}
		// X-Google-DKIM-Signature (1e100.net) is not a DKIM-Signature: only
		// google.com's is checked.
		if len(auth) != 1 || !passed["google.com"] {
			t.Fatalf("%s: the signatures do not pass: %+v", lang, auth)
		}
		v := judge(t, dns, raw, gmailMailbox)
		if !v.recognised || !v.verified || v.forAnother {
			t.Errorf("%s: %+v", lang, v)
			continue
		}
		c := v.confirmation
		if c.Code != "" || c.Requester != gmailRequester || c.Link != f.link {
			t.Fatalf("%s: code %q requester %q link %q", lang, c.Code, c.Requester, c.Link)
		}
		// The same message evaluated for another mailbox is Google's for
		// another address.
		if v := judge(t, dns, raw, "someone.else@inbox.permaily.com"); !v.recognised || v.verified || !v.forAnother {
			t.Fatalf("%s for another mailbox: %+v", lang, v)
		}
	}
}

// Each altered copy of Google's messages fails the predicate.
func TestGmailConfirmationAlteredCopiesFail(t *testing.T) {
	atCapture(t)
	dns := googleDNS(t)
	header := func(raw, name, value string) string { // one more header field, before the signature
		return name + ": " + value + "\r\n" + raw
	}
	for lang, f := range gmailFixtures {
		raw := fixture(t, f.file)
		bodyAt := strings.Index(raw, "\r\n\r\n") + 4
		head, body := raw[:bodyAt], raw[bodyAt:]
		subject := "(Gmail Forwarding Confirmation - Receive Mail from " + gmailRequester
		if lang == "Spanish" {
			subject = "=?UTF-8?Q?Confirmaci=C3=B3n_de_reenv=C3=ADo_de_Gmail=3A_recibir_correo_d?=\r\n\t=?UTF-8?Q?e_permailycheck=40gmail=2Ecom?="
		}
		if !strings.Contains(head, "\r\nSubject: "+subject+"\r\n") {
			t.Fatalf("%s: the subject is not where the test expects it", lang)
		}
		requester := strings.Replace(subject, "permailycheck", "permailyother", 1)
		if lang == "Spanish" {
			requester = strings.Replace(subject, "permailycheck=40", "permailyother=40", 1)
		}
		changed := []byte(body)
		changed[len(changed)/2] ^= 1
		cases := map[string]string{
			"a second From":           header(raw, "From", "Gmail Team <forwarding-noreply@google.com>"),
			"a second To":             header(raw, "To", gmailMailbox),
			"a second Subject":        header(raw, "Subject", "(Gmail Forwarding Confirmation - Receive Mail from "+gmailRequester),
			"another To":              strings.Replace(raw, "\r\nTo: "+gmailMailbox+"\r\n", "\r\nTo: x"+gmailMailbox+"\r\n", 1),
			"a body naming another":   head + strings.Replace(body, gmailMailbox, "x"+gmailMailbox, 1),
			"another requester":       strings.Replace(raw, subject, requester, 1),
			"a byte of the body":      head + string(changed),
			"no signature of google":  strings.Replace(raw, "\r\nDKIM-Signature:", "\r\nX-Was-DKIM-Signature:", 1),
			"the signature's b= lost": strings.Replace(raw, "b=oeOKVZag8IWir1zUg1LuS2cS7MXhx", "b=AeOKVZag8IWir1zUg1LuS2cS7MXhx", 1),
		}
		if lang == "Spanish" {
			cases["the signature's b= lost"] = strings.Replace(raw, "b=nYx9OuRADpGYDgjxLYzPFBg6t8QDL", "b=AYx9OuRADpGYDgjxLYzPFBg6t8QDL", 1)
		}
		for name, altered := range cases {
			if altered == raw {
				t.Fatalf("%s, %s: not altered", lang, name)
			}
			if v := judge(t, dns, altered, gmailMailbox); v.verified || v.forAnother {
				t.Errorf("%s, %s: %+v", lang, name, v)
			}
		}
	}
}

// What Gmail sends today, signed in the test with the test key: the three
// English subjects and the Spanish one are recognised with no code and
// verified; a Spanish subject over an English body is not; another language
// with no code is not even recognised; the link is only Google's.
func TestGmailTemplatesWithoutACode(t *testing.T) {
	withVerify(t)
	english := "someone@gmail.com has requested to automatically forward mail to\r\nyour email\r\naddress " + address + ".\r\n\r\n" +
		"please click the link below to confirm the request:\r\n\r\nhttps://mail-settings.google.com/mail/vf-%5BAbC_-1%5D-xyz\r\n\r\n" +
		"click this link to cancel this verification:\r\nhttps://mail-settings.google.com/mail/uf-%5BAbC%5D-xyz\r\n"
	spanish := "someone@gmail.com ha solicitado reenviar automáticamente el\r\ncorreo a tu dirección " + address + ".\r\n\r\n" +
		"para confirmar la solicitud:\r\n\r\nhttps://mail-settings.google.com/mail/vf-%5BAbC_-1%5D-xyz\r\n"
	msg := func(subject, body string) string {
		return "From: Gmail Team <forwarding-noreply@google.com>\r\nTo: " + address + "\r\nSubject: " + subject +
			"\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	}
	const link = "https://mail-settings.google.com/mail/vf-%5BAbC_-1%5D-xyz"
	for name, raw := range map[string]string{
		"English, (#code)":   msg("(#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com", english),
		"English, (":         msg("(Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com", english),
		"English, no prefix": msg("Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com", english),
		"Spanish":            msg("=?UTF-8?Q?Confirmaci=C3=B3n_de_reenv=C3=ADo_de_Gmail=3A_recibir_correo_de_someone=40gmail=2Ecom?=", spanish),
	} {
		recognised, verified, other := verdict(t, sign(t, raw, "From", "To", "Subject"))
		if !recognised || !verified || other {
			t.Errorf("%s: recognised %v, verified %v, for another %v", name, recognised, verified, other)
		}
		p := inbox.Parse(context.Background(), []byte(raw))
		if p.Confirmation == nil || p.Confirmation.Requester != "someone@gmail.com" || p.Confirmation.Link != link {
			t.Errorf("%s: %+v", name, p.Confirmation)
		}
	}
	// A subject in one language over a body in another names no address.
	mixed := msg("Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com", spanish)
	if recognised, verified, _ := verdict(t, sign(t, mixed, "From", "To", "Subject")); !recognised || verified {
		t.Errorf("mixed languages: recognised %v, verified %v", recognised, verified)
	}
	// Another language: with a code in its subject it is recognised and never
	// verified; with none it is ordinary mail.
	french := "someone@gmail.com a demandé à transférer automatiquement les e-mails vers " + address + ".\r\n"
	coded := msg("(#123456789) Confirmation de transfert Gmail - Recevoir des e-mails de someone@gmail.com", french)
	if recognised, verified, _ := verdict(t, sign(t, coded, "From", "To", "Subject")); !recognised || verified {
		t.Errorf("another language with a code: recognised %v, verified %v", recognised, verified)
	}
	if p := inbox.Parse(context.Background(), []byte(coded)); p.Confirmation == nil || p.Confirmation.Code != "123456789" || p.Confirmation.Requester != "" {
		t.Errorf("another language with a code: %+v", p.Confirmation)
	}
	uncoded := msg("Confirmation de transfert Gmail - Recevoir des e-mails de someone@gmail.com", french)
	if recognised, _, _ := verdict(t, sign(t, uncoded, "From", "To", "Subject")); recognised {
		t.Error("another language without a code is recognised")
	}
	// The link is Google's confirmation page or nothing.
	for name, body := range map[string]string{
		"another host":    strings.ReplaceAll(english, "mail-settings.google.com", "mail-settings.google.com.example.org"),
		"http":            strings.ReplaceAll(english, "https://", "http://"),
		"the cancel link": strings.ReplaceAll(english, "/mail/vf-", "/mail/xf-"),
		"another path":    strings.ReplaceAll(english, "/mail/vf-", "/other/vf-"),
		"user info":       strings.ReplaceAll(english, "https://mail-settings", "https://x@mail-settings"),
		"no link":         strings.ReplaceAll(english, "https://", ""),
		"a link of 4 KB":  strings.Replace(english, "-xyz\r\n", "-"+strings.Repeat("a", 4096)+"\r\n", 1),
	} {
		p := inbox.Parse(context.Background(), []byte(msg("(Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com", body)))
		if p.Confirmation == nil {
			t.Fatalf("%s: not recognised", name)
		}
		if p.Confirmation.Link != "" {
			t.Errorf("%s: %q", name, p.Confirmation.Link)
		}
	}
}

// The clock that atCapture sets is the one the DKIM verifier reads: the same
// message passes at capture and fails once its x= has passed. Without this, a
// renamed variable in a new version of the library would leave dkimNow
// unused, and these tests would start failing only a week after a capture.
func TestTheDKIMClockIsTheVerifiers(t *testing.T) {
	if dkimNow == nil {
		t.Fatal("dkim.now is not linked")
	}
	dns := googleDNS(t)
	raw := fixture(t, gmailFixtures["English"].file)
	result := func() string {
		auth := msgauth.DKIM(dns)(context.Background(), []byte(raw))
		if len(auth) != 1 {
			t.Fatalf("signatures: %+v", auth)
		}
		return auth[0].Result
	}
	atCapture(t)
	if r := result(); r != "pass" {
		t.Fatalf("at capture: %s", r)
	}
	dkimNow = func() time.Time { return time.Unix(gmailSigned+8*24*3600, 0) }
	if r := result(); r == "pass" {
		t.Fatal("a signature past its x= still passes: the verifier does not read dkimNow")
	}
}

// A body that speaks of another requester than the subject names no address:
// the confirmation is recognised and never verified, not even for another
// address.
func TestGmailBodyOfAnotherRequesterIsNotVerified(t *testing.T) {
	withVerify(t)
	body := strings.Replace(template(address), "someone@gmail.com", "other@gmail.com", 1)
	raw := sign(t, confirmation("forwarding-noreply@google.com", address, body), "From", "To", "Subject")
	if recognised, verified, other := verdict(t, raw); !recognised || verified || other {
		t.Fatalf("recognised %v, verified %v, for another %v", recognised, verified, other)
	}
	// The same message with the subject's requester in the body is verified.
	raw = sign(t, confirmation("forwarding-noreply@google.com", address, template(address)), "From", "To", "Subject")
	if recognised, verified, _ := verdict(t, raw); !recognised || !verified {
		t.Fatalf("the control: recognised %v, verified %v", recognised, verified)
	}
}
