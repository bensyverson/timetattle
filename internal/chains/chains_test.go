package chains

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// day is 2026-07-11, the day the published lightbar figures were taken on.
func day(hour, min, sec int) time.Time {
	return time.Date(2026, 7, 11, hour, min, sec, 0, time.Local)
}

// obs builds one filesystem observation, the shape internal/fsobserve emits:
// a Point, carrying the class in State and the absolute path in Locator.
func obs(at time.Time, locator string, class fsobserve.Class) source.Event {
	base := locator[strings.LastIndex(locator, "/")+1:]
	doc := base
	if i := strings.LastIndex(base, "."); i > 0 {
		doc = base[:i]
	}
	e := source.NewPoint(at, source.Identity{Document: doc}, fsobserve.ID)
	e.Locator = locator
	e.State = string(class)
	e.Cause = source.CauseUserAction
	return e
}

func src(at time.Time, locator string) source.Event {
	return obs(at, locator, fsobserve.ClassSource)
}

// only returns the single chain in r, failing if there is not exactly one.
func only(t *testing.T, r Result) Chain {
	t.Helper()
	if len(r.Chains) != 1 {
		t.Fatalf("got %d chains, want exactly 1:\n%s", len(r.Chains), describe(r))
	}
	return r.Chains[0]
}

func describe(r Result) string {
	var b strings.Builder
	for _, c := range r.Chains {
		fmt.Fprintf(&b, "  chain %s root=%q\n", c.Directory, c.Root)
		for _, s := range c.Steps {
			// The date is not optional: a chain routinely spans weeks, and a
			// bare clock time makes an ordered chain look unordered.
			fmt.Fprintf(&b, "    %s %v (+%s)\n", s.At.Format("2006-01-02 15:04:05"), s.Documents, s.Gap)
		}
	}
	if len(r.Unchained) > 0 {
		fmt.Fprintf(&b, "  unchained: %v\n", r.Unchained)
	}
	return b.String()
}

// names flattens a chain to its documents in step order, for readable failures.
func names(c Chain) []string {
	var out []string
	for _, s := range c.Steps {
		out = append(out, s.Documents...)
	}
	return out
}

// TestChainRecoversOrderAndIntervals is the shape of criterion Pdv, held away
// from the real filesystem so that it keeps testing the model after somebody
// touches a file on this machine. The real-data version is in realdata_test.go
// and neither substitutes for the other.
func TestChainRecoversOrderAndIntervals(t *testing.T) {
	const dir = "/Users/you/git/acorn/blender"
	r := Reconstruct([]source.Event{
		// Deliberately out of order: events arrive in store order, and a
		// model that only works on pre-sorted input would pass by accident.
		src(day(9, 36, 26), dir+"/lightbar-top-filmic.blend"),
		src(day(9, 19, 26), dir+"/lightbar.blend"),
		src(day(16, 53, 50), dir+"/lightbar-top-filmic-fable.blend"),
		src(day(9, 33, 38), dir+"/lightbar-top.blend"),
		src(day(13, 40, 58), dir+"/lightbar-top-filmic-rect.blend"),
		src(day(13, 33, 43), dir+"/lightbar-top-filmic-rect-prefable.blend"),
	}, Options{})

	c := only(t, r)
	if c.Directory != dir {
		t.Errorf("Directory = %q, want %q", c.Directory, dir)
	}
	if c.Root != "lightbar" {
		t.Errorf("Root = %q, want %q", c.Root, "lightbar")
	}

	want := []struct {
		at  time.Time
		doc string
		gap time.Duration
	}{
		{day(9, 19, 26), "lightbar", 0},
		{day(9, 33, 38), "lightbar-top", 14*time.Minute + 12*time.Second},
		{day(9, 36, 26), "lightbar-top-filmic", 2*time.Minute + 48*time.Second},
		{day(13, 33, 43), "lightbar-top-filmic-rect-prefable", 237*time.Minute + 17*time.Second},
		{day(13, 40, 58), "lightbar-top-filmic-rect", 7*time.Minute + 15*time.Second},
		{day(16, 53, 50), "lightbar-top-filmic-fable", 192*time.Minute + 52*time.Second},
	}
	if len(c.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d:\n%s", len(c.Steps), len(want), describe(r))
	}
	for i, w := range want {
		got := c.Steps[i]
		if !got.At.Equal(w.at) {
			t.Errorf("step %d at %s, want %s", i, got.At.Format("15:04:05"), w.at.Format("15:04:05"))
		}
		if len(got.Documents) != 1 || got.Documents[0] != w.doc {
			t.Errorf("step %d documents = %v, want [%s]", i, got.Documents, w.doc)
		}
		if got.Gap != w.gap {
			t.Errorf("step %d gap = %s, want %s", i, got.Gap, w.gap)
		}
	}
}

