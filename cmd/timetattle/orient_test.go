package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// seedOrient writes one point event per observation at each locator, plus
// noPath observations carrying no locator at all, and deliberately does NOT run
// the attribution pass. An unresolved store is the state orient exists to
// describe, so seeding a resolved one would test the wrong thing.
func seedOrient(t *testing.T, locators map[string]int, noPath int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	var (
		events []source.Event
		n      int
	)
	// Sorted so the events are seeded in a deterministic order; map iteration
	// order would otherwise change which instant each locator landed on.
	for _, locator := range orientSortedLocators(locators) {
		for range locators[locator] {
			e := source.NewPoint(base.Add(time.Duration(n)*time.Second),
				source.Identity{Document: "notes.md"}, "claude-transcripts")
			e.Locator = locator
			events = append(events, e)
			n++
		}
	}
	for range noPath {
		events = append(events, source.NewInterval(
			base.Add(time.Duration(n)*time.Second), base.Add(time.Duration(n)*time.Second+time.Minute),
			source.Identity{App: "com.googlecode.iterm2"}, "biome-appusage"))
		n++
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
	return path
}

func orientSortedLocators(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// A tiny insertion sort keeps this file free of a dependency on how any
	// other helper happens to order things.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// registerOrientDir blesses dir and records it in indexPath, which is what
// harvest reads and therefore what orient must read too.
func registerOrientDir(t *testing.T, indexPath, dir, project string) {
	t.Helper()
	if _, err := registry.Register(registry.Registration{
		Dir: dir, Client: project, Project: project,
	}); err != nil {
		t.Fatalf("register %s: %v", dir, err)
	}
	if err := registry.RecordInIndex(indexPath, dir); err != nil {
		t.Fatalf("record %s in the index: %v", dir, err)
	}
}

func runOrient(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := orientTo(&out, args); err != nil {
		t.Fatalf("orient %v: %v", args, err)
	}
	return out.String()
}

// D2Q. The whole point of this command is that an agent with no memory of the
// last session can run it cold and be told what to do, so the test runs it the
// way an agent would: no arguments beyond the two paths that isolate it.
func TestOrientRunColdNamesTheNextActionAndWhatItIsWorth(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	// Registered, but never resolved: exactly the live store's condition on
	// 2026-08-01, where 620,643 observations sat inside a registered directory
	// and were attributed to nothing because no harvest had run since.
	path := seedOrient(t, map[string]int{dir: 7}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "NEXT") {
		t.Errorf("orient does not name a next action:\n%s", out)
	}
	if !strings.Contains(out, "timetattle harvest") {
		t.Errorf("orient does not name harvest, which is the only thing that runs the "+
			"re-attribution pass:\n%s", out)
	}
	if !strings.Contains(out, "7 observations") {
		t.Errorf("orient does not say what the action is worth:\n%s", out)
	}
}

// D2Q, the other half. An empty actionable pool is a legitimate first screen
// and has to say so out loud, rather than printing an empty section that reads
// as "not measured".
func TestOrientSaysThereIsNothingToDoWhenEveryPathIsAttributed(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	path := seedOrient(t, map[string]int{dir: 3}, 0)

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	markers, err := registry.List(index)
	if err != nil {
		t.Fatalf("list the registry: %v", err)
	}
	if err := resolveProjects(context.Background(), st, markers, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	st.Close()

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "NOTHING TO DO") {
		t.Errorf("orient does not say the actionable pool is empty:\n%s", out)
	}
	if strings.Contains(out, "NEXT") {
		t.Errorf("orient names a next action when there is none:\n%s", out)
	}
}

// A3D. registry.Resolve climbs to the NEAREST marker, so a marker in the parent
// already covers the child. Recommending the child would leave the parent's own
// evidence unattributed and cost a second registration.
func TestOrientRollsAnUnregisteredSubdirectoryIntoTheDirectoryThatWouldBeRegistered(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "Examples", "Hello")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("make the child directory: %v", err)
	}
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	// The child carries far more observations than the parent, so a flat
	// ranking by count would put it first. It is still the wrong answer.
	path := seedOrient(t, map[string]int{parent: 2, child: 40}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, parent+" (42)") {
		t.Errorf("orient does not roll the subdirectory up into %s with 42 observations:\n%s", parent, out)
	}
	if strings.Contains(out, child) {
		t.Errorf("orient recommends %s, which a marker in its parent already covers:\n%s", child, out)
	}
}

