package send

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/internal/webhook/webhooktest"
)

// K19: the real sender through the played vendor (Go's HTTP/2 server
// verifying as a vendor must): every call verified and taken once, also a
// retry of a notice it took, and during a rotation, when the call carries
// both keys' signatures and the vendor holds either.
func TestK19TheRealSenderReachesThePlayedVendor(t *testing.T) {
	older, err := webhook.KeyFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	seed[31] = 1
	newer, err := webhook.KeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	const subscription = "sub_0123456789abcdefghijklmnop"
	url := "https://" + host + "/hooks/permaily"
	notice := func(id string) (contracts.WebhookNotice, []byte) {
		now := time.Now().UTC()
		n := contracts.WebhookNotice{
			V: contracts.WebhookNoticeVersion, Type: contracts.NoticeMailMatched, ID: id, Subscription: subscription,
			ClientID: "https://" + host + "/oauth/client.json", Audience: url, CreatedAt: now, Since: now.Truncate(time.Minute),
		}
		b, err := webhook.Body(n)
		if err != nil {
			t.Fatal(err)
		}
		return n, b
	}
	call := func(id string, keys ...ed25519.PrivateKey) webhook.Call {
		_, body := notice(id)
		ts, sig, err := webhook.Sign(keys, subscription, id, time.Now(), body)
		if err != nil {
			t.Fatal(err)
		}
		return webhook.Call{URL: url, ID: id, Timestamp: ts, Signature: sig, Body: body, WriteBy: time.Now().Add(5 * time.Second)}
	}
	public := func(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

	cases := map[string]struct {
		holds []ed25519.PublicKey
		signs []ed25519.PrivateKey
	}{
		"one key":                        {[]ed25519.PublicKey{public(older)}, []ed25519.PrivateKey{older}},
		"a rotation, the vendor's older": {[]ed25519.PublicKey{public(older)}, []ed25519.PrivateKey{newer, older}},
		"a rotation, the vendor's newer": {[]ed25519.PublicKey{public(newer)}, []ed25519.PrivateKey{newer, older}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := &webhooktest.Vendor{Keys: c.holds, Endpoint: subscription, Audience: url}
			s, d := served(t, v)
			first := call("ntc_aaaaaaaaaaaaaaaaaaaaaaaaaa", c.signs...)
			if o := s.Send(context.Background(), first); o != webhook.OutcomeSent {
				t.Fatalf("%s", o)
			}
			// A retry of it, freshly signed: taken as the one it took.
			retry := call("ntc_aaaaaaaaaaaaaaaaaaaaaaaaaa", c.signs...)
			if o := s.Send(context.Background(), retry); o != webhook.OutcomeSent {
				t.Fatalf("retry: %s", o)
			}
			if o := s.Send(context.Background(), call("ntc_bbbbbbbbbbbbbbbbbbbbbbbbbb", c.signs...)); o != webhook.OutcomeSent {
				t.Fatalf("second: %s", o)
			}
			taken := v.Taken()
			if v.Calls() != 3 || v.Refused() != 0 || len(taken) != 2 || taken[0].ID != first.ID || taken[1].ID != "ntc_bbbbbbbbbbbbbbbbbbbbbbbbbb" {
				t.Errorf("%d calls, %d refused, taken %+v", v.Calls(), v.Refused(), taken)
			}
			d.allClosed(t)
		})
	}

	// A key the vendor does not hold: refused by it, 400, status.
	v := &webhooktest.Vendor{Keys: []ed25519.PublicKey{public(newer)}, Endpoint: subscription, Audience: url}
	s, _ := served(t, v)
	if o := s.Send(context.Background(), call("ntc_cccccccccccccccccccccccccc", older)); o != webhook.OutcomeStatus || v.Refused() != 1 {
		t.Errorf("%s, %d refused", o, v.Refused())
	}
}

// K19b: the specification's Go library at webhook.SpecRevision (run outside
// the module by testdata/v2a-go.gen.go) gives every vector of
// internal/webhook/testdata/v2a.json the JavaScript library's verdict, signs
// as it does, and verifies and reproduces the specification's example; and
// Sign writes what it writes.
func TestK19bTheGoLibraryAgreesWithTheJavaScriptOne(t *testing.T) {
	raw, err := os.ReadFile("../testdata/v2a.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		GoSource    string `json:"go_source"`
		Endpoint    string `json:"endpoint"`
		ID          string `json:"id"`
		Timestamp   string `json:"timestamp"`
		Body        string `json:"body"`
		GoSignature string `json:"go_signature"`
		Cases       []struct {
			Name      string `json:"name"`
			Signature string `json:"signature"`
			Accepts   bool   `json:"accepts"`
			GoAccepts *bool  `json:"go_accepts"`
		} `json:"cases"`
		Spec struct {
			Signature   string `json:"signature"`
			GoAccepts   bool   `json:"go_accepts"`
			GoSignature string `json:"go_signature"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.GoSource == "" || len(v.Cases) == 0 {
		t.Fatal("no verdicts of the Go library")
	}
	for _, c := range v.Cases {
		if c.GoAccepts == nil || *c.GoAccepts != c.Accepts {
			t.Errorf("%s: the Go library %v, the JavaScript one %v", c.Name, c.GoAccepts, c.Accepts)
		}
	}
	if v.GoSignature != v.Cases[0].Signature {
		t.Errorf("the Go library signs %s, the JavaScript one %s", v.GoSignature, v.Cases[0].Signature)
	}
	if !v.Spec.GoAccepts || v.Spec.GoSignature != v.Spec.Signature {
		t.Errorf("the specification's example: %+v", v.Spec)
	}
	key, err := webhook.KeyFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sec, err := strconv.ParseInt(v.Timestamp, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(sec, 0)
	if _, sig, err := webhook.Sign([]ed25519.PrivateKey{key}, v.Endpoint, v.ID, ts, []byte(v.Body)); err != nil || sig != v.GoSignature {
		t.Errorf("Sign writes %s (%v), the Go library %s", sig, err, v.GoSignature)
	}
}