// TestDashedNameIsNotSplit is criterion gqe. dark-crystal is one document, and
// the failure this guards against is a model that reads it as a "dark" family
// and then adopts anything else beginning dark-.
func TestDashedNameIsNotSplit(t *testing.T) {
	const dir = "/Users/you/git/acorn/local"
	r := Reconstruct([]source.Event{
		src(day(12, 57, 23), dir+"/dark-crystal.motn"),
		src(day(14, 46, 10), dir+"/dark-crystal-apex.motn"),
		src(day(15, 0, 0), dir+"/dark-matter.motn"),
		src(day(15, 30, 0), dir+"/dark-matter-halo.motn"),
	}, Options{})

	if len(r.Chains) != 2 {
		t.Fatalf("got %d chains, want 2 — dark-crystal and dark-matter are separate families:\n%s",
			len(r.Chains), describe(r))
	}
	for _, c := range r.Chains {
		if c.Root == "dark" {
			t.Fatalf("Root = %q: the name was split at its dash, which is the exact failure this rule exists to prevent", c.Root)
		}
		for _, n := range names(c) {
			if !strings.HasPrefix(n, c.Root) {
				t.Errorf("chain rooted %q contains %q", c.Root, n)
			}
		}
	}
	roots := []string{r.Chains[0].Root, r.Chains[1].Root}
	if !(contains(roots, "dark-crystal") && contains(roots, "dark-matter")) {
		t.Errorf("roots = %v, want dark-crystal and dark-matter", roots)
	}
}

func contains(hay []string, needle string) bool {
	return slices.Contains(hay, needle)
}

// TestSimultaneousBirthsAreOneAction is criterion QN2. Seven files duplicated
// together are one gesture; scoring them as seven steps produces six intervals
// of zero and reads as a burst of decisions that never happened.
func TestSimultaneousBirthsAreOneAction(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	together := day(16, 15, 22)
	events := []source.Event{src(day(15, 58, 57), dir+"/scanline.motn")}
	for _, n := range []string{
		"scanline-native", "scanline-card-granted", "scanline-face-granted",
		"scanline-face-denied", "scanline-card-denied", "scanline2-face-denied",
		"scanline3-face-denied",
	} {
		events = append(events, src(together, dir+"/"+n+".motn"))
	}

	c := only(t, Reconstruct(events, Options{}))
	if len(c.Steps) != 2 {
		t.Fatalf("got %d steps, want 2 — one save, then one duplication of seven:\n%v", len(c.Steps), c.Steps)
	}
	burst := c.Steps[1]
	if len(burst.Documents) != 7 {
		t.Errorf("step 1 names %d documents, want 7", len(burst.Documents))
	}
	if c.Documents() != 8 {
		t.Errorf("Documents() = %d, want 8", c.Documents())
	}
	for i, s := range c.Steps {
		if i > 0 && s.Gap == 0 {
			t.Errorf("step %d has a zero gap; a collapsed burst must not leave intervals of zero behind", i)
		}
	}
	if burst.Gap != 16*time.Minute+25*time.Second {
		t.Errorf("gap = %s, want 16m25s measured from the previous step", burst.Gap)
	}
}

