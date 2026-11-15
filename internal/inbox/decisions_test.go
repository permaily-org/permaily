package inbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/store"
)

// The kept decisions on the store (ADR 0028, "Kept decisions", pull request
// 3): A23, the store's half of A39, A24's deletion and the interleavings of
// A41 that need no sweep (the sweep's are in internal/services/mailboxes).

// kNow is the kept decisions' tests' clock.
var kNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func kID(n int) string { return fmt.Sprintf("%016x%016x", kNow.UnixNano(), n) }

const kHash = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

func kAnswer(topic string, p float64) inbox.KeptAnswer {
	return inbox.KeptAnswer{
		Content: kHash, Var: string(contracts.VarMessage), Op: string(contracts.OpAbout), Topic: topic,
		Model: "gpt-6-luna", Prompt: decide.PromptVersion, P: p, At: kNow, CanaryChecked: "2026-11-15",
	}
}

func kAdd(answers ...inbox.KeptAnswer) *inbox.KeptDecisions {
	return &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{
		decide.PromptVersion + "/" + inbox.ViewOriginal.Key(): {Content: kHash, Subject: kHash},
	}, Answers: answers}
}

// kStore is the memory store under the kept store, where a test says what
// another writer does just before a write of dec# (before), and how many
// such writes were tried.
type kStore struct {
	*store.Memory
	mu     sync.Mutex
	before func(e *store.Entry) error
	puts   int
}

func newKStore() *kStore {
	mem := store.NewMemory()
	mem.Now = func() time.Time { return kNow }
	return &kStore{Memory: mem}
}

func (s *kStore) PutEntry(ctx context.Context, e *store.Entry) error {
	if strings.HasPrefix(e.Key, inbox.KeyDecision) {
		s.mu.Lock()
		s.puts++
		f := s.before
		s.mu.Unlock()
		if f != nil {
			if err := f(e); err != nil {
				return err
			}
		}
	}
	return s.Memory.PutEntry(ctx, e)
}

func (s *kStore) setBefore(f func(e *store.Entry) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.before, s.puts = f, 0
}

