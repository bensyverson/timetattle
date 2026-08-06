package chains

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fsfixture"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// testdata/realchains.txt is a synthetic save-as corpus with the SHAPE of the
// real one realdata_test.go validates against, walked off a real filesystem by
// internal/fsobserve rather than handed to Reconstruct as pre-built events.
//
// That distinction is the whole point of it. chains_test.go already holds
// constructed cases, and they are events built in memory — they exercise the
// model and nothing underneath it. realdata_test.go exercises the whole path,
// walk included, but only on one laptop. This corpus is the third thing: the
// whole path, on any machine.
//
// IT CARRIES THE CONSTRUCTED DASH-SPLITTING CASE DELIBERATELY. CLAUDE.md
// records that dash-splitting passes against the ENTIRE real save-as corpus
// with zero wrong joins, purely because no dark-* sibling family happens to
// exist on that disk — and that the constructed case is what actually holds the
// rule. TestDashedNameIsNotSplit in chains_test.go holds it at the model level.
// This corpus puts the same sibling family on a real filesystem, so the rule is
// held at the walk level too: local/dark-crystal* and local/dark-matter* are
// two families sharing four characters, and a model that cut names at their
// dashes would merge them into one "dark" chain here and could not do so
// against Ben's disk no matter how many times it was run.
const fixtureChainsManifest = "testdata/realchains.txt"

// fixtureChainsAt is the synthetic working day the corpus sits in. It is
// deliberately NOT 2026-07-11, the day the published lightbar figures were
// taken on, so that a figure from this fixture can never be mistaken for one of
// those measurements.
func fixtureChainsAt(hh, mm, ss int) time.Time {
	return time.Date(2026, 3, 4, hh, mm, ss, 0, time.Local)
}

// fixtureChainsRoot materialises the corpus into a temporary directory.
func fixtureChainsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	err := fsfixture.Materialize(root, fixtureChainsManifest)
	if errors.Is(err, fsfixture.ErrNoBirthtime) {
		t.Skipf("this platform cannot set a file's birth time, so a corpus carrying one cannot be built here: %v", err)
	}
	if err != nil {
		t.Fatalf("Materialize %s: %v", fixtureChainsManifest, err)
	}
	return root
}

// fixtureChainsEvents walks the corpus exactly as a harvest would.
func fixtureChainsEvents(t *testing.T) (string, []source.Event) {
	t.Helper()
	root := fixtureChainsRoot(t)
	events, err := fsobserve.New(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated fixture corpus")
	}
	return root, events
}

// fixtureChainsFind returns the chain in dir with the given root, or fails.
//
// It duplicates realdata_test.go's own find deliberately rather than sharing
// it: that file is about to move behind a build tag, and a helper this file
// needs must not disappear with it.
func fixtureChainsFind(t *testing.T, r Result, dir, wantRoot string) Chain {
	t.Helper()
	for _, c := range r.Chains {
		if c.Directory == dir && c.Root == wantRoot {
			return c
		}
	}
	var got []string
	for _, c := range r.Chains {
		if c.Directory == dir {
			got = append(got, c.Root)
		}
	}
	t.Fatalf("no chain rooted %q in %s; roots there: %v", wantRoot, dir, got)
	return Chain{}
}

func fixtureChainsNames(c Chain) []string {
	var out []string
	for _, s := range c.Steps {
		out = append(out, s.Documents...)
	}
	return out
}