// A near-simultaneous burst is still one gesture. The seven real files agree to
// the second, so this constructed case is the one that actually exercises
// Coincidence rather than exact equality.
func TestNearSimultaneousBirthsCollapseToo(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	base := day(16, 15, 22)
	c := only(t, Reconstruct([]source.Event{
		src(day(15, 58, 57), dir+"/scanline.motn"),
		src(base, dir+"/scanline-a.motn"),
		src(base.Add(300*time.Millisecond), dir+"/scanline-b.motn"),
		src(base.Add(700*time.Millisecond), dir+"/scanline-c.motn"),
	}, Options{}))

	if len(c.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(c.Steps))
	}
	if len(c.Steps[1].Documents) != 3 {
		t.Errorf("burst names %v, want all three", c.Steps[1].Documents)
	}
	if !c.Steps[1].At.Equal(base) {
		t.Errorf("burst at %s, want the earliest member %s", c.Steps[1].At, base)
	}
}

// Beyond Coincidence they are separate decisions, or the rule would swallow
// genuine fast iteration.
func TestBirthsBeyondCoincidenceStaySeparate(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	base := day(16, 15, 22)
	c := only(t, Reconstruct([]source.Event{
		src(base, dir+"/scanline.motn"),
		src(base.Add(2*time.Second), dir+"/scanline-a.motn"),
	}, Options{}))
	if len(c.Steps) != 2 {
		t.Fatalf("got %d steps, want 2; two seconds apart is two decisions", len(c.Steps))
	}
}

// TestChainsDoNotCrossDirectories is the resolution of a real ambiguity in the
// record. project/private/2026-07-28-spike-findings.md cites lightbar.psd on one line
// and a lightbar Blender chain on the next; they are two different files in two
// different directories, and a model that clustered on the name alone would
// have merged a Photoshop document into a Blender chain and produced an
// iteration rhythm belonging to neither.
func TestChainsDoNotCrossDirectories(t *testing.T) {
	r := Reconstruct([]source.Event{
		src(day(9, 19, 26), "/Users/you/git/acorn/blender/lightbar.blend"),
		src(day(9, 33, 38), "/Users/you/git/acorn/blender/lightbar-top.blend"),
		src(day(9, 55, 54), "/Users/you/git/acorn/scanline/lightbar.psd"),
		src(day(13, 20, 26), "/Users/you/git/acorn/scanline/lightbar-beat2.psd"),
	}, Options{})

	if len(r.Chains) != 2 {
		t.Fatalf("got %d chains, want 2 — one per directory:\n%s", len(r.Chains), describe(r))
	}
	for _, c := range r.Chains {
		for _, s := range c.Steps {
			for _, l := range s.Locators {
				if !strings.HasPrefix(l, c.Directory+"/") {
					t.Errorf("chain in %s holds %s", c.Directory, l)
				}
			}
		}
	}
}

// A thousand rendered frames are one export. Chaining output would report the
// largest chain in the corpus for the action that took the least thought.
func TestRenderedOutputIsNotChained(t *testing.T) {
	const dir = "/Users/you/git/acorn/test-renders"
	var events []source.Event
	base := day(11, 0, 0)
	for i := range 1000 {
		events = append(events,
			obs(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("%s/frame-%04d.png", dir, i), fsobserve.ClassOutput))
	}
	r := Reconstruct(events, Options{})
	if len(r.Chains) != 0 {
		t.Fatalf("got %d chains from rendered output, want 0:\n%s", len(r.Chains), describe(r))
	}
	if r.EventsConsidered != 0 {
		t.Errorf("EventsConsidered = %d, want 0 — output was filtered before clustering", r.EventsConsidered)
	}

	// Two defences, and it matters which one fired. Ask for output explicitly
	// and the frames are STILL not a chain, because frame-0004 and frame-0005
	// agree only up to a digit that follows a digit. The class filter is the
	// cheap defence; the name rule is the one that holds when somebody widens it.
	wide := Reconstruct(events, Options{Classes: []string{string(fsobserve.ClassOutput)}})
	if wide.EventsConsidered != 1000 {
		t.Fatalf("EventsConsidered = %d, want 1000 — Classes must actually widen the filter", wide.EventsConsidered)
	}
	if len(wide.Chains) != 0 {
		t.Errorf("got %d chains, want 0 — sequential frames are refused by the name rule too:\n%s",
			len(wide.Chains), describe(wide))
	}

	// And the parameter is real rather than a hard-coded refusal: output that
	// genuinely forms a family chains once output is asked for.
	family := []source.Event{
		obs(day(11, 0, 0), dir+"/lightbar.png", fsobserve.ClassOutput),
		obs(day(11, 30, 0), dir+"/lightbar-med-top.png", fsobserve.ClassOutput),
	}
	if got := len(Reconstruct(family, Options{}).Chains); got != 0 {
		t.Errorf("by default got %d chains from output, want 0", got)
	}
	widened := Reconstruct(family, Options{Classes: []string{string(fsobserve.ClassOutput)}})
	if len(widened.Chains) != 1 {
		t.Fatalf("with Classes=output got %d chains, want 1", len(widened.Chains))
	}
}