func (s *kStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

func topicsOf(d *inbox.KeptDecisions) map[string]float64 {
	out := map[string]float64{}
	if d == nil {
		return out
	}
	for _, a := range d.Answers {
		out[a.Topic] = a.P
	}
	return out
}

func mustKeep(t *testing.T, ks inbox.KeptStore, b inbox.Box, id string, add *inbox.KeptDecisions) *inbox.KeptDecisions {
	t.Helper()
	d, err := ks.Keep(context.Background(), b, id, kNow.Add(time.Hour), add)
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	return d
}

// A23: an entry is sealed under the mailbox's key and bound to the message:
// another mailbox's key, another message's identifier, another mailbox's
// partition and a changed byte do not open it, and read as none; what a
// later Keep writes over one that does not open is what is read.
func TestA23KeptIsSealedAndBoundToTheMessage(t *testing.T) {
	ctx := context.Background()
	st := newKStore()
	ks := inbox.NewKeptStore(st)
	b := qBox(t)
	id := kID(1)
	mustKeep(t, ks, b, id, kAdd(kAnswer("invoices", 0.91)))
	got, err := ks.Kept(ctx, b, id)
	if err != nil || topicsOf(got)["invoices"] != 0.91 || got.Views[decide.PromptVersion+"/original/1"].Content != kHash {
		t.Fatalf("kept %+v, %v", got, err)
	}
	e, err := st.GetEntry(ctx, inbox.Partition(b.MailboxID), inbox.KeyDecision+id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(e.Value, []byte("invoices")) || bytes.Contains(e.Value, []byte(kHash)) {
		t.Fatal("the entry holds a topic or a hash in the clear")
	}
	other := newBox(t, b.MailboxID)
	if d, err := ks.Kept(ctx, other, id); d != nil || err != nil {
		t.Fatalf("another key opened it: %+v, %v", d, err)
	}
	moved := *e
	moved.Key = inbox.KeyDecision + kID(2)
	st.SetEntry(moved)
	if d, err := ks.Kept(ctx, b, kID(2)); d != nil || err != nil {
		t.Fatalf("another message's identifier opened it: %+v, %v", d, err)
	}
	b2 := inbox.Box{MailboxID: "mbx_rrrrrrrrrrrrrrrrrrrrrrrrrr", Key: b.Key}
	moved = *e
	moved.Partition = inbox.Partition(b2.MailboxID)
	st.SetEntry(moved)
	if d, err := ks.Kept(ctx, b2, id); d != nil || err != nil {
		t.Fatalf("another mailbox opened it: %+v, %v", d, err)
	}
	for i := range e.Value {
		bad := *e
		bad.Value = bytes.Clone(e.Value)
		bad.Value[i] ^= 1
		st.SetEntry(bad)
		if d, err := ks.Kept(ctx, b, id); d != nil || err != nil {
			t.Fatalf("byte %d changed and it opened: %+v, %v", i, d, err)
		}
	}
	// Not KeptDecisions, though sealed right.
	for _, plain := range []string{"not json", `{"v":2,"answers":[]}`} {
		sealed, err := envelope.SealText(b.Key, []byte(plain), envelope.AAD(inbox.PurposeDecision, b.MailboxID, id))
		if err != nil {
			t.Fatal(err)
		}
		bad := *e
		bad.Value = sealed
		st.SetEntry(bad)
		if d, err := ks.Kept(ctx, b, id); d != nil || err != nil {
			t.Fatalf("%q read as kept: %+v, %v", plain, d, err)
		}
	}
	// What is asked replaces what does not open.
	d := mustKeep(t, ks, b, id, kAdd(kAnswer("travel bookings", 0.04)))
	if got := topicsOf(d); len(got) != 1 || got["travel bookings"] != 0.04 {
		t.Fatalf("written over a damaged entry: %v", got)
	}
	if got, _ := ks.Kept(ctx, b, id); len(topicsOf(got)) != 1 {
		t.Fatalf("read back %v", topicsOf(got))
	}
	// An identifier that is no message's has nothing kept, and keeps nothing.
	if d, err := ks.Kept(ctx, b, "../"+id); d != nil || err != nil {
		t.Fatal("a malformed identifier read something")
	}
	if _, err := ks.Keep(ctx, b, "nope", kNow.Add(time.Hour), kAdd(kAnswer("x", 1))); !errors.Is(err, inbox.ErrGone) {
		t.Fatalf("a malformed identifier kept: %v", err)
	}
}

// A23: the entry expires with the message, and is then none.
func TestA23TheEntryExpiresWithTheMessage(t *testing.T) {
	ctx := context.Background()
	st := newKStore()
	clock := kNow
	st.Now = func() time.Time { return clock }
	ks := inbox.NewKeptStore(st)
	b := qBox(t)
	expires := kNow.Add(90 * 24 * time.Hour)
	if _, err := ks.Keep(ctx, b, kID(1), expires, kAdd(kAnswer("invoices", 0.9))); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetEntry(ctx, inbox.Partition(b.MailboxID), inbox.KeyDecision+kID(1))
	if err != nil || !e.ExpiresAt.Equal(expires) {
		t.Fatalf("expires %v, %v", e, err)
	}
	clock = expires
	if d, err := ks.Kept(ctx, b, kID(1)); d != nil || err != nil {
		t.Fatalf("an expired entry read: %+v, %v", d, err)
	}
}

// A23: a create that another writer made first (store.ErrExists) and an
// update another changed (store.ErrConflict) are read again and added to,
// at most KeepTries times; past them nothing is kept, and the error says
// so.
func TestA23AConflictIsReadAgainAtMostKeepTriesTimes(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			st := newKStore()
			ks, other := inbox.NewKeptStore(st), inbox.NewKeptStore(st.Memory)
			if existing {
				mustKeep(t, ks, b, kID(1), kAdd(kAnswer("first", 0.1)))
			}
			once := sync.Once{}
			st.setBefore(func(*store.Entry) error {
				once.Do(func() { mustKeep(t, other, b, kID(1), kAdd(kAnswer("theirs", 0.5))) })
				return nil
			})
			d := mustKeep(t, ks, b, kID(1), kAdd(kAnswer("mine", 0.9)))
			got := topicsOf(d)
			if got["theirs"] != 0.5 || got["mine"] != 0.9 || (existing && got["first"] != 0.1) {
				t.Fatalf("returned %v", got)
			}
			if st.putCount() != 2 {
				t.Fatalf("%d writes, want 2", st.putCount())
			}
			read, _ := ks.Kept(ctx, b, kID(1))
			if len(topicsOf(read)) != len(got) {
				t.Fatalf("read back %v, returned %v", topicsOf(read), got)
			}
		})
	}
	t.Run("always changed", func(t *testing.T) {
		st := newKStore()
		ks, other := inbox.NewKeptStore(st), inbox.NewKeptStore(st.Memory)
		n := 0
		st.setBefore(func(*store.Entry) error {
			n++
			mustKeep(t, other, b, kID(1), kAdd(kAnswer("theirs "+strconv.Itoa(n), 0.5)))
			return nil
		})
		d, err := ks.Keep(ctx, b, kID(1), kNow.Add(time.Hour), kAdd(kAnswer("mine", 0.9)))
		if !errors.Is(err, store.ErrConflict) || d != nil {
			t.Fatalf("Keep = %+v, %v", d, err)
		}
		if st.putCount() != inbox.KeepTries {
			t.Fatalf("%d writes, want %d", st.putCount(), inbox.KeepTries)
		}
		read, _ := ks.Kept(ctx, b, kID(1))
		if _, ok := topicsOf(read)["mine"]; ok {
			t.Fatal("an answer kept after KeepTries")
		}
	})
	t.Run("the store fails", func(t *testing.T) {
		st := newKStore()
		ks := inbox.NewKeptStore(st)
		st.setBefore(func(*store.Entry) error { return errQ })
		if d, err := ks.Keep(ctx, b, kID(1), kNow.Add(time.Hour), kAdd(kAnswer("mine", 0.9))); !errors.Is(err, errQ) || d != nil {
			t.Fatalf("Keep = %+v, %v", d, err)
		}
		st.setBefore(nil)
		st.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
			if step == store.StepRead && strings.HasPrefix(key, inbox.KeyDecision) {
				return errQ
			}
			return nil
		}
		if _, err := ks.Keep(ctx, b, kID(1), kNow.Add(time.Hour), kAdd(kAnswer("mine", 0.9))); !errors.Is(err, errQ) {
			t.Fatalf("a failed read kept: %v", err)
		}
		if _, err := ks.Kept(ctx, b, kID(1)); !errors.Is(err, errQ) {
			t.Fatalf("a failed read is none: %v", err)
		}
	})
}

