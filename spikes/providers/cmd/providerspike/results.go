package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// results writes each check's findings as JSON lines under results/ (git
// ignored, mode 0600), to be summarised by hand into notes.md. A record that
// looks like it holds an address or a token is refused, not written.
type results struct {
	dir   string
	now   func() time.Time
	units map[string]int // Gmail units the run spent, by method, as documented
}

func newResults(dir string) *results {
	return &results{dir: dir, now: time.Now, units: map[string]int{}}
}

// Documented Gmail units (developers.google.com/workspace/gmail/api/reference/quota,
// read 2026-11-27); Gmail does not report units per response.
var gmailUnits = map[string]int{
	"getProfile":       1,
	"history.list":     2,
	"messages.list":    5,
	"messages.get":     20,
	"messages.get raw": 60,
}

func (r *results) spend(method string) { r.units[method] += gmailUnits[method] }

// forbidden are substrings that never belong in a result: an address, a
// Google access or refresh token, a JWT, a bearer header.
var forbidden = []string{"@", "ya29.", "1//", "eyJ", "Bearer "}

func checkSafe(path string, v any) error {
	switch x := v.(type) {
	case string:
		for _, f := range forbidden {
			if strings.Contains(x, f) {
				return fmt.Errorf("result field %s looks like it holds an address or a token (%q); not written", path, f)
			}
		}
	case map[string]any:
		for k, e := range x {
			if err := checkSafe(path+"."+k, e); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range x {
			if err := checkSafe(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	case []string:
		for i, e := range x {
			if err := checkSafe(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// record appends one finding of a check.
func (r *results) record(check string, fields map[string]any) error {
	fields["check"] = check
	fields["at"] = r.now().UTC().Format(time.RFC3339)
	// Round-trip through JSON so typed values are checked as written.
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		return err
	}
	if err := checkSafe(check, generic); err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(r.dir, r.now().UTC().Format("2006-01-02")+"-"+check+".jsonl")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("%s: recorded %s (%s)\n", check, name, strings.Join(keys, ", "))
	return nil
}

// state keeps non-secret readings between runs (baselines, UIDVALIDITY) in
// results/state.json.
type state struct {
	GmailBaseline   string            `json:"gmail_baseline,omitempty"`
	GmailBaselineAt int64             `json:"gmail_baseline_at,omitempty"`
	UIDValidity     map[string]uint32 `json:"uidvalidity,omitempty"`
}

func (r *results) loadState() (state, error) {
	var s state
	b, err := os.ReadFile(filepath.Join(r.dir, "state.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func (r *results) saveState(s state) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.dir, "state.json"), b, 0o600)
}