// Events from another source share nothing with a filesystem walk but the
// store they are read from, and a Biome focus interval has no filename to
// cluster on at all.
func TestOtherSourcesAreIgnored(t *testing.T) {
	const dir = "/Users/you/git/acorn/blender"
	foreign := source.NewInterval(day(9, 0, 0), day(10, 0, 0),
		source.Identity{App: "org.blenderfoundation.blender"}, "biome-appusage")
	r := Reconstruct([]source.Event{
		foreign,
		src(day(9, 19, 26), dir+"/lightbar.blend"),
		src(day(9, 33, 38), dir+"/lightbar-top.blend"),
	}, Options{})
	if r.EventsConsidered != 2 {
		t.Errorf("EventsConsidered = %d, want 2 — the Biome interval is not a file observation", r.EventsConsidered)
	}
	if len(r.Chains) != 1 {
		t.Fatalf("got %d chains, want 1", len(r.Chains))
	}
}

// A file saved twice is one branch. internal/fsobserve emits birth and mtime as
// two indistinguishable Points, so the model has to reduce per file or every
// long-lived document acquires a phantom second step at the moment it was last
// touched — which for lightbar.psd is 23:41, thirteen hours after its birth.
func TestASecondObservationOfTheSameFileIsNotASecondStep(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	r := Reconstruct([]source.Event{
		src(day(9, 55, 54), dir+"/lightbar.psd"),
		src(day(23, 41, 6), dir+"/lightbar.psd"), // same file, its mtime
		src(day(13, 20, 26), dir+"/lightbar-beat2.psd"),
	}, Options{})

	c := only(t, r)
	if len(c.Steps) != 2 {
		t.Fatalf("got %d steps, want 2 — the 23:41 mtime is an edit, not a branch:\n%s", len(c.Steps), describe(r))
	}
	if !c.Steps[0].At.Equal(day(9, 55, 54)) {
		t.Errorf("first step at %s, want the earliest observation 09:55:54", c.Steps[0].At.Format("15:04:05"))
	}
	if !c.Steps[1].At.Equal(day(13, 20, 26)) {
		t.Errorf("second step at %s, want 13:20:26", c.Steps[1].At.Format("15:04:05"))
	}
}