// A23, A13's half here: two writers at once, each with an answer for one
// key, both end with one answer for it, the one kept first, and each
// returns the entry as written.
func TestA23TwoWritersKeepTheFirstAnswerForAKey(t *testing.T) {
	b := qBox(t)
	st := newKStore()
	a, other := inbox.NewKeptStore(st), inbox.NewKeptStore(st.Memory)
	var theirs *inbox.KeptDecisions
	once := sync.Once{}
	st.setBefore(func(*store.Entry) error {
		once.Do(func() { theirs = mustKeep(t, other, b, kID(1), kAdd(kAnswer("invoices", 0.1))) })
		return nil
	})
	mine := mustKeep(t, a, b, kID(1), kAdd(kAnswer("invoices", 0.9)))
	if topicsOf(mine)["invoices"] != 0.1 || topicsOf(theirs)["invoices"] != 0.1 || len(mine.Answers) != 1 {
		t.Fatalf("mine %v, theirs %v", topicsOf(mine), topicsOf(theirs))
	}
	// Later writers too, and another content, model or prompt is another
	// key.
	again := kAnswer("invoices", 0.7)
	otherContent, otherModel, otherPrompt := again, again, again
	otherContent.Content = strings.Repeat("d", 64)
	otherModel.Model = "gpt-7"
	otherPrompt.Prompt = "p3"
	d := mustKeep(t, a, b, kID(1), kAdd(again, otherContent, otherModel, otherPrompt))
	if len(d.Answers) != 4 || d.Answers[0].P != 0.1 {
		t.Fatalf("answers %+v", d.Answers)
	}
	// Nothing new to add writes nothing.
	st.setBefore(nil)
	if d := mustKeep(t, a, b, kID(1), kAdd(again)); len(d.Answers) != 4 || st.putCount() != 0 {
		t.Fatalf("%d answers after %d writes", len(d.Answers), st.putCount())
	}
	if d := mustKeep(t, a, b, kID(1), nil); len(d.Answers) != 4 {
		t.Fatalf("%d answers", len(d.Answers))
	}
}

