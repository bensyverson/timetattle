package main

import (
	"strings"
	"testing"
)

// WHAT THE STAGED SCAN IS FOR. The leak this gate exists to stop arrives when
// somebody pastes a live capture into a document and commits it, and the
// interval between that landing and a human happening to look has been
// unbounded. A pre-commit hook closes the interval, but only if it judges the
// right bytes and only if it can be lived with: a whole-tree gate that fails
// on findings the commit did not introduce blocks every unrelated commit until
// somebody has time to fix the backlog, and a gate that blocks unrelated work
// gets bypassed with --no-verify and then removed.
//
// So the hook asks a narrower question than the release gate does: WHAT DOES
// THIS COMMIT ADD? The baseline it measures against is the same file's own
// content at HEAD — derived, never a checked-in list of exemptions, so it
// cannot go stale and there is nothing to prune. Findings already at HEAD are
// COUNTED AND PRINTED as carried, never silently dropped: a run that says
// nothing about them would be reporting a clean commit as a clean tree.

// stagedLeak is the fixture line every test here plants. Invented name.
const stagedLeak = "engaged hours for flonk this week\n"

func stagedScanner() Scanner { return Scanner{Names: []string{"flonk"}} }

// The central case: a file that already carried a leak, with another added.
// Only the addition is this commit's doing.
func TestOnlyTheFindingsThisCommitAddsAreReported(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"doc.md": "intro\n" + stagedLeak})
	runGit(t, dir, "add", "doc.md")
	commitGit(t, dir, "first")

	writeTree(t, dir, map[string]string{"doc.md": "intro\n" + stagedLeak + "and again for flonk\n"})
	runGit(t, dir, "add", "doc.md")

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 1 {
		t.Errorf("Added = %v, want the one line this commit introduces", got.Added)
	}
	if len(got.Carried) != 1 {
		t.Errorf("Carried = %v, want the pre-existing line counted and printed, not dropped", got.Carried)
	}
}

// A leak that merely moved is not a new leak. This is why the baseline is the
// line's CONTENT and not its number: a path:line list rots the instant a line
// is inserted above it, which is the same defect the -skip anchors below fix.
func TestALeakThatOnlyMovedIsNotReportedAsAdded(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"doc.md": "intro\n" + stagedLeak})
	runGit(t, dir, "add", "doc.md")
	commitGit(t, dir, "first")

	writeTree(t, dir, map[string]string{"doc.md": "intro\nan inserted line\n" + stagedLeak})
	runGit(t, dir, "add", "doc.md")

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 0 {
		t.Errorf("Added = %v, want none: the same line moved down one", got.Added)
	}
	if len(got.Carried) != 1 {
		t.Errorf("Carried = %v, want the moved line counted as carried", got.Carried)
	}
}

// A second copy of a line that already exists IS an addition. Counting
// signatures rather than merely testing for membership is what makes that
// true.
func TestASecondCopyOfAnExistingLeakIsAdded(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"doc.md": stagedLeak})
	runGit(t, dir, "add", "doc.md")
	commitGit(t, dir, "first")

	writeTree(t, dir, map[string]string{"doc.md": stagedLeak + stagedLeak})
	runGit(t, dir, "add", "doc.md")

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 1 {
		t.Errorf("Added = %v, want the duplicate counted as an addition", got.Added)
	}
}

// A file with no version at HEAD has no baseline at all, so everything in it is
// this commit's doing. This is the shape the actual leak took twice: a NEW
// project/ document holding a pasted capture.
func TestEveryFindingInANewFileIsAdded(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"kept.md": "nothing here\n"})
	runGit(t, dir, "add", "kept.md")
	commitGit(t, dir, "first")

	writeTree(t, dir, map[string]string{"note.md": stagedLeak})
	runGit(t, dir, "add", "note.md")

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 1 {
		t.Fatalf("Added = %v, want the new file's only finding", got.Added)
	}
	if got.Added[0].Path != "note.md" {
		t.Errorf("Added[0].Path = %q, want note.md", got.Added[0].Path)
	}
}

// THE BYTES THAT MATTER ARE THE INDEX'S. A partially staged file differs from
// the working tree, and the commit records the index. A gate that read the
// working tree would wave through a leak that is actually being committed.
func TestTheStagedScanReadsTheIndexAndNotTheWorkingTree(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"doc.md": "nothing here\n"})
	runGit(t, dir, "add", "doc.md")
	commitGit(t, dir, "first")

	// Stage the leak, then clean it out of the working tree.
	writeTree(t, dir, map[string]string{"doc.md": stagedLeak})
	runGit(t, dir, "add", "doc.md")
	writeTree(t, dir, map[string]string{"doc.md": "nothing here\n"})

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 1 {
		t.Errorf("Added = %v, want the staged leak, which is what this commit will record", got.Added)
	}
}

