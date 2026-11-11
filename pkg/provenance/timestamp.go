package provenance

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"time"
)

// A timestamp (RFC 3161) is a CMS SignedData (RFC 5652) whose content says
// "a message with this hash existed at this time", signed by a timestamp
// authority. Here the message is the signature of the statement.

var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
)

// hashes are the digest algorithms a timestamp may use.
var hashes = []struct {
	oid  asn1.ObjectIdentifier
	hash crypto.Hash
}{
	{asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}, crypto.SHA256},
	{asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}, crypto.SHA384},
	{asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}, crypto.SHA512},
}

func hashOf(alg pkix.AlgorithmIdentifier) (crypto.Hash, bool) {
	for _, h := range hashes {
		if h.oid.Equal(alg.Algorithm) {
			return h.hash, true
		}
	}
	return 0, false
}

func sum(h crypto.Hash, data []byte) []byte {
	w := h.New()
	w.Write(data)
	return w.Sum(nil)
}

type timeStampResp struct {
	Status struct {
		Status int
		Text   []asn1.RawValue `asn1:"optional"`
		Fail   asn1.BitString  `asn1:"optional"`
	}
	Token asn1.RawValue `asn1:"optional"`
}

type contentInfo struct {
	Type    asn1.ObjectIdentifier
	Content asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version    int
	DigestAlgs []pkix.AlgorithmIdentifier `asn1:"set"`
	Content    struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,optional,tag:0"`
	}
	Certificates asn1.RawValue `asn1:"optional,tag:0"`
	CRLs         asn1.RawValue `asn1:"optional,tag:1"`
	Signers      []signerInfo  `asn1:"set"`
}