// fill keeps n answers for a message, by topics "t0"… .
func fill(t *testing.T, ks inbox.KeptStore, b inbox.Box, id string, n int) {
	t.Helper()
	var add []inbox.KeptAnswer
	for i := range n {
		add = append(add, kAnswer("t"+strconv.Itoa(i), 0.5))
	}
	if d := mustKeep(t, ks, b, id, kAdd(add...)); len(d.Answers) != min(n, inbox.KeptMaxAnswers) {
		t.Fatalf("filled %d of %d", len(d.Answers), n)
	}
}

// A23 (and the store's half of A39): at most KeptMaxAnswers answers; at 255, 256 and 257 of room
// needed, the ones past it are not added and existing ones stay; two writers
// racing two atoms for the last slot keep one; two answers for one atom when
// the entry is full keep neither.
func TestA23AtMostKeptMaxAnswers(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	t.Run("one write past the bound", func(t *testing.T) {
		ks := inbox.NewKeptStore(newKStore())
		fill(t, ks, b, kID(1), 300)
		d, _ := ks.Kept(ctx, b, kID(1))
		if len(d.Answers) != inbox.KeptMaxAnswers || topicsOf(d)["t255"] != 0.5 {
			t.Fatalf("%d answers", len(d.Answers))
		}
		if _, ok := topicsOf(d)["t256"]; ok {
			t.Fatal("the 257th answer was kept")
		}
	})
	for _, have := range []int{255, 256, 257} {
		t.Run("have "+strconv.Itoa(have), func(t *testing.T) {
			ks := inbox.NewKeptStore(newKStore())
			fill(t, ks, b, kID(1), have)
			d := mustKeep(t, ks, b, kID(1), kAdd(kAnswer("new a", 0.9), kAnswer("new b", 0.9)))
			got := topicsOf(d)
			_, a := got["new a"]
			_, bb := got["new b"]
			if len(d.Answers) != inbox.KeptMaxAnswers || a != (have == 255) || bb || got["t0"] != 0.5 {
				t.Fatalf("%d answers, new a %v, new b %v", len(d.Answers), a, bb)
			}
		})
	}
	t.Run("two writers for the last slot", func(t *testing.T) {
		st := newKStore()
		ks, other := inbox.NewKeptStore(st), inbox.NewKeptStore(st.Memory)
		fill(t, ks, b, kID(1), 255)
		var theirs *inbox.KeptDecisions
		once := sync.Once{}
		st.setBefore(func(*store.Entry) error {
			once.Do(func() { theirs = mustKeep(t, other, b, kID(1), kAdd(kAnswer("theirs", 0.2))) })
			return nil
		})
		mine := mustKeep(t, ks, b, kID(1), kAdd(kAnswer("mine", 0.9)))
		if _, ok := topicsOf(mine)["mine"]; ok || topicsOf(mine)["theirs"] != 0.2 || topicsOf(theirs)["theirs"] != 0.2 {
			t.Fatalf("mine %d, theirs %d", len(mine.Answers), len(theirs.Answers))
		}
	})
	t.Run("two answers for one atom when full", func(t *testing.T) {
		ks := inbox.NewKeptStore(newKStore())
		fill(t, ks, b, kID(1), 256)
		for _, p := range []float64{0.9, 0.1} {
			if d := mustKeep(t, ks, b, kID(1), kAdd(kAnswer("late", p))); func() bool { _, ok := topicsOf(d)["late"]; return ok }() {
				t.Fatal("an answer kept past the bound")
			}
		}
	})
}

