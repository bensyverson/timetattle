package sessions_test

import (
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/sittings"
)

// The corpus, written beside the parameters.
//
// sessions.exclude is applied where events are READ, so it never reaches
// Cluster and cannot be a field of Params: every field of Params is consumed by
// the clustering, and a field it silently ignored would be the first. It is
// still part of the derivation — a bigger part than most of them, worth 126.2h
// of presence on the live corpus — so it is stored, and Replace is handed it.

// Two runs configured with the same patterns in a different order, or with one
// of them written twice, are the same derivation. Matching does not depend on
// the order and a repeated pattern removes what it removed the first time, so
// the stored bytes must not differ either — the same rule joinSorted already
// applies to the source lists, for the same reason.
func TestTheExcludeListIsStoredInACanonicalForm(t *testing.T) {
	written := sittings.Corpus{Exclude: []string{"/Users/ben/git/Easley/reports/", "*.log", "*.gz", "*.log"}}
	sorted := sittings.Corpus{Exclude: []string{"*.gz", "*.log", "/Users/ben/git/Easley/reports/"}}

	st, db := open(t)
	res := sample(t)
	if err := replaceFrom(st, res, written, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	first := fingerprint(t, db, "derived_session")

	if err := replaceFrom(st, res, sorted, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if second := fingerprint(t, db, "derived_session"); second != first {
		t.Errorf("the same corpus written two ways stored different rows.\nfirst:  %s\nsecond: %s",
			first, second)
	}
	if !strings.Contains(first, `["*.gz","*.log","/Users/ben/git/Easley/reports/"]`) {
		t.Errorf("the stored exclude list is not the canonical one: %s", first)
	}
}

// A derivation that excluded nothing stores the empty list rather than nothing
// at all, so that it stays distinguishable from a table nobody derived into.
func TestADerivationThatExcludedNothingStoresTheEmptyList(t *testing.T) {
	st, db := open(t)
	res := sample(t)
	if err := replaceFrom(st, res, sittings.Corpus{}, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	var stored string
	if err := db.QueryRow(`SELECT DISTINCT exclude_patterns FROM derived_session`).Scan(&stored); err != nil {
		t.Fatalf("read exclude_patterns: %v", err)
	}
	if stored != "[]" {
		t.Errorf("a derivation that excluded nothing stored %q, want %q", stored, "[]")
	}
	var from, to string
	if err := db.QueryRow(`SELECT DISTINCT corpus_from, corpus_to FROM derived_session`).Scan(&from, &to); err != nil {
		t.Fatalf("read the corpus window: %v", err)
	}
	if from != "" || to != "" {
		t.Errorf("an unbounded corpus stored %q..%q, want two empty strings", from, to)
	}
}
