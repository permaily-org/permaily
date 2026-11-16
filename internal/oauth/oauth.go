// Package oauth is what the authorization server of the API and MCP decides
// from its arguments alone (ADR 0030, which settles ADR 0007): the metadata it
// publishes, the rules a client_id URL and a client metadata document must
// keep (CIMD), the checks of an authorization request, PKCE, redirect URIs
// (loopback on any port, RFC 8252), resources (RFC 8707), scopes, the shape
// and hashing of codes and tokens, and the check of a client's signed
// assertion (private_key_jwt, RFC 7523) against keys it is given.
//
// It is pure: no HTTP, no storage, no clock of its own, no network. Fetching
// a vendor's document is internal/vendors/cimd's; keeping codes, tokens and
// connections is internal/services/connections'; the routes are
// internal/httpapi's and internal/mcpapi's. depguard's oauth-is-pure lists
// what it may import.
//
// This file is the seam, committed with the design: the constants and types
// the pull requests of ADR 0030 share. The functions are written by its pull
// request 1; "The contract and the seams" there gives their signatures.
package oauth

import (
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Lifetimes (ADR 0030, "Codes and tokens"). Every one is also bounded by the
// connection's own end, read when the token is used, never when it is made.
const (
	// AccessTokenTTL is how long an access token is taken. Claude Code
	// refreshes before every call when fewer than 3 to 10 minutes are left
	// (the spike); an hour spares a refresh per call, and every request is
	// checked against the connection anyway.
	AccessTokenTTL = time.Hour
	// RefreshTokenTTL is how long a refresh token is taken; each use
	// rotates it.
	RefreshTokenTTL = 30 * 24 * time.Hour
	// RefreshGrace is how long a refresh token or a code used once more
	// answers the same pair its first use made (two enclaves, a client's
	// retry); after it, a second use ends the connection (EndedByReuse).
	RefreshGrace = 30 * time.Second
	// CodeTTL is how long an authorization code is taken.
	CodeTTL = 60 * time.Second
	// RequestTTL is how long an authorization request waits for the
	// person's decision, signing in included.
	RequestTTL = 15 * time.Minute
	// PushedRequestTTL is how long a pushed request's request_uri is taken
	// (RFC 9126).
	PushedRequestTTL = 60 * time.Second
	// ConnectionFresh is how long an enclave may answer from what it read of
	// a connection's record and of its account's standing: a revocation
	// through one enclave stops its tokens there at once and in any other
	// within this time (the owner's decision 6).
	ConnectionFresh = 5 * time.Second
	// AssertionMaxLife bounds how far in the future a client assertion's
	// exp may be; ClockSkew is what is allowed of an iat or exp past.
	AssertionMaxLife = 10 * time.Minute
	ClockSkew        = time.Minute
)

// Connection lifetimes (ADR 0019, "Every connection ends").
const (
	ConnectionDefaultDays = 90
	ConnectionMaxDays     = 365
)

// Prefixes of what the server hands out. Each is followed by 32 random bytes
// in unpadded base64url (43 characters). A secret is kept only as Hash of
// it, never as itself.
const (
	PrefixAccess  = "pma_" // an access token
	PrefixRefresh = "pmr_" // a refresh token
	PrefixCode    = "pmc_" // an authorization code
	PrefixRequest = "pmq_" // an authorization request waiting for the person
	PrefixPushed  = "pmp_" // a pushed request, after RequestURIPrefix
	PrefixCursor  = "pmk_" // a vendor's page cursor (sealed, not a secret)
	// RequestURIPrefix starts a pushed request's request_uri (RFC 9126).
	RequestURIPrefix = "urn:ietf:params:oauth:request_uri:"
	// SecretBytes is how many random bytes follow a prefix.
	SecretBytes = 32
)

// Kind names what a secret is in its hash's domain separation and in the
// partition it is kept under.
type Kind string

const (
	KindAccess  Kind = "access"
	KindRefresh Kind = "refresh"
	KindCode    Kind = "code"
	KindRequest Kind = "request"
	KindPushed  Kind = "pushed"
	KindJTI     Kind = "jti" // a client assertion's jti, with its client_id
)

// Scopes are the parts (ADR 0030, "Scopes are the parts", the owner's
// decision 8): coarse, asked up front, one per part. ScopeSender is part of
// every connection, asked or not.
const (
	ScopeSender      = "mail.sender"
	ScopeSubject     = "mail.subject"
	ScopeBody        = "mail.body"
	ScopeAttachments = "mail.attachments"
)

// ScopeParts maps each scope to its part, in the order parts are shown.
var ScopeParts = []struct {
	Scope string
	Part  contracts.Part
}{
	{ScopeSender, contracts.PartSender},
	{ScopeSubject, contracts.PartSubject},
	{ScopeBody, contracts.PartBody},
	{ScopeAttachments, contracts.PartAttachments},
}

// DefaultScopes are what protected resource metadata lists in
// scopes_supported and what a 401's challenge names: what a client asks for
// when it is told nothing else. mail.attachments is accepted when asked and
// named by a step-up, never offered first (ADR 0019: attachments are off
// unless asked for).
var DefaultScopes = []string{ScopeSender, ScopeSubject, ScopeBody}

// Paths and identifiers, under the hosts the image compiles in (Config).
const (
	// PathAuthorize is the authorization endpoint, on the app's host and
	// outside /api/: a vendor reaches it by a navigation from another site,
	// which the session cookie (SameSite=Strict) and the cross-site rule of
	// /api/ do not take (the spike).
	PathAuthorize = "/oauth/authorize"
	// PathToken, PathRevoke and PathPushed are on the issuer's host, which
	// has no cookie.
	PathToken  = "/oauth/token" //nolint:gosec // G101: a path, not a credential
	PathRevoke = "/oauth/revoke"
	PathPushed = "/oauth/par"
	// PathServerMetadata is RFC 8414's, at the issuer's root.
	PathServerMetadata = "/.well-known/oauth-authorization-server"
	// PathResourceMetadata is RFC 9728's, followed by the resource's path;
	// the MCP host also answers it at its root.
	PathResourceMetadata = "/.well-known/oauth-protected-resource"
	// PathAPI and PathMCP are the resources' paths: https://api.<domain>/v1
	// and https://mcp.<domain>/mcp.
	PathAPI = "/v1"
	PathMCP = "/mcp"
)

// Config is what the image compiles in for the server: its hosts. The issuer
// is permanent: clients key what they keep by it (the spike).
type Config struct {
	// Issuer is "https://api.<domain>".
	Issuer string
	// AppOrigin is "https://app.<domain>", where PathAuthorize is.
	AppOrigin string
	// APIResource and MCPResource are the two resources' identifiers,
	// "https://api.<domain>/v1" and "https://mcp.<domain>/mcp".
	APIResource string
	MCPResource string
}

// ServerMetadata is the authorization server's metadata (RFC 8414), as
// served at PathServerMetadata. Its values are fixed by the image; ADR 0030,
// "Metadata", lists them.
type ServerMetadata struct {
	Issuer                             string   `json:"issuer"`
	AuthorizationEndpoint              string   `json:"authorization_endpoint"`
	TokenEndpoint                      string   `json:"token_endpoint"`
	RevocationEndpoint                 string   `json:"revocation_endpoint"`
	PushedAuthorizationRequestEndpoint string   `json:"pushed_authorization_request_endpoint"`
	ResponseTypesSupported             []string `json:"response_types_supported"`
	ResponseModesSupported             []string `json:"response_modes_supported"`
	GrantTypesSupported                []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported      []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported  []string `json:"token_endpoint_auth_methods_supported"`
	TokenEndpointAuthSigningAlgs       []string `json:"token_endpoint_auth_signing_alg_values_supported"`
	RevocationEndpointAuthMethods      []string `json:"revocation_endpoint_auth_methods_supported"`
	ScopesSupported                    []string `json:"scopes_supported"`
	ClientIDMetadataDocumentSupported  bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseISSSupported  bool     `json:"authorization_response_iss_parameter_supported"`
	RequirePushedAuthorizationRequests bool     `json:"require_pushed_authorization_requests"`
	ServiceDocumentation               string   `json:"service_documentation,omitempty"`
}

// ResourceMetadata is a protected resource's metadata (RFC 9728).
type ResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
}

