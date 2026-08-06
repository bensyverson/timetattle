package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// THE FIXTURE IS THE TEST. Every client in the live registry is 1:1 with its
// project except one, which spans 33 of them, so a filter and a roll-up proved
// against a 1:1 client prove nothing at all — the client row and the project
// row would be the same number arrived at two ways. This fixture reproduces the
// shape that matters: one client across THREE projects whose evidence overlaps
// inside one sitting, beside a second client on one project.
//
// The names are invented. The live client names are a confidentiality matter
// (task 5nCl4) and none of them appears in this repository.

// seedAClientAcrossManyProjects returns a store in which "atelier" owns loom,
// kiln and press, and "bindery" owns folio. The directories are really
// registered and the attribution pass really runs, so the client reaches the
// column the same way it will in production rather than being written straight
// into it by the test.
func seedAClientAcrossManyProjects(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	var events []source.Event
	var registered []*registry.Marker
	owners := []struct{ client, project string }{
		{"atelier", "loom"}, {"atelier", "kiln"}, {"atelier", "press"}, {"bindery", "folio"},
	}
	for i, o := range owners {
		dir := t.TempDir()
		m, err := registry.Register(registry.Registration{
			Dir: dir, Client: o.client, Project: o.project, Among: registered,
		})
		if err != nil {
			t.Fatalf("Register %s/%s: %v", o.client, o.project, err)
		}
		registered = append(registered, m)
		// Three minutes apart, so one sitting holds all four and the three
		// atelier projects overlap almost entirely. That overlap is what makes
		// a union differ from a sum.
		e := source.NewPoint(base.Add(time.Duration(i)*3*time.Minute),
			source.Identity{Document: "notes.md"}, "claude-transcripts")
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
	if err := resolveProjects(ctx, st, registered, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	return path
}

func noConfig(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "none.json")
}

// CRITERION 4i3. The client appears once, above its projects, and its projects
// appear too — a roll-up that replaced the detail would answer a different
// question from the one asked.
func TestMetricsRollsAClientUpAcrossItsProjects(t *testing.T) {
	out := runMetrics(t, "-store", seedAClientAcrossManyProjects(t), "-config", noConfig(t))

	if !strings.Contains(out, "ENGAGED, per client") {
		t.Errorf("the report has no client roll-up at all:\n%s", out)
	}
	for _, want := range []string{"atelier", "bindery", "loom", "kiln", "press", "folio"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// CRITERION FiE, first half. -client narrows the report to one client and to
// that client's projects, and says nothing about anybody else's.
func TestMetricsFiltersToOneClient(t *testing.T) {
	out := runMetrics(t, "-store", seedAClientAcrossManyProjects(t), "-config", noConfig(t),
		"-client", "atelier")

	named := rowNames(out)
	for _, want := range []string{"atelier", "loom", "kiln", "press"} {
		if !named[want] {
			t.Errorf("filtering to atelier dropped %q, which is atelier's:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"bindery", "folio"} {
		if named[unwanted] {
			t.Errorf("filtering to atelier still gives %q a row:\n%s", unwanted, out)
		}
	}
}

// CRITERION FiE, second half.
//
// The assertion is on the TABLE ROWS and not on the whole report, because the
// report is prose as well as figures: matching "press" as a substring finds it
// inside "suppressed" in the observation counts at the foot, which is a false
// positive that would have made this test pass or fail for the wrong reason.
func TestMetricsFiltersToOneProject(t *testing.T) {
	out := runMetrics(t, "-store", seedAClientAcrossManyProjects(t), "-config", noConfig(t),
		"-project", "kiln")

	named := rowNames(out)
	if !named["kiln"] {
		t.Errorf("filtering to kiln dropped kiln:\n%s", out)
	}
	for _, unwanted := range []string{"loom", "press", "folio"} {
		if named[unwanted] {
			t.Errorf("filtering to kiln still gives %q a row:\n%s", unwanted, out)
		}
	}
}

// rowNames is the set of names in the first column of every table row, which is
// what a filter selects. Whole-report substring matching cannot be used for
// this: a project called "press" appears inside the word "suppressed" in the
// report's own footer, and one called "sum" inside a dozen sentences.
func rowNames(report string) map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(report, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		fields := strings.Fields(line)
		// A data row is a name followed by figures; the "0.0h" hanging
		// indents of the unattributed breakdown start with a figure instead.
		if len(fields) >= 6 && strings.HasSuffix(fields[1], "h") {
			out[fields[0]] = true
		}
	}
	return out
}

// A FILTER SELECTS ROWS AND CHANGES NO FIGURE IN THEM, and that has to be
// asserted rather than assumed.
//
// The tempting implementation is to filter the EVENTS and recompute, which is
// wrong in a way nobody would see: presence is wall clock across every source
// at once, so dropping the other clients' evidence shrinks the denominator and
// every share climbs toward 100%. The whole point of asking for one client's
// metrics is to see what share of a real day it took.
func TestFilteringSelectsRowsWithoutChangingAnyFigureInThem(t *testing.T) {
	path := seedAClientAcrossManyProjects(t)
	whole := runMetrics(t, "-store", path, "-config", noConfig(t))
	one := runMetrics(t, "-store", path, "-config", noConfig(t), "-client", "atelier")

	presence := func(report string) string {
		for line := range strings.SplitSeq(report, "\n") {
			if strings.HasPrefix(line, "PRESENCE ") {
				return line
			}
		}
		return ""
	}
	if presence(whole) == "" {
		t.Fatalf("the unfiltered report has no PRESENCE line:\n%s", whole)
	}
	if got, want := presence(one), presence(whole); got != want {
		t.Errorf("filtering changed presence from %q to %q; a filter must select rows out of "+
			"one computation, never re-run a smaller one", want, got)
	}

	row := func(report, name string) string {
		for line := range strings.SplitSeq(report, "\n") {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == name {
				return line
			}
		}
		return ""
	}
	if got, want := row(one, "kiln"), row(whole, "kiln"); got == "" || got != want {
		t.Errorf("kiln reads %q filtered and %q unfiltered; the figures must be identical",
			got, want)
	}
}

// Asking for a client nothing was recorded for must say so, and must never be
// answered with a confident set of zeroes. "Observed not-X" and "nothing was
// recorded" are three-state discipline this codebase runs on, and a filter is
// the easiest place in the whole tool to collapse them.
func TestFilteringToSomethingWithNoEvidenceSaysSoRatherThanPrintingZeroes(t *testing.T) {
	var out bytes.Buffer
	err := metricsTo(&out, []string{"-store", seedAClientAcrossManyProjects(t),
		"-config", noConfig(t), "-client", "no-such-client"})
	if err != nil {
		t.Fatalf("metrics -client no-such-client: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "no-such-client") {
		t.Errorf("the report does not name the client that matched nothing:\n%s", got)
	}
	if !strings.Contains(got, "nothing") {
		t.Errorf("a filter matching no evidence printed no such statement; a reader cannot tell "+
			"it from a client with zero hours:\n%s", got)
	}
}

// A BARE DATE ON -to NAMES A DAY, AND A DAY ENDS AT THE END OF IT.
//
// -to takes an INSTANT, so `-to 2026-07-28` meant 2026-07-28T00:00:00 and
// silently dropped the whole of the day the user named. store.Filter bounds the
// span INCLUSIVELY at both ends, so `-from D -to D` — much the most natural way
// to ask for one day — selected only whatever landed exactly on midnight, which
// on this corpus is nothing.
//
// The fixture's evidence is at 15:00 UTC on 2026-07-28, so it is inside the day
// and outside the midnight instant, and that is the whole difference.
func TestABareDateOnToCoversTheWholeOfThatDay(t *testing.T) {
	out := runMetrics(t, "-store", seedAClientAcrossManyProjects(t), "-config", noConfig(t),
		"-from", "2026-07-28", "-to", "2026-07-28")

	if strings.Contains(out, "nothing was recorded") {
		t.Errorf("-from 2026-07-28 -to 2026-07-28 found nothing, so -to still means midnight "+
			"that morning and the day the user named was dropped:\n%s", out)
	}
	if !strings.Contains(out, "atelier") {
		t.Errorf("the day's evidence is missing from a report bounded to that day:\n%s", out)
	}
}

// The other half of the same decision, and the reason it is not simply "-to
// means later". An explicit instant is an instant: somebody who writes a time
// meant that time, and rounding it up to midnight would silently widen a window
// they were precise about.
func TestAnExplicitInstantOnToIsStillThatInstant(t *testing.T) {
	out := runMetrics(t, "-store", seedAClientAcrossManyProjects(t), "-config", noConfig(t),
		"-from", "2026-07-28T00:00:00Z", "-to", "2026-07-28T00:00:00Z")

	if !strings.Contains(out, "nothing was recorded") {
		t.Errorf("-to with an explicit midnight instant was widened to the end of the day; a "+
			"time somebody typed is not a date:\n%s", out)
	}
}
