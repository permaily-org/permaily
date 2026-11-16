package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// modelStatus is a model's section of /status as a release reports it.
func modelStatus(change func(m *attestation.ModelStatus)) *attestation.ModelStatus {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	m := &attestation.ModelStatus{
		Provider: "openai", Endpoint: "https://api.openai.com/v1/decisions", Model: "gpt-6-luna", PromptVersion: "p2-2026-11-15", PromptCache: "off",
		Organization: "org-a33", Project: "proj-a33", MonthLimitUSD: 50, DayAlarmUSD: 10, AccountDayUSD: 0.5, KeySet: true, KeySetAt: at,
		Guard: attestation.ModelGuardOK, Canary: &attestation.CanaryStatus{Baseline: "aa", CheckedAt: at, State: attestation.CanarySame}, Since: at,
		Calls: map[string]int{attestation.ModelCallAnswered: 3, "later_kind": 1},
	}
	if change != nil {
		change(m)
	}
	return m
}

// A33. The model's section: the prompt cache not off fails; every canary
// state but same, a baseline that is not the canary of the release
// verified, an organisation or project not pinned and a guard that asks
// nothing new are warned of.
func TestA33CheckModel(t *testing.T) {
	for _, cache := range []string{"", "on", "explicit", "OFF"} {
		if _, err := checkModel(modelStatus(func(m *attestation.ModelStatus) { m.PromptCache = cache }), "aa", nil); err == nil || !strings.Contains(err.Error(), "prompt cache") {
			t.Errorf("prompt cache %q: %v", cache, err)
		}
	}
	r, err := checkModel(modelStatus(nil), "AA", nil)
	if err != nil || len(r.Warnings) != 0 || r.ExpectedBaseline != "AA" {
		t.Fatalf("a model as it should be: %+v %v", r, err)
	}
	cases := map[string]struct {
		change   func(m *attestation.ModelStatus)
		expected string
		err      error
		want     string
	}{
		"changed":            {func(m *attestation.ModelStatus) { m.Canary.State = attestation.CanaryChanged }, "aa", nil, "may have changed under its name"},
		"failed":             {func(m *attestation.ModelStatus) { m.Canary.State = attestation.CanaryFailed }, "aa", nil, "did not complete"},
		"stale":              {func(m *attestation.ModelStatus) { m.Canary.State = attestation.CanaryStale }, "aa", nil, "for two days"},
		"unreadable":         {func(m *attestation.ModelStatus) { m.Canary.State = attestation.CanaryUnreadable }, "aa", nil, "cannot read the canary's state"},
		"none":               {func(m *attestation.ModelStatus) { m.Canary.State = attestation.CanaryNone }, "aa", nil, "has completed yet"},
		"a state not known":  {func(m *attestation.ModelStatus) { m.Canary.State = "paused" }, "aa", nil, `does not know ("paused")`},
		"no canary":          {func(m *attestation.ModelStatus) { m.Canary = nil }, "aa", nil, "reports no canary"},
		"another baseline":   {nil, "bb", nil, "the canary's baseline is aa, not bb"},
		"no baseline read":   {nil, "", errors.New("download: HTTP 404"), "was not compared: download: HTTP 404"},
		"no organisation":    {func(m *attestation.ModelStatus) { m.Organization = "" }, "aa", nil, "do not pin both"},
		"no project":         {func(m *attestation.ModelStatus) { m.Project = "" }, "aa", nil, "do not pin both"},
		"a guard that holds": {func(m *attestation.ModelStatus) { m.Guard = attestation.ModelGuardMonthLimit }, "aa", nil, "the spend guard says month_limit"},
	}
	for name, c := range cases {
		r, err := checkModel(modelStatus(c.change), c.expected, c.err)
		if err != nil || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], c.want) {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
}

