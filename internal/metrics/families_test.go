package metrics_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// THE ACTIVITY MIX: which KIND of evidence stream observed the work.
//
// Constructed cases. They sit beside the real-data checks in realdata_test.go
// rather than in place of them, for the reason metrics_test.go's own header
// gives: a fixture cannot establish that this survives 800,000 observations,
// and real data can validate a wrong rule.
//
// The property under test throughout is that these figures OVERLAP. A commit
// made during a Claude Code session is observed by gitlog and by
// claude-transcripts, so the same minute belongs to both families and columns
// that summed to presence would be arithmetically false. Every case below that
// has two families in it checks the sum EXCEEDS the clock, on purpose.

// commitAt is a commit: the gitlog half of a minute Claude also observed.
func commitAt(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "gitlog-commits")
	e.Locator = "/Users/you/git/" + project
	return e
}

// taskEvent and taskClaim are the two streams internal/jobs emits. They are one
// family, and TestBothJobStreamsAreOneFamily is why.
func taskEvent(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "job-events")
	e.Locator = "/Users/you/git/" + project
	return e
}

func taskClaim(start, end time.Time, project string) source.Event {
	e := source.NewInterval(start, end, source.Identity{Project: project}, "job-claims")
	e.Locator = "/Users/you/git/" + project
	return e
}

// blenderSave is an explicit save; motionSave is a fifteen-minute timer. Same
// kind, same shape, same reader, measurably different worth — see
// internal/source/strength.go.
func blenderSave(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "blender-backups")
	e.Locator = "/Users/you/git/" + project
	return e
}

func motionSave(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "motion-autosave")
	e.Locator = "/Users/you/git/" + project
	return e
}

// backlit is a presence reading. It bounds sittings and creates none, so it is
// in no family at all.
func backlit(start, end time.Time, state string) source.Event {
	e := source.NewInterval(start, end, source.Identity{}, "knowledgec-backlit")
	e.State = state
	return e
}

func mixOf(t *testing.T, r metrics.Report, name string) metrics.Family {
	t.Helper()
	f, ok := r.Mix.Family(name)
	if !ok {
		var have []string
		for _, f := range r.Mix.Families {
			have = append(have, f.Name)
		}
		t.Fatalf("no family %q in the activity mix; it holds %v", name, have)
	}
	return f
}

func mixNames(r metrics.Report) []string {
	out := make([]string, 0, len(r.Mix.Families))
	for _, f := range r.Mix.Families {
		out = append(out, f.Name)
	}
	return out
}

// THE CASE THE WHOLE FEATURE EXISTS FOR. A commit made in the middle of a
// Claude session: both families observed the same minute, so the two
// participation figures overlap and sum to more than the clock. A breakdown
// whose columns summed to presence would have to take that minute off one of
// them, and there is no principled way to choose which.
func TestTwoFamiliesObservingTheSameMinuteBothHoldItAndTheSumExceedsTheClock(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 10), "acorn"),
		point(at(9, 20), "acorn"),
		commitAt(at(9, 10), "acorn"),
	})

	// One sitting, 08:55–09:25.
	if want := 30 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v (one sitting, 08:55–09:25)", r.Presence.Total, want)
	}
	claude, git := mixOf(t, r, "Claude"), mixOf(t, r, "Git")
	if want := 30 * time.Minute; claude.Engaged.Total != want {
		t.Errorf("Claude participated in %v, want %v: its own three observations cluster to the "+
			"whole sitting", claude.Engaged.Total, want)
	}
	if want := 10 * time.Minute; git.Engaged.Total != want {
		t.Errorf("Git participated in %v, want %v (09:05–09:15): one commit reaches its lead-in and "+
			"tail-out and no further", git.Engaged.Total, want)
	}
	if summed := claude.Engaged.Total + git.Engaged.Total; summed <= r.Presence.Total {
		t.Errorf("the families summed to %v against %v of presence; a minute two streams both "+
			"observed must count for both, so the rows have to overlap",
			summed, r.Presence.Total)
	}
}

