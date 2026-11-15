package decisions

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

var update = flag.Bool("update", false, "write the alarms' golden text")

// smallDay is a day's alarm at two blocks, so the third block crosses it.
var smallDay = Limits{Month: DefaultLimits.Month, DayAlarm: 2 * block, AccountDay: DefaultLimits.AccountDay, Block: block}

// A15. The alarms, in the one record of alarms.
func TestA15TheAlarmsInTheOneRecordOfAlarms(t *testing.T) {
	ctx := context.Background()
	today := "2026-11-16"
	t.Run("the grant that crosses the day's alarm adds it before the grant is written", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(smallDay))
		e.reserveOnce(block, "")
		e.reserveOnce(block, "")
		if len(w.alarms().Entries) != 0 && w.alarms().count(AlarmDaySpend) != 0 {
			t.Fatal("due before the day crossed")
		}
		checked := false
		e.records.onPut(func(name string) error {
			if name == RecordSpend+month {
				if _, ok := w.alarms().has(AlarmDaySpend, today); !ok {
					t.Error("the grant is written before its alarm")
				}
				checked = true
			}
			return nil
		})
		if h := e.reserveOnce(block, ""); h != holdNone || !checked {
			t.Fatalf("hold %v", h)
		}
		e.records.onPut(nil)
		// RunAlarms reads that record and only it, raises it and marks it
		// sent.
		e.records.onGet(func(name string, _ int) (bool, *store.Record, store.Mark, error) {
			if name != RecordAlarms {
				t.Errorf("RunAlarms read %s", name)
			}
			return false, nil, store.Mark{}, nil
		})
		if err := e.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		e.records.onGet(nil)
		if got := w.alarm.sent(); !slices.Equal(got, []string{"day_spend " + today}) {
			t.Fatalf("sent %v", got)
		}
		if a, _ := w.alarms().has(AlarmDaySpend, today); a.SentAt.IsZero() {
			t.Fatal("not marked sent")
		}
		// Each kind at most once a day.
		e.reserveOnce(block, "")
		e.guard.mu.Lock()
		e.guard.grants = nil
		e.guard.mu.Unlock()
		e.reserveOnce(block, "")
		_ = e.RunAlarms(ctx)
		if w.alarms().count(AlarmDaySpend) != 1 || len(w.alarm.sent()) != 1 {
			t.Fatalf("%+v, sent %v", w.alarms().Entries, w.alarm.sent())
		}
	})
	t.Run("a failed send is sent by a later RunAlarms of another service, after the day changed", func(t *testing.T) {
		w := newWorld(t)
		a := w.keyed(withLimits(smallDay))
		for range 3 {
			a.guard.mu.Lock()
			a.guard.grants = nil
			a.guard.mu.Unlock()
			a.reserveOnce(block, "")
		}
		w.alarm.fail(true)
		if err := a.RunAlarms(ctx); err == nil {
			t.Fatal("no error for a failed send")
		}
		if e, _ := w.alarms().has(AlarmDaySpend, today); !e.SentAt.IsZero() {
			t.Fatal("marked sent after Raise failed")
		}
		if !strings.Contains(w.logs.String(), "kind=day_spend") || strings.Contains(w.logs.String(), "hello@permaily.com") {
			t.Fatalf("logged %q", w.logs.String())
		}
		w.alarm.fail(false)
		w.advance(48 * time.Hour)
		b := w.enclave()
		if err := b.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := w.alarm.sent(); !slices.Equal(got, []string{"day_spend " + today}) {
			t.Fatalf("sent %v", got)
		}
	})
	t.Run("a grant refused for the month's limit adds that alarm with no grant", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 500)
		e := w.keyed()
		if h := e.reserveOnce(block, ""); h != holdLimit {
			t.Fatal(h)
		}
		if d := w.spend(month); len(d.Grants) != 1 {
			t.Fatalf("a grant was made: %+v", d.Grants)
		}
		if err := e.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := w.alarm.sent(); !slices.Equal(got, []string{"month_limit " + today}) {
			t.Fatalf("sent %v", got)
		}
	})
	t.Run("added at the version read, retried at most RecordTries times; twice changes nothing", func(t *testing.T) {
		w := newWorld(t)
		e, other := w.enclave(), w.enclave()
		if err := e.addAlarm(ctx, AlarmCanaryFailed, today, "b1"); err != nil {
			t.Fatal(err)
		}
		if err := e.addAlarm(ctx, AlarmCanaryFailed, today, "b1"); err != nil {
			t.Fatal(err)
		}
		if w.alarms().count(AlarmCanaryFailed) != 1 {
			t.Fatal("added twice")
		}
		tries := 0
		e.records.onPut(func(name string) error {
			if name == RecordAlarms {
				tries++
				_ = other.addAlarm(ctx, AlarmKeyRefused, "2026-11-0"+itoa(tries), "") // another enclave writes first
			}
			return nil
		})
		if err := e.addAlarm(ctx, AlarmProviderLimit, today, ""); !errors.Is(err, errBusy) || tries != RecordTries {
			t.Fatalf("%v after %d tries", err, tries)
		}
		e.records.onPut(nil)
		// Kept in memory, and added by the next RunAlarms.
		if err := e.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := w.alarms().has(AlarmProviderLimit, today); !ok {
			t.Fatal("the pending alarm was not added")
		}
	})
	t.Run("sent stays sent; sent ones older than 30 days are dropped, due ones never", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		_ = e.addAlarm(ctx, AlarmCanaryChanged, today, "b1")
		_ = e.RunAlarms(ctx)
		w.alarm.fail(true)
		_ = e.addAlarm(ctx, AlarmCanaryFailed, today, "b1")
		_ = e.RunAlarms(ctx)
		w.advance(31 * 24 * time.Hour)
		_ = e.addAlarm(ctx, AlarmKeyRefused, "2026-12-17", "")
		d := w.alarms()
		if _, ok := d.has(AlarmCanaryChanged, today); ok {
			t.Fatal("a sent alarm older than 30 days was kept")
		}
		if a, ok := d.has(AlarmCanaryFailed, today); !ok || !a.SentAt.IsZero() {
			t.Fatal("a due alarm was dropped")
		}
	})
	t.Run("an alarm that cannot be added sends no attempt for its grant", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(smallDay))
		e.reserveOnce(2*block, "")
		e.records.onPut(func(name string) error {
			if name == RecordAlarms {
				return errors.New("storage")
			}
			return nil
		})
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 0 || len(w.spend(month).Grants) != 1 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("the crash between adding the alarm and writing the grant", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(smallDay))
		e.reserveOnce(2*block, "")
		e.records.onPut(func(name string) error {
			if name == RecordSpend+month {
				return errors.New("the enclave stopped")
			}
			return nil
		})
		if h := e.reserveOnce(block, ""); h != holdLimit {
			t.Fatal(h)
		}
		if len(w.spend(month).Grants) != 1 {
			t.Fatal("the grant was recorded")
		}
		restarted := w.keyed(withLimits(smallDay))
		if err := restarted.RunAlarms(ctx); err != nil || len(w.alarm.sent()) != 1 {
			t.Fatalf("%v, %v", err, w.alarm.sent())
		}
		if h := restarted.reserveOnce(block, ""); h != holdNone {
			t.Fatal(h)
		}
		if w.alarms().count(AlarmDaySpend) != 1 || len(w.spend(month).Grants) != 2 {
			t.Fatalf("%+v", w.alarms().Entries)
		}
	})
	t.Run("the month's rollover", func(t *testing.T) {
		w := newWorld(t)
		w.set(time.Date(2026, 11, 30, 23, 50, 0, 0, time.UTC))
		a, b := w.keyed(withLimits(smallDay)), w.keyed(withLimits(smallDay))
		a.reserveOnce(2*block, "")
		b.reserveOnce(block, "")
		w.alarm.fail(true)
		_ = a.RunAlarms(ctx)
		_ = b.RunAlarms(ctx)
		w.set(time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC))
		w.alarm.fail(false)
		a, b = w.keyed(withLimits(smallDay)), w.keyed(withLimits(smallDay))
		if err := b.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := w.alarm.sent(); !slices.Equal(got, []string{"day_spend 2026-11-30"}) {
			t.Fatalf("sent %v", got)
		}
		_ = a.RunAlarms(ctx)
		if e, _ := w.alarms().has(AlarmDaySpend, "2026-11-30"); e.SentAt.IsZero() || len(w.alarm.sent()) != 1 {
			t.Fatal("the sent mark was not kept")
		}
	})
	t.Run("a crash between sending and marking sends it twice", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		_ = e.addAlarm(ctx, AlarmKeyRefused, today, "")
		e.records.onPut(func(name string) error { return errors.New("the enclave stopped") })
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("no error")
		}
		e.records.onPut(nil)
		_ = e.RunAlarms(ctx)
		if got := w.alarm.sent(); len(got) != 2 {
			t.Fatalf("sent %v", got)
		}
	})
	t.Run("an earlier copy is refused, a forked record sends nothing", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		_ = e.addAlarm(ctx, AlarmKeyRefused, today, "")
		earlier, _ := w.records.GetRecord(ctx, w.versions(RecordAlarms)[0])
		_ = e.RunAlarms(ctx)
		current := w.versions(RecordAlarms)[0]
		w.records.SetRecord(store.Record{Name: current, Value: earlier.Value})
		if err := w.enclave().RunAlarms(ctx); err == nil || len(w.alarm.sent()) != 1 {
			t.Fatalf("%v, sent %v", err, w.alarm.sent())
		}
		w2 := newWorld(t)
		e2 := w2.enclave()
		_ = e2.addAlarm(ctx, AlarmKeyRefused, today, "")
		w2.commits.Fork(RecordAlarms, 1, sha256.Sum256([]byte("another")))
		if err := w2.enclave().RunAlarms(ctx); err == nil || len(w2.alarm.sent()) != 0 {
			t.Fatalf("%v, sent %v", err, w2.alarm.sent())
		}
		if !strings.Contains(w2.logs.String(), "kind=alarms_unreadable") {
			t.Fatal(w2.logs.String())
		}
	})
	t.Run("Raise is bounded by AlarmTimeout", func(t *testing.T) {
		w := newWorld(t)
		var left time.Duration
		e := w.enclave(withAlarm(alarmFunc(func(ctx context.Context, _ AlarmKind, _ string) error {
			d, _ := ctx.Deadline()
			left = time.Until(d)
			return nil
		})))
		_ = e.addAlarm(ctx, AlarmKeyRefused, today, "")
		if err := e.RunAlarms(ctx); err != nil || left <= 0 || left > AlarmTimeout {
			t.Fatalf("%v, %v left", err, left)
		}
	})
	t.Run("no Alarm given sends nothing and says so", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave(withAlarm(nil))
		_ = e.addAlarm(ctx, AlarmKeyRefused, today, "")
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("no error")
		}
	})
}

