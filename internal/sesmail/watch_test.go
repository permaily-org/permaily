package sesmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

const (
	fromDomain  = "mail.example.org"
	fromAddress = "no-reply@mail.example.org"
	// setName is a configuration set someone put there. Whoever does chooses
	// the name, so it must not get out.
	setName = "copy-everything"

	askDomain  = "GET /v2/email/identities/mail.example.org"
	askAddress = "GET /v2/email/identities/no-reply%40mail.example.org"
	askSend    = "POST /v2/email/outbound-emails"
)

// said is one answer of SES.
type said struct {
	status int
	kind   string
	body   string
}

// identityOf is what SES answers about an identity that exists.
func identityOf(kindOf, set string, verified bool) said {
	body, _ := json.Marshal(map[string]any{
		"IdentityType": kindOf, "ConfigurationSetName": set, "VerifiedForSendingStatus": verified,
		"VerificationStatus": "SUCCESS", "FeedbackForwardingStatus": true,
		"DkimAttributes": map[string]any{"SigningEnabled": true, "Status": "SUCCESS", "Tokens": []string{"a", "b", "c"}},
		"Policies":       map[string]string{}, "Tags": []any{},
	})
	return said{status: http.StatusOK, body: string(body)}
}

var (
	domainAlone   = identityOf("DOMAIN", "", true)
	addressAlone  = identityOf("EMAIL_ADDRESS", "", true)
	domainCopied  = identityOf("DOMAIN", setName, true)
	addressCopied = identityOf("EMAIL_ADDRESS", setName, true)
	// What SES says in words names the identity.
	noSuch = said{http.StatusNotFound, "NotFoundException", `{"message":"Email identity ` + fromAddress + ` does not exist."}`}
	broken = said{http.StatusInternalServerError, "InternalFailure", `{"message":"something about ` + fromAddress + `"}`}
)

// account is a stand-in for SES as the watch and the sender see it: two
// identities and a place to send to.
type account struct {
	mu      sync.Mutex
	domain  said
	address said
	send    int
	asked   []string // every request, in order
	auth    []http.Header
	// hold, when set, keeps an answer about an identity until it is closed;
	// held counts the answers waiting.
	hold chan struct{}
	held int
}

// clean is an account as it should be: the domain may send, nothing is set
// on it, and the address has no identity of its own.
func clean() *account {
	return &account{domain: domainAlone, address: noSuch, send: http.StatusOK}
}

func (a *account) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.asked = append(a.asked, r.Method+" "+r.RequestURI)
	a.auth = append(a.auth, r.Header.Clone())
	var answer said
	hold := a.hold
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/email/outbound-emails":
		answer, hold = said{status: a.send, kind: "MessageRejected", body: `{}`}, nil
	case r.Method == http.MethodGet && r.URL.Path == "/v2/email/identities/"+fromDomain:
		answer = a.domain
	case r.Method == http.MethodGet && r.URL.Path == "/v2/email/identities/"+fromAddress:
		answer = a.address
	default:
		answer, hold = said{status: http.StatusBadRequest, kind: "BadRequestException"}, nil
	}
	if hold != nil {
		a.held++
	}
	a.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if answer.kind != "" && answer.status != http.StatusOK {
		w.Header().Set("X-Amzn-Errortype", answer.kind)
	}
	w.WriteHeader(answer.status)
	_, _ = w.Write([]byte(answer.body))
}

func (a *account) set(domain, address said) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.domain, a.address = domain, address
}

func (a *account) headers() []http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.auth)
}

func (a *account) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

// count is how many requests were of one kind.
func (a *account) count(what string) int {
	n := 0
	for _, r := range a.requests() {
		if r == what {
			n++
		}
	}
	return n
}

func (a *account) waiting() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held
}

// clock is a clock that moves only when told.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// lines is where a test keeps the logs: a reading writes them from another
// goroutine.
type lines struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var start = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func watch(t *testing.T, a *account) (*Watch, *clock, *lines) {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	c, logs := &clock{t: start}, &lines{}
	w := &Watch{
		HTTP: srv.Client(), Credentials: staticCredentials{}, Region: "us-east-2",
		Domain: fromDomain, Address: fromAddress, Endpoint: srv.URL,
		Log: slog.New(slog.NewJSONHandler(logs, nil)), Now: c.now,
	}
	// A wait is over at once, and the clock says it happened.
	w.pause = func(ctx context.Context, d time.Duration) error {
		c.add(d)
		return ctx.Err()
	}
	return w, c, logs
}