// A23: a full entry written through the store, 256 answers whose topics are
// 100 characters of the longest JSON forms (four bytes of UTF-8, six as an
// escape) and sixteen views, is under 350,000 bytes sealed; a seventeenth
// view is not added.
func TestA23AFullEntryWrittenThroughTheStore(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, r := range []rune{'\U0001D11E', '<', ' '} {
		st := newKStore()
		ks := inbox.NewKeptStore(st)
		add := &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}}
		for i := range 17 {
			add.Views[decide.PromptVersion+"/view-"+strconv.Itoa(i)+"/"+strconv.Itoa(1<<30)] = inbox.KeptView{Content: strings.Repeat("f", 64), Subject: strings.Repeat("f", 64)}
		}
		for i := range inbox.KeptMaxAnswers {
			a := kAnswer(strings.Repeat(string(r), 99)+string(rune('a'+i%26)), 0.12345678901234568)
			a.Content, a.Refused, a.CanaryChange = fmt.Sprintf("%064x", i), true, "2026-11-15"
			a.Var = string(contracts.VarMessage)
			a.Op = string(contracts.OpMentions)
			add.Answers = append(add.Answers, a)
		}
		d := mustKeep(t, ks, b, kID(1), add)
		if len(d.Answers) != inbox.KeptMaxAnswers || len(d.Views) != 16 {
			t.Fatalf("%q: %d answers, %d views", r, len(d.Answers), len(d.Views))
		}
		e, err := st.GetEntry(ctx, inbox.Partition(b.MailboxID), inbox.KeyDecision+kID(1))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%q: %d bytes sealed", r, len(e.Value))
		if len(e.Value) > 350_000 {
			t.Fatalf("%q: %d bytes sealed", r, len(e.Value))
		}
		if read, _ := ks.Kept(ctx, b, kID(1)); read == nil || len(read.Answers) != inbox.KeptMaxAnswers {
			t.Fatal("a full entry does not read back")
		}
	}
}

// seedOne stores one message in b as delivery does, and its kept decisions.
func seedOne(t *testing.T, st *kStore, b inbox.Box, n int, keep bool) string {
	t.Helper()
	id := kID(n)
	m := inboxtest.Message{ID: id, ReceivedAt: kNow, RetentionDays: 30, Raw: []byte("Subject: hello\r\n\r\nhi"), Meta: inbox.StoredMeta{Subject: "hello"}}
	if err := inboxtest.Seed(context.Background(), st.Memory, st.Memory, b, m); err != nil {
		t.Fatal(err)
	}
	if keep {
		mustKeep(t, inbox.NewKeptStore(st.Memory), b, id, kAdd(kAnswer("invoices", 0.9)))
	}
	return id
}

