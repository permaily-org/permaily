package oauth

import "time"

// An authorization code is used once, with no grace (ADR 0030, finding 8;
// RFC 6749 section 4.1.2). Whether it was used is decided only by its
// connection's record, a strict anchored record, where each code and the
// family it made are listed (CodeState); its sealed entry holds only what
// checking it needs. The first exchange that matches the code's client,
// redirect URI, verifier and resource makes its family; an exchange that
// does not match spends it (one attempt, a guessed verifier included);
// a second exchange of a code that made a family ends that family.

// CodeStatus is where one code is.
type CodeStatus string

const (
	CodeIssued    CodeStatus = "issued"
	CodeExchanged CodeStatus = "exchanged"
	CodeSpent     CodeStatus = "spent"
)

// CodeState is one code as its connection's record lists it.
type CodeState struct {
	// Hash is Hash(KindCode, the code).
	Hash      string     `json:"hash"`
	Family    string     `json:"family"`
	ExpiresAt time.Time  `json:"expires_at"`
	Status    CodeStatus `json:"status"`
}

// CodeResult is what one exchange decides.
type CodeResult int8

const (
	// CodeOK: the family is made; write the record, then issue its first pair.
	CodeOK CodeResult = iota + 1
	// CodeReused: a second exchange of a code that made a family: refused
	// (invalid_grant); write the record with that family ended
	// (EndedByReuse).
	CodeReused
	// CodeRefused: expired, spent, or not matching (which spends it):
	// invalid_grant. Write the record when Spent changed.
	CodeRefused
)

// Exchange is the transition of one exchange; matches says the client,
// redirect URI, verifier and resource were the code's. It reports whether c
// changed and must be written.
func (c *CodeState) Exchange(matches bool, now time.Time) (CodeResult, bool) {
	switch c.Status {
	case CodeExchanged:
		return CodeReused, false
	case CodeSpent:
		return CodeRefused, false
	}
	if !now.Before(c.ExpiresAt) {
		return CodeRefused, false
	}
	if !matches {
		c.Status = CodeSpent
		return CodeRefused, true
	}
	c.Status = CodeExchanged
	return CodeOK, true
}
