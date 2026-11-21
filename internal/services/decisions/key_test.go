package decisions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// A19. The key.
func TestA19TheKey(t *testing.T) {
	ctx := context.Background()
	sum := sha256.Sum256([]byte(testKey))
	wantFP := hex.EncodeToString(sum[:])[:16]

	t.Run("New reads no storage; the credential reads on its first use", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		if len(e.records.gets) != 0 {
			t.Fatalf("New read %v", e.records.gets)
		}
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("a key with none set")
		}
		if e.records.gets[RecordKey] != 1 {
			t.Fatalf("reads %v", e.records.gets)
		}
	})
	t.Run("set, replaced and removed; its fingerprint; never answered", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		got, err := e.SetKey(ctx, keySet())
		if err != nil || !got.Set || got.Fingerprint != wantFP || !got.SetAt.Equal(t0) {
			t.Fatalf("%+v, %v", got, err)
		}
		if k, ok := e.model.key(ctx); !ok || k != testKey {
			t.Fatal("the credential does not give the key set")
		}
		got, _ = e.Key(ctx)
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), testKey) || got.Fingerprint != wantFP {
			t.Fatalf("%s", raw)
		}
		if _, err := e.SetKey(ctx, keySetOf("sk-replaced")); err != nil {
			t.Fatal(err)
		}
		if k, _ := e.model.key(ctx); k != "sk-replaced" {
			t.Fatal("not replaced")
		}
		if err := e.RemoveKey(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("a key after its removal")
		}
		if got, _ := e.Key(ctx); got.Set || got.Fingerprint != "" {
			t.Fatalf("%+v", got)
		}
		if err := e.RemoveKey(ctx); err != nil {
			t.Fatal("removing none", err)
		}
		// The stored bytes hold no byte sequence of the key.
		for _, e := range w.records.AllEntries() {
			if bytes.Contains(e.Value, []byte("sk-")) {
				t.Fatal("an entry holds the key")
			}
		}
		for _, name := range w.records.RecordNames() {
			r, _ := w.records.GetRecord(ctx, name)
			for _, k := range []string{testKey, "sk-replaced", base64.StdEncoding.EncodeToString([]byte(testKey))} {
				if bytes.Contains(r.Value, []byte(k)) || bytes.Contains(r.Value, []byte(k[:8])) {
					t.Fatalf("%s holds the key", name)
				}
			}
		}
	})
	t.Run("an invalid key is refused, in words that do not repeat it", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		for _, k := range []string{"", "sk with space", "sk-\n", strings.Repeat("k", maxKeyLength+1), "sk-é"} {
			if _, err := e.SetKey(ctx, keySetOf(k)); !errors.Is(err, ErrInvalidKey) || (k != "" && strings.Contains(err.Error(), k)) {
				t.Fatalf("%q: %v", k, err)
			}
		}
	})
	t.Run("set through one enclave is used by the other within keyFresh; removed too", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		if _, ok := b.model.key(ctx); ok {
			t.Fatal("a key")
		}
		_, _ = a.SetKey(ctx, keySet())
		if _, ok := b.model.key(ctx); ok {
			t.Fatal("read again before keyFresh")
		}
		w.advance(keyFresh)
		if k, ok := b.model.key(ctx); !ok || k != testKey {
			t.Fatal("not used within keyFresh")
		}
		_, _ = a.SetKey(ctx, keySetOf("sk-new"))
		w.advance(keyFresh)
		if k, _ := b.model.key(ctx); k != "sk-new" {
			t.Fatal("the old key sent after keyFresh")
		}
		_ = a.RemoveKey(ctx)
		w.advance(keyFresh)
		if _, ok := b.model.key(ctx); ok {
			t.Fatal("a removed key is still used")
		}
	})
	noKey := func(t *testing.T, w *world, kind string) {
		t.Helper()
		e := w.enclave()
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("a key")
		}
		if got, err := e.Key(ctx); err != nil || got.Set {
			t.Fatalf("%+v, %v", got, err)
		}
		if !strings.Contains(w.logs.String(), "kind="+kind) {
			t.Fatalf("not logged as %s: %s", kind, w.logs.String())
		}
	}
	t.Run("an earlier copy of the record is refused", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		_, _ = e.SetKey(ctx, keySet())
		earlier, _ := w.records.GetRecord(ctx, w.versions(RecordKey)[0])
		_ = e.RemoveKey(ctx)
		current := w.versions(RecordKey)[0]
		w.records.SetRecord(store.Record{Name: current, Value: earlier.Value})
		noKey(t, w, "key_unreadable")
	})
	t.Run("a lost record is no key", func(t *testing.T) {
		w := newWorld(t)
		_, _ = w.enclave().SetKey(ctx, keySet())
		for _, n := range w.versions(RecordKey) {
			_ = w.records.DeleteRecord(ctx, n)
		}
		noKey(t, w, "key_unreadable")
	})
	t.Run("a forked record is no key", func(t *testing.T) {
		w := newWorld(t)
		_, _ = w.enclave().SetKey(ctx, keySet())
		w.commits.Fork(RecordKey, 1, sha256.Sum256([]byte("another")))
		noKey(t, w, "key_forked")
	})
	t.Run("a record that does not open is no key", func(t *testing.T) {
		w := newWorld(t)
		_, _ = w.enclave().SetKey(ctx, keySet())
		e := w.enclave(withKeys(testKeys{root: "another"}))
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("a key")
		}
		if !strings.Contains(w.logs.String(), "kind=key_unreadable") {
			t.Fatal(w.logs.String())
		}
	})
	t.Run("a read that fails is no key, logged, and read again next time", func(t *testing.T) {
		w := newWorld(t)
		_, _ = w.enclave().SetKey(ctx, keySet())
		e := w.enclave()
		e.records.onGet(func(string, int) (bool, *store.Record, store.Mark, error) {
			return true, nil, store.Mark{}, errors.New("storage")
		})
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("a key")
		}
		if _, err := e.Key(ctx); err == nil {
			t.Fatal("no error for the operator")
		}
		if !strings.Contains(w.logs.String(), "kind=key_read_failed") {
			t.Fatal(w.logs.String())
		}
		e.records.onGet(nil)
		if _, ok := e.model.key(ctx); !ok {
			t.Fatal("a failed read was kept")
		}
	})
	t.Run("keys that cannot be derived store nothing", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave(withKeys(failingKeys{}))
		if _, err := e.SetKey(ctx, keySet()); err == nil {
			t.Fatal("stored")
		}
	})
	t.Run("no exported method or field of the service returns the key", func(t *testing.T) {
		typ := reflect.TypeFor[*Service]()
		var methods []string
		for m := range typ.Methods() {
			methods = append(methods, m.Name)
		}
		// WordsCalls answers counts of fixed kinds (ADR 0040, /status).
		want := []string{"Ask", "AskSentences", "Gate", "Key", "RemoveKey", "RunAlarms", "RunCanary", "SetKey", "Status", "WordsCalls", "Write"}
		if !slices.Equal(methods, want) {
			t.Fatalf("exported methods %v: each must be checked not to return the key", methods)
		}
		for f := range typ.Elem().Fields() {
			if f.IsExported() {
				t.Fatalf("an exported field %s", f.Name)
			}
		}
		w := newWorld(t)
		e := w.keyed(withWordsModel(&wordsModel{}))
		var outs []any
		set, _ := e.SetKey(ctx, keySet())
		k, _ := e.Key(ctx)
		asked := e.ask(newQuestion("m1", "Hi", "text", subj("x")))
		sentences := e.askSentences([]string{"Hi."}, mentions("x"))
		written, err := e.Write(ctx, "acc_a", "hide mail from bank.example")
		outs = append(outs, set, k, asked, sentences, written, err, e.Gate(ctx, "acc_a"), e.Status(ctx), e.RunCanary(ctx), e.RunAlarms(ctx), e.RemoveKey(ctx))
		raw, _ := json.Marshal(outs)
		if strings.Contains(string(raw), testKey) || strings.Contains(w.logs.String(), testKey) || strings.Contains(w.logs.String(), wantFP) {
			t.Fatal("the key was answered or logged")
		}
		if s := e.Status(ctx); strings.Contains(mustJSON(s), wantFP) {
			t.Fatal("/status holds the fingerprint")
		}
	})
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