// The SOLELY figure: what would drop out of the mix if a stream were not there.
// Git observed nothing Claude did not, so removing Git loses no clock at all,
// while removing Claude loses twenty minutes.
func TestSolelyIsTheClockNoOtherFamilyReaches(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 10), "acorn"),
		point(at(9, 20), "acorn"),
		commitAt(at(9, 10), "acorn"),
	})

	claude, git := mixOf(t, r, "Claude"), mixOf(t, r, "Git")
	if want := 20 * time.Minute; claude.Solely != want {
		t.Errorf("Claude alone holds %v, want %v: 08:55–09:05 and 09:15–09:25 are reached by "+
			"nothing else", claude.Solely, want)
	}
	if git.Solely != 0 {
		t.Errorf("Git alone holds %v, want none: every minute the commit reaches is inside the "+
			"Claude session it was made during", git.Solely)
	}
}

// THE THIRD STATE. Clock that IS presence and that no family's own evidence
// clusters to: silence the global gap threshold bridged between two streams,
// which neither stream bridges by itself. Giving it to a family would credit a
// stream with clock it never reached; leaving it out of the report entirely
// would make the mix look like a partition of presence, which it is not.
func TestClockNoFamilyReachesIsReportedRatherThanGivenToOne(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		commitAt(at(9, 14), "acorn"),
		point(at(9, 28), "acorn"),
	})

	// Every gap is 14m, under the 15m threshold, so this is ONE sitting
	// running 08:55–09:33.
	if want := 38 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v (one sitting)", r.Presence.Total, want)
	}
	claude, git := mixOf(t, r, "Claude"), mixOf(t, r, "Git")
	// Claude's own two observations are 28m apart, which its own clustering
	// does NOT bridge: 08:55–09:05 and 09:23–09:33.
	if want := 20 * time.Minute; claude.Engaged.Total != want {
		t.Errorf("Claude participated in %v, want %v", claude.Engaged.Total, want)
	}
	if want := 10 * time.Minute; git.Engaged.Total != want {
		t.Errorf("Git participated in %v, want %v", git.Engaged.Total, want)
	}
	if want := 8 * time.Minute; r.Mix.Unreached != want {
		t.Errorf("%v of presence is reached by no family, want %v (09:05–09:09 and 09:19–09:23); "+
			"the clustering bridged it and no single stream does", r.Mix.Unreached, want)
	}
	// The mix accounts for the clock exactly once it is told what nothing
	// reaches: 20 + 10 solely, plus 8 nothing reaches, is the 38m sitting.
	if got := claude.Solely + git.Solely + r.Mix.Unreached; got != r.Presence.Total {
		t.Errorf("what each family alone holds (%v, %v) plus what nothing reaches (%v) came to %v "+
			"against %v of presence", claude.Solely, git.Solely, r.Mix.Unreached, got,
			r.Presence.Total)
	}
}

// THE MAPPING IS THE SAME ONE THE DASHBOARD ALREADY DRAWS. Both streams
// internal/jobs emits are one family, and the reason is the whole design of
// this section: rows never sum, so splitting one tool's output across two of
// them would leave a reader who asked "how much of my work touched task
// tracking" with two figures and no way to add them.
func TestBothJobStreamsAreOneFamily(t *testing.T) {
	r := compute(t, []source.Event{
		taskEvent(at(9, 0), "acorn"),
		taskClaim(at(9, 2), at(9, 8), "acorn"),
	})

	if got := mixNames(r); !slices.Equal(got, []string{"Jobs"}) {
		t.Fatalf("the mix holds %v, want one Jobs family covering both streams", got)
	}
	jobs := mixOf(t, r, "Jobs")
	if want := []string{"job-claims", "job-events"}; !slices.Equal(jobs.Sources, want) {
		t.Errorf("Jobs was built from %v, want %v; the report has to name the sources behind a "+
			"row or the grouping is a verdict nobody can check", jobs.Sources, want)
	}
	// A claim is an INTERVAL and observes a duration, which almost nothing else
	// attributable does. It is what stops the observed column being a constant
	// zero.
	if want := 6 * time.Minute; jobs.Engaged.Observed != want {
		t.Errorf("Jobs observed %v of its own participation, want %v: a held claim spans clock "+
			"rather than marking an instant", jobs.Engaged.Observed, want)
	}
}