// A3D, and the case that has real data behind it. On the live store the
// vanished /Users/you/git/theaitrellis carries 9,924 observations at the
// directory itself and 4,294 at theaitrellis/stacktrace; one
// `consolidate -former` recovers 14,218 because a former path matches by
// prefix, and naming the subdirectory separately would be two declarations for
// one rename.
func TestOrientRollsAStrandedSubdirectoryIntoTheVanishedRootConsolidateWouldName(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "theaitrellis")
	gonesub := filepath.Join(gone, "stacktrace")
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	path := seedOrient(t, map[string]int{gone: 9, gonesub: 4}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "timetattle consolidate") {
		t.Errorf("orient does not name consolidate for a path that is gone:\n%s", out)
	}
	if !strings.Contains(out, gone+" (13)") {
		t.Errorf("orient does not roll the stranded subdirectory into %s with 13 observations:\n%s", gone, out)
	}
	if strings.Contains(out, gonesub) {
		t.Errorf("orient names %s separately; one -former on its parent recovers both:\n%s", gonesub, out)
	}
	if orientRecommendsRegistering(out) {
		t.Errorf("orient recommends registering a directory that does not exist:\n%s", out)
	}
}

// orientRecommendsRegistering reports whether the output tells anyone to
// register anything.
//
// It looks for the flag rather than the word: the census names every verb it
// knows in its left-hand column, including the ones whose count is zero,
// because a row that vanishes when empty reads as "not measured". Only a
// command line with a -path in it is a recommendation.
func orientRecommendsRegistering(out string) bool {
	return strings.Contains(out, "register -path")
}

// The recommendation orient prints has no compiler checking it against
// register's actual flags — it is a format string with a directory dropped
// in. It named `-dir` for the whole time register's flag has been `-path`,
// and nothing failed until someone tried to run it: `-dir` has never been a
// register flag on either the pre- or post-Cobra command. This test closes
// that gap by extracting the invocation orient prints and parsing it against
// registerCmd's REAL flag set — the same constructor register() itself calls
// — through the same longFlags rewrite dispatch applies to every real
// invocation, so a future rename of a register flag fails this test rather
// than rotting silently again.
func TestOrientRegisterRecommendationParsesAgainstRegistersRealFlags(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	path := seedOrient(t, map[string]int{dir: 9}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	args := orientRecommendedRegisterArgs(t, out)

	cmd := registerCmd()
	prepareSubcommand(cmd)
	if err := cmd.ParseFlags(longFlags(cmd.Flags(), args)); err != nil {
		t.Errorf("orient recommends `timetattle register %s`, which register's own flag "+
			"set refuses to parse: %v", strings.Join(args, " "), err)
	}
}

// orientRecommendedRegisterArgs extracts the flags orient recommends passing
// to register, so a test can hand them to register's real flag set instead of
// eyeballing the printed string. It knows only the two-line shape orientNext
// prints: a command line ending in a continuation backslash, and the
// remaining flags on the line after it.
func orientRecommendedRegisterArgs(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "NEXT  timetattle register") {
			continue
		}
		first := strings.TrimSuffix(strings.TrimSpace(line), "\\")
		rest := ""
		if i+1 < len(lines) {
			rest = strings.TrimSpace(lines[i+1])
		}
		fields := strings.Fields(strings.TrimSpace(first) + " " + rest)
		if len(fields) < 3 || fields[0] != "NEXT" || fields[1] != "timetattle" || fields[2] != "register" {
			t.Fatalf("could not parse the register recommendation out of:\n%s", out)
		}
		return fields[3:]
	}
	t.Fatalf("orient does not recommend registering:\n%s", out)
	return nil
}

