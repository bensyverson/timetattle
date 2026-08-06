package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every fixture here uses invented names and invented figures. The tool must not
// carry the real client symbols in its own source, or it could never report
// itself clean, and publishing the auditor would publish the thing it audits.

func clientHits(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		if f.Kind == KindClient {
			out = append(out, f.Match)
		}
	}
	return out
}

func kindHits(fs []Finding, k string) int {
	n := 0
	for _, f := range fs {
		if f.Kind == k {
			n++
		}
	}
	return n
}

// The property the whole scripted pass rests on: a short client name that is also
// a substring of ordinary English must not match the English. A blind substring
// replace corrupts prose and code.
func TestEnglishWordsContainingANameAreNotClientReferences(t *testing.T) {
	src := "the catalog and category concatenate cleanly"
	got := clientHits(scanText("a.md", src, Scanner{Names: []string{"cat"}}))
	if len(got) != 0 {
		t.Errorf("scanText matched %v in %q, want none: catalog/category/concatenate are English", got, src)
	}
}

func TestExactNameIsAClientReference(t *testing.T) {
	got := clientHits(scanText("a.md", "the cat repository", Scanner{Names: []string{"cat"}}))
	if len(got) != 1 {
		t.Errorf("scanText found %v, want the bare name to match once", got)
	}
}

// A word-boundary grep MISSES these, which is the dangerous direction: they are
// real client references that a `\bname\b` measurement never counts.
func TestCompoundIdentifiersAreClientReferences(t *testing.T) {
	src := "realAcme(t) returned acmeHours; see TestGlobex for the pairing."
	got := clientHits(scanText("a_test.go", src, Scanner{Names: []string{"acme", "globex"}}))
	if len(got) != 3 {
		t.Errorf("scanText found %v (%d), want 3: realAcme, acmeHours, TestGlobex", got, len(got))
	}
}

func TestUppercaseAndPathFormsAreClientReferences(t *testing.T) {
	src := `register -client "ACME" -path ~/git/acme/blender`
	got := clientHits(scanText("registry.md", src, Scanner{Names: []string{"acme"}}))
	if len(got) != 2 {
		t.Errorf("scanText found %v (%d), want 2: the quoted ACME and the path component", got, len(got))
	}
}

func TestUnderscoreComponentsAreClientReferences(t *testing.T) {
	got := clientHits(scanText("x.sql", "acme_hours ACME_ROOT", Scanner{Names: []string{"acme"}}))
	if len(got) != 2 {
		t.Errorf("scanText found %v, want both underscore-separated forms", got)
	}
}

func TestNoNamesConfiguredReportsNoClients(t *testing.T) {
	if got := clientHits(scanText("a.md", "acme and globex", Scanner{})); len(got) != 0 {
		t.Errorf("scanText found %v with no names configured, want none", got)
	}
}

// Contract figures are more sensitive than a name and the criterion is that none
// appear at all, so the tool has to see both a lump sum and an hourly rate.
func TestContractFiguresAreFlagged(t *testing.T) {
	src := "Contract: $1,234 fixed price, expected 40 hours, nominal $99/hr." // gate:fixture
	if n := kindHits(scanText("g.md", src, Scanner{}), KindRate); n < 2 {
		t.Errorf("scanText found %d rate findings in %q, want at least 2", n, src)
	}
}

func TestSecretAssignmentsAreFlagged(t *testing.T) {
	src := "EXAMPLE_ACCESS_TOKEN=abcdefghijklmnopqrstuvwxyz" // gate:fixture
	if n := kindHits(scanText("commit-message", src, Scanner{}), KindSecret); n != 1 {
		t.Errorf("scanText found %d secret findings in a token assignment, want 1", n)
	}
}

// Naming a variable without a value is documentation, not a leak: CLAUDE.md
// narrates this very incident by name on purpose.
func TestSecretNameWithoutValueIsNotFlagged(t *testing.T) {
	src := "the commit body included `EXAMPLE_ACCESS_TOKEN` with its value"
	if n := kindHits(scanText("CLAUDE.md", src, Scanner{}), KindSecret); n != 0 {
		t.Errorf("scanText reported %d secrets for a prose mention with no value, want 0", n)
	}
}

