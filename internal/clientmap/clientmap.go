// Package clientmap folds an append-only log of client-project assertions into
// the mapping in force at any instant.
//
// # Why a log and not a column
//
// A project's client has 38 distinct values across ~973,000 events. Storing it
// on the event row makes changing one of them ~927,599 row updates to record a
// fact the size of a small table, and — because the attribution pass rewrites
// every row on every harvest — the stored value is a cache of exactly this
// mapping applied retroactively. It answers no question a lookup could not, and
// its one structural advantage, a past event keeping the client it was actually
// done for, is destroyed by the same overwrite that maintains it.
//
// So the mapping lives here, in about as many rows as there are projects, and
// the event keeps only its project. See project/private/2026-08-04-the-client-dimension.md
// for the argument in full, including the column design this replaced.
//
// # Two timestamps, because there are two kinds of change
//
// This is the design point everything else follows from, and a single timestamp
// silently picks one of them.
//
//	REASSIGNMENT   the project moved to a new client on some date. Work done
//	               before that date was done for the OLD client and keeps it.
//	               Rewriting it would make an invoiced period mutable against a
//	               later model change, which task mjalb's criterion XZk forbids.
//
//	CORRECTION     it was ALWAYS the new client and the old value was a mistake.
//	               This applies retroactively, because the old answer was never
//	               true of any instant.
//
// An Assertion therefore carries EffectiveFrom — when the association took
// effect — separately from RecordedAt, when somebody wrote it down. A
// reassignment sets EffectiveFrom to the date of the move. A correction leaves
// it zero, meaning "from the beginning of time". Nothing else distinguishes
// them, and one fold serves both.
//
// This is internal/adjustments' pattern, deliberately: an append-only log of
// assertions carrying their own effective span, folded in recorded order, with
// the state at any past instant recoverable by folding up to it (FoldAsOf).
// Task mjalb records that reasoning. Two things here differ from that package,
// both because this log answers a different shape of question:
//
//   - An assertion is keyed to a PROJECT and open-ended forward, rather than to
//     a closed wall-clock span. A client association has no natural end; it
//     runs until something else supersedes it.
//   - An assertion carries its SOURCE, the directory whose marker asserted it,
//     which internal/adjustments has no analogue of because a person correcting
//     a span is the only kind of author there is. Here it is load-bearing —
//     see Collision.
//
// # A collision is a fourth answer
//
// Two DIRECTORIES asserting different clients for one project is not a
// correction: neither of them changed its mind, and there is no later-is-truer
// ordering between them. Resolving it by recorded_at is last-writer-wins, which
// produces an entirely plausible wrong number and is this project's worst
// failure mode. At refuses to answer for such a project and Collisions names it,
// the way store.Attribution reports an unreadable locator rather than folding it
// into "unattributed".
//
// So a project has four possible states here and they must never be collapsed:
// resolved to a client; asserted but not yet in effect at this instant; never
// asserted at all; and claimed by two directories at once.
package clientmap

import (
	"slices"
	"time"
)

// Assertion is one row of the log: a claim that a project belongs to a client,
// made from one directory, effective from one instant, recorded at another.
type Assertion struct {
	// ID orders two assertions recorded in the same microsecond, so a fold is
	// deterministic rather than depending on the caller's ORDER BY. This is
	// internal/adjustments' rule unchanged.
	ID int64
	// Project is the body of work, and the key this log is folded by.
	Project string
	// Client is who it belongs to.
	Client string
	// Source is the directory whose marker asserted this. It is what tells a
	// CORRECTION (one directory revising itself) from a COLLISION (two
	// directories disagreeing), which are the same two rows without it.
	Source string
	// EffectiveFrom is when the association took effect. THE ZERO TIME MEANS
	// FROM THE BEGINNING OF TIME, which is what a correction and a first
	// registration both want: registering a directory after its evidence
	// already exists is the ordinary case, and an assertion that began when it
	// was typed would attribute none of it.
	EffectiveFrom time.Time
	// RecordedAt is when somebody wrote it down. It never affects WHICH
	// instants an assertion covers — only which of two assertions covering the
	// same instant is the later word.
	RecordedAt time.Time
}

// Collision is one project two directories claim for different clients.
type Collision struct {
	// Project is the contested name.
	Project string
	// Clients are the distinct clients claiming it, sorted. Always two or more.
	Clients []string
	// Sources are the directories doing the claiming, sorted, so the report
	// names something a person can go and edit rather than only the conflict.
	Sources []string
}

// Map is the mapping in force, folded out of a log.
type Map struct {
	// byProject holds each project's surviving assertions, one per source
	// directory, already reduced to that directory's latest word.
	byProject  map[string][]Assertion
	collisions []Collision
}

// Fold resolves a log into the mapping it describes.
//
// It reads the whole log; FoldAsOf is the same thing restricted to what had been
// recorded by a given instant. Neither mutates or supersedes a row: the log is
// append-only and the fold is a pure function over it, so re-folding is free and
// a past answer is always recoverable.
func Fold(log []Assertion) Map { return FoldAsOf(log, time.Time{}) }

