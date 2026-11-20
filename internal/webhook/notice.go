package webhook

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
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

// The signature's profile (design review, finding 7): Standard Webhooks'
// asymmetric signatures as the specification says at SpecRevision, "v2a":
// Ed25519 (pure) over the SHA-256 digest of
// "endpoint_id.msg_id.timestamp.payload", the endpoint's identifier being
// the subscription's (WebhookNotice.Subscription, given to the vendor in
// GET /v1/connection and never in a call), so one key serves every
// subscription and a notice signed for one fails for every other. The
// libraries the specification publishes at that revision verify a notice
// with no code of ours (testdata/v2a.json, computed by its JavaScript
// library, TestANoticeVerifiesWithTheStandardsLibraries).
const (
	SignatureVersion = "v2a"
	// SpecRevision is the commit of github.com/standard-webhooks/standard-webhooks
	// whose spec/standard-webhooks.md this package follows.
	SpecRevision = "3d65b1824b6697f05a5a4584e7062bd23512fc8e"
	// PublicKeyPrefix is how the specification writes a public key for its
	// libraries ("whpk_" and the key in base64): LibraryKey.
	PublicKeyPrefix = "whpk_"
)

// signed is the digest a signature covers: SHA-256 of the endpoint's, the
// notice's and the timestamp's text and the body, joined by dots. None of
// the three may hold a dot, or two different triples could join the same.
func signed(endpoint, id, timestamp string, body []byte) ([32]byte, bool) {
	if endpoint == "" || id == "" || timestamp == "" ||
		strings.Contains(endpoint, ".") || strings.Contains(id, ".") || strings.Contains(timestamp, ".") {
		return [32]byte{}, false
	}
	h := sha256.New()
	for _, part := range []string{endpoint, ".", id, ".", timestamp, "."} {
		h.Write([]byte(part))
	}
	h.Write(body)
	var d [32]byte
	h.Sum(d[:0])
	return d, true
}

// Sign is the webhook-timestamp and webhook-signature headers of a call of
// notice id, for the subscription endpoint, with body, at at: one "v2a,"
// signature (Ed25519 of the digest, base64) by every key, separated by
// spaces, so a vendor still holding only the older of two keys during a
// rotation verifies it. An endpoint or identifier with a dot signs nothing.
func Sign(keys []ed25519.PrivateKey, endpoint, id string, at time.Time, body []byte) (timestamp, signature string, err error) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	d, ok := signed(endpoint, id, timestamp, body)
	if !ok || len(keys) == 0 {
		return "", "", ErrNotice
	}
	sigs := make([]string, 0, len(keys))
	for _, k := range keys {
		if len(k) != ed25519.PrivateKeySize {
			return "", "", ErrNotice
		}
		sigs = append(sigs, SignatureVersion+","+base64.StdEncoding.EncodeToString(ed25519.Sign(k, d[:])))
	}
	return timestamp, strings.Join(sigs, " "), nil
}

// Verify is what a vendor does with a call (and what the played vendor of
// the tests does), as the specification's libraries do: the timestamp
// within TimestampTolerance of now, and one "v2a," signature of the digest
// for endpoint (the subscription the vendor was given, never one taken from
// the call) that one of keys verifies. A vendor then opens the body (Open)
// and takes each identifier once, for DedupKeepFor.
func Verify(keys []ed25519.PublicKey, endpoint, id, timestamp, signature string, body []byte, now time.Time) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(sec, 10) != timestamp {
		return ErrVerify
	}
	if d := now.Sub(time.Unix(sec, 0)); d > TimestampTolerance || d < -TimestampTolerance {
		return ErrVerify
	}
	d, ok := signed(endpoint, id, timestamp, body)
	if !ok {
		return ErrVerify
	}
	for _, s := range strings.Split(signature, " ") {
		v, ok := strings.CutPrefix(s, SignatureVersion+",")
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.Strict().DecodeString(v)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		for _, k := range keys {
			if ed25519.Verify(k, d[:], sig) {
				return nil
			}
		}
	}
	return ErrVerify
}

