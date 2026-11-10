// Package nsm talks to the Nitro Security Module (/dev/nsm) to produce
// attestation documents.
package nsm

import (
	"errors"
	"fmt"
	"sync"

	"github.com/hf/nsm"
	"github.com/hf/nsm/request"
)

// Attester produces attestation documents.
type Attester interface {
	// Attest returns a COSE_Sign1 attestation document that embeds nonce and
	// userData. Both may be at most 512 bytes.
	Attest(nonce, userData []byte) ([]byte, error)
}

// Device is an Attester backed by the real NSM device.
type Device struct {
	mu   sync.Mutex
	sess *nsm.Session
}

// Open opens the NSM device. It only works inside a Nitro Enclave.
func Open() (*Device, error) {
	sess, err := nsm.OpenDefaultSession()
	if err != nil {
		return nil, fmt.Errorf("nsm: open: %w", err)
	}
	return &Device{sess: sess}, nil
}

// Attest implements Attester.
func (d *Device) Attest(nonce, userData []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.sess.Send(&request.Attestation{Nonce: nonce, UserData: userData})
	if err != nil {
		return nil, fmt.Errorf("nsm: attestation: %w", err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("nsm: attestation: %s", res.Error)
	}
	if res.Attestation == nil || len(res.Attestation.Document) == 0 {
		return nil, errors.New("nsm: attestation: empty document")
	}
	return res.Attestation.Document, nil
}

// AttestPublicKey returns an attestation document carrying publicKey, which
// KMS uses as the recipient of data keys (Recipient parameter).
func (d *Device) AttestPublicKey(publicKey []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.sess.Send(&request.Attestation{PublicKey: publicKey})
	if err != nil {
		return nil, fmt.Errorf("nsm: attestation: %w", err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("nsm: attestation: %s", res.Error)
	}
	if res.Attestation == nil || len(res.Attestation.Document) == 0 {
		return nil, errors.New("nsm: attestation: empty document")
	}
	return res.Attestation.Document, nil
}

// Measurements returns the measurements of this enclave's own image (PCR0 to
// PCR2), as the Nitro Security Module holds them. In debug mode they are all zero.
func (d *Device) Measurements() (map[uint][]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[uint][]byte{}
	for _, i := range []uint16{0, 1, 2} {
		res, err := d.sess.Send(&request.DescribePCR{Index: i})
		if err != nil {
			return nil, fmt.Errorf("nsm: describe PCR%d: %w", i, err)
		}
		if res.Error != "" || res.DescribePCR == nil || len(res.DescribePCR.Data) == 0 {
			return nil, fmt.Errorf("nsm: describe PCR%d: %s", i, res.Error)
		}
		out[uint(i)] = res.DescribePCR.Data
	}
	return out, nil
}

// Close releases the device.
func (d *Device) Close() error { return d.sess.Close() }
