package connections

import (
	"errors"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

// What each call site answers of a refusal of internal/oauth (*oauth.Error):
// every refusal converted explicitly, and anything that is not one, or
// names a code that is not an OAuth code, failing closed.

// endpointError is what the token, revocation and pushed request endpoints
// answer of a refusal: its code, and invalid_request for anything else.
func endpointError(err error) error {
	var r *oauth.Error
	if errors.As(err, &r) && r.Code.Valid() {
		return oauthErr(r.Code)
	}
	return oauthErr(oauth.ErrInvalidRequest)
}

// shownClient is what the person is shown of a refusal whose redirect URI
// was not proven: the client's fixed problem, and redirect_uri_invalid for
// anything else, which is never redirected.
func shownClient(err error) *contracts.ClientError {
	var r *oauth.Error
	if errors.As(err, &r) && r.Code == oauth.ErrInvalidClient {
		if p := contracts.ClientProblem(r.Reason); p.Valid() {
			return contracts.InvalidClient(p)
		}
	}
	return contracts.InvalidClient(contracts.RedirectURIInvalid)
}

// redirectCode is the code a refusal whose redirect URI was proven is sent
// back with: server_error for anything that is not an OAuth refusal.
func redirectCode(err error) oauth.ErrorCode {
	var r *oauth.Error
	if errors.As(err, &r) && r.Code.Valid() {
		return r.Code
	}
	return oauth.ErrServerError
}

// jwsKeys are a vendor's keys as jws verifies them: identifier, algorithm
// and key kept, no issuer (the vendors' sets name none).
func jwsKeys(keys []vendors.Key) []jws.Key {
	out := make([]jws.Key, len(keys))
	for i, k := range keys {
		out[i] = jws.Key{ID: k.ID, Algorithm: k.Algorithm, Public: k.Public}
	}
	return out
}