// A24: Reader.Delete writes the mark first, expiring with the message, then
// deletes the message and its txt#, confirm# and dec# entries; a message
// whose item is not there gets no mark; a deletion tried again writes the
// mark again.
func TestA24DeleteWritesTheMarkFirst(t *testing.T) {
	ctx := context.Background()
	st := newKStore()
	b := qBox(t)
	id := seedOne(t, st, b, 1, true)
	var steps []string
	var mu sync.Mutex
	st.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step != store.StepRead {
			mu.Lock()
			steps = append(steps, string(step)+" "+partition+" "+key)
			mu.Unlock()
		}
		return nil
	}
	clock := kNow.Add(time.Minute)
	r := inbox.NewReaderAt(st, func() time.Time { return clock })
	if n, err := r.Delete(ctx, b, []string{id}); n != 1 || err != nil {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	part := inbox.Partition(b.MailboxID)
	want := []string{
		"entry " + inbox.GonePartition + " " + inbox.GoneKey(b.MailboxID, id),
		"delete-raw " + part + " msg#" + id,
		"delete-item " + part + " msg#" + id,
		"delete-entry " + part + " " + inbox.KeySearch + id,
		"delete-entry " + part + " " + inbox.KeyConfirm + id,
		"delete-entry " + part + " " + inbox.KeyDecision + id,
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}
	mark, err := st.GetEntry(ctx, inbox.GonePartition, inbox.GoneKey(b.MailboxID, id))
	if err != nil || !mark.ExpiresAt.Equal(kNow.AddDate(0, 0, 30)) || !mark.UpdatedAt.Equal(clock) {
		t.Fatalf("mark %+v, %v", mark, err)
	}
	if _, err := st.GetEntry(ctx, part, inbox.KeyDecision+id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dec# stays: %v", err)
	}
	// Gone already: no mark is written for it, nor for an identifier that
	// was never a message.
	st.Interrupt = nil
	steps = nil
	other := kID(99)
	if n, err := r.Delete(ctx, b, []string{id, other}); n != 0 || err != nil {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	if _, err := st.GetEntry(ctx, inbox.GonePartition, inbox.GoneKey(b.MailboxID, other)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a mark for no message: %v", err)
	}
	// Tried again while the message is still there (the first try failed
	// after its mark): the mark is written again, its grace from now.
	id2 := seedOne(t, st, b, 2, true)
	fail := true
	st.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepDeleteRaw && fail {
			return errQ
		}
		return nil
	}
	if _, err := r.Delete(ctx, b, []string{id2}); !errors.Is(err, errQ) {
		t.Fatalf("Delete = %v", err)
	}
	first, _ := st.GetEntry(ctx, inbox.GonePartition, inbox.GoneKey(b.MailboxID, id2))
	fail, clock = false, clock.Add(time.Hour)
	if n, err := r.Delete(ctx, b, []string{id2}); n != 1 || err != nil {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	again, _ := st.GetEntry(ctx, inbox.GonePartition, inbox.GoneKey(b.MailboxID, id2))
	if again.Version <= first.Version || !again.UpdatedAt.Equal(clock) {
		t.Fatalf("the mark was not written again: %+v then %+v", first, again)
	}
}

// A24: a deletion fails as its mark's write or read fails, before anything
// of the message is deleted.
func TestA24NoMarkNoDeletion(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, failing := range []string{"item", "mark", "reread"} {
		st := newKStore()
		id := seedOne(t, st, b, 1, true)
		r := inbox.NewReader(st)
		if failing == "reread" {
			// A first try wrote its mark and failed: the mark is there.
			st.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
				if step == store.StepDeleteRaw {
					return errStopped
				}
				return nil
			}
			if _, err := r.Delete(ctx, b, []string{id}); !errors.Is(err, errStopped) {
				t.Fatal(err)
			}
		}
		st.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
			switch {
			case failing == "item" && step == store.StepRead && strings.HasPrefix(key, "msg#"),
				failing == "mark" && step == store.StepEntry && partition == inbox.GonePartition,
				failing == "reread" && step == store.StepRead && partition == inbox.GonePartition:
				return errQ
			}
			return nil
		}
		if _, err := r.Delete(ctx, b, []string{id}); !errors.Is(err, errQ) {
			t.Fatalf("%s: Delete = %v", failing, err)
		}
		st.Interrupt = nil
		if _, err := st.Item(ctx, b.MailboxID, id); err != nil {
			t.Fatalf("%s: the message was deleted without its mark: %v", failing, err)
		}
	}
}

// A41, without the sweep: A reads a message and waits on the model; B
// deletes it (mark, item, txt#, confirm#, dec#) and answers; A's Keep
// writes, finds the mark, deletes what it wrote and returns ErrGone, so A
// uses no new answer. The same while A updates an entry that existed, and
// when A had nothing new to write.
func TestA41KeepAfterDelete(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, existed := range []bool{false, true} {
		t.Run(fmt.Sprintf("existed=%v", existed), func(t *testing.T) {
			st := newKStore()
			id := seedOne(t, st, b, 1, existed)
			ks := inbox.NewKeptStore(st)
			r := inbox.NewReader(st.Memory)
			// A has read the entry (Keep's first read) when B deletes.
			once := sync.Once{}
			st.setBefore(func(*store.Entry) error {
				once.Do(func() {
					if n, err := r.Delete(ctx, b, []string{id}); n != 1 || err != nil {
						t.Errorf("B: %d, %v", n, err)
					}
				})
				return nil
			})
			d, err := ks.Keep(ctx, b, id, kNow.Add(time.Hour), kAdd(kAnswer("new", 0.9)))
			if !errors.Is(err, inbox.ErrGone) || d != nil {
				t.Fatalf("Keep = %+v, %v", d, err)
			}
			if left, err := st.GetEntry(ctx, inbox.Partition(b.MailboxID), inbox.KeyDecision+id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("a decision kept for a deleted message: %+v, %v", left, err)
			}
		})
	}
	t.Run("nothing new", func(t *testing.T) {
		st := newKStore()
		id := seedOne(t, st, b, 1, true)
		ks := inbox.NewKeptStore(st)
		// B's deletion stopped after its mark: the message's dec# is there.
		st.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
			if step == store.StepDeleteRaw {
				return errStopped
			}
			return nil
		}
		if _, err := inbox.NewReader(st).Delete(ctx, b, []string{id}); !errors.Is(err, errStopped) {
			t.Fatal(err)
		}
		st.Interrupt = nil
		if d, err := ks.Keep(ctx, b, id, kNow.Add(time.Hour), kAdd(kAnswer("invoices", 0.9))); !errors.Is(err, inbox.ErrGone) || d != nil {
			t.Fatalf("Keep = %+v, %v", d, err)
		}
	})
	t.Run("the mark cannot be read", func(t *testing.T) {
		st := newKStore()
		id := seedOne(t, st, b, 1, false)
		ks := inbox.NewKeptStore(st)
		st.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
			if step == store.StepRead && partition == inbox.GonePartition {
				return errQ
			}
			return nil
		}
		if d, err := ks.Keep(ctx, b, id, kNow.Add(time.Hour), kAdd(kAnswer("new", 0.9))); !errors.Is(err, errQ) || d != nil {
			t.Fatalf("Keep = %+v, %v", d, err)
		}
	})
	t.Run("the undoing fails", func(t *testing.T) {
		st := newKStore()
		id := seedOne(t, st, b, 1, false)
		ks := inbox.NewKeptStore(st)
		if _, err := inbox.NewReader(st.Memory).Delete(ctx, b, []string{id}); err != nil {
			t.Fatal(err)
		}
		st.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
			if step == store.StepDeleteEntry {
				return errQ
			}
			return nil
		}
		if _, err := ks.Keep(ctx, b, id, kNow.Add(time.Hour), kAdd(kAnswer("new", 0.9))); !errors.Is(err, errQ) {
			t.Fatalf("Keep = %v", err)
		}
	})
}

