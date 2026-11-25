// Package chains reconstructs save-as chains from filesystem observations.
//
// Blender and Motion are not used the way a text editor is used. A file is
// created, worked on, and then *branched*: File > Save As to a new name beside
// the old one. Every .blend in /Users/you/git/acorn/blender has a birthtime equal
// to its mtime to the second — 25 of 25, measured with `stat -f '%B %m %N'` —
// so no single file carries a span at all, and every source that reports one
// file at a time reports a scatter of instants with no structure.
//
// The structure is in the filenames:
//
//	09:19:26  lightbar
//	09:33:38  lightbar-top                        (+14m12s)
//	09:36:26  lightbar-top-filmic                  (+2m48s)
//	13:33:27  lightbar-top-filmic-rect            (+237m01s)
//	13:33:43  lightbar-top-filmic-rect-prefable      (+16s)
//	15:41:02  lightbar-top-filmic-fable           (+127m19s)
//
// Tight iteration bursts separated by long gaps, at a granularity no other
// source in this project reaches. This is the version history that
// .DocumentRevisions-V100 turned out not to hold: it is not in the Versions
// store, it is in the filenames.
//
// # This is inference, not a source
//
// Nothing here implements source.Source and nothing here runs during harvest.
// Clustering files by name and deriving intervals between them is a model, it
// will be revised, and the event store is append-only with no retraction API —
// so a clustering decision written as events could never be taken back. What
// this package produces is a derived table (migration 0004) that is wiped and
// rebuilt in one transaction, and Reconstruct itself is a pure function over
// []source.Event so the model can be tested with no database at all.
//
// # Do not split on dashes
//
// The obvious implementation cuts each name at its dashes and calls the first
// segment the stem. It fails immediately: dark-crystal is one real document in
// /Users/you/git/acorn/local, not a "dark" family with a "crystal" variant, and
// it sits in a directory that also holds dark-crystal-v2 and dark-crystal-apex,
// which genuinely are its descendants. This is the same failure Claude Code's
// project directories have, unable to tell a separator from a dash inside a
// real name.
//
// So no name is ever cut apart. Names are only ever compared whole against each
// other, and a boundary is recognised only where two names already agree up to
// it. See linked for the rule.
//
// # What is chained, and what is not
//
//   - Only events whose State is the "source" Class (see internal/fsobserve):
//     a .motn, a .blend, a .psd — something a person made decisions in. Output
//     is excluded and the reason is quantitative rather than tidy: one export
//     writes a thousand sequentially-named frames, they share a long prefix and
//     land in the same second, and a model that chained them would report a
//     thousand-step chain for one action. Options.Classes can widen this, and
//     the choice is recorded with the derivation.
//
//   - Within one directory only. Save As offers the current folder, so a branch
//     lands beside its parent. Scoping to a directory is also what keeps two
//     unrelated documents that happen to share a stem apart, and this machine
//     has the case: /Users/you/git/acorn/blender/lightbar.blend and
//     /Users/you/git/acorn/scanline/lightbar.psd are a Blender chain and a
//     Photoshop chain, on the same day, with nothing to do with each other.
//     Merging them on the shared name "lightbar" would interleave two people's
//     worth of iteration rhythm into one false sequence.
//
//   - Earliest observation per file, never every observation. internal/fsobserve
//     emits up to two points per file, birth and mtime, and does not label which
//     is which — from the store they are two Points with identical Identity,
//     State and Locator differing only in time. The earlier one is the birth
//     whenever birth <= mtime, which is the ordinary case; a file restored with
//     its mtime preserved can invert that, and then the earlier stamp is the
//     preserved mtime, which is still the earliest instant this machine can
//     attest for that file. Taking both would put lightbar.psd in its chain at
//     09:55 and again at 23:41 and invent a branch that never happened: a save
//     into an existing file is an edit, and only a new NAME is a branch.
//
//   - One step per document name, not per file. Blender writes foo.blend1
//     beside foo.blend, and internal/fsobserve deliberately classifies both as
//     source; both name the same document, so they are one step whose time is
//     the earlier of the two. The same rule merges dark-crystal.psd and
//     dark-crystal.motn, which is a design worked in two applications and is
//     one lineage, not two.
//
// # Simultaneous births are one action
//
// Seven .motn files in /Users/you/git/acorn/scanline share a birthtime of
// 2026-07-11 16:15:22, because they were duplicated together in one gesture.
// Seven steps would be six intervals of zero, which would read as furious
// activity and is in fact one action. Steps within Coincidence of the first
// member of a group collapse into a single step naming every document. The
// grouping is greedy from the first member rather than by truncating to a
// second, because truncation splits 16:15:21.999 from 16:15:22.001 while
// treating 16:15:22.001 and 16:15:22.999 as one.
//
// # A chain is lineage, not a session
//
// No gap threshold is applied and none belongs here. lightbar-top-filmic-2 was
// born 18 days after the rest of its family and is still the same lineage;
// whether those 18 days are one working session is a question for
// sessionization, fitted against ground truth, with a parameter this package
// must not quietly pre-empt. Steps carry their gap so a consumer can decide.
//
// Time order is recorded rather than the parent-child tree. The real structure
// is a tree — lightbar-top-filmic-rect and lightbar-top-filmic-fable both
// branch from lightbar-top-filmic — and the names retain enough to recover it,
// but the question this package was built to answer is when work happened, and
// that is the time order.
package chains

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/source"
)

