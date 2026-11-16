package oauth

import (
	"slices"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// ServiceDocumentation is where a vendor reads how to connect.
const ServiceDocumentation = "https://permaily.com/developers/"

// Metadata is the authorization server's metadata (RFC 8414) the image
// serves at PathServerMetadata: every value fixed by the image (ADR 0030,
// "Metadata"). There is no registration endpoint, no device endpoint and
// nothing of OpenID: this is not an OpenID provider.
func Metadata(c Config) ServerMetadata {
	auth := []string{AuthNone, AuthPrivateKeyJWT}
	scopes := make([]string, 0, len(ScopeParts))
	for _, sp := range ScopeParts {
		scopes = append(scopes, sp.Scope)
	}
	return ServerMetadata{
		Issuer:                             c.Issuer,
		AuthorizationEndpoint:              c.AppOrigin + PathAuthorize,
		TokenEndpoint:                      c.Issuer + PathToken,
		RevocationEndpoint:                 c.Issuer + PathRevoke,
		PushedAuthorizationRequestEndpoint: c.Issuer + PathPushed,
		ResponseTypesSupported:             []string{"code"},
		ResponseModesSupported:             []string{"query"},
		GrantTypesSupported:                []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:      []string{"S256"},
		TokenEndpointAuthMethodsSupported:  auth,
		TokenEndpointAuthSigningAlgs:       []string{"RS256", "ES256"},
		RevocationEndpointAuthMethods:      slices.Clone(auth),
		ScopesSupported:                    scopes,
		ClientIDMetadataDocumentSupported:  true,
		AuthorizationResponseISSSupported:  true,
		RequirePushedAuthorizationRequests: false,
		ServiceDocumentation:               ServiceDocumentation,
	}
}

// ResourceMetadataFor is a resource's protected resource metadata (RFC
// 9728): the default scopes, the header alone as the bearer's place.
func ResourceMetadataFor(c Config, r contracts.Resource) ResourceMetadata {
	return ResourceMetadata{
		Resource:               resource(c, r),
		AuthorizationServers:   []string{c.Issuer},
		ScopesSupported:        slices.Clone(DefaultScopes),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Permaily",
	}
}

// resource is a resource's identifier.
func resource(c Config, r contracts.Resource) string {
	if r == contracts.ResourceAPI {
		return c.APIResource
	}
	return c.MCPResource
}

// ResourceMetadataURL is where a resource's metadata is: its origin,
// PathResourceMetadata and its path (RFC 9728, section 3.1).
func ResourceMetadataURL(c Config, r contracts.Resource) string {
	id := resource(c, r)
	rest := strings.TrimPrefix(id, "https://")
	host, path, _ := strings.Cut(rest, "/")
	return id[:len(id)-len(rest)] + host + PathResourceMetadata + "/" + path
}

// Challenge is the WWW-Authenticate value of a 401 or a 403 of resource r
// (RFC 6750, section 3; RFC 9728, section 5.1): code "" for a request
// without a token, ErrInvalidToken for one whose token is not valid,
// ErrInsufficientScope for one that needs a part the connection does not
// have, with every scope the client then needs. scopes are said in
// ScopeParts' order, each once, unknown ones never (nothing a client sent is
// echoed); none means the DefaultScopes.
func Challenge(c Config, r contracts.Resource, code ErrorCode, scopes []string) string {
	var said []string
	for _, sp := range ScopeParts {
		if slices.Contains(scopes, sp.Scope) {
			said = append(said, sp.Scope)
		}
	}
	if len(said) == 0 {
		said = DefaultScopes
	}
	var b strings.Builder
	b.WriteString("Bearer ")
	if code != "" {
		b.WriteString(`error="` + string(code) + `", `)
	}
	b.WriteString(`resource_metadata="` + ResourceMetadataURL(c, r) + `", scope="` + strings.Join(said, " ") + `"`)
	return b.String()
}
