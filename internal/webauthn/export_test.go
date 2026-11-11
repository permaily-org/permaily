package webauthn

// Excused is what a test lets an answer captured at another relying party go
// without. Nothing excused is what the service asks.
type Excused struct {
	Unverified     bool // the person was not verified (UV off)
	ShortChallenge bool // the challenge is shorter than the service's own, though never empty
}

func (e Excused) asked() asked {
	ask := service
	if e.Unverified {
		ask.verified = false
	}
	if e.ShortChallenge {
		ask.challenge = 1
	}
	return ask
}

// RegisteredExcusing is Registered with some of what the service asks excused.
// It exists only in the tests, so a test can say that an answer is refused for
// one reason and for no other.
func (rp RelyingParty) RegisteredExcusing(e Excused, challenge, clientDataJSON, attestationObject []byte) (*Credential, error) {
	return rp.registered(e.asked(), challenge, clientDataJSON, attestationObject)
}

// AssertedExcusing is Asserted, excused the same way.
func (rp RelyingParty) AssertedExcusing(e Excused, challenge, publicKey, clientDataJSON, authenticatorData, signature []byte) (*Assertion, error) {
	return rp.asserted(e.asked(), challenge, publicKey, clientDataJSON, authenticatorData, signature)
}