// And the mirror, or the check above would pass on a tool that scanned neither.
func TestALeakOnlyInTheWorkingTreeIsNotThisCommitsDoing(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{"doc.md": "nothing here\n"})
	runGit(t, dir, "add", "doc.md")
	commitGit(t, dir, "first")

	writeTree(t, dir, map[string]string{"doc.md": "still nothing\n"})
	runGit(t, dir, "add", "doc.md")
	writeTree(t, dir, map[string]string{"doc.md": stagedLeak})

	got, err := scanStaged(dir, stagedScanner())
	if err != nil {
		t.Fatalf("scanStaged: %v", err)
	}
	if len(got.Added) != 0 {
		t.Errorf("Added = %v, want none: that line is not staged", got.Added)
	}
}

// --- skip anchors: what happens when a skipped line moves ---------------------
//
// -skip clears one path:line site a human has reviewed — a genuine English use
// of a short client name, which no machine can tell from a real reference. A
// path:line list rots the instant a line is inserted above it, and a rotted
// skip does not merely stop working: it silently exempts a DIFFERENT line, so
// the gate goes on reporting clean while clearing something nobody reviewed.
//
// The fix is an anchor: the site carries a digest of the line that was
// reviewed. A digest, not the text, because the text of a reviewed line is
// usually the sensitive thing itself and -skip is typed into commands and
// pasted into documents. If the digest does not match what is there now, the
// skip is NOT applied and the site is reported, so rot fails closed and says so.

func TestAnAnchoredSkipSuppressesTheLineItWasReviewedOn(t *testing.T) {
	src := "line one names flonk\nline two names flonk\n"
	anchor := lineAnchor("line one names flonk")
	sc := Scanner{
		Names:  []string{"flonk"},
		Skip:   map[Site]bool{{"a.md", 1}: true},
		Anchor: map[Site]string{{"a.md", 1}: anchor},
	}
	if got := clientHits(scanText("a.md", src, sc)); len(got) != 1 {
		t.Fatalf("scanText found %v (%d), want 1: line 1 is cleared by an anchor that still matches", got, len(got))
	}
}

func TestAnAnchoredSkipStopsSuppressingWhenTheLineMoves(t *testing.T) {
	anchor := lineAnchor("line one names flonk")
	sc := Scanner{
		Names:  []string{"flonk"},
		Skip:   map[Site]bool{{"a.md", 1}: true},
		Anchor: map[Site]string{{"a.md", 1}: anchor},
	}
	// A line has been inserted above, so site 1 is now a different line.
	src := "inserted above, also naming flonk\nline one names flonk\n"

	got := scanText("a.md", src, sc)
	if n := len(clientHits(got)); n != 2 {
		t.Errorf("scanText cleared %d of 2 client findings; a rotted skip must exempt nothing", 2-n)
	}
	stale := 0
	for _, f := range got {
		if f.Kind == KindSkip {
			stale++
		}
	}
	if stale != 1 {
		t.Errorf("scanText reported %d stale-skip findings, want 1 naming the site that rotted", stale)
	}
}

// The report of a rotted skip must carry the anchor to use, and must not carry
// the line: -skip strings get pasted into commands and documents.
func TestAStaleSkipReportsTheNewAnchorAndNotTheLine(t *testing.T) {
	sc := Scanner{
		Names:  []string{"flonk"},
		Skip:   map[Site]bool{{"a.md", 1}: true},
		Anchor: map[Site]string{{"a.md", 1}: "0000000000000000"},
	}
	got := scanText("a.md", "line one names flonk\n", sc)
	for _, f := range got {
		if f.Kind != KindSkip {
			continue
		}
		if !strings.Contains(f.Match, lineAnchor("line one names flonk")) {
			t.Errorf("stale-skip finding %q does not carry the anchor that would clear the site now", f.Match)
		}
		if strings.Contains(f.Match+f.Text, "flonk") {
			t.Errorf("stale-skip finding echoes the line: %q / %q", f.Match, f.Text)
		}
		return
	}
	t.Error("scanText reported no stale-skip finding for an anchor that does not match")
}

func TestParseSkipSitesReadsAnAnchoredSite(t *testing.T) {
	sites := parseSkipSites([]string{"a.md:142:abcdef0123456789"})
	if !sites[Site{"a.md", 142}] {
		t.Errorf("parseSkipSites = %v, want the anchored site", sites)
	}
	anchors := parseSkipAnchors([]string{"a.md:142:abcdef0123456789"})
	if anchors[Site{"a.md", 142}] != "abcdef0123456789" {
		t.Errorf("parseSkipAnchors = %v, want the anchor recorded", anchors)
	}
}

// An unanchored skip is the shape that rots. It still works for an exploratory
// run, where a human is reading the output anyway — but a GATE run must refuse
// it, because a gate's whole output is one exit code and a rotted exemption in
// it is invisible.
func TestTheGateRefusesAnUnanchoredSkip(t *testing.T) {
	sites := parseSkipSites([]string{"a.md:1", "b.md:2:abcdef0123456789"})
	anchors := parseSkipAnchors([]string{"a.md:1", "b.md:2:abcdef0123456789"})

	got := unanchoredSkips(sites, anchors)

	if len(got) != 1 || got[0] != (Site{"a.md", 1}) {
		t.Errorf("unanchoredSkips = %v, want exactly the site with no anchor", got)
	}
}