// soon waits for something another goroutine does.
func soon(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting: %s", what)
		}
	}
}

// idle waits until no reading is being made.
func idle(t *testing.T, w *Watch) {
	t.Helper()
	soon(t, "the reading to end", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.flight == nil
	})
}

// run starts Run and returns what stops it and waits for it to return.
func run(t *testing.T, w *Watch) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// tellsNothing fails when what is logged, returned or published names who
// the mail is from, the configuration set, or repeats SES or the network.
func tellsNothing(t *testing.T, what string) {
	t.Helper()
	for _, secret := range []string{fromAddress, fromDomain, setName, "does not exist", "something about", "127.0.0.1"} {
		if strings.Contains(what, secret) {
			t.Errorf("%q is in %q", secret, what)
		}
	}
}

func TestWatchReads(t *testing.T) {
	a := clean()
	w, _, _ := watch(t, a)
	if st := w.Status(); st == nil || st.Domain != fromDomain || st.Address != fromAddress || st.State != nil || !st.CheckedAt.IsZero() || !st.WatchedSince.IsZero() || st.Failing || len(st.History) != 0 {
		t.Fatalf("before any reading: %+v", st)
	}
	if err := w.Clean(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Both identities, and the address written the way AWS signs it.
	if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress}) {
		t.Fatalf("asked %q", got)
	}
	for _, h := range a.headers() {
		if auth := h.Get("Authorization"); !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261111/us-east-2/ses/aws4_request") || !strings.Contains(auth, "Signature=") {
			t.Errorf("authorization %q", auth)
		}
		if h.Get("X-Amz-Security-Token") != "session" || h.Get("X-Amz-Date") != "20261111T120000Z" || h.Get("Content-Type") != "" {
			t.Errorf("headers %v", h)
		}
	}
	want := attestation.MailState{DomainIdentity: attestation.MailNoConfigurationSet, AddressIdentity: attestation.MailAbsent}
	st := w.Status()
	if st.State == nil || *st.State != want || !st.CheckedAt.Equal(start) || !st.WatchedSince.Equal(start) || st.Failing || st.HistoryTruncated {
		t.Fatalf("status %+v", st)
	}
	if !reflect.DeepEqual(st.History, []attestation.MailObservation{{MailState: want, FirstSeen: start, LastSeen: start}}) {
		t.Fatalf("history %+v", st.History)
	}
	// What a caller does to the answer does not reach what the watch remembers.
	st.History[0].DomainIdentity, st.State.AddressIdentity = attestation.MailConfigurationSet, attestation.MailConfigurationSet
	if again := w.Status(); *again.State != want || again.History[0].MailState != want {
		t.Fatalf("status changed from outside: %+v", again)
	}
}

func TestWatchStates(t *testing.T) {
	const none, set, unverified, absent = attestation.MailNoConfigurationSet, attestation.MailConfigurationSet, attestation.MailNotVerified, attestation.MailAbsent
	for name, tc := range map[string]struct {
		domain, address said
		want            attestation.MailState
		err             error
	}{
		"nothing on the domain, no identity for the address": {domainAlone, noSuch, attestation.MailState{DomainIdentity: none, AddressIdentity: absent}, nil},
		"nothing on either":                    {domainAlone, addressAlone, attestation.MailState{DomainIdentity: none, AddressIdentity: none}, nil},
		"an address identity that cannot send": {domainAlone, identityOf("EMAIL_ADDRESS", "", false), attestation.MailState{DomainIdentity: none, AddressIdentity: unverified}, nil},
		// The ways to get a copy.
		"a configuration set on the domain":                    {domainCopied, noSuch, attestation.MailState{DomainIdentity: set, AddressIdentity: absent}, errNotClean},
		"a configuration set on the address":                   {domainAlone, addressCopied, attestation.MailState{DomainIdentity: none, AddressIdentity: set}, errNotClean},
		"a configuration set on an address that cannot send":   {domainAlone, identityOf("EMAIL_ADDRESS", setName, false), attestation.MailState{DomainIdentity: none, AddressIdentity: set}, errNotClean},
		"a configuration set on both":                          {domainCopied, addressCopied, attestation.MailState{DomainIdentity: set, AddressIdentity: set}, errNotClean},
		"no identity for the domain: a parent's would be used": {noSuch, noSuch, attestation.MailState{DomainIdentity: absent, AddressIdentity: absent}, errNotClean},
		"a domain that cannot send: a parent's would be used":  {identityOf("DOMAIN", "", false), noSuch, attestation.MailState{DomainIdentity: unverified, AddressIdentity: absent}, errNotClean},
		"no identity for the domain, one for the address":      {noSuch, addressAlone, attestation.MailState{DomainIdentity: absent, AddressIdentity: none}, errNotClean},
	} {
		t.Run(name, func(t *testing.T) {
			a := clean()
			a.set(tc.domain, tc.address)
			w, _, logs := watch(t, a)
			err := w.Clean(context.Background())
			if !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			st := w.Status()
			if st.State == nil || *st.State != tc.want || st.Failing {
				t.Fatalf("status %+v", st)
			}
			if tc.want.Clean() != (tc.err == nil) {
				t.Errorf("the enclave and a verifier would not agree on %+v", tc.want)
			}
			published, _ := json.Marshal(st)
			if strings.Contains(string(published), setName) {
				t.Errorf("the name of the configuration set is published: %s", published)
			}
			if err != nil {
				tellsNothing(t, err.Error())
			}
			tellsNothing(t, logs.String())
		})
	}
}

