package webhook

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Body is a notice's body as it is sent and signed: its JSON, at most
// NoticeBytesMax, after checking that it holds what ADR 0039 says and
// nothing else can be put in it: the version, a known type, identifiers
// with their prefixes, the client and the audience (the URL called), and
// times in UTC, Since on a minute.
func Body(n contracts.WebhookNotice) ([]byte, error) {
	if n.V != contracts.WebhookNoticeVersion ||
		!strings.HasPrefix(n.ID, NoticePrefix) || !strings.HasPrefix(n.Subscription, SubscriptionPrefix) ||
		n.ClientID == "" || n.Audience == "" || n.CreatedAt.IsZero() || n.Since.IsZero() ||
		n.CreatedAt.Location() != time.UTC || n.Since.Location() != time.UTC || !n.Since.Equal(n.Since.Truncate(time.Minute)) {
		return nil, ErrNotice
	}
	switch n.Type {
	case contracts.NoticeMailMatched:
		if !n.ScheduledAt.IsZero() || n.Late {
			return nil, ErrNotice
		}
	case contracts.NoticeScheduleDue:
		if n.ScheduledAt.IsZero() || n.ScheduledAt.Location() != time.UTC {
			return nil, ErrNotice
		}
	default:
		return nil, ErrNotice
	}
	b, err := json.Marshal(n)
	if err != nil || len(b) > NoticeBytesMax {
		return nil, ErrNotice
	}
	return b, nil
}

// signed is what a signature covers (Standard Webhooks): the notice's
// identifier, the timestamp in Unix seconds and the body, joined by dots.
func signed(id, timestamp string, body []byte) []byte {
	b := make([]byte, 0, len(id)+len(timestamp)+len(body)+2)
	b = append(b, id...)
	b = append(b, '.')
	b = append(b, timestamp...)
	b = append(b, '.')
	return append(b, body...)
}

// Sign is the webhook-timestamp and webhook-signature headers of a call of
// notice id with body at at: one "v1a," signature (Ed25519, base64) by
// every key, separated by spaces, so a vendor still holding only the older
// of two keys during a rotation verifies it.
func Sign(keys []ed25519.PrivateKey, id string, at time.Time, body []byte) (timestamp, signature string) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	msg := signed(id, timestamp, body)
	sigs := make([]string, 0, len(keys))
	for _, k := range keys {
		sigs = append(sigs, "v1a,"+base64.StdEncoding.EncodeToString(ed25519.Sign(k, msg)))
	}
	return timestamp, strings.Join(sigs, " ")
}

// Verify is what a vendor does with a call (and what the played vendor of
// the tests does): the timestamp within TimestampTolerance of now, and one
// "v1a," signature of the identifier, the timestamp and the body that one
// of keys verifies. A vendor then opens the body (Open) and takes each
// identifier once.
func Verify(keys []ed25519.PublicKey, id, timestamp, signature string, body []byte, now time.Time) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || id == "" {
		return ErrVerify
	}
	if d := now.Sub(time.Unix(sec, 0)); d > TimestampTolerance || d < -TimestampTolerance {
		return ErrVerify
	}
	msg := signed(id, timestamp, body)
	for _, s := range strings.Fields(signature) {
		v, ok := strings.CutPrefix(s, "v1a,")
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			continue
		}
		for _, k := range keys {
			if ed25519.Verify(k, msg, sig) {
				return nil
			}
		}
	}
	return ErrVerify
}

// Open reads a verified body as a vendor does: one notice with no member
// but WebhookNotice's, of a version it knows, whose identifier is the
// webhook-id header's and whose audience is the URL the vendor was called
// on. A notice for another URL is refused: a vendor never takes one meant
// for someone else.
func Open(body []byte, id, audience string) (*contracts.WebhookNotice, error) {
	if len(body) > NoticeBytesMax {
		return nil, ErrNotice
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var n contracts.WebhookNotice
	if err := d.Decode(&n); err != nil || d.More() {
		return nil, ErrNotice
	}
	if n.V != contracts.WebhookNoticeVersion || n.ID != id || n.Audience != audience {
		return nil, ErrNotice
	}
	return &n, nil
}

// KeyFromSeed is the signing key of a 32-byte seed (Keys.Derive(KeyPurpose)).
func KeyFromSeed(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, ErrVerify
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// jwk is one public key as a JWK (RFC 8037).
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
}

// KeySet is the JWK set the api host publishes at KeysPath and /status
// reports: every key a notice may be signed with now, by identifier, in
// order.
func KeySet(keys map[string]ed25519.PublicKey) ([]byte, error) {
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	set := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for _, id := range ids {
		k := keys[id]
		if len(k) != ed25519.PublicKeySize || id == "" {
			return nil, ErrVerify
		}
		set.Keys = append(set.Keys, jwk{Kty: "OKP", Crv: "Ed25519", Kid: id, Use: "sig", Alg: "EdDSA", X: base64.RawURLEncoding.EncodeToString(k)})
	}
	return json.Marshal(set)
}

// ParseKeySet reads a JWK set KeySet wrote (permaily-verify compares the
// published one with /status's; a vendor reads it to verify).
func ParseKeySet(b []byte) (map[string]ed25519.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&set); err != nil || d.More() || len(set.Keys) == 0 {
		return nil, ErrVerify
	}
	keys := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || k.Kty != "OKP" || k.Crv != "Ed25519" || k.Alg != "EdDSA" || k.Use != "sig" || k.Kid == "" || len(x) != ed25519.PublicKeySize {
			return nil, ErrVerify
		}
		if _, dup := keys[k.Kid]; dup {
			return nil, ErrVerify
		}
		keys[k.Kid] = ed25519.PublicKey(x)
	}
	return keys, nil
}