// TestFixtureChainRecoversTheOrderAndIntervalsOffARealWalk is the portable half
// of TestRealLightbarChainIsBlenderAndReproducesThePublishedFigures. The
// intervals are the ones that document reports — +14m12s, +2m48s, +237m17s,
// +7m15s, +192m52s — reproduced here from birthtimes this test set itself, so
// the model, the walk and the birthtime reader are all exercised together.
func TestFixtureChainRecoversTheOrderAndIntervalsOffARealWalk(t *testing.T) {
	root, events := fixtureChainsEvents(t)
	render := filepath.Join(root, "render")

	r := Reconstruct(events, Options{Extensions: []string{".blend"}})
	c := fixtureChainsFind(t, r, render, "beacon")

	want := []struct {
		at  time.Time
		doc string
		gap time.Duration
	}{
		{fixtureChainsAt(9, 19, 26), "beacon", 0},
		{fixtureChainsAt(9, 33, 38), "beacon-top", 14*time.Minute + 12*time.Second},
		{fixtureChainsAt(9, 36, 26), "beacon-top-filmic", 2*time.Minute + 48*time.Second},
		{fixtureChainsAt(13, 33, 43), "beacon-top-filmic-rect-prefable", 237*time.Minute + 17*time.Second},
		{fixtureChainsAt(13, 40, 58), "beacon-top-filmic-rect", 7*time.Minute + 15*time.Second},
		{fixtureChainsAt(16, 53, 50), "beacon-top-filmic-fable", 192*time.Minute + 52*time.Second},
	}
	if len(c.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d:\n%s", len(c.Steps), len(want), fixtureChainsDescribe(r))
	}
	for i, w := range want {
		got := c.Steps[i]
		if !got.At.Equal(w.at) {
			t.Errorf("step %d at %s, want %s", i, got.At.Format("2006-01-02 15:04:05"), w.at.Format("2006-01-02 15:04:05"))
		}
		if len(got.Documents) != 1 || got.Documents[0] != w.doc {
			t.Errorf("step %d documents = %v, want [%s]", i, got.Documents, w.doc)
		}
		if got.Gap != w.gap {
			t.Errorf("step %d gap = %s, want %s", i, got.Gap, w.gap)
		}
	}
}

// The same directory read under the default model, which admits Blender's own
// .blend1 backups. Two documents first appear as a backup, EARLIER than the
// .blend that carries the name, so two intervals shrink and one adjacent pair
// swaps order. Both readings are honest readings of the same disk, and the
// difference is recorded rather than resolved by preference.
func TestFixtureChainWithBackupsReordersTheTable(t *testing.T) {
	root, events := fixtureChainsEvents(t)
	render := filepath.Join(root, "render")
	c := fixtureChainsFind(t, Reconstruct(events, Options{}), render, "beacon")

	byName := map[string]time.Time{}
	for _, s := range c.Steps {
		for _, d := range s.Documents {
			byName[d] = s.At
		}
	}
	if got, want := byName["beacon-top-filmic-rect"], fixtureChainsAt(13, 33, 27); !got.Equal(want) {
		t.Errorf("beacon-top-filmic-rect first appears at %s, want %s (its .blend1)", got, want)
	}
	if got, want := byName["beacon-top-filmic-fable"], fixtureChainsAt(15, 41, 2); !got.Equal(want) {
		t.Errorf("beacon-top-filmic-fable first appears at %s, want %s (its .blend1)", got, want)
	}
	if !byName["beacon-top-filmic-rect"].Before(byName["beacon-top-filmic-rect-prefable"]) {
		t.Error("expected the .blend1 to move rect ahead of rect-prefable")
	}

	// A .blend and its .blend1 are one document, so the file count must exceed
	// the document count.
	files := 0
	for _, s := range c.Steps {
		files += len(s.Locators)
	}
	if got := c.Documents(); files != 8 || got != 6 {
		t.Errorf("%d files across %d documents, want 8 across 6; the .blend/.blend1 merge did not happen",
			files, got)
	}
}

