package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"fmt"
)

// KMS returns CiphertextForRecipient as a CMS EnvelopedData (RFC 5652): the
// data key encrypted with AES-256-CBC under a content key, and the content key
// encrypted to the enclave's RSA key with RSAES-OAEP-SHA-256. KMS may use BER
// (indefinite lengths, chunked octet strings), which encoding/asn1 rejects, so
// this file has a minimal BER reader for exactly that structure.

var (
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidRSAESOAEP     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}
	oidAES256CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

var errCMS = errors.New("keys: malformed CMS enveloped data")

type berNode struct {
	class       int
	tag         int
	constructed bool
	content     []byte // primitive contents
	children    []berNode
}

const maxBERDepth = 16

func parseBER(b []byte, depth int) (berNode, []byte, error) {
	if depth > maxBERDepth || len(b) < 2 {
		return berNode{}, nil, errCMS
	}
	id := b[0]
	n := berNode{class: int(id >> 6), constructed: id&0x20 != 0, tag: int(id & 0x1f)}
	if n.tag == 0x1f {
		return berNode{}, nil, errCMS // high tag numbers do not occur in this structure
	}
	b = b[1:]
	l := int(b[0])
	b = b[1:]
	if l == 0x80 { // indefinite length: children until end-of-contents
		if !n.constructed {
			return berNode{}, nil, errCMS
		}
		for {
			if len(b) >= 2 && b[0] == 0 && b[1] == 0 {
				return n, b[2:], nil
			}
			child, rest, err := parseBER(b, depth+1)
			if err != nil {
				return berNode{}, nil, err
			}
			n.children = append(n.children, child)
			b = rest
		}
	}
	if l > 0x80 {
		k := l & 0x7f
		if k > 4 || len(b) < k {
			return berNode{}, nil, errCMS
		}
		l = 0
		for _, c := range b[:k] {
			l = l<<8 | int(c)
		}
		b = b[k:]
	}
	if l < 0 || len(b) < l {
		return berNode{}, nil, errCMS
	}
	body, rest := b[:l], b[l:]
	if !n.constructed {
		n.content = body
		return n, rest, nil
	}
	for len(body) > 0 {
		child, r, err := parseBER(body, depth+1)
		if err != nil {
			return berNode{}, nil, err
		}
		n.children = append(n.children, child)
		body = r
	}
	return n, rest, nil
}

// octets returns the bytes of a primitive or constructed (chunked) string.
func (n berNode) octets() []byte {
	if !n.constructed {
		return n.content
	}
	var out []byte
	for _, c := range n.children {
		out = append(out, c.octets()...)
	}
	return out
}

func (n berNode) oid() (asn1.ObjectIdentifier, error) {
	var o asn1.ObjectIdentifier
	der, err := asn1.Marshal(asn1.RawValue{Tag: asn1.TagOID, Class: asn1.ClassUniversal, Bytes: n.content})
	if err != nil {
		return nil, err
	}
	_, err = asn1.Unmarshal(der, &o)
	return o, err
}

func (n berNode) child(i int) (berNode, error) {
	if i >= len(n.children) {
		return berNode{}, errCMS
	}
	return n.children[i], nil
}

// decryptEnvelopedData returns the plaintext of a CMS EnvelopedData addressed
// to key.
func decryptEnvelopedData(der []byte, key *rsa.PrivateKey) ([]byte, error) {
	ci, _, err := parseBER(der, 0)
	if err != nil {
		return nil, err
	}
	ct, err := ci.child(0)
	if err != nil {
		return nil, err
	}
	if o, err := ct.oid(); err != nil || !o.Equal(oidEnvelopedData) {
		return nil, fmt.Errorf("%w: not enveloped data", errCMS)
	}
	wrapper, err := ci.child(1) // [0] EXPLICIT
	if err != nil {
		return nil, err
	}
	ed, err := wrapper.child(0)
	if err != nil {
		return nil, err
	}

	var recipients, eci berNode
	found := 0
	for _, c := range ed.children {
		switch {
		case c.class == 0 && c.tag == 17: // SET OF RecipientInfo
			recipients = c
			found++
		case c.class == 0 && c.tag == 16: // EncryptedContentInfo
			eci = c
			found++
		}
	}
	if found != 2 || len(recipients.children) == 0 {
		return nil, errCMS
	}

	// KeyTransRecipientInfo: version, rid, keyEncryptionAlgorithm, encryptedKey.
	ktri := recipients.children[0]
	if len(ktri.children) < 4 {
		return nil, errCMS
	}
	alg := ktri.children[2]
	if len(alg.children) == 0 {
		return nil, errCMS
	}
	if o, err := alg.children[0].oid(); err != nil || !o.Equal(oidRSAESOAEP) {
		return nil, fmt.Errorf("%w: key encryption is not RSAES-OAEP", errCMS)
	}
	cek, err := rsa.DecryptOAEP(sha256.New(), nil, key, ktri.children[3].octets(), nil)
	if err != nil {
		return nil, fmt.Errorf("keys: decrypt content key: %w", err)
	}

	// EncryptedContentInfo: contentType, contentEncryptionAlgorithm, [0] encryptedContent.
	if len(eci.children) < 3 {
		return nil, errCMS
	}
	calg := eci.children[1]
	if len(calg.children) < 2 {
		return nil, errCMS
	}
	if o, err := calg.children[0].oid(); err != nil || !o.Equal(oidAES256CBC) {
		return nil, fmt.Errorf("%w: content encryption is not AES-256-CBC", errCMS)
	}
	iv := calg.children[1].octets()
	content := eci.children[2].octets()
	if len(cek) != 32 || len(iv) != aes.BlockSize || len(content) == 0 || len(content)%aes.BlockSize != 0 {
		return nil, errCMS
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(content))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, content)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return nil, errCMS
	}
	for _, p := range plain[len(plain)-pad:] {
		if int(p) != pad {
			return nil, errCMS
		}
	}
	return plain[:len(plain)-pad], nil
}