func TestWatchFailsClosed(t *testing.T) {
	// More than is read of an answer, with what matters at the end.
	long := `{"IdentityType":"EMAIL_ADDRESS","Tags":["` + strings.Repeat("x", maxAnswer) + `"],"ConfigurationSetName":""}`
	for name, change := range map[string]func(a *account, w *Watch){
		"SES fails on the domain":  func(a *account, _ *Watch) { a.set(broken, noSuch) },
		"SES fails on the address": func(a *account, _ *Watch) { a.set(domainAlone, broken) },
		"not allowed to ask": func(a *account, _ *Watch) {
			a.set(domainAlone, said{http.StatusForbidden, "AccessDeniedException", `{}`})
		},
		"asked too often": func(a *account, _ *Watch) {
			a.set(domainAlone, said{http.StatusTooManyRequests, "TooManyRequestsException", `{}`})
		},
		"not found, said by who knows": func(a *account, _ *Watch) { a.set(domainAlone, said{http.StatusNotFound, "", `{}`}) },
		"not found, of something else": func(a *account, _ *Watch) {
			a.set(domainAlone, said{http.StatusNotFound, "UnknownOperationException", `{}`})
		},
		"an answer that is not JSON":   func(a *account, _ *Watch) { a.set(domainAlone, said{http.StatusOK, "", `<html>`}) },
		"an answer cut short":          func(a *account, _ *Watch) { a.set(domainAlone, said{http.StatusOK, "", long}) },
		"an answer about nothing":      func(a *account, _ *Watch) { a.set(domainAlone, said{http.StatusOK, "", `{}`}) },
		"an answer about another kind": func(a *account, _ *Watch) { a.set(domainAlone, domainAlone) },
		"a configuration set that is not a name": func(a *account, _ *Watch) {
			a.set(domainAlone, said{http.StatusOK, "", `{"IdentityType":"EMAIL_ADDRESS","ConfigurationSetName":{"Name":"` + setName + `"}}`})
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := clean()
			w, _, logs := watch(t, a)
			change(a, w)
			unread(t, a, w, logs)
			if len(a.requests()) == 0 {
				t.Fatal("SES was not asked")
			}
		})
	}

	// A watch that is not set up to read the identities the mail goes out
	// under reads nothing, and so nothing is sent.
	for name, change := range map[string]func(w *Watch){
		"no client":                    func(w *Watch) { w.HTTP = nil },
		"no credentials":               func(w *Watch) { w.Credentials = nil },
		"no region":                    func(w *Watch) { w.Region = "" },
		"no domain":                    func(w *Watch) { w.Domain = "" },
		"no address":                   func(w *Watch) { w.Address = "" },
		"an address of another domain": func(w *Watch) { w.Address = "no-reply@example.org" },
		"an address of a subdomain":    func(w *Watch) { w.Address = "no-reply@eu." + fromDomain },
		"an address without a name":    func(w *Watch) { w.Address = "@" + fromDomain },
		"credentials off":              func(w *Watch) { w.Credentials = staticCredentials{err: errors.New("the host is down")} },
		"a bad endpoint":               func(w *Watch) { w.Endpoint = "http://[::1" },
		"SES out of reach": func(w *Watch) {
			gone := httptest.NewServer(http.NotFoundHandler())
			gone.Close()
			w.Endpoint = gone.URL
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := clean()
			w, _, logs := watch(t, a)
			change(w)
			unread(t, a, w, logs)
			if got := a.requests(); len(got) != 0 {
				t.Fatalf("asked %q", got)
			}
		})
	}
}

