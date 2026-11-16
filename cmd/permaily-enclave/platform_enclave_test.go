//go:build !dev

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/store"
)

// /status says that one of the model's records went back, and never which
// one or at which version: the versions of a month's spend say how much
// was spent (ADR 0028, "What /status and permaily-verify show").
func TestAnchorStatusSaysTheModelsRecordsWentBackWithoutTheirVersions(t *testing.T) {
	ctx := context.Background()
	mem, commits := store.NewMemory(), store.NewMemoryCommits()
	tls := &store.Anchored{Records: mem, Commits: commits}
	model := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true}
	if err := model.CreateNew(ctx, &store.Record{Name: "model-spend-2026-11", Value: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	names, err := mem.ListRecords(ctx, "model-spend-2026-11", 10)
	if err != nil || len(names) == 0 {
		t.Fatalf("%v %v", names, err)
	}
	for _, name := range names {
		if err := mem.DeleteRecord(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := model.GetRecordAt(ctx, "model-spend-2026-11"); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a record whose value was deleted: %v", err)
	}
	st := anchorStatus(tls, nil, nil, model, "anchors", 3650)
	if len(st.Records) != 0 || len(st.Regressions) != 1 {
		t.Fatalf("%+v", st)
	}
	if r := st.Regressions[0]; r.Name != modelRecord || !r.Lost || r.Had != 0 || r.Found != 0 || r.At.IsZero() {
		t.Fatalf("%+v", r)
	}
	if st := anchorStatus(tls, nil, nil, nil, "anchors", 3650); len(st.Regressions) != 0 {
		t.Fatalf("an image without the model: %+v", st)
	}
}
