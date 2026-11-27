// Package graphsim is a simulated Microsoft identity platform and Microsoft
// Graph mail API for ADR 0042's spike items 4, 5 and 8 (Microsoft side).
//
// Outlook stays off (the owner's decision 18) and no Microsoft application is
// registered, so nothing here was observed on Microsoft's servers. Every
// scripted answer names the documentation page it comes from and says whether
// that page documents it ("as documented") or is silent, in which case the
// answer is this simulator's assumption and the question goes to T201, the
// real check before Outlook is switched on.
//
// It is never imported by the main module nor deployed.
package graphsim

// Status says how far the documentation backs a scripted answer.
type Status string

const (
	// Documented: the answer's shape is taken from the cited page.
	Documented Status = "as documented"
	// Undocumented: the cited page (or every page read) is silent; the answer
	// is an assumption and the question is listed for T201.
	Undocumented Status = "not documented (T201)"
)

// Script is one scripted answer of the simulator.
type Script struct {
	ID     string
	What   string
	Source string
	Status Status
}

// Documentation pages, read on 2026-11-27.
const (
	docAuthCode     = "https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow"
	docOIDC         = "https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc"
	docIDToken      = "https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference"
	docErrorCodes   = "https://learn.microsoft.com/en-us/entra/identity-platform/reference-error-codes"
	docRefresh      = "https://learn.microsoft.com/en-us/entra/identity-platform/refresh-tokens"
	docScopes       = "https://learn.microsoft.com/en-us/entra/identity-platform/scopes-oidc"
	docMailFolder   = "https://learn.microsoft.com/en-us/graph/api/resources/mailfolder?view=graph-rest-1.0"
	docMessageDelta = "https://learn.microsoft.com/en-us/graph/api/message-delta?view=graph-rest-1.0"
	docDeltaMsgs    = "https://learn.microsoft.com/en-us/graph/delta-query-messages"
	docDelta        = "https://learn.microsoft.com/en-us/graph/delta-query-overview"
	docFolderDelta  = "https://learn.microsoft.com/en-us/graph/api/mailfolder-delta?view=graph-rest-1.0"
	docImmutable    = "https://learn.microsoft.com/en-us/graph/outlook-immutable-id"
	docErrors       = "https://learn.microsoft.com/en-us/graph/errors"
	docThrottling   = "https://learn.microsoft.com/en-us/graph/throttling"
	docLimits       = "https://learn.microsoft.com/en-us/graph/throttling-limits"
	docFolderDelete = "https://learn.microsoft.com/en-us/graph/api/mailfolder-delete?view=graph-rest-1.0"
	docFolderMove   = "https://learn.microsoft.com/en-us/graph/api/mailfolder-move?view=graph-rest-1.0"
	docMIME         = "https://learn.microsoft.com/en-us/graph/outlook-get-mime-message"
)