// unread fails unless the watch says it could not read, keeps and publishes
// nothing half read, and logs it without repeating anyone.
func unread(t *testing.T, a *account, w *Watch, logs *lines) {
	t.Helper()
	err := w.Clean(context.Background())
	if !errors.Is(err, errUnread) {
		t.Fatalf("got %v after %q, want %v", err, a.requests(), errUnread)
	}
	st := w.Status()
	if st.State != nil || len(st.History) != 0 || !st.CheckedAt.IsZero() || !st.Failing || !st.WatchedSince.Equal(start) {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(logs.String(), "could not be read") {
		t.Errorf("not logged: %s", logs)
	}
	tellsNothing(t, err.Error())
	tellsNothing(t, logs.String())
}

// A domain is the same in capital letters: the address is of it, and SES is
// asked. (The stand-in knows it in small letters only, so it has no answer.)
func TestWatchAddressOfTheDomain(t *testing.T) {
	a := clean()
	w, _, _ := watch(t, a)
	w.Domain = strings.ToUpper(fromDomain)
	if err := w.Clean(context.Background()); !errors.Is(err, errUnread) || a.count("GET /v2/email/identities/MAIL.EXAMPLE.ORG") != 1 {
		t.Fatalf("got %v after %q", err, a.requests())
	}
}

func TestWatchUsesAReadingForFiveSeconds(t *testing.T) {
	a := clean()
	w, c, _ := watch(t, a)
	ctx := context.Background()
	for range 3 {
		if err := w.Clean(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c.add(maxAge)
	if err := w.Clean(ctx); err != nil || len(a.requests()) != 2 {
		t.Fatalf("%v after %d requests, want one reading", err, len(a.requests()))
	}
	// What it says is not taken for longer: a change is seen within maxAge.
	a.set(domainAlone, addressCopied)
	c.add(time.Nanosecond)
	if err := w.Clean(ctx); !errors.Is(err, errNotClean) || len(a.requests()) != 4 {
		t.Fatalf("%v after %d requests, want a second reading that is not clean", err, len(a.requests()))
	}
	// And one that is not clean holds for as long, not for ever: mail is sent
	// again once it is undone.
	a.set(domainAlone, noSuch)
	c.add(maxAge)
	if err := w.Clean(ctx); !errors.Is(err, errNotClean) || len(a.requests()) != 4 {
		t.Fatalf("%v after %d requests", err, len(a.requests()))
	}
	c.add(time.Nanosecond)
	if err := w.Clean(ctx); err != nil || len(a.requests()) != 6 {
		t.Fatalf("%v after %d requests, want a third reading that is clean", err, len(a.requests()))
	}
}

func TestWatchDoesNotAskAgainAtOnce(t *testing.T) {
	a := clean()
	a.set(broken, noSuch)
	w, c, _ := watch(t, a)
	ctx := context.Background()
	for range 5 {
		if err := w.Clean(ctx); !errors.Is(err, errUnread) {
			t.Fatalf("got %v", err)
		}
	}
	c.add(minGap - time.Nanosecond)
	if err := w.Clean(ctx); !errors.Is(err, errUnread) || len(a.requests()) != 1 {
		t.Fatalf("%v after %d requests, want one", err, len(a.requests()))
	}
	// A reading that failed is no reading: an older one that was clean does
	// not count, and the next try comes after minGap.
	a.set(domainAlone, noSuch)
	c.add(time.Nanosecond)
	if err := w.Clean(ctx); err != nil || len(a.requests()) != 3 {
		t.Fatalf("%v after %d requests, want a second reading", err, len(a.requests()))
	}
	a.set(broken, noSuch)
	c.add(maxAge + time.Nanosecond)
	if err := w.Clean(ctx); !errors.Is(err, errUnread) {
		t.Fatalf("sent on a reading that failed: %v", err)
	}
	st := w.Status()
	if !st.Failing || st.State == nil || !st.State.Clean() || !st.CheckedAt.Equal(start.Add(minGap)) || len(st.History) != 1 {
		t.Fatalf("status %+v", st)
	}
}

func TestWatchSharesOneReading(t *testing.T) {
	a := clean()
	a.hold = make(chan struct{})
	release := sync.OnceFunc(func() { close(a.hold) })
	w, _, _ := watch(t, a)
	t.Cleanup(release)

	const callers = 20
	results := make(chan error, callers)
	for range callers {
		go func() { results <- w.Clean(context.Background()) }()
	}
	soon(t, "SES to be asked", func() bool { return a.waiting() == 1 })
	// Give the others time to ask too, if they were going to.
	time.Sleep(50 * time.Millisecond)
	if got := a.waiting(); got != 1 {
		t.Fatalf("%d readings at once, want 1", got)
	}

	// Whoever stops waiting gets no for an answer, and the reading goes on
	// for the others.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Clean(ctx); !errors.Is(err, errUnread) {
		t.Fatalf("got %v without waiting", err)
	}

	release()
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress}) {
		t.Fatalf("asked %q", got)
	}
}

