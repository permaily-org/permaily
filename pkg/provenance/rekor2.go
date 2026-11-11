package provenance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Rekor v2 (github.com/sigstore/rekor-tiles) keeps one kind of entry, and a
// signed statement is recorded as the hash of what was signed, the signature
// and the certificate. Nothing in the log says when: the bundle carries a
// timestamp from a timestamp authority instead.

// maxTimestamps bounds the timestamps read from a bundle.
const maxTimestamps = 8

// checkRekor2 checks an entry of a Rekor v2 log: the entry this statement,
// signature and certificate make is in a tree whose head the log signed, and a
// timestamp authority of the trust root signed when the signature existed. It
// returns that time: the earliest, when several authorities signed one.
func checkRekor2(tr *TrustRoot, log Log, entry logEntry, env envelope, cert *x509.Certificate, stamps [][]byte) (time.Time, error) {
	// The entry is rebuilt from the bundle instead of read from it, so there
	// is nothing to compare: the leaf that must be in the tree is the one for
	// this statement, this signature and this certificate.
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return time.Time{}, fmt.Errorf("%w: the signing certificate does not hold an ECDSA P-256 key", ErrUntrusted)
	}
	sig := env.Signatures[0].Sig
	signed := sha256.Sum256(fmt.Appendf(nil, "DSSEv1 %d %s %d %s", len(env.PayloadType), env.PayloadType, len(env.Payload), env.Payload))
	b64 := base64.StdEncoding.EncodeToString
	// The canonical JSON (RFC 8785) of the entry: keys in order, no spaces.
	body := `{"apiVersion":"0.0.2","kind":"hashedrekord","spec":{"hashedRekordV002":{"data":{"algorithm":"SHA2_256","digest":"` + b64(signed[:]) +
		`"},"signature":{"content":"` + b64(sig) + `","verifier":{"keyDetails":"PKIX_ECDSA_P256_SHA_256","x509Certificate":{"rawBytes":"` + b64(cert.Raw) + `"}}}}}}`
	leaf := sha256.Sum256(append([]byte{0}, body...))

	// The log signed the head of a tree (checkpoint), and the entry is in it.
	// Size and root come from what the log signed, not from the proof.
	size, root, err := openCheckpoint(log, entry.InclusionProof.Checkpoint.Envelope)
	if err != nil {
		return time.Time{}, err
	}
	index, err := strconv.ParseUint(entry.LogIndex, 10, 63)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: log entry: bad index", ErrMalformed)
	}
	r, err := treeRoot(leaf, index, size, entry.InclusionProof.Hashes)
	if err != nil {
		return time.Time{}, err
	}
	if !bytes.Equal(r[:], root) {
		return time.Time{}, fmt.Errorf("%w: inclusion proof: the entry is not in the tree", ErrUntrusted)
	}

	// A timestamp authority signed that the signature existed at a time. One
	// timestamp per authority counts, and one that does not verify is ignored,
	// as it would be for an authority this trust root does not name.
	if len(stamps) > maxTimestamps {
		return time.Time{}, fmt.Errorf("%w: bundle: too many timestamps", ErrMalformed)
	}
	var at time.Time
	used := make([]bool, len(tr.Timestampers))
	last := fmt.Errorf("the bundle carries none")
	for _, stamp := range stamps {
		for i, ts := range tr.Timestampers {
			if used[i] {
				continue
			}
			t, err := checkTimestamp(ts, stamp, sig)
			if err != nil {
				last = err
				continue
			}
			used[i] = true
			if at.IsZero() || t.Before(at) {
				at = t
			}
			break
		}
	}
	if at.IsZero() {
		return time.Time{}, fmt.Errorf("%w: no timestamp from an authority of the trust root: %w", ErrUntrusted, last)
	}
	return at, nil
}

// openCheckpoint verifies a Rekor v2 log's signature on a checkpoint (a signed
// note, c2sp.org/tlog-checkpoint) and returns the tree size and root hash it
// carries.
func openCheckpoint(log Log, checkpoint string) (uint64, []byte, error) {
	// Origin, size and root, maybe more lines, a blank line, signatures.
	text, sigs, ok := strings.Cut(checkpoint, "\n\n")
	lines := strings.Split(text, "\n")
	if !ok || len(lines) < 3 || lines[0] != log.Origin {
		return 0, nil, fmt.Errorf("%w: checkpoint: not for this log", ErrUntrusted)
	}
	size, err := strconv.ParseUint(lines[1], 10, 63)
	root, err2 := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || err2 != nil || len(root) != sha256.Size || strconv.FormatUint(size, 10) != lines[1] {
		return 0, nil, fmt.Errorf("%w: checkpoint: bad size or root", ErrUntrusted)
	}
	id, check := noteKey(log)
	if check == nil {
		return 0, nil, fmt.Errorf("%w: the log does not sign entries of this kind", ErrUntrusted)
	}
	for line := range strings.SplitSeq(sigs, "\n") {
		name, sigB64, ok := strings.Cut(strings.TrimPrefix(line, "— "), " ")
		if !ok || !strings.HasPrefix(line, "— ") || name != log.Origin {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(sigB64)
		if err == nil && len(raw) > 4 && binary.BigEndian.Uint32(raw) == id && check([]byte(text+"\n"), raw[4:]) {
			return size, root, nil
		}
	}
	return 0, nil, fmt.Errorf("%w: checkpoint: the log's signature does not verify", ErrUntrusted)
}

// noteKey returns the key id a log's signature lines carry (c2sp.org/signed-note)
// and how to check a signature, or nil for a key this package does not check.
func noteKey(log Log) (uint32, func(msg, sig []byte) bool) {
	switch key := log.Key.(type) {
	case ed25519.PublicKey:
		sum := sha256.Sum256(append(append([]byte(log.Origin+"\n"), 1), key...))
		return binary.BigEndian.Uint32(sum[:]), func(msg, sig []byte) bool { return ed25519.Verify(key, msg, sig) }
	case *ecdsa.PublicKey:
		der, err := x509.MarshalPKIXPublicKey(key)
		if err != nil {
			return 0, nil
		}
		sum := sha256.Sum256(der)
		return binary.BigEndian.Uint32(sum[:]), func(msg, sig []byte) bool { return verify(key, msg, sig) }
	}
	return 0, nil
}
