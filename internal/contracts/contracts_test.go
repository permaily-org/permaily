package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrors(t *testing.T) {
	wrapped := fmt.Errorf("finishing: %w", &Error{Code: "invalid_code", Message: "another wording"})
	if !errors.Is(wrapped, ErrInvalidCode) {
		t.Error("an error with the same code is not matched")
	}
	if errors.Is(wrapped, ErrUnauthorized) || errors.Is(errors.New("invalid_code"), ErrInvalidCode) {
		t.Error("another error is matched")
	}
	if got := ErrInvalidCode.Error(); got != "invalid_code: that code or link is not valid any more; ask for a new one" {
		t.Errorf("message: %q", got)
	}
}

// Binary values travel as base64url without padding, in both directions.
func TestBytes(t *testing.T) {
	out, err := json.Marshal(RequestOptions{Challenge: Bytes{0xfb, 0xff, 0xfe, 0x01}, RPID: "app.permaily.test"})
	if err != nil || !strings.Contains(string(out), `"challenge":"-__-AQ"`) || !strings.Contains(string(out), `"rpId":"app.permaily.test"`) {
		t.Fatalf("written: %s %v", out, err)
	}
	var back RequestOptions
	if err := json.Unmarshal(out, &back); err != nil || back.Challenge.String() != "-__-AQ" {
		t.Fatalf("read back: %+v %v", back, err)
	}
	// Padding, the other alphabet and anything that is not a string are refused.
	for _, bad := range []string{`"-__-AQ=="`, `"+//+AQ"`, `"-__-A"`, `[251,255]`, `7`} {
		if err := json.Unmarshal([]byte(`{"challenge":`+bad+`}`), &back); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
	if out, _ := json.Marshal(Passkey{}); !strings.Contains(string(out), `"id":""`) {
		t.Errorf("no bytes: %s", out)
	}
}

// What a browser's toJSON() gives is read whatever else it carries, even by
// a reader that refuses fields it does not know.
func TestCredentials(t *testing.T) {
	strict := func(body string, v any) error {
		dec := json.NewDecoder(strings.NewReader(body))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	var made PasskeyFinish
	err := strict(`{"ceremony":"cer_x","name":"Laptop","credential":{"id":"AQID","rawId":"AQID","type":"public-key","authenticatorAttachment":"platform",
		"response":{"clientDataJSON":"e30","attestationObject":"oA","transports":["internal"],"publicKeyAlgorithm":-7},"clientExtensionResults":{"credProps":{"rk":true}}}}`, &made)
	if err != nil || made.Name != "Laptop" || made.Credential.RawID.String() != "AQID" ||
		string(made.Credential.Response.ClientDataJSON) != "{}" || made.Credential.Response.AttestationObject.String() != "oA" {
		t.Fatalf("a credential that was made: %+v %v", made, err)
	}
	var used PasskeySignIn
	err = strict(`{"ceremony":"cer_x","credential":{"id":"AQID","rawId":"AQID","type":"public-key",
		"response":{"clientDataJSON":"e30","authenticatorData":"BA","signature":"BQ","userHandle":"YWNj","more":1},"clientExtensionResults":{}}}`, &used)
	r := used.Credential.Response
	if err != nil || used.Credential.RawID.String() != "AQID" || r.AuthenticatorData.String() != "BA" || r.Signature.String() != "BQ" || string(r.UserHandle) != "acc" {
		t.Fatalf("a credential that was used: %+v %v", used, err)
	}
	// The request around the credential stays strict, and a credential that
	// is not one is refused.
	for _, bad := range []string{`{"ceremony":"cer_x","credential":{},"admin":true}`, `{"credential":{"rawId":"+"}}`, `{"credential":7}`} {
		if strict(bad, &made) == nil || strict(bad, &used) == nil {
			t.Errorf("%s was read", bad)
		}
	}
}