// Open reads a verified body as a vendor does: one object read exactly
// (jws.Object) with no member but WebhookNotice's, each of its type, of a
// version it knows, whose identifier is the webhook-id header's, whose
// audience is the URL the vendor was called on, and made within NoticeLife
// before now (and not more than TimestampTolerance after it). A notice for
// another URL is refused: a vendor never takes one meant for someone else;
// one older than any retry is refused, so a vendor keeps identifiers only
// DedupKeepFor.
func Open(body []byte, id, audience string, now time.Time) (*contracts.WebhookNotice, error) {
	if len(body) > NoticeBytesMax {
		return nil, ErrNotice
	}
	m, ok := members(body, "v", "type", "id", "subscription", "client_id", "aud", "created_at", "since", "scheduled_at", "late")
	if !ok || string(m["v"]) != strconv.Itoa(contracts.WebhookNoticeVersion) {
		return nil, ErrNotice
	}
	n := contracts.WebhookNotice{V: contracts.WebhookNoticeVersion}
	var typ string
	for name, to := range map[string]*string{"type": &typ, "id": &n.ID, "subscription": &n.Subscription, "client_id": &n.ClientID, "aud": &n.Audience} {
		if *to, ok = jws.Text(m[name]); !ok {
			return nil, ErrNotice
		}
	}
	n.Type = contracts.WebhookNoticeType(typ)
	for name, to := range map[string]*time.Time{"created_at": &n.CreatedAt, "since": &n.Since, "scheduled_at": &n.ScheduledAt} {
		v, there := m[name]
		if !there && name == "scheduled_at" {
			continue
		}
		text, ok := jws.Text(v)
		t, err := time.Parse(time.RFC3339Nano, text)
		if !ok || err != nil {
			return nil, ErrNotice
		}
		*to = t.UTC()
	}
	if v, there := m["late"]; there {
		if n.Late, ok = boolean(v); !ok {
			return nil, ErrNotice
		}
	}
	if n.ID != id || n.Audience != audience ||
		now.Sub(n.CreatedAt) > NoticeLife || n.CreatedAt.Sub(now) > TimestampTolerance {
		return nil, ErrNotice
	}
	// What Body would not have sent is not a notice either.
	if again, err := Body(n); err != nil || !bytes.Equal(again, body) {
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
// published one with /status's; a vendor reads it to verify): one object
// read exactly with "keys" alone, a list of at most 8 keys, each an object
// read exactly with KeySet's members, each a string.
func ParseKeySet(b []byte) (map[string]ed25519.PublicKey, error) {
	set, ok := members(b, "keys")
	if !ok {
		return nil, ErrVerify
	}
	var list []json.RawMessage
	if v := set["keys"]; len(v) == 0 || v[0] != '[' || json.Unmarshal(v, &list) != nil || len(list) == 0 || len(list) > keySetMax {
		return nil, ErrVerify
	}
	keys := map[string]ed25519.PublicKey{}
	for _, raw := range list {
		m, ok := members(raw, "kty", "crv", "kid", "use", "alg", "x")
		if !ok {
			return nil, ErrVerify
		}
		var k jwk
		for name, to := range map[string]*string{"kty": &k.Kty, "crv": &k.Crv, "kid": &k.Kid, "use": &k.Use, "alg": &k.Alg, "x": &k.X} {
			if *to, ok = jws.Text(m[name]); !ok {
				return nil, ErrVerify
			}
		}
		x, err := base64.RawURLEncoding.Strict().DecodeString(k.X)
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

// keySetMax bounds the keys a set holds: two during a rotation, room for
// more.
const keySetMax = 8

// LibraryKey is a public key as the specification's libraries take it
// ("whpk_" and the key in base64): what the developers' page tells a vendor
// to give them, the key set's "x" written another way.
func LibraryKey(k ed25519.PublicKey) string {
	return PublicKeyPrefix + base64.StdEncoding.EncodeToString(k)
}
