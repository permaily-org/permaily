package keys

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// Attester produces an attestation document that carries publicKey.
type Attester interface {
	AttestPublicKey(publicKey []byte) ([]byte, error)
}

// KMSAPI is the subset of the KMS client used here.
type KMSAPI interface {
	GenerateDataKey(ctx context.Context, in *kms.GenerateDataKeyInput, opts ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// KMS is the enclave's Service backed by AWS KMS with attestation.
type KMS struct {
	api      KMSAPI
	keyARN   string
	attester Attester
	priv     *rsa.PrivateKey
	pubDER   []byte
}

// NewKMS creates a KMS service for the key pinned in the image. A fresh RSA
// key is generated for this enclave's lifetime; KMS encrypts data keys to it.
func NewKMS(api KMSAPI, keyARN string, att Attester) (*KMS, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &KMS{api: api, keyARN: keyARN, attester: att, priv: priv, pubDER: pub}, nil
}

func (k *KMS) recipient() (*types.RecipientInfo, error) {
	doc, err := k.attester.AttestPublicKey(k.pubDER)
	if err != nil {
		return nil, fmt.Errorf("keys: attestation: %w", err)
	}
	return &types.RecipientInfo{AttestationDocument: doc, KeyEncryptionAlgorithm: types.KeyEncryptionMechanismRsaesOaepSha256}, nil
}

// NewDataKey implements Service.
func (k *KMS) NewDataKey(ctx context.Context, c map[string]string) (DataKey, error) {
	rec, err := k.recipient()
	if err != nil {
		return DataKey{}, err
	}
	out, err := k.api.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:             aws.String(k.keyARN),
		KeySpec:           types.DataKeySpecAes256,
		EncryptionContext: c,
		Recipient:         rec,
	})
	if err != nil {
		return DataKey{}, fmt.Errorf("keys: generate data key: %w", err)
	}
	if aws.ToString(out.KeyId) != k.keyARN {
		return DataKey{}, ErrKeyMismatch
	}
	plain, err := decryptEnvelopedData(out.CiphertextForRecipient, k.priv)
	if err != nil {
		return DataKey{}, err
	}
	return DataKey{Plaintext: plain, Wrapped: out.CiphertextBlob}, nil
}

// Unwrap implements Service.
func (k *KMS) Unwrap(ctx context.Context, wrapped []byte, c map[string]string) ([]byte, error) {
	rec, err := k.recipient()
	if err != nil {
		return nil, err
	}
	out, err := k.api.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             aws.String(k.keyARN),
		EncryptionContext: c,
		Recipient:         rec,
	})
	if err != nil {
		return nil, fmt.Errorf("keys: decrypt data key: %w", err)
	}
	if aws.ToString(out.KeyId) != k.keyARN {
		return nil, ErrKeyMismatch
	}
	return decryptEnvelopedData(out.CiphertextForRecipient, k.priv)
}