// A secret value must never be echoed into the tool's own output.
func TestSecretFindingDoesNotEchoTheValue(t *testing.T) {
	fs := scanText("c", "EXAMPLE_ACCESS_TOKEN=supersecretvalue123", Scanner{}) // gate:fixture
	for _, f := range fs {
		if f.Kind == KindSecret && (f.Text == "EXAMPLE_ACCESS_TOKEN=supersecretvalue123" || f.Match == "supersecretvalue123") { // gate:fixture
			t.Errorf("finding %+v echoes the secret value", f)
		}
	}
}

func TestFindingCarriesLineNumber(t *testing.T) {
	src := "clean line\nanother clean line\nthis one names acme\n"
	fs := scanText("x.md", src, Scanner{Names: []string{"acme"}})
	if len(fs) != 1 || fs[0].Line != 3 {
		t.Fatalf("scanText = %+v, want a single finding on line 3", fs)
	}
}

// --- Developer and platform identifiers ---------------------------------------
//
// This class was missing from the original surface list. A team ID is not a
// client name, so hunting client symbols would never have found it.

// Shape alone is hopeless: the working tree holds 78 distinct 10-character
// upper-alphanumeric tokens, essentially all SQL columns and Go constants. A
// detector keyed on shape would drown the one real finding.
func TestTenCharTokensAreNotIdentitiesOnTheirOwn(t *testing.T) {
	src := "REFERENCES ZSTARTDATE ACTIONABLE TIOCGWINSZ"
	if n := kindHits(scanText("q.sql", src, Scanner{}), KindIdentity); n != 0 {
		t.Errorf("scanText reported %d identities for ordinary uppercase tokens, want 0", n)
	}
}

// A known-sensitive literal, supplied from outside the repo, must be found
// anywhere -- including where it wraps onto a line away from its label.
func TestDenyLiteralIsFlaggedAnywhere(t *testing.T) {
	sc := Scanner{Deny: []string{"AB1234CDEF"}}
	src := "anchored to certificate leaf[subject.OU] =\nAB1234CDEF, not to a cdhash."
	if n := kindHits(scanText("d.md", src, sc), KindIdentity); n != 1 {
		t.Errorf("scanText found %d identity findings for a denied literal, want 1", n)
	}
}

func TestDenyFindingDoesNotEchoTheValue(t *testing.T) {
	sc := Scanner{Deny: []string{"AB1234CDEF"}}
	for _, f := range scanText("d.md", "team AB1234CDEF here", sc) {
		if f.Kind == KindIdentity && (f.Match == "AB1234CDEF" || f.Text == "team AB1234CDEF here") {
			t.Errorf("finding %+v echoes the denied value", f)
		}
	}
}

// The real leak reads "Developer ID Application: Syverson\n   Industries LLC // gate:fixture
// (TEAMID)". The certificate common name is itself identifying, and it wraps, so
// the label alone has to be enough to flag the line.
func TestDeveloperIDCertificateNameIsFlagged(t *testing.T) {
	src := "signed with **Developer ID Application: Acme Industries LLC" // gate:fixture
	if n := kindHits(scanText("n.md", src, Scanner{}), KindIdentity); n != 1 {
		t.Errorf("scanText found %d identity findings for a Developer ID common name, want 1", n)
	}
}

// scripts/sign.sh already documents the placeholder form. The gate must stay
// quiet about it, or the release path can never be green.
func TestPlaceholderDeveloperIDIsNotFlagged(t *testing.T) {
	src := `CERT="Developer ID Application: Someone (TEAMID)"`
	if n := kindHits(scanText("sign.sh", src, Scanner{}), KindIdentity); n != 0 {
		t.Errorf("scanText found %d identity findings for the documented placeholder, want 0", n)
	}
}

// --- Defect 1: a name inside a regex escape -----------------------------------
//
// The confidentiality pass (task 5nCl4) quotes the audit probe itself as
// `\bacme\b`. A naive identifier scan tokenises that as "bacme" -- the 'b' of
// the escape merges into the name -- and no finding is ever emitted. The gate
// would report the tree clean with the client symbol still printed on the
// page. This is the serious defect: not a false alarm, a false all-clear.

func TestNameInsideARegexEscapeIsAClientReference(t *testing.T) {
	src := `reproduce with: grep -rio '\bacme\b' --exclude-dir=.git .`
	got := clientHits(scanText("probe.md", src, Scanner{Names: []string{"acme"}}))
	if len(got) != 1 {
		t.Errorf("scanText found %v (%d) in %q, want 1: acme inside \\b...\\b", got, len(got), src)
	}
}