// FoldAsOf resolves the log AS IT STOOD at an instant, ignoring anything
// recorded later. The zero time means "everything".
//
// This is what makes the log worth its shape rather than a table of 38 rows: an
// answer given before a correction was recorded cannot depend on that
// correction, and reproducing a report as it read last month needs exactly this.
func FoldAsOf(log []Assertion, asOf time.Time) Map {
	m := Map{byProject: map[string][]Assertion{}}

	// One directory's latest word about one project, keyed by both. Reducing
	// per source FIRST is what keeps a directory's own revisions out of the
	// collision test: a correction replaces that directory's earlier claim
	// rather than standing beside it.
	latest := map[[2]string][]Assertion{}
	for _, a := range log {
		if !asOf.IsZero() && a.RecordedAt.After(asOf) {
			continue
		}
		key := [2]string{a.Project, a.Source}
		latest[key] = append(latest[key], a)
	}
	for key, as := range latest {
		slices.SortFunc(as, func(x, y Assertion) int { return order(x, y) })
		m.byProject[key[0]] = append(m.byProject[key[0]], as...)
	}

	for project, as := range m.byProject {
		if c, ok := collisionIn(project, as); ok {
			m.collisions = append(m.collisions, c)
		}
	}
	slices.SortFunc(m.collisions, func(a, b Collision) int {
		return compareStrings(a.Project, b.Project)
	})
	return m
}

// At reports which client a project belonged to at an instant.
//
// ok is false for a project nothing has asserted, for one whose assertions all
// take effect later than the instant asked about, and for one two directories
// claim at once. THOSE ARE THREE DIFFERENT FACTS and a caller that needs to tell
// them apart asks Collisions and Asserted rather than reading a false into
// whichever it expected — absence of a row is not an observation.
func (m Map) At(project string, at time.Time) (string, bool) {
	for _, c := range m.collisions {
		if c.Project == project {
			return "", false
		}
	}
	best, found := Assertion{}, false
	for _, a := range m.byProject[project] {
		if a.EffectiveFrom.After(at) {
			continue
		}
		// Later effective wins; among equally effective assertions, the later
		// recorded word wins. A correction and a reassignment differ only in
		// which of those two clauses decides them.
		if !found || a.EffectiveFrom.After(best.EffectiveFrom) ||
			(a.EffectiveFrom.Equal(best.EffectiveFrom) && order(best, a) < 0) {
			best, found = a, true
		}
	}
	if !found {
		return "", false
	}
	return best.Client, true
}

// AssertedBy reports whether a particular directory has ever said anything
// about this project.
//
// It is the question a backfill has to ask, and it is NOT the same as Asserted:
// a project several directories share may be well known to the log and still
// have never been heard from by one of them. Asking the weaker question would
// leave that directory permanently unrecorded, and its disagreement — if it
// disagreed — permanently invisible.
func (m Map) AssertedBy(project, source string) bool {
	for _, a := range m.byProject[project] {
		if a.Source == source {
			return true
		}
	}
	return false
}

// AssertedByAt reports what a particular directory says this project's client
// was at an instant, and whether it has said anything at all.
//
// It is the question `timetattle register` asks before appending: re-running a
// registration that changes nothing must append nothing, and "changes nothing"
// means THIS directory already says THIS client for the instant being asserted.
// Asking the project-wide At would answer with another directory's word and
// leave this one silently unrecorded.
func (m Map) AssertedByAt(project, source string, at time.Time) (string, bool) {
	best, found := Assertion{}, false
	for _, a := range m.byProject[project] {
		if a.Source != source || a.EffectiveFrom.After(at) {
			continue
		}
		if !found || a.EffectiveFrom.After(best.EffectiveFrom) ||
			(a.EffectiveFrom.Equal(best.EffectiveFrom) && order(best, a) < 0) {
			best, found = a, true
		}
	}
	if !found {
		return "", false
	}
	return best.Client, true
}

// Asserted reports whether anything in the log mentions this project at all,
// which is the difference between "no client yet" and "nothing was recorded".
func (m Map) Asserted(project string) bool { return len(m.byProject[project]) > 0 }

// Projects names every project the log mentions, sorted.
func (m Map) Projects() []string {
	out := make([]string, 0, len(m.byProject))
	for p := range m.byProject {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// Collisions names every project two directories claim for different clients,
// sorted by project. A caller is expected to REPORT these rather than test them:
// a fold that found none says so with an empty slice, and cannot be confused
// with a build that never looked.
func (m Map) Collisions() []Collision { return slices.Clone(m.collisions) }

// collisionIn reports whether one project's surviving assertions disagree.
//
// The test is on the LATEST word of each distinct source, which is why the
// caller reduces per source first. Two rows from one directory are a revision;
// two rows from two directories are a disagreement, and nothing but Source can
// tell them apart.
func collisionIn(project string, as []Assertion) (Collision, bool) {
	bySource := map[string]Assertion{}
	for _, a := range as {
		if cur, seen := bySource[a.Source]; !seen || order(cur, a) < 0 {
			bySource[a.Source] = a
		}
	}
	var clients, sources []string
	for source, a := range bySource {
		if !slices.Contains(clients, a.Client) {
			clients = append(clients, a.Client)
		}
		sources = append(sources, source)
	}
	if len(clients) < 2 {
		return Collision{}, false
	}
	slices.Sort(clients)
	slices.Sort(sources)
	return Collision{Project: project, Clients: clients, Sources: sources}, true
}

// order compares two assertions by when they were recorded, then by id.
//
// The id tiebreak is not decoration: two assertions written in the same
// microsecond would otherwise resolve by whatever order the rows came back in,
// and the fold would stop being a function of the log. internal/adjustments
// makes the same choice for the same reason.
func order(a, b Assertion) int {
	switch {
	case a.RecordedAt.Before(b.RecordedAt):
		return -1
	case a.RecordedAt.After(b.RecordedAt):
		return 1
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
