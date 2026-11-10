package keys

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// --- CMS builders (DER and BER) used by the fake KMS ---

func derLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte(n)}, b...)
		n >>= 8
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

func tlv(tag byte, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	return append(append([]byte{tag}, derLen(len(body))...), body...)
}

// indef encodes a constructed value with BER indefinite length.
func indef(tag byte, parts ...[]byte) []byte {
	return append(append([]byte{tag, 0x80}, bytes.Join(parts, nil)...), 0, 0)
}

func oid(o asn1.ObjectIdentifier) []byte {
	b, err := asn1.Marshal(o)
	if err != nil {
		panic(err)
	}
	return b
}

func pkcs7pad(b []byte) []byte {
	p := aes.BlockSize - len(b)%aes.BlockSize
	return append(bytes.Clone(b), bytes.Repeat([]byte{byte(p)}, p)...)
}

func envelop(t *testing.T, plaintext []byte, pub *rsa.PublicKey, ber bool) []byte {
	t.Helper()
	cek := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(cek)
	_, _ = rand.Read(iv)
	block, _ := aes.NewCipher(cek)
	content := pkcs7pad(plaintext)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(content, content)
	encKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, cek, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, set, ctx0 := byte(0x30), byte(0x31), byte(0xa0)
	ktri := tlv(seq, tlv(0x02, []byte{2}), tlv(0x80, []byte("skid")), tlv(seq, oid(oidRSAESOAEP), tlv(seq)), tlv(0x04, encKey))
	var encContent []byte
	if ber { // chunked constructed [0]
		half := len(content) / 2
		encContent = indef(ctx0, tlv(0x04, content[:half]), tlv(0x04, content[half:]))
	} else {
		encContent = tlv(0x80, content)
	}
	eci := tlv(seq, oid(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}), tlv(seq, oid(oidAES256CBC), tlv(0x04, iv)), encContent)
	if ber {
		ed := indef(seq, tlv(0x02, []byte{0}), indef(set, ktri), eci)
		return indef(seq, oid(oidEnvelopedData), indef(ctx0, ed))
	}
	ed := tlv(seq, tlv(0x02, []byte{0}), tlv(set, ktri), eci)
	return tlv(seq, oid(oidEnvelopedData), tlv(ctx0, ed))
}

// --- fakes ---

type pubAttester struct{}

// AttestPublicKey returns the key itself; the fake KMS reads it back.
func (pubAttester) AttestPublicKey(pub []byte) ([]byte, error) { return pub, nil }

type fakeKMS struct {
	t      *testing.T
	arn    string
	ber    bool
	stored map[string][]byte // wrapped -> plaintext
}

func (f *fakeKMS) recipientKey(doc []byte) *rsa.PublicKey {
	k, err := x509.ParsePKIXPublicKey(doc)
	if err != nil {
		f.t.Fatal(err)
	}
	return k.(*rsa.PublicKey)
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, in *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	dk := make([]byte, 32)
	_, _ = rand.Read(dk)
	wrapped := append([]byte("wrapped:"), dk[:8]...)
	f.stored[string(wrapped)] = dk
	return &kms.GenerateDataKeyOutput{
		KeyId:                  aws.String(f.arn),
		CiphertextBlob:         wrapped,
		CiphertextForRecipient: envelop(f.t, dk, f.recipientKey(in.Recipient.AttestationDocument), f.ber),
	}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	dk, ok := f.stored[string(in.CiphertextBlob)]
	if !ok {
		return nil, errors.New("InvalidCiphertextException")
	}
	return &kms.DecryptOutput{
		KeyId:                  aws.String(f.arn),
		CiphertextForRecipient: envelop(f.t, dk, f.recipientKey(in.Recipient.AttestationDocument), f.ber),
	}, nil
}

const arn = "arn:aws:kms:us-east-2:111122223333:key/test"

func TestKMSRoundTrip(t *testing.T) {
	for _, ber := range []bool{false, true} {
		api := &fakeKMS{t: t, arn: arn, ber: ber, stored: map[string][]byte{}}
		svc, err := NewKMS(api, arn, pubAttester{})
		if err != nil {
			t.Fatal(err)
		}
		dk, err := svc.NewDataKey(context.Background(), map[string]string{"mailbox": "m1"})
		if err != nil {
			t.Fatalf("ber=%v: %v", ber, err)
		}
		if len(dk.Plaintext) != 32 || len(dk.Wrapped) == 0 {
			t.Fatalf("ber=%v: bad data key", ber)
		}
		got, err := svc.Unwrap(context.Background(), dk.Wrapped, map[string]string{"mailbox": "m1"})
		if err != nil || !bytes.Equal(got, dk.Plaintext) {
			t.Fatalf("ber=%v: unwrap: %v", ber, err)
		}
	}
}

func TestKMSRejectsOtherKey(t *testing.T) {
	api := &fakeKMS{t: t, arn: "arn:aws:kms:us-east-2:999999999999:key/attacker", stored: map[string][]byte{}}
	svc, err := NewKMS(api, arn, pubAttester{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NewDataKey(context.Background(), nil); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
}

func TestCMSRejectsGarbage(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	good := envelop(t, []byte("secret data key 32 bytes long!!!"), &priv.PublicKey, false)
	for name, in := range map[string][]byte{
		"empty":         nil,
		"truncated":     good[:len(good)/2],
		"not a CMS":     tlv(0x30, oid(asn1.ObjectIdentifier{1, 2, 3})),
		"deep nesting":  bytes.Repeat([]byte{0x30, 0x80}, 40),
		"bad length":    {0x30, 0x84, 0xff, 0xff, 0xff, 0xff},
		"other key":     envelop(t, []byte("x"), &other.PublicKey, false),
		"tampered tail": append(bytes.Clone(good[:len(good)-1]), good[len(good)-1]^0xff),
	} {
		if _, err := decryptEnvelopedData(in, priv); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got, err := decryptEnvelopedData(good, priv); err != nil || string(got) != "secret data key 32 bytes long!!!" {
		t.Fatalf("good: %q %v", got, err)
	}
}

func FuzzCMS(f *testing.F) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	f.Add([]byte{0x30, 0x80, 0x00, 0x00})
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = decryptEnvelopedData(b, priv) // must not panic
	})
}

func TestLocal(t *testing.T) {
	l, err := NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	dk, err := l.NewDataKey(context.Background(), map[string]string{"mailbox": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Unwrap(context.Background(), dk.Wrapped, map[string]string{"mailbox": "b"}); err == nil {
		t.Fatal("unwrapped with a different context")
	}
	got, err := l.Unwrap(context.Background(), dk.Wrapped, map[string]string{"mailbox": "a"})
	if err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatal("round trip failed")
	}
}