var errStopped = errors.New("stopped by the test")

// A23: what cannot be written is not kept: an answer that is not a number
// JSON can hold, a box whose key seals nothing.
func TestA23WhatCannotBeSealedIsNotKept(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	ks := inbox.NewKeptStore(newKStore())
	nan := kAnswer("invoices", 0)
	nan.P = math.NaN()
	if d, err := ks.Keep(ctx, b, kID(1), kNow.Add(time.Hour), kAdd(nan)); err == nil || d != nil {
		t.Fatalf("Keep = %+v, %v", d, err)
	}
	if d, err := ks.Keep(ctx, inbox.Box{MailboxID: b.MailboxID, Key: []byte("short")}, kID(1), kNow.Add(time.Hour), kAdd(kAnswer("x", 1))); err == nil || d != nil {
		t.Fatalf("Keep = %+v, %v", d, err)
	}
}

// A24: the mark of a deletion tried again, when the sweep deleted it in
// between, is written anew; and a mark that cannot be written again fails
// the deletion before anything of the message is deleted.
func TestA24TheMarkWrittenAgain(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, c := range []string{"deleted in between", "write fails"} {
		st := newKStore()
		id := seedOne(t, st, b, 1, false)
		r := inbox.NewReader(st)
		st.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
			if step == store.StepDeleteRaw {
				return errStopped
			}
			return nil
		}
		if _, err := r.Delete(ctx, b, []string{id}); !errors.Is(err, errStopped) {
			t.Fatal(err)
		}
		mark := inbox.GoneKey(b.MailboxID, id)
		writes := 0
		st.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
			switch {
			case c == "deleted in between" && step == store.StepRead && partition == inbox.GonePartition:
				st.Interrupt = nil
				if err := st.DeleteEntry(ctx, inbox.GonePartition, mark); err != nil {
					t.Error(err)
				}
			case c == "write fails" && step == store.StepEntry && partition == inbox.GonePartition:
				if writes++; writes == 2 {
					return errQ
				}
			}
			return nil
		}
		n, err := r.Delete(ctx, b, []string{id})
		st.Interrupt = nil
		switch c {
		case "deleted in between":
			if n != 1 || err != nil {
				t.Fatalf("%s: %d, %v", c, n, err)
			}
			if _, err := st.GetEntry(ctx, inbox.GonePartition, mark); err != nil {
				t.Fatalf("%s: no mark: %v", c, err)
			}
		case "write fails":
			if !errors.Is(err, errQ) {
				t.Fatalf("%s: %v", c, err)
			}
			if _, err := st.Item(ctx, b.MailboxID, id); err != nil {
				t.Fatalf("%s: the message went: %v", c, err)
			}
		}
	}
}