// The compound case, not just the bare one: a client name that is itself
// camelCase-compounded, sitting inside a regex escape.
func TestCompoundIdentifierInsideARegexEscapeIsAClientReference(t *testing.T) {
	src := `probe: '\bacmeHours\b' and '\bTestAcme\b'`
	got := clientHits(scanText("probe.md", src, Scanner{Names: []string{"acme"}}))
	if len(got) != 2 {
		t.Errorf("scanText found %v (%d), want 2: acmeHours and TestAcme, each inside a regex escape", got, len(got))
	}
}

// --- Defect 3a: a bare single-digit dollar amount is a shell parameter, not a
// rate -----------------------------------------------------------------------
//
// $0, $1, $2 are positional parameters and appear throughout scripts/*.sh.
// This project's real contract figures are never a single bare digit -- at
// least fifty dollars an hour, or a four-figure lump sum -- so filtering the
// shell-parameter shape costs nothing real.
func TestBareDigitDollarShellParameterIsNotFlagged(t *testing.T) {
	src := `if [ -z "$1" ]; then echo "$0 needs an argument, got $2"; fi`
	if n := kindHits(scanText("release.sh", src, Scanner{}), KindRate); n != 0 {
		t.Errorf("scanText found %d rate findings in %q, want 0: these are shell positional parameters", n, src)
	}
}

// A genuine rate must still be caught even though it is short: this is a
// precision fix, not a blanket exemption for small dollar amounts.
func TestShortButGenuineDollarAmountIsStillFlagged(t *testing.T) {
	src := "nominal $99/hr, or $50 flat" // gate:fixture
	if n := kindHits(scanText("g.md", src, Scanner{}), KindRate); n < 2 {
		t.Errorf("scanText found %d rate findings in %q, want at least 2 (a lump sum and an hourly rate)", n, src) // gate:fixture
	}
}

// --- Defect 3b: the gate must be able to pass on its own repository ----------
//
// scripts/confidential/main_test.go's fixtures are deliberately rate-, secret-
// and identity-shaped, to pin the very patterns the gate hunts for. "Skip
// anything under scripts/confidential" would hide a real leak pasted anywhere
// in that directory. The exemption here is narrower on two axes at once: it
// names exactly one file, and even within that file it only silences a line
// that carries an explicit, visible marker -- so an unmarked line in the very
// same file is still audited.
func TestSelfFixtureMarkerSuppressesOnlyTheMarkedLineInTheOneExemptFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"scripts/confidential/main_test.go": "src := \"EXAMPLE_ACCESS_TOKEN=abcdefghijklmnopqrstuvwxyz\" // " + fixtureMarker + "\n" + // gate:fixture
			"src2 := \"EXAMPLE_ACCESS_TOKEN=zzzzzzzzzzzzzzzzzzzzzzzz\"\n", // gate:fixture
	})
	findings, files := scanTree(dir, Scanner{})
	if files != 1 {
		t.Fatalf("scanTree scanned %d files, want 1", files)
	}
	if n := kindHits(findings, KindSecret); n != 1 {
		t.Fatalf("scanTree found %d secret findings, want 1: the marked line must be silenced, the unmarked one must not", n)
	}
	if findings[0].Line != 2 {
		t.Errorf("the surviving finding is on line %d, want line 2 (the unmarked one)", findings[0].Line)
	}
}

// The same marker, on the same-shaped line, in a DIFFERENT file, must not be
// silenced. If it were, the marker would be a repo-wide bypass comment rather
// than an exemption scoped to the one file that legitimately needs it.
func TestSelfFixtureMarkerDoesNothingOutsideTheExemptFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"scripts/confidential/other.go": "src := \"EXAMPLE_ACCESS_TOKEN=abcdefghijklmnopqrstuvwxyz\" // " + fixtureMarker + "\n", // gate:fixture
	})
	findings, _ := scanTree(dir, Scanner{})
	if n := kindHits(findings, KindSecret); n != 1 {
		t.Errorf("scanTree found %d secret findings in a non-exempt file carrying the marker, want 1: the marker must not work here", n)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", abs, err)
		}
	}
}

// --- Defect 2: the gate must audit what git would publish, not what happens
// to be sitting on disk --------------------------------------------------------
//
// -dir . used to walk the whole filesystem tree, including .gitignore'd
// directories. On a machine that has run an agent, .claude/worktrees/ holds
// stale copies from every worktree ever created -- none of it tracked, none
// of it publishable -- and the gate could never pass there. It must instead
// default to what `git ls-files` would report: tracked files, plus untracked
// files that are NOT gitignored (the "about to be git add'ed" case, so a new
// file with a leak is not invisible just because nobody has staged it yet).

