package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
		{"/Users/ben/git/acme", "/Users/ben/git/kestrel"},
		{"-Users-ben-git-acme", "-Users-ben-git-kestrel"},
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
