// Adds the Standard Webhooks Go library's verdicts to ADR 0039's v2a
// vectors (internal/webhook/testdata/v2a.json, K19b), as v2a.gen.ts computed
// the JavaScript library's: nothing of Permaily's code is used. To run it
// again: clone github.com/standard-webhooks/standard-webhooks at
// 3d65b1824b6697f05a5a4584e7062bd23512fc8e into ../sw, copy this file to
// ../sw/libraries/permaily-k19b/main.go, and in ../sw/libraries run
// `go run ./permaily-k19b < v2a.json > v2a.json.new` on the vectors, then
// put v2a.json.new in their place. It reads the vectors as v2a.gen.ts wrote
// them and writes them back with go_source, each case's go_accepts, the Go
// library's signature of the first case (go_signature) and the
// specification's example verified and signed by it (spec.go_accepts,
// spec.go_signature).
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
)

type vectors struct {
	Source      string            `json:"source"`
	GoSource    string            `json:"go_source"`
	PublicKeys  map[string]string `json:"public_keys"`
	Endpoint    string            `json:"endpoint"`
	ID          string            `json:"id"`
	Timestamp   string            `json:"timestamp"`
	Body        string            `json:"body"`
	GoSignature string            `json:"go_signature"`
	Cases       []struct {
		Name      string   `json:"name"`
		Signature string   `json:"signature"`
		Accepts   bool     `json:"accepts"`
		GoAccepts bool     `json:"go_accepts"`
		Keys      []string `json:"keys,omitempty"`
	} `json:"cases"`
	Spec struct {
		SecretKey   string `json:"secret_key"`
		PublicKey   string `json:"public_key"`
		Endpoint    string `json:"endpoint"`
		ID          string `json:"id"`
		Timestamp   string `json:"timestamp"`
		Body        string `json:"body"`
		Signature   string `json:"signature"`
		GoAccepts   bool   `json:"go_accepts"`
		GoSignature string `json:"go_signature"`
	} `json:"spec"`
}

// accepts is the Go library's verdict. Its timestamp is not what is tested
// (the vectors are checked at their own), as in v2a.gen.ts.
func accepts(endpoint string, keys []string, id, timestamp, signature, body string) bool {
	wh, err := standardwebhooks.NewWebhookAsymmetric(endpoint, keys)
	if err != nil {
		return false
	}
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", timestamp)
	h.Set("webhook-signature", signature)
	return wh.VerifyIgnoringTimestamp([]byte(body), h) == nil
}

func sign(secret, endpoint, id, timestamp, body string) string {
	s, err := standardwebhooks.NewWebhookAsymmetricSigner(secret)
	if err != nil {
		panic(err)
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		panic(err)
	}
	sig, err := s.Sign(endpoint, id, time.Unix(ts, 0), []byte(body))
	if err != nil {
		panic(err)
	}
	return sig
}

func main() {
	var v vectors
	if err := json.NewDecoder(os.Stdin).Decode(&v); err != nil {
		panic(err)
	}
	v.GoSource = "github.com/standard-webhooks/standard-webhooks libraries/go at 3d65b1824b6697f05a5a4584e7062bd23512fc8e, run by v2a-go.gen.go (ADR 0039, K19b)"
	for i, c := range v.Cases {
		keys := []string{v.PublicKeys["wk1"]}
		if c.Keys != nil {
			keys = nil
			for _, k := range c.Keys {
				keys = append(keys, v.PublicKeys[k])
			}
		}
		v.Cases[i].GoAccepts = accepts(v.Endpoint, keys, v.ID, v.Timestamp, c.Signature, v.Body)
	}
	// The first case's key: the seed of zeros, as v2a.gen.ts's signerA.
	v.GoSignature = sign("whsk_"+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", v.Endpoint, v.ID, v.Timestamp, v.Body)
	s := &v.Spec
	s.GoAccepts = accepts(s.Endpoint, []string{s.PublicKey}, s.ID, s.Timestamp, s.Signature, s.Body)
	s.GoSignature = sign(s.SecretKey, s.Endpoint, s.ID, s.Timestamp, s.Body)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	if _, err := os.Stdout.Write(out.Bytes()); err != nil {
		panic(err)
	}
}