func TestWatchHistory(t *testing.T) {
	a := clean()
	w, c, _ := watch(t, a)
	ctx := context.Background()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	step := func(domain, address said, want error) {
		t.Helper()
		a.set(domain, address)
		c.add(10*time.Second + 300*time.Millisecond)
		if err := w.Clean(ctx); !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
	if err := w.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	step(domainAlone, noSuch, nil)
	step(domainAlone, addressCopied, errNotClean)
	step(domainAlone, addressCopied, errNotClean)
	// A reading that fails is not a state, and does not end the one before.
	step(broken, noSuch, errUnread)
	step(domainAlone, addressCopied, errNotClean)
	// Undone: mail is sent again, and what was seen stays.
	step(domainAlone, noSuch, nil)

	alone := attestation.MailState{DomainIdentity: attestation.MailNoConfigurationSet, AddressIdentity: attestation.MailAbsent}
	copied := attestation.MailState{DomainIdentity: attestation.MailNoConfigurationSet, AddressIdentity: attestation.MailConfigurationSet}
	st := w.Status()
	want := []attestation.MailObservation{
		{MailState: alone, FirstSeen: at(0), LastSeen: at(10)},
		{MailState: copied, FirstSeen: at(20), LastSeen: at(51)},
		{MailState: alone, FirstSeen: at(61), LastSeen: at(61)},
	}
	if !reflect.DeepEqual(st.History, want) || st.HistoryTruncated {
		t.Fatalf("history %+v", st.History)
	}
	if *st.State != alone || !st.CheckedAt.Equal(at(61)) || !st.WatchedSince.Equal(at(0)) || st.Failing {
		t.Fatalf("status %+v", st)
	}

	// The history is bounded, and says so when it lost something.
	for i := range maxHistory {
		if i%2 == 0 {
			step(domainAlone, addressAlone, nil)
		} else {
			step(domainAlone, noSuch, nil)
		}
	}
	st = w.Status()
	if len(st.History) != maxHistory || !st.HistoryTruncated || st.History[maxHistory-1].MailState != *st.State || *st.State != alone {
		t.Fatalf("%d states, truncated %v, current %+v", len(st.History), st.HistoryTruncated, st.State)
	}
	for _, o := range st.History {
		if o.MailState == copied {
			t.Fatal("the oldest states were not the ones dropped")
		}
	}
}

func TestWatchRun(t *testing.T) {
	t.Run("reads at once and stops", func(t *testing.T) {
		a := clean()
		w, _, _ := watch(t, a)
		stop := run(t, w)
		soon(t, "the first reading", func() bool { return w.Status().State != nil })
		stop()
		if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress}) {
			t.Fatalf("asked %q", got)
		}
	})

	t.Run("reads on its own", func(t *testing.T) {
		a := clean()
		w, _, _ := watch(t, a)
		w.interval = 2 * time.Millisecond
		run(t, w)
		soon(t, "three readings", func() bool { return a.count(askAddress) >= 3 })
	})

	t.Run("reads when asked, a second after the last reading", func(t *testing.T) {
		a := clean()
		w, c, _ := watch(t, a)
		var mu sync.Mutex
		var waited []time.Duration
		waiting := func() int {
			mu.Lock()
			defer mu.Unlock()
			return len(waited)
		}
		proceed := make(chan struct{})
		w.pause = func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			waited = append(waited, d)
			mu.Unlock()
			<-proceed
			c.add(d)
			return ctx.Err()
		}
		run(t, w)
		soon(t, "the first reading", func() bool { return w.Status().State != nil })
		c.add(300 * time.Millisecond)
		w.again()
		soon(t, "the watch to wait", func() bool { return waiting() == 1 })
		// Another message leaves before that reading starts: it is one reading.
		w.again()
		close(proceed)
		soon(t, "the reading asked for", func() bool { return a.count(askAddress) == 2 })
		idle(t, w)
		if pending(w) {
			t.Fatal("a reading is still asked for")
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(waited, []time.Duration{700 * time.Millisecond}) {
			t.Fatalf("waited %v, want what was left of a second", waited)
		}
		if got := w.Status().CheckedAt; !got.Equal(start.Add(minGap)) {
			t.Fatalf("read at %s", got)
		}
		if len(a.requests()) != 4 {
			t.Fatalf("asked %q", a.requests())
		}
	})

	t.Run("reads after the reading that was on its way", func(t *testing.T) {
		a := clean()
		a.hold = make(chan struct{})
		release := sync.OnceFunc(func() { close(a.hold) })
		w, _, _ := watch(t, a)
		t.Cleanup(release)
		// A message is about to leave on this reading.
		before := make(chan error, 1)
		go func() { before <- w.Clean(context.Background()) }()
		soon(t, "SES to be asked", func() bool { return a.waiting() == 1 })
		// It left, and Run is asked: the reading on its way does not do.
		after := make(chan struct{})
		go func() { w.next(context.Background()); close(after) }()
		time.Sleep(20 * time.Millisecond)
		release()
		if err := <-before; err != nil {
			t.Fatal(err)
		}
		<-after
		if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress, askDomain, askAddress}) {
			t.Fatalf("asked %q", got)
		}
	})

	t.Run("stops while it waits", func(t *testing.T) {
		a := clean()
		w, _, _ := watch(t, a)
		ctx, cancel := context.WithCancel(context.Background())
		w.pause = func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		if err := w.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		w.next(ctx)
		if len(a.requests()) != 2 {
			t.Fatalf("asked %q after it was told to stop", a.requests())
		}
		// And when it is told before it starts, it does not start.
		(&Watch{}).next(ctx)
	})

	t.Run("stops while SES does not answer", func(t *testing.T) {
		a := clean()
		a.hold = make(chan struct{})
		release := sync.OnceFunc(func() { close(a.hold) })
		w, _, _ := watch(t, a)
		t.Cleanup(release)
		stop := run(t, w)
		soon(t, "SES to be asked", func() bool { return a.waiting() == 1 })
		stop()
		// The same with a reading someone else started.
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { w.next(ctx); close(done) }()
		time.Sleep(10 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("still waiting for a reading nobody wants")
		}
	})

	t.Run("waits for real", func(t *testing.T) {
		begin := time.Now()
		if err := (&Watch{}).wait(context.Background(), 20*time.Millisecond); err != nil || time.Since(begin) < 20*time.Millisecond {
			t.Errorf("waited %s: %v", time.Since(begin), err)
		}
	})
}