// TestFixtureDashedNameIsNotSplitOffARealWalk is the case this whole corpus
// exists to carry, and the one CLAUDE.md singles out as the project's canonical
// example of real data validating a wrong rule.
//
// A dash-splitting model reads dark-crystal as a "dark" family with a "crystal"
// variant, and then adopts anything else beginning dark-. Against Ben's own
// disk that model produces zero wrong joins — not because it is right, but
// because no dark-* sibling family happens to exist there. Here one does.
func TestFixtureDashedNameIsNotSplitOffARealWalk(t *testing.T) {
	root, events := fixtureChainsEvents(t)
	dir := filepath.Join(root, "local")
	r := Reconstruct(events, Options{})

	for _, c := range r.Chains {
		if c.Root == "dark" {
			t.Fatalf("a chain rooted %q exists in %s: the name was split at its dash, which is "+
				"the exact failure this corpus was built to be able to catch", c.Root, c.Directory)
		}
	}

	crystal := fixtureChainsFind(t, r, dir, "dark-crystal")
	matter := fixtureChainsFind(t, r, dir, "dark-matter")
	if crystal.Directory != matter.Directory {
		t.Fatalf("the two families are in %s and %s; this test is not exercising the sibling case",
			crystal.Directory, matter.Directory)
	}

	got := fixtureChainsNames(crystal)
	slices.Sort(got)
	want := []string{"dark-crystal", "dark-crystal-apex", "dark-crystal-v2"}
	if !slices.Equal(got, want) {
		t.Errorf("dark-crystal chain names %v, want %v", got, want)
	}
	got = fixtureChainsNames(matter)
	slices.Sort(got)
	want = []string{"dark-matter", "dark-matter-halo"}
	if !slices.Equal(got, want) {
		t.Errorf("dark-matter chain names %v, want %v", got, want)
	}
	// The join that a dash-splitting model would make, named explicitly so a
	// failure says what went wrong rather than only that a count changed.
	for _, n := range fixtureChainsNames(crystal) {
		if slices.Contains(fixtureChainsNames(matter), n) {
			t.Errorf("%q appears in both families; the two were merged on the four characters "+
				"they share", n)
		}
	}

	// dark-crystal.motn and dark-crystal.psd are one document under two
	// extensions, so the chain holds more files than names.
	files := 0
	for _, s := range crystal.Steps {
		files += len(s.Locators)
	}
	if files != 4 {
		t.Errorf("%d files behind %d documents, want 4 (.motn and .psd both present)", files, crystal.Documents())
	}
}

// The same stem in two directories is two pieces of work, not one. This is the
// strongest argument in the real corpus for scoping clusters to a directory,
// and it is reproduced here as two "beacon" chains that must not touch.
func TestFixtureChainsDoNotCrossDirectoriesOffARealWalk(t *testing.T) {
	root, events := fixtureChainsEvents(t)
	render := filepath.Join(root, "render")
	plates := filepath.Join(root, "plates")

	r := Reconstruct(events, Options{})
	blend := fixtureChainsFind(t, r, render, "beacon")
	psd := fixtureChainsFind(t, r, plates, "beacon")

	if blend.Directory == psd.Directory {
		t.Fatal("both beacon chains are in the same directory; this test is not exercising the split")
	}
	for _, c := range []Chain{blend, psd} {
		for _, s := range c.Steps {
			for _, l := range s.Locators {
				if filepath.Dir(l) != c.Directory {
					t.Errorf("the chain in %s holds %s", c.Directory, l)
				}
			}
		}
	}
	if blend.Documents() == psd.Documents() && len(blend.Steps) == len(psd.Steps) {
		t.Errorf("the two beacon chains have identical shape (%d steps, %d documents); "+
			"they may have been merged and split back apart", len(blend.Steps), blend.Documents())
	}
}

// Seven files duplicated in one gesture are one action. Scoring them as seven
// steps produces six intervals of zero and reads as a burst of decisions that
// never happened.
func TestFixtureSimultaneousBirthsAreOneActionOffARealWalk(t *testing.T) {
	root, events := fixtureChainsEvents(t)
	dir := filepath.Join(root, "plates")
	c := fixtureChainsFind(t, Reconstruct(events, Options{}), dir, "gridline")

	at := fixtureChainsAt(16, 15, 22)
	var burst *Step
	for i := range c.Steps {
		if c.Steps[i].At.Equal(at) {
			burst = &c.Steps[i]
		}
		if i > 0 && c.Steps[i].Gap <= 0 {
			t.Errorf("step %d has gap %s; collapsing a burst must leave no zero intervals", i, c.Steps[i].Gap)
		}
	}
	if burst == nil {
		t.Fatalf("no step at %s; steps are:\n%s", at, fixtureChainsDescribe(Reconstruct(events, Options{})))
	}
	if len(burst.Documents) != 7 {
		t.Errorf("the 16:15:22 burst names %d documents, want 7: %v", len(burst.Documents), burst.Documents)
	}
	if len(c.Steps) != 2 || c.Documents() != 8 {
		t.Errorf("%d steps for %d documents, want 2 for 8; simultaneous births were not collapsed",
			len(c.Steps), c.Documents())
	}
}

