// Package differential asks go-webauthn the questions internal/webauthn
// answers inside the enclave, so the two can be compared. It is a module of
// its own: the library and what it needs (TPM, JWT, metadata) never reach the
// image.
package differential

import (
	"encoding/base64"
	"encoding/json"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// algorithms are the ones the service offers, in its order (webauthn.Algorithms).
var algorithms = []protocol.CredentialParameter{
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgEdDSA},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// credential writes an answer the way PublicKeyCredential.toJSON() does, which is what the library reads.
func credential(id []byte, response map[string]string) ([]byte, error) {
	return json.Marshal(map[string]any{"id": b64(id), "rawId": b64(id), "type": "public-key", "response": response})
}

// Registered is the library's answer to a creation, and the public key it
// would store. It is asked what the service asks: the person there and
// verified, no page inside another site, no metadata, one of three algorithms.
func Registered(rpID string, origins []string, challenge, id, clientDataJSON, attestationObject []byte) ([]byte, error) {
	return RegisteredAsking(true, rpID, origins, challenge, id, clientDataJSON, attestationObject)
}

// RegisteredAsking is Registered, asked whether the person must have been
// verified. The service always asks it; answers captured from real
// authenticators were often made without, and the library can say whether
// that is all that is wrong with one.
func RegisteredAsking(verified bool, rpID string, origins []string, challenge, id, clientDataJSON, attestationObject []byte) ([]byte, error) {
	body, err := credential(id, map[string]string{"clientDataJSON": b64(clientDataJSON), "attestationObject": b64(attestationObject)})
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return nil, err
	}
	if _, err := parsed.Verify(b64(challenge), rpID, origins, nil, nil, protocol.TopOriginExplicitVerificationMode,
		false, verified, true, nil, algorithms, protocol.AttestationPolicy{}, protocol.SignaturePolicy{}); err != nil {
		return nil, err
	}
	return parsed.Response.AttestationObject.AuthData.AttData.CredentialPublicKey, nil
}

// Asserted is the library's answer to a use, against a stored public key, asked the same way.
func Asserted(rpID string, origins []string, challenge, id, publicKey, clientDataJSON, authenticatorData, signature []byte) error {
	return AssertedAsking(true, rpID, origins, challenge, id, publicKey, clientDataJSON, authenticatorData, signature)
}

// AssertedAsking is Asserted, asked whether the person must have been verified, as RegisteredAsking is.
func AssertedAsking(verified bool, rpID string, origins []string, challenge, id, publicKey, clientDataJSON, authenticatorData, signature []byte) error {
	body, err := credential(id, map[string]string{
		"clientDataJSON": b64(clientDataJSON), "authenticatorData": b64(authenticatorData), "signature": b64(signature),
	})
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return err
	}
	return parsed.Verify(b64(challenge), rpID, "", origins, nil, nil, protocol.TopOriginExplicitVerificationMode,
		false, verified, true, publicKey, protocol.SignaturePolicy{})
}