// Whatever the number of messages, SES is asked about once a second at most.
func TestWatchReadsOncePerSecondAtMost(t *testing.T) {
	for name, answer := range map[string]said{"while all is well": domainAlone, "while SES fails": broken} {
		t.Run(name, func(t *testing.T) {
			a := clean()
			a.set(answer, noSuch)
			w, c, _ := watch(t, a)
			stop := run(t, w)
			s := &Sender{
				HTTP: w.HTTP, Credentials: staticCredentials{}, Region: "us-east-2", Name: "Permaily", Address: fromAddress,
				Endpoint: w.Endpoint, Now: c.now, Watch: w,
				Wait: func(context.Context, time.Duration) error { return nil },
			}
			const messages = 200
			var wg sync.WaitGroup
			for i := range messages {
				// Four at a time, each 10 ms of the clock after the one before.
				c.add(10 * time.Millisecond)
				wg.Add(1)
				go func() { defer wg.Done(); _ = s.Send(context.Background(), message()) }()
				if i%4 == 3 {
					wg.Wait()
				}
			}
			wg.Wait()
			stop()
			idle(t, w)
			// The clock moved with the messages and with each wait of the watch.
			most := int(c.now().Sub(start)/minGap) + 1
			readings := a.count(askDomain)
			if readings > most || readings < 2 {
				t.Fatalf("%d readings in %s, want from 2 to %d", readings, c.now().Sub(start), most)
			}
			if sent := a.count(askSend); (answer.status == http.StatusOK) != (sent == messages) || (answer.status != http.StatusOK && sent != 0) {
				t.Fatalf("%d of %d messages sent", sent, messages)
			}
		})
	}
}

