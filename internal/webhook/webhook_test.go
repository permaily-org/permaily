package webhook

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

const client = "https://trip-app.com/oauth/client.json"

func TestAWebhookIsOnTheVendorsOwnHost(t *testing.T) {
	ok := []string{"https://trip-app.com/hooks/permaily", "https://TRIP-APP.com/h"}
	for _, raw := range ok {
		if host, err := URL(raw, client, "permaily.com"); err != nil || host != "trip-app.com" {
			t.Errorf("%s: %q %v", raw, host, err)
		}
	}
	refused := []string{
		"", "http://trip-app.com/h", "https://trip-app.com", "https://trip-app.com/",
		"https://hooks.trip-app.com/h",                             // another host, even under the vendor's domain
		"https://trip-app.com.evil.com/h",                          // a lookalike
		"https://trip-app.com:443/h",                               // a port written
		"https://trip-app.com:8443/h",                              // another port
		"https://user@trip-app.com/h",                              // a user
		"https://trip-app.com/h?tenant=1",                          // a query
		"https://trip-app.com/h#x",                                 // a fragment
		"https://trip-app.com/a/../h",                              // a dot segment
		"https://127.0.0.1/h",                                      // an address
		"https://api.permaily.com/h",                               // the deployment's own
		"https://trip-app.com/" + strings.Repeat("a", URLBytesMax), // too long
	}
	for _, raw := range refused {
		if _, err := URL(raw, client, "permaily.com"); !errors.Is(err, ErrURL) {
			t.Errorf("%q: %v, want ErrURL", raw, err)
		}
	}
	if _, err := URL("https://permaily.com/h", "https://permaily.com/client.json", "permaily.com"); !errors.Is(err, ErrURL) {
		t.Errorf("the deployment's own client: %v", err)
	}
}

func TestAWebhookRequestIsStrict(t *testing.T) {
	r, err := ParseRequest(`{"url":"https://trip-app.com/h","new_mail":true,"schedule":{"every":"week","on":"friday","at":"17:45"}}`, client, "permaily.com")
	if err != nil || !r.NewMail || r.Schedule == nil || r.Schedule.On != "friday" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, raw := range []string{
		``, `null`, `[]`, `{}`,
		`{"url":"https://trip-app.com/h"}`,                       // neither new mail nor a schedule
		`{"url":"https://trip-app.com/h","new_mail":false}`,      // the same
		`{"url":"https://other.com/h","new_mail":true}`,          // another host
		`{"url":"https://trip-app.com/h","new_mail":true,"x":1}`, // an unknown member
		`{"url":"https://trip-app.com/h","new_mail":true} {}`,    // two values
		`{"url":"https://trip-app.com/h","schedule":{"every":"hour","at":"08:00"}}`,
		`{"url":"https://trip-app.com/h","schedule":{"every":"day","at":"08:00","cron":"* * * * *"}}`,
		`{"url":"https://trip-app.com/h","new_mail":true,"pad":"` + strings.Repeat("a", RequestBytesMax) + `"}`,
	} {
		if _, err := ParseRequest(raw, client, "permaily.com"); err == nil {
			t.Errorf("%.80q accepted", raw)
		}
	}
}

