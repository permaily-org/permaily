package certwatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/certwatch"
)

const account = "https://acme-v02.api.letsencrypt.org/acme/acct/123"

func caa(t *testing.T, records ...string) []certwatch.CAA {
	t.Helper()
	var out []certwatch.CAA
	for _, r := range records {
		rec, err := certwatch.ParseCAA(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

func pinned(uri string) string {
	return `0 issue "letsencrypt.org; accounturi=` + uri + `; validationmethods=tls-alpn-01"`
}

func TestParseCAA(t *testing.T) {
	r, err := certwatch.ParseCAA(`128 Issue "LetsEncrypt.org ; AccountURI = ` + account + `;validationmethods=tls-alpn-01; flag"`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "issue" || r.Issuer != "letsencrypt.org" || r.Params["accounturi"] != account || r.Params["validationmethods"] != "tls-alpn-01" || len(r.Params) != 2 {
		t.Fatalf("record %+v", r)
	}
	if r, err := certwatch.ParseCAA(`0 issuewild ";"`); err != nil || r.Issuer != "" || r.Tag != "issuewild" {
		t.Fatalf("no-issuer record %+v %v", r, err)
	}
	for _, bad := range []string{"", `0 issue`, `x issue "a"`, `0 issue letsencrypt.org`, `0 issue "unterminated`} {
		if _, err := certwatch.ParseCAA(bad); !errors.Is(err, certwatch.ErrMalformed) {
			t.Fatalf("%q: got %v, want ErrMalformed", bad, err)
		}
	}
}

func TestCheckPinned(t *testing.T) {
	if err := certwatch.CheckPinned(caa(t, pinned(account), `0 iodef "mailto:security@example.org"`), "letsencrypt.org", account, "tls-alpn-01"); err != nil {
		t.Fatal(err)
	}
	// Each of these lets someone other than the enclave get a certificate for the name.
	for name, c := range map[string]struct {
		records []string
		want    string
	}{
		"any Let's Encrypt account": {[]string{`0 issue "letsencrypt.org"`}, "ACME account"},
		"another account":           {[]string{pinned("https://acme-v02.api.letsencrypt.org/acme/acct/999")}, "ACME account"},
		"another CA too":            {[]string{pinned(account), `0 issue "amazon.com"`}, `"amazon.com"`},
		"any validation method":     {[]string{`0 issue "letsencrypt.org; accounturi=` + account + `"`}, "validation"},
		"dns validation":            {[]string{`0 issue "letsencrypt.org; accounturi=` + account + `; validationmethods=dns-01"`}, "validation"},
		"no issue record":           {[]string{`0 issuewild ";"`}, "no issue record"},
		"no records":                {nil, "no issue record"},
	} {
		t.Run(name, func(t *testing.T) {
			err := certwatch.CheckPinned(caa(t, c.records...), "letsencrypt.org", account, "tls-alpn-01")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestCheckNoWildcard(t *testing.T) {
	if err := certwatch.CheckNoWildcard(caa(t, `0 issue "amazon.com"`, `0 issuewild ";"`)); err != nil {
		t.Fatal(err)
	}
	for name, records := range map[string][]string{
		"a CA may issue wildcards": {`0 issuewild ";"`, `0 issuewild "amazon.com"`},
		"no issuewild record":      {`0 issue "amazon.com"`},
		"no records":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			if err := certwatch.CheckNoWildcard(caa(t, records...)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLookupCAA(t *testing.T) {
	answers := map[string]string{
		"app.test":     `{"Status":0,"Answer":[{"type":5,"data":"alias.test."},{"type":257,"data":"0 issue \"letsencrypt.org\""}]}`,
		"missing.test": `{"Status":3}`,
		"broken.test":  `{"Status":2}`,
		"garbage.test": `{"Status":0,"Answer":[{"type":257,"data":"nope"}]}`,
		"html.test":    `<html>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/dns-json" || r.URL.Query().Get("type") != "CAA" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, ok := answers[r.URL.Query().Get("name")]
		if !ok {
			http.Error(w, "resolver down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ctx := context.Background()

	got, err := certwatch.LookupCAA(ctx, srv.Client(), srv.URL, "app.test")
	if err != nil || len(got) != 1 || got[0].Issuer != "letsencrypt.org" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := certwatch.LookupCAA(ctx, srv.Client(), srv.URL, "missing.test"); err != nil || len(got) != 0 {
		t.Fatalf("missing name: %+v, %v", got, err)
	}
	// A failed lookup must never look like "no records".
	for _, name := range []string{"broken.test", "garbage.test", "html.test", "down.test"} {
		if _, err := certwatch.LookupCAA(ctx, srv.Client(), srv.URL, name); err == nil {
			t.Fatalf("%s: no error", name)
		}
	}
	if _, err := certwatch.LookupCAA(ctx, srv.Client(), "http://127.0.0.1:1", "app.test"); err == nil {
		t.Fatal("unreachable resolver: no error")
	}
	if _, err := certwatch.LookupCAA(ctx, srv.Client(), "://bad", "app.test"); err == nil {
		t.Fatal("bad resolver URL: no error")
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		name, host string
		want       bool
	}{
		{"app.permaily.com", "app.permaily.com", true},
		{"APP.permaily.com", "app.Permaily.com", true},
		{"*.permaily.com", "app.permaily.com", true},
		{"*.permaily.com", "mta-sts.inbox.permaily.com", false}, // a wildcard covers one label
		{"*.inbox.permaily.com", "mta-sts.inbox.permaily.com", true},
		{"*.permaily.com", "permaily.com", false},
		{"permaily.com", "app.permaily.com", false},
		{"*.example.com", "app.permaily.com", false},
		{"*.com", "com", false},
	} {
		if got := certwatch.Covers(c.name, c.host); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.name, c.host, got, c.want)
		}
	}
}

func issuance(id, key string, notBefore, notAfter time.Time, names ...string) certwatch.Issuance {
	return certwatch.Issuance{ID: id, DNSNames: names, PubkeySHA256: key, NotBefore: notBefore, NotAfter: notAfter}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	since := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	later := now.Add(60 * 24 * time.Hour)
	hosts := []string{"app.permaily.com", "mta-sts.inbox.permaily.com"}
	revoked := issuance("revoked", "ffff", since.Add(time.Hour), later, "app.permaily.com")
	revoked.Revoked = true

	r := certwatch.Classify([]certwatch.Issuance{
		issuance("own", "ABCD", since.Add(time.Hour), later, "app.permaily.com", "mta-sts.inbox.permaily.com"),
		// The same key renewed by another enclave while the first is valid.
		issuance("own-renewed", "abcd", since.Add(50*24*time.Hour), later.Add(50*24*time.Hour), "app.permaily.com", "mta-sts.inbox.permaily.com"),
		issuance("old-wildcard", "1111", since.Add(-24*time.Hour), later, "*.permaily.com", "permaily.com"),
		issuance("new-wildcard", "2222", since.Add(time.Hour), later, "*.permaily.com"),
		issuance("new-exact", "3333", since, later, "app.permaily.com"),
		issuance("expired", "4444", since.Add(time.Hour), now, "app.permaily.com"),
		issuance("site", "5555", since.Add(time.Hour), later, "permaily.com", "www.permaily.com"),
		issuance("deep-wildcard-miss", "6666", since.Add(time.Hour), later, "*.inbox.example.com"),
		revoked,
	}, hosts, "abcd", since, now)

	ids := func(list []certwatch.Issuance) string {
		var out []string
		for _, i := range list {
			out = append(out, i.ID)
		}
		return strings.Join(out, ",")
	}
	if ids(r.Own) != "own,own-renewed" || ids(r.Older) != "old-wildcard" || ids(r.Foreign) != "new-wildcard,new-exact" {
		t.Fatalf("own %q, older %q, foreign %q", ids(r.Own), ids(r.Older), ids(r.Foreign))
	}
}

func TestIssuances(t *testing.T) {
	pages := map[string][]certwatch.Issuance{
		"":  {issuance("1", "aa", time.Time{}, time.Time{}, "a.test"), issuance("2", "bb", time.Time{}, time.Time{}, "b.test")},
		"2": {issuance("3", "cc", time.Time{}, time.Time{}, "c.test")},
		"3": {},
	}
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("domain") != "permaily.test" || q.Get("include_subdomains") != "true" || len(q["expand"]) != 2 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		switch mode {
		case "limited":
			http.Error(w, `{"code":"rate_limited"}`, http.StatusTooManyRequests)
		case "garbage":
			_, _ = w.Write([]byte("<html>"))
		case "no-id":
			_, _ = w.Write([]byte(`[{"dns_names":["a.test"]}]`))
		case "endless":
			_, _ = fmt.Fprintf(w, `[{"id":"%s0"}]`, q.Get("after"))
		default:
			_ = json.NewEncoder(w).Encode(pages[q.Get("after")])
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	got, err := certwatch.Issuances(ctx, srv.Client(), srv.URL, "permaily.test")
	if err != nil || len(got) != 3 || got[2].ID != "3" || got[0].PubkeySHA256 != "aa" {
		t.Fatalf("got %+v, %v", got, err)
	}
	// An incomplete answer must be an error, never a shorter list.
	for _, mode = range []string{"limited", "garbage", "no-id", "endless"} {
		if _, err := certwatch.Issuances(ctx, srv.Client(), srv.URL, "permaily.test"); err == nil {
			t.Fatalf("%s: no error", mode)
		}
	}
}