// watched is a sender with a watch, both asking the same stand-in.
func watched(t *testing.T, a *account) (*Sender, *clock, *lines, *int) {
	t.Helper()
	w, c, logs := watch(t, a)
	waits := new(int)
	return &Sender{
		HTTP: w.HTTP, Credentials: staticCredentials{}, Region: "us-east-2", Name: "Permaily", Address: fromAddress,
		Endpoint: w.Endpoint, Log: w.Log, Now: c.now, Watch: w,
		Wait: func(context.Context, time.Duration) error { *waits++; return nil },
	}, c, logs, waits
}

// pending says whether the watch has been asked for a reading it has not
// started.
func pending(w *Watch) bool {
	return len(w.asks()) == 1
}

func TestSendWithWatch(t *testing.T) {
	ctx := context.Background()

	t.Run("reads, sends, and has it read again", func(t *testing.T) {
		a := clean()
		s, _, _, _ := watched(t, a)
		if err := s.Send(ctx, message()); err != nil {
			t.Fatal(err)
		}
		if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress, askSend}) {
			t.Fatalf("asked %q", got)
		}
		if !pending(s.Watch) {
			t.Fatal("no reading asked for after the message")
		}
		// Run makes it, without the sender waiting.
		run(t, s.Watch)
		soon(t, "the reading after the message", func() bool { return a.count(askAddress) == 2 })
		if got := a.requests(); !slices.Equal(got, []string{askDomain, askAddress, askSend, askDomain, askAddress}) {
			t.Fatalf("asked %q", got)
		}
	})

	t.Run("sends nothing when someone gets a copy", func(t *testing.T) {
		for name, set := range map[string]func(a *account){
			"of the domain":  func(a *account) { a.set(domainCopied, noSuch) },
			"of the address": func(a *account) { a.set(domainAlone, addressCopied) },
		} {
			t.Run(name, func(t *testing.T) {
				a := clean()
				set(a)
				s, c, logs, waits := watched(t, a)
				err := s.Send(ctx, message())
				if !errors.Is(err, errNotClean) {
					t.Fatalf("got %v", err)
				}
				// Trying again changes nothing, so it is not tried.
				if a.count(askSend) != 0 || *waits != 0 || len(a.requests()) != 2 || pending(s.Watch) {
					t.Fatalf("asked %q, waited %d times", a.requests(), *waits)
				}
				saysNothing(t, err.Error())
				saysNothing(t, logs.String())
				tellsNothing(t, err.Error())
				tellsNothing(t, logs.String())

				// Undone, and read again: mail is sent.
				a.set(domainAlone, noSuch)
				c.add(maxAge + time.Nanosecond)
				if err := s.Send(ctx, message()); err != nil || a.count(askSend) != 1 {
					t.Fatalf("%v, %d sent", err, a.count(askSend))
				}
			})
		}
	})

	t.Run("sends nothing when it cannot read", func(t *testing.T) {
		a := clean()
		a.set(domainAlone, broken)
		s, _, logs, waits := watched(t, a)
		err := s.Send(ctx, message())
		if !errors.Is(err, errUnread) {
			t.Fatalf("got %v", err)
		}
		// It may work a moment later, so it is tried as often as a message is.
		if a.count(askSend) != 0 || *waits != attempts-1 || pending(s.Watch) {
			t.Fatalf("asked %q, waited %d times", a.requests(), *waits)
		}
		// The watch says so once; the sender does not blame the network.
		if strings.Count(logs.String(), "could not be read") != 1 || strings.Contains(logs.String(), "could not be reached") {
			t.Errorf("logged %s", logs)
		}
		saysNothing(t, logs.String())
		tellsNothing(t, logs.String())
	})

	t.Run("sends once it can read", func(t *testing.T) {
		a := clean()
		a.set(domainAlone, broken)
		s, c, _, _ := watched(t, a)
		s.Wait = func(context.Context, time.Duration) error {
			a.set(domainAlone, noSuch)
			c.add(minGap)
			return nil
		}
		if err := s.Send(ctx, message()); err != nil || a.count(askSend) != 1 {
			t.Fatalf("%v, %d sent", err, a.count(askSend))
		}
	})

	t.Run("has it read again whatever SES said", func(t *testing.T) {
		a := clean()
		a.send = http.StatusBadRequest
		s, _, _, _ := watched(t, a)
		var refused *Error
		if err := s.Send(ctx, message()); !errors.As(err, &refused) {
			t.Fatalf("got %v", err)
		}
		// SES said no, and may have taken the message all the same.
		if !pending(s.Watch) {
			t.Fatal("no reading asked for after a message SES refused")
		}
	})

	t.Run("reads before each try", func(t *testing.T) {
		a := clean()
		a.send = http.StatusServiceUnavailable
		s, c, _, _ := watched(t, a)
		n := 0
		s.Wait = func(context.Context, time.Duration) error {
			// Before the last try someone puts a configuration set there.
			if n++; n == attempts-1 {
				a.set(domainAlone, addressCopied)
			}
			c.add(maxAge + time.Nanosecond)
			return nil
		}
		if err := s.Send(ctx, message()); !errors.Is(err, errNotClean) {
			t.Fatalf("got %v", err)
		}
		if a.count(askSend) != attempts-1 || a.count(askAddress) != attempts {
			t.Fatalf("asked %q", a.requests())
		}
	})
}

