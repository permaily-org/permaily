package webhook

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The design review of ADR 0039, finding 10: encoding/json alone takes a
// stray closing delimiter after the value (Decoder.More looks only inside an
// array or object), matches member names in any case, and lets the last of
// two members with one name win. Every parser of this package reads one
// object exactly (internal/jws.Object): names matched byte for byte, a name
// twice in any case refused, nothing after the value, each member's type
// checked (K1, K7, K8).
func TestTheParsersReadOneObjectExactly(t *testing.T) {
	const url = `"url":"https://trip-app.com/h"`
	for _, raw := range []string{
		`{` + url + `,"new_mail":true}}`,                                     // a stray closing brace
		`{` + url + `,"new_mail":true}]`,                                     // a stray closing bracket
		`{` + url + `,"new_mail":false,"NEW_MAIL":true}`,                     // a case-folded name, later
		`{"URL":"https://trip-app.com/h","new_mail":true}`,                   // a case-folded name alone
		`{` + url + `,"new_mail":true,"new_mail":true}`,                      // a member twice
		`{` + url + `,"url":"https://trip-app.com/x","new_mail":true}`,       // the URL twice
		`{` + url + `,"new_mail":null}`,                                      // null is no boolean
		`{` + url + `,"new_mail":"true"}`,                                    // nor is a string
		`{` + url + `,"new_mail":1}`,                                         // nor a number
		`{"url":null,"new_mail":true}`,                                       // a URL that is not a string
		`{` + url + `,"schedule":{"every":"day","at":"08:00","AT":"09:00"}}`, // inside the schedule
		`{` + url + `,"schedule":{"every":"day","at":"08:00","at":"09:00"}}`,
		`{` + url + `,"schedule":{"Every":"day","at":"08:00"}}`,
		`{` + url + `,"schedule":null}`,
		`{` + url + `,"schedule":{"every":"day","at":"08:00"}}}`,
		"{" + url + ",\"new_mail\":true,\"x\xff\":1}", // not UTF-8
	} {
		if _, err := ParseRequest(raw, client, "permaily.com"); err == nil {
			t.Errorf("ParseRequest took %q", raw)
		}
	}

	n := notice()
	body, err := Body(n)
	if err != nil {
		t.Fatal(err)
	}
	b := string(body)
	for name, bad := range map[string]string{
		"a stray brace":     b + "}",
		"a case-folded aud": strings.Replace(b, `"aud"`, `"AUD"`, 1),
		"aud twice":         strings.Replace(b, `{`, `{"aud":"https://trip-app.com/other",`, 1),
		"a version as 1.0":  strings.Replace(b, `"v":1`, `"v":1.0`, 1),
		"late as a string":  strings.Replace(b, `}`, `,"late":"true"}`, 1),
	} {
		if _, err := openNotice([]byte(bad), n.ID, n.Audience); err == nil {
			t.Errorf("Open took %s: %s", name, bad)
		}
	}

	const key = `{"kty":"OKP","crv":"Ed25519","kid":"wk1","use":"sig","alg":"EdDSA","x":"O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik"}`
	for name, bad := range map[string]string{
		"a stray bracket": `{"keys":[` + key + `]}]`,
		"a case-folded x": `{"keys":[` + strings.Replace(key, `"x"`, `"X"`, 1) + `]}`,
		"keys twice":      `{"keys":[],"keys":[` + key + `]}`,
		"a member twice":  `{"keys":[` + strings.Replace(key, `"kid":"wk1"`, `"kid":"wk1","kid":"wk2"`, 1) + `]}`,
		"KEYS":            `{"KEYS":[` + key + `]}`,
	} {
		if _, err := ParseKeySet([]byte(bad)); err == nil {
			t.Errorf("ParseKeySet took %s", name)
		}
	}
}

// openNotice is Open as the vendor calls it at the notice's own time.
func openNotice(body []byte, id, audience string) (any, error) {
	return Open(body, id, audience, notice().CreatedAt)
}