func TestAScheduleIsOneOfTheFixedChoices(t *testing.T) {
	for _, s := range []contracts.WebhookSchedule{
		{Every: contracts.EveryDay, At: "00:00"}, {Every: contracts.EveryDay, At: "23:45"},
		{Every: contracts.EveryWeek, On: "sunday", At: "08:15"}, {Every: contracts.EveryWeek, On: "saturday", At: "12:30"},
	} {
		if err := CheckSchedule(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	for _, s := range []contracts.WebhookSchedule{
		{}, {Every: contracts.EveryDay}, {Every: contracts.EveryDay, At: "8:00"}, {Every: contracts.EveryDay, At: "08:10"},
		{Every: contracts.EveryDay, At: "24:00"}, {Every: contracts.EveryDay, At: "08:60"}, {Every: contracts.EveryDay, At: "08-00"},
		{Every: contracts.EveryDay, At: "08:00", On: "monday"}, {Every: contracts.EveryWeek, At: "08:00"},
		{Every: contracts.EveryWeek, On: "Monday", At: "08:00"}, {Every: contracts.EveryWeek, On: "mon", At: "08:00"},
		{Every: "month", At: "08:00"}, {Every: contracts.EveryDay, At: "٠٨:٠٠"},
	} {
		if err := CheckSchedule(s); !errors.Is(err, ErrSchedule) {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

func TestAZoneIsAnIANANameOfTheImage(t *testing.T) {
	for _, name := range []string{"Europe/Madrid", "America/New_York", "Australia/Lord_Howe", "UTC", "Etc/GMT+5"} {
		if _, err := Zone(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"", "Local", "/etc/localtime", "../../etc/passwd", "Europe/../UTC", "Mars/Olympus", "Europe/Madrid ", strings.Repeat("A", 65)} {
		if _, err := Zone(name); !errors.Is(err, ErrTimeZone) {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := Zone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestTheNextRunIsInThePersonsTimeZone(t *testing.T) {
	ny, madrid, lordHowe, apia := zone(t, "America/New_York"), zone(t, "Europe/Madrid"), zone(t, "Australia/Lord_Howe"), zone(t, "Pacific/Apia")
	daily := func(at string) contracts.WebhookSchedule {
		return contracts.WebhookSchedule{Every: contracts.EveryDay, At: at}
	}
	cases := []struct {
		name  string
		s     contracts.WebhookSchedule
		loc   *time.Location
		after time.Time
		want  time.Time
	}{
		{"later today", daily("08:00"), madrid, time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC), time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)},
		{"at the time itself, the next day", daily("08:00"), madrid, time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC), time.Date(2026, 11, 21, 7, 0, 0, 0, time.UTC)},
		{"the person's day, not UTC's", daily("23:00"), ny, time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC), time.Date(2026, 11, 21, 4, 0, 0, 0, time.UTC)},
		// New York skips 02:00 to 03:00 on 2026-03-08: 02:30 falls when the clocks change, 07:00 UTC.
		{"a time the clocks skip", daily("02:30"), ny, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"the day after a skip, as usual", daily("02:30"), ny, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)},
		// New York goes back from 02:00 to 01:00 on 2026-11-01: 01:30 happens twice, the first at 05:30 UTC.
		{"a time that happens twice, the first", daily("01:30"), ny, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)},
		{"and never the second", daily("01:30"), ny, time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC)},
		// Madrid goes forward at 02:00 on 2026-03-29: 02:15 falls at 01:00 UTC.
		{"Madrid's skip", daily("02:15"), madrid, time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		// Lord Howe moves by half an hour: 02:15 on 2026-10-04 is skipped (02:00 becomes 02:30, +10:30 to +11).
		{"a half-hour change", daily("02:15"), lordHowe, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)},
		{"weekly, on its weekday", contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "monday", At: "08:00"}, madrid, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)},
		// Apia skipped 2011-12-30 whole (-10 to +14): a weekly Friday run falls at the start of Saturday 2011-12-31.
		{"a day a zone skipped", contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "friday", At: "08:00"}, apia, time.Date(2011, 12, 28, 0, 0, 0, 0, time.UTC), time.Date(2011, 12, 30, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := Next(c.s, c.loc, c.after); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got.UTC(), c.want)
		}
	}
}

// Every run of a year falls at the time asked on the person's clock, or at
// a change of the clocks when that time does not exist, once a day, in order.
func TestAYearOfRunsInEveryKindOfZone(t *testing.T) {
	for _, name := range []string{"America/New_York", "Europe/Madrid", "Australia/Lord_Howe", "Asia/Kolkata", "America/Sao_Paulo", "Pacific/Chatham"} {
		loc := zone(t, name)
		for _, at := range []string{"00:00", "01:30", "02:15", "02:45", "08:00", "23:45"} {
			s := contracts.WebhookSchedule{Every: contracts.EveryDay, At: at}
			prev := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for range 366 {
				next := Next(s, loc, prev)
				if !next.After(prev) || next.Sub(prev) > 26*time.Hour {
					t.Fatalf("%s %s: %s after %s", name, at, next, prev)
				}
				if w := next.In(loc).Format("15:04"); w != at {
					// Only at a change of the clocks, and later on the wall than asked.
					start, _ := next.In(loc).ZoneBounds()
					if !start.Equal(next) || w < at {
						t.Fatalf("%s %s: %s reads %s", name, at, next, w)
					}
				}
				prev = next
			}
		}
	}
}

