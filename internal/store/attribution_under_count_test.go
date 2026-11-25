package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

func resolveAs(project string) store.Resolver {
	return func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: project}, true, nil
	}
}

// Events is quoted to a person as how many observations were attributed, so it
// leaves out what no report counts. The rows are still attributed: the rule is
// applied where they are read.
func TestResolveProjectsUnderAttributesStoredGeneratedFilesWithoutCountingThem(t *testing.T) {
	f := newStore(t)
	run, err := f.BeginRun(context.Background(), store.RunSpec{SourceID: generated.Source, Input: "/tmp/walk"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	at := func(offset time.Duration, sourceID, locator string) source.Event {
		e := source.NewPoint(nine.Add(offset), source.Identity{Document: locator}, sourceID)
		e.Locator = locator
		return e
	}
	const (
		kept    = "/work/app/main.swift"
		built   = "/work/app/.build/debug/main.o"
		session = "/work/app/.build/checkouts/dep"
	)
	f.write(t, run, at(0, generated.Source, kept), at(time.Minute, generated.Source, built),
		at(time.Second, generated.Source, "/work/app/.venv/lib/site.py"),
		at(2*time.Second, generated.Source, "/work/app/.mypy_cache/x.json"))
	// Another source's locator under .build records somebody working there.
	f.write(t, f.run(t), at(2*time.Minute, sourceID, session))

	att, err := f.ResolveProjectsUnder(context.Background(), []string{"/work/app"}, resolveAs("p"))
	if err != nil {
		t.Fatalf("ResolveProjectsUnder: %v", err)
	}
	if att.Events["p"] != 2 {
		t.Errorf("Events = %v, want 2 under p: the source file and the session", att.Events)
	}
	if att.Updated != 5 {
		t.Errorf("Updated = %d, want all 5 rows", att.Updated)
	}
	if got := projectsByLocator(t, f.all(t))[built]; got != "p" {
		t.Errorf("a stored generated file has project %q, want p", got)
	}
}

// A directory already attributed needs no write, so a harvest holding the lock
// must not make it fail.
func TestResolveProjectsUnderTakesNoWriteLockWhenNothingWouldChange(t *testing.T) {
	f := newStore(t)
	f.write(t, f.run(t), located("/work/site/a"))
	ctx := context.Background()
	dirs := []string{"/work/site"}
	if _, err := f.ResolveProjectsUnder(ctx, dirs, resolveAs("p")); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	holder, err := sql.Open("sqlite", "file:"+f.Path()+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer holder.Close()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("take the write lock: %v", err)
	}
	defer tx.Rollback()

	att, err := f.ResolveProjectsUnder(ctx, dirs, resolveAs("p"))
	if err != nil {
		t.Fatalf("a pass with nothing to change = %v, want success", err)
	}
	if att.Updated != 0 || att.Events["p"] != 1 {
		t.Errorf("Updated, Events = %d, %v, want 0 and 1 under p", att.Updated, att.Events)
	}
}