func TestWatchLogs(t *testing.T) {
	a := clean()
	w, c, logs := watch(t, a)
	ctx := context.Background()
	read := func(domain, address said) {
		a.set(domain, address)
		c.add(maxAge + time.Nanosecond)
		_ = w.Clean(ctx)
	}
	count := func(what string) int { return strings.Count(logs.String(), what) }

	read(domainAlone, noSuch)
	read(domainAlone, noSuch)
	read(domainAlone, noSuch)
	if count(`"msg"`) != 1 || count(`"level":"INFO"`) != 1 || count("nothing else; mail is sent") != 1 {
		t.Fatalf("the same reading three times: %s", logs)
	}
	read(domainAlone, addressCopied)
	read(domainAlone, addressCopied)
	if count(`"msg"`) != 2 || count(`"level":"ERROR"`) != 1 || count(`"domain_identity":"no_configuration_set","address_identity":"configuration_set"`) != 1 {
		t.Fatalf("a configuration set: %s", logs)
	}
	read(broken, noSuch)
	read(broken, noSuch)
	if count(`"msg"`) != 3 || count(`"level":"WARN"`) != 1 || count(`"status":500`) != 1 {
		t.Fatalf("SES failing: %s", logs)
	}
	read(domainAlone, addressCopied)
	if count(`"msg"`) != 4 || count(`"level":"ERROR"`) != 2 {
		t.Fatalf("read again: %s", logs)
	}
	read(domainAlone, noSuch)
	if count(`"msg"`) != 5 || count(`"level":"INFO"`) != 2 {
		t.Fatalf("undone: %s", logs)
	}
	tellsNothing(t, logs.String())

	// Without a logger nothing breaks.
	w.Log = nil
	read(domainCopied, noSuch)
}

func TestWhereSESIsAsked(t *testing.T) {
	var asked []string
	w := &Watch{
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			asked = append(asked, r.URL.String())
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"IdentityType":"DOMAIN","VerifiedForSendingStatus":true}`))}, nil
		})},
		Credentials: staticCredentials{}, Region: "us-east-2", Domain: fromDomain, Address: fromAddress,
	}
	// The answer about the address is of a domain: no reading, and the real
	// clock, which the watch uses when it is given none.
	if err := w.Clean(context.Background()); !errors.Is(err, errUnread) {
		t.Fatalf("got %v", err)
	}
	want := []string{
		"https://email.us-east-2.amazonaws.com/v2/email/identities/mail.example.org",
		"https://email.us-east-2.amazonaws.com/v2/email/identities/no-reply%40mail.example.org",
	}
	if !slices.Equal(asked, want) {
		t.Fatalf("asked %q", asked)
	}
	if at := w.Status().WatchedSince; time.Since(at) > time.Minute || time.Since(at) < 0 {
		t.Fatalf("watched since %s", at)
	}
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		fromDomain:        fromDomain,
		fromAddress:       "no-reply%40mail.example.org",
		"a+b@example.org": "a%2Bb%40example.org",
		"a/b?c#d%e f":     "a%2Fb%3Fc%23d%25e%20f",
		"AZaz09-._~":      "AZaz09-._~",
		"ñ":               "%C3%B1",
	} {
		if got := escape(in); got != want {
			t.Errorf("%q written as %q, want %q", in, got, want)
		}
	}
}

func TestNoWatch(t *testing.T) {
	var w *Watch
	if w.Status() != nil {
		t.Fatal("a watch that is not there reports mail")
	}
}