// A WALKED FILE TIMESTAMP IS NOT A CREATIVE APPLICATION'S SAVE. fs-observations
// says a file changed and cannot say which application changed it or whether a
// person did; a .blend1 and a Motion autosave name the application they came
// from. Folding the generic walk in with them would put the stream that read a
// log-rotation cron as 135.2h of one client's work in the same row as an
// explicit save measured 4 of 4 and 5 of 5.
func TestTheGenericFileWalkIsItsOwnFamilyAndTheApplicationsAreTheirs(t *testing.T) {
	r := compute(t, []source.Event{
		mtime(at(9, 0), "acorn"),
		blenderSave(at(9, 10), "acorn"),
		motionSave(at(9, 20), "acorn"),
	})

	if got := mixNames(r); !slices.Equal(got, []string{"Blender", "Files", "Motion"}) {
		t.Fatalf("the mix holds %v, want Blender, Files and Motion as three separate families", got)
	}
	if got := mixOf(t, r, "Files").Sources; !slices.Equal(got, []string{"fs-observations"}) {
		t.Errorf("Files was built from %v, want only fs-observations", got)
	}
}

// A SOURCE NO FAMILY HAS BEEN DECIDED FOR IS NAMED, NOT DROPPED. A manifest can
// declare any id it likes, so the mapping is a list and a list goes stale
// silently — in the direction that makes a report look complete. This is the
// state that is neither "observed in a family" nor "nothing was recorded".
func TestASourceWithNoFamilyIsNamedRatherThanDropped(t *testing.T) {
	strange := source.NewPoint(at(9, 0), source.Identity{Project: "acorn"}, "wibble-widgets")
	strange.Locator = "/Users/you/git/acorn"

	r := compute(t, []source.Event{strange})
	if got := mixNames(r); !slices.Equal(got, []string{"unclassified"}) {
		t.Fatalf("the mix holds %v, want the observation carried under an unclassified row", got)
	}
	if got := mixOf(t, r, "unclassified").Sources; !slices.Equal(got, []string{"wibble-widgets"}) {
		t.Errorf("the unclassified row was built from %v, want it to name wibble-widgets so "+
			"somebody can decide where it belongs", got)
	}
}

// PRESENCE AND IGNORED SOURCES ARE IN NO FAMILY. They create no engaged time by
// construction — a backlight reading bounds a sitting and never starts one —
// so a row for them would be a figure about work that the stream cannot make.
// The backlight has its own four figures elsewhere in the report.
func TestPresenceAndIgnoredSourcesGetNoRowInTheMix(t *testing.T) {
	// The shipped ignore list, which testParams deliberately leaves empty:
	// under those defaults knowledgec-islocked IS evidence, and the point here
	// is what a ROLE does to the mix rather than what any one id is.
	p := testParams()
	p.IgnoreSources = []string{"knowledgec-islocked"}

	r, err := metrics.Compute([]source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 10), "acorn"),
		backlit(at(8, 0), at(11, 0), "on"),
		source.NewInterval(at(9, 0), at(9, 5), source.Identity{}, "knowledgec-islocked"),
	}, p)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if got := mixNames(r); !slices.Equal(got, []string{"Claude"}) {
		t.Fatalf("the mix holds %v, want only Claude: the other two are a presence reading and an "+
			"ignored one, and neither evidences any work", got)
	}
	// Both of those ids map to Apps, so this is not passing because they are
	// unmapped: change either role to evidence and an Apps row appears.
	if fam, ok := source.FamilyOf("knowledgec-islocked"); !ok || fam != source.FamilyApps {
		t.Errorf("knowledgec-islocked maps to %q; this case is only meaningful while it has a "+
			"family that the role is what keeps out of the mix", fam)
	}
}

// A FAMILY WITH NOTHING IN THIS SPAN IS ABSENT, NOT ZERO. "Blender observed
// none of this week" and "no Blender observation exists in this window at all"
// are different facts, and a row of 0.0h states the first when only the second
// is known.
func TestAFamilyWithNoObservationInTheSpanIsAbsentRatherThanZero(t *testing.T) {
	r := compute(t, []source.Event{point(at(9, 0), "acorn"), point(at(9, 10), "acorn")})
	// Asserted first, so this cannot pass by the mix being empty: the row that
	// SHOULD be here is here, and the one that should not is not.
	if _, ok := r.Mix.Family("Claude"); !ok {
		t.Fatalf("the mix holds %v and not the Claude row this corpus is made of", mixNames(r))
	}
	if _, ok := r.Mix.Family("Blender"); ok {
		t.Errorf("the mix carries a Blender row on a corpus holding no Blender observation")
	}
}

