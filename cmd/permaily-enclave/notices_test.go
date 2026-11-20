package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/sesmail"
	"github.com/permaily-org/permaily/internal/store"
)

// playedSES is SES as the watch and the sender see it (ADR 0021): the two
// identities, the domain's with a configuration set while copied is set,
// and the messages it was given.
type playedSES struct {
	copied atomic.Bool
	mu     sync.Mutex
	sent   []sesMessage
}

// sesMessage is what a message given to SES said.
type sesMessage struct{ to, subject, text string }

func (s *playedSES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/email/identities/"):
		if strings.Contains(r.URL.EscapedPath(), "%40") {
			// No identity for the address alone.
			w.Header().Set("X-Amzn-Errortype", "NotFoundException")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		set := ""
		if s.copied.Load() {
			set = "archive-everything"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"IdentityType": "DOMAIN", "ConfigurationSetName": set, "VerifiedForSendingStatus": true})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/email/outbound-emails":
		var m struct {
			Destination struct{ ToAddresses []string }
			Content     struct {
				Simple struct {
					Subject struct{ Data string }
					Body    struct{ Text struct{ Data string } }
				}
			}
		}
		if json.Unmarshal(body, &m) != nil || len(m.Destination.ToAddresses) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.sent = append(s.sent, sesMessage{to: m.Destination.ToAddresses[0], subject: m.Content.Simple.Subject.Data, text: m.Content.Simple.Body.Text.Data})
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"MessageId":"played"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// with is the messages SES was given with the subject.
func (s *playedSES) with(subject string) []sesMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sesMessage
	for _, m := range s.sent {
		if m.subject == subject {
			out = append(out, m)
		}
	}
	return out
}

func (s *playedSES) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// playedClock is a clock a test moves.
type playedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *playedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *playedClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// unreadableRecords is the table of accounts' records, whose reads of one
// account's records fail while unreadable names it: a storage that does
// not answer for that account.
type unreadableRecords struct {
	*store.Memory
	unreadable atomic.Pointer[string]
}

var errUnreadable = errors.New("played: the storage does not answer")

func (u *unreadableRecords) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	if id := u.unreadable.Load(); id != nil && strings.Contains(name, *id) {
		return nil, errUnreadable
	}
	return u.Memory.GetRecord(ctx, name)
}

// noticesWorld is the queue composed as the image composes it, accounts
// its Deliverer and sending through sesmail behind its watch, over a
// played SES and a table in memory.
type noticesWorld struct {
	ses     *playedSES
	clock   *playedClock
	watched *playedClock
	records *unreadableRecords
	queue   *composedNotices
	acc     *accounts.Service
	logs    *strings.Builder
	logMu   *sync.Mutex
}

