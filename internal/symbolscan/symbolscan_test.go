package symbolscan

import "testing"

// The bug this package exists to prevent: scripts/confidential and
// scripts/pseudonymize each tokenise identifiers their own way, and had they
// disagreed about what counts as a boundary, the auditor could report clean
// on exactly the text the rewriter still needed to touch, or vice versa. Both
// must resolve "is the letter right after a backslash escape part of the
// identifier" the same way.

// "flonk" below is an invented token with no referent anywhere in this
// project -- not a real client symbol, and deliberately not one of the
// pseudonyms (acorn/birch/cypress/dogwood) either, since those now stand in
// for real clients elsewhere in the repo. The tokenising rule this package
// implements is name-agnostic: it does not care whose symbol it is, so there
// is never a reason for a fixture here to use a real one.

func TestDeescapeBlanksABackslashLetterEscape(t *testing.T) {
	got := Deescape(`grep -rio '\bflonk\b' .`)
	want := `grep -rio '  flonk  ' .`
	if got != want {
		t.Errorf("Deescape = %q, want %q", got, want)
	}
}

func TestDeescapePreservesLineLength(t *testing.T) {
	src := `\bflonk\b and \dfoo\w bar`
	if got := Deescape(src); len(got) != len(src) {
		t.Errorf("Deescape(%q) changed length: %d -> %d (%q)", src, len(src), len(got), got)
	}
}

func TestDeescapeLeavesOrdinaryTextAlone(t *testing.T) {
	src := "no escapes here, just flonk and FLONK_ROOT"
	if got := Deescape(src); got != src {
		t.Errorf("Deescape(%q) = %q, want it unchanged", src, got)
	}
}

func TestEscapedLetterBoundaryRecognisesABackslashEscape(t *testing.T) {
	s := `\bflonk\b`
	// s[2] is 'f', the first letter of "flonk", which sits right after the
	// escape "\b" at s[0:2] ('\\', 'b').
	if !EscapedLetterBoundary(s, 2) {
		t.Errorf("EscapedLetterBoundary(%q, 2) = false, want true: %q sits right after \\b", s, s[2:])
	}
}

func TestEscapedLetterBoundaryRejectsAnOrdinaryPosition(t *testing.T) {
	s := "realFlonk"
	if EscapedLetterBoundary(s, 4) {
		t.Errorf("EscapedLetterBoundary(%q, 4) = true, want false: no backslash escape precedes %q", s, s[4:])
	}
}