// The rule registry.Register enforces, restated where it can be recommended.
// The Motion Autosave Vault holds every client's compositions, so blessing it
// attributes all of them to whoever ran the command; Register refuses it, and
// a command that recommends what Register refuses is an instruction that fails
// every time it is followed.
func TestOrientNeverRecommendsRegisteringAShareCacheRegisterWouldRefuse(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "Autosave Vault")
	composition := filepath.Join(cache, "dark-crystal-apex")
	if err := os.MkdirAll(composition, 0o755); err != nil {
		t.Fatalf("make the composition directory: %v", err)
	}
	// The autosave itself has to be on disk. A locator that is gone is a
	// stranded path, which is a different classification and would let this
	// test pass without the cache rule ever being consulted.
	autosave := filepath.Join(composition, "dark-crystal-apex_2026-07-10_1804.motn")
	if err := os.WriteFile(autosave, []byte("composition"), 0o644); err != nil {
		t.Fatalf("write the autosave: %v", err)
	}
	writeOrientCacheManifest(t, cache)

	index := filepath.Join(t.TempDir(), "registered-directories.json")
	path := seedOrient(t, map[string]int{autosave: 5}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	if orientRecommendsRegistering(out) {
		t.Errorf("orient recommends registering inside a declared shared cache, which "+
			"registry.Register refuses:\n%s", out)
	}
	if !strings.Contains(out, "shared application cache") {
		t.Errorf("orient does not say why nothing can be done about cache evidence:\n%s", out)
	}
	// Counted where it belongs, not merely kept out of the recommendation.
	if !strings.Contains(out, "5  inside a shared application cache") {
		t.Errorf("orient does not count the cache evidence in the cache class:\n%s", out)
	}
}

