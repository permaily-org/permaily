// Package differential asks go-oidc the question internal/idp answers inside
// the enclave: is this ID token the provider's, for this application and
// this sign-in, and who is it about. It is a module of its own: the library
// and what it needs (go-jose, golang.org/x/oauth2) never reach the image.
package differential

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Answer is who the library says a token is about.
type Answer struct {
	Subject string // as internal/idp makes it: Microsoft's tenant and object when both are there
	Address string
}

// tenantPlace stands, in Microsoft's issuer, for the tenant of each token.
const tenantPlace = "{tenantid}"

// Reference checks an ID token the way an application that uses go-oidc
// does: the library checks the signature against the keys the provider
// publishes at keysURL, the algorithm, the issuer, the audience and the
// expiry; the nonce is the application's to compare, as the library's
// documentation says; and for Microsoft, whose issuer is another for each
// tenant, the application checks the issuer against the token's own tenant,
// as Microsoft's documentation says.
func Reference(ctx context.Context, keysURL, issuer, clientID, nonce, token string, now time.Time) (*Answer, error) {
	perTenant := strings.Contains(issuer, tenantPlace)
	// A key set for each token, so nothing is remembered from one case to the next.
	verifier := oidc.NewVerifier(issuer, oidc.NewRemoteKeySet(ctx, keysURL), &oidc.Config{
		ClientID:             clientID,
		SupportedSigningAlgs: []string{oidc.RS256},
		SkipIssuerCheck:      perTenant,
		Now:                  func() time.Time { return now },
	})
	verified, err := verifier.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	if verified.Nonce != nonce {
		return nil, errors.New("another nonce")
	}
	var claims struct {
		Email  *string `json:"email"`
		Name   *string `json:"name"`
		Tenant *string `json:"tid"`
		Object *string `json:"oid"`
	}
	if err := verified.Claims(&claims); err != nil {
		return nil, err
	}
	answer := &Answer{Subject: verified.Subject}
	if claims.Email != nil {
		answer.Address = *claims.Email
	}
	if perTenant {
		if claims.Tenant == nil || verified.Issuer != strings.ReplaceAll(issuer, tenantPlace, *claims.Tenant) {
			return nil, errors.New("another issuer")
		}
		if claims.Object != nil {
			answer.Subject = *claims.Tenant + "." + *claims.Object
		}
	}
	return answer, nil
}