// ClientDocument is what a client metadata document says that the server
// reads; every other member is ignored. ADR 0030, "Clients", says what each
// must be.
type ClientDocument struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	TokenEndpointAuthAlg    string   `json:"token_endpoint_auth_signing_alg"`
	JWKSURI                 string   `json:"jwks_uri"`
	ApplicationType         string   `json:"application_type"`
}

// Client authentication methods the server takes.
const (
	AuthNone          = "none"
	AuthPrivateKeyJWT = "private_key_jwt"
	// AssertionType is client_assertion_type for private_key_jwt.
	AssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// Limits of what a vendor sends and what is fetched of it.
const (
	// ClientIDMaxBytes bounds a client_id URL.
	ClientIDMaxBytes = 512
	// DocumentMaxBytes bounds a client metadata document; JWKSMaxBytes a
	// key set. The documents of 2026-11-16 are 317 to 590 bytes.
	DocumentMaxBytes = 8 << 10
	JWKSMaxBytes     = 16 << 10
	// ClientNameMaxRunes bounds what is shown of client_name; a longer one
	// is cut, never refused.
	ClientNameMaxRunes = 64
	// MaxRedirectURIs bounds a document's redirect_uris.
	MaxRedirectURIs = 16
	// StateMaxBytes bounds state; a code_challenge is 43 to 128 characters.
	StateMaxBytes = 512
	// FormMaxBytes bounds the body of the token, revocation and pushed
	// request endpoints.
	FormMaxBytes = 64 << 10
)

// ErrorCode is an OAuth error code (RFC 6749, 6750, 7009, 8707, 9126).
type ErrorCode string

const (
	ErrInvalidRequest       ErrorCode = "invalid_request"
	ErrInvalidClient        ErrorCode = "invalid_client"
	ErrInvalidGrant         ErrorCode = "invalid_grant"
	ErrUnauthorizedClient   ErrorCode = "unauthorized_client"
	ErrUnsupportedGrantType ErrorCode = "unsupported_grant_type"
	ErrUnsupportedResponse  ErrorCode = "unsupported_response_type"
	ErrInvalidScope         ErrorCode = "invalid_scope"
	ErrInvalidTarget        ErrorCode = "invalid_target"
	ErrAccessDenied         ErrorCode = "access_denied"
	ErrServerError          ErrorCode = "server_error"
	ErrTemporarily          ErrorCode = "temporarily_unavailable"
	ErrInvalidToken         ErrorCode = "invalid_token"
	ErrInsufficientScope    ErrorCode = "insufficient_scope"
)

// AuthorizeParams is an authorization request as the server reads it, from
// the query string or a pushed request. Query and Since come only from a
// pushed request (ADR 0030, "Pushed requests").
type AuthorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               []string
	RequestURI          string
	// Query is the vendor's formula, as sent (a pushed request's "query").
	Query []byte
	// Since is a pushed request's "mail_since": "new" or an RFC 3339 time.
	Since string
	// EndsInDays is a pushed request's "ends_in_days", 1 to 365; 0 when not
	// sent.
	EndsInDays int
}

// Grant is what a valid access token stands for, once the token, its
// connection and its account have been checked (internal/services/connections).
// The routes and the MCP tools are given it and nothing of the token.
type Grant struct {
	Connection string
	Account    string
	ClientID   string
	Vendor     string
	Resource   contracts.Resource
	Parts      []contracts.Part
	Scopes     []string
}
