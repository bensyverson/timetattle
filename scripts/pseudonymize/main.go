// Command pseudonymize renames confidential symbols to stable pseudonyms across
// the tracked working tree, preserving case and compound shape.
//
// It is the rewriting half of the pre-publication pass; scripts/confidential is
// the auditing half and never edits anything. Run this, then gate with that.
//
// # The names live outside the repository
//
// The mapping is supplied at run time via -map or TIMETATTLE_PSEUDONYM_MAP and is
// deliberately not compiled in, for the same reason scripts/confidential takes
// its symbols at run time: a rewriter carrying the real names in its own source
// could not be published beside the repository it cleaned.
//
// # Why this is neither sed nor a word-boundary grep
//
// A substring replace corrupts English: renaming a client called "cat" would
// mangle every "catalog" and "category" in the prose.
// A word-boundary replace has the opposite fault and is the one that actually
// bites -- `\bacme\b` cannot see realAcme, acmeHours, tACME or TestAcmeFoo, all
// of which are client references. This tool accepts a match when the surrounding
// characters form a component boundary: a non-alphanumeric neighbour, a camelCase
// hump, or the tail of an uppercase run (ACMERepo -> ACME|Repo). That is the same
// notion of "component" scripts/confidential audits with.
//
// It also has to handle a name written inside a regex escape. The project/
// documents quote the audit probe as `\bacme\b`; a naive identifier scan reads
// that as "bacme" and never flags it, so a gate that got this wrong could
// report clean while the name is still on the page. A letter preceded by a
// backslash is an escape sequence, not an identifier character, and is
// treated here as a boundary via internal/symbolscan.EscapedLetterBoundary --
// the one rule this tool and scripts/confidential share, so they cannot
// disagree about a single case.
//
// # Casing
//
// acme -> kestrel, ACME -> KESTREL, Acme -> Kestrel. Any other casing (AcMe) has
// no defensible target, so it is reported and left alone for a human. Guessing
// silently is how a rename pass corrupts something nobody rechecks.
//
// # Fixed-width columns
//
// Two names of different lengths are the ordinary case, and a correct
// substitution therefore changes a line's width. In prose that is simply right.
// In a fixed-width ASCII chart it is a silent corruption: a rename over a
// document holding a 17-character label column shifted every glyph row against
// the hour ruler above it, and the tool reported "0 site(s) need a human"
// having broken two charts. Column-aligned output is ordinary in this
// repository's documents.
//
// So when a line is column-aligned — two or more spaces with content on both
// sides — the difference is absorbed into the padding of the field that
// changed, and every following column keeps its offset. When that padding
// cannot absorb it without running two fields together, the line is left
// EXACTLY as it was and the site is reported, the same refusal the mixed-case
// rule makes. Trailing spaces at the end of a line are a Markdown hard break
// rather than padding: nothing follows them that could move, so they are never
// eaten.
//
// # Usage
//
//	go run ./scripts/pseudonymize -map old=new,other=new2            # dry run
//	go run ./scripts/pseudonymize -map old=new -apply                # rewrite
//	go run ./scripts/pseudonymize -map old=new -skip doc.md:142 -apply
//
// -skip takes path:line pairs that must be left exactly as they are: a genuine
// English use of a short name, for instance. Files are taken from `git ls-files`,
// so ignored and untracked material is never touched.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/bensyverson/timetattle/internal/gitenv"
	"github.com/bensyverson/timetattle/internal/symbolscan"
)

