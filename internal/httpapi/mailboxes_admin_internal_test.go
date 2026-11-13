package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An admin the panel carries that cannot act on mailboxes (an AdminOperator
// that is no MailboxOperator) is answered not_found by every route of
// mailboxes, as an endpoint that is not there, and nothing is asked of it.
func TestAnAdminWithoutMailboxesFindsNoMailboxRoute(t *testing.T) {
	mux := http.NewServeMux()
	adminMailboxRoutes(mux, "/api/v1/admin", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var admin AdminOperator = struct{ AdminOperator }{}
	if _, ok := admin.(MailboxOperator); ok {
		t.Fatal("the admin of this test acts on mailboxes")
	}
	box := "/api/v1/admin/mailboxes/mbx_" + strings.Repeat("a", 26)
	for _, r := range [][2]string{
		{"GET", "/api/v1/admin/accounts/acc_" + strings.Repeat("a", 26) + "/mailboxes"},
		{"GET", "/api/v1/admin/mailboxes?address=someone%40inbox.permaily.test"},
		{"POST", box + "/pause"}, {"POST", box + "/resume"},
	} {
		req := httptest.NewRequest(r[0], "https://app.permaily.test"+r[1], nil)
		req = req.WithContext(context.WithValue(req.Context(), adminKey{}, admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s %s: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}
}
