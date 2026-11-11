package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

const (
	mailNone       = attestation.MailNoConfigurationSet
	mailSet        = attestation.MailConfigurationSet
	mailUnverified = attestation.MailNotVerified
	mailAbsent     = attestation.MailAbsent
)

// sentMail builds what an enclave reports: the states it saw, oldest first,
// ten minutes each; the last one is the current state, read a minute ago.
func sentMail(now time.Time, states ...attestation.MailState) *attestation.MailStatus {
	since := now.Add(-time.Duration(len(states)) * 10 * time.Minute)
	m := &attestation.MailStatus{Domain: "mail.example.org", Address: "no-reply@mail.example.org", CheckedAt: now.Add(-time.Minute), WatchedSince: since}
	for i, s := range states {
		first := since.Add(time.Duration(i) * 10 * time.Minute)
		m.History = append(m.History, attestation.MailObservation{MailState: s, FirstSeen: first, LastSeen: first.Add(9 * time.Minute)})
	}
	last := states[len(states)-1]
	m.State = &last
	m.History[len(states)-1].LastSeen = m.CheckedAt
	return m
}

func TestCheckSentMail(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	alone := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailAbsent}
	both := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailNone}
	state := func(domain, address string) attestation.MailState {
		return attestation.MailState{DomainIdentity: domain, AddressIdentity: address}
	}

	for name, m := range map[string]*attestation.MailStatus{
		"unchanged since the enclave started":       sentMail(now, alone),
		"an identity for the address, with nothing": sentMail(now, both),
		"an address identity that cannot send":      sentMail(now, state(mailNone, mailUnverified)),
		"changed between states that all pass":      sentMail(now, alone, both, alone),
		"read just now":                             func() *attestation.MailStatus { m := sentMail(now, alone); m.CheckedAt = now; return m }(),
		// It sends nothing meanwhile, and the reading is still a recent one.
		"the latest reading failed": func() *attestation.MailStatus { m := sentMail(now, alone); m.Failing = true; return m }(),
	} {
		if err := checkSentMail(m, now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	for name, c := range map[string]struct {
		m    *attestation.MailStatus
		want []string
	}{
		"a configuration set on the domain":  {sentMail(now, state(mailSet, mailAbsent)), []string{"the SES identity mail.example.org has a default configuration set", "keep a copy"}},
		"a configuration set on the address": {sentMail(now, state(mailNone, mailSet)), []string{"the SES identity no-reply@mail.example.org has a default configuration set"}},
		"a configuration set on both":        {sentMail(now, state(mailSet, mailSet)), []string{"the SES identity mail.example.org has"}},
		"no identity for the domain":         {sentMail(now, state(mailAbsent, mailAbsent)), []string{"no identity that may send for mail.example.org", "under another identity"}},
		"a domain that cannot send":          {sentMail(now, state(mailUnverified, mailNone)), []string{"no identity that may send for mail.example.org"}},
		"a state this verifier does not know": {sentMail(now, state(mailNone, "archived")),
			[]string{`does not know (domain identity "no_configuration_set", address identity "archived")`}},
		"a state without names": {sentMail(now, state("", "")), []string{"does not know"}},
		// The current state is clean in these; only the history gives it away.
		"a configuration set on the address for a while": {sentMail(now, alone, state(mailNone, mailSet), alone),
			[]string{"from 2026-11-11T07:40:00Z to 2026-11-11T07:49:00Z", "the SES identity no-reply@mail.example.org has a default configuration set"}},
		"a configuration set on the domain at the start": {sentMail(now, state(mailSet, mailAbsent), alone),
			[]string{"from 2026-11-11T07:40:00Z to 2026-11-11T07:49:00Z", "the SES identity mail.example.org has"}},
		"the domain identity gone for a while": {sentMail(now, alone, state(mailAbsent, mailAbsent), alone), []string{"from 2026-11-11T07:40:00Z", "under another identity"}},
		"more changes than the enclave remembers": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.HistoryTruncated = true
			return m
		}(), []string{"history is incomplete"}},
		"no history": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.History = nil
			return m
		}(), []string{"history is incomplete"}},
		"a history that does not end in the current state": {func() *attestation.MailStatus {
			m := sentMail(now, alone, state(mailNone, mailSet))
			m.State = &alone
			return m
		}(), []string{"does not end in the current state"}},
		"an old reading": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.CheckedAt = now.Add(-time.Hour)
			return m
		}(), []string{"read by the enclave at 2026-11-11T07:00:00Z, not within 5m0s of now"}},
		"a reading from the future": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.CheckedAt = now.Add(time.Hour)
			return m
		}(), []string{"not within"}},
		"no reading yet": {&attestation.MailStatus{Domain: "mail.example.org", Address: "no-reply@mail.example.org", Failing: true}, []string{"has not read from SES"}},
		"a state without a time": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.CheckedAt = time.Time{}
			return m
		}(), []string{"has not read from SES"}},
		"a time without a state": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.State = nil
			return m
		}(), []string{"has not read from SES"}},
		"nobody the mail is from": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.Address = ""
			return m
		}(), []string{"does not say who its mail is from"}},
		"no domain": {func() *attestation.MailStatus {
			m := sentMail(now, alone)
			m.Domain = ""
			return m
		}(), []string{"does not say who its mail is from"}},
	} {
		err := checkSentMail(c.m, now)
		if err == nil {
			t.Errorf("%s: passed", name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", name, err, w)
			}
		}
	}
}