func main() {
	dir := flag.String("dir", ".", "repository root to rewrite")
	mapping := flag.String("map", os.Getenv("TIMETATTLE_PSEUDONYM_MAP"),
		"comma-separated old=new pairs (or set TIMETATTLE_PSEUDONYM_MAP)")
	skip := flag.String("skip", "", "comma-separated path:line sites to leave untouched")
	apply := flag.Bool("apply", false, "write the changes; without it, only report")
	flag.Parse()

	m, err := parseMap(splitList(*mapping))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if len(m) == 0 {
		fmt.Fprintln(os.Stderr, "error: -map is required and must not be empty")
		os.Exit(2)
	}
	skips := parseSkips(splitList(*skip))

	files, err := trackedFiles(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	total, touched := 0, 0
	var notes []string
	for _, rel := range files {
		abs := filepath.Join(*dir, rel)
		b, err := os.ReadFile(abs)
		if err != nil || looksBinary(b) {
			continue
		}
		lines := strings.Split(string(b), "\n")
		n := 0
		for i, line := range lines {
			if skips[site{rel, i + 1}] {
				if c := countMatches(line, m); c > 0 {
					fmt.Printf("  skipped %s:%d (%d match(es), by request)\n", rel, i+1, c)
				}
				continue
			}
			out, ns := rewriteLine(line, m)
			for _, s := range ns {
				notes = append(notes, fmt.Sprintf("%s:%d %s", rel, i+1, s))
			}
			if out != line {
				n += countMatches(line, m)
				lines[i] = out
			}
		}
		if n == 0 {
			continue
		}
		total += n
		touched++
		fmt.Printf("  %-70s %d\n", rel, n)
		if *apply {
			info, _ := os.Stat(abs)
			mode := os.FileMode(0o644)
			if info != nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(abs, []byte(strings.Join(lines, "\n")), mode); err != nil {
				fmt.Fprintln(os.Stderr, "error writing", rel, err)
				os.Exit(2)
			}
		}
	}
	sort.Strings(notes)
	for _, s := range notes {
		fmt.Fprintln(os.Stderr, "left alone, needs a human:", s)
	}
	verb := "would rewrite"
	if *apply {
		verb = "rewrote"
	}
	fmt.Printf("%s %d occurrence(s) in %d file(s); %d site(s) need a human\n",
		verb, total, touched, len(notes))
}

// --- mapping ------------------------------------------------------------------

func parseMap(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		old, new, ok := strings.Cut(p, "=")
		if !ok || old == "" || new == "" {
			return nil, fmt.Errorf("malformed pair %q, want old=new", p)
		}
		m[strings.ToLower(old)] = strings.ToLower(new)
	}
	return m, nil
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

type site struct {
	path string
	line int
}

func parseSkips(ss []string) map[site]bool {
	out := map[site]bool{}
	for _, s := range ss {
		p, l, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(l)
		if err != nil {
			continue
		}
		out[site{p, n}] = true
	}
	return out
}

// --- the rewrite ---------------------------------------------------------------

func isUpper(b byte) bool  { return b >= 'A' && b <= 'Z' }
func isLower(b byte) bool  { return b >= 'a' && b <= 'z' }
func isDigit(b byte) bool  { return b >= '0' && b <= '9' }
func isLetter(b byte) bool { return isUpper(b) || isLower(b) }
func isAlnum(b byte) bool  { return isLetter(b) || isDigit(b) }

// matchAt reports whether the name occupies s[i:i+len(name)] as a whole
// component: bounded by non-alphanumerics, by a camelCase hump, by the tail of an
// uppercase run, or by a regex escape such as \b.
func matchAt(s string, i int, name string) bool {
	j := i + len(name)
	if j > len(s) || !strings.EqualFold(s[i:j], name) {
		return false
	}
	m := s[i:j]
	allUpper := m == strings.ToUpper(m) && strings.ToLower(m) != m

	leftOK := i == 0 || !isAlnum(s[i-1]) ||
		// camelCase hump: realAcme, tACME, TestAcme
		(isUpper(s[i]) && !isUpper(s[i-1])) ||
		// tail of an uppercase run: ...ACME|Repo shape, seen from the right side
		(isUpper(s[i]) && isUpper(s[i-1]) && j < len(s) && isLower(s[j])) ||
		// a letter preceded by a backslash is an escape, not an identifier char.
		// This is the one boundary rule scripts/confidential must agree with, so
		// it lives in internal/symbolscan rather than being redefined here.
		symbolscan.EscapedLetterBoundary(s, i)
	if !leftOK {
		return false
	}

	switch {
	case j == len(s), !isAlnum(s[j]):
		return true
	case !isUpper(s[j]):
		return false
	case !allUpper:
		// acmeHours, TestAcmeDoesNot
		return true
	default:
		// ACME followed by more uppercase is one word (CATALOG), unless the run
		// ends immediately: ACME|Repo.
		return j+1 < len(s) && isLower(s[j+1])
	}
}

// applyCase renders replacement in the same casing as matched, or reports that
// the casing has no defensible target.
func applyCase(matched, replacement string) (string, bool) {
	switch {
	case matched == strings.ToLower(matched):
		return strings.ToLower(replacement), true
	case matched == strings.ToUpper(matched):
		return strings.ToUpper(replacement), true
	case matched == strings.ToUpper(matched[:1])+strings.ToLower(matched[1:]):
		return strings.ToUpper(replacement[:1]) + strings.ToLower(replacement[1:]), true
	default:
		return matched, false
	}
}

// rewriteLine returns the rewritten line and a note for every site it refused
// to guess at — a casing with no defensible target, or a width change on a
// column-aligned line that its padding cannot absorb.
func rewriteLine(s string, m map[string]string) (string, []string) {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	// longest first, so a name that prefixes another cannot win by accident
	sort.Slice(names, func(a, b int) bool { return len(names[a]) > len(names[b]) })

	var out strings.Builder
	var notes []string
	var reps []substitution
	for i := 0; i < len(s); {
		hit := false
		for _, n := range names {
			if !matchAt(s, i, n) {
				continue
			}
			matched := s[i : i+len(n)]
			rep, ok := applyCase(matched, m[n])
			if !ok {
				notes = append(notes, fmt.Sprintf("%q has no unambiguous target casing", matched))
			}
			start := out.Len()
			out.WriteString(rep)
			reps = append(reps, substitution{start: start, end: out.Len(), delta: len(rep) - len(matched)})
			i += len(n)
			hit = true
			break
		}
		if !hit {
			out.WriteByte(s[i])
			i++
		}
	}

	rewritten := out.String()
	if !isColumnAligned(s) || len(rewritten) == len(s) {
		return rewritten, notes
	}
	repaired, ok := repairColumns(rewritten, reps)
	if !ok {
		// Nothing about this line is safe to write: rewriting it would shift
		// every column after the field, and padding it back would run two
		// fields together. Report the site and leave the line exactly as it
		// was, the same refusal the mixed-case rule makes.
		return s, append(notes, "changes the width of a column-aligned line, and its padding cannot absorb the difference")
	}
	return repaired, notes
}

// substitution records where one replacement landed in the rewritten line and
// how much longer (or shorter) it made it. Positions are into the OUTPUT, which
// is what repairColumns has to edit.
type substitution struct {
	start, end, delta int
}

// isColumnAligned reports whether s uses runs of spaces to place fields at
// fixed offsets: two or more spaces with non-space content on both sides. It is
// deliberately not "contains two spaces" — trailing spaces at the end of a
// Markdown line are a hard line break with no following field, so nothing after
// them can move and they must not be eaten.
func isColumnAligned(s string) bool {
	run := 0
	seenContent := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			run++
			continue
		}
		if run >= 2 && seenContent {
			return true
		}
		seenContent = true
		run = 0
	}
	return false
}

