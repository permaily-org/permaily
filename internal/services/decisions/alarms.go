package decisions

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"
)

// The alarms (ADR 0028, "The alarm"). An alarm is due in one place only,
// RecordAlarms, which every image reads: the enclave that sees a cause adds
// the alarm there first and records the cause after; RunAlarms, on its own
// ticker, raises every alarm due there and marks it sent once Raise
// returned nil. Keyed by kind, day and, for the canary's kinds, baseline,
// each is due at most once a day.

// alarmKeep is how long a sent alarm stays in the record; a due one stays
// until it is sent.
const alarmKeep = 30 * 24 * time.Hour

// alarmsDoc is the record of alarms, as sealed.
type alarmsDoc struct {
	V       int          `json:"v"`
	Entries []alarmEntry `json:"entries"`
}

// alarmEntry is one alarm: synthetic, no account, no amount, nothing of a
// message.
type alarmEntry struct {
	Kind     AlarmKind `json:"kind"`
	Day      string    `json:"day"`
	Baseline string    `json:"baseline,omitempty"`
	DueAt    time.Time `json:"due_at"`
	SentAt   time.Time `json:"sent_at,omitzero"`
}

func (e alarmEntry) key() string { return string(e.Kind) + "/" + e.Day + "/" + e.Baseline }

func (d *alarmsDoc) find(key string) int {
	return slices.IndexFunc(d.Entries, func(e alarmEntry) bool { return e.key() == key })
}

// drop forgets the sent alarms older than alarmKeep; a due one never.
func (d *alarmsDoc) drop(now time.Time) {
	d.Entries = slices.DeleteFunc(d.Entries, func(e alarmEntry) bool {
		return !e.SentAt.IsZero() && now.Sub(e.SentAt) > alarmKeep
	})
}

// addAlarm makes an alarm due in RecordAlarms, at the version read; adding
// one that is there changes nothing. It returns nil only once a fresh
// strict read found the alarm in the record, every time it is asked: what
// this enclave added before is no evidence once the record was lost or
// forked since. An alarm that cannot be added is kept in memory and added
// by the next RunAlarms, and the error says the cause must not be
// recorded.
func (s *Service) addAlarm(ctx context.Context, kind AlarmKind, day, baseline string) error {
	e := alarmEntry{Kind: kind, Day: day, Baseline: baseline, DueAt: s.now()}
	k := e.key()
	s.alarmMu.Lock()
	defer s.alarmMu.Unlock()
	if err := s.putAlarm(ctx, e); err != nil {
		s.alarmsPending[k] = e
		return err
	}
	delete(s.alarmsPending, k)
	return nil
}

// errAlarmUnconfirmed is an alarm whose write returned but which the read
// after it did not find.
var errAlarmUnconfirmed = errors.New("decisions: an alarm was not found in the record after its write")

// putAlarm writes e to RecordAlarms unless its key is there, and returns
// nil only when a fresh strict read finds it: the read that found it there
// already, or a read after the write. A write that returned nil is not yet
// an alarm, as it is not yet a grant (Records): an append over a delete
// marker can return nil on a line no read accepts. s.alarmMu is held.
func (s *Service) putAlarm(ctx context.Context, e alarmEntry) error {
	k := e.key()
	now := s.now()
	wrote := false
	_, err := changeRecord(ctx, s, RecordAlarms, purposeAlarms, func(d *alarmsDoc, found bool) bool {
		if !found {
			*d = alarmsDoc{V: 1}
		}
		if d.find(k) >= 0 {
			wrote = false
			return false
		}
		d.drop(now)
		d.Entries = append(d.Entries, e)
		wrote = true
		return true
	})
	if err != nil || !wrote {
		return err
	}
	var after alarmsDoc
	_, found, err := readRecord(ctx, s, RecordAlarms, purposeAlarms, &after)
	switch {
	case err != nil:
		return err
	case !found || after.find(k) < 0:
		return errAlarmUnconfirmed
	}
	return nil
}

// errAlarms is RunAlarms' error: some alarm was not sent, or the record
// could not be read. Its words say nothing more.
var errAlarms = errors.New("decisions: some alarms of the model were not sent")

// RunAlarms adds the alarms this enclave could not add before, reads
// RecordAlarms, and only it, raises every alarm due and not marked sent
// there, whatever image, baseline or month it came from, with its own kind
// and day, and marks each sent once Raise returned nil. It makes the record
// when no image has yet. The composition calls it when it starts and every
// AlarmRetry.
func (s *Service) RunAlarms(ctx context.Context) error {
	s.alarmMu.Lock()
	failed := false
	for k, e := range s.alarmsPending {
		if err := s.putAlarm(ctx, e); err != nil {
			failed = true
			continue
		}
		delete(s.alarmsPending, k)
	}
	s.alarmMu.Unlock()
	doc, err := changeRecord(ctx, s, RecordAlarms, purposeAlarms, func(d *alarmsDoc, found bool) bool {
		if found {
			return false
		}
		*d = alarmsDoc{V: 1}
		return true
	})
	if err != nil {
		s.logKind(slog.LevelError, "decisions: the record of alarms cannot be read; no alarm is sent", "alarms_unreadable")
		return errAlarms
	}
	for _, e := range doc.Entries {
		if !e.SentAt.IsZero() {
			continue
		}
		if s.cfg.Alarm == nil {
			s.logKind(slog.LevelError, "decisions: an alarm is due and there is no way to send it", string(e.Kind))
			failed = true
			continue
		}
		raiseCtx, cancel := context.WithTimeout(ctx, AlarmTimeout)
		err := s.cfg.Alarm.Raise(raiseCtx, e.Kind, e.Day)
		cancel()
		if err != nil {
			s.logKind(slog.LevelError, "decisions: an alarm could not be sent; it is sent again later", string(e.Kind))
			failed = true
			continue
		}
		k := e.key()
		now := s.now()
		if _, err := changeRecord(ctx, s, RecordAlarms, purposeAlarms, func(d *alarmsDoc, found bool) bool {
			i := d.find(k)
			if !found || i < 0 || !d.Entries[i].SentAt.IsZero() {
				return false
			}
			d.Entries[i].SentAt = now
			d.drop(now)
			return true
		}); err != nil {
			s.logKind(slog.LevelError, "decisions: an alarm was sent and could not be marked sent; it may be sent again", string(e.Kind))
			failed = true
		}
	}
	if failed {
		return errAlarms
	}
	return nil
}