// foo.blend and foo.blend1 are one document under two extensions, and
// internal/fsobserve classifies both as source on purpose. Two steps would
// double the apparent branch count of every Blender directory on this machine.
func TestOneDocumentUnderTwoExtensionsIsOneStep(t *testing.T) {
	const dir = "/Users/you/git/acorn/blender"
	r := Reconstruct([]source.Event{
		src(day(9, 19, 26), dir+"/lightbar.blend"),
		src(day(13, 33, 27), dir+"/lightbar-top-filmic-rect.blend1"),
		src(day(13, 40, 58), dir+"/lightbar-top-filmic-rect.blend"),
	}, Options{})

	c := only(t, r)
	if len(c.Steps) != 2 {
		t.Fatalf("got %d steps, want 2:\n%s", len(c.Steps), describe(r))
	}
	if !c.Steps[1].At.Equal(day(13, 33, 27)) {
		t.Errorf("step at %s, want the earlier of the two files, 13:33:27", c.Steps[1].At.Format("15:04:05"))
	}
	if len(c.Steps[1].Documents) != 1 {
		t.Errorf("documents = %v, want one name", c.Steps[1].Documents)
	}
	if len(c.Steps[1].Locators) != 2 {
		t.Errorf("locators = %v, want both files kept as provenance", c.Steps[1].Locators)
	}

	// Restricting to .blend is how the published figures were taken, and it
	// has to actually change the answer or the flag is decoration.
	narrow := only(t, Reconstruct([]source.Event{
		src(day(9, 19, 26), dir+"/lightbar.blend"),
		src(day(13, 33, 27), dir+"/lightbar-top-filmic-rect.blend1"),
		src(day(13, 40, 58), dir+"/lightbar-top-filmic-rect.blend"),
	}, Options{Extensions: []string{".blend"}}))
	if !narrow.Steps[1].At.Equal(day(13, 40, 58)) {
		t.Errorf("with Extensions=.blend the step is at %s, want 13:40:58",
			narrow.Steps[1].At.Format("15:04:05"))
	}
}

// The brief's hardest question: a family whose earliest member was deleted.
// This is not hypothetical — /Users/you/git/acorn/blender holds seven
// animation_final-* documents and no animation_final. Without a rule for it the
// whole family fragments into singletons and disappears from the corpus.
func TestFamilySurvivesAMissingAncestor(t *testing.T) {
	const dir = "/Users/you/git/acorn/blender"
	r := Reconstruct([]source.Event{
		src(day(9, 0, 0), dir+"/animation_final-preround.blend"),
		src(day(9, 30, 0), dir+"/animation_final-rotating.blend"),
		src(day(10, 0, 0), dir+"/animation_final-tristate-rotation-size.blend"),
		src(day(11, 0, 0), dir+"/animation_final-short-t1.blend"),
	}, Options{})

	c := only(t, r)
	if c.Root != "animation_final" {
		t.Errorf("Root = %q, want animation_final — the label is what the family agrees on, not a file", c.Root)
	}
	if len(c.Steps) != 4 {
		t.Errorf("got %d steps, want 4:\n%s", len(c.Steps), describe(r))
	}

	// The chain must start where the evidence starts, and the caller must be
	// able to tell that from the chain itself: no animation_final locator
	// exists, so nothing claims the lineage began at 09:00.
	for _, l := range c.Steps[0].Locators {
		if strings.HasSuffix(l, "/animation_final.blend") {
			t.Fatal("invented a member that is not on disk")
		}
	}
}

// The floor that makes the missing-ancestor rule safe. Four shared characters
// is a coincidence; the rule must not adopt on it.
func TestAShortSharedPrefixIsNotAFamily(t *testing.T) {
	const dir = "/Users/you/git/acorn/local"
	r := Reconstruct([]source.Event{
		src(day(9, 0, 0), dir+"/dark-crystal-apex.motn"),
		src(day(9, 30, 0), dir+"/dark-matter-halo.motn"),
	}, Options{})
	if len(r.Chains) != 0 {
		t.Fatalf("got %d chains, want 0 — 'dark' is not a family:\n%s", len(r.Chains), describe(r))
	}
	if len(r.Unchained) != 2 {
		t.Errorf("Unchained = %v, want both documents named", r.Unchained)
	}
}