// repairColumns absorbs each substitution's width change into the padding of
// the field it sits in, so every column after it keeps the offset it had. It
// reports false when some substitution's field cannot absorb the difference —
// there is no honest answer there, and guessing is what corrupted two charts.
//
// Substitutions are repaired right to left: an edit at one offset moves
// everything after it, and everything after it has already been dealt with.
func repairColumns(out string, reps []substitution) (string, bool) {
	for _, r := range slices.Backward(reps) {

		if r.delta == 0 {
			continue
		}
		// A left-padded (right-aligned) field pads before the value, a
		// left-aligned one after it. Try the trailing gap first: that is the
		// ordinary label column.
		if repaired, ok := absorbTrailing(out, r); ok {
			out = repaired
			continue
		}
		if repaired, ok := absorbLeading(out, r); ok {
			out = repaired
			continue
		}
		return out, false
	}
	return out, true
}

// absorbTrailing takes the difference out of the run of spaces immediately
// after the replacement. It requires a following field — a gap that runs to the
// end of the line pads nothing — and at least one space left over, or two
// fields would run together.
func absorbTrailing(out string, r substitution) (string, bool) {
	j := r.end
	for j < len(out) && out[j] == ' ' {
		j++
	}
	gap := j - r.end
	if gap < 2 || j == len(out) {
		return out, false
	}
	want := gap - r.delta
	if want < 1 {
		return out, false
	}
	return out[:r.end] + strings.Repeat(" ", want) + out[j:], true
}

// absorbLeading is absorbTrailing against the padding before the replacement,
// which is where a right-aligned field keeps it.
func absorbLeading(out string, r substitution) (string, bool) {
	p := r.start
	for p > 0 && out[p-1] == ' ' {
		p--
	}
	gap := r.start - p
	if gap < 2 || p == 0 {
		return out, false
	}
	want := gap - r.delta
	if want < 1 {
		return out, false
	}
	return out[:p] + strings.Repeat(" ", want) + out[r.start:], true
}

// countMatches counts component-boundary occurrences, including the ones whose
// casing was left for a human.
func countMatches(s string, m map[string]string) int {
	n := 0
	for i := 0; i < len(s); {
		hit := 0
		for name := range m {
			if matchAt(s, i, name) {
				hit = len(name)
				break
			}
		}
		if hit > 0 {
			n++
			i += hit
		} else {
			i++
		}
	}
	return n
}

// --- file selection -------------------------------------------------------------

// withoutGitRepoEnv strips the variables git exports into every hook and
// subprocess it runs, naming the repository that invoked it. They silently
// override -C, so an unscrubbed `git -C root` acts on the invoking repository
// instead of root -- internal/gitenv holds this rule once for every
// git-shelling package in this repository, rather than a copy here. This
// rewriter is the worst place to get it wrong: reading the wrong file list
// means editing files nobody asked it to touch.
//
// This is a thin forwarding wrapper, kept only because main_test.go's
// initRepoWithFile fixture helper calls it directly to build isolated test
// repositories; trackedFiles itself goes straight through gitenv.Command.
// See TestTrackedFilesDoesNotInheritAnExportedGitDir.
func withoutGitRepoEnv(env []string) []string {
	return gitenv.Strip(env)
}

func trackedFiles(root string) ([]string, error) {
	cmd := gitenv.Command("git", "-C", root, "ls-files", "-z")
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var out []string
	for p := range strings.SplitSeq(strings.TrimRight(string(b), "\x00"), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func looksBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return strings.IndexByte(string(b), 0) >= 0
}