// A33. The baseline is compared with the canary.json of the commit the
// enclave runs, never the verifier's own, the current branch's or the
// latest release's: a release whose canary differs from both passes with
// its own, and is warned of with either of the others.
func TestA33TheCanaryIsTheReleaseVerifieds(t *testing.T) {
	branch, err := os.ReadFile("../../internal/decide/canary.json")
	if err != nil {
		t.Fatal(err)
	}
	release := bytes.Replace(branch, []byte(`"tolerance"`), []byte(`"tolerance" `), 1)
	latest := append(append([]byte(nil), branch...), '\n')
	if bytes.Equal(release, branch) || bytes.Equal(latest, branch) {
		t.Fatal("the three canaries are not three")
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	var asked []string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		body := release
		if !strings.Contains(r.URL.Path, "/"+commit+"/") {
			body = latest
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})}
	fetch := canaryOf(client, "")
	got, err := fetch(context.Background(), commit)
	if err != nil || got != sha(release) || len(asked) != 1 || asked[0] != "https://raw.githubusercontent.com/permaily-org/permaily/"+commit+"/internal/decide/canary.json" {
		t.Fatalf("%s %v %q", got, err, asked)
	}
	host := hostResult{URL: "https://app.permaily.test", Commit: commit, SPKIHash: "ab"}
	for name, c := range map[string]struct {
		baseline string
		warned   bool
	}{"the release's own": {sha(release), false}, "the current branch's": {sha(branch), true}, "the latest release's": {sha(latest), true}} {
		st := &attestation.Status{Commit: commit, Model: modelStatus(func(m *attestation.ModelStatus) { m.Canary.Baseline = c.baseline })}
		r := checkStatus(st, host, imageA, kmsOptions{skip: true, expectedCanary: got})
		if !r.OK || r.Model == nil || (len(r.Model.Warnings) == 1) != c.warned {
			t.Errorf("%s: %+v", name, r.Model)
		}
	}
	// A commit that is not one, a canary GitHub does not serve, and one
	// past its bound are not compared.
	if _, err := fetch(context.Background(), "dev"); err == nil {
		t.Error("compared with the canary of no commit")
	}
	missing := canaryOf(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}, "")
	if _, err := missing(context.Background(), commit); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("a canary not served: %v", err)
	}
	huge := canaryOf(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(make([]byte, maxCanary+1))), Header: http.Header{}}, nil
	})}, "")
	if _, err := huge(context.Background(), commit); err == nil {
		t.Error("a canary past its bound was read")
	}
	down := canaryOf(&http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}, "")
	if _, err := down(context.Background(), commit); err == nil {
		t.Error("read a canary offline")
	}
	// From the user's own checkout of that commit, read offline.
	file := filepath.Join(t.TempDir(), "canary.json")
	if err := os.WriteFile(file, release, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := canaryOf(nil, file)(context.Background(), "dev"); err != nil || got != sha(release) {
		t.Fatalf("from a file: %s %v", got, err)
	}
	if _, err := canaryOf(nil, filepath.Join(t.TempDir(), "none.json"))(context.Background(), commit); err == nil {
		t.Error("read a file that is not there")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The model is checked whatever else is skipped, and a prompt cache not off
// fails the host.
func TestCheckStatusChecksTheModel(t *testing.T) {
	host := hostResult{URL: "https://app.permaily.test", Commit: "abc", SPKIHash: "ab"}
	on := &attestation.Status{Commit: "abc", Model: modelStatus(func(m *attestation.ModelStatus) { m.PromptCache = "on" })}
	for name, opts := range map[string]kmsOptions{"every check": {}, "-skip-kms-policy": {skip: true}} {
		if r := checkStatus(on, host, imageA, opts); r.OK || r.Model != nil || !strings.HasPrefix(r.Error, "model: ") {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := checkStatus(&attestation.Status{Commit: "abc"}, host, imageA, kmsOptions{skip: true}); !r.OK || r.Model != nil {
		t.Fatalf("an image without the model: %+v", r)
	}
}

func TestPrintModel(t *testing.T) {
	var b bytes.Buffer
	printModel(&b, nil)
	if b.Len() != 0 {
		t.Fatalf("no model printed %q", b.String())
	}
	r, _ := checkModel(modelStatus(func(m *attestation.ModelStatus) {
		m.Organization, m.KeySet = "", false
		m.Canary.State, m.Canary.ChangeSeenAt = attestation.CanaryChanged, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	}), "aa", nil)
	printModel(&b, r)
	for _, want := range []string{
		"model: openai gpt-6-luna at https://api.openai.com/v1/decisions, prompt p2-2026-11-15, prompt cache off",
		"organisation not pinned, project proj-a33; zero data retention not approved",
		"at most 50 USD a month", "past 10 USD a day", "0.5 USD a day for one account",
		"no key set: the model is off; guard ok",
		"canary changed (baseline aa), last check completed 2026-11-16T09:00:00Z, last change seen 2026-11-15",
		"answered 3, kept 0", "later_kind 1",
		"warning: the model's canary saw an answer move", "warning: the requests do not pin both",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	r, _ = checkModel(modelStatus(func(m *attestation.ModelStatus) { m.ZeroDataRetention, m.Canary.CheckedAt = true, time.Time{} }), "aa", nil)
	printModel(&b, r)
	for _, want := range []string{"zero data retention approved", "a key is set, since 2026-11-16T09:00:00Z", "last check completed never"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "warning") {
		t.Errorf("warned of a model as it should be:\n%s", b.String())
	}
}