// lockedWriter is a log's buffer written from several goroutines.
type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func newNoticesWorld(t *testing.T) *noticesWorld {
	t.Helper()
	ses := &playedSES{}
	srv := httptest.NewServer(ses)
	t.Cleanup(srv.Close)
	mem, commits := store.NewMemory(), store.NewMemoryCommits()
	local, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w := &noticesWorld{
		ses: ses, clock: &playedClock{t: time.Now().UTC()}, watched: &playedClock{t: time.Now().UTC()},
		records: &unreadableRecords{Memory: mem}, logs: &strings.Builder{}, logMu: &sync.Mutex{},
	}
	log := slog.New(slog.NewJSONHandler(lockedWriter{w.logMu, w.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "played", SecretAccessKey: "played"}, nil
	})
	watch := &sesmail.Watch{
		HTTP: srv.Client(), Credentials: creds, Region: "us-east-2", Domain: "mail.permaily.test", Address: "no-reply@mail.permaily.test",
		Endpoint: srv.URL, Log: log, Now: w.watched.now,
	}
	sender := &sesmail.Sender{
		HTTP: srv.Client(), Credentials: creds, Region: "us-east-2", Name: "Permaily", Address: "no-reply@mail.permaily.test",
		Endpoint: srv.URL, Log: log, Watch: watch, Wait: func(context.Context, time.Duration) error { return nil },
	}
	if w.queue, err = composeNotices(noticesParts{entries: mem, keys: local, now: w.clock.now, log: log}); err != nil {
		t.Fatal(err)
	}
	if w.acc, err = accounts.New(accounts.Config{
		Records: &store.Anchored{Records: w.records, Commits: commits, OnlyNewest: true, Now: w.clock.now}, Entries: mem, Keys: local,
		Mailer: sender, Notices: w.queue.svc, AppURL: "https://app.permaily.test", Now: w.clock.now, Log: log,
	}); err != nil {
		t.Fatal(err)
	}
	w.queue.deliverBy(w.acc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runNotices(ctx, w.queue)
	return w
}

// signUp makes an account at address through the played SES, and returns
// its identifier.
func (w *noticesWorld) signUp(t *testing.T, address string) string {
	t.Helper()
	ctx := context.Background()
	inv, err := w.acc.CreateInvitations(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	started, err := w.acc.StartEmail(ctx, contracts.EmailStart{Address: address, Invitation: inv.Invitations[0], Terms: domain.TermsVersion})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sign-in message", func() bool { return len(w.ses.with("Sign in to Permaily")) > 0 })
	signIn := w.ses.with("Sign in to Permaily")
	code := regexp.MustCompile(`\n    (\d{6})\n`).FindStringSubmatch(signIn[len(signIn)-1].text)
	if code == nil {
		t.Fatalf("no code in %q", signIn[len(signIn)-1].text)
	}
	me, _, err := w.acc.FinishEmail(ctx, contracts.EmailFinish{Attempt: started.Attempt, Code: code[1]}, accounts.Browser{})
	if err != nil {
		t.Fatal(err)
	}
	return me.Account.ID
}

// drain makes one pass of the queue that tries every notice whose lease
// ended, due or not, and waits for its attempts.
func (w *noticesWorld) drain(t *testing.T) {
	t.Helper()
	if err := w.queue.svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// E27 (ADR 0041): while the watch of ADR 0021 refuses to send, attempts
// fail and are tried again, and nothing is sent through another path; once
// it lets mail go, the notice is sent once. An account whose record cannot
// be read for an hour gets its notice once it can, and is not skipped. The
// queue, accounts' Deliver, the sender and its watch are the image's,
// composed as it composes them (composeNotices, deliverBy), over a played
// SES; the log holds nothing of the account.
func TestE27TheWatchAndAnUnreadableAccountHoldNoticesBack(t *testing.T) {
	w := newNoticesWorld(t)
	ctx := context.Background()
	id := w.signUp(t, "dana@example.org")
	before := w.ses.count()
	const signedOut = "Every device was signed out of your Permaily account"

	// SES would keep a copy: the watch refuses, the operator's sign-out is
	// queued and its attempts fail, again and again, and nothing leaves.
	w.ses.copied.Store(true)
	w.watched.add(10 * time.Second)
	if _, err := w.acc.CloseAccountSessions(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first attempt to fail", func() bool { return w.queue.status().Failed >= 1 })
	for range 3 {
		w.watched.add(10 * time.Second)
		w.clock.add(notices.RetryMax)
		w.drain(t)
	}
	if st := w.queue.status(); st.Failed < 4 || st.Sent != 0 || st.Skipped != 0 || st.Dropped != 0 || st.Queued != 1 {
		t.Fatalf("while the watch refuses: %+v", st)
	}
	if w.ses.count() != before {
		t.Fatalf("SES was given %d messages while its watch refused", w.ses.count()-before)
	}

	// SES as it should be: the next attempt sends it, once.
	w.ses.copied.Store(false)
	w.watched.add(10 * time.Second)
	w.drain(t)
	w.drain(t)
	if got := w.ses.with(signedOut); len(got) != 1 || got[0].to != "dana@example.org" || !strings.Contains(got[0].text, "Permaily's operator signed every device out") {
		t.Fatalf("the sign-out's notice: %+v", got)
	}
	if st := w.queue.status(); st.Sent != 1 || st.Skipped != 0 || st.Dropped != 0 {
		t.Fatalf("after the watch let it go: %+v", st)
	}

	// The account's record cannot be read for an hour: an incident's
	// notice waits, tried again, never skipped, nothing sent; once it can
	// be read, it is sent once.
	const incident = "Some of your receipts were deleted or changed"
	w.records.unreadable.Store(&id)
	at := w.clock.now()
	if err := w.queue.svc.Enqueue(ctx, notices.Notice{Account: id, Kind: notices.KindReceiptsIncident, Key: notices.IncidentKey(id, at), At: at}); err != nil {
		t.Fatal(err)
	}
	failed := w.queue.status().Failed
	waitFor(t, "the first attempt to fail", func() bool { return w.queue.status().Failed > failed })
	for range 6 {
		w.clock.add(10 * time.Minute)
		w.drain(t)
	}
	if st := w.queue.status(); st.Sent != 1 || st.Skipped != 0 || st.Dropped != 0 || st.Failed < failed+7 || len(w.ses.with(incident)) != 0 {
		t.Fatalf("an hour unreadable: %+v", st)
	}
	w.records.unreadable.Store(nil)
	w.watched.add(10 * time.Second)
	w.drain(t)
	w.drain(t)
	if got := w.ses.with(incident); len(got) != 1 || got[0].to != "dana@example.org" {
		t.Fatalf("the incident's notice: %+v", got)
	}
	if st := w.queue.status(); st.Sent != 2 || st.Skipped != 0 || st.Dropped != 0 || st.Refused != 0 {
		t.Fatalf("at the end: %+v", st)
	}
	w.logMu.Lock()
	logs := w.logs.String()
	w.logMu.Unlock()
	for _, never := range []string{id, id[len("acc_"):], "dana", "example.org", "archive-everything"} {
		if strings.Contains(logs, never) {
			t.Fatalf("the log holds %q", never)
		}
	}
}

// waitFor waits for what another goroutine does.
func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting for %s", what)
		}
	}
}

// The queue tries again what it could not send before accounts were given
// to it, and the composition refuses a queue without its storage.
func TestTheQueueWaitsForItsDeliverer(t *testing.T) {
	d := &lateDeliverer{}
	if err := d.Deliver(context.Background(), notices.Notice{}); !errors.Is(err, errNoDeliverer) || errors.Is(err, notices.ErrSkip) || errors.Is(err, notices.ErrInvalid) {
		t.Fatalf("before accounts: %v", err)
	}
	if _, err := composeNotices(noticesParts{}); !errors.Is(err, notices.ErrConfig) {
		t.Fatalf("no storage: %v", err)
	}
}
