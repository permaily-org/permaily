package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestCheckAnchors(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	good := func() *attestation.AnchorStatus {
		return &attestation.AnchorStatus{Bucket: "permaily-anchors-1", RetentionDays: 3650, Records: []attestation.AnchoredRecord{
			{Name: "tls-acme-v02.api.letsencrypt.org", Version: 4, CommittedAt: now.Add(-time.Hour), LockedUntil: now.AddDate(10, 0, 0)},
		}}
	}
	if err := checkAnchors(good(), now); err != nil {
		t.Fatal(err)
	}
	// An enclave that has read nothing yet has nothing to show, and that is not a failure.
	empty := good()
	empty.Records = []attestation.AnchoredRecord{}
	if err := checkAnchors(empty, now); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		change func(a *attestation.AnchorStatus)
		want   string
	}{
		"a commit whose lock ended":     {func(a *attestation.AnchorStatus) { a.Records[0].LockedUntil = now.Add(-time.Minute) }, "is not locked"},
		"a commit that reports no lock": {func(a *attestation.AnchorStatus) { a.Records[0].LockedUntil = time.Time{} }, "is not locked"},
		"a record without a version":    {func(a *attestation.AnchorStatus) { a.Records[0].Version = 0 }, "is not locked"},
		"no bucket":                     {func(a *attestation.AnchorStatus) { a.Bucket = "" }, "names no store"},
		"no retention":                  {func(a *attestation.AnchorStatus) { a.RetentionDays = 0 }, "names no store"},
		"the store of commits went back": {func(a *attestation.AnchorStatus) {
			a.Regressions = []attestation.AnchorRegression{{Name: "tls-acme-v02.api.letsencrypt.org", Had: 4, Found: 3, At: now}}
		}, "had tls-acme-v02.api.letsencrypt.org at version 4, and at 2026-11-11T08:00:00Z the store answered version 3"},
	} {
		a := good()
		c.change(a)
		if err := checkAnchors(a, now); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPrintAnchors(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	printAnchors(&out, nil, now)
	if out.Len() != 0 {
		t.Fatalf("printed %q for nothing", out.String())
	}
	a := &attestation.AnchorStatus{Bucket: "permaily-anchors-1", RetentionDays: 3650, Records: []attestation.AnchoredRecord{}}
	printAnchors(&out, a, now)
	if !strings.Contains(out.String(), "none read yet") || !strings.Contains(out.String(), "permaily-anchors-1, each locked for 3650 days") {
		t.Fatalf("nothing read: %s", out.String())
	}
	out.Reset()
	a.Records = []attestation.AnchoredRecord{
		{Name: "tls-acme-v02.api.letsencrypt.org", Version: 4, CommittedAt: now.Add(-time.Hour), LockedUntil: now.AddDate(10, 0, 0)},
		{Name: "about-to-end", Version: 2, CommittedAt: now.Add(-time.Hour), LockedUntil: now.AddDate(0, 0, 3)},
	}
	printAnchors(&out, a, now)
	for _, want := range []string{
		"2 record(s) cannot be put back to an earlier copy", "S3 bucket permaily-anchors-1",
		"tls-acme-v02.api.letsencrypt.org is at version 4, committed 2026-11-11T07:00:00Z, locked until 2036-11-11",
		"about-to-end is at version 2", "warning: that lock ends within 30 days; after it an earlier copy of about-to-end could be put back",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Count(out.String(), "warning") != 1 {
		t.Errorf("warnings:\n%s", out.String())
	}
}
