package source

import "slices"

// WHICH KIND OF EVIDENCE STREAM AN OBSERVATION CAME FROM.
//
// This is the fifth axis, and like WeakEvidence it is a judgment about SOURCES
// made in one place rather than a property deduced from a field. Kind says how
// a source is read, Shape says what an event means, Cause says what wrote an
// artifact and WeakEvidence says what an observation is worth. None of them
// answers the question a person asks of a week — "how much of that was Claude,
// how much was git?" — because that question is about the shape of the WORK and
// every one of those axes is about the mechanics of the reading.
//
// # Why the granular id is the wrong grain, and this is the right one
//
// knowledgec-appusage against biome-appusage is an implementation detail of two
// collectors that read the same fact off the same machine. A reader wants to
// know that an hour was spent in applications, not which of macOS's two
// databases happened to record it. Rolling the ids up is what turns a list of
// collectors into a description of a working day.
//
// # This table has exactly one copy, and that is the whole point of it living here
//
// It was written in cmd/timetattle/dashboard_sources.go, where the dashboard's
// source-attribution bar draws it, and was declared display-only: "nothing
// outside this file knows families exist". `timetattle metrics` then needed the
// same rollup for its activity mix, and two tables mapping the same ids would be
// two tables to go stale — in the direction where `dashboard` says an hour was
// Claude's and `metrics` says it was something else, with nobody subtracting.
// A grouping that two commands disagree about is worse than no grouping. So the
// judgment moved here, beside WeakEvidence, which is the same kind of judgment
// about the same subject; the dashboard's own family type is now defined in
// terms of these constants and its rendering rules are unchanged.
//
// The IDs are literals rather than references to the collectors that define
// them, for the reason attests gives: every one of those collectors imports this
// package. cmd/timetattle's TestSourceFamilyMapsEveryKnownSourceID is what
// covers the cost, running every id the built binary's collectors can emit
// through FamilyOf from a list read out of those packages.

// Family is the kind of evidence stream a source belongs to.
type Family string

// The seven families. They are named for the thing a person recognises — the
// application or the tool — rather than for an abstraction over it, because the
// question they answer is asked in those words.
const (
	// FamilyApps is application focus and machine state: biome-appusage and
	// every knowledgec- stream. It is the ambient layer, and none of it
	// carries a path, so it can never be attributed to a project.
	FamilyApps Family = "Apps"
	// FamilyBlender is a .blend1 written by the Save a person pressed.
	FamilyBlender Family = "Blender"
	// FamilyClaude is claude-transcripts: a message was typed and an answer
	// came back.
	FamilyClaude Family = "Claude"
	// FamilyFiles is fs-observations, the generic walk over registered
	// directories. It says a file changed and cannot say which application
	// changed it or whether a person did — which is exactly what separates it
	// from Blender and Motion, who name the application they came from.
	FamilyFiles Family = "Files"
	// FamilyGit is every gitlog- stream: a commit was made, a branch moved.
	FamilyGit Family = "Git"
	// FamilyJobs is every job- stream: a task was created, claimed, noted or
	// closed, and the intervals somebody held one for.
	FamilyJobs Family = "Jobs"
	// FamilyMotion is a Motion autosave, written by a fifteen-minute timer
	// that does not care whether anybody is at the machine.
	FamilyMotion Family = "Motion"
)

// FamilyOf reports which family a source id rolls up to. ok is false for an id
// no family has been decided for.
//
// AN UNKNOWN ID IS NOT A FAMILY, and callers must not invent one. A source id
// arriving here unrecognised means a collector or a manifest nobody classified
// is writing events, and the two shipped callers answer that differently on
// purpose: the dashboard fails the render, because a chart drawn with a
// grouping decision nobody made is worse than no chart, while `metrics` carries
// the observations under a row that NAMES the ids and says they are
// unclassified, because refusing a whole report over an unrecognised stream
// would withhold every figure that stream has nothing to do with. Both are the
// same refusal to guess; neither silently drops it.
//
// # Why prefix rules for three families and a literal table for the rest
//
// gitlog-, job- and knowledgec- each name a single collector emitting more than
// one stream under a shared namespace (internal/gitlog's CommitsID and ReflogID;
// internal/jobs' EventsID and ClaimsID; internal/knowledgec's four reader ids).
// A prefix rule covers a whole collector's present and future streams in one
// line, and is exactly as safe as the literal alternative since every one of
// those ids is already required to start with its collector's own namespace. The
// remaining four families each cover exactly one single-stream source, where a
// prefix rule would be a more roundabout way of writing the same one-to-one
// entry.
func FamilyOf(sourceID string) (Family, bool) {
	switch {
	case hasPrefix(sourceID, "gitlog-"):
		return FamilyGit, true
	case hasPrefix(sourceID, "job-"):
		return FamilyJobs, true
	case hasPrefix(sourceID, "knowledgec-"):
		return FamilyApps, true
	}
	switch sourceID {
	case "claude-transcripts":
		return FamilyClaude, true
	case "biome-appusage":
		return FamilyApps, true
	case "fs-observations":
		return FamilyFiles, true
	case "motion-autosave":
		return FamilyMotion, true
	case "blender-backups":
		return FamilyBlender, true
	default:
		return "", false
	}
}

// hasPrefix is strings.HasPrefix, kept local so this package's import list stays
// what it is: one standard library package, for a file that is a judgment table
// and nothing else.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Families lists every family FamilyOf can return, sorted.
//
// A tool that groups a corpus has to be able to print the rule it applied, and
// a second copy of the list kept by hand in a report is a copy that goes stale
// silently. The slice is fresh on every call so that a caller cannot rewrite the
// judgment for everybody else.
func Families() []Family {
	out := []Family{FamilyApps, FamilyBlender, FamilyClaude, FamilyFiles, FamilyGit,
		FamilyJobs, FamilyMotion}
	slices.Sort(out)
	return out
}
