package msgauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

const message = "From: Sender <sender@example.org>\r\nTo: u@inbox.permaily.com\r\nSubject: Hi\r\nDate: Mon, 09 Nov 2026 10:00:00 +0000\r\n\r\nHello there.\r\n"

func sign(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	var out bytes.Buffer
	err := dkim.Sign(&out, strings.NewReader(message), &dkim.SignOptions{
		Domain: "example.org", Selector: "s1", Signer: priv, HeaderKeys: []string{"From", "To", "Subject", "Date"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDKIM(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	record := "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
	lookup := func(_ context.Context, name string) ([]string, error) {
		if name != "s1._domainkey.example.org" {
			t.Errorf("unexpected lookup %q", name)
		}
		return []string{record}, nil
	}
	signed := sign(t, priv)

	res := DKIM(lookup)(context.Background(), signed)
	if len(res) != 1 || res[0].Result != "pass" || res[0].Domain != "example.org" {
		t.Fatalf("valid signature: %+v", res)
	}

	tampered := bytes.Replace(signed, []byte("Hello there."), []byte("Hello thief."), 1)
	res = DKIM(lookup)(context.Background(), tampered)
	if len(res) != 1 || !strings.HasPrefix(res[0].Result, "fail") {
		t.Fatalf("tampered body: %+v", res)
	}

	if res := DKIM(lookup)(context.Background(), []byte(message)); len(res) != 0 {
		t.Fatalf("unsigned message: %+v", res)
	}
}

func TestDoH(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/dns-json" || r.URL.Query().Get("type") != "TXT" {
			http.Error(w, "bad request", 400)
			return
		}
		switch r.URL.Query().Get("name") {
		case "s1._domainkey.example.org":
			_, _ = w.Write([]byte(`{"Status":0,"Answer":[{"type":5,"data":"cname.example."},{"type":16,"data":"\"v=DKIM1; p=abc\" \"def\\\"g\""}]}`))
		default:
			_, _ = w.Write([]byte(`{"Status":3}`))
		}
	}))
	defer srv.Close()
	d := DoH{Client: srv.Client(), Endpoint: srv.URL}
	got, err := d.LookupTXT(context.Background(), "s1._domainkey.example.org")
	if err != nil || len(got) != 1 || got[0] != `v=DKIM1; p=abcdef"g` {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := d.LookupTXT(context.Background(), "missing.example.org"); err == nil {
		t.Fatal("NXDOMAIN not reported")
	}
}
