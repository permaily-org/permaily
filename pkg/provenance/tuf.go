package provenance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Sigstore publishes its trust root through a TUF repository
// (https://tuf-repo-cdn.sigstore.dev, The Update Framework): root metadata
// names the keys of five keyholders, three of whom must sign a change, and
// targets metadata, signed the same way, names the hash of trusted_root.json.
// Each root is signed by the keys of the one before, so from one root every
// later one can be checked.
//
// This file checks that chain and nothing else of TUF. The timestamp and
// snapshot roles say which metadata is the latest, which only something that
// fetches from the repository can use; here the files are handed over, and a
// version that is not newer than the one in use is refused.

const (
	// trustedRootTarget is the name of the trust root in the targets metadata.
	trustedRootTarget = "trusted_root.json"

	maxTUFMetadata = 256 << 10
	maxTUFRoots    = 64
)

// Publication says which of the trust roots Sigstore published a TrustRoot is.
type Publication struct {
	// SHA256 is the hash of trusted_root.json, in hex.
	SHA256 string
	// RootVersion and TargetsVersion are the versions of the TUF metadata that
	// vouch for it.
	RootVersion, TargetsVersion int
}

// Update is a trust root newer than the one in use, with what proves that
// Sigstore published it.
type Update struct {
	// Roots are the TUF root metadata published after the one in use, each
	// version in order; empty when there is none.
	Roots [][]byte
	// Targets is the TUF targets metadata that names TrustedRoot.
	Targets []byte
	// TrustedRoot is trusted_root.json.
	TrustedRoot []byte
}

type tufKey struct {
	KeyType string `json:"keytype"`
	Scheme  string `json:"scheme"`
	KeyVal  struct {
		Public string `json:"public"`
	} `json:"keyval"`
}

type tufRole struct {
	KeyIDs    []string `json:"keyids"`
	Threshold int      `json:"threshold"`
}

type tufRoot struct {
	Type        string             `json:"_type"`
	SpecVersion string             `json:"spec_version"`
	Version     int                `json:"version"`
	Expires     time.Time          `json:"expires"`
	Keys        map[string]tufKey  `json:"keys"`
	Roles       map[string]tufRole `json:"roles"`
}

type tufTargets struct {
	Type        string    `json:"_type"`
	SpecVersion string    `json:"spec_version"`
	Version     int       `json:"version"`
	Expires     time.Time `json:"expires"`
	Targets     map[string]struct {
		Length int64             `json:"length"`
		Hashes map[string]string `json:"hashes"`
	} `json:"targets"`
}

// tufMetadata is a signed document: what is signed, in the bytes the
// signatures cover, and the signatures.
type tufMetadata struct {
	canonical  []byte
	signatures []struct {
		KeyID string `json:"keyid"`
		Sig   string `json:"sig"`
	}
}

func parseTUF(doc []byte, into any) (*tufMetadata, error) {
	if len(doc) > maxTUFMetadata {
		return nil, fmt.Errorf("%w: TUF metadata too large", ErrMalformed)
	}
	var outer struct {
		Signatures []struct {
			KeyID string `json:"keyid"`
			Sig   string `json:"sig"`
		} `json:"signatures"`
		Signed json.RawMessage `json:"signed"`
	}
	if err := json.Unmarshal(doc, &outer); err != nil {
		return nil, fmt.Errorf("%w: TUF metadata: %w", ErrMalformed, err)
	}
	canonical, err := canonicalJSON(outer.Signed)
	if err != nil {
		return nil, fmt.Errorf("%w: TUF metadata: %w", ErrMalformed, err)
	}
	if err := json.Unmarshal(outer.Signed, into); err != nil {
		return nil, fmt.Errorf("%w: TUF metadata: %w", ErrMalformed, err)
	}
	return &tufMetadata{canonical: canonical, signatures: outer.Signatures}, nil
}

