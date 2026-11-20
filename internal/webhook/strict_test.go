package webhook

import (
	"strings"
	"testing"
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
	return Open(body, id, audience)
}