type alarmFunc func(ctx context.Context, kind AlarmKind, day string) error

func (f alarmFunc) Raise(ctx context.Context, kind AlarmKind, day string) error {
	return f(ctx, kind, day)
}

// fakeMailer keeps what it was given, or fails.
type fakeMailer struct {
	sent []accounts.Mail
	err  error
}

func (m *fakeMailer) Send(_ context.Context, mail accounts.Mail) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, mail)
	return nil
}

// A22. The alarm's words: golden text for each kind.
func TestA22TheAlarmsWords(t *testing.T) {
	ctx := context.Background()
	kinds := []AlarmKind{AlarmDaySpend, AlarmMonthLimit, AlarmProviderLimit, AlarmKeyRefused, AlarmCanaryChanged, AlarmCanaryFailed}
	if len(alarmWords) != len(kinds) {
		t.Fatalf("%d kinds have words", len(alarmWords))
	}
	for _, kind := range kinds {
		m := &fakeMailer{}
		a := &MailAlarm{Mailer: m, To: "hello@permaily.com"}
		if err := a.Raise(ctx, kind, "2026-11-16"); err != nil || len(m.sent) != 1 {
			t.Fatalf("%s: %v", kind, err)
		}
		got := m.sent[0]
		if got.To != "hello@permaily.com" || got.HTML == "" || !strings.Contains(got.Text, "2026-11-16") {
			t.Fatalf("%s: %+v", kind, got)
		}
		text := "Subject: " + got.Subject + "\n\n" + got.Text
		path := filepath.Join("testdata", "alarm-"+string(kind)+".txt")
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", kind, err)
		}
		if text != string(want) {
			t.Errorf("%s:\n%s\nwant:\n%s", kind, text, want)
		}
		for _, banned := range []string{"$", "USD", "acc_", "mbx_"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s holds %q", kind, banned)
			}
		}
	}
	t.Run("refused: an unknown kind, a day that is not one, no mailer", func(t *testing.T) {
		m := &fakeMailer{}
		for _, a := range []*MailAlarm{{Mailer: m, To: "a@b.c"}, {To: "a@b.c"}, {Mailer: m}} {
			if a.Raise(ctx, "nope", "2026-11-16") == nil || a.Raise(ctx, AlarmKeyRefused, "<b>") == nil {
				t.Fatal("raised")
			}
		}
		if len(m.sent) != 0 {
			t.Fatal("sent")
		}
	})
	t.Run("a mail system's error is logged by kind", func(t *testing.T) {
		w := newWorld(t)
		m := &fakeMailer{err: errors.New("ses: 400 for hello@permaily.com")}
		e := w.enclave(withAlarm(&MailAlarm{Mailer: m, To: "hello@permaily.com"}))
		_ = e.addAlarm(ctx, AlarmMonthLimit, "2026-11-16", "")
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("no error")
		}
		logs := w.logs.String()
		if !strings.Contains(logs, "kind=month_limit") || strings.Contains(logs, "hello@") || strings.Contains(logs, "ses:") {
			t.Fatalf("logged %q", logs)
		}
	})
}

