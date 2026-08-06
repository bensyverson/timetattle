package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The bare command: what `timetattle` with no arguments says.
//
// IT IS BOTH THINGS AT ONCE AND THAT IS THE DESIGN. It answers the question the
// current directory affords, and it prints the usage it has always printed, so
// that the one command a person types before they know anything still tells
// them what the commands are. A headline that REPLACED the usage would make the
// bare command mean different things in different directories, which is the
// worst property a bare command can have.

// seedRegisteredProject registers a directory, seeds the store with two of its
// observations inside the headline's own window, and derives the sittings the
// headline reads. It returns the directory and the store path.
//
// THE EVENTS ARE PLACED RELATIVE TO NOW, deliberately. The headline's window is
// the last fortnight, so a fixture written at a fixed date would report an
// empty project from the day it was written and would go on passing every
// assertion about the shape of the output.
func seedRegisteredProject(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	marker, err := registry.Register(registry.Registration{
		Dir: dir, Client: "Larkfield", Project: "Recruiting Site",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	var events []source.Event
	for _, offset := range []time.Duration{0, 4 * time.Minute} {
		e := source.NewPoint(base.Add(offset), source.Identity{Document: "notes.md"},
			"claude-transcripts")
		e.Locator = dir
		events = append(events, e)
	}
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	// The marker is handed to the attribution pass so the client-project log
	// gets its assertion: a client association lives in that log and never on
	// the marker alone, which is the whole reason the headline reads the log.
	if err := resolveProjects(ctx, st, []*registry.Marker{marker}, &strings.Builder{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	st.Close()
	return dir, path
}

// derive runs sessionize so that the headline has a stored derivation to read,
// which is the whole reason it can answer in a second rather than in fifteen.
func derive(t *testing.T, path string) {
	t.Helper()
	if err := sessionizeTo(io.Discard, []string{"-store", path,
		"-config", filepath.Join(t.TempDir(), "none.json"), "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}
}

func runBare(t *testing.T, dir, path string) string {
	t.Helper()
	var out strings.Builder
	if err := bareTo(&out, dir, path, filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatalf("bare: %v", err)
	}
	return out.String()
}

// Criterion Puj. Inside a registered directory the bare command names the
// project and says what it accounts for.
func TestBareInsideARegisteredDirectoryNamesTheProjectAndItsClient(t *testing.T) {
	dir, path := seedRegisteredProject(t)
	derive(t, path)

	out := runBare(t, dir, path)
	for _, want := range []string{"Recruiting Site", "Larkfield", "engaged", "presence"} {
		if !strings.Contains(out, want) {
			t.Errorf("the headline does not mention %q:\n%s", want, out)
		}
	}
}

// A FIGURE THAT DOES NOT NAME ITS WINDOW IS NOT A MEASUREMENT. "32.8h" over
// what?
func TestBareNamesTheWindowItsFiguresCover(t *testing.T) {
	dir, path := seedRegisteredProject(t)
	derive(t, path)

	out := runBare(t, dir, path)
	if !strings.Contains(out, "days to ") {
		t.Errorf("the headline does not say what span its figures cover:\n%s", out)
	}
}

// THE USAGE NEVER DISAPPEARS. That is what stops the bare command meaning one
// thing in one directory and something else in another.
func TestBareStillPrintsTheUsageItAlwaysPrinted(t *testing.T) {
	dir, path := seedRegisteredProject(t)
	derive(t, path)

	for _, out := range []string{runBare(t, dir, path), runBare(t, t.TempDir(), path)} {
		if !strings.Contains(out, "timetattle orient") || !strings.Contains(out, "timetattle metrics") {
			t.Errorf("the bare command does not print its usage:\n%s", out)
		}
	}
}

// ABSENCE OF A ROW IS NOT AN OBSERVATION, at the one place a person meets it
// first. Outside a registered directory there is no project headline, and that
// has to read as "this directory is not registered" rather than as a project
// with nothing in it.
func TestBareOutsideARegisteredDirectorySaysSoRatherThanShowingAnEmptyProject(t *testing.T) {
	_, path := seedRegisteredProject(t)
	derive(t, path)

	out := runBare(t, t.TempDir(), path)
	if !strings.Contains(out, "not a registered directory") {
		t.Errorf("the bare command does not say the directory is unregistered:\n%s", out)
	}
	if !strings.Contains(out, "timetattle register") {
		t.Errorf("the bare command does not say how to register it:\n%s", out)
	}
	if strings.Contains(out, "Recruiting Site") {
		t.Errorf("the bare command named a project the directory does not belong to:\n%s", out)
	}
}

// A store nobody has sessionized has no sittings, and that is not a fortnight
// of no work. The headline says which it is and names the command that fixes
// it.
func TestBareSaysNothingHasBeenDerivedRatherThanReportingZero(t *testing.T) {
	dir, path := seedRegisteredProject(t)

	out := runBare(t, dir, path)
	if !strings.Contains(out, "timetattle sessionize") {
		t.Errorf("the headline does not name the command that derives sittings:\n%s", out)
	}
	if !strings.Contains(out, "nothing has been derived") {
		t.Errorf("the headline does not say that nothing has been derived at all:\n%s", out)
	}
}

// On a machine with no store the bare command is still the command that says
// what the commands are. Failing there would break the one invocation a new
// user makes first.
func TestBareWithNoStoreAtAllStillPrintsUsage(t *testing.T) {
	dir, _ := seedRegisteredProject(t)
	missing := filepath.Join(t.TempDir(), "nothing.db")

	out := runBare(t, dir, missing)
	if !strings.Contains(out, "timetattle harvest") {
		t.Errorf("the bare command dropped its usage when the store was missing:\n%s", out)
	}
	if !strings.Contains(out, "no store") {
		t.Errorf("the bare command does not say the store is missing:\n%s", out)
	}
}

// THE STORED DERIVATION IS A SNAPSHOT AND THE WINDOW IS NOT.
//
// This is the cost of answering in two seconds instead of fifteen, and it must
// be said out loud rather than paid silently. `sessionize` clusters what was in
// the store when it ran; every observation harvested since is in no sitting at
// all, so it contributes nothing to the figures above. On the live store on
// 2026-08-04 the stored derivation was three days old, and the last three days
// of work were reading as no work.
//
// Counting the observations NEWER than the derivation is the check, rather than
// comparing the derivation's timestamp with the window's end: it measures the
// evidence that is actually being left out instead of inferring that some
// might be.
func TestBareSaysHowMuchEvidenceIsNewerThanTheDerivationItRead(t *testing.T) {
	dir, path := seedRegisteredProject(t)
	derive(t, path)

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/later"})
	if err != nil {
		t.Fatalf("begin a later run: %v", err)
	}
	// NOT truncated to the second. The point of this observation is that it is
	// later than the derivation, and the derivation was computed microseconds
	// ago; truncating would round it back before the thing it must come after.
	e := source.NewPoint(time.Now().UTC(),
		source.Identity{Document: "later.md"}, "claude-transcripts")
	e.Locator = dir
	if _, err := run.Write(ctx, []source.Event{e}); err != nil {
		t.Fatalf("write a later event: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the later run: %v", err)
	}
	if err := resolveProjects(ctx, st, nil, &strings.Builder{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	st.Close()

	// Whitespace collapsed first: the explanation is wrapped to the terminal
	// width, so any phrase in it may be split across a line and an assertion on
	// the raw text would pass or fail on where the margin happened to fall.
	out := runBare(t, dir, path)
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "newer than the derivation") {
		t.Errorf("the headline does not say that evidence arrived after the derivation it read, so "+
			"the newest work reads as no work:\n%s", out)
	}
}

// EXIT STATUS. The bare command always produces the output it is designed to
// produce, so it succeeds. It used to exit 2 — usage-as-error — which is now
// wrong twice over: the output is a legitimate answer, and `timetattle -h`
// prints the same usage and exits 0 already.
//
// The exit code must NOT depend on the directory. A status that said 0 inside a
// registered project and 2 outside one would make every script wrapping this
// command behave differently depending on where it was run from, which is a
// worse trap than either fixed value.
func TestBareExitsZeroWhateverTheDirectoryHolds(t *testing.T) {
	dir, path := seedRegisteredProject(t)
	derive(t, path)

	for _, where := range []string{dir, t.TempDir()} {
		t.Chdir(where)
		if code := run(nil, io.Discard, io.Discard, path, ""); code != 0 {
			t.Errorf("bare timetattle in %s exited %d, want 0", where, code)
		}
	}
	// An unknown command is still a usage error and still exits 2.
	if code := run([]string{"frobnicate"}, io.Discard, io.Discard, path, ""); code != 2 {
		t.Errorf("an unknown command exited %d, want 2", code)
	}
}