func TestPrintSentMail(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	alone := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailAbsent}
	both := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailNone}
	var out bytes.Buffer
	printSentMail(&out, nil)
	if out.Len() != 0 {
		t.Fatalf("printed %q for nothing", out.String())
	}

	printSentMail(&out, sentMail(now, alone))
	for _, want := range []string{
		"mail from no-reply@mail.example.org goes out through Amazon SES with no configuration set",
		"none on the identity mail.example.org, and there is no identity for the address alone",
		"read by the enclave every minute and around each message since 2026-11-11T07:50:00Z, last at 2026-11-11T07:59:00Z: unchanged",
		"note: whoever administers the account can set one",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "warning") || strings.Contains(out.String(), "other state") {
		t.Errorf("output:\n%s", out.String())
	}

	out.Reset()
	m := sentMail(now, alone, both, alone, both)
	m.Failing = true
	printSentMail(&out, m)
	for _, want := range []string{
		"none on the identity mail.example.org, and none on the identity of the address",
		"since 2026-11-11T07:20:00Z, last at 2026-11-11T07:59:00Z: 3 other state(s), none with a configuration set",
		"warning: the enclave's latest reading of SES failed; it sends no mail until one succeeds",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// What /status says about the mail decides whether a host passes, and a
// status without it passes as before.
func TestCheckStatusSentMail(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	alone := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailAbsent}
	copied := attestation.MailState{DomainIdentity: mailNone, AddressIdentity: mailSet}
	host := hostResult{URL: "https://app.example.org", SPKIHash: "ab"}
	check := func(m *attestation.MailStatus, opts kmsOptions) hostResult {
		opts.now = now
		return checkStatus(&attestation.Status{Commit: "abc", Mail: m}, host, imageA, opts)
	}

	if r := check(sentMail(now, alone), kmsOptions{}); !r.OK || r.Error != "" || r.SentMail == nil || r.SentMail.Address != "no-reply@mail.example.org" {
		t.Fatalf("clean: %+v", r)
	}
	if r := check(nil, kmsOptions{}); !r.OK || r.Error != "" || r.SentMail != nil {
		t.Fatalf("an image that sends no mail: %+v", r)
	}
	for name, m := range map[string]*attestation.MailStatus{
		"a configuration set now":         sentMail(now, alone, copied),
		"a configuration set for a while": sentMail(now, alone, copied, alone),
		"no reading":                      {Domain: "mail.example.org", Address: "no-reply@mail.example.org"},
	} {
		if r := check(m, kmsOptions{}); r.OK || !strings.HasPrefix(r.Error, "mail the enclave sends: ") || r.SentMail != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// The result is printed with the rest of what was checked on the host.
	var out bytes.Buffer
	printHuman(&out, "test", map[uint][]byte{0: {1}, 1: {2}, 2: {3}}, []hostResult{check(sentMail(now, alone), kmsOptions{})})
	if !strings.Contains(out.String(), "PASS  https://app.example.org") || !strings.Contains(out.String(), "mail from no-reply@mail.example.org goes out through Amazon SES") {
		t.Fatalf("output:\n%s", out.String())
	}
}
