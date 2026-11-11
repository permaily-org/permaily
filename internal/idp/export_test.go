package idp

import (
	"slices"
	"time"
)

// What the tests outside the package read of it. Nothing here is in the image.

const (
	KeysLife       = keysLife
	KeysWait       = keysWait
	ClockSkew      = clockSkew
	MaxIDToken     = maxIDToken
	MaxKeySet      = maxKeySet
	MaxKeys        = maxKeys
	MaxTokenAnswer = maxTokenAnswer
	MaxUserAnswer  = maxUserAnswer
	MaxCode        = maxCode
	MaxName        = maxName
	GitHubPage     = githubPage
	PersonalTenant = personalTenant
)

var (
	ParseGitHubUser   = parseGitHubUser
	ParseGitHubEmails = parseGitHubEmails
	ParseMetaUser     = parseMetaUser
)

// SetTimeout changes how long a request may take, and returns how to put it back.
func SetTimeout(d time.Duration) (restore func()) {
	before := timeout
	timeout = d
	return func() { timeout = before }
}

// ParseTokenAnswer is parseTokenAnswer, with what it read spelled out.
func ParseTokenAnswer(body []byte) (access, id string, err error) {
	answer, err := parseTokenAnswer(body)
	if err != nil {
		return "", "", err
	}
	return answer.access, answer.id, nil
}

// UsableKeys reads a set of keys and returns the identifiers of the ones a
// token can be checked with, sorted, and how many keys were kept in all.
func UsableKeys(body []byte) (usable []string, kept int, err error) {
	keys, err := parseKeys(body)
	if err != nil {
		return nil, 0, err
	}
	for kid, key := range keys {
		if key.public != nil {
			usable = append(usable, kid)
		}
	}
	slices.Sort(usable)
	return usable, len(keys), nil
}

// Verify is the check of an ID token with the keys of a set as it is
// published, and no network.
func Verify(p Provider, issuer, clientID, nonce, token string, now time.Time, keySet []byte) (*Identity, error) {
	keys, err := parseKeys(keySet)
	if err != nil {
		return nil, err
	}
	return verify(p, issuer, clientID, nonce, token, now, func(kid string) (*signingKey, error) {
		if key, ok := keys[kid]; ok {
			return key, nil
		}
		return nil, refuse("ID token signed with a key the provider does not publish")
	})
}

// Claims is the check of what a token says, as if its signature had been
// checked with a key that names no issuer.
func Claims(p Provider, issuer, clientID, nonce string, payload []byte, now time.Time) (*Identity, error) {
	return claims(p, issuer, "", clientID, nonce, payload, now)
}