var integer = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// canonicalJSON re-encodes a JSON value the way TUF signs it (the canonical
// JSON of OLPC): object keys in order, no space, integers only, and strings
// with nothing escaped but the quote and the backslash.
func canonicalJSON(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, v, 0); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, v any, depth int) error {
	if depth > 32 {
		return fmt.Errorf("JSON nested too deep")
	}
	quote := func(s string) {
		out.WriteByte('"')
		out.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		out.WriteByte('"')
	}
	switch v := v.(type) {
	case bool:
		fmt.Fprint(out, v)
	case json.Number:
		if !integer.MatchString(v.String()) {
			return fmt.Errorf("a number that is not an integer: %s", v)
		}
		out.WriteString(v.String())
	case string:
		quote(v)
	case []any:
		out.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, e, depth+1); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			quote(k)
			out.WriteByte(':')
			if err := writeCanonical(out, v[k], depth+1); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		// The decoder gives nothing else but nil.
		out.WriteString("null")
	}
	return nil
}

// signedBy reports whether enough of the keys that root gives the role signed
// the metadata. A key counts once, however many times it signed or is listed.
func (m *tufMetadata) signedBy(root *tufRoot, role string) error {
	r, ok := root.Roles[role]
	if !ok || r.Threshold < 1 {
		return fmt.Errorf("%w: TUF root %d names no %s role", ErrUntrusted, root.Version, role)
	}
	digest := sha256.Sum256(m.canonical)
	counted := map[string]bool{}
	for _, s := range m.signatures {
		if !slices.Contains(r.KeyIDs, s.KeyID) {
			continue
		}
		key, der := root.key(s.KeyID)
		sig, err := hex.DecodeString(s.Sig)
		if key == nil || err != nil || !ecdsa.VerifyASN1(key, digest[:], sig) {
			continue
		}
		counted[string(der)] = true
	}
	if len(counted) < r.Threshold {
		return fmt.Errorf("%w: %d of the %d signatures the %s role of TUF root %d needs", ErrUntrusted, len(counted), r.Threshold, role, root.Version)
	}
	return nil
}

// key returns an ECDSA P-256 key of the root and its encoding, or nil for a
// key it does not have or of a kind this package does not check.
func (r *tufRoot) key(id string) (*ecdsa.PublicKey, []byte) {
	k, ok := r.Keys[id]
	if !ok || k.Scheme != "ecdsa-sha2-nistp256" {
		return nil, nil
	}
	block, _ := pem.Decode([]byte(k.KeyVal.Public))
	if block == nil {
		return nil, nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil
	}
	key, ok := pub.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, nil
	}
	return key, block.Bytes
}

func parseTUFRoot(doc []byte) (*tufRoot, *tufMetadata, error) {
	var root tufRoot
	meta, err := parseTUF(doc, &root)
	if err != nil {
		return nil, nil, err
	}
	if root.Type != "root" || !strings.HasPrefix(root.SpecVersion, "1.") || root.Version < 1 {
		return nil, nil, fmt.Errorf("%w: not TUF root metadata this package reads", ErrMalformed)
	}
	return &root, meta, nil
}

// FromTUF returns the trust root trustedRootJSON, after checking that the TUF
// targets metadata names it and that the keys of the TUF root signed that
// metadata. The TUF root is taken as given: it is the anchor, and what makes
// it trustworthy is that it is compiled into the image. Nothing is checked
// against a clock.
func FromTUF(rootJSON, targetsJSON, trustedRootJSON []byte) (*TrustRoot, error) {
	root, _, err := parseTUFRoot(rootJSON)
	if err != nil {
		return nil, err
	}
	return fromTUF(root, targetsJSON, trustedRootJSON, time.Time{})
}