var _ = contracts.ModelKey{}

// A19. A change of the key that returns after a later one never brings
// its key back: the credential and the next Ask use the record as it is
// (the review of pull request 2, finding 1).
func TestA19AChangeThatReturnsLateNeverBringsBackAnOlderKey(t *testing.T) {
	ctx := context.Background()
	t.Run("a set that returns after a removal", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			stored, err := inner(c)
			if err != nil {
				return stored, err
			}
			// The set landed; a removal lands and returns before it does.
			if err := e.RemoveKey(ctx); err != nil {
				t.Fatal(err)
			}
			return stored, nil
		})
		if _, err := e.SetKey(ctx, keySetOf("sk-set-before-the-removal")); err != nil {
			t.Fatal(err)
		}
		if got, err := e.Key(ctx); err != nil || got.Set {
			t.Fatalf("the record is not removed: %+v, %v", got, err)
		}
		if _, ok := e.model.key(ctx); ok {
			t.Fatal("the credential gives a removed key")
		}
		if out := e.ask(newQuestion("m1", "Hi", "", subj("x"))); out.Outcome != inbox.AskOff || e.model.count() != 0 {
			t.Fatalf("asked with a removed key: %+v", out)
		}
	})
	t.Run("a set that returns after a newer set", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			stored, err := inner(c)
			if err != nil {
				return stored, err
			}
			if _, err := e.SetKey(ctx, keySetOf("sk-the-newer-key")); err != nil {
				t.Fatal(err)
			}
			return stored, nil
		})
		if _, err := e.SetKey(ctx, keySetOf("sk-the-older-key")); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.Key(ctx); got.Fingerprint != fingerprint("sk-the-newer-key") {
			t.Fatalf("the record: %+v", got)
		}
		if k, ok := e.model.key(ctx); !ok || k != "sk-the-newer-key" {
			t.Fatal("the credential gives the older key")
		}
		e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if e.model.count() != 1 || e.model.keys[0] != "sk-the-newer-key" {
			t.Fatal("asked with the older key")
		}
	})
}