// DefaultMinSharedPrefix is how many characters two names must already agree on
// before they are called relatives in the absence of a surviving ancestor.
//
// It applies to that case ONLY. Where one document's whole name is a
// boundary-prefix of another's, the ancestor is on disk and no floor is needed
// or wanted: a two-character document is as real as a twenty-character one.
//
// Six is chosen against the real corpus rather than by feel. The case it has to
// admit is /Users/you/git/acorn/blender/animation_final-*, seven documents with
// no animation_final of their own — the ancestor was never kept — agreeing on
// 15 characters. The case it has to refuse is dark-crystal against a
// hypothetical dark-matter, agreeing on 4. Anything from 5 to 15 separates
// those two; 6 is low enough to leave the half-length rule doing the work and
// high enough that a two- or three-character coincidence is never a family.
const DefaultMinSharedPrefix = 6

// DefaultCoincidence is how close two births must be to count as one action.
//
// One second, matching internal/fsobserve's own rule for deciding that a
// birthtime and an mtime are the same event. The seven scanline files agree
// exactly, so nothing here rests on the precise value; what rests on it is that
// a duplicate-and-rename gesture writing files a few hundred milliseconds apart
// does not read as several decisions.
const DefaultCoincidence = time.Second

// Options are the parameters of the model. Every one of them is recorded
// alongside the derivation, so a stale recompute is auditable rather than
// silently mixed with a fresh one.
type Options struct {
	// Source is the source_id whose events are considered. Empty means
	// internal/fsobserve, the only source that observes files this way.
	Source string
	// Classes are the event State values to consider. Nil means the source
	// Class alone — see the package doc for why output is excluded.
	Classes []string
	// Extensions restricts to these file extensions, lowercase and with the
	// leading dot. Nil means every extension the classes admit. This exists
	// because the published lightbar figures were taken over .blend alone
	// and reproducing them exactly requires saying so.
	Extensions []string
	// MinSharedPrefix overrides DefaultMinSharedPrefix. Zero means the default.
	MinSharedPrefix int
	// Coincidence overrides DefaultCoincidence. Zero means the default.
	Coincidence time.Duration
}

// withDefaults resolves the zero values. It never mutates the receiver, so the
// Options recorded with a derivation are the ones actually used.
func (o Options) withDefaults() Options {
	if o.Source == "" {
		o.Source = fsobserve.ID
	}
	if len(o.Classes) == 0 {
		o.Classes = []string{string(fsobserve.ClassSource)}
	}
	if o.MinSharedPrefix == 0 {
		o.MinSharedPrefix = DefaultMinSharedPrefix
	}
	if o.Coincidence == 0 {
		o.Coincidence = DefaultCoincidence
	}
	lower := make([]string, 0, len(o.Extensions))
	for _, ext := range o.Extensions {
		lower = append(lower, strings.ToLower(ext))
	}
	o.Extensions = lower
	return o
}

// Step is one branch point: a new name appearing in a directory.
//
// Documents holds more than one name when several were created together within
// Coincidence, and that is one action rather than several — the count is what
// says how wide the gesture was, not how much work it represents.
type Step struct {
	// At is when the earliest member of this step was first observed.
	At time.Time
	// Documents are the document names created, sorted.
	Documents []string
	// Locators are the files behind them, sorted. There can be more of these
	// than Documents: foo.blend and foo.blend1 are one document.
	Locators []string
	// Gap is the time since the previous step, and is zero on the first.
	Gap time.Duration
}

