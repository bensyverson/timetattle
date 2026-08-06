package sittings

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// The corpus: which observations a derivation was drawn FROM.
//
// # Why this is not a field of Params
//
// Params is the input to the clustering, and every field of it is consumed by
// internal/sessions.Cluster. The exclusion is applied where events are READ,
// before Cluster ever sees them, and Cluster would ignore it — it would be the
// first field of Params that did nothing, and the next bug is somebody setting
// it and expecting the events to be filtered. Params' own doc says the
// property precisely: two runs with equal Params over EQUAL EVENTS produce
// equal output. The corpus is the "equal events" half, and it had no home.
//
// It is not a smaller fact for being a different one. Adding four patterns to
// sessions.exclude moved total presence over the live store from 1193.2h to
// 1067.0h and the median sitting from 10m18s to 22m18s
// (project/private/2026-08-01-machine-generated-file-activity.md), which is more
// movement than most gap changes — and until this type existed, a derivation
// built under one list was byte-identical to one built under another, so
// nothing anywhere could say that a stored answer and a current question
// disagreed about which observations exist.
//
// # From and To are here for the same reason
//
// store.Filter narrows the corpus by time exactly as the patterns narrow it by
// path, and was unrecorded in exactly the same way. A derivation computed over
// one month reads back as a table with nothing in January — which is
// indistinguishable, to every reader above it, from a January nobody worked in.
// That is this project's oldest bug wearing a different hat, so both halves of
// the corpus are recorded together rather than one now and one after the next
// person is misled.
type Corpus struct {
	// Exclude names the patterns from sessions.exclude that were applied when
	// the events were read. The SQLite sidecar rule is NOT here: it is
	// universal, unconfigurable and identical in every derivation, so
	// recording it would distinguish nothing while looking as though it might.
	//
	// An empty list means this derivation excluded nothing, which is a fact.
	// "Nobody recorded what this derivation excluded" is a different fact, and
	// it is carried by Derived.ComputedAt and by the absence of rows — never by
	// a value in here. The stored column is NOT NULL so that the two cannot be
	// spelled alike; see migration 0010.
	Exclude []string
	// From and To are the window the events were read over. The zero time
	// means unbounded, which is a value rather than an absence: nothing was
	// excluded by time, and that is what was recorded.
	From, To time.Time
}

// Canonical returns the corpus in the form it is stored and compared in: the
// patterns sorted and deduplicated.
//
// The order a person writes their patterns in does not change what is removed,
// and a pattern written twice removes exactly what it removed the first time.
// Two spellings of one instruction must therefore not read as two derivations,
// or `sessionize` would be demanded after an edit that changed nothing. It is
// the same rule internal/sessions.joinSorted applies to the source lists, for
// the same reason and with the same consequence for Equal.
func (c Corpus) Canonical() Corpus {
	c.Exclude = slices.Compact(slices.Sorted(slices.Values(c.Exclude)))
	return c
}

// Equal reports whether two derivations were drawn from the same population.
//
// It compares the canonical form, so it agrees with the storage by
// construction: two corpora that store the same bytes are equal here, and two
// that store different bytes are not.
func (c Corpus) Equal(d Corpus) bool {
	a, b := c.Canonical(), d.Canonical()
	return slices.Equal(a.Exclude, b.Exclude) && a.From.Equal(b.From) && a.To.Equal(b.To)
}

// Validate reports why a corpus could not have been drawn from.
func (c Corpus) Validate() error {
	if !c.From.IsZero() && !c.To.IsZero() && c.To.Before(c.From) {
		return fmt.Errorf("the corpus runs from %v to %v, which ends before it begins; "+
			"no observation can be in it", c.From, c.To)
	}
	if slices.Contains(c.Exclude, "") {
		return fmt.Errorf("an empty exclude pattern was recorded with the derivation; " +
			"it matches every path there is and cannot have been meant")
	}
	return nil
}

// ExcludeJSON is the stored form of the exclude list: a JSON array of the
// canonical patterns, and "[]" for a derivation that excluded nothing.
//
// JSON rather than the comma-joined form the source lists use, because a
// pattern is a path and a path may hold a comma —
// "/Users/ben/Documents/Smith, John/" is an ordinary directory. Splitting that
// on commas invents two patterns, one of which is " John/" and the other of
// which is a prefix that matches far more than was written. A source id cannot
// hold a comma, which is why the older columns can get away with it.
//
// It cannot fail: a []string always marshals.
func (c Corpus) ExcludeJSON() string {
	list := c.Canonical().Exclude
	if list == nil {
		list = []string{}
	}
	out, err := json.Marshal(list)
	if err != nil {
		// Unreachable for []string, and a panic here would be worse than a
		// value that is visibly wrong in the one place it is written.
		return "[]"
	}
	return string(out)
}

// ParseExcludeJSON reads the stored form back.
//
// Anything that is not a JSON array of strings is an error rather than an empty
// list. A row nobody can read back is not a record of what was excluded, and
// answering "nothing was excluded" to a value that says something else is the
// collapse this whole type exists to prevent.
func ParseExcludeJSON(stored string) ([]string, error) {
	if stored == "" {
		return nil, fmt.Errorf("the stored exclude list is empty; " +
			"a derivation that excluded nothing records \"[]\", and an empty column records nothing at all")
	}
	var out []string
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return nil, fmt.Errorf("the stored exclude list %q is not a list of patterns: %w", stored, err)
	}
	if out == nil {
		return nil, fmt.Errorf("the stored exclude list is %q, which is not a list of patterns", stored)
	}
	return out, nil
}