// writeOrientCacheManifest declares root as a shared application cache, in the
// isolated HOME this package's TestMain sets up, so that the real
// registry.CacheConflict sees it — rather than the test asserting against a
// rule it stubbed for itself.
func writeOrientCacheManifest(t *testing.T, root string) {
	t.Helper()
	dir, err := manifest.UserDir()
	if err != nil {
		t.Fatalf("locate the manifest directory: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make the manifest directory: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"manifest_version": 1,
		"sources": []map[string]any{{
			"id":          "orient-test-cache",
			"type":        "timestamped-files",
			"emits":       "point",
			"app":         "com.example.test",
			"cause":       "timer",
			"description": "A shared cache declared by a test, so orient sees the same refusal Register does.",
			"files":       map[string]any{"root": root, "match": "*/*.motn"},
			"timestamp":   map[string]any{"from": "mtime"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal the manifest: %v", err)
	}
	path := filepath.Join(dir, "orient-test-cache.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
}

// A marker above a directory that is already registered becomes the catch-all
// for every unregistered sibling beneath it, and quietly attributing somebody
// else's work to a catch-all is the wrong-attribution-is-worse-than-none
// failure internal/registry names in as many words. On the live store this is
// the locator "/", which 1,311 Claude transcript observations carry.
func TestOrientNeverRecommendsADirectoryAboveOneAlreadyRegistered(t *testing.T) {
	container := t.TempDir()
	inner := filepath.Join(container, "acorn")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("make the registered directory: %v", err)
	}
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, inner, "ACORN")
	path := seedOrient(t, map[string]int{container: 11}, 0)

	out := runOrient(t, "-store", path, "-index", index)

	if orientRecommendsRegistering(out) {
		t.Errorf("orient recommends registering %s, which sits above a registered "+
			"directory and would become a catch-all:\n%s", container, out)
	}
	if !strings.Contains(out, "11  above a directory already registered") {
		t.Errorf("orient drops the 11 observations rather than reporting them as unclearable:\n%s", out)
	}
}

// The filesystem root is the case a prefix comparison gets wrong, and it is not
// hypothetical: 1,311 Claude transcript observations on the live store carry the
// locator "/", because an agent session's cwd really was the root. Appending a
// separator to "/" gives "//", which is a prefix of nothing, so the container
// rule silently passed it and the first run against real data recommended
// `timetattle register -path /` as the highest-value action on the machine —
// a marker that would have claimed every unregistered directory on the volume.
func TestOrientNeverRecommendsTheFilesystemRoot(t *testing.T) {
	if !orientContains("/", "/Users/you/git/acorn") {
		t.Error(`orientContains("/", "/Users/you/git/acorn") = false; the root contains everything`)
	}
	if orientContains("/", "/") {
		t.Error(`orientContains("/", "/") = true; a directory does not contain itself`)
	}
	if !orientContains("/Users", "/Users/you") {
		t.Error(`orientContains("/Users", "/Users/you") = false`)
	}
	if orientContains("/Users/you/git/ac", "/Users/you/git/acorn") {
		t.Error(`orientContains("/Users/you/git/ac", "/Users/you/git/acorn") = true; a string prefix is not a path prefix`)
	}
}

// WJ4. Evidence carrying no path at all can never be attributed by registering
// anything, however many directories are blessed, so it must never be folded
// into a figure labelled with an action.
func TestOrientReportsEvidenceNoRegistrationCanReachSeparately(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	path := seedOrient(t, map[string]int{dir: 6}, 9)

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "NO PATH AT ALL") {
		t.Errorf("orient does not separate evidence that carries no path:\n%s", out)
	}
	if !strings.Contains(out, "9 observations carry NO PATH AT ALL") {
		t.Errorf("orient does not count the pathless evidence on its own:\n%s", out)
	}
	// 15 is the store's whole population and the header says so correctly. What
	// must never happen is the pathless nine landing in the census that carries
	// the actions.
	if !strings.Contains(out, "CARRYING A PATH: 6 observations") {
		t.Errorf("the actionable census is not the six that carry a path:\n%s", out)
	}
	if strings.Contains(out, "CARRYING A PATH: 15 observations") {
		t.Errorf("orient folds pathless evidence into the actionable figure:\n%s", out)
	}
}

// "What is it worth" cannot be answered by a bare count, because a count has no
// scale: 325 observations is the difference between an afternoon's work and a
// rounding error depending on what else is in the store. On the live store on
// 2026-08-01 the whole remaining pool was 1,743 of 919,811 observations — 0.2% —
// and no threshold this command could invent says that better than the share
// does. So the share is stated and the judgement is left to the reader.
func TestOrientStatesWhatShareOfTheStoreAnActionWouldCover(t *testing.T) {
	registered := t.TempDir()
	fresh := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, registered, "Recruiting Site")

	path := seedOrient(t, map[string]int{registered: 98, fresh: 2}, 0)
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	markers, err := registry.List(index)
	if err != nil {
		t.Fatalf("list the registry: %v", err)
	}
	if err := resolveProjects(context.Background(), st, markers, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	st.Close()

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "CARRYING A PATH: 2 observations, 2.0% of the store") {
		t.Errorf("orient does not say what share of the store the remaining pool is:\n%s", out)
	}
	if !strings.Contains(out, "2 observations, 2.0% of the store, sit in directories") {
		t.Errorf("the NEXT action does not say what share of the store it would cover:\n%s", out)
	}
}

// A share that rounds to 0.0% reads as zero, and zero is a different claim from
// "smaller than this report can render". 325 of 919,811 is 0.035%, which is the
// live figure this was found on.
func TestOrientShareNeverPrintsZeroForSomethingThatIsNotZero(t *testing.T) {
	if got := orientShare(325, 919811); got != "<0.1% of the store" {
		t.Errorf("orientShare(325, 919811) = %q, want %q", got, "<0.1% of the store")
	}
	if got := orientShare(0, 919811); got != "0.0% of the store" {
		t.Errorf("orientShare(0, 919811) = %q; nothing really is 0.0%%", got)
	}
	if got := orientShare(1743, 919811); got != "0.2% of the store" {
		t.Errorf("orientShare(1743, 919811) = %q, want %q", got, "0.2% of the store")
	}
}

// The report is read on a terminal and by an agent that will not scroll
// sideways. Nothing may run past 80 columns.
func TestOrientWrapsInsideEightyColumns(t *testing.T) {
	registered := t.TempDir()
	gone := filepath.Join(t.TempDir(), "theaitrellis")
	fresh := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, registered, "Recruiting Site")
	path := seedOrient(t, map[string]int{registered: 40, gone: 9, fresh: 4}, 6)

	for line := range strings.SplitSeq(runOrient(t, "-store", path, "-index", index), "\n") {
		// A path is as long as it is and cannot be wrapped without becoming
		// uncopyable; every line of prose can be.
		if strings.Contains(line, string(filepath.Separator)) {
			continue
		}
		if n := len([]rune(line)); n > 80 {
			t.Errorf("line is %d columns:\n%s", n, line)
		}
	}
}

