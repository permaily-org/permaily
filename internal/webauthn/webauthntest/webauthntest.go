// Package webauthntest is a software authenticator: it answers the creation
// and the use of a passkey the way a browser with an authenticator would. It
// exists for tests and for the enclave's dev mode; its keys live in memory and
// it asks nobody for a finger.
package webauthntest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"slices"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

// COSE algorithms an Authenticator can be made for.
const (
	ES256 = -7
	EdDSA = -8
	RS256 = -257
)

// Authenticator is a software authenticator with one key per credential.
//
// The zero values of its fields give right answers. A test sets them to get a
// wrong one, signed all the same, which is what a check must refuse.
type Authenticator struct {
	NoPresence     bool   // the UP flag is left out
	NoVerification bool   // the UV flag is left out
	BackupEligible bool   // the BE flag is set
	Synced         bool   // the BE and BS flags are set
	CrossOrigin    bool   // the client data says the page was inside another site's frame
	RPID           string // when set, the answer is made for this relying party instead of the one asked
	Counter        uint32 // the signature counter

	// EditClientData and EditAuthData change the client data and the
	// authenticator data before they are signed, for answers no field makes.
	EditClientData func([]byte) []byte
	EditAuthData   func([]byte) []byte

	alg         int
	mu          sync.Mutex
	credentials map[string]credential
}

// idSize is how long the identifiers it makes are.
const idSize = 32

type credential struct {
	signer     crypto.Signer
	userHandle []byte
}

// New makes an authenticator whose keys are of one algorithm: ES256, EdDSA or RS256.
func New(alg int) *Authenticator {
	if alg != ES256 && alg != EdDSA && alg != RS256 {
		panic("webauthntest: algorithm not supported")
	}
	return &Authenticator{alg: alg, credentials: map[string]credential{}}
}

// Create answers a creation: it returns the credential id, clientDataJSON and attestationObject.
// The attestation is "none", as a browser gives when the service asks for none.
func (a *Authenticator) Create(rpID, origin string, challenge, userHandle []byte) (id, clientDataJSON, attestationObject []byte) {
	signer := a.generate()
	id = make([]byte, idSize)
	must(rand.Read(id))

	a.mu.Lock()
	a.credentials[string(id)] = credential{signer: signer, userHandle: slices.Clone(userHandle)}
	a.mu.Unlock()

	attested := make([]byte, 16, 18+idSize) // an authenticator model of zeros: none says nothing of the device
	attested = binary.BigEndian.AppendUint16(attested, idSize)
	attested = append(attested, id...)
	attested = append(attested, COSEKey(signer.Public())...)

	authData := a.authData(rpID, 0x40, attested)
	clientDataJSON = a.clientData("webauthn.create", origin, challenge)
	attestationObject = encode(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	return id, clientDataJSON, attestationObject
}

// Get answers a request with the credential id: clientDataJSON, authenticatorData, signature and the user handle.
// For an id it did not make, it returns nothing.
func (a *Authenticator) Get(rpID, origin string, challenge, id []byte) (clientDataJSON, authenticatorData, signature, userHandle []byte) {
	a.mu.Lock()
	cred, ok := a.credentials[string(id)]
	a.mu.Unlock()
	if !ok {
		return nil, nil, nil, nil
	}
	authenticatorData = a.authData(rpID, 0, nil)
	clientDataJSON = a.clientData("webauthn.get", origin, challenge)
	hash := sha256.Sum256(clientDataJSON)
	signature = sign(cred.signer, slices.Concat(authenticatorData, hash[:]))
	return clientDataJSON, authenticatorData, signature, slices.Clone(cred.userHandle)
}

func (a *Authenticator) generate() crypto.Signer {
	switch a.alg {
	case ES256:
		return must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	case EdDSA:
		_, private, err := ed25519.GenerateKey(rand.Reader)
		return must(private, err)
	default:
		return must(rsa.GenerateKey(rand.Reader, 2048))
	}
}

func (a *Authenticator) authData(rpID string, flags byte, attested []byte) []byte {
	if a.RPID != "" {
		rpID = a.RPID
	}
	if !a.NoPresence {
		flags |= 0x01
	}
	if !a.NoVerification {
		flags |= 0x04
	}
	if a.BackupEligible || a.Synced {
		flags |= 0x08
	}
	if a.Synced {
		flags |= 0x10
	}
	hash := sha256.Sum256([]byte(rpID))
	data := append(hash[:], flags)
	data = binary.BigEndian.AppendUint32(data, a.Counter)
	data = append(data, attested...)
	if a.EditAuthData != nil {
		data = a.EditAuthData(data)
	}
	return data
}

func (a *Authenticator) clientData(call, origin string, challenge []byte) []byte {
	data := must(json.Marshal(struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}{call, base64.RawURLEncoding.EncodeToString(challenge), origin, a.CrossOrigin}))
	if a.EditClientData != nil {
		data = a.EditClientData(data)
	}
	return data
}

// COSEKey writes a public key (ECDSA P-256, Ed25519 or RSA) as the COSE_Key an authenticator gives.
func COSEKey(public crypto.PublicKey) []byte {
	switch pub := public.(type) {
	case *ecdsa.PublicKey:
		point := must(pub.Bytes()) // 0x04, x, y
		return encode(map[int]any{1: 2, 3: ES256, -1: 1, -2: point[1:33], -3: point[33:]})
	case ed25519.PublicKey:
		return encode(map[int]any{1: 1, 3: EdDSA, -1: 6, -2: []byte(pub)})
	case *rsa.PublicKey:
		return encode(map[int]any{1: 3, 3: RS256, -1: pub.N.Bytes(), -2: big.NewInt(int64(pub.E)).Bytes()})
	}
	panic("webauthntest: key not supported")
}

// sign signs as WebAuthn says for the key's algorithm: ECDSA in ASN.1 DER and
// RSA PKCS #1 v1.5 over SHA-256, Ed25519 over the message itself.
func sign(signer crypto.Signer, message []byte) []byte {
	if _, ok := signer.(ed25519.PrivateKey); ok {
		return must(signer.Sign(rand.Reader, message, crypto.Hash(0)))
	}
	digest := sha256.Sum256(message)
	return must(signer.Sign(rand.Reader, digest[:], crypto.SHA256))
}

// ctap2 writes CBOR as authenticators do: shortest forms, keys in order.
var ctap2 = must(cbor.CTAP2EncOptions().EncMode())

func encode(v any) []byte {
	return must(ctap2.Marshal(v))
}

// must is for what fails only when the machine has no randomness left.
func must[T any](v T, err error) T {
	if err != nil {
		panic("webauthntest: " + err.Error())
	}
	return v
}