// A15. An alarm authorises its cause only once a fresh strict read finds it
// in the record of alarms, every time (the review of pull request 2,
// finding 3).
func TestA15AnAlarmIsAcceptedOnlyFromTheRecord(t *testing.T) {
	ctx := context.Background()
	t.Run("an append forked before its return authorises no grant", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(smallDay))
		e.reserveOnce(2*block, "")
		if err := e.RunAlarms(ctx); err != nil { // the record of alarms is made
			t.Fatal(err)
		}
		e.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			stored, err := inner(c)
			if err != nil {
				return stored, err
			}
			if c.Name != RecordAlarms {
				t.Fatalf("the first append is %s", c.Name)
			}
			// Another enclave's append at the same version, over a delete
			// marker: this one returned nil and is not the record's line.
			w.commits.Fork(c.Name, c.Version, sha256.Sum256([]byte("the other append")))
			return stored, nil
		})
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 0 || len(w.spend(month).Grants) != 1 {
			t.Fatalf("%d attempts and %d grants for an alarm no RunAlarms can send", out.Called, len(w.spend(month).Grants))
		}
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("the fork was not found")
		}
	})
	t.Run("an alarm added before its record was lost authorises nothing after", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed(withLimits(smallDay))
		e.reserveOnce(3*block, "") // one grant that crosses the alarm, nothing left of it
		for _, name := range w.versions(RecordAlarms) {
			_ = w.records.DeleteRecord(ctx, name)
		}
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("the record of alarms is lost")
		}
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 0 || len(w.spend(month).Grants) != 1 {
			t.Fatalf("%d attempts and %d grants after the alarm was lost", out.Called, len(w.spend(month).Grants))
		}
	})
}

