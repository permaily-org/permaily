package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// canaryURL is where the canary of a commit is read from when no -canary
// file is given: the file compiled into the image built from that commit.
const canaryURL = "https://raw.githubusercontent.com/permaily-org/permaily/%s/internal/decide/canary.json"

// maxCanary bounds the canary that is read.
const maxCanary = 1 << 20

// modelResult is what the verified enclave reports of the model it asks
// (ADR 0028), after it passed checkModel, and what the verifier warns of.
type modelResult struct {
	*attestation.ModelStatus
	// ExpectedBaseline is the SHA-256 of the canary.json of the commit the
	// enclave runs, which its canary's baseline must be; empty when it could
	// not be read.
	ExpectedBaseline string   `json:"expected_canary_baseline,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// canaryOf returns the SHA-256 of the canary.json of a commit: of the file
// given with -canary when there is one (from a checkout of that commit), or
// as GitHub serves it at that commit. It is never the canary of the
// verifier's own build, of the current branch or of the latest release.
func canaryOf(client *http.Client, file string) func(ctx context.Context, commit string) (string, error) {
	return func(ctx context.Context, commit string) (string, error) {
		var data []byte
		if file != "" {
			var err error
			if data, err = os.ReadFile(file); err != nil { //nolint:gosec // path given by the user on purpose
				return "", err
			}
		} else {
			if !fullCommit.MatchString(commit) {
				return "", fmt.Errorf("the enclave names no commit to read its canary at (%q)", commit)
			}
			url := fmt.Sprintf(canaryURL, commit)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return "", err
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
			}
			if data, err = io.ReadAll(io.LimitReader(resp.Body, maxCanary+1)); err != nil {
				return "", err
			}
		}
		if len(data) > maxCanary {
			return "", errors.New("the canary is longer than any the image holds")
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), nil
	}
}

// fullCommit is a commit's full hexadecimal name.
var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// canaryWarnings says what each state of the canary but same means.
var canaryWarnings = map[string]string{
	attestation.CanaryChanged:    "the model's canary saw an answer move from what was measured, or another model answer: the model may have changed under its name. The service goes on; a release that takes the new answers, or a new prompt version, should follow",
	attestation.CanaryFailed:     "the model's latest canary check in this enclave did not complete, and none completed today",
	attestation.CanaryStale:      "no check of the model's canary completed for two days: a change of the model under its name would not be seen",
	attestation.CanaryUnreadable: "the enclave cannot read the canary's state: whoever writes the storage may have deleted or put back an earlier copy of it",
	attestation.CanaryNone:       "no check of the model's canary has completed yet for this release",
}

// checkModel checks what the enclave reports of its model. It fails when the
// prompt cache is not off: a release that turns it on keeps copies of mail
// at OpenAI that the notice a person agreed to does not mention. Everything
// else is warned of, never failed: the canary's state, a baseline that is
// not the canary of the release verified (expected, or why it could not be
// read), an organisation or project the requests do not pin, a guard that
// asks nothing new.
func checkModel(m *attestation.ModelStatus, expected string, expectedErr error) (*modelResult, error) {
	if m.PromptCache != "off" {
		return nil, fmt.Errorf("the enclave says the provider's prompt cache is %q, not off: OpenAI would keep the mail it is sent as cache state, which the model's notice does not say", m.PromptCache)
	}
	r := &modelResult{ModelStatus: m, ExpectedBaseline: expected}
	if c := m.Canary; c == nil {
		r.Warnings = append(r.Warnings, "the enclave reports no canary: a change of the model under its name would not be seen")
	} else {
		if w, ok := canaryWarnings[c.State]; ok {
			r.Warnings = append(r.Warnings, w)
		} else if c.State != attestation.CanarySame {
			r.Warnings = append(r.Warnings, fmt.Sprintf("the model's canary is in a state this verifier does not know (%q)", c.State))
		}
		switch {
		case expectedErr != nil:
			r.Warnings = append(r.Warnings, "the canary of the release verified could not be read, so its baseline was not compared: "+expectedErr.Error())
		case expected != "" && !strings.EqualFold(c.Baseline, expected):
			r.Warnings = append(r.Warnings, fmt.Sprintf("the canary's baseline is %s, not %s, the SHA-256 of the canary.json of the commit this enclave runs: its answers are compared with another release's", c.Baseline, expected))
		}
	}
	if m.Organization == "" || m.Project == "" {
		r.Warnings = append(r.Warnings, "the requests do not pin both an OpenAI organisation and project: a key of another organisation or project would be used rather than refused")
	}
	if m.Guard != attestation.ModelGuardOK {
		r.Warnings = append(r.Warnings, fmt.Sprintf("the spend guard says %s: nothing new is asked of the model, and what needs it is not delivered", m.Guard))
	}
	return r, nil
}

func printModel(w io.Writer, m *modelResult) {
	if m == nil {
		return
	}
	fmt.Fprintf(w, "      model: %s %s at %s, prompt %s, prompt cache %s\n", m.Provider, m.Model, m.Endpoint, m.PromptVersion, m.PromptCache)
	pin := func(v string) string {
		if v == "" {
			return "not pinned"
		}
		return v
	}
	retention := "not approved: OpenAI may keep what it is sent for up to 30 days to monitor abuse, as the model's notice says"
	if m.ZeroDataRetention {
		retention = "approved, as the model's notice says"
	}
	fmt.Fprintf(w, "      OpenAI organisation %s, project %s; zero data retention %s\n", pin(m.Organization), pin(m.Project), retention)
	fmt.Fprintf(w, "      spend: at most %s USD a month for every enclave together, an alarm past %s USD a day, %s USD a day for one account in one enclave\n", usd(m.MonthLimitUSD), usd(m.DayAlarmUSD), usd(m.AccountDayUSD))
	key := "no key set: the model is off"
	if m.KeySet {
		key = "a key is set, since " + m.KeySetAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(w, "      %s; guard %s\n", key, m.Guard)
	if c := m.Canary; c != nil {
		checked := "never"
		if !c.CheckedAt.IsZero() {
			checked = c.CheckedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "      canary %s (baseline %s), last check completed %s", c.State, c.Baseline, checked)
		if !c.ChangeSeenAt.IsZero() {
			fmt.Fprintf(w, ", last change seen %s", c.ChangeSeenAt.UTC().Format(time.DateOnly))
		}
		fmt.Fprintln(w)
	}
	kinds := slices.Clone(attestation.ModelCalls)
	var others []string
	for kind := range m.Calls {
		if !slices.Contains(kinds, kind) {
			others = append(others, kind)
		}
	}
	slices.Sort(others)
	var calls []string
	for _, kind := range append(kinds, others...) {
		calls = append(calls, fmt.Sprintf("%s %d", kind, m.Calls[kind]))
	}
	fmt.Fprintf(w, "      calls since %s: %s\n", m.Since.UTC().Format(time.RFC3339), strings.Join(calls, ", "))
	fmt.Fprintln(w, "      note: the enclave proves which model, address, organisation and project it calls, and that the prompt cache is off; what OpenAI keeps, and whether the key is used elsewhere, is OpenAI's and the operator's to say, not something the enclave can see")
	for _, warning := range m.Warnings {
		fmt.Fprintf(w, "      warning: %s\n", warning)
	}
}

// usd writes an amount of US dollars as briefly as it reads.
func usd(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}
