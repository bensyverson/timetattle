// Package symbolscan holds the one tokenising rule scripts/confidential (the
// auditor) and scripts/pseudonymize (the rewriter) must agree on: what counts
// as a boundary around an identifier-shaped run of characters. The two tools
// find that boundary by different means -- confidential extracts a whole
// identifier and splits it into camelCase/underscore components, while
// pseudonymize scans position by position -- but if the two ever disagreed
// about a single case, the auditor could report a tree clean that the
// rewriter still needed to touch, or the reverse. That disagreement is not
// hypothetical: it shipped once, as the reason confidential could not see a
// client name inside a regex escape such as \bflonk\b.
package symbolscan

import "regexp"

// escapeSeq matches a backslash followed by a single ASCII letter, as in a
// regex escape such as \b, \d or \w. To a naive identifier scan the escaped
// letter reads as an ordinary word character and merges into whatever
// follows it -- \bflonk\b tokenises as "bflonk", not "flonk" -- so a client
// name written inside a regex probe (which is exactly how this project's own
// audit commands are quoted in project/ documents) goes unseen.
var escapeSeq = regexp.MustCompile(`\\[A-Za-z]`)

// Deescape blanks every backslash-letter escape to two spaces, preserving
// every other byte's position (so line content used for anything other than
// identifier extraction, and any column/line accounting, stays valid) while
// guaranteeing the escaped letter can no longer merge into an adjacent
// identifier run.
func Deescape(s string) string {
	return escapeSeq.ReplaceAllString(s, "  ")
}

// IsLetter reports whether b is an ASCII letter.
func IsLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// EscapedLetterBoundary reports whether the byte at s[i-1] is a letter
// immediately preceded by a backslash -- i.e. whether position i in s sits
// right after a regex-style escape such as \b -- and should therefore count
// as a match boundary the same way a non-alphanumeric neighbour does.
func EscapedLetterBoundary(s string, i int) bool {
	return i >= 2 && s[i-2] == '\\' && IsLetter(s[i-1])
}
