package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// storeWithEvidence writes one observation at each locator into a temporary
// store and attributes none of them, which is what a harvest of a directory
// nobody had registered yet leaves behind.
func storeWithEvidence(t *testing.T, locators ...string) string {
	t.Helper()
	st, path := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "motion-autosave", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var events []source.Event
	for i, l := range locators {
		// Distinct instants, because locator is not part of the natural key.
		events = append(events, motionEventAt(time.Date(2026, 7, 14, 9, i, 0, 0, time.UTC), l))
	}
	if _, err := run.Write(context.Background(), events); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return path
}

func storedProjects(t *testing.T, path string) map[string]string {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer st.Close()
	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	out := map[string]string{}
	for _, e := range events {
		out[e.Locator] = e.Identity.Project
	}
	return out
}

func registerOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return printed(t, func() error { return register(args) })
}

// printed runs f and returns what it wrote to the process's own streams, which
// is where register and harvest print.
func printed(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	runErr := f()
	os.Stdout, os.Stderr = stdout, stderr
	w.Close()
	return <-read, runErr
}

func registerArgs(t *testing.T, dir, db, project string) []string {
	t.Helper()
	return []string{"-client", "atelier", "-project", project, "-path", dir, "-store", db,
		"-index", filepath.Join(t.TempDir(), "index.json")}
}

func TestRegisterAttributesTheEvidenceAlreadyStoredUnderTheDirectory(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	inside := filepath.Join(dir, "sub", "a.motn")
	outside := filepath.Join(elsewhere, "b.motn")
	db := storeWithEvidence(t, dir, inside, outside)

	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	got := storedProjects(t, db)
	if got[dir] != "loom" || got[inside] != "loom" {
		t.Errorf("projects = %v, want the two observations under %s attributed to loom", got, dir)
	}
	if got[outside] != "" {
		t.Errorf("%s was attributed to %q; it is not under the directory", outside, got[outside])
	}
	if !strings.Contains(out, "attributed 2 stored observations to loom") {
		t.Errorf("output does not say how many it tagged:\n%s", out)
	}
}

func TestRegisterSaysSoWhenThereIsNoStoredEvidenceToAttribute(t *testing.T) {
	db := storeWithEvidence(t, filepath.Join(t.TempDir(), "b.motn"))

	out, err := registerOutput(t, registerArgs(t, t.TempDir(), db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(out, "attributed 0 stored observations") {
		t.Errorf("a zero has to be stated rather than left unsaid:\n%s", out)
	}
}

func TestRegisterLeavesANestedMarkerItsOwnEvidence(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "vendor", "inner")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := registry.Register(registry.Registration{Dir: nested, Client: "bindery", Project: "inner"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	outerFile, innerFile := filepath.Join(dir, "a.motn"), filepath.Join(nested, "b.motn")
	db := storeWithEvidence(t, outerFile, innerFile)

	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	got := storedProjects(t, db)
	if got[outerFile] != "loom" || got[innerFile] != "inner" {
		t.Errorf("projects = %v, want the nested directory's evidence under inner and the rest under loom", got)
	}
	// The count credited to this project must not include the nested one's.
	if !strings.Contains(out, "attributed 1 stored observation to loom") ||
		!strings.Contains(out, "and 1 to another project") {
		t.Errorf("output does not separate what went to the nested project:\n%s", out)
	}
}

// A directory blessed through a symlink has evidence spelled both ways: through
// the link, and by the physical path a program reports for its own cwd.
func TestRegisterThroughASymlinkAttributesBothSpellings(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	physical, linked := filepath.Join(real, "a.motn"), filepath.Join(link, "b.motn")
	db := storeWithEvidence(t, physical, linked)

	if _, err := registerOutput(t, registerArgs(t, link, db, "loom")...); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := storedProjects(t, db)
	if got[physical] != "loom" || got[linked] != "loom" {
		t.Errorf("projects = %v, want both spellings attributed", got)
	}
}

func TestRegisterAttributesEvidenceUnderADeclaredFormerPath(t *testing.T) {
	dir := t.TempDir()
	former := filepath.Join(t.TempDir(), "old-name")
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "atelier", Project: "loom"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former,
		IndexPath: filepath.Join(t.TempDir(), "index.json")}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	stranded := filepath.Join(former, "a.motn")
	db := storeWithEvidence(t, stranded)

	if _, err := registerOutput(t, registerArgs(t, dir, db, "loom")...); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := storedProjects(t, db)[stranded]; got != "loom" {
		t.Errorf("evidence under the former path has project %q, want loom", got)
	}
}

// A harvest holds the write lock for as long as its attribution pass takes.
// Registration is a marker file and must not depend on that.
func TestRegisterSucceedsAgainstALockedStoreAndSaysWhatWasNotDone(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "a.motn")
	db := storeWithEvidence(t, inside)

	holder, err := sql.Open("sqlite", "file:"+db+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer holder.Close()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("take the write lock: %v", err)
	}
	defer tx.Rollback()

	began := time.Now()
	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register against a locked store = %v, want success", err)
	}
	// Loose on purpose: it proves the wait ends, not how long it is.
	if waited := time.Since(began); waited > time.Minute {
		t.Errorf("register waited %v for the lock", waited)
	}
	for _, want := range []string{"registered to atelier / loom", "not yet attributed", "timetattle harvest", "locked"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if _, found, err := registry.Resolve(dir); err != nil || !found {
		t.Errorf("no marker after registering: %v, %v", found, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	if got := storedProjects(t, db)[inside]; got != "" {
		t.Errorf("project = %q after a locked run, want none", got)
	}
}

func TestRegisterNamesALocatorItCouldNotResolve(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	bad := filepath.Join(loop, "a.motn")
	good := filepath.Join(dir, "b.motn")
	db := storeWithEvidence(t, bad, good)

	out, err := registerOutput(t, registerArgs(t, dir, db, "loom")...)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if storedProjects(t, db)[good] != "loom" || !strings.Contains(out, "unreadable: "+bad) {
		t.Errorf("want the readable evidence attributed and the unreadable one named:\n%s", out)
	}
}