// wQr. Nothing here goes through the clustering, so nothing here may be
// presented as time. The rule is enforced structurally — no figure in the
// output may look like an hours figure — rather than by a disclaimer alone.
func TestOrientPresentsNoFigureAsTime(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	path := seedOrient(t, map[string]int{dir: 6}, 9)

	out := runOrient(t, "-store", path, "-index", index)

	hours := regexp.MustCompile(`\d+(\.\d+)?\s*(h\b|hours?\b|hrs?\b)`)
	// The last-harvest line is an age, not a measurement of work, and is the
	// one place a duration is legitimate; it is excluded by name.
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "last harvest") {
			continue
		}
		if m := hours.FindString(line); m != "" {
			t.Errorf("orient prints %q, which reads as an hours figure, but nothing here "+
				"went through the clustering:\n%s", m, line)
		}
	}
	if !strings.Contains(out, "NOT TIME") {
		t.Errorf("orient does not say its counts are not estimates of time:\n%s", out)
	}
}

// Fxk. Three commands that each half-answer the same question is the failure
// this boundary exists to prevent, so orient points at the other two rather
// than restating them.
func TestOrientDoesNotRestateStatusOrMetrics(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	path := seedOrient(t, map[string]int{dir: 6}, 9)

	out := runOrient(t, "-store", path, "-index", index)

	for _, want := range []string{"timetattle status", "timetattle metrics"} {
		if !strings.Contains(out, want) {
			t.Errorf("orient does not point at %s for the question it owns:\n%s", want, out)
		}
	}
	// metrics' vocabulary. Any of these appearing here means the two commands
	// have started answering the same question.
	for _, forbidden := range []string{"ENGAGED", "PRESENCE", "sitting", "allocated"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("orient prints %q, which is metrics' answer, not orient's:\n%s", forbidden, out)
		}
	}
	// status' vocabulary: orient states the age of the last harvest as a fact
	// and never returns a verdict on it.
	for _, forbidden := range []string{"HEALTHY", "BROKEN", "capture_run"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("orient prints %q, which is status' verdict to give, not orient's:\n%s", forbidden, out)
		}
	}
}

