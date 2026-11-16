package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// PR #131's review, finding 5: the error body's details are typed; a
// formula's are written byte for byte as before, an invalid client's are its
// fixed reason alone, and none is no details member at all.
func TestTheErrorBodysDetails(t *testing.T) {
	cases := []struct {
		name string
		code string
		d    contracts.ErrorDetails
		want string
	}{
		{"none", "not_found", contracts.ErrorDetails{}, `{"error":{"code":"not_found","message":"m"}}`},
		{"a formula", "invalid_formula", contracts.ErrorDetails{FormulaProblem: contracts.FormulaProblem{At: "all[0]", Reason: contracts.FormulaEmptyGroup}},
			`{"error":{"code":"invalid_formula","message":"m","details":{"at":"all[0]","reason":"empty_group"}}}`},
		{"an invalid client", "invalid_client", contracts.InvalidClient(contracts.ClientDocumentInvalid).Details(),
			`{"error":{"code":"invalid_client","message":"m","details":{"reason":"client_document_invalid"}}}`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeErrorWith(rec, 400, c.code, "m", c.d)
		if got := rec.Body.String(); got != c.want+"\n" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %s", c.name, got)
		}
	}
}
