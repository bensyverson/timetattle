package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// STDOUT IS THE CONTRACT AND STDERR IS THE COMMENTARY.
//
// The primary consumer of this CLI is an agent capturing stdout, so nothing
// about a run being slow, fast, watched or timed may reach it. Every test here
// pins one half of that: the progress and the timings go to stderr, and the
// bytes on stdout are the same with them as without.

// metricsStreams runs the command with both streams captured and returns them.
// stderr is a plain buffer, so it is NOT a terminal and the live display stays
// off; use metricsOnATerminal for the other case.
func metricsStreams(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	if err := metricsWatching(&out, &errs, args); err != nil {
		t.Fatalf("metrics %v: %v", args, err)
	}
	return out.String(), errs.String()
}

// metricsOnATerminal runs the command with a stderr that reports a width, which
// is how the rest of this package pins terminal behaviour without depending on
// the terminal the tests happen to run in.
func metricsOnATerminal(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	if err := metricsWatching(&out, atWidth{&errs, 80}, args); err != nil {
		t.Fatalf("metrics %v: %v", args, err)
	}
	return out.String(), errs.String()
}

func metricsArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	return append([]string{"-store", seedTwoProjects(t), "-config",
		filepath.Join(t.TempDir(), "none.json"), "-all"}, extra...)
}

// A run that says nothing for a minute is indistinguishable from a hang, which
// is the whole reason this exists. Every phase names itself on the way past.
func TestMetricsNamesEachPhaseOnStderrWhileItRuns(t *testing.T) {
	_, errs := metricsOnATerminal(t, metricsArgs(t)...)

	for _, want := range []string{"reading events", "clustering", "client roll-up"} {
		if !strings.Contains(errs, want) {
			t.Errorf("the progress never mentions %q, so a reader cannot tell a slow phase from a "+
				"stuck one:\n%q", want, errs)
		}
	}
}

// THE ROLL-UP COUNTS ITSELF. "client roll-up" alone cannot distinguish progress
// from a stall; "client roll-up 2 of 2" can.
//
// The fixture is the one with clients in it. seedTwoProjects registers a client
// per directory but writes no client-project log, so nothing stamps a client
// onto its events and the roll-up has nothing to count — which is a real state
// of the world and is why the count is asserted against the fixture that does
// have clients rather than against whichever one was nearest.
func TestTheClientRollUpCountsItselfOnStderr(t *testing.T) {
	_, errs := metricsOnATerminal(t, "-store", seedAClientAcrossManyProjects(t),
		"-config", noConfig(t), "-all")

	if !strings.Contains(errs, "client roll-up 2 of 2") {
		t.Errorf("the progress never counts the clients it is rolling up:\n%q", errs)
	}
	if !strings.Contains(errs, "per-project time 4 of 4") {
		t.Errorf("the progress never counts the projects it is reducing:\n%q", errs)
	}
}

// A pipe gets no progress. Redrawing a line into a log file fills it with
// carriage returns and tells nobody anything, and scripts/profile captures
// exactly that stream.
func TestMetricsPrintsNoProgressWhenStderrIsNotATerminal(t *testing.T) {
	_, errs := metricsStreams(t, metricsArgs(t)...)

	if errs != "" {
		t.Errorf("a non-terminal stderr received %q; progress belongs on a screen, not in a log", errs)
	}
}

// The display erases itself, so the report is not printed underneath a stale
// half-line saying the run is still segmenting.
func TestTheProgressDisplayErasesItselfBeforeTheReport(t *testing.T) {
	_, errs := metricsOnATerminal(t, metricsArgs(t)...)

	if !strings.HasSuffix(errs, "\r") {
		t.Errorf("the progress does not end by returning to the start of a cleared line, so it "+
			"stays on screen above the report: %q", errs)
	}
	if strings.Contains(errs, "\n") {
		t.Errorf("the progress emitted a newline, so each phase scrolls the report away: %q", errs)
	}
}

// THE FLAG IS THE REPRODUCTION INSTRUMENT. Every figure this project has
// recorded about where a metrics run goes came from an agent temporarily
// editing internal/metrics, which nobody else could re-run. This is what
// replaces that.
func TestTheTimingsFlagNamesEveryPhaseOnStderr(t *testing.T) {
	_, errs := metricsStreams(t, metricsArgs(t, "-timings")...)

	for _, want := range []string{
		"reading events",
		"dropping machine activity",
		"stamping clients",
		"clustering",
		"grouping evidence",
		"per-project time",
		"client roll-up",
		"segmenting",
		"formatting",
		"total",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("the timings table never names %q:\n%s", want, errs)
		}
	}
}

// A TABLE THAT DOES NOT ADD UP IS WORSE THAN NO TABLE. Whatever the phases do
// not account for is named as its own row rather than left as a silent
// difference a reader has to subtract for themselves.
func TestTheTimingsTableAccountsForTheWholeRun(t *testing.T) {
	_, errs := metricsStreams(t, metricsArgs(t, "-timings")...)

	if !strings.Contains(errs, "elsewhere") {
		t.Errorf("the timings table does not name the time its phases failed to account for:\n%s", errs)
	}
}

// The timings go to stderr like the progress does, and for the same reason.
func TestTheTimingsFlagLeavesStdoutByteForByteAlone(t *testing.T) {
	args := metricsArgs(t)
	plain, _ := metricsStreams(t, args...)
	timed, errs := metricsStreams(t, append(append([]string{}, args...), "-timings")...)

	if plain != timed {
		t.Errorf("-timings changed stdout; a measurement flag that alters the report is not a "+
			"measurement of the report\nwithout:\n%s\nwith:\n%s", plain, timed)
	}
	if errs == "" {
		t.Error("-timings printed nothing at all to stderr")
	}
}

// Without the flag there is no table, on a terminal or off one. A run that
// always printed its timings would put commentary in front of every reader who
// did not ask for it.
func TestThereIsNoTimingsTableWithoutTheFlag(t *testing.T) {
	_, errs := metricsOnATerminal(t, metricsArgs(t)...)

	if strings.Contains(errs, "elsewhere") || strings.Contains(errs, "total") {
		t.Errorf("a run that was not asked for timings printed some:\n%q", errs)
	}
}