// UNATTRIBUTED EVIDENCE IS STILL EVIDENCE OF A KIND OF WORK. Biome and
// knowledgeC carry no path and can never be attributed to a project, so they
// appear in no client row and no project row anywhere in the report. The
// activity mix is the one place they are counted, and leaving them out would
// answer "what kind of work was this" with only the streams that happen to
// resolve to a directory.
func TestAFamilyThatCanNeverBeAttributedStillAppearsInTheMix(t *testing.T) {
	r := compute(t, []source.Event{
		unlocatable(at(9, 0), at(9, 20)),
		point(at(9, 30), "acorn"),
	})

	if want := 40 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v", r.Presence.Total, want)
	}
	apps := mixOf(t, r, "Apps")
	if want := 30 * time.Minute; apps.Engaged.Total != want {
		t.Errorf("Apps participated in %v, want %v (08:55–09:25)", apps.Engaged.Total, want)
	}
	if want := 20 * time.Minute; apps.Engaged.Observed != want {
		t.Errorf("Apps observed %v of it, want %v: a focus interval spans real clock", //
			apps.Engaged.Observed, want)
	}
	// And it holds every project row at zero, which is the contrast that makes
	// the row worth printing.
	if len(r.Projects) != 1 {
		t.Fatalf("%d projects, want 1", len(r.Projects))
	}
}

// The rows are sorted, so two runs read the same way and a diff of two reports
// is a diff of the figures.
func TestTheMixIsSortedByFamilyName(t *testing.T) {
	r := compute(t, []source.Event{
		commitAt(at(9, 0), "acorn"),
		point(at(9, 5), "acorn"),
		mtime(at(9, 10), "acorn"),
	})
	got := mixNames(r)
	if !slices.IsSorted(got) {
		t.Errorf("the mix reads %v, which is not sorted", got)
	}
	if !slices.Equal(got, []string{"Claude", "Files", "Git"}) {
		t.Errorf("the mix holds %v, want Claude, Files and Git", got)
	}
}

// EVERY FAMILY IS INSIDE PRESENCE. A family's participation is its own evidence
// clustered and then intersected with the sittings, so it can never exceed the
// clock however much evidence it holds.
func TestNoFamilyExceedsPresence(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		commitAt(at(9, 10), "acorn"),
		mtime(at(9, 20), "acorn"),
		taskEvent(at(9, 30), "acorn"),
	})
	// Named first, so the loop below cannot be satisfied by an empty mix.
	if got := mixNames(r); !slices.Equal(got, []string{"Claude", "Files", "Git", "Jobs"}) {
		t.Fatalf("the mix holds %v, want all four streams this corpus carries", got)
	}
	for _, f := range r.Mix.Families {
		if f.Engaged.Total > r.Presence.Total {
			t.Errorf("%s participated in %v against %v of presence", f.Name, f.Engaged.Total,
				r.Presence.Total)
		}
		if f.Solely > f.Engaged.Total {
			t.Errorf("%s alone holds %v of the %v it participated in", f.Name, f.Solely,
				f.Engaged.Total)
		}
	}
}

// The mapping is asked of one place, and a tool that has to print the rule it
// applied can read it back. Two copies of a judgment are two copies to go
// stale, and cmd/timetattle's dashboard already draws these same seven names.
func TestTheMappingIsTheOneEveryReaderShares(t *testing.T) {
	cases := map[string]source.Family{
		"claude-transcripts":  source.FamilyClaude,
		"gitlog-commits":      source.FamilyGit,
		"gitlog-reflog":       source.FamilyGit,
		"job-events":          source.FamilyJobs,
		"job-claims":          source.FamilyJobs,
		"fs-observations":     source.FamilyFiles,
		"motion-autosave":     source.FamilyMotion,
		"blender-backups":     source.FamilyBlender,
		"biome-appusage":      source.FamilyApps,
		"knowledgec-appusage": source.FamilyApps,
		"knowledgec-backlit":  source.FamilyApps,
	}
	for id, want := range cases {
		got, ok := source.FamilyOf(id)
		if !ok {
			t.Errorf("source %q has no family", id)
			continue
		}
		if got != want {
			t.Errorf("source %q is family %s, want %s", id, got, want)
		}
	}
	if _, ok := source.FamilyOf("wibble-widgets"); ok {
		t.Errorf("an id nobody has classified reported a family; an unknown source must be " +
			"reported as unclassified rather than guessed at")
	}
}