type signerInfo struct {
	Version     int
	ID          asn1.RawValue
	DigestAlg   pkix.AlgorithmIdentifier
	SignedAttrs asn1.RawValue `asn1:"optional,tag:0"`
	SigAlg      pkix.AlgorithmIdentifier
	Signature   []byte
	Unsigned    asn1.RawValue `asn1:"optional,tag:1"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

type messageImprint struct {
	Alg  pkix.AlgorithmIdentifier
	Hash []byte
}

// readTSTInfo reads the start of a TSTInfo: version, policy, the hash of the
// message, serial number and time. Accuracy, ordering, nonce, the authority's
// name and extensions may follow; none of them changes what is checked here.
func readTSTInfo(der []byte) (messageImprint, asn1.RawValue, error) {
	var (
		seq     asn1.RawValue
		version int
		policy  asn1.ObjectIdentifier
		imprint messageImprint
		serial  *big.Int
		genTime asn1.RawValue
	)
	if rest, err := asn1.Unmarshal(der, &seq); err != nil || len(rest) != 0 || seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence || !seq.IsCompound {
		return imprint, genTime, fmt.Errorf("malformed timestamp")
	}
	rest := seq.Bytes
	for _, field := range []any{&version, &policy, &imprint, &serial, &genTime} {
		var err error
		if rest, err = asn1.Unmarshal(rest, field); err != nil {
			return imprint, genTime, fmt.Errorf("malformed timestamp")
		}
	}
	if version != 1 {
		return imprint, genTime, fmt.Errorf("timestamp of version %d", version)
	}
	return imprint, genTime, nil
}

// checkTimestamp verifies that ts signed a timestamp for message, and returns
// the time it gives. der is an RFC 3161 TimeStampResp.
func checkTimestamp(ts Timestamper, der, message []byte) (time.Time, error) {
	fail := func(format string, a ...any) (time.Time, error) {
		return time.Time{}, fmt.Errorf("timestamp: "+format, a...)
	}
	var resp timeStampResp
	if rest, err := asn1.Unmarshal(der, &resp); err != nil || len(rest) != 0 {
		return fail("not a timestamp response")
	}
	// 0 is granted and 1 granted with modifications; anything else carries no token.
	if resp.Status.Status != 0 && resp.Status.Status != 1 {
		return fail("the authority refused the request (status %d)", resp.Status.Status)
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(resp.Token.FullBytes, &ci); err != nil || len(rest) != 0 || !ci.Type.Equal(oidSignedData) {
		return fail("no signed data")
	}
	var sd signedData
	if rest, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil || len(rest) != 0 {
		return fail("malformed signed data")
	}
	if !sd.Content.Type.Equal(oidTSTInfo) || len(sd.Signers) != 1 {
		return fail("not one signature over a timestamp")
	}
	signer := sd.Signers[0]

	// The signer is the authority's certificate, by issuer and serial number
	// or by key identifier.
	var byName struct {
		Issuer asn1.RawValue
		Serial *big.Int
	}
	switch {
	case signer.ID.Class == asn1.ClassContextSpecific && signer.ID.Tag == 0:
		if len(ts.Leaf.SubjectKeyId) == 0 || !bytes.Equal(signer.ID.Bytes, ts.Leaf.SubjectKeyId) {
			return fail("signed by another certificate")
		}
	default:
		if rest, err := asn1.Unmarshal(signer.ID.FullBytes, &byName); err != nil || len(rest) != 0 || byName.Serial == nil ||
			!bytes.Equal(byName.Issuer.FullBytes, ts.Leaf.RawIssuer) || byName.Serial.Cmp(ts.Leaf.SerialNumber) != 0 {
			return fail("signed by another certificate")
		}
	}

	// The signature covers attributes, and the attributes name the content.
	digest, ok := hashOf(signer.DigestAlg)
	if !ok {
		return fail("digest algorithm %v", signer.DigestAlg.Algorithm)
	}
	if len(signer.SignedAttrs.FullBytes) == 0 {
		return fail("no signed attributes")
	}
	// What is signed is the attributes as a SET OF, not with the implicit tag
	// they have inside the signer (RFC 5652, 5.4).
	signedAttrs := append([]byte{0x31}, signer.SignedAttrs.FullBytes[1:]...)
	var attrs []attribute
	if rest, err := asn1.UnmarshalWithParams(signedAttrs, &attrs, "set"); err != nil || len(rest) != 0 {
		return fail("malformed signed attributes")
	}
	var contentType asn1.ObjectIdentifier
	var messageDigest []byte
	for _, a := range attrs {
		if len(a.Values) != 1 {
			return fail("an attribute with %d values", len(a.Values))
		}
		var err error
		var rest []byte
		switch {
		case a.Type.Equal(oidContentType):
			if contentType != nil {
				return fail("two content types")
			}
			rest, err = asn1.Unmarshal(a.Values[0].FullBytes, &contentType)
		case a.Type.Equal(oidMessageDigest):
			if messageDigest != nil {
				return fail("two message digests")
			}
			rest, err = asn1.Unmarshal(a.Values[0].FullBytes, &messageDigest)
		}
		if err != nil || len(rest) != 0 {
			return fail("malformed signed attribute")
		}
	}
	if !contentType.Equal(oidTSTInfo) || !bytes.Equal(messageDigest, sum(digest, sd.Content.Content)) {
		return fail("the signature is not over this timestamp")
	}
	if err := checkSignature(ts.Leaf, digest, signedAttrs, signer.Signature); err != nil {
		return fail("the authority's signature does not verify")
	}

	// The timestamp is for this message.
	imprint, genTime, err := readTSTInfo(sd.Content.Content)
	if err != nil {
		return fail("%w", err)
	}
	imprintHash, ok := hashOf(imprint.Alg)
	if !ok || !bytes.Equal(imprint.Hash, sum(imprintHash, message)) {
		return fail("it is for another message")
	}
	at, err := generalizedTime(genTime)
	if err != nil {
		return fail("%w", err)
	}

	// The authority was in use then, and its certificate valid, for
	// timestamping only.
	if !within(at, ts.NotBefore, ts.NotAfter) {
		return fail("the authority was not in use at %s", at.Format(time.RFC3339))
	}
	if len(ts.Leaf.ExtKeyUsage) != 1 || ts.Leaf.ExtKeyUsage[0] != x509.ExtKeyUsageTimeStamping || len(ts.Leaf.UnknownExtKeyUsage) != 0 {
		return fail("the authority's certificate is not for timestamping only")
	}
	if _, err := ts.Leaf.Verify(x509.VerifyOptions{
		Roots: ts.Roots, Intermediates: ts.Intermediates, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping},
	}); err != nil {
		return fail("the authority's certificate at %s: %w", at.Format(time.RFC3339), err)
	}
	return at, nil
}

// checkSignature verifies an ECDSA signature by cert's key over the hash h of
// msg. Sigstore's timestamp authority signs with ECDSA; a timestamp signed
// with another kind of key does not count.
func checkSignature(cert *x509.Certificate, h crypto.Hash, msg, sig []byte) error {
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !ecdsa.VerifyASN1(key, sum(h, msg), sig) {
		return fmt.Errorf("signature does not verify")
	}
	return nil
}

// generalizedTime reads a GeneralizedTime in UTC, with or without a fraction
// of a second (RFC 3161 allows one; encoding/asn1 does not read it).
func generalizedTime(v asn1.RawValue) (time.Time, error) {
	if v.Class != asn1.ClassUniversal || v.Tag != asn1.TagGeneralizedTime {
		return time.Time{}, fmt.Errorf("no time")
	}
	for _, layout := range []string{"20060102150405Z", "20060102150405.999999999Z"} {
		if t, err := time.Parse(layout, string(v.Bytes)); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q", v.Bytes)
}