// Every member of a notice is read with its type, and only a body Body
// would have written opens: a vendor and Permaily read the same notice.
func TestANoticeOpensOnlyAsBodyWroteIt(t *testing.T) {
	n := notice()
	n.Type, n.ScheduledAt, n.Late = contracts.NoticeScheduleDue, n.Since, true
	body, err := Body(n)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(body, n.ID, n.Audience, n.CreatedAt); err != nil || *got != n {
		t.Fatalf("a scheduled notice: %+v %v", got, err)
	}
	b := string(body)
	for name, bad := range map[string]string{
		"no version":              strings.Replace(b, `"v":1,`, ``, 1),
		"a type that is a number": strings.Replace(b, `"type":"schedule.due"`, `"type":1`, 1),
		"a time that is no time":  strings.Replace(b, `"since":"2026-11-20T08:03:00Z"`, `"since":"yesterday"`, 1),
		"a time in another zone":  strings.Replace(b, `"since":"2026-11-20T08:03:00Z"`, `"since":"2026-11-20T09:03:00+01:00"`, 1),
		"a scheduled time absent": strings.Replace(b, `,"scheduled_at":"2026-11-20T08:03:00Z"`, ``, 1),
		"late written false":      strings.Replace(b, `"late":true`, `"late":false`, 1),
		"spaces":                  strings.Replace(b, `,`, `, `, 1),
		"too long":                b + strings.Repeat(" ", NoticeBytesMax),
	} {
		if _, err := Open([]byte(bad), n.ID, n.Audience, n.CreatedAt); !errors.Is(err, ErrNotice) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A signature is read as the libraries read it: one that is not base64, or
// not 64 bytes, is skipped, and another of the header may still verify.
func TestASignatureNotOfTheProfileIsSkipped(t *testing.T) {
	k, _ := KeyFromSeed(make([]byte, 32))
	pub := []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}
	n := notice()
	body, _ := Body(n)
	ts, sig, _ := Sign([]ed25519.PrivateKey{k}, endpoint, n.ID, n.CreatedAt, body)
	for _, junk := range []string{"v2a,not base64!", "v2a,AAAA", "v2a," + strings.Repeat("A", 88)} {
		if err := Verify(pub, endpoint, n.ID, ts, junk, body, n.CreatedAt); !errors.Is(err, ErrVerify) {
			t.Errorf("%q: %v", junk, err)
		}
		if err := Verify(pub, endpoint, n.ID, ts, junk+" "+sig, body, n.CreatedAt); err != nil {
			t.Errorf("%q then a good one: %v", junk, err)
		}
	}
	if err := Verify(pub, endpoint, "ntc_a.b", ts, sig, body, n.CreatedAt); !errors.Is(err, ErrVerify) {
		t.Errorf("an identifier with a dot: %v", err)
	}
}

// What ParseRequest takes once read exactly is still checked as before: the
// URL on the vendor's host, the schedule one of the choices, a weekday only
// for a week.
func TestAWebhookRequestReadExactlyIsStillChecked(t *testing.T) {
	for _, raw := range []string{
		`{"url":"https://other.com/h","new_mail":true}`,
		`{"url":"https://trip-app.com/h","schedule":{"every":"day","at":"08:10"}}`,
		`{"url":"https://trip-app.com/h","schedule":{"every":"week","on":"","at":"08:00"}}`,
		`{"url":"https://trip-app.com/h","schedule":{"every":"week","on":1,"at":"08:00"}}`,
		`{"url":"https://trip-app.com/h","schedule":{"every":"day"}}`,
		`{"url":"https://trip-app.com/h","schedule":{"at":"08:00"}}`,
		`{"url":"https://trip-app.com/h","schedule":[]}`,
	} {
		if _, err := ParseRequest(raw, client, "permaily.com"); err == nil {
			t.Errorf("took %s", raw)
		}
	}
	r, err := ParseRequest(`{"url":"https://trip-app.com/h","new_mail":false,"schedule":{"every":"week","on":"monday","at":"08:00"}}`, client, "permaily.com")
	if err != nil || r.NewMail || r.Schedule.On != "monday" {
		t.Fatalf("%+v %v", r, err)
	}
}