// The whole-corpus shape, asserted exactly rather than as a range. A model that
// chains everything is as useless as one that chains nothing, and on a fixture
// the numbers can be pinned instead of merely bounded.
func TestFixtureCorpusDistributionIsExact(t *testing.T) {
	_, events := fixtureChainsEvents(t)
	r := Reconstruct(events, Options{})

	if len(r.Chains) != 5 {
		t.Errorf("got %d chains, want 5 (beacon twice, gridline, dark-crystal, dark-matter):\n%s",
			len(r.Chains), fixtureChainsDescribe(r))
	}
	if r.Documents != 22 {
		t.Errorf("got %d documents, want 22", r.Documents)
	}
	if r.Chained != 21 {
		t.Errorf("got %d chained documents, want 21", r.Chained)
	}
	// Naming the unchained one rather than counting it: "nothing was chained
	// here" and "this directory was never walked" are different facts.
	if len(r.Unchained) != 1 {
		t.Errorf("got %d unchained documents, want exactly 1: %v", len(r.Unchained), r.Unchained)
	} else if filepath.Base(r.Unchained[0]) != "lone-study" {
		t.Errorf("the unchained document is %s, want one named lone-study", r.Unchained[0])
	}
	if r.Chained+len(r.Unchained) != r.Documents {
		t.Errorf("%d chained + %d unchained != %d documents", r.Chained, len(r.Unchained), r.Documents)
	}

	steps := 0
	for _, c := range r.Chains {
		steps += len(c.Steps)
	}
	if steps != 14 {
		t.Errorf("got %d steps across the corpus, want 14", steps)
	}
	if steps >= r.Chained {
		t.Errorf("%d steps for %d chained documents; nothing collapsed", steps, r.Chained)
	}

	// Rendered output shares the directory and the stem with a source family
	// and must not be chained with it: one export writes a sequence of
	// sequentially-named frames in the same second, and a model that chained
	// them would report a long chain for one action.
	if r.EventsConsidered >= len(events) {
		t.Errorf("%d of %d events were considered; the output frames in the corpus were not filtered out",
			r.EventsConsidered, len(events))
	}
	for _, c := range r.Chains {
		for _, s := range c.Steps {
			for _, l := range s.Locators {
				if fsobserve.Classify(l) != fsobserve.ClassSource {
					t.Errorf("chain %s/%s holds %s, which is not a source document", c.Directory, c.Root, l)
				}
			}
		}
	}
}

// A guard on the corpus itself. If the manifest ever stops carrying the sibling
// family, the dash-splitting test above would keep passing while proving
// nothing — which is precisely how an over-general rule survived an entire live
// stream once already.
func TestFixtureCorpusStillCarriesTheDashSiblingFamily(t *testing.T) {
	entries, err := fsfixture.Load(fixtureChainsManifest)
	if err != nil {
		t.Fatalf("Load %s: %v", fixtureChainsManifest, err)
	}
	var crystal, matter int
	for _, e := range entries {
		switch base := filepath.Base(e.Path); {
		case strings.HasPrefix(base, "dark-crystal"):
			crystal++
		case strings.HasPrefix(base, "dark-matter"):
			matter++
		}
	}
	if crystal < 2 || matter < 2 {
		t.Fatalf("the corpus holds %d dark-crystal files and %d dark-matter files; both families "+
			"need at least two members or a dash-splitting model has nothing to get wrong",
			crystal, matter)
	}
}

func fixtureChainsDescribe(r Result) string {
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