// A prefix that lands mid-word is not a boundary. scanline is a whole document
// on this machine and scanlines is a different artifact entirely.
//
// The positive control in the same directory is load-bearing: a "want 0 chains"
// assertion passes against a model that does nothing at all, so the same call
// has to also produce the chain that should be there.
func TestPrefixMustLandOnANameBoundary(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	r := Reconstruct([]source.Event{
		src(day(15, 58, 57), dir+"/scanline.motn"),
		src(day(21, 54, 40), dir+"/scanlines.psd"),
		src(day(16, 15, 22), dir+"/scanline-native.motn"),
	}, Options{})

	c := only(t, r)
	if got := names(c); len(got) != 2 {
		t.Fatalf("chain names %v, want scanline and scanline-native only", got)
	}
	for _, n := range names(c) {
		if n == "scanlines" {
			t.Error("scanlines was adopted into the scanline chain: the prefix landed mid-word")
		}
	}
	if !contains(r.Unchained, dir+"/scanlines") {
		t.Errorf("Unchained = %v, want scanlines named — it was observed and not chained, which is not the same as never seen", r.Unchained)
	}
}

// A digit is a boundary only where the prefix does not already end in one.
// Without that qualification a camera roll becomes a save-as chain.
func TestSequentiallyNumberedFilesAreNotAFamily(t *testing.T) {
	const dir = "/Users/you/Pictures"
	var events []source.Event
	for i := 1230; i < 1240; i++ {
		events = append(events, src(day(9, 0, i-1230), fmt.Sprintf("%s/IMG_%d.psd", dir, i)))
	}
	r := Reconstruct(events, Options{})
	if len(r.Chains) != 0 {
		t.Fatalf("got %d chains, want 0 — IMG_1234 and IMG_1235 are two photographs:\n%s", len(r.Chains), describe(r))
	}

	// But a digit after a non-digit IS a version marker, and the same corpus
	// depends on it: scanline2-face-denied descends from scanline.
	versioned := Reconstruct([]source.Event{
		src(day(9, 0, 0), "/Users/you/git/acorn/scanline/scanline.motn"),
		src(day(9, 30, 0), "/Users/you/git/acorn/scanline/scanline2-face-denied.motn"),
	}, Options{})
	if len(versioned.Chains) != 1 {
		t.Fatalf("got %d chains for scanline/scanline2, want 1", len(versioned.Chains))
	}
}

// One step is a file, not a chain. Reporting it would make every isolated
// document in the corpus a "chain" and the chain count meaningless.
func TestASingleStepIsNotAChain(t *testing.T) {
	const dir = "/Users/you/git/acorn/blender"
	r := Reconstruct([]source.Event{
		// tile.blend and tile.blend1 on this machine: one document, two files,
		// born in the same minute.
		src(day(9, 0, 0), dir+"/tile.blend1"),
		src(day(9, 8, 49), dir+"/tile.blend"),
		src(day(10, 0, 0), dir+"/backup.blend"),
	}, Options{})
	if len(r.Chains) != 0 {
		t.Fatalf("got %d chains, want 0:\n%s", len(r.Chains), describe(r))
	}
	if len(r.Unchained) != 2 {
		t.Errorf("Unchained = %v, want tile and backup", r.Unchained)
	}
	if r.Chained != 0 {
		t.Errorf("Chained = %d, want 0", r.Chained)
	}
	if r.Documents != 2 {
		t.Errorf("Documents = %d, want 2 — tile.blend and tile.blend1 are one document", r.Documents)
	}
}

// A burst that is the whole family is one action and no interval at all, so it
// is not a chain either. Files duplicated together and never touched again tell
// you one thing happened, and a chain claims a rhythm.
//
// Adding one later save to the same family must turn it into a chain, in the
// same call — otherwise "want 0 chains" is satisfied by a model that never
// clusters anything.
func TestABurstAloneIsNotAChain(t *testing.T) {
	const dir = "/Users/you/git/acorn/scanline"
	at := day(16, 15, 22)
	// All three agree on "scanline-card", which is over half of each name, so
	// they are one family even with no scanline-card of their own on disk.
	// scanline-face-granted would NOT join them without the scanline ancestor
	// present — see TestLinked — and using it here would test the clustering
	// rule instead of the burst rule.
	burst := []source.Event{
		src(at, dir+"/scanline-card-granted.motn"),
		src(at, dir+"/scanline-card-denied.motn"),
		src(at, dir+"/scanline-card-native.motn"),
	}
	r := Reconstruct(burst, Options{})
	if len(r.Chains) != 0 {
		t.Fatalf("got %d chains, want 0 — one action is not a rhythm:\n%s", len(r.Chains), describe(r))
	}
	if r.Documents != 3 || len(r.Unchained) != 3 {
		t.Errorf("Documents = %d, Unchained = %v; the three files were observed, they just formed no rhythm",
			r.Documents, r.Unchained)
	}

	withLater := only(t, Reconstruct(append(burst, src(day(17, 0, 0), dir+"/scanline-card-granted-2.motn")), Options{}))
	if len(withLater.Steps) != 2 {
		t.Fatalf("one later save gives %d steps, want 2 — the burst is one of them", len(withLater.Steps))
	}
	if len(withLater.Steps[0].Documents) != 3 {
		t.Errorf("first step names %v, want the whole burst", withLater.Steps[0].Documents)
	}
}