// Chain is one lineage: a family of names in a directory, in time order.
type Chain struct {
	// Directory is the folder every member lives in.
	Directory string
	// Root is what the family agrees on — the longest prefix shared by every
	// member, trimmed to a name boundary. It is a label, not a file: the
	// animation_final family has no animation_final of its own.
	Root string
	// Steps are in time order and always number at least two. One step is not
	// a chain; it is a file.
	Steps []Step
}

// Documents counts the names in the chain, which is at least Steps because a
// step can name several.
func (c Chain) Documents() int {
	n := 0
	for _, s := range c.Steps {
		n += len(s.Documents)
	}
	return n
}

// Span is first branch to last. It is not time worked and must never be summed
// as if it were: the whole reason this package exists is that these files carry
// no observed duration.
func (c Chain) Span() time.Duration {
	if len(c.Steps) < 2 {
		return 0
	}
	return c.Steps[len(c.Steps)-1].At.Sub(c.Steps[0].At)
}

// Result is one run of the model, carrying the counts that say whether it did
// anything. A model that chains everything is as useless as one that chains
// nothing, so Chained and Unchained are reported rather than left to be
// inferred from the chain list.
type Result struct {
	// Options are the parameters actually used, with defaults resolved.
	Options Options
	// Chains are in time order of their first step.
	Chains []Chain
	// EventsConsidered is how many events survived the class, source and
	// extension filters — not how many were passed in.
	EventsConsidered int
	// Generated is how many of the source's observations were left out as
	// generated files.
	Generated int
	// Documents is how many distinct (directory, name) pairs those events
	// described.
	Documents int
	// Chained is how many of those landed in a chain.
	Chained int
	// Unchained names the rest, as directory/name, sorted. Naming them rather
	// than counting them is deliberate: "nothing was chained here" and "this
	// directory was never walked" are different facts and a bare count
	// collapses them.
	Unchained []string
}

// key identifies a document: a name within a directory. Two files under one
// name — foo.blend and foo.blend1 — share a key and are one branch point.
type key struct {
	dir  string
	name string
}

func (k key) String() string { return k.dir + "/" + k.name }

// doc is one document as the model sees it: the earliest instant anything
// under that name was observed, and the files that carried the observations.
type doc struct {
	key      key
	at       time.Time
	locators []string
}

