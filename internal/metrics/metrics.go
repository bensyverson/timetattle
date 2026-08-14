// Package metrics turns sittings into the two numbers a working day is
// actually judged by.
//
// ENGAGED time is per-project and MAY OVERLAP, because concurrent work on two
// projects is billable to both. It is the basis for billing and for effective
// rate. PRESENCE is wall clock, is never double-counted, and is the denominator
// for capacity: a half-time retainer measured against anything else reads as
// satisfied at 30% of real attention. They cannot collapse into one metric, and
// internal/sessions' package doc names this package's task by ID for that
// reason.
//
// Nothing here is stored. Sittings and save-as chains are stored because
// clustering is expensive; these are cheap aggregations over sittings, and a
// third stored derivation would be a third thing to keep in step with the
// events. There is consequently no migration, and no way for a metric to be
// stale relative to the sessions it came from.
//
// # Where per-project time comes from, and the model that was rejected
//
// derived_session has no project column, deliberately: sittings are global
// wall-clock spans clustered over every evidence source at once, because a
// per-project clustering could produce engaged time and could never recover
// presence — two projects' sittings would each claim the same hour. Per-project
// engaged time is therefore an intersection against that skeleton.
//
// The obvious intersection is wrong, and it was measured to be wrong rather
// than argued out of. Crediting a project the WHOLE of any sitting its evidence
// appears anywhere inside ("model A") gave, on 2026-07-28 — the concurrent day
// project/private/2026-07-31-concurrent-project-validation.md documents — birch
// 6.10h against the 2.5h that document independently measures from Claude
// transcripts alone, and summed to 24.01h across a 12.78h day. What this
// package does instead is re-cluster each project's OWN evidence under the same
// parameters and intersect the result with the global sittings ("model B"),
// which gave birch 2.54h, timetattle 5.68h and acorn 5.51h against that
// document's 2.5h, 5.4h and 4.1h. Two of the three agree to within 5% of a
// figure computed from a different source by different code; acorn is higher, in
// exactly the direction that document predicts, because acorn's day left 1,102
// file outputs that a Claude-transcript measure cannot see.
//
// Cluster is a pure function over a slice of events, which is what makes this
// possible: a caller wanting one project's sittings passes one project's events
// and gets them under the same parameters. The presence readings are passed in
// alongside, so a project's sittings are trimmed by observed darkness the same
// way the global ones are, and the intersection is belt as well as braces.
//
// # Every duration says how much of it was observed
//
// 77.2% of the live corpus's 1001.9h of engaged time is MARGIN — lead-in,
// tail-out, and silence the gap threshold bridged. A metric that reports 1001.9
// hours without saying that three quarters of it was never observed by anything
// is not imprecise, it is the difference between a defensible invoice and a
// fabricated one. So there are no bare durations on the totals: a Span carries
// Total and Observed, and a caller has to name which one it wants.
//
// Applied per project, that decomposition says something sharper than the
// headline. Engaged.Observed counts only the project's OWN evidence, and the
// sources that carry the most of it — Claude transcripts, git, file saves —
// emit INSTANTS, which observe no duration.
//
// THIS COMMENT USED TO CONCLUDE that every project's Engaged.Observed is
// therefore zero and 100% of every billable hour is inferred. That was wrong,
// and had been for some time before anyone noticed: not every attributable
// source emits instants — job-claims are intervals and carry a project — so the
// column has been non-zero on the live store for as long as they have been
// collected. Measured 2026-08-14 with `timetattle metrics --all`: 178.7h
// observed against 1216.0h absolute across all projects, so 85.3% is inferred
// rather than 100%. A round number in a comment is worth re-measuring for
// exactly this reason; the note further down predicted the same drift about a
// different constant. Corroborated is the third state: how much of a
// project's engaged time some source observed WITHOUT saying whose it was, a
// Biome focus interval being the case. "acorn was observed working", "somebody
// was observed working" and "nothing was recorded" are three answers, and this
// project has shipped the two-answer version of that distinction three times.
//
// # Absolute and allocation
//
// A minute two projects were both active across either counts fully for each
// (ABSOLUTE, in Project.Engaged) or is split between them (ALLOCATION, in
// Project.Allocated). Absolute is what an invoice is built from; allocation is
// what a capacity or utilisation figure needs, and its defining property is
// that the shares sum to EXACTLY the presence they were split from, including
// the nanoseconds an even division would drop. Allocated is a bare duration
// rather than a Span on purpose: apportioning margin pro-rata would be
// arithmetic nobody measured, the same refusal internal/sessions' calendar.go
// makes for the margin of a sitting split across a day boundary.
//
// # Unattributed presence is a measurement, not a leftover
//
// It is tracked explicitly because it is the correct denominator and because a
// large bucket means something is unaccounted for. It is also not one bucket.
// Evidence carrying a path that resolved to no project MAY be recoverable, and
// this package cannot say which of it is: it is handed events and nothing else,
// so it never consults the registry and never stats a directory. A locator that
// names a live unregistered directory, one that names a directory since deleted,
// and the literal path "/" are indistinguishable here and need three different
// responses. cmd/timetattle's orient is where that distinction is drawn, because
// it is the only reader that has both the registry and the filesystem in hand;
// this figure is the population it divides. Evidence carrying no path at all,
// knowledgeC and Biome, can never be attributed by the registry however many
// directories are blessed; it needs a different mechanism or none. And clock
// that no observation is near enough to be responsible for was BRIDGED by the
// gap threshold and nothing recorded anything there at all. Collapsing the
// second and third is this project's cardinal bug.
package metrics