// Chains come back in time order of their first step, which is the order a
// report reads them in and the order the derived table stores them in.
func TestChainsAreOrderedByTheirFirstStep(t *testing.T) {
	r := Reconstruct([]source.Event{
		src(day(15, 0, 0), "/Users/you/git/acorn/b/zebra.motn"),
		src(day(15, 30, 0), "/Users/you/git/acorn/b/zebra-two.motn"),
		src(day(9, 0, 0), "/Users/you/git/acorn/a/apple.motn"),
		src(day(9, 30, 0), "/Users/you/git/acorn/a/apple-two.motn"),
	}, Options{})
	if len(r.Chains) != 2 {
		t.Fatalf("got %d chains, want 2", len(r.Chains))
	}
	if r.Chains[0].Root != "apple" || r.Chains[1].Root != "zebra" {
		t.Errorf("roots in order = %q, %q; want apple then zebra", r.Chains[0].Root, r.Chains[1].Root)
	}
}

// Reconstruct records what it was actually run with, because the derived table
// stores those parameters and a recompute is only auditable if they are real.
// The parameters must survive a real run, not only the empty one.
func TestOptionsComeBackResolved(t *testing.T) {
	r := Reconstruct([]source.Event{
		src(day(9, 19, 26), "/Users/you/git/acorn/blender/lightbar.blend"),
		src(day(9, 33, 38), "/Users/you/git/acorn/blender/lightbar-top.blend"),
	}, Options{})
	if len(r.Chains) != 1 {
		t.Fatalf("got %d chains, want 1; the options below would be recorded against a derivation that did nothing", len(r.Chains))
	}
	if r.Options.Source != fsobserve.ID {
		t.Errorf("Source = %q, want %q", r.Options.Source, fsobserve.ID)
	}
	if len(r.Options.Classes) != 1 || r.Options.Classes[0] != string(fsobserve.ClassSource) {
		t.Errorf("Classes = %v, want [source]", r.Options.Classes)
	}
	if r.Options.MinSharedPrefix != DefaultMinSharedPrefix {
		t.Errorf("MinSharedPrefix = %d, want %d", r.Options.MinSharedPrefix, DefaultMinSharedPrefix)
	}
	if r.Options.Coincidence != DefaultCoincidence {
		t.Errorf("Coincidence = %s, want %s", r.Options.Coincidence, DefaultCoincidence)
	}

	// An override must survive too, or the recorded parameters are a
	// restatement of the defaults rather than what ran.
	tuned := Reconstruct(nil, Options{MinSharedPrefix: 12, Coincidence: 5 * time.Second})
	if tuned.Options.MinSharedPrefix != 12 || tuned.Options.Coincidence != 5*time.Second {
		t.Errorf("overrides came back as %d / %s", tuned.Options.MinSharedPrefix, tuned.Options.Coincidence)
	}
}