func TestScanTreeIgnoresGitignoredFiles(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{
		".gitignore":      "ignored/\n",
		"ignored/leak.md": "client fixturewombat leaked here\n",
		"tracked.md":      "nothing sensitive here\n",
	})
	runGit(t, dir, "add", "tracked.md", ".gitignore")

	findings, files := scanTree(dir, Scanner{Names: []string{"fixturewombat"}})
	if files != 2 {
		t.Fatalf("scanTree scanned %d files, want 2 (tracked.md and .gitignore), not the ignored one: %+v", files, findings)
	}
	if n := kindHits(findings, KindClient); n != 0 {
		t.Errorf("scanTree found %d client findings, want 0: the only occurrence is under a gitignored directory", n)
	}
}

func TestScanTreeIncludesUntrackedFilesThatAreNotIgnored(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{
		"brand-new.md": "not yet git add'ed, mentions fixturewombat\n",
	})
	// deliberately no `git add`: this is the "about to be added" case

	findings, files := scanTree(dir, Scanner{Names: []string{"fixturewombat"}})
	if files != 1 {
		t.Fatalf("scanTree scanned %d files, want 1", files)
	}
	if n := kindHits(findings, KindClient); n != 1 {
		t.Errorf("scanTree found %d client findings, want 1: an untracked-but-unignored file must still be audited", n)
	}
}

func TestScanTreeIncludesTrackedFiles(t *testing.T) {
	dir := initGitDir(t)
	writeTree(t, dir, map[string]string{
		"tracked.md": "mentions fixturewombat\n",
	})
	runGit(t, dir, "add", "tracked.md")

	findings, _ := scanTree(dir, Scanner{Names: []string{"fixturewombat"}})
	if n := kindHits(findings, KindClient); n != 1 {
		t.Errorf("scanTree found %d client findings for a tracked file, want 1", n)
	}
}

// A plain directory that is not itself a git worktree -- the shape the scrub
// used, exporting `git ls-files` into a fresh directory and auditing that --
// must still work by falling back to a full walk.
func TestScanTreeFallsBackToARawWalkOutsideAGitWorktree(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"export/note.md": "mentions fixturewombat\n",
	})

	findings, files := scanTree(dir, Scanner{Names: []string{"fixturewombat"}})
	if files != 1 {
		t.Fatalf("scanTree scanned %d files, want 1", files)
	}
	if n := kindHits(findings, KindClient); n != 1 {
		t.Errorf("scanTree found %d client findings outside a git worktree, want 1 (raw-walk fallback)", n)
	}
}

// --- GIT_DIR must not leak into the git commands this tool shells out to ----
//
// git exports GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE and friends into every
// hook and subprocess it runs, naming the repository that invoked it. Those
// variables silently override -C. This project's own pre-commit hook is a
// real example: its "go test" step is a subprocess of `git commit`, which set
// exactly these variables -- and that is exactly how a pre-publication gate
// WOULD be run, as a step in some CI job or hook. Without stripping them,
// `git -C root ls-files` resolves against whichever repository set GIT_DIR,
// not root, and the gate silently audits the wrong tree: a false all-clear,
// the same failure shape as Defect 1, just with the whole repository as the
// blind spot instead of one line.
func TestScanTreeDoesNotLeakGitDirIntoTheGitCommand(t *testing.T) {
	decoy := initGitDir(t)
	writeTree(t, decoy, map[string]string{"decoy.md": "mentions fixturewombat\n"})
	runGit(t, decoy, "add", "decoy.md")

	target := initGitDir(t)
	writeTree(t, target, map[string]string{"target.md": "nothing sensitive here\n"})
	runGit(t, target, "add", "target.md")

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	findings, files := scanTree(target, Scanner{Names: []string{"fixturewombat"}})
	if files != 1 {
		t.Fatalf("scanTree scanned %d files with a leaked GIT_DIR, want 1 (target.md); "+
			"a leaked GIT_DIR pointed the gate at the decoy repo instead of -dir", files)
	}
	if n := kindHits(findings, KindClient); n != 0 {
		t.Fatalf("scanTree found %d client findings, want 0: it should never have seen decoy's content "+
			"(a leaked GIT_DIR would make it audit decoy's tree instead of target's)", n)
	}
}

