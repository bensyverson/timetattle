package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// lockStore holds the write lock a harvest's attribution pass would, until the
// test ends.
func lockStore(t *testing.T, db string) {
	t.Helper()
	holder, err := sql.Open("sqlite", "file:"+db+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { holder.Close() })
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("take the write lock: %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })
}

// declareFormerPath registers dir and records that it used to be former,
// without putting it in any index a test goes on to use.
func declareFormerPath(t *testing.T, dir, former string) {
	t.Helper()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "atelier", Project: "loom"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former,
		IndexPath: filepath.Join(t.TempDir(), "other.json")}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
}

// A harvest already running may have passed these locators before the marker
// existed, so only one that starts afterwards can be promised.
func TestRegisterAgainstALockedStorePromisesOnlyAHarvestThatStartsLater(t *testing.T) {
	dir := t.TempDir()
	db := storeWithEvidence(t, filepath.Join(dir, "a.motn"))
	lockStore(t, db)

	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, want := range []string{"`timetattle harvest` that starts from now on", "already running may not"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

func TestReRegisteringAnAttributedDirectoryAgainstALockedStoreClaimsNothingIsMissing(t *testing.T) {
	dir := t.TempDir()
	db := storeWithEvidence(t, filepath.Join(dir, "a.motn"))
	args := registerArgs(t, dir, db, "loom")
	if _, err := registerOutput(t, args...); err != nil {
		t.Fatalf("register: %v", err)
	}
	lockStore(t, db)

	out, err := registerOutput(t, args...)
	if err != nil {
		t.Fatalf("register again: %v", err)
	}
	if strings.Contains(out, "not yet attributed") ||
		!strings.Contains(out, "attributed 1 stored observation to loom") {
		t.Errorf("output about an attributed directory:\n%s", out)
	}
}

// The full pass reads former paths through the index alone. Applying one for a
// marker the index does not hold would be undone by the next harvest.
func TestRegisterLeavesFormerPathsAloneWhenTheIndexCannotRecordTheMarker(t *testing.T) {
	dir, former := t.TempDir(), filepath.Join(t.TempDir(), "old-name")
	declareFormerPath(t, dir, former)
	stranded, inside := filepath.Join(former, "a.motn"), filepath.Join(dir, "b.motn")
	db := storeWithEvidence(t, stranded, inside)
	index := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(index, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("break the index: %v", err)
	}

	out, err := registerOutput(t, "-client", "atelier", "-project", "loom", "-path", dir,
		"-store", db, "-index", index)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	got := storedProjects(t, db)
	if got[stranded] != "" || got[inside] != "loom" {
		t.Errorf("projects = %v, want only the evidence under the directory itself", got)
	}
	for _, want := range []string{"attributed 1 stored observation to loom", "former path"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

func TestRegisterCountLeavesOutStoredGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	kept := filepath.Join(dir, "main.swift")
	built := filepath.Join(dir, ".build", "debug", "main.o")
	st, db := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: generated.Source, Input: "/tmp/walk"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var events []source.Event
	for i, l := range []string{kept, built} {
		e := source.NewPoint(time.Date(2026, 7, 14, 9, i, 0, 0, time.UTC),
			source.Identity{Document: filepath.Base(l)}, generated.Source)
		e.Locator = l
		events = append(events, e)
	}
	if _, err := run.Write(context.Background(), events); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(out, "attributed 1 stored observation to loom") {
		t.Errorf("count includes a generated file:\n%s", out)
	}
	if got := storedProjects(t, db)[built]; got != "loom" {
		t.Errorf("a stored generated file has project %q, want loom", got)
	}
}
