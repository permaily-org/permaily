package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
	"github.com/permaily-org/permaily/pkg/provenance"
)

func TestHandoff(t *testing.T) {
	var got keys.Handoff
	var fail error
	var logs bytes.Buffer
	h := httpapi.Handoff(func(_ context.Context, in keys.Handoff) (*keys.HandoffResult, error) {
		got = in
		if fail != nil {
			return nil, fail
		}
		return &keys.HandoffResult{Image: "bb", Commit: "c1", KeyARN: "arn:aws:kms:us-east-2:1:key/k", Created: true}, nil
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/handoff", strings.NewReader(body)))
		return rec
	}
	// base64 of "to-pcrs", "to-bundle", "from-pcrs", "from-bundle".
	const good = `{"to_pcrs":"dG8tcGNycw==","to_bundle":"dG8tYnVuZGxl","from_pcrs":"ZnJvbS1wY3Jz","from_bundle":"ZnJvbS1idW5kbGU="}`

	rec := post(good)
	var res keys.HandoffResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || !res.Created || res.Image != "bb" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if string(got.ToPCRs) != "to-pcrs" || string(got.ToBundle) != "to-bundle" || string(got.FromPCRs) != "from-pcrs" || string(got.FromBundle) != "from-bundle" {
		t.Fatalf("request %q", got)
	}

	for name, body := range map[string]string{
		"not JSON":         `nope`,
		"a missing field":  `{"to_pcrs":"dG8tcGNycw==","to_bundle":"dG8tYnVuZGxl","from_pcrs":"ZnJvbS1wY3Jz"}`,
		"not base64":       `{"to_pcrs":"***","to_bundle":"dG8tYnVuZGxl","from_pcrs":"ZnJvbS1wY3Jz","from_bundle":"ZnJvbS1idW5kbGU="}`,
		"an empty request": `{}`,
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
	}
	if rec := post(`{"to_pcrs":"` + strings.Repeat("A", 2<<20) + `"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/handoff", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d", rec.Code)
	}

	// A refusal says why; a failure of the infrastructure does not.
	for _, c := range []struct {
		err  error
		code int
		show bool
	}{
		{fmt.Errorf("the next release: %w of x/y", provenance.ErrNotARelease), http.StatusUnprocessableEntity, true},
		{fmt.Errorf("%w: bad signature", provenance.ErrUntrusted), http.StatusUnprocessableEntity, true},
		{fmt.Errorf("%w: bundle", provenance.ErrMalformed), http.StatusUnprocessableEntity, true},
		{keys.ErrDowngrade, http.StatusUnprocessableEntity, true},
		{keys.ErrNotThisImage, http.StatusUnprocessableEntity, true},
		{fmt.Errorf("key k: %w: the policy can be replaced", kmspolicy.ErrNotSealed), http.StatusUnprocessableEntity, true},
		{keys.ErrKeyAccount, http.StatusUnprocessableEntity, true},
		{keys.ErrDebugMode, http.StatusServiceUnavailable, false},
		{keys.ErrAwaitingHandoff, http.StatusServiceUnavailable, false},
		{errors.New("dynamodb: secret detail"), http.StatusBadGateway, false},
	} {
		fail = c.err
		logs.Reset()
		rec := post(good)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != c.code || strings.Contains(string(body), c.err.Error()) != c.show {
			t.Fatalf("%v: status %d: %s", c.err, rec.Code, body)
		}
		if c.code == http.StatusBadGateway && !strings.Contains(logs.String(), "secret detail") {
			t.Fatalf("the cause is not in the log: %s", logs.String())
		}
	}
}

func TestTrustRoot(t *testing.T) {
	var got provenance.Update
	var fail error
	h := httpapi.TrustRoot(func(u provenance.Update) (*attestation.SigstoreStatus, error) {
		got = u
		if fail != nil {
			return nil, fail
		}
		return &attestation.SigstoreStatus{TrustedRootSHA256: "ab", TUFRootVersion: 17, TUFTargetsVersion: 15}, nil
	})
	call := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/v1/admin/trust-root", strings.NewReader(body)))
		return rec
	}
	// base64 of "root-17", "targets" and "trusted-root".
	const good = `{"roots":["cm9vdC0xNw=="],"targets":"dGFyZ2V0cw==","trusted_root":"dHJ1c3RlZC1yb290"}`

	rec := call(http.MethodPost, good)
	var st attestation.SigstoreStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.TUFRootVersion != 17 || st.TUFTargetsVersion != 15 || st.TrustedRootSHA256 != "ab" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(got.Roots) != 1 || string(got.Roots[0]) != "root-17" || string(got.Targets) != "targets" || string(got.TrustedRoot) != "trusted-root" {
		t.Fatalf("request %q", got)
	}
	// Roots are only needed when there are newer ones.
	if rec := call(http.MethodPost, `{"targets":"dGFyZ2V0cw==","trusted_root":"dHJ1c3RlZC1yb290"}`); rec.Code != http.StatusOK || got.Roots != nil {
		t.Fatalf("without roots: status %d, %q", rec.Code, got.Roots)
	}

	for name, body := range map[string]string{
		"not JSON":                  `nope`,
		"no targets":                `{"trusted_root":"dHJ1c3RlZC1yb290"}`,
		"no trust root":             `{"targets":"dGFyZ2V0cw=="}`,
		"not base64":                `{"targets":"***","trusted_root":"dHJ1c3RlZC1yb290"}`,
		"roots that are not a list": `{"roots":"cm9vdC0xNw==","targets":"dGFyZ2V0cw==","trusted_root":"dHJ1c3RlZC1yb290"}`,
	} {
		if rec := call(http.MethodPost, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
	}
	if rec := call(http.MethodPost, `{"targets":"`+strings.Repeat("A", 5<<20)+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: status %d", rec.Code)
	}
	if rec := call(http.MethodGet, ""); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: status %d", rec.Code)
	}

	// A refusal says why: the reason names versions of public metadata.
	fail = fmt.Errorf("%w: TUF targets 12 is not newer than 14, the one in use", provenance.ErrUntrusted)
	if rec := call(http.MethodPost, good); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not newer than 14") {
		t.Fatalf("refusal: status %d: %s", rec.Code, rec.Body)
	}
}
