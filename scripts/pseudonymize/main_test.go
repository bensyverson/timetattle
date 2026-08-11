package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every fixture here uses invented names, for the same reason scripts/confidential
// does: a rewriter that carried the real symbols in its own source could never be
// published alongside the repository it cleaned.

func rewrite(t *testing.T, src string, pairs ...string) string {
	t.Helper()
	m, err := parseMap(pairs)
	if err != nil {
		t.Fatalf("parseMap(%v) = %v", pairs, err)
	}
	out, _ := rewriteLine(src, m)
	return out
}

// --- case preservation --------------------------------------------------------

func TestBareNamePreservesCase(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"acme", "kestrel"},
		{"ACME", "KESTREL"},
		{"Acme", "Kestrel"},
	} {
		if got := rewrite(t, tc.in, "acme=kestrel"); got != tc.want {
			t.Errorf("rewriteLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The compounds a `\bname\b` pass misses entirely. These are the ones that make a
// plain word-boundary sed leave client references behind.
func TestCompoundIdentifiersAreRewritten(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"realAcme", "realKestrel"},
		{"acmeHours", "kestrelHours"},
		{"tACME", "tKESTREL"},
		{"acme_hours", "kestrel_hours"},
		{"ACME_ROOT", "KESTREL_ROOT"},
		{"TestGlobexDoesNotSwallowTheConcurrentDay", "TestBorealisDoesNotSwallowTheConcurrentDay"},
	} {
		if got := rewrite(t, tc.in, "acme=kestrel", "globex=borealis"); got != tc.want {
			t.Errorf("rewriteLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The whole reason this is not a substring replace. A short client symbol is
// routinely a substring of ordinary English; corrupting prose is the failure
// mode that matters.
func TestEnglishWordsContainingANameAreUntouched(t *testing.T) {
	src := "the catalog and category concatenate cleanly; a cat is not a catalog"
	want := "the catalog and category concatenate cleanly; a kestrel is not a catalog"
	if got := rewrite(t, src, "cat=kestrel"); got != want {
		t.Errorf("rewriteLine(%q) = %q, want %q", src, got, want)
	}
}

// An all-uppercase name followed by more uppercase is one word, not a compound:
// CATALOG must survive a rename of CAT. This is the mirror of the camelCase rule
// and the case a naive "uppercase next char is a boundary" rule gets wrong.
func TestUppercaseRunIsNotACompoundBoundary(t *testing.T) {
	if got := rewrite(t, "CATALOG CAT", "cat=kestrel"); got != "CATALOG KESTREL" {
		t.Errorf("rewriteLine = %q, want %q", got, "CATALOG KESTREL")
	}
}

// --- paths --------------------------------------------------------------------

func TestPathFormsAreRewritten(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/Users/you/git/acme", "/Users/you/git/kestrel"},
		{"-Users-you-git-acme", "-Users-you-git-kestrel"},
		{"~/git/acme/blender", "~/git/kestrel/blender"},
		{`"ACME"`, `"KESTREL"`},
	} {
		if got := rewrite(t, tc.in, "acme=kestrel"); got != tc.want {
			t.Errorf("rewriteLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- regex escapes ------------------------------------------------------------

// The project/ documents quote the audit probe itself, as `\bacme\b`. The `b` of
// an escape sequence is not an identifier character, so the name beside it is
// still a bare client reference. scripts/confidential's component matcher does NOT
// see these -- it tokenises \bacme\b as "bacme" -- so a rewriter that shares that
// blind spot would leave the name standing while the gate reported clean.
func TestNameInsideARegexEscapeIsRewritten(t *testing.T) {
	src := `grep -rio '\bacme\b' .`
	want := `grep -rio '\bkestrel\b' .`
	if got := rewrite(t, src, "acme=kestrel"); got != want {
		t.Errorf("rewriteLine(%q) = %q, want %q", src, got, want)
	}
}

// --- mixed case is a human's problem, not a machine's -------------------------

// "AcMe" has no defensible target casing. Guessing silently is how a rename pass
// corrupts something nobody checks. Report it and rewrite nothing.
func TestUnhandledCasingIsReportedAndNotRewritten(t *testing.T) {
	out, notes := rewriteLine("AcMe", map[string]string{"acme": "kestrel"})
	if out != "AcMe" {
		t.Errorf("rewriteLine(%q) = %q, want it left alone", "AcMe", out)
	}
	if len(notes) != 1 {
		t.Errorf("rewriteLine reported %v, want one unhandled-casing note", notes)
	}
}

// --- fixed-width columns ------------------------------------------------------
//
// THE DEFECT THIS SECTION PINS. A rename of two names of different lengths ran
// over a document holding a fixed-width ASCII chart with a 17-character label
// column. Every substitution was individually correct, so the tool reported
// "0 site(s) need a human" -- and every glyph row after the label had shifted
// against the hour ruler above it. Column-aligned output is ordinary in this
// repository's documents, so a rewriter that changes a field's width silently
// corrupts a chart nobody rechecks.
//
// The rule: when the line is column-aligned, absorb the difference into the
// padding of the field that changed, so every following column keeps its
// offset. When the padding cannot absorb it, rewrite nothing on that line and
// report the site -- the same refusal the mixed-case rule already makes.

// columnStarts is where each column of a fixed-width line begins: the offset of
// every non-space that follows a run of two or more spaces. It is what must not
// move.
func columnStarts(s string) []int {
	var out []int
	run := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			run++
			continue
		}
		if run >= 2 {
			out = append(out, i)
		}
		run = 0
	}
	return out
}

func TestALongerReplacementKeepsAColumnAlignedLinesColumns(t *testing.T) {
	src := "flonk             ██████ 4.2h"
	got := rewrite(t, src, "flonk=kestrelbird")
	if !strings.Contains(got, "kestrelbird") {
		t.Fatalf("rewriteLine(%q) = %q, want the name replaced", src, got)
	}
	if want, have := columnStarts(src), columnStarts(got); !slices.Equal(want, have) {
		t.Errorf("columns moved from %v to %v:\n  before %q\n  after  %q", want, have, src, got)
	}
}

func TestAShorterReplacementKeepsAColumnAlignedLinesColumns(t *testing.T) {
	src := "kestrelbird       ██████ 4.2h"
	got := rewrite(t, src, "kestrelbird=flonk")
	if !strings.Contains(got, "flonk") {
		t.Fatalf("rewriteLine(%q) = %q, want the name replaced", src, got)
	}
	if want, have := columnStarts(src), columnStarts(got); !slices.Equal(want, have) {
		t.Errorf("columns moved from %v to %v:\n  before %q\n  after  %q", want, have, src, got)
	}
}

// Padding that cannot absorb the difference has no defensible answer: shrinking
// it to nothing would run two fields together. Refuse, exactly as the mixed-case
// rule does, and say which site needs a human.
func TestAColumnThatCannotAbsorbTheDifferenceIsLeftForAHuman(t *testing.T) {
	src := "flonk  ██████ 4.2h"
	out, notes := rewriteLine(src, map[string]string{"flonk": "kestrelbird"})
	if out != src {
		t.Errorf("rewriteLine(%q) = %q, want the line left exactly as it was", src, out)
	}
	if len(notes) != 1 {
		t.Errorf("rewriteLine reported %v, want one note naming the site a human must fix", notes)
	}
}

// The counterweight: prose is not a chart, and a rename that changes a
// sentence's length is simply correct. Reporting every one of those would bury
// the chart rows that genuinely need a human.
func TestProseIsRewrittenEvenThoughItsWidthChanges(t *testing.T) {
	src := "the flonk report is late"
	out, notes := rewriteLine(src, map[string]string{"flonk": "kestrelbird"})
	if out != "the kestrelbird report is late" {
		t.Errorf("rewriteLine(%q) = %q, want the ordinary rewrite", src, out)
	}
	if len(notes) != 0 {
		t.Errorf("rewriteLine reported %v, want no notes for prose", notes)
	}
}

// A right-aligned field pads on the LEFT, so that is where the difference has to
// be absorbed. Same rule, other side.
func TestARightAlignedFieldAbsorbsFromItsLeadingPadding(t *testing.T) {
	src := "hours       flonk|"
	got := rewrite(t, src, "flonk=kes")
	if !strings.Contains(got, "kes|") {
		t.Fatalf("rewriteLine(%q) = %q, want the name replaced", src, got)
	}
	if len(got) != len(src) {
		t.Errorf("rewriteLine(%q) = %q, width %d, want the field width preserved at %d", src, got, len(got), len(src))
	}
}

// Two trailing spaces at the end of a Markdown line are a hard line break, not a
// column: there is no following field whose offset could move, and eating them
// would change how the document renders.
func TestTrailingMarkdownBreakIsNotTreatedAsAColumn(t *testing.T) {
	src := "the flonk report  "
	out, notes := rewriteLine(src, map[string]string{"flonk": "kestrelbird"})
	if out != "the kestrelbird report  " {
		t.Errorf("rewriteLine(%q) = %q, want the trailing break kept", src, out)
	}
	if len(notes) != 0 {
		t.Errorf("rewriteLine reported %v, want no notes", notes)
	}
}

// --- counting -----------------------------------------------------------------

func TestReplacementsAreCounted(t *testing.T) {
	_, notes := rewriteLine("acme and ACME and realAcme", map[string]string{"acme": "kestrel"})
	if len(notes) != 0 {
		t.Fatalf("unexpected notes %v", notes)
	}
	if n := countMatches("acme and ACME and realAcme", map[string]string{"acme": "kestrel"}); n != 3 {
		t.Errorf("countMatches = %d, want 3", n)
	}
}

func TestParseMapRejectsMalformedPairs(t *testing.T) {
	if _, err := parseMap([]string{"acme"}); err == nil {
		t.Error("parseMap(\"acme\") succeeded, want an error for a missing replacement")
	}
}

// --- git invocation must not inherit a hook's repository -----------------------
//
// git commit exports GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE into every hook,
// and a hook that runs the test suite passes them to every child. The -C flag
// changes only the working directory, so an unscrubbed `git -C dir` still acts on
// the exported repository instead. That is not hypothetical: it clobbered this
// repository's .gitignore twice on 2026-08-02 before it was root-caused, and the
// identical leak made scripts/confidential scan zero files while reporting
// success. A rewriter reading the wrong file list is worse than an auditor doing
// it, because it goes on to edit what it found.

func TestTrackedFilesDoesNotInheritAnExportedGitDir(t *testing.T) {
	target := initRepoWithFile(t, "kept.md")
	decoy := initRepoWithFile(t, "decoy.md")

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_WORK_TREE", decoy)

	got, err := trackedFiles(target)
	if err != nil {
		t.Fatalf("trackedFiles(%s) = %v", target, err)
	}
	if len(got) != 1 || got[0] != "kept.md" {
		t.Errorf("trackedFiles read %v; an exported GIT_DIR redirected it away from %s", got, target)
	}
}

func initRepoWithFile(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "t"}, {"add", name},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = withoutGitRepoEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}