// Scripts lists every scripted answer; notes.md summarises them by item.
var Scripts = []Script{
	// Item 4: Microsoft's trip.
	{"token-response", "code redeemed: access_token, token_type Bearer, expires_in, scope, refresh_token, id_token", docAuthCode, Documented},
	{"token-scope-optional", "`scope` is optional; omitted means the scopes requested on the first leg", docAuthCode, Documented},
	{"token-scope-example-form", "the documented example's `scope` is lower-case and percent-encoded (\"https%3A%2F%2Fgraph.microsoft.com%2Fmail.read\")", docAuthCode, Documented},
	{"token-scope-oidc-listed", "whether `openid`, `email`, `profile`, `offline_access` are echoed in a code redemption's `scope`", docAuthCode, Undocumented},
	{"refresh-needs-offline-access", "refresh_token only when `offline_access` was requested", docAuthCode, Documented},
	{"id-token-needs-openid", "id_token only when `openid` was requested, on redemption and on refresh", docAuthCode, Documented},
	{"refresh-rotates", "each refresh returns a new refresh token; the old one is not revoked", docRefresh, Documented},
	{"refresh-lifetime", "90 days by default (24 hours for SPA); expiry by inactivity is AADSTS700082 inside invalid_grant", docRefresh, Documented},
	{"refresh-revocation-table", "a confidential client's refresh token survives the user's own password change and SSPR; an admin reset in the Entra or M365 admin center and any revocation end it", docRefresh, Documented},
	{"refresh-tenant-path", "a refresh at /{tid}/oauth2/v2.0/token for a personal account (tid 9188040d-…)", docOIDC, Undocumented},
	{"id-token-claims", "v2.0 ID token: aud, iss ending /v2.0, iat, nbf, exp, sub (pairwise), tid, ver, nonce", docIDToken, Documented},
	{"id-token-oid-needs-profile", "`oid` and `preferred_username` need the `profile` scope", docIDToken, Documented},
	{"id-token-email", "`email` only with the `email` scope (or optional claim) and only when the account has one; mutable, unverified", docIDToken + " " + docScopes, Documented},
	{"id-token-personal-tid", "personal accounts carry tid 9188040d-6c67-4c5b-b112-36a304b66dad", docIDToken, Documented},
	{"jwks", "signing keys at /{tenant}/discovery/v2.0/keys", docOIDC, Documented},
	{"token-error-shape", "error, error_description, error_codes, timestamp, trace_id, correlation_id; react on `error`, never on the description or the code numbers", docAuthCode + " " + docErrorCodes, Documented},
	{"code-reused", "a redeemed code again: invalid_grant, AADSTS54005", docErrorCodes, Documented},
	{"interaction-required", "MFA or conditional access at the token endpoint: interaction_required, AADSTS50076", docAuthCode + " " + docErrorCodes, Documented},
	{"blocked-by-ca", "AADSTS53003 BlockedByConditionalAccess; the `error` value that carries it", docErrorCodes, Undocumented},
	{"authorize-error-shape", "an authorize error at the redirect carries only `error` and `error_description` (no error_codes)", docAuthCode, Documented},
	{"admin-approval-redirect", "what the redirect carries when a work account needs an administrator's approval (AADSTS90094/65001 appear only inside error_description, if at all)", docAuthCode + " " + docErrorCodes, Undocumented},
	{"graph-403-ca", "Graph 403 with a conditional-access challenge (`error=insufficient_claims`)", docErrors, Documented},

	// Item 5: Graph's folders and delta.
	{"well-known-names", "the 17 well-known folder names; they work whatever the mailbox's locale", docMailFolder, Documented},
	{"localised-display-names", "the display names a localised mailbox gives well-known folders", docMailFolder, Undocumented},
	{"well-known-immovable", "that well-known folders cannot be moved, renamed or deleted (move takes any destination; no exception listed)", docFolderMove + " " + docFolderDelete, Undocumented},
	{"folder-delete-destination", "a deleted folder goes into deleted items with its messages' parentFolderId unchanged", docFolderDelete, Undocumented},
	{"folder-ids-constant", "folder ids never change (containers do not need immutable ids)", docImmutable, Documented},
	{"folder-list-hidden", "listing hides hidden folders unless includeHiddenFolders=true", docMailFolder, Documented},
	{"delta-per-folder", "message delta is per folder; child folders are tracked separately", docDeltaMsgs, Documented},
	{"delta-links", "a page has @odata.nextLink ($skiptoken) or @odata.deltaLink ($deltatoken), never both", docDelta, Documented},
	{"delta-removed", "a message deleted or moved out of the folder: {\"id\", \"@removed\": {\"reason\": \"deleted\"}}", docMessageDelta + " " + docDeltaMsgs, Documented},
	{"delta-replays", "the same change may appear again in later responses", docDelta, Documented},
	{"delta-filter-5000", "with $filter on receivedDateTime a delta returns at most 5,000 messages; unfiltered has no stated cap", docDeltaMsgs, Documented},
	{"delta-maxpagesize", "Prefer: odata.maxpagesize sets the page size (default and maximum not stated)", docMessageDelta, Documented},
	{"delta-token-expired", "an expired token: a 40X with code syncStateNotFound (exact status not stated)", docDelta, Documented},
	{"delta-reset-410", "410 Gone with a Location header holding an empty $deltatoken: start a full synchronisation", docDelta, Documented},
	{"delta-reset-code", "the error code of the 410 (resyncRequired is reported only in community answers)", docDelta, Undocumented},
	{"folder-delta-quota", "mailFolder delta may answer 403 ErrorQuotaExceeded when the mailbox is over quota", docFolderDelta, Documented},
	{"immutable-ids", "Prefer: IdType=\"ImmutableId\" applies per request; the id survives moves between folders, not a move to the archive mailbox nor export and import", docImmutable, Documented},
	{"immutable-delta-links", "delta links work with both id formats", docImmutable, Documented},
	{"regular-ids-change", "a message's regular id changes when it moves", docImmutable, Documented},
	{"item-not-found", "a message deleted for good: 404 with code ErrorItemNotFound", docErrors, Undocumented},
	{"malformed-id", "a malformed message id: 400 ErrorInvalidIdMalformed", docErrors, Undocumented},
	{"error-shape", "{\"error\": {\"code\", \"message\", \"innerError\"}}; code against `code`, never `message`", docErrors, Documented},
	{"mime-value", "GET /me/messages/{id}/$value returns the MIME message", docMIME, Documented},
	{"mime-does-not-mark-read", "that reading $value leaves isRead unchanged", docMIME, Undocumented},
	{"throttle-429", "429 with Retry-After (seconds) and code TooManyRequests; wait, then retry", docThrottling, Documented},
	{"outlook-limits", "per app and mailbox: 10,000 requests in 10 minutes, 4 concurrent", docLimits, Documented},
	{"concurrency-429-form", "the answer to a fifth concurrent request", docLimits, Undocumented},
	{"retry-after-503", "503 may carry Retry-After", docErrors, Documented},

	// Item 8: HTTP versions.
	{"http2", "whether login.microsoftonline.com and graph.microsoft.com negotiate h2", docThrottling, Undocumented},
}