// fromTUF checks targets metadata and the trust root it names against a TUF
// root, and that neither expired by now (a zero now skips that).
func fromTUF(root *tufRoot, targetsJSON, trustedRootJSON []byte, now time.Time) (*TrustRoot, error) {
	var targets tufTargets
	meta, err := parseTUF(targetsJSON, &targets)
	if err != nil {
		return nil, err
	}
	if targets.Type != "targets" || !strings.HasPrefix(targets.SpecVersion, "1.") || targets.Version < 1 {
		return nil, fmt.Errorf("%w: not TUF targets metadata this package reads", ErrMalformed)
	}
	if err := meta.signedBy(root, "targets"); err != nil {
		return nil, err
	}
	if !now.IsZero() && (!now.Before(root.Expires) || !now.Before(targets.Expires)) {
		return nil, fmt.Errorf("%w: the TUF metadata expired (root %d on %s, targets %d on %s)", ErrUntrusted,
			root.Version, root.Expires.Format(time.DateOnly), targets.Version, targets.Expires.Format(time.DateOnly))
	}
	target, ok := targets.Targets[trustedRootTarget]
	sum := sha256.Sum256(trustedRootJSON)
	if !ok || target.Length != int64(len(trustedRootJSON)) || target.Hashes["sha256"] != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("%w: this is not the %s that TUF targets %d names", ErrUntrusted, trustedRootTarget, targets.Version)
	}
	tr, err := ParseTrustRoot(trustedRootJSON)
	if err != nil {
		return nil, err
	}
	tr.Publication = &Publication{SHA256: hex.EncodeToString(sum[:]), RootVersion: root.Version, TargetsVersion: targets.Version}
	tr.tuf = root
	return tr, nil
}

// Update returns the trust root that u carries, if Sigstore published it after
// the one in use: every TUF root in u is signed by the keyholders of the one
// before it, starting from the one tr came with, the last of them signed the
// targets metadata, that metadata names the new trusted_root.json, and nothing
// expired by now.
//
// The new trust root replaces the logs and the timestamp authorities. It does
// not replace who issues signing certificates: the authorities stay the ones
// of tr, which in an enclave are the ones compiled into the image, and the
// update can only end the period of one of them. So the keyholders of
// Sigstore's TUF root can move where signatures are recorded and timestamped,
// and cannot name a new issuer of certificates to an image that is running.
func (tr *TrustRoot) Update(u Update, now time.Time) (*TrustRoot, error) {
	if tr == nil || tr.tuf == nil || tr.Publication == nil {
		return nil, fmt.Errorf("%w: this trust root did not come from TUF metadata, so nothing says what may follow it", ErrMalformed)
	}
	if len(u.Roots) > maxTUFRoots || len(u.TrustedRoot) > maxTUFMetadata || now.IsZero() {
		return nil, fmt.Errorf("%w: update too large, or no time to check it against", ErrMalformed)
	}
	root := tr.tuf
	for _, doc := range u.Roots {
		next, meta, err := parseTUFRoot(doc)
		if err != nil {
			return nil, err
		}
		if next.Version != root.Version+1 {
			return nil, fmt.Errorf("%w: TUF root %d does not follow TUF root %d", ErrUntrusted, next.Version, root.Version)
		}
		// The keyholders of the root in use accept the new one, and the new one
		// is signed by the keys it names.
		if err := meta.signedBy(root, "root"); err != nil {
			return nil, err
		}
		if err := meta.signedBy(next, "root"); err != nil {
			return nil, err
		}
		root = next
	}
	next, err := fromTUF(root, u.Targets, u.TrustedRoot, now)
	if err != nil {
		return nil, err
	}
	if next.Publication.TargetsVersion < tr.Publication.TargetsVersion ||
		(next.Publication.TargetsVersion == tr.Publication.TargetsVersion && next.Publication.SHA256 != tr.Publication.SHA256) {
		return nil, fmt.Errorf("%w: TUF targets %d is not newer than %d, the one in use", ErrUntrusted, next.Publication.TargetsVersion, tr.Publication.TargetsVersion)
	}

	// Who issues signing certificates stays as it was. An authority whose
	// period the new trust root ends earlier ends earlier here too.
	authorities := slices.Clone(tr.Authorities)
	for i, a := range authorities {
		for _, b := range next.Authorities {
			if a.id == b.id && !b.NotAfter.IsZero() && (a.NotAfter.IsZero() || b.NotAfter.Before(a.NotAfter)) {
				authorities[i].NotAfter = b.NotAfter
			}
		}
	}
	next.Authorities = authorities
	return next, nil
}