// Reconstruct is the model. It is pure: same events and options, same result,
// no clock and no filesystem.
func Reconstruct(events []source.Event, opts Options) Result {
	o := opts.withDefaults()
	result := Result{Options: o}

	classes := make(map[string]bool, len(o.Classes))
	for _, c := range o.Classes {
		classes[c] = true
	}
	extensions := make(map[string]bool, len(o.Extensions))
	for _, e := range o.Extensions {
		extensions[e] = true
	}

	// Reduce to one earliest instant per document. Two passes are not needed:
	// the minimum is order-independent, which is why events may arrive in any
	// order and why a store read does not have to be sorted first.
	docs := map[key]*doc{}
	for _, e := range events {
		if e.Source == o.Source && generated.Is(e) {
			result.Generated++
			continue
		}
		if e.Source != o.Source || e.Shape != source.Point || e.Locator == "" || e.Start.IsZero() {
			continue
		}
		if !classes[e.State] {
			continue
		}
		if len(extensions) > 0 && !extensions[strings.ToLower(filepath.Ext(e.Locator))] {
			continue
		}
		result.EventsConsidered++

		name := e.Identity.Document
		if name == "" {
			name = document(e.Locator)
		}
		k := key{dir: filepath.Dir(e.Locator), name: name}
		d, seen := docs[k]
		if !seen {
			d = &doc{key: k, at: e.Start}
			docs[k] = d
		}
		if e.Start.Before(d.at) {
			d.at = e.Start
		}
		d.locators = append(d.locators, e.Locator)
	}
	result.Documents = len(docs)
	if len(docs) == 0 {
		return result
	}

	// Cluster within each directory. Names are sorted first so that the union
	// order — and therefore nothing observable — depends on map iteration.
	byDir := map[string][]*doc{}
	for _, d := range docs {
		byDir[d.key.dir] = append(byDir[d.key.dir], d)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var unchained []string
	for _, dir := range dirs {
		members := byDir[dir]
		sort.Slice(members, func(i, j int) bool { return members[i].key.name < members[j].key.name })

		for _, cluster := range cluster(members, o.MinSharedPrefix) {
			c, ok := chainOf(dir, cluster, o.Coincidence)
			if !ok {
				for _, d := range cluster {
					unchained = append(unchained, d.key.String())
				}
				continue
			}
			result.Chains = append(result.Chains, c)
			result.Chained += c.Documents()
		}
	}
	sort.Strings(unchained)
	result.Unchained = unchained

	sort.SliceStable(result.Chains, func(i, j int) bool {
		a, b := result.Chains[i], result.Chains[j]
		if !a.Steps[0].At.Equal(b.Steps[0].At) {
			return a.Steps[0].At.Before(b.Steps[0].At)
		}
		if a.Directory != b.Directory {
			return a.Directory < b.Directory
		}
		return a.Root < b.Root
	})
	return result
}

// cluster partitions one directory's documents into families by transitive
// closure over linked.
//
// Transitivity is what lets a family hang together through its surviving
// ancestor: lightbar-top-filmic-rect and lightbar-top-filmic-fable are 19
// characters apart from a shared parent that is itself on disk, and both reach
// it directly. Where the ancestor is gone the pairwise rule has to carry the
// family on its own, which is exactly what MinSharedPrefix governs.
//
// The pairwise sweep is quadratic in the size of one directory. That is
// deliberate: the largest source-document directory in the real corpus holds
// tens of names, and a smarter structure would trade a rule anybody can read
// for a saving nothing has asked for.
func cluster(members []*doc, minShared int) [][]*doc {
	parent := make([]int, len(members))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range members {
		for j := i + 1; j < len(members); j++ {
			if linked(members[i].key.name, members[j].key.name, minShared) {
				if a, b := find(i), find(j); a != b {
					parent[a] = b
				}
			}
		}
	}

	groups := map[int][]*doc{}
	var order []int
	for i, m := range members {
		root := find(i)
		if _, seen := groups[root]; !seen {
			order = append(order, root)
		}
		groups[root] = append(groups[root], m)
	}
	out := make([][]*doc, 0, len(order))
	for _, root := range order {
		out = append(out, groups[root])
	}
	return out
}

// chainOf turns one family into a chain, collapsing simultaneous births, and
// reports false where what is left is not a chain at all.
//
// Two conditions disqualify a family, and they are the same condition: fewer
// than two steps. A lone document is a file. A family born entirely in one
// gesture is one action — six files duplicated together are not five intervals
// of zero, and reporting them as a chain would claim a rhythm that consists of
// somebody pressing Duplicate once.
func chainOf(dir string, members []*doc, coincidence time.Duration) (Chain, bool) {
	sort.SliceStable(members, func(i, j int) bool {
		if !members[i].at.Equal(members[j].at) {
			return members[i].at.Before(members[j].at)
		}
		return members[i].key.name < members[j].key.name
	})

	var steps []Step
	for i := 0; i < len(members); {
		s := Step{At: members[i].at}
		j := i
		// Greedy from the first member of the group rather than from the
		// previous one, so a long run of near-simultaneous births cannot walk
		// the window forward indefinitely and swallow real iteration.
		for ; j < len(members) && members[j].at.Sub(s.At) < coincidence; j++ {
			s.Documents = append(s.Documents, members[j].key.name)
			s.Locators = append(s.Locators, members[j].locators...)
		}
		s.Documents = sortedUnique(s.Documents)
		s.Locators = sortedUnique(s.Locators)
		steps = append(steps, s)
		i = j
	}
	if len(steps) < 2 {
		return Chain{}, false
	}
	for i := 1; i < len(steps); i++ {
		steps[i].Gap = steps[i].At.Sub(steps[i-1].At)
	}

	return Chain{Directory: dir, Root: root(members), Steps: steps}, true
}

// root labels a family with what every member agrees on, cut back to a name
// boundary. It is a label and not a claim that a file exists: the
// animation_final family has no animation_final on disk, and inventing one
// would be a member nobody observed.
func root(members []*doc) string {
	label := members[0].key.name
	for _, m := range members[1:] {
		next := sharedPrefix(label, m.key.name)
		if next == "" {
			// Cannot happen for a family that clustered, since every member
			// reached every other through linked. Falling back to the first
			// name rather than to "" keeps a label on the chain if it ever does.
			return members[0].key.name
		}
		label = next
	}
	return label
}

// sharedPrefix is the longest prefix a and b share that ends at a name boundary
// in BOTH of them, or "" if they share no such point.
//
// The walk backwards is not decoration. The longest common prefix routinely
// overshoots into the component that differs: "Wall Switch+Keypad_1line_off"
// and "Wall Switch+Keypad_1line_on" — two real files in
// /Users/you/git/acorn/demo/chrome — share "..._1line_o", the o of "on" and of
// "off", which is a boundary in neither. Trimming only trailing separators
// leaves that o in place and refuses a real family; walking back to the last
// point both names agree is a boundary recovers "Wall Switch+Keypad_1line".
//
// This is still not splitting a name. Nothing here decides where a name's
// components begin — the candidate cut points come only from where the two
// names already stop agreeing, and each is tested against both names.
func sharedPrefix(a, b string) string {
	for n := commonPrefix(a, b); n > 0; n-- {
		p := trimSeparators(a[:n])
		if p == "" {
			continue
		}
		if endsAtBoundary(a, len(p)) && endsAtBoundary(b, len(p)) {
			return p
		}
	}
	return ""
}

// linked reports whether two document names in the same directory belong to one
// lineage.
//
// Neither name is ever cut apart. The only thing computed is how far the two
// already agree, and a boundary is recognised only at a point both names reach.
// That is the whole defence against splitting dark-crystal at its dash: there
// is no dark to split off, because nothing here ever asks where a name's parts
// begin — only whether two whole names already coincide.
//
// The rule, in order:
//
//  1. Take the longest prefix the two share that ends at a name boundary in
//     both of them — see sharedPrefix, and note that the longest common prefix
//     alone is not good enough, because it overshoots into the component that
//     differs. scanline and scanlines share eight characters and no boundary at
//     all, so they get nothing.
//  2. Accept with no length floor if that prefix IS one of the names. The
//     ancestor is on disk; a two-character document is as real as a
//     twenty-character one, and no threshold makes that judgement better.
//  3. Otherwise — no surviving ancestor — require the shared prefix to reach
//     MinSharedPrefix and to be at least half the shorter name. Half is what
//     stops scanline-face-denied and scanline-card-denied from being called
//     one family on eight characters of a twenty-character name, while
//     admitting animation_final-t1 and animation_final-short-t1 on fifteen.
//
// The floor is also what makes step 1's backward walk safe. IMG_1234 and
// IMG_1235 do reach a real boundary, at "IMG"; three characters is not a
// family and the floor is what says so.
func linked(a, b string, minShared int) bool {
	p := sharedPrefix(a, b)
	if p == "" {
		return false
	}
	if p == a || p == b {
		return true
	}
	shorter := min(len(b), len(a))
	return len(p) >= minShared && len(p)*2 >= shorter
}

// commonPrefix is how many leading bytes a and b share.
func commonPrefix(a, b string) int {
	n := min(len(b), len(a))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// endsAtBoundary reports whether name may be considered to end a component at
// n bytes in, without inventing a split.
//
// A digit counts as a boundary only where the prefix does not already end in
// one. Without that, IMG_1234 and IMG_1235 agree on "IMG_123" and the trailing
// 4 and 5 look like version markers on a shared stem — two photographs
// declared a save-as chain. With it, the boundary must be a transition from
// non-digit to digit, so lightbar/lightbar2 links and IMG_1234/IMG_1235 does not.
func endsAtBoundary(name string, n int) bool {
	if n == len(name) {
		return true
	}
	if n <= 0 || n > len(name) {
		return false
	}
	if isSeparator(name[n]) {
		return true
	}
	return isDigit(name[n]) && !isDigit(name[n-1])
}

func isSeparator(c byte) bool {
	return c == '-' || c == '_' || c == ' ' || c == '.' || c == '+'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// trimSeparators removes trailing separators so that a shared prefix is
// reported as a name ("lightbar-top-filmic") rather than as a cut
// ("lightbar-top-filmic-").
func trimSeparators(s string) string {
	for len(s) > 0 && isSeparator(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

// document names the artifact a locator records: its base name without the
// extension. It matches internal/fsobserve.document, and is used only where an
// event arrived with no Document of its own.
func document(locator string) string {
	base := filepath.Base(locator)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sortedUnique is a small helper used for the name lists on a step.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i == 0 || s != out[n-1] {
			out[n] = s
			n++
		}
	}
	return out[:n]
}