// A15. The guard's and the canary's observations are published only once
// their alarm is in the record; until then the guard still stops, and
// RunAlarms publishes them once it adds the alarm (the review of pull
// request 2, finding 4).
func TestA15AnObservationIsPublishedOnlyAfterItsAlarm(t *testing.T) {
	ctx := context.Background()
	failAlarms := func(e *enclave, failing *bool, check func()) {
		e.records.onPut(func(name string) error {
			if name != RecordAlarms {
				return nil
			}
			if !*failing {
				return nil
			}
			if check != nil {
				check()
			}
			return errors.New("storage")
		})
	}
	for _, tc := range []struct {
		err     error
		guard   string
		outcome inbox.AskOutcome
	}{
		{decide.ErrKeyRefused, attestation.ModelGuardKeyRefused, inbox.AskOff},
		{decide.ErrProviderLimit, attestation.ModelGuardProviderLimit, inbox.AskLimit},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
				return nil, &decide.AttemptError{Err: tc.err, Sent: true}
			})
			failing, seen := true, false
			failAlarms(e, &failing, func() {
				seen = true
				if got := e.guardState(ctx); got == tc.guard {
					t.Errorf("%s published before its alarm's write", got)
				}
			})
			e.ask(newQuestion("m1", "Hi", "", subj("x")))
			if !seen {
				t.Fatal("no alarm was tried")
			}
			if got := e.Status(ctx).Guard; got == tc.guard {
				t.Fatalf("%s published although its alarm was not added", got)
			}
			if out := e.ask(newQuestion("m2", "Hi", "", subj("x"))); out.Called != 0 || out.Outcome != tc.outcome {
				t.Fatalf("the guard does not stop while its alarm is pending: %+v", out)
			}
			failing = false
			if err := e.RunAlarms(ctx); err != nil {
				t.Fatal(err)
			}
			if got := e.Status(ctx).Guard; got != tc.guard {
				t.Fatalf("not published once its alarm was added: %s", got)
			}
		})
	}
	t.Run("the month's limit", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 500)
		e := w.keyed()
		failing := true
		failAlarms(e, &failing, nil)
		if out := e.ask(newQuestion("m1", "Hi", "", subj("x"))); out.Called != 0 || out.Outcome != inbox.AskLimit {
			t.Fatalf("%+v", out)
		}
		if got := e.Status(ctx).Guard; got == attestation.ModelGuardMonthLimit {
			t.Fatalf("%s published although its alarm was not added", got)
		}
		failing = false
		if err := e.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := e.Status(ctx).Guard; got != attestation.ModelGuardMonthLimit {
			t.Fatalf("not published once its alarm was added: %s", got)
		}
	})
	t.Run("a stale canary", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true}
		})
		_ = e.RunCanary(ctx)
		w.advance(48 * time.Hour)
		failing := true
		failAlarms(e, &failing, nil)
		_ = e.RunCanary(ctx)
		if got := e.canaryStateNow(ctx); got == attestation.CanaryStale {
			t.Fatalf("%s published although its alarm was not added", got)
		}
		failing = false
		if err := e.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := e.canaryStateNow(ctx); got != attestation.CanaryStale {
			t.Fatalf("not published once its alarm was added: %s", got)
		}
	})
}