func TestMissedRunsMakeOneLateNotice(t *testing.T) {
	madrid := zone(t, "Europe/Madrid")
	s := contracts.WebhookSchedule{Every: contracts.EveryDay, At: "08:00"}
	last := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	if _, _, ok := Due(s, madrid, last, last.Add(23*time.Hour)); ok {
		t.Error("nothing due before the next run")
	}
	at, late, ok := Due(s, madrid, last, last.Add(24*time.Hour+time.Minute))
	if !ok || late || !at.Equal(last.Add(24*time.Hour)) {
		t.Errorf("on time: %s %v %v", at, late, ok)
	}
	at, late, ok = Due(s, madrid, last, last.Add(24*time.Hour+LateAfter+time.Second))
	if !ok || !late || !at.Equal(last.Add(24*time.Hour)) {
		t.Errorf("late: %s %v %v", at, late, ok)
	}
	at, late, ok = Due(s, madrid, last, last.Add(3*24*time.Hour+time.Minute))
	if !ok || !late || !at.Equal(last.Add(3*24*time.Hour)) {
		t.Errorf("three missed, one notice for the latest: %s %v %v", at, late, ok)
	}
	at, late, ok = Due(s, madrid, last, last.Add(400*24*time.Hour+time.Minute))
	if !ok || !late || !at.Equal(Next(s, madrid, last.Add(399*24*time.Hour))) {
		t.Errorf("a long downtime: %s %v %v", at, late, ok)
	}
}

func notice() contracts.WebhookNotice {
	at := time.Date(2026, 11, 20, 8, 3, 0, 0, time.UTC)
	return contracts.WebhookNotice{V: 1, Type: contracts.NoticeMailMatched, ID: "ntc_0123456789abcdefghijklmnop", Subscription: "sub_0123456789abcdefghijklmnop",
		ClientID: client, Audience: "https://trip-app.com/h", CreatedAt: at.Add(7 * time.Second), Since: at}
}