// runGit itself must not leak either: it is the fixture machinery every test
// above depends on, and this is the actual bug that corrupted this project's
// own repository twice today. With GIT_DIR pointing at a decoy repo, `git -C
// dir init` silently reinitializes the DECOY's .git in place rather than
// creating dir/.git, so dir never becomes the isolated repository a test
// thinks it is -- every subsequent fixture write in that test lands in
// whatever real repository GIT_DIR named.
func TestRunGitDoesNotLeakIntoADecoyRepo(t *testing.T) {
	decoy := initGitDir(t)

	dir := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	runGit(t, dir, "init", "-q")

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("dir has no .git after `git -C dir init`: %v -- "+
			"a leaked GIT_DIR pointed init at the decoy repo instead of dir", err)
	}
}

func initGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	return dir
}

// testGitRepoEnvPrefixes is an INDEPENDENT copy of internal/gitenv.Prefixes
// -- duplicated rather than imported so this fixture helper strips them
// independently of gitCommand, which now delegates to internal/gitenv. See
// TestScanTreeDoesNotLeakGitDirIntoTheGitCommand, which relies on scanTree
// doing its OWN stripping, not this helper's.
var testGitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

// runGit is the test-side git runner, building the fixtures these tests
// exercise scanTree against. It strips GIT_DIR and friends itself: this
// suite runs as part of this project's own pre-commit hook, whose "go test"
// step inherits a real GIT_DIR naming the outer repository from the `git
// commit` that spawned it, and without stripping it here every `git
// init`/`git add` fixture below is silently redirected into that repository
// instead of the fresh temp directory it names -- which is exactly what
// corrupted this repository's own .gitignore and added a stray tracked.md
// twice before this was found.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		blocked := false
		for _, prefix := range testGitRepoEnvPrefixes {
			if strings.HasPrefix(kv, prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// --- -skip: a human-reviewed line, cleared by exact position -----------------
//
// A word-boundary scan cannot tell a genuine standalone English use of a short
// client name from an actual client reference -- the doc comment says so, and
// this repo has a real, standing example of exactly that ambiguity in project/
// (a past-tense verb that happens to spell one of the client symbols). Nothing
// in this tool can resolve that automatically without also being able to wave
// away a real leak, so a human has to adjudicate it -- and scripts/pseudonymize
// already established the shape that lets a human do that: -skip path:line,
// checked into no file, passed explicitly at run time, so clearing a line is
// as visible and as deliberate as the command that runs the gate.

func TestSkipSuppressesFindingsOnlyOnTheGivenLine(t *testing.T) {
	src := "line one names flonk\nline two names flonk\n"
	sc := Scanner{Names: []string{"flonk"}, Skip: map[Site]bool{{"a.md", 1}: true}}
	got := clientHits(scanText("a.md", src, sc))
	if len(got) != 1 {
		t.Fatalf("scanText found %v (%d), want 1: line 1 is skipped, line 2 is not", got, len(got))
	}
}

// The skip is keyed on path AND line: the same line number in a different
// file must not be silenced by it.
func TestSkipIsScopedToItsExactFile(t *testing.T) {
	src := "line one names flonk\n"
	sc := Scanner{Names: []string{"flonk"}, Skip: map[Site]bool{{"other.md", 1}: true}}
	got := clientHits(scanText("a.md", src, sc))
	if len(got) != 1 {
		t.Errorf("scanText found %v (%d), want 1: the skip names a different file", got, len(got))
	}
}

func TestParseSkipSitesBuildsPathLinePairs(t *testing.T) {
	got := parseSkipSites([]string{"a.md:142", "b/c.md:7"})
	want := map[Site]bool{{"a.md", 142}: true, {"b/c.md", 7}: true}
	if len(got) != len(want) {
		t.Fatalf("parseSkipSites = %v, want %v", got, want)
	}
	for s := range want {
		if !got[s] {
			t.Errorf("parseSkipSites(%v) missing %v", got, s)
		}
	}
}

// A malformed entry (no line number, or not a number) is dropped rather than
// crashing the gate -- mirrors scripts/pseudonymize's parseSkips.
func TestParseSkipSitesIgnoresMalformedEntries(t *testing.T) {
	got := parseSkipSites([]string{"no-colon-here", "a.md:notanumber", "a.md:5"})
	if len(got) != 1 || !got[Site{"a.md", 5}] {
		t.Errorf("parseSkipSites = %v, want only a.md:5", got)
	}
}