import (
	"fmt"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// Span is a duration together with how much of it anything actually observed.
//
// There is no accessor returning "the number", and that is the point. A caller
// has to write Total or Observed, so a figure that is three-quarters inference
// cannot be mistaken for a measurement by anybody reading the call site.
type Span struct {
	// Total is the wall clock covered.
	Total time.Duration
	// Observed is the part of it an observation covered. The rest was
	// inferred by the clustering.
	Observed time.Duration
}

// Margin is the inferred part: lead-in, tail-out and bridged silence.
func (s Span) Margin() time.Duration { return s.Total - s.Observed }

// MarginShare is the inferred part as a fraction, and zero over nothing.
func (s Span) MarginShare() float64 {
	if s.Total == 0 {
		return 0
	}
	return s.Margin().Seconds() / s.Total.Seconds()
}

// Reason says why a stretch of presence belongs to no project.
type Reason string

const (
	// ReasonUnregistered is evidence carrying a path that resolved to no
	// project. It is the actionable bucket: blessing the directory attributes
	// the time.
	ReasonUnregistered Reason = "unregistered"
	// ReasonUnlocatable is evidence carrying no path at all. knowledgeC and
	// Biome are the whole of it, and no amount of registering will ever reach
	// them — attributing them needs a different mechanism, which is a
	// different fact from "nobody has registered that folder yet".
	ReasonUnlocatable Reason = "unlocatable"
	// ReasonBridged is clock no observation is near enough to be responsible
	// for: silence the gap threshold decided to treat as one sitting. Nothing
	// was recorded here, which is neither "observed and unattributable" nor
	// "observed and unregistered".
	ReasonBridged Reason = "bridged"
	// ReasonAsserted is a person having said the span belonged to no project.
	//
	// It is a FOURTH reason and not a variant of the other three, which are all
	// ways the machine failed to attribute something and all of which suggest an
	// action — register the directory, or accept that registering can never
	// reach it. This one says the attribution is settled and correct: somebody
	// looked and the hour was nobody's. Filing it under any of the others would
	// send a reader to fix a registry that has nothing wrong with it.
	ReasonAsserted Reason = "asserted"
)

// Segment is one atomic piece of presence: a span of wall clock across the
// whole of which the same projects were active and the same things were true.
//
// The segments partition presence exactly, in order, with no gap and no
// overlap, and every total in a Report is a sum over them. That is what makes a
// figure here falsifiable rather than merely produced — a caller can re-derive
// any of them, and internal/metrics' own tests do.
//
// It is also what a report grouping by working day needs (task XF3Oz): the
// segments can be bucketed by a sessions.Calendar without re-clustering
// anything.
type Segment struct {
	Start, End time.Time
	// Projects active across this segment, sorted. Empty means unattributed.
	Projects []string
	// Clients active across this segment, sorted. It is NOT derivable from
	// Projects by a caller — that would need the project-to-client mapping,
	// which is exactly what this package refuses to consult a registry for —
	// and it is not the same population either: a project whose client never
	// resolved appears in Projects and in no client.
	Clients []string
	// Evidenced names the projects whose OWN evidence observed this segment,
	// a subset of Projects. Empty is the ordinary case and is a finding rather
	// than a gap: no attributable source observes a duration.
	Evidenced []string
	// Attested names the projects here that an observation RECORDING AN ACT
	// accounts for — a message, a commit, a claim held — and Weak names the
	// ones only a file's modification time accounts for. Both are subsets of
	// Projects, they never share a name, and a project in neither is one that
	// nothing at all accounts for here: silence the gap threshold bridged
	// inside its own cluster. Those are three answers and this project has
	// shipped the two-answer version of that distinction repeatedly.
	Attested []string
	Weak     []string
	// Observed says some source — attributable or not — observed this
	// stretch. It is the difference between "the machine was in use" and "the
	// clustering bridged a silence".
	Observed bool
	// Unattributed says why nothing owns this segment, and is empty when
	// something does.
	Unattributed Reason
}

// Duration is how long the segment lasted.
func (s Segment) Duration() time.Duration { return s.End.Sub(s.Start) }

// Project is one client project's share of the clock.
type Project struct {
	// Name is the project as the registry resolved it.
	Name string
	// Events is how many attributed observations this rests on.
	Events int
	// Engaged is ABSOLUTE: every instant this project was active counts in
	// full, so two projects that shared an hour are each credited the hour.
	// This is the billing figure. Observed counts only this project's own
	// evidence — see the package doc for why it is zero on real data today.
	Engaged Span
	// Allocated is ALLOCATION: an instant k projects shared gives each 1/k of
	// it. The shares sum to exactly the attributed presence, to the
	// nanosecond. This is the capacity figure.
	Allocated time.Duration
	// Corroborated is how much of Engaged some source observed WITHOUT saying
	// whose work it was — a Biome focus interval running underneath. It is
	// evidence the machine was in use, and none at all about the client.
	Corroborated time.Duration
	// Attested, Weak and Unaccounted partition Engaged.Total, and they answer
	// a different question from Corroborated. Corroborated asks whether the
	// MACHINE was observed in use, which on this corpus is mostly a question
	// about whether Biome and knowledgeC were alive at the time. These ask
	// what accounts for the hours THIS PROJECT is being credited:
	//
	//   Attested    an observation recording something a person did — a
	//               message, a commit, a claim held — is responsible for it.
	//   Weak        nothing but a file's modification time is responsible for
	//               it. A log-rotation cron writes those, which is how one
	//               project came to read 135.2h.
	//   Unaccounted no observation of this project is responsible for it at
	//               all: silence the gap threshold bridged inside the
	//               project's own cluster. The third state, and it is neither
	//               of the other two.
	//
	// Weak and Corroborated can BOTH be true of the same minute and do not
	// cancel: a focus interval says somebody was at the machine, and says
	// nothing whatever about which client.
	Attested    time.Duration
	Weak        time.Duration
	Unaccounted time.Duration
	// Asserted is how much of Engaged.Total this project holds ONLY because a
	// person said so — clock no evidence of its own reaches, moved here by a
	// recorded attribution correction. It is always part of Unaccounted, since
	// an assertion is not an observation and never becomes one, and it is named
	// separately because "nobody recorded anything here" and "somebody looked
	// and told us whose it was" are opposite kinds of ignorance wearing the same
	// number. Zero unless Corrected was given assertions.
	Asserted time.Duration
	// Sittings is the length distribution of this project's own active spans.
	Sittings Distribution
}

// Client is one client's share of the clock, rolled up across every project of
// theirs.
//
// A ROLL-UP IS NEVER A SUM, and that is the whole reason this type exists rather
// than a caller adding Project rows together. Engaged is ABSOLUTE: a minute two
// projects were both active in counts in full for each, which is what makes it
// the invoice figure. Add three of one client's project rows up and the client
// is credited three times for a minute they worked once.
//
// IT IS NOT THE UNION OF THOSE ROWS EITHER, and this doc comment said for months
// that it was. A client's hours are its OWN evidence — every one of its
// projects' observations at once — clustered again under the same parameters and
// intersected with the global sittings, exactly as a project's hours are derived
// one level down. Clustering does not distribute over a union, so the two are
// not the same figure: a silence too wide for any single project to bridge can
// be narrow enough to bridge once the client's evidence is taken together, and
// the client is then credited a stretch none of its projects is credited. On the
// live store that surplus is 19m13s of 617h40m54s, in 11 stretches, measured by
// `go run ./scripts/rollupunion` on 2026-08-13.
//
// THE SURPLUS IS WORK AND NOT AN ARTEFACT, which is why it is kept. Moving
// between two of one client's projects costs attention that neither project's
// own evidence records, so a stretch bridged only at the client level is that
// switching cost made visible. A client's hours are what that client's evidence
// clusters to when taken together, and the projects' sum is the figure that is
// missing something.
//
// TWO CONSEQUENCES A READER OF A REPORT SHOULD KNOW. A client can exceed the sum
// of its projects, so a report where it does is correct rather than broken — the
// check that used to refuse exactly that report is gone, and what replaced it is
// at the close of Compute. And clock a client holds that no project of any client
// holds is ALSO printed under unattributed presence as silence the gap threshold
// bridged: the same minutes appear in two sections, once as this client's and
// once as clock nothing could be attributed. Both statements are true of it.
//
// The distinction is invisible where a client has one project — one row is the
// whole of its evidence — and in the live registry every client is 1:1 except
// one, which spans 33 projects. So the whole of it sits on that one shape.
//
// Allocated divides by CLIENT and not by project, which is a different split
// rather than the same one re-added. A minute two of one client's projects and
// one of another's were all active in gives each PROJECT a third and each
// CLIENT a half. Allocation is the capacity figure, and capacity asks how the
// day was divided between the people being worked for; a client who keeps more
// repositories does not thereby consume more of the day.
type Client struct {
	// Name is the client as the registry resolved it.
	Name string
	// Projects names the projects rolled up here, sorted, so a figure one
	// level up can be traced to the rows it came from rather than trusted.
	Projects []string
	// Events is how many attributed observations this rests on.
	Events int
	// Engaged is ABSOLUTE, and is this client's own evidence re-clustered
	// rather than its projects' spans merged: it can exceed both their sum and
	// their union, by the silence only their combined evidence bridges. The
	// same decomposition as Project.Engaged: Observed counts only this
	// client's own evidence, which is zero on real data for the reason the
	// package doc gives.
	Engaged Span
	// Allocated is ALLOCATION, divided among the CLIENTS active in an
	// instant. The shares sum to exactly the presence at least one client was
	// attributed, to the nanosecond.
	Allocated time.Duration
	// Corroborated is how much of Engaged some source observed WITHOUT saying
	// whose work it was.
	Corroborated time.Duration
	// Attested, Weak and Unaccounted partition Engaged.Total, and mean exactly
	// what the same three fields on Project mean.
	Attested    time.Duration
	Weak        time.Duration
	Unaccounted time.Duration
	// Asserted is the same figure Project.Asserted is, one level up: clock this
	// client holds because a person named one of its projects for it.
	Asserted time.Duration
	// Sittings is the length distribution of this client's own active spans.
	// It cannot be summed from its projects' distributions at all, which is a
	// second reason a roll-up is computed rather than accumulated.
	Sittings Distribution
}

// Unattributed is presence no project could be attributed, and why.
type Unattributed struct {
	// Presence is the whole of it. Observed is the part an observation
	// actually spanned, and it is NOT the sum of the figures below: an
	// instant is RESPONSIBLE for the lead-in and tail-out around it and
	// OBSERVES neither, so Observed is always the smaller of the two. Every
	// source that can be unattributed-by-registry emits instants, so on real
	// data Observed here is a small fraction of Unregistered.
	Presence Span
	// Unregistered, Unlocatable, Asserted and Bridged partition Presence.Total.
	// See Reason for what each one means and why they are four buckets and not
	// one. Asserted is zero unless Corrected was given assertions.
	Unregistered time.Duration
	Unlocatable  time.Duration
	Asserted     time.Duration
	Bridged      time.Duration
	// UnregisteredEvents and UnlocatableEvents count the observations behind
	// the first two, so a large bucket can be traced to how much evidence is
	// sitting in it rather than only to how long it ran.
	UnregisteredEvents int
	UnlocatableEvents  int
}

// Backlight is how much of Presence the presence sources actually witnessed.
//
// It exists because Presence is NOT screen-on time and a reader will assume it
// is. Presence is the clock the SITTINGS cover, clustered out of evidence; the
// backlight only bounds them, by splitting a sitting where the screen was
// observed dark. Measured on the live store on 2026-08-01, `timetattle metrics`
// at the shipped defaults: the backlight witnessed 252.1h of the 1001.9h of
// presence and said nothing whatever about the other 749.9h, because knowledgeC
// prunes itself in 9–28 days while the Claude transcript corpus reaches back
// far past that.
//
// So a capacity denominator resting on the backlight would be resting on a
// quarter of the corpus, silently. These four figures are what make that
// visible rather than something a reader has to already know.
type Backlight struct {
	// Readings is how many presence observations the whole of this rests on.
	Readings int
	// Lit is the union of the 'on' readings, wherever they fall.
	Lit time.Duration
	// Corroborated is the part of Presence a lit reading covers: the screen
	// was observed on and evidence was observed too.
	Corroborated time.Duration
	// LitOutsideASitting is lit clock no sitting covers — the screen was on
	// and nothing was observed happening. Reading, a call, coffee before the
	// day starts; ground-truth.md finding 4 is that this is a consistent,
	// same-signed bias rather than noise.
	LitOutsideASitting time.Duration
	// Unwitnessed is presence the backlight said NOTHING about, neither on
	// nor off. It is the third state, and reading it as darkness is the bug
	// this project has shipped in three separate tools.
	Unwitnessed time.Duration
}

// Report is both metrics and everything needed to interpret them.
type Report struct {
	// Params is what produced these figures. It travels with them for the
	// same reason a stored session carries its own: the gap threshold moves
	// total engaged time by 2.3x across the plausible range, so a number
	// separated from it means nothing.
	Params sessions.Params
	// Presence is wall clock at the machine, counted once however many
	// projects were being worked in it.
	Presence Span
	// Sittings is the length distribution of the spans Presence is the sum
	// of. Reported alongside the total because the two answer different
	// questions and a total alone cannot tell a sustained day from a
	// fragmented one.
	Sittings Distribution
	// Projects, sorted by name.
	Projects []Project
	// Clients, sorted by name: the same accounting rolled up one level, by
	// re-clustering each client's own evidence rather than by adding or
	// merging the project rows. See Client, which says what that makes it
	// larger than.
	Clients []Client
	// Mix is the ACTIVITY MIX: which kinds of evidence stream observed this
	// clock, and how much of it none of them reaches. It is the only figure in
	// the report about what KIND of work was done rather than whose it was, and
	// its rows overlap and never sum — see ActivityMix and the head of
	// families.go.
	Mix ActivityMix
	// Clientless is presence a PROJECT was attributed but no CLIENT was: the
	// project resolved, and the client-project log had no single answer for it
	// at that instant.
	//
	// IT IS A FOURTH FIGURE AND IS NOT PART OF Unattributed, which is presence
	// no project could be attributed at all. Folding it into any of those three
	// would say a directory is unregistered when it is registered and its
	// client is merely unrecorded — a different fact calling for a different
	// action. It is also not the same as a client with zero hours. See
	// internal/clientmap for the three ways a project can have no client:
	// nothing ever asserted one, an assertion exists but takes effect later
	// than this instant, or two directories claim it for different clients.
	Clientless time.Duration
	// Unattributed is the rest of presence, and why.
	Unattributed Unattributed
	// Corrections is how much of this report is a person's word rather than the
	// machine's answer, and how much of what they said moved nothing. It is the
	// zero value on a report nobody has corrected, which is a different fact
	// from a report whose corrections all agreed.
	Corrections Corrections
	// Backlight is how much of Presence the presence sources witnessed, and
	// how much of it they were silent about.
	Backlight Backlight
	// Segments is the partition every figure above is summed from.
	Segments []Segment
	// Sessions is the clustering this rests on, and Stats its own accounting
	// of what it read, inferred and dropped.
	Sessions []sessions.Session
	Stats    sessions.Stats
}

// Project finds one project's metrics by name.
func (r Report) Project(name string) (Project, bool) {
	for _, p := range r.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// Client finds one client's roll-up by name.
//
// A client and a project may carry the same name, and in the live registry one
// pair does. The two are looked up in separate lists for that reason; keying
// both out of one map would silently merge them into a row that is neither.
func (r Report) Client(name string) (Client, bool) {
	for _, c := range r.Clients {
		if c.Name == name {
			return c, true
		}
	}
	return Client{}, false
}

// Compute derives both metrics from a slice of events, with no human
// correction laid over them: the machine's answer, alone.
//
// It is pure, like the clustering it rests on: the same events under the same
// parameters give the same report, so a figure can be re-run and argued with
// rather than being a property of a database.
func Compute(events []source.Event, p sessions.Params) (Report, error) {
	return Corrected(events, p, nil)
}

// Corrected derives the same metrics with a person's attribution assertions
// laid over the machine's answer.
//
// said is a FOLDED log — adjustments.Overlay.Attributions — so the pieces are
// disjoint and every "a correction to a correction" has already been resolved
// by the time it arrives. An empty Project is the assertion that a span
// belonged to NO project; the third state, nobody having said, is the absence
// of a piece, and reading a missing piece as an empty one would turn every
// unexamined hour in the corpus into a correction nobody made.
//
// # What an attribution assertion may and may not move
//
// It moves WHOSE an hour was. It never moves whether there was one. Presence,
// the sitting distribution and the backlight figures are byte for byte what
// Compute produces, and an assertion reaching clock no sitting covers moves
// nothing at all — reported as Corrections.Outside rather than dropped, because
// a correction that silently changed nothing is exactly the failure this
// function exists to end.
//
// ENGAGED AND NOT-ENGAGED ASSERTIONS ARE DELIBERATELY NOT APPLIED HERE, though
// the same folded log carries them. They move the capacity denominator every
// share in the report is divided by, and internal/fit measures the model
// against precisely that difference: the residual between what the clustering
// said and what the reviewer said IS the error signal, and a metrics report
// that had already absorbed the reviewer's engagement corrections would shrink
// it toward zero for reasons having nothing to do with the model improving.
// Attribution carries no such hazard, because no parameter being fitted is an
// attribution parameter. Applying the other two needs its own case made.
//
// # Corrections are laid over the ATTRIBUTION and never over the clustering
//
// The assertions are applied to each level's active spans after every
// clustering has run, never to the events going into one. Rewriting an event's
// project before sessions.Cluster would make the correction an input to the
// model it is meant to measure — the objection internal/adjustments' package
// doc makes at length — and it could not express the case that matters most
// anyway: a person can reattribute a stretch of bridged silence, where there is
// no event to rewrite.
func Corrected(events []source.Event, p sessions.Params, said []Attribution) (Report, error) {
	return Watched(events, p, said, nil)
}

// Watched is Corrected, saying which phase it is in as it goes.
//
// It is the same computation and returns the same report — see Corrected for
// what any of it means, and watch.go for why the instrument lives inside this
// package rather than in a stopwatch around the call. A nil Watcher makes this
// Corrected exactly.
func Watched(events []source.Event, p sessions.Params, said []Attribution, w Watcher) (Report, error) {
	v := watch{w}

	v.begin(PhaseCluster, 0)
	global, err := sessions.Cluster(events, p)
	v.done(PhaseCluster)
	if err != nil {
		return Report{}, err
	}

	r := Report{Params: p, Sessions: global.Sessions, Stats: global.Stats}
	presence := make([]span, 0, len(global.Sessions))
	for _, s := range global.Sessions {
		presence = append(presence, span{s.Start, s.End})
	}
	r.Sittings = distribute(presence)
	if len(presence) == 0 {
		return r, nil
	}

	roles := rolesOf(p)

	v.begin(PhaseGroup, 0)

	// EVERY BUCKET IS SIZED BEFORE ANYTHING IS PUT IN IT. Growing a
	// map-of-slices by append costs roughly five times the bytes it ends up
	// holding, because each of a slice's regrowths allocates a new backing
	// array and abandons the old one; over a corpus of a million events that
	// was the largest allocating line in this function, and all of it garbage
	// the collector then had to walk.
	//
	// This pass is a HINT and not a contract. If it ever counted differently
	// from the pass below — a role table read twice, a condition that drifted
	// apart — append still produces exactly the same buckets, just at the old
	// cost. Nothing here can make the result wrong, which is why it is allowed
	// to duplicate the two conditions rather than being fused into the loop it
	// sizes.
	perProject := make(map[string]int)
	perClient := make(map[string]int)
	perFamily := make(map[string]int)
	for _, e := range events {
		if role := roles[e.Source]; role == roleIgnored || role == rolePresence {
			continue
		}
		// Counted BEFORE the attribution test below, because the activity mix
		// holds every evidence source and not only the ones that resolve to a
		// directory: Biome and knowledgeC carry no path, appear in no project
		// row anywhere in the report, and are exactly the streams a reader
		// asking what KIND of work this was wants counted.
		perFamily[familyOf(e.Source)]++
		if e.Identity.Project == "" {
			continue
		}
		perProject[e.Identity.Project]++
		if e.Identity.Client != "" {
			perClient[e.Identity.Client]++
		}
	}
	var (
		byProject = make(map[string][]source.Event, len(perProject))
		// The client level is grouped in its own map rather than sharing the
		// one above. A client and a project may carry the SAME NAME — in the
		// live registry one pair does — and one map keyed on the bare name
		// would merge them into a bucket that is neither.
		byClient = make(map[string][]source.Event, len(perClient))
		// The activity mix's buckets, and the source ids that actually landed
		// in each of them. The ids are collected from the CORPUS rather than
		// read back out of the mapping table, so the report names the streams
		// that produced the figures beside them and a collector nobody
		// classified appears by name the first time it harvests.
		byFamily      = make(map[string][]source.Event, len(perFamily))
		familyIDs     = make(map[string]map[string]bool, len(perFamily))
		clientOwns    = map[string]map[string]bool{}
		bounds        []source.Event
		unregistered  []source.Event
		unlocatable   []source.Event
		observedRaw   = make([]span, 0, len(events))
		unregResponse []span
		unlocResponse []span
		lit, dark     []span
	)
	for name, n := range perProject {
		byProject[name] = make([]source.Event, 0, n)
	}
	for name, n := range perClient {
		byClient[name] = make([]source.Event, 0, n)
	}
	for name, n := range perFamily {
		byFamily[name] = make([]source.Event, 0, n)
		familyIDs[name] = map[string]bool{}
	}
	for _, e := range events {
		switch role := roles[e.Source]; {
		case role == roleIgnored:
			continue
		case role == rolePresence:
			bounds = append(bounds, e)
			r.Backlight.Readings++
			// An unbounded reading observed one edge and no extent, and a
			// state that is neither on nor off is a stream this package does
			// not understand. Neither witnesses anything, and counting them
			// as either would be guessing at a polarity.
			if e.Shape != source.Unbounded {
				switch e.State {
				case "on":
					lit = append(lit, span{e.Start, e.End})
				case "off":
					dark = append(dark, span{e.Start, e.End})
				}
			}
			continue
		}
		observedRaw = append(observedRaw, rawSpan(e))
		// Filed by KIND of stream before it is filed by whose work it was.
		// These two questions are asked of the same observation and the answers
		// are independent: an unattributable focus interval still says what kind
		// of work was going on.
		fam := familyOf(e.Source)
		byFamily[fam] = append(byFamily[fam], e)
		if familyIDs[fam] == nil {
			familyIDs[fam] = map[string]bool{}
		}
		familyIDs[fam][e.Source] = true
		switch {
		case e.Identity.Project != "":
			byProject[e.Identity.Project] = append(byProject[e.Identity.Project], e)
			// A project whose client never resolved is filed under no client
			// at all. An empty name would read in a report as a real client,
			// which is the "observed not-X versus nothing was recorded"
			// collapse this package exists to refuse.
			if c := e.Identity.Client; c != "" {
				byClient[c] = append(byClient[c], e)
				if clientOwns[c] == nil {
					clientOwns[c] = map[string]bool{}
				}
				clientOwns[c][e.Identity.Project] = true
			}
		case e.Locator != "":
			unregistered = append(unregistered, e)
			unregResponse = append(unregResponse, responsibleFor(e, p))
		default:
			unlocatable = append(unlocatable, e)
			unlocResponse = append(unlocResponse, responsibleFor(e, p))
		}
	}

	// Raw spans for "was this instant observed", expanded ones for "what is
	// responsible for this instant being inside a sitting at all". They are
	// different questions and this package has to answer both: a corpus of
	// instants observes no duration whatsoever, so classifying unattributed
	// presence by raw coverage would report every one of its hours as bridged
	// silence and lose the actionable bucket entirely.
	// What the backlight witnessed, against what the sittings claim. Presence
	// less both readings is the third state: clock the stream was SILENT
	// about, which is neither lit nor dark and must never be read as either.
	litU, darkU := union(lit), union(dark)
	r.Backlight.Lit = total(litU)
	r.Backlight.Corroborated = total(intersect(presence, litU))
	r.Backlight.LitOutsideASitting = total(subtract(litU, presence))
	r.Backlight.Unwitnessed = total(subtract(subtract(presence, litU), darkU))

	observed := intersect(union(observedRaw), presence)
	unregCover := intersect(union(unregResponse), presence)
	unlocCover := intersect(union(unlocResponse), presence)

	v.done(PhaseGroup)

	// Both levels of attribution are reduced the same way, by the same code.
	// The client roll-up is NOT the project rows re-added: it re-clusters each
	// client's own evidence and intersects that with presence, exactly as a
	// project's is derived, because absolute engaged time overlaps and a sum of
	// overlapping spans credits shared clock more than once. See Client.
	projects, err := reduce(byProject, bounds, presence, p, v, PhaseProjects)
	if err != nil {
		return Report{}, err
	}
	clients, err := reduce(byClient, bounds, presence, p, v, PhaseClients)
	if err != nil {
		return Report{}, err
	}
	// AND THE SAME REDUCTION A THIRD TIME, one axis over. The other two ask
	// whose an hour was; this one asks what KIND of work it was, and the answer
	// is a union for exactly the same reason theirs is — a commit made during a
	// Claude session is observed by both streams and neither can be made to give
	// the minute up. Re-clustered rather than accumulated from anything above,
	// because the family buckets cut across every project and client in the
	// report and nothing already computed sums to them.
	//
	// It costs one more pass of the same shape, and it is bounded where the
	// other two are not: there are at most eight families however many projects
	// a store holds.
	mix, err := reduce(byFamily, bounds, presence, p, v, PhaseFamilies)
	if err != nil {
		return Report{}, err
	}
	if r.Mix, err = mixOf(mix, byFamily, familyIDs, presence); err != nil {
		return Report{}, err
	}

	// Opened HERE rather than at the carve below, so that the phases account
	// for the whole of this function and a timing table built from them adds up
	// to the run. The human correction layer is part of the same pass by more
	// than bookkeeping: an assertion that a span belonged to nobody contributes
	// a cut, so the corrections have to be resolved before presence can be
	// carved at all. Closed by defer, because everything from here to the
	// return is one phase and several of the arithmetic checks below return
	// early.
	v.begin(PhaseSegments, 0)
	defer v.done(PhaseSegments)

	// THE HUMAN CORRECTION LAYER, applied here and nowhere earlier.
	//
	// Both levels are corrected by the same code against the same resolved log.
	// The client level gets its target through the project-to-client mapping the
	// report has ALREADY derived from its own events — clientOwns — rather than
	// from a registry, which this package reads nothing of. A project the corpus
	// cannot place, or one two clients claim, resolves to no client at all and
	// its asserted hours land in Clientless, which is a figure that already
	// exists and already says what it means. Guessing an owner there would be
	// last-writer-wins dressed as a roll-up.
	var (
		assertedNone []span
		gainedBy     map[string][]span
		gainedFor    map[string][]span
	)
	if len(said) > 0 {
		// Read BEFORE anything is corrected: what the machine said is the
		// baseline the four correction figures are measured against.
		if r.Corrections, err = account(said, presence, projects.active); err != nil {
			return Report{}, err
		}
		owner, contested := ownersOf(clientOwns)
		gainedBy = projects.correct(said, presence, func(a Attribution) string { return a.Project })
		gainedFor = clients.correct(said, presence, func(a Attribution) string {
			if a.Project == "" || contested[a.Project] {
				return ""
			}
			return owner[a.Project]
		})
		assertedNone = assertedToNobody(said, presence)
	}
	names, active := projects.names, projects.active

	// Cut presence at every boundary that could make one half of a piece
	// different from the other, so that each segment is wholly inside or
	// wholly outside each run and there is no partial answer to represent.
	// BOTH levels contribute cuts: a client's span can begin where no project's
	// does not, but the reverse is not true of the client segments a roll-up is
	// summed from, and a missing cut makes cursor.covers report false for a
	// whole piece and quietly loses part of an hour.
	edges := cuts(observed, unregCover, unlocCover)
	edges = append(edges, projects.cuts()...)
	edges = append(edges, clients.cuts()...)
	// An assertion that a span belonged to NOBODY moves no active run at all,
	// so it contributes no cut through either level above and has to bring its
	// own. Without it a segment straddles the edge of the assertion, covers
	// reports false for the whole of it, and the reason silently reverts to
	// whichever of the machine's three the rest of the segment carried.
	edges = append(edges, cuts(assertedNone)...)
	pieces := carve(presence, edges)

	observedAt := &cursor{spans: observed}
	unregAt := &cursor{spans: unregCover}
	unlocAt := &cursor{spans: unlocCover}
	assertedNoneAt := &cursor{spans: assertedNone}
	projectAt, clientAt := projects.cursors(), clients.cursors()

	totals := make(map[string]*Project, len(names))
	for _, name := range names {
		totals[name] = &Project{Name: name, Events: len(byProject[name])}
	}
	clientTotals := make(map[string]*Client, len(clients.names))
	for _, name := range clients.names {
		owned := make([]string, 0, len(clientOwns[name]))
		for project := range clientOwns[name] {
			owned = append(owned, project)
		}
		slices.Sort(owned)
		clientTotals[name] = &Client{Name: name, Projects: owned, Events: len(byClient[name])}
	}
	// Presence at least one client was attributed, accumulated beside the
	// segments so the client allocation can be reconciled against the clock it
	// was actually divided from. It is not the same population as the presence
	// the PROJECTS cover: a project whose client never resolved is attributed
	// at one level and not the other, and asserting them equal would be
	// asserting something no pass guarantees.
	var clientPresence time.Duration

	r.Segments = make([]Segment, 0, len(pieces))
	for _, piece := range pieces {
		seg := Segment{Start: piece.start, End: piece.end, Observed: observedAt.covers(piece)}
		for _, name := range names {
			if !projectAt.active[name].covers(piece) {
				continue
			}
			seg.Projects = append(seg.Projects, name)
			if projectAt.evidenced[name].covers(piece) {
				seg.Evidenced = append(seg.Evidenced, name)
			}
			// Priority, stated: where an act and a file timestamp both account
			// for the same instant, the act is the true answer. A project is
			// not made weaker by ALSO having saved a file.
			switch {
			case projectAt.attested[name].covers(piece):
				seg.Attested = append(seg.Attested, name)
			case projectAt.weak[name].covers(piece):
				seg.Weak = append(seg.Weak, name)
			}
		}
		for _, name := range clients.names {
			if clientAt.active[name].covers(piece) {
				seg.Clients = append(seg.Clients, name)
			}
		}

		d := piece.length()
		r.Presence.Total += d
		if seg.Observed {
			r.Presence.Observed += d
		}

		if len(seg.Clients) == 0 && len(seg.Projects) > 0 {
			// Attributed to a project and to nobody above it. Counted here and
			// nowhere else: it is not unattributed presence, because a project
			// owns it.
			r.Clientless += d
		}
		if len(seg.Clients) > 0 {
			clientPresence += d
			for _, name := range seg.Clients {
				clientTotals[name].Engaged.Total += d
				if seg.Observed {
					clientTotals[name].Corroborated += d
				}
				if clientAt.evidenced[name].covers(piece) {
					clientTotals[name].Engaged.Observed += d
				}
				switch {
				case clientAt.attested[name].covers(piece):
					clientTotals[name].Attested += d
				case clientAt.weak[name].covers(piece):
					clientTotals[name].Weak += d
				}
			}
			// Divided among the CLIENTS present, not re-added from the
			// per-project split. Two of one client's projects and one of
			// another's give each project a third and each client a half.
			for name, share := range allocate(d, seg.Clients) {
				clientTotals[name].Allocated += share
			}
		}

		if len(seg.Projects) == 0 {
			// Priority, stated: an unregistered directory is actionable and a
			// source with no path is not, so where both cover the same
			// instant the actionable answer is the useful one to report. A
			// PERSON'S ASSERTION OUTRANKS BOTH: the other three say why the
			// machine could not attribute the span, and this one says it has
			// been attributed — to nobody — by somebody who was there.
			switch {
			case assertedNoneAt.covers(piece):
				seg.Unattributed = ReasonAsserted
				r.Unattributed.Asserted += d
			case unregAt.covers(piece):
				seg.Unattributed = ReasonUnregistered
				r.Unattributed.Unregistered += d
			case unlocAt.covers(piece):
				seg.Unattributed = ReasonUnlocatable
				r.Unattributed.Unlocatable += d
			default:
				seg.Unattributed = ReasonBridged
			}
			r.Unattributed.Presence.Total += d
			if seg.Observed {
				r.Unattributed.Presence.Observed += d
			}
			r.Segments = append(r.Segments, seg)
			continue
		}

		for _, name := range seg.Projects {
			totals[name].Engaged.Total += d
			if seg.Observed {
				totals[name].Corroborated += d
			}
		}
		for _, name := range seg.Evidenced {
			totals[name].Engaged.Observed += d
		}
		for _, name := range seg.Attested {
			totals[name].Attested += d
		}
		for _, name := range seg.Weak {
			totals[name].Weak += d
		}
		for name, share := range allocate(d, seg.Projects) {
			totals[name].Allocated += share
		}
		r.Segments = append(r.Segments, seg)
	}
	r.Unattributed.Bridged = r.Unattributed.Presence.Total -
		r.Unattributed.Unregistered - r.Unattributed.Unlocatable - r.Unattributed.Asserted
	r.Unattributed.UnregisteredEvents = len(unregistered)
	r.Unattributed.UnlocatableEvents = len(unlocatable)

	r.Projects = make([]Project, 0, len(names))
	for _, name := range names {
		totals[name].Sittings = distribute(active[name])
		// The remainder, computed rather than accumulated, so it cannot be
		// anything other than what the other two leave behind.
		totals[name].Unaccounted = totals[name].Engaged.Total -
			totals[name].Attested - totals[name].Weak
		totals[name].Asserted = total(gainedBy[name])
		r.Projects = append(r.Projects, *totals[name])
	}
	r.Clients = make([]Client, 0, len(clients.names))
	for _, name := range clients.names {
		clientTotals[name].Sittings = distribute(clients.active[name])
		clientTotals[name].Unaccounted = clientTotals[name].Engaged.Total -
			clientTotals[name].Attested - clientTotals[name].Weak
		clientTotals[name].Asserted = total(gainedFor[name])
		r.Clients = append(r.Clients, *clientTotals[name])
	}

	// The report checks its own arithmetic before anybody reads it, against a
	// figure computed a different way rather than against itself.
	//
	// Summing the segments and summing the runs the segments were carved out
	// of are two independent computations of the same clock. If they disagree,
	// the segmentation is missing a cut: a piece straddles a boundary,
	// cursor.covers reports false for the whole of it, and a project quietly
	// loses part of an hour. That produces an entirely plausible report and no
	// symptom whatever, which is the shape of silent wrongness this project
	// has shipped before. Removing the active-span cuts and re-running the
	// tests drives every project's engaged time to zero, and this is what
	// turns that into a sentence rather than a plausible number.
	if got := total(presence); got != r.Presence.Total {
		return Report{}, fmt.Errorf("the segments cover %v against %v of sittings; "+
			"they do not tile presence and every figure resting on them is wrong", r.Presence.Total, got)
	}
	for _, p := range r.Projects {
		if got := total(active[p.Name]); got != p.Engaged.Total {
			return Report{}, fmt.Errorf("%s was engaged %v by the segments and %v by its own spans, "+
				"off by %v; the segmentation is missing a cut", p.Name, p.Engaged.Total, got,
				p.Engaged.Total-got)
		}
	}
	// The same check one level up, and it is not redundant. A client's spans
	// are a SEPARATE clustering, so a client boundary that no project boundary
	// happens to coincide with is a cut only the client level contributes — and
	// a missing one produces an entirely plausible roll-up that is quietly short
	// of an hour, which is the shape of wrongness this project has shipped
	// before.
	for _, c := range r.Clients {
		if got := total(clients.active[c.Name]); got != c.Engaged.Total {
			return Report{}, fmt.Errorf("client %s was engaged %v by the segments and %v by its own "+
				"spans, off by %v; the segmentation is missing a cut", c.Name, c.Engaged.Total, got,
				c.Engaged.Total-got)
		}
		// A CLIENT WITH ONE PROJECT IS THAT PROJECT'S OWN EVIDENCE CLUSTERED
		// AGAIN, so it can never hold more clock than the project does.
		//
		// This is what is left of a check that used to compare EVERY client
		// against the SUM of its projects, on the belief that a roll-up is the
		// union of their spans. That belief is false: clustering does not
		// distribute over a union, so where a client's projects are worked
		// alternately the client bridges silences none of them bridges and
		// legitimately exceeds the sum. The check refused whole reports over
		// correct figures — no row missing, no report at all — and it did not
		// catch the addition-shaped bug it was written for, which the equality
		// immediately above catches first, every time.
		//
		// What it DID catch, established by mutation on the live store: the
		// client level clustered at a wider gap than the projects, inflating a
		// client's own spans while its segments and spans still agreed with each
		// other. The whole test suite passed under that mutation and this check
		// alone refused it, on a client with ONE project, by 3h38m2.96s. Five of
		// the six clients in that store own one project, so this is not a narrow
		// case; it is where the containment is exact.
		//
		// IT IS AN INEQUALITY AND NOT AN EQUALITY, deliberately. A project is
		// stamped with its client per observation from a log with effective
		// dates, so a directory registered to a client partway through its life
		// has earlier evidence that is the project's and no client's. The
		// project then holds clock its client does not — the report calls that
		// Clientless — and an equality here would refuse the whole report over
		// an ordinary registration. A project worked for one client in the
		// morning and another in the afternoon does the same. Both shapes are
		// in this package's own tests, and were measured against the equality
		// in project/2026-08-13-rollup-reclustering-resolved.md.
		if len(c.Projects) == 1 {
			if p, ok := r.Project(c.Projects[0]); ok && c.Engaged.Total > p.Engaged.Total {
				return Report{}, fmt.Errorf("client %s is credited %v against the %v held by its "+
					"only project %s; a client with one project is that project's own evidence "+
					"clustered again and can never exceed it",
					c.Name, c.Engaged.Total, p.Engaged.Total, p.Name)
			}
		}
	}

	// Allocation's defining property, asserted rather than assumed: an even
	// division that dropped its remainder is a capacity figure that fails to
	// add up, by an amount too small for anybody to notice by eye.
	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	if got := allocated + r.Unattributed.Presence.Total; got != r.Presence.Total {
		return Report{}, fmt.Errorf("the allocated shares (%v) plus the unattributed presence (%v) "+
			"come to %v against %v of presence, off by %v; a shared instant was divided and the "+
			"remainder lost", allocated, r.Unattributed.Presence.Total, got, r.Presence.Total,
			got-r.Presence.Total)
	}
	// A SECOND DIVISOR IS A SECOND PLACE THE REMAINDER CAN BE LOST, so the
	// client split gets the same assertion against the clock it was divided
	// from — presence at least one client was attributed, which is what
	// clientPresence accumulated beside the segments rather than a figure
	// derived from the project side.
	var allocatedByClient time.Duration
	for _, c := range r.Clients {
		allocatedByClient += c.Allocated
	}
	if allocatedByClient != clientPresence {
		return Report{}, fmt.Errorf("the client shares (%v) come to %v of the presence at least one "+
			"client was attributed, off by %v; a shared instant was divided between clients and the "+
			"remainder lost", allocatedByClient, clientPresence, allocatedByClient-clientPresence)
	}
	// The three-way split, asserted the same way. A negative remainder means
	// one stretch was counted as attested AND weak, which would make a project
	// look better evidenced than it is by exactly the amount it was
	// double-counted — and would produce a report nobody could tell was wrong.
	for _, p := range r.Projects {
		if p.Unaccounted < 0 {
			return Report{}, fmt.Errorf("%s was engaged %v, of which %v is attested and %v weak, "+
				"leaving %v unaccounted; a negative remainder means one stretch was counted twice",
				p.Name, p.Engaged.Total, p.Attested, p.Weak, p.Unaccounted)
		}
		// AN ASSERTION IS NOT AN OBSERVATION, checked rather than intended.
		// Clock a person moved to a project is clock no evidence of that
		// project's reaches, so it can only ever land in the unaccounted
		// remainder. If it exceeded that remainder, the correction would have
		// been counted as attested or weak somewhere — a project's hours
		// reading as evidenced when a person's word is all there is behind
		// them, which is the exact misreading every figure in this package is
		// shaped to prevent.
		if p.Asserted > p.Unaccounted {
			return Report{}, fmt.Errorf("%s holds %v moved to it by a correction against %v "+
				"unaccounted; asserted time has been counted as though something observed it",
				p.Name, p.Asserted, p.Unaccounted)
		}
	}
	if r.Unattributed.Bridged < 0 {
		return Report{}, fmt.Errorf("unattributed presence is %v of which %v is unregistered, %v "+
			"unlocatable and %v asserted to be nobody's, leaving %v bridged; a negative remainder "+
			"means one stretch was counted under two reasons", r.Unattributed.Presence.Total,
			r.Unattributed.Unregistered, r.Unattributed.Unlocatable, r.Unattributed.Asserted,
			r.Unattributed.Bridged)
	}
	return r, nil
}

// level is one tier of attribution — the projects, or the clients above them —
// reduced to the spans every figure about it is summed from.
//
// It exists so that both tiers go through ONE implementation. The alternative
// is the same thirty lines written twice, and the failure mode of that is not a
// compile error: it is a roll-up computed under subtly different rules from the
// rows beneath it, producing two plausible numbers nobody subtracts.
type level struct {
	names     []string
	active    map[string][]span
	evidenced map[string][]span
	attested  map[string][]span
	weak      map[string][]span
}

// reduce clusters each group's OWN evidence under the same parameters and
// intersects the result with the global sittings.
//
// This is "model B" from the package doc, applied at whichever tier it is
// handed. Crediting a group the whole of any sitting its evidence appears
// anywhere inside was measured wrong — it summed to 24.01h across a 12.78h day
// — and re-clustering is what fixed it. A client is reduced the same way for
// the same reason, one level up.
func reduce(by map[string][]source.Event, bounds []source.Event, presence []span,
	p sessions.Params, v watch, ph Phase) (level, error) {
	l := level{
		names:     make([]string, 0, len(by)),
		active:    make(map[string][]span, len(by)),
		evidenced: make(map[string][]span, len(by)),
		attested:  make(map[string][]span, len(by)),
		weak:      make(map[string][]span, len(by)),
	}
	for name := range by {
		l.names = append(l.names, name)
	}
	slices.Sort(l.names)

	// Announced AFTER the names are known and sorted, so the total is the real
	// one rather than an estimate, and so the steps below count up in the order
	// a reader watching the display sees them.
	v.begin(ph, len(l.names))
	defer v.done(ph)

	for i, name := range l.names {
		// The presence readings travel with the group's own evidence, so its
		// sittings are split and trimmed by observed darkness exactly as the
		// global ones were rather than being trimmed only by the intersection
		// afterwards.
		sub := make([]source.Event, 0, len(by[name])+len(bounds))
		sub = append(sub, by[name]...)
		sub = append(sub, bounds...)
		res, err := sessions.Cluster(sub, p)
		if err != nil {
			return level{}, err
		}
		own := make([]span, 0, len(res.Sessions))
		for _, s := range res.Sessions {
			own = append(own, span{s.Start, s.End})
		}
		l.active[name] = intersect(union(own), presence)

		raw := make([]span, 0, len(by[name]))
		// What ACCOUNTS for a stretch of engaged time is the same question
		// responsibleFor already answers for unattributed presence: an
		// observation's own extent, plus the lead-in before it and the tail-out
		// after. Split by whether the observation records an act, it is what
		// tells real hours apart from the file timestamps some of them rest on.
		var acts, stamps []span
		for _, e := range by[name] {
			raw = append(raw, rawSpan(e))
			if source.WeakEvidence(e.Source) {
				stamps = append(stamps, responsibleFor(e, p))
			} else {
				acts = append(acts, responsibleFor(e, p))
			}
		}
		l.evidenced[name] = intersect(union(raw), l.active[name])
		l.attested[name] = intersect(union(acts), l.active[name])
		l.weak[name] = intersect(union(stamps), l.active[name])
		v.step(ph, i+1)
	}
	return l, nil
}

// cuts is every boundary this level contributes to the segmentation.
func (l level) cuts() []time.Time {
	var out []time.Time
	for _, name := range l.names {
		out = append(out, cuts(l.active[name], l.evidenced[name], l.attested[name], l.weak[name])...)
	}
	return out
}

// levelCursors walks this level's spans alongside the segments, so each
// "is this group active here" question is answered in one pass rather than by
// searching.
type levelCursors struct {
	active    map[string]*cursor
	evidenced map[string]*cursor
	attested  map[string]*cursor
	weak      map[string]*cursor
}

func (l level) cursors() levelCursors {
	c := levelCursors{
		active:    make(map[string]*cursor, len(l.names)),
		evidenced: make(map[string]*cursor, len(l.names)),
		attested:  make(map[string]*cursor, len(l.names)),
		weak:      make(map[string]*cursor, len(l.names)),
	}
	for _, name := range l.names {
		c.active[name] = &cursor{spans: l.active[name]}
		c.evidenced[name] = &cursor{spans: l.evidenced[name]}
		c.attested[name] = &cursor{spans: l.attested[name]}
		c.weak[name] = &cursor{spans: l.weak[name]}
	}
	return c
}

// allocate divides one segment among the projects active across it, exactly.
//
// An even division loses the remainder, and a hundred thousand segments each
// losing a nanosecond or two is a capacity figure that quietly fails to add up
// to the presence it was split from. The remainder is therefore handed out one
// nanosecond at a time, to the projects in the order they were sorted into, so
// the split is deterministic as well as exact.
func allocate(d time.Duration, among []string) map[string]time.Duration {
	out := make(map[string]time.Duration, len(among))
	k := time.Duration(len(among))
	base, remainder := d/k, d%k
	for i, name := range among {
		share := base
		if time.Duration(i) < remainder {
			share++
		}
		out[name] = share
	}
	return out
}

// rawSpan is the clock an observation itself covered. A point observes an
// instant and no duration, and an unbounded one observed a single edge and no
// extent; both come out empty, which union then drops.
func rawSpan(e source.Event) span {
	if e.Shape != source.Interval {
		at := e.Anchor()
		return span{at, at}
	}
	return span{e.Start, e.End}
}

// responsibleFor is the stretch of a sitting an observation accounts for: its
// own extent plus the lead-in before it and the tail-out after.
//
// This is the same rule internal/sessions uses to decide which piece of a split
// cluster an observation belongs to, so a stretch of presence that no
// responsibleFor span covers is exactly a stretch the gap threshold bridged.
func responsibleFor(e source.Event, p sessions.Params) span {
	s := rawSpan(e)
	return span{s.start.Add(-p.LeadIn), s.end.Add(p.TailOut)}
}

type role int

const (
	roleEvidence role = iota
	rolePresence
	roleIgnored
)

func rolesOf(p sessions.Params) map[string]role {
	out := make(map[string]role, len(p.PresenceSources)+len(p.IgnoreSources))
	for _, id := range p.PresenceSources {
		out[id] = rolePresence
	}
	for _, id := range p.IgnoreSources {
		out[id] = roleIgnored
	}
	return out
}