// "Nothing was recorded" and "observed, and formed no chain" are different
// facts, and this project has shipped the bug of collapsing them twice. Both
// have to be distinguishable from the Result alone.
func TestNothingRecordedIsDistinguishableFromNothingChained(t *testing.T) {
	empty := Reconstruct(nil, Options{})
	if len(empty.Chains) != 0 || len(empty.Unchained) != 0 || empty.Documents != 0 || empty.EventsConsidered != 0 {
		t.Errorf("empty input gave %+v, want every count zero", empty)
	}

	observed := Reconstruct([]source.Event{
		src(day(10, 0, 0), "/Users/you/git/acorn/blender/backup.blend"),
	}, Options{})
	if len(observed.Chains) != 0 {
		t.Fatalf("got %d chains from one file, want 0", len(observed.Chains))
	}
	if observed.Documents != 1 || observed.EventsConsidered != 1 {
		t.Errorf("Documents = %d, EventsConsidered = %d, want 1 and 1", observed.Documents, observed.EventsConsidered)
	}
	if len(observed.Unchained) != 1 {
		t.Fatalf("Unchained = %v, want the one document named", observed.Unchained)
	}
	if observed.Unchained[0] != "/Users/you/git/acorn/blender/backup" {
		t.Errorf("Unchained[0] = %q, want the directory and name so it can be found again", observed.Unchained[0])
	}
}

// linked is the whole model in one predicate, so it gets a table of its own —
// including the cases that reach it only through a directory nobody has yet.
func TestLinked(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
		why  string
	}{
		{"lightbar", "lightbar-top", true, "whole name is a boundary prefix"},
		{"lightbar", "lightbar2", true, "digit after a non-digit is a version marker"},
		{"scanline", "scanlines", false, "prefix lands mid-word"},
		{"dark-crystal", "dark-matter", false, "'dark' is four characters of coincidence"},
		{"dark-crystal", "dark-crystal-v2", true, "the ancestor is on disk"},
		{"IMG_1234", "IMG_1235", false, "digit after a digit is not a boundary"},
		{"animation_final-t1", "animation_final-short-t1", true, "missing ancestor, 15 shared characters"},
		{"lightbar-top-filmic-rect", "lightbar-top-filmic-fable", true, "siblings under a shared parent"},
		{"scanline-face-denied", "scanline-card-denied", false, "8 shared characters is under half the shorter name"},
		{"tile", "transmission", false, "one letter"},
		{"backup", "tile", false, "nothing shared"},
		{"a", "a-b", true, "no length floor where the whole name is the prefix"},
		{"mockup", "mockup", true, "a name is its own relative"},
		{"report-jan", "report-feb", true, "6 shared characters over a 10-character name"},

		// Found by running the model over the real corpus. The longest common
		// prefix of these two overshoots into the component that differs —
		// they share "..._1line_o", the o of "on" and of "off" — so a rule
		// that only trims trailing separators sees a boundary that is not one
		// and refuses a real family. The prefix has to be walked back to the
		// last point that is a boundary in BOTH names.
		{"Wall Switch+Keypad_1line_off", "Wall Switch+Keypad_1line_on", true, "backs off to Wall Switch+Keypad_1line, 24 characters"},
		{"render-off", "render-on", true, "backs off to 'render', 6 characters over a 9-character name"},
		// Backing off must not become a way in for the cases the floor exists
		// to refuse. Both of these back off to a real boundary and are then
		// stopped by the length rule, which is the floor doing its job.
		{"IMG_1234", "IMG_1235", false, "backs off to 'IMG', three characters"},
		// A known conservative miss, recorded rather than tuned away: these
		// ARE siblings, and 'take' is four characters, the same width as the
		// 'dark' that must never adopt dark-matter. No floor separates them,
		// so the model declines to guess and both come back unchained.
		{"take-01-final", "take-02-final", false, "backs off to 'take', under the floor"},
	}
	for _, c := range cases {
		if got := linked(c.a, c.b, DefaultMinSharedPrefix); got != c.want {
			t.Errorf("linked(%q, %q) = %v, want %v — %s", c.a, c.b, got, c.want, c.why)
		}
		if got := linked(c.b, c.a, DefaultMinSharedPrefix); got != c.want {
			t.Errorf("linked(%q, %q) = %v, want %v — the relation must be symmetric", c.b, c.a, got, c.want)
		}
	}
}