func TestANoticeIsWhatTheADRSaysAndFitsItsBound(t *testing.T) {
	b, err := Body(notice())
	if err != nil || len(b) > NoticeBytesMax {
		t.Fatalf("%s %v", b, err)
	}
	long := notice()
	long.Audience = "https://trip-app.com/" + strings.Repeat("a", URLBytesMax)
	long.ClientID = long.Audience
	if b, err := Body(long); err != nil || len(b) > NoticeBytesMax {
		t.Errorf("the longest URLs: %d bytes, %v", len(b), err)
	}
	for name, change := range map[string]func(*contracts.WebhookNotice){
		"a version":                     func(n *contracts.WebhookNotice) { n.V = 2 },
		"a type":                        func(n *contracts.WebhookNotice) { n.Type = "mail.arrived" },
		"an identifier":                 func(n *contracts.WebhookNotice) { n.ID = "con_x" },
		"a connection for subscription": func(n *contracts.WebhookNotice) { n.Subscription = "con_x" },
		"no audience":                   func(n *contracts.WebhookNotice) { n.Audience = "" },
		"a second in since":             func(n *contracts.WebhookNotice) { n.Since = n.Since.Add(time.Second) },
		"a zone":                        func(n *contracts.WebhookNotice) { n.CreatedAt = n.CreatedAt.In(time.FixedZone("x", 3600)) },
		"a schedule's time on mail":     func(n *contracts.WebhookNotice) { n.ScheduledAt = n.Since },
		"late mail":                     func(n *contracts.WebhookNotice) { n.Late = true },
		"a schedule without its time":   func(n *contracts.WebhookNotice) { n.Type = contracts.NoticeScheduleDue },
	} {
		n := notice()
		change(&n)
		if _, err := Body(n); !errors.Is(err, ErrNotice) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

const endpoint = "sub_0123456789abcdefghijklmnop"

func TestANoticeIsSignedAsStandardWebhooksSays(t *testing.T) {
	seed := make([]byte, 32)
	k, _ := KeyFromSeed(seed)
	n := notice()
	body, _ := Body(n)
	at := n.CreatedAt
	ts, sig, err := Sign([]ed25519.PrivateKey{k}, endpoint, n.ID, at, body)
	if err != nil || ts != "1795161787" || !strings.HasPrefix(sig, "v2a,") || strings.Contains(sig, " ") {
		t.Fatalf("%s %s %v", ts, sig, err)
	}
	pub := []ed25519.PublicKey{k.Public().(ed25519.PublicKey)}
	if err := Verify(pub, endpoint, n.ID, ts, sig, body, at.Add(TimestampTolerance)); err != nil {
		t.Fatal(err)
	}
	got, err := Open(body, n.ID, n.Audience, at)
	if err != nil || *got != n {
		t.Fatalf("%+v %v", got, err)
	}
	other, _ := KeyFromSeed(append(make([]byte, 31), 1))
	for name, check := range map[string]error{
		"another body":           Verify(pub, endpoint, n.ID, ts, sig, append(body, ' '), at),
		"another identifier":     Verify(pub, endpoint, "ntc_other", ts, sig, body, at),
		"another subscription":   Verify(pub, "sub_other", n.ID, ts, sig, body, at),
		"another timestamp":      Verify(pub, endpoint, n.ID, "1795161788", sig, body, at),
		"a timestamp padded":     Verify(pub, endpoint, n.ID, "01795161787", sig, body, at),
		"too old":                Verify(pub, endpoint, n.ID, ts, sig, body, at.Add(TimestampTolerance+time.Second)),
		"from the future":        Verify(pub, endpoint, n.ID, ts, sig, body, at.Add(-TimestampTolerance-time.Second)),
		"another key":            Verify([]ed25519.PublicKey{other.Public().(ed25519.PublicKey)}, endpoint, n.ID, ts, sig, body, at),
		"v1 (HMAC) only":         Verify(pub, endpoint, n.ID, ts, strings.Replace(sig, "v2a,", "v1,", 1), body, at),
		"v1a only":               Verify(pub, endpoint, n.ID, ts, strings.Replace(sig, "v2a,", "v1a,", 1), body, at),
		"no signature":           Verify(pub, endpoint, n.ID, ts, "", body, at),
		"a timestamp not number": Verify(pub, endpoint, n.ID, "now", sig, body, at),
		"an endpoint with a dot": Verify(pub, "sub_a.b", n.ID, ts, sig, body, at),
	} {
		if !errors.Is(check, ErrVerify) {
			t.Errorf("%s: %v", name, check)
		}
	}
	if _, _, err := Sign([]ed25519.PrivateKey{k}, "sub_a.b", n.ID, at, body); err == nil {
		t.Error("an endpoint with a dot is signed")
	}
	if _, _, err := Sign(nil, endpoint, n.ID, at, body); err == nil {
		t.Error("signed by no key")
	}
	if _, _, err := Sign([]ed25519.PrivateKey{k[:10]}, endpoint, n.ID, at, body); err == nil {
		t.Error("signed by a key that is none")
	}
	// During a rotation: signed by both, verified by either.
	ts2, both, _ := Sign([]ed25519.PrivateKey{other, k}, endpoint, n.ID, at, body)
	if err := Verify(pub, endpoint, n.ID, ts2, both, body, at); err != nil {
		t.Errorf("the older key during a rotation: %v", err)
	}
	if _, err := Open(body, n.ID, "https://trip-app.com/other", at); !errors.Is(err, ErrNotice) {
		t.Errorf("a notice for another URL: %v", err)
	}
	if _, err := Open(body, "ntc_other", n.Audience, at); !errors.Is(err, ErrNotice) {
		t.Errorf("a notice under another identifier: %v", err)
	}
}

// What the specification's own library computes (testdata/v2a.json, made by
// testdata/v2a.gen.ts with its JavaScript library at SpecRevision, not by
// Sign) is what Sign writes and what Verify takes and refuses: positive and
// negative vectors (design review, finding 7).
func TestANoticeVerifiesWithTheStandardsLibraries(t *testing.T) {
	raw, err := os.ReadFile("testdata/v2a.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Source     string            `json:"source"`
		PublicKeys map[string]string `json:"public_keys"`
		Endpoint   string            `json:"endpoint"`
		ID         string            `json:"id"`
		Timestamp  string            `json:"timestamp"`
		Body       string            `json:"body"`
		Cases      []struct {
			Name      string   `json:"name"`
			Signature string   `json:"signature"`
			Accepts   bool     `json:"accepts"`
			Keys      []string `json:"keys"`
		} `json:"cases"`
		Spec struct {
			SecretKey string `json:"secret_key"`
			PublicKey string `json:"public_key"`
			Endpoint  string `json:"endpoint"`
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
			Body      string `json:"body"`
			Signature string `json:"signature"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || !strings.Contains(v.Source, SpecRevision) {
		t.Fatalf("%v %q", err, v.Source)
	}
	ka, _ := KeyFromSeed(make([]byte, 32))
	kb, _ := KeyFromSeed(append(make([]byte, 31), 1))
	keys := map[string]ed25519.PublicKey{"wk1": ka.Public().(ed25519.PublicKey), "wk2": kb.Public().(ed25519.PublicKey)}
	for id, k := range keys {
		if LibraryKey(k) != v.PublicKeys[id] {
			t.Fatalf("%s: %s, the library has %s", id, LibraryKey(k), v.PublicKeys[id])
		}
	}
	body, _ := Body(notice())
	if string(body) != v.Body || v.Endpoint != endpoint || v.ID != notice().ID {
		t.Fatalf("the vectors are of another notice: %s", v.Body)
	}
	at := notice().CreatedAt
	if ts, sig, _ := Sign([]ed25519.PrivateKey{ka}, endpoint, v.ID, at, body); ts != v.Timestamp || sig != v.Cases[0].Signature {
		t.Errorf("Sign: %s, the library: %s", sig, v.Cases[0].Signature)
	}
	if _, sig, _ := Sign([]ed25519.PrivateKey{kb, ka}, endpoint, v.ID, at, body); sig != v.Cases[1].Signature {
		t.Errorf("Sign with both keys: %s, the library: %s", sig, v.Cases[1].Signature)
	}
	for _, c := range v.Cases {
		with := []ed25519.PublicKey{keys["wk1"]}
		if c.Keys != nil {
			with = nil
			for _, id := range c.Keys {
				with = append(with, keys[id])
			}
		}
		if err := Verify(with, v.Endpoint, v.ID, v.Timestamp, c.Signature, body, at); (err == nil) != c.Accepts {
			t.Errorf("%s: Verify %v, the library accepts: %v", c.Name, err, c.Accepts)
		}
	}
	// The specification's example: its key signs its message as it says.
	s := v.Spec
	seed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s.SecretKey, "whsk_"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := KeyFromSeed(seed)
	sec, _ := strconv.ParseInt(s.Timestamp, 10, 64)
	if _, sig, _ := Sign([]ed25519.PrivateKey{k}, s.Endpoint, s.ID, time.Unix(sec, 0), []byte(s.Body)); sig != s.Signature {
		t.Errorf("the specification's example: %s, want %s", sig, s.Signature)
	}
	if LibraryKey(k.Public().(ed25519.PublicKey)) != s.PublicKey {
		t.Error("the specification's public key")
	}
	if err := Verify([]ed25519.PublicKey{k.Public().(ed25519.PublicKey)}, s.Endpoint, s.ID, s.Timestamp, s.Signature, []byte(s.Body), time.Unix(sec, 0)); err != nil {
		t.Errorf("the specification's example does not verify: %v", err)
	}
}

// A vendor keeps a notice's identifier DedupKeepFor after it first took it:
// a retry may come, freshly signed, until GiveUpAfter after the notice was
// made, either end TimestampTolerance off; and Open refuses a notice older
// than any retry, so nothing needs keeping longer (design review, finding 9).
func TestANoticeIsOpenedOnlyWithinItsLife(t *testing.T) {
	n := notice()
	body, _ := Body(n)
	made := n.CreatedAt
	for name, now := range map[string]time.Time{
		"when made":                  made,
		"its clock a little behind":  made.Add(-TimestampTolerance),
		"the last retry, late clock": made.Add(GiveUpAfter + TimestampTolerance),
	} {
		if _, err := Open(body, n.ID, n.Audience, now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, now := range map[string]time.Time{
		"older than any retry": made.Add(NoticeLife + time.Second),
		"from the future":      made.Add(-TimestampTolerance - time.Second),
	} {
		if _, err := Open(body, n.ID, n.Audience, now); !errors.Is(err, ErrNotice) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The first acceptance is at least TimestampTolerance before the
	// notice's making on the vendor's clock; the last is NoticeLife after it.
	if first, last := made.Add(-TimestampTolerance), made.Add(NoticeLife); last.Sub(first) != DedupKeepFor {
		t.Errorf("a vendor keeps %s, a retry may come %s apart", DedupKeepFor, last.Sub(first))
	}
}

// A scheduled notice's first coverage boundary is the connection's making,
// on its minute (design review, finding 6).
func TestAScheduleCoversFromTheConnectionsMinute(t *testing.T) {
	made := time.Date(2026, 11, 20, 8, 3, 59, 999, time.FixedZone("x", 3600))
	if got := Coverage(made); !got.Equal(time.Date(2026, 11, 20, 7, 3, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Fatal(got)
	}
	n := notice()
	n.Type, n.ScheduledAt, n.Since = contracts.NoticeScheduleDue, time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC), Coverage(made)
	if _, err := Body(n); err != nil {
		t.Fatalf("a notice since the coverage boundary: %v", err)
	}
}

func TestTheKeysArePublishedAsAJWKSet(t *testing.T) {
	k, _ := KeyFromSeed(make([]byte, 32))
	pub := k.Public().(ed25519.PublicKey)
	b, err := KeySet(map[string]ed25519.PublicKey{KeyID: pub})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"wk1","use":"sig","alg":"EdDSA","x":"O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik"}]}`; string(b) != want {
		t.Errorf("%s", b)
	}
	keys, err := ParseKeySet(b)
	if err != nil || !keys[KeyID].Equal(pub) {
		t.Fatalf("%v %v", keys, err)
	}
	for _, bad := range []string{`{"keys":[]}`, `{"keys":[{"kty":"RSA","crv":"Ed25519","kid":"a","use":"sig","alg":"EdDSA","x":"O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik"}]}`,
		strings.Replace(string(b), `]}`, `,`+strings.TrimSuffix(strings.TrimPrefix(string(b), `{"keys":[`), `]}`)+`]}`, 1)} {
		if _, err := ParseKeySet([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := KeyFromSeed(make([]byte, 31)); err == nil {
		t.Error("a short seed")
	}
}

func TestRetriesBackOffAndAWebhookIsTheSameOnlyWhenEqual(t *testing.T) {
	var total time.Duration
	calls := 1
	for total < GiveUpAfter {
		total += Wait(calls)
		calls++
	}
	if Wait(0) != time.Minute || Wait(1) != time.Minute || Wait(100) != 12*time.Hour || calls < 10 || calls > 14 {
		t.Errorf("calls in %s: %d", GiveUpAfter, calls)
	}
	a := &contracts.WebhookRequest{URL: "https://trip-app.com/h", NewMail: true, Schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "08:00"}}
	b := *a
	b.Schedule = &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "08:00"}
	if !Same(a, &b) || !Same(nil, nil) || Same(a, nil) {
		t.Error("equal webhooks")
	}
	b.Schedule.At = "08:15"
	if Same(a, &b) {
		t.Error("another time")
	}
}
