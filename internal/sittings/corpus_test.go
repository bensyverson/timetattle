package sittings_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
)

// The corpus is what a derivation was drawn FROM. Params is what it was drawn
// WITH, and the two are separate because Cluster consumes every field of Params
// and none of the corpus: the events were already narrowed before it saw them.

func corpus() sittings.Corpus {
	return sittings.Corpus{Exclude: []string{"*.bz2", "*.gz", "*.log", "/Users/ben/git/Easley/reports/"}}
}

// The defect this exists for. Adding four patterns moved total presence from
// 1193.2h to 1067.0h and the median sitting from 10m18s to 22m18s on the live
// corpus — more movement than most gap changes — and a derivation built under
// one list was byte-identical to one built under another.
func TestTwoCorporaDifferingOnlyInTheExcludeListAreNotEqual(t *testing.T) {
	with := corpus()
	if !with.Equal(with) {
		t.Fatal("a corpus is not equal to itself")
	}
	without := sittings.Corpus{}
	if with.Equal(without) {
		t.Error("a derivation that excluded four patterns was read as the same one as a derivation that excluded nothing")
	}
	fewer := sittings.Corpus{Exclude: []string{"*.bz2", "*.gz", "*.log"}}
	if with.Equal(fewer) {
		t.Error("dropping the last pattern of four left the corpus indistinguishable")
	}
	other := sittings.Corpus{Exclude: []string{"*.tmp"}}
	if fewer.Equal(other) {
		t.Error("two different exclude lists of different lengths were read as one")
	}
}

// Order and duplicates are decided here rather than left to whatever the
// storage happened to do. Matching is order-independent and a repeated pattern
// removes exactly what it removed the first time, so these ARE the same
// derivation, and Equal has to agree with the canonical form the store writes.
func TestReorderingOrRepeatingThePatternsIsTheSameCorpus(t *testing.T) {
	a := sittings.Corpus{Exclude: []string{"*.gz", "*.log"}}
	b := sittings.Corpus{Exclude: []string{"*.log", "*.gz"}}
	c := sittings.Corpus{Exclude: []string{"*.log", "*.gz", "*.log"}}
	if !a.Equal(b) {
		t.Error("the same two patterns written in the other order were read as a different derivation")
	}
	if !a.Equal(c) {
		t.Error("a pattern written twice was read as a different derivation")
	}
	if a.ExcludeJSON() != b.ExcludeJSON() || a.ExcludeJSON() != c.ExcludeJSON() {
		t.Errorf("three spellings of one corpus store three different values: %q, %q, %q",
			a.ExcludeJSON(), b.ExcludeJSON(), c.ExcludeJSON())
	}
}

// An empty list and no list are the same FACT — this derivation excluded
// nothing — and both are distinct from a derivation nobody recorded, which is
// carried by Derived.ComputedAt and by the absence of rows rather than by a
// value here.
func TestExcludingNothingIsOneFactHoweverItIsSpelt(t *testing.T) {
	nilList := sittings.Corpus{}
	empty := sittings.Corpus{Exclude: []string{}}
	if !nilList.Equal(empty) {
		t.Error("an empty exclude list and an absent one were read as two different derivations")
	}
	if got := nilList.ExcludeJSON(); got != "[]" {
		t.Errorf("a corpus that excluded nothing stores %q, want %q; a stored NULL or empty string "+
			"would make \"excluded nothing\" indistinguishable from \"nobody recorded it\"", got, "[]")
	}
}

// From and To narrow the corpus exactly as the exclude list does, and were
// unrecorded for the same reason. A derivation computed over one month, read
// back, reports an empty January that nobody can tell from a January with no
// work in it.
func TestCorporaDifferingOnlyInTheWindowAreNotEqual(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	whole := sittings.Corpus{}
	windowed := sittings.Corpus{From: from, To: to}
	if whole.Equal(windowed) {
		t.Error("a derivation over one month was read as the same one as a derivation over everything")
	}
	if windowed.Equal(sittings.Corpus{From: from}) {
		t.Error("a derivation with a closing bound was read as the same one as a derivation without")
	}
	if windowed.Equal(sittings.Corpus{To: to}) {
		t.Error("a derivation with an opening bound was read as the same one as a derivation without")
	}
}

// The stored form is JSON and not a comma-separated list, because a pattern is
// a path and a path may hold a comma. presence_sources can be comma-joined
// because a source id cannot.
func TestAPatternHoldingACommaSurvivesTheStoredForm(t *testing.T) {
	c := sittings.Corpus{Exclude: []string{"/Users/ben/Documents/Smith, John/"}}
	back, err := sittings.ParseExcludeJSON(c.ExcludeJSON())
	if err != nil {
		t.Fatalf("ParseExcludeJSON(%q): %v", c.ExcludeJSON(), err)
	}
	if len(back) != 1 || back[0] != "/Users/ben/Documents/Smith, John/" {
		t.Errorf("a pattern holding a comma read back as %q; splitting on commas would have "+
			"invented two patterns, one of which excludes everything", back)
	}
}

func TestParseExcludeJSONRefusesWhatIsNotAListOfPatterns(t *testing.T) {
	for _, stored := range []string{"", "null", "*.log", `{"a":1}`, "[1,2]"} {
		if got, err := sittings.ParseExcludeJSON(stored); err == nil {
			t.Errorf("ParseExcludeJSON(%q) = %q, want an error: a value nobody can read back "+
				"is not a record of what was excluded", stored, got)
		}
	}
	back, err := sittings.ParseExcludeJSON("[]")
	if err != nil {
		t.Fatalf(`ParseExcludeJSON("[]"): %v`, err)
	}
	if len(back) != 0 {
		t.Errorf(`ParseExcludeJSON("[]") = %q, want nothing excluded`, back)
	}
}

// Round trip, through the canonical form, so that reading a corpus back and
// comparing it with the live configuration is a comparison rather than a
// translation.
func TestACorpusReadBackFromItsStoredFormIsTheSameCorpus(t *testing.T) {
	c := corpus()
	back, err := sittings.ParseExcludeJSON(c.ExcludeJSON())
	if err != nil {
		t.Fatalf("ParseExcludeJSON: %v", err)
	}
	if !c.Equal(sittings.Corpus{Exclude: back}) {
		t.Errorf("a corpus stored as %s read back as %q, which is a different derivation",
			c.ExcludeJSON(), back)
	}
}

func TestCorpusValidateRefusesAWindowThatCannotHoldAnything(t *testing.T) {
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := (sittings.Corpus{From: from, To: to}).Validate(); err == nil {
		t.Error("Validate accepted a corpus whose window ends before it begins")
	}
	if err := (sittings.Corpus{From: to, To: from}).Validate(); err != nil {
		t.Errorf("Validate refused an ordinary window: %v", err)
	}
	if err := (sittings.Corpus{}).Validate(); err != nil {
		t.Errorf("Validate refused the unbounded corpus: %v", err)
	}
	if err := (corpus()).Validate(); err != nil {
		t.Errorf("Validate refused an ordinary exclude list: %v", err)
	}
}

// A derivation carries the corpus beside the parameters. A caller handed one
// without the other cannot say what population the sittings came out of.
func TestDerivedCarriesTheCorpusBesideTheParameters(t *testing.T) {
	d := sittings.Derived{Corpus: corpus()}
	if !d.Corpus.Equal(corpus()) {
		t.Error("Derived does not carry the corpus it was drawn from")
	}
	var zero sittings.Derived
	if !zero.Corpus.Equal(sittings.Corpus{}) {
		t.Error("the zero Derived claims a corpus")
	}
}
