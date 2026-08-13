package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// THE ACTIVITY MIX AT THE COMMAND. The report could say a week held forty hours
// and could not say how much of that was Claude, how much was git and how much
// was a file changing on disk. What it must never do is print those as columns
// that add up, because the streams observe the same minutes.

func mixReport(t *testing.T, extra ...string) string {
	t.Helper()
	args := append([]string{"-store", seedTwoProjects(t),
		"-config", filepath.Join(t.TempDir(), "none.json"), "-all"}, extra...)
	return runMetrics(t, args...)
}

// The section exists, names the families, and NAMES THE SOURCE IDS BEHIND EACH
// ONE. A grouping a reader cannot check is a verdict whose list nobody has
// seen — the standing rule about a tool only being as good as the list it was
// handed — so the mapping travels with the figures rather than living in code.
func TestTheReportShowsTheActivityMixAndTheSourcesBehindEachRow(t *testing.T) {
	out := mixReport(t)

	for _, want := range []string{
		"ACTIVITY MIX",
		"participated",
		"solely",
		"Claude",
		"Apps",
		// The mapping, made visible: this corpus holds one transcript source
		// and one focus-interval source, and the report says which row each
		// went into.
		"claude-transcripts",
		"biome-appusage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// THE SENTENCE THE WHOLE SECTION DEPENDS ON. Every other table in this report
// has a total; this one must not be read as having one, and a reader who adds
// the column up gets a number that is not any quantity at all.
func TestTheActivityMixSaysItsRowsOverlapAndNeverSum(t *testing.T) {
	out := mixReport(t)

	for _, want := range []string{"OVERLAP", "NEVER SUM"} {
		if !strings.Contains(out, want) {
			t.Errorf("the activity mix does not say its rows %q:\n%s", want, out)
		}
	}
}

// THE THIRD STATE, printed even at zero. Clock no family's own evidence reaches
// is a measured figure here rather than an absence, and it is what tells a
// reader the rows do not tile presence. Omitting it when it happens to be zero
// would make the mix look like a partition on exactly the corpora where it most
// nearly is one.
func TestTheActivityMixNamesTheClockNoFamilyReaches(t *testing.T) {
	out := mixReport(t)

	if !strings.Contains(out, "reached by NO family") {
		t.Errorf("the activity mix does not say how much of presence no family reaches:\n%s", out)
	}
}

// A FILTER SELECTS ROWS AND RECOMPUTES NOTHING, which is already true of the
// PRESENCE line this section is a decomposition of. So the mix is the whole
// span's, under -client or -project, and says so rather than letting a reader
// take it for the filtered scope's own mix.
func TestTheActivityMixIsTheWholeSpansUnderAFilterAndSaysSo(t *testing.T) {
	out := mixReport(t, "-project", "Holiday Campaign")

	if !strings.Contains(out, "ACTIVITY MIX") {
		t.Fatalf("a filtered report dropped the activity mix entirely:\n%s", out)
	}
	if !strings.Contains(out, "the mix above is the WHOLE span's") {
		t.Errorf("a filtered report does not say the mix was not narrowed:\n%s", out)
	}
	// The unfiltered report says nothing of the kind, so the sentence is a
	// property of filtering rather than boilerplate.
	if strings.Contains(mixReport(t), "the mix above is the WHOLE span's") {
		t.Errorf("the unfiltered report carries the filtered report's caveat")
	}
}

// ABSENCE OF A ROW IS NOT AN OBSERVATION, which is this project's cardinal
// rule and the one it has broken most often. A family with no observation in
// this span has no row, and correctly so — a 0.0h row would state "Blender
// observed none of this week" when the only thing known is that no Blender
// observation exists here. But silence about it says nothing either, and a
// reader cannot tell a family that recorded nothing from one the tool has
// never heard of. So the families that are missing are named.
func TestTheActivityMixNamesTheFamiliesThatRecordedNothing(t *testing.T) {
	out := mixReport(t)

	// This corpus is transcripts and one focus interval: five of the seven
	// families recorded nothing in it, and the report must say which.
	if !strings.Contains(out, "no observation in this span") {
		t.Fatalf("the mix does not say which families recorded nothing:\n%s", out)
	}
	for _, want := range []string{"Blender", "Files", "Git", "Jobs", "Motion"} {
		if !strings.Contains(out, want) {
			t.Errorf("family %q is neither a row nor named as absent, so a reader cannot tell "+
				"it recorded nothing from it not existing:\n%s", want, out)
		}
	}
}