// Every count in the census has to add up to the population it came out of, or
// a category has been dropped silently — which is the failure mode this project
// has shipped more than once.
func TestOrientCensusReconcilesWithThePopulationItCameFrom(t *testing.T) {
	registered := t.TempDir()
	container := t.TempDir()
	inner := filepath.Join(container, "acorn")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("make the registered directory: %v", err)
	}
	gone := filepath.Join(t.TempDir(), "theaitrellis")
	fresh := t.TempDir()

	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, registered, "Recruiting Site")
	registerOrientDir(t, index, inner, "ACORN")

	path := seedOrient(t, map[string]int{
		registered: 5, container: 3, gone: 2, fresh: 4,
	}, 8)

	out := runOrient(t, "-store", path, "-index", index)

	if !strings.Contains(out, "CARRYING A PATH: 14 observations") {
		t.Errorf("orient does not state the population its census divides:\n%s", out)
	}
	// 5 + 2 + 4 + 3 + 0 = 14, with no remainder and nothing counted twice.
	for _, want := range []string{
		"5  inside a registered directory already",
		"2  the path is gone from disk",
		"4  a directory register would accept",
		"3  above a directory already registered",
		"0  inside a shared application cache",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the census has no line %q, so it does not reconcile:\n%s", want, out)
		}
	}
	if strings.Contains(out, "COULD NOT BE CLASSIFIED") {
		t.Errorf("orient could not classify part of a population it should read entirely:\n%s", out)
	}
}

// The roll-up rule on its own, so a change to the ranking cannot quietly
// change what subsumes what.
func TestOrientSubsumeFoldsEachCandidateIntoTheShallowestOneAboveIt(t *testing.T) {
	got := orientSubsume(map[string]int{
		"/a/b":         2,
		"/a/b/c":       40,
		"/a/b/c/d":     1,
		"/other":       9,
		"/a/bsibling":  7,
		"/other/child": 1,
	})
	want := []orientCandidate{
		{Dir: "/a/b", Observations: 43},
		{Dir: "/other", Observations: 10},
		{Dir: "/a/bsibling", Observations: 7},
	}
	if len(got) != len(want) {
		t.Fatalf("orientSubsume returned %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// FITTING THE TERMINAL. orient hand-wrapped its prose at a hardcoded eighty,
// which is right in an eighty-column terminal and wrong in every other one. The
// margin is now the terminal's, and a pipe still gets eighty — every paragraph
// in this package was written against that and a caller's grep must keep seeing
// the same lines.
func TestOrientWrapsToTheTerminalItIsPrintingInto(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	path := seedOrient(t, map[string]int{dir: 7}, 0)

	for _, cols := range []int{60, 100} {
		var out bytes.Buffer
		if err := orientTo(atWidth{&out, cols}, []string{"-store", path, "-index", index}); err != nil {
			t.Fatalf("orient at %d columns: %v", cols, err)
		}
		fits(t, cols, "orient", out.String())
	}

	var narrow, wide bytes.Buffer
	if err := orientTo(atWidth{&narrow, 60}, []string{"-store", path, "-index", index}); err != nil {
		t.Fatalf("orient: %v", err)
	}
	if err := orientTo(atWidth{&wide, 100}, []string{"-store", path, "-index", index}); err != nil {
		t.Fatalf("orient: %v", err)
	}
	// A hundred-column report must actually USE the room, or nothing adapted.
	if n, _ := widest(wide.String()); n <= 80 {
		t.Errorf("orient's widest line in a hundred columns is %d; it is still wrapping at "+
			"eighty:\n%s", n, wide.String())
	}
	if strings.Count(narrow.String(), "\n") <= strings.Count(wide.String(), "\n") {
		t.Errorf("the narrow report is no taller than the wide one, so the prose did not reflow")
	}
}

func TestOrientWrapsAPipeAtEightyAsBefore(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerOrientDir(t, index, dir, "Recruiting Site")
	out := runOrient(t, "-store", seedOrient(t, map[string]int{dir: 7}, 0), "-index", index)
	if strings.Contains(out, "…") {
		t.Errorf("piped output was truncated:\n%s", out)
	}
	// Every line that CAN be broken is inside eighty. The ones that cannot are
	// single words — a store path or a directory being recommended — and those
	// are left overlong on purpose, because a path split across two lines
	// cannot be pasted into the command it is an argument to.
	for line := range strings.SplitSeq(out, "\n") {
		if w := len([]rune(line)); w > 80 && strings.Contains(strings.TrimSpace(line), " ") {
			t.Errorf("a %d-column line went into a pipe from orient, which wraps at eighty:\n%s",
				w, line)
		}
	}
}
