// Command claimset characterises WHICH job claims are usable as ground truth,
// and states the fraction that survives.
//
// # What this is for, and what it deliberately is not
//
// scripts/claimlag measures lead, tail and internal silence across every claim
// it can. This tool asks the prior question: which claims should have been in
// that population at all. It produces a subset and its size. It fits nothing,
// chooses no parameter and changes no default — that is task PkY6I, and a
// median is not a fit.
//
// # A claim is not a work unit, and four different things can be wrong with one
//
// Each is a separate stage, each is reported with what it costs, and -mutate
// re-runs the whole cascade with each stage removed in turn so that a stage's
// value is a MEASUREMENT rather than an assertion.
//
//   - THE END WAS NEVER OBSERVED. A claim terminated by claim_expired is
//     Unbounded in the store: the expiry says only that nobody spoke, and its
//     timestamp is when some later command happened to notice.
//
//   - THE END WAS OBSERVED, BUT AS AN ABANDONMENT. `released` and `canceled`
//     are real observations, and they are not observations that the work
//     finished. Reading them as `done` is the "absence of a row is not an
//     observation" bug wearing a different hat: here the row is present and
//     says something else.
//
//   - NO ARTIFACT LIES INSIDE IT. There is then no lag to measure, so the
//     claim cannot serve a lead-in or tail-out fit. It remains informative
//     about something else — it is a floor under what an artifact-only model
//     can see — and those two uses are kept apart here on purpose.
//
//   - NOBODY IS KNOWN TO HAVE BEEN AT THE MACHINE. A claim held across lunch
//     is not one unit of work. This is where the three states matter most: the
//     screen observed lit, the screen observed dark, and the sampler silent
//     are three facts and not two.
//
// # Presence, and why it is knowledgec-backlit by default
//
// internal/config names knowledgec-backlit as the only stream this project
// treats as presence, and gives the measured reason: Biome's focus intervals
// record which application was frontmost, which continues perfectly well with
// the screen off — the 2026-07-11 17:53 Motion autosave was written during a
// confirmed break. Reading biome-appusage as presence would re-import exactly
// that defect. -presence-sources exists so the alternative can be measured
// rather than argued about, and it defaults to the config's answer — but a
// source whose State is not a backlight reading is REFUSED by name rather than
// scored, because scoring it produced 433 observed-dark claims and three
// survivors, which reads like a finding and is a units error.
//
// # The clipped-window trap
//
// scripts/sleepcheck once clipped to the span two sources shared, reported
// "1.88h of gaps, largest 1h27m", and had excluded a 6h23m gap that ended 26
// seconds before its window opened. Nothing here clips. A claim reaching past
// the last presence sample is UNSAMPLED for the remainder, which is a third
// verdict and not a pass; there is a constructed regression test for exactly
// the 26-second shape.
//
// # The co-emission trap
//
// A source can be co-emitted with the act being measured without being that
// source. 705 of 719 "first artifacts" in the first version of the claim-lag
// measurement were the transcript of the `job claim` command itself. Two
// exclusions carry that lesson forward, both reachable so the contaminated
// figure can be reproduced and recognised:
//
//   - claude-transcripts is excluded by default (-with-transcripts).
//   - fs-observations naming a .jobs.db or its sidecars is excluded by
//     default (-with-jobsdb). `job claim` WRITES that file, so an observation
//     of its mtime is another record of the claim.
//
// # Usage
//
//	go run ./scripts/claimset
//	go run ./scripts/claimset -mutate
//	go run ./scripts/claimset -by-project -by-month
//	go run ./scripts/claimset -since 2026-07-03            # same window as presence
//	go run ./scripts/claimset -presence-rule not-dark      # merely uncontradicted
//	go run ./scripts/claimset -artifact-sources gitlog-commits   # durable only
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// knownArtifactSources observe a trace work left behind, with a note on how
// DURABLE that trace is — which turns out to matter more than anything else
// this tool measures.
//
// A commit timestamp is written once and stands for ever. A file mtime is the
// time of the LAST save, and internal/fsobserve reads it at harvest time; the
// first fs-observations harvest in the live store ran at 2026-08-01T13:29:42Z,
// so every one of its 130,594 observations dated before then is a single
// snapshot in which every earlier save of that file has been overwritten by the
// last. That is why -artifact-sources exists: "the claim contained no artifact"
// means something entirely different for the two, and the difference is
// measurable by running this tool twice.
//
// claude-transcripts is absent, and that omission is the whole point; see the
// file header.
var knownArtifactSources = map[string]string{
	"gitlog-commits":  "durable: a commit timestamp is permanent",
	"gitlog-reflog":   "mostly durable: reflog entries expire, by default at 90 days",
	"fs-observations": "SNAPSHOT: the file's last mtime, read at harvest time",
	"motion-autosave": "SNAPSHOT: a file mtime, same caveat",
	"blender-backups": "SNAPSHOT: a file mtime, same caveat",
}

// defaultArtifactSources is every known one, which is what scripts/claimlag
// used and what makes the two tools comparable out of the box.
const defaultArtifactSources = "gitlog-commits,gitlog-reflog,fs-observations,motion-autosave,blender-backups"

// artifactSet resolves the -artifact-sources flag.
//
// An unrecognised name is an error rather than a source that contributes
// nothing. A tool that answers "no claim has an artifact" because of a typo is
// the silent zero this project has shipped in six readers, and here it would
// look exactly like a finding.
func artifactSet(list string, withTranscripts bool) (map[string]bool, error) {
	names := splitList(list)
	if len(names) == 0 && !withTranscripts {
		return nil, fmt.Errorf("no artifact sources named; there would be nothing to measure a lag to")
	}
	out := map[string]bool{}
	for _, n := range names {
		if _, ok := knownArtifactSources[n]; !ok {
			return nil, fmt.Errorf("%q is not an artifact source; known: %s", n, defaultArtifactSources)
		}
		out[n] = true
	}
	if withTranscripts {
		out["claude-transcripts"] = true
	}
	return out, nil
}

// termination is what ENDED a claim, as three states rather than two.
type termination string

const (
	// termDone: somebody said the work finished.
	termDone termination = "done"
	// termAbandoned: somebody said the claim was given up — released or
	// canceled. A real observation, and not the same one.
	termAbandoned termination = "abandoned"
	// termUnobserved: nothing was recorded. The claim expired, or it was
	// still open when the harvest ran.
	termUnobserved termination = "unobserved"
)

// presenceVerdict is the same discipline applied to the machine.
type presenceVerdict string

const (
	// presenceLit: the screen was observed lit for the whole claim.
	presenceLit presenceVerdict = "lit"
	// presenceDark: the screen was SAMPLED and read dark for part of it.
	presenceDark presenceVerdict = "dark"
	// presenceUnsampled: for part of the claim there is no reading at all.
	// This is not a synonym for dark and must never be merged with it.
	presenceUnsampled presenceVerdict = "unsampled"
)

// presenceRule is which of two defensible readings of "presence-corroborated"
// the cascade applies. They are not interchangeable and the difference is
// large, so the tool refuses to pick one silently.
type presenceRule string

const (
	// ruleLit is CORROBORATION: the screen was observed lit for the whole
	// claim. It is a positive observation, and it is only available where the
	// sampler was running.
	ruleLit presenceRule = "lit"
	// ruleNotDark is NON-CONTRADICTION: nothing observed says the person was
	// away. It admits every claim the sampler never reached, so it is much
	// larger and much weaker, and it must never be described as corroborated.
	ruleNotDark presenceRule = "not-dark"
)

func (p presenceRule) admits(v presenceVerdict) bool {
	if p == ruleNotDark {
		return v != presenceDark
	}
	return v == presenceLit
}

// Stage names. They are constants because -mutate names one to remove and a
// typo there would silently run the full cascade and report no difference.
const (
	stageAll         = "all claim spans"
	stageEndObserved = "end observed"
	stageFinished    = "finished, not abandoned"
	stageArtifact    = "artifact-bearing"
	stagePresence    = "presence-corroborated"
	stageShort       = "short enough"
)

func main() {
	dbPath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	maxClaim := flag.Duration("max-claim", 0, "reject claims longer than this; 0 disables it, which is the default because the cutoff did not survive its own derivation — see the length profile the tool prints")
	slack := flag.Duration("presence-slack", time.Minute, "unsampled or dark time inside a claim tolerated before its presence verdict changes")
	presenceSources := flag.String("presence-sources", "knowledgec-backlit", "comma-separated sources read as presence; the default is internal/config's answer and the reason is measured")
	rule := flag.String("presence-rule", string(ruleLit), "lit = corroborated, the screen was observed lit throughout; not-dark = merely not contradicted, which admits every claim the sampler never reached")
	artifacts := flag.String("artifact-sources", defaultArtifactSources, "comma-separated artifact sources; only gitlog-commits is fully durable, the rest are file-mtime snapshots")
	withTranscripts := flag.Bool("with-transcripts", false, "count claude-transcripts as artifacts, which CO-EMITS with an agent's own job commands")
	withJobsDB := flag.Bool("with-jobsdb", false, "count fs-observations of .jobs.db as artifacts, which `job claim` itself writes")
	since := flag.String("since", "", "consider only claims anchored on or after this date (YYYY-MM-DD, local)")
	until := flag.String("until", "", "consider only claims anchored before this date (YYYY-MM-DD, local)")
	mutate := flag.Bool("mutate", false, "re-run the cascade with each stage removed in turn, so each stage's value is measured")
	byProject := flag.Bool("by-project", false, "break the surviving subset down by project")
	byMonth := flag.Bool("by-month", false, "break the surviving subset down by month")
	flag.Parse()

	sinceT, err := parseDay(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claimset: -since: %v\n", err)
		os.Exit(2)
	}
	untilT, err := parseDay(*until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claimset: -until: %v\n", err)
		os.Exit(2)
	}
	if *rule != string(ruleLit) && *rule != string(ruleNotDark) {
		fmt.Fprintf(os.Stderr, "claimset: -presence-rule must be %q or %q, not %q\n", ruleLit, ruleNotDark, *rule)
		os.Exit(2)
	}

	opts := options{
		store:           *dbPath,
		maxClaim:        *maxClaim,
		slack:           *slack,
		presence:        splitList(*presenceSources),
		artifacts:       *artifacts,
		rule:            presenceRule(*rule),
		since:           sinceT,
		until:           untilT,
		withTranscripts: *withTranscripts,
		withJobsDB:      *withJobsDB,
		mutate:          *mutate,
		byProject:       *byProject,
		byMonth:         *byMonth,
	}
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "claimset: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	store           string
	maxClaim        time.Duration
	slack           time.Duration
	presence        []string
	artifacts       string
	rule            presenceRule
	since, until    time.Time
	withTranscripts bool
	withJobsDB      bool
	mutate          bool
	byProject       bool
	byMonth         bool
}

// parseDay reads a YYYY-MM-DD in the machine's own zone, because a day is a
// local thing and the store's UTC text would shift Ben's evening into tomorrow.
func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// row is one claim, with every verdict already taken. The cascade reads only
// this, which is what lets the cascade be tested without a database.
type row struct {
	project   string
	task      string
	start     time.Time
	dur       time.Duration
	term      termination
	artifacts int
	// artifactCoverage says whether the claim's project is observed by any
	// artifact source AT ALL. Without it, "no artifact inside this claim"
	// cannot be told apart from "this project is not watched", and those are
	// the two facts this project keeps collapsing.
	artifactCoverage bool
	presence         presenceVerdict
	unsampled, dark  time.Duration
	lead, tail       time.Duration
	maxGap           time.Duration
	firstSource      string
}

func run(o options) error {
	path := o.store
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".timetattle", "timetattle.db")
	}

	st, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer st.Close()

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		return err
	}

	sources, err := artifactSet(o.artifacts, o.withTranscripts)
	if err != nil {
		return err
	}
	presenceOf := map[string]bool{}
	for _, s := range o.presence {
		presenceOf[s] = true
	}

	var (
		claims    []source.Event
		jobEvents []source.Event
		presence  []source.Event
		artifacts = map[string][]source.Event{}
	)
	for _, e := range events {
		switch {
		case e.Source == "job-claims":
			claims = append(claims, e)
		case e.Source == "job-events":
			jobEvents = append(jobEvents, e)
		case presenceOf[e.Source]:
			presence = append(presence, e)
		case sources[e.Source] && e.Identity.Project != "":
			if !o.withJobsDB && isCoEmitted(e) {
				continue
			}
			artifacts[e.Identity.Project] = append(artifacts[e.Identity.Project], e)
		}
	}
	if len(claims) == 0 {
		return fmt.Errorf("no job-claims events in %s — has a harvest run since internal/jobs landed?", path)
	}
	if err := checkPresenceReadings(o.presence, presence); err != nil {
		return err
	}
	for p := range artifacts {
		sort.Slice(artifacts[p], func(i, j int) bool { return artifacts[p][i].Start.Before(artifacts[p][j].Start) })
	}
	sort.Slice(presence, func(i, j int) bool { return presence[i].Start.Before(presence[j].Start) })

	terms := newTerminators(jobEvents)
	rows := make([]row, 0, len(claims))
	for _, c := range claims {
		rows = append(rows, classify(c, terms, artifacts, presence, o.slack))
	}

	report(path, o, inWindow(rows, o.since, o.until))
	return nil
}

// isCoEmitted reports whether an observation is another record of `job claim`
// itself. `job` writes <root>/.jobs.db and SQLite writes the -wal and -shm
// beside it, so internal/fsobserve sees a file change at the instant of every
// claim, in the very project the claim is about.
func isCoEmitted(ev source.Event) bool {
	base := filepath.Base(ev.Locator)
	return base == ".jobs.db" || strings.HasPrefix(base, ".jobs.db-")
}

// termKey scopes a terminator to one task in one database. Short ids collide
// freely across projects, so keying on the task alone would let one project's
// `done` close another's claim.
type termKey struct {
	db, task string
	at       time.Time
}

// newTerminators indexes the job-events points that can end a claim.
//
// The terminator is recovered from job-events rather than carried on the span,
// because internal/jobs deliberately writes a CONSTANT State on every claim
// interval: State is part of the store's identity key, so encoding the
// terminator there would make one claim two unrelated rows. The point at the
// same instant is the real observation.
func newTerminators(events []source.Event) map[termKey][]string {
	out := map[termKey][]string{}
	for _, e := range events {
		switch e.State {
		case "done", "released", "canceled", "claim_expired":
			k := termKey{db: e.Locator, task: e.Identity.Document, at: e.Start}
			out[k] = append(out[k], e.State)
		}
	}
	return out
}

// terminationOf reports what ended a claim, in three states.
func terminationOf(c source.Event, terms map[termKey][]string) termination {
	if c.Shape != source.Interval || c.End.IsZero() {
		// Unbounded: an expiry, or a claim still open when the harvest ran.
		return termUnobserved
	}
	states := terms[termKey{db: c.Locator, task: c.Identity.Document, at: c.End}]
	// `job done` writes a done row and a released row at one instant. That is
	// one act of finishing, so done outranks released rather than conflicting
	// with it.
	if slices.Contains(states, "done") {
		return termDone
	}
	if slices.Contains(states, "released") || slices.Contains(states, "canceled") {
		return termAbandoned
	}
	// No terminator recorded at this instant. NOT a pass: this is the third
	// state, and it is why the function does not return a bool.
	return termUnobserved
}

// checkPresenceReadings refuses a presence source whose readings this tool
// cannot interpret.
//
// presenceOf calls an interval lit only when its State reads "on", which is
// knowledgec-backlit's vocabulary and nobody else's. Point -presence-sources at
// biome-appusage, whose State is not a backlight reading at all, and every
// interval becomes sampled-but-not-lit: the live store then reports 433 claims
// as OBSERVED DARK and three survivors, which reads like a devastating finding
// and is a units error. Refusing by name is the only outcome that is not a
// quiet wrong answer.
//
// A source named but never seen is refused for the mirror reason: it can only
// report every claim as unsampled, which would look like the same finding.
func checkPresenceReadings(names []string, events []source.Event) error {
	seen := map[string]int{}
	readable := map[string]int{}
	for _, e := range events {
		seen[e.Source]++
		if e.State == "on" || e.State == "off" {
			readable[e.Source]++
		}
	}
	for _, n := range names {
		switch {
		case seen[n] == 0:
			return fmt.Errorf("presence source %q has no events in this store; "+
				"it can only report every claim as unsampled, which is not a finding", n)
		case readable[n] == 0:
			return fmt.Errorf("presence source %q carries no on/off reading in %d events; "+
				"this tool scores presence from a backlight state, so every one of its "+
				"intervals would be counted as observed-dark", n, seen[n])
		}
	}
	return nil
}

// span is a half-open interval used for the coverage arithmetic.
type span struct{ from, to time.Time }

// presenceOf returns the three-state presence verdict for a claim, plus the
// TOTAL unsampled and TOTAL dark time inside it.
//
// Nothing is clipped to the span the sources share. A claim reaching past the
// last presence sample counts every second past it as unsampled, which is the
// bug scripts/sleepcheck shipped and the constructed test named "sampling stops
// inside the claim" reproduces.
func presenceOf(c source.Event, samples []source.Event, slack time.Duration) (presenceVerdict, time.Duration, time.Duration) {
	if c.Shape != source.Interval || c.Start.IsZero() || c.End.IsZero() {
		return presenceUnsampled, 0, 0
	}
	total := c.End.Sub(c.Start)

	var sampled, lit []span
	for _, s := range samples {
		if s.Shape != source.Interval || !s.End.After(c.Start) || !s.Start.Before(c.End) {
			continue
		}
		cl := span{from: maxTime(s.Start, c.Start), to: minTime(s.End, c.End)}
		sampled = append(sampled, cl)
		// knowledgeC writes contradictory overlapping rows — both an
		// isBacklit=1 and an isBacklit=0 with identical endpoints exist on
		// 2026-07-14 at 09:01:12 — so lit is taken as the union of the "on"
		// readings and dark is what is sampled but not lit. A contradiction
		// therefore resolves to lit, which is the direction that does not
		// invent an absence.
		if s.State == "on" {
			lit = append(lit, cl)
		}
	}

	sampledDur := coverage(merge(sampled))
	litDur := coverage(merge(lit))
	unsampled := total - sampledDur
	dark := sampledDur - litDur

	switch {
	// THE SLACK IS ABSOLUTE, AND A SHORT CLAIM CAN THEREFORE PASS ON NOTHING.
	// A 14-second claim has at most 14 seconds of unsampled time, which is
	// under any sane slack even when not one reading exists. The first run of
	// this tool reported a surviving subset reaching back to 2026-04-20, ten
	// weeks before knowledgec-backlit's first row, entirely through this hole.
	// Requiring the sampled part to be the MAJORITY of the span closes it and
	// costs nothing above 2×slack, where the unsampled test is stricter anyway.
	case sampledDur*2 <= total:
		return presenceUnsampled, unsampled, dark
	case unsampled > slack:
		return presenceUnsampled, unsampled, dark
	case dark > slack:
		return presenceDark, unsampled, dark
	default:
		return presenceLit, unsampled, dark
	}
}

func merge(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	s := slices.Clone(in)
	sort.Slice(s, func(i, j int) bool { return s[i].from.Before(s[j].from) })
	out := []span{s[0]}
	for _, cur := range s[1:] {
		last := &out[len(out)-1]
		if cur.from.After(last.to) {
			out = append(out, cur)
			continue
		}
		if cur.to.After(last.to) {
			last.to = cur.to
		}
	}
	return out
}

func coverage(merged []span) time.Duration {
	var d time.Duration
	for _, s := range merged {
		d += s.to.Sub(s.from)
	}
	return d
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// classify takes every verdict for one claim.
func classify(c source.Event, terms map[termKey][]string, artifacts map[string][]source.Event, presence []source.Event, slack time.Duration) row {
	r := row{
		project:          c.Identity.Project,
		task:             c.Identity.Document,
		start:            c.Anchor(),
		term:             terminationOf(c, terms),
		artifactCoverage: len(artifacts[c.Identity.Project]) > 0,
	}
	if c.Shape == source.Interval && !c.Start.IsZero() && !c.End.IsZero() {
		r.dur = c.End.Sub(c.Start)
		in := window(artifacts[c.Identity.Project], c.Start, c.End)
		r.artifacts = len(in)
		if len(in) > 0 {
			r.lead = in[0].Start.Sub(c.Start)
			r.tail = c.End.Sub(in[len(in)-1].Start)
			r.firstSource = in[0].Source
			for i := 1; i < len(in); i++ {
				if g := in[i].Start.Sub(in[i-1].Start); g > r.maxGap {
					r.maxGap = g
				}
			}
		}
	}
	r.presence, r.unsampled, r.dark = presenceOf(c, presence, slack)
	return r
}

func window(in []source.Event, from, to time.Time) []source.Event {
	lo := sort.Search(len(in), func(i int) bool { return !in[i].Start.Before(from) })
	hi := sort.Search(len(in), func(i int) bool { return in[i].Start.After(to) })
	return in[lo:hi]
}

// inWindow restricts the population to claims anchored in [since, until).
//
// It exists so that the two presence rules can be compared on the same
// calendar. knowledgec-backlit's first row is 2026-07-02 and claims begin
// 2026-04-20, so the corroborated subset is inherently a July subset: comparing
// its median lead against the loose subset's without a window compares the
// rules and ten weeks of different work at once, and this project's rule is
// that two figures nobody has subtracted are not a finding.
func inWindow(rows []row, since, until time.Time) []row {
	if since.IsZero() && until.IsZero() {
		return rows
	}
	var out []row
	for _, r := range rows {
		if !since.IsZero() && r.start.Before(since) {
			continue
		}
		if !until.IsZero() && !r.start.Before(until) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// stage is one line of the cascade: what the filter is, and what is left.
type stage struct {
	name     string
	n        int
	total    time.Duration
	projects int
	days     int
	rows     []row
}

// cascade applies every stage in order.
func cascade(rows []row, maxClaim time.Duration, rule presenceRule) []stage {
	return cascadeWithout(rows, maxClaim, rule, "")
}

// cascadeWithout applies every stage except one, which is how a stage's value
// is measured rather than asserted.
func cascadeWithout(rows []row, maxClaim time.Duration, rule presenceRule, skip string) []stage {
	keep := []struct {
		name string
		pass func(row) bool
	}{
		{stageEndObserved, func(r row) bool { return r.term != termUnobserved }},
		{stageFinished, func(r row) bool { return r.term == termDone }},
		{stageArtifact, func(r row) bool { return r.artifacts > 0 }},
		{stagePresence, func(r row) bool { return rule.admits(r.presence) }},
		{stageShort, func(r row) bool { return maxClaim <= 0 || r.dur <= maxClaim }},
	}

	cur := rows
	out := []stage{summarise(stageAll, cur)}
	for _, k := range keep {
		if k.name == skip {
			continue
		}
		var next []row
		for _, r := range cur {
			if k.pass(r) {
				next = append(next, r)
			}
		}
		cur = next
		out = append(out, summarise(k.name, cur))
	}
	return out
}

func summarise(name string, rows []row) stage {
	s := stage{name: name, n: len(rows), rows: rows}
	projects := map[string]bool{}
	days := map[string]bool{}
	for _, r := range rows {
		s.total += r.dur
		if r.project != "" {
			projects[r.project] = true
		}
		if !r.start.IsZero() {
			days[r.start.Local().Format("2006-01-02")] = true
		}
	}
	s.projects, s.days = len(projects), len(days)
	return s
}

func report(path string, o options, rows []row) {
	fmt.Printf("store: %s\n", path)
	fmt.Printf("artifact sources: %s\n", o.artifacts)
	fmt.Printf("presence sources: %s   presence rule: %s   presence slack: %s   max claim: %s\n",
		strings.Join(o.presence, ","), o.rule, o.slack, o.maxClaim)
	if !o.since.IsZero() || !o.until.IsZero() {
		fmt.Printf("window: %s to %s (local)\n", dayOrAll(o.since), dayOrAll(o.until))
	}
	if o.withTranscripts {
		fmt.Println("WARNING: claude-transcripts counted as artifacts. An agent's `job claim` IS a\n" +
			"tool call, so a claim and its 'first artifact' become two records of one keystroke.")
	}
	if o.withJobsDB {
		fmt.Println("WARNING: fs-observations of .jobs.db counted as artifacts. `job claim` writes\n" +
			"that file, so those observations are co-emitted with the act being measured.")
	}
	fmt.Println()

	fmt.Println("HOW EACH CLAIM ENDED — three states, not two.")
	byTerm := map[termination]int{}
	for _, r := range rows {
		byTerm[r.term]++
	}
	fmt.Printf("  %-12s %5d  somebody said the work finished\n", termDone, byTerm[termDone])
	fmt.Printf("  %-12s %5d  somebody said the claim was given up (released or canceled)\n", termAbandoned, byTerm[termAbandoned])
	fmt.Printf("  %-12s %5d  nothing was recorded: the claim expired, or was still open\n", termUnobserved, byTerm[termUnobserved])
	fmt.Printf("  %-12s %5d\n\n", "total", len(rows))

	fmt.Println("WHETHER ANYBODY WAS AT THE MACHINE — three states, not two.")
	byPres := map[presenceVerdict]int{}
	for _, r := range rows {
		byPres[r.presence]++
	}
	fmt.Printf("  %-12s %5d  screen observed lit throughout\n", presenceLit, byPres[presenceLit])
	fmt.Printf("  %-12s %5d  screen SAMPLED and read dark for more than the slack\n", presenceDark, byPres[presenceDark])
	fmt.Printf("  %-12s %5d  no reading at all for part of the claim — an absence of rows,\n"+
		"                      which is not an observation of absence\n\n", presenceUnsampled, byPres[presenceUnsampled])

	fmt.Println("WHETHER THE PROJECT IS WATCHED AT ALL — the same distinction, for artifacts.")
	var noneAndWatched, noneAndUnwatched int
	for _, r := range rows {
		if r.artifacts > 0 {
			continue
		}
		if r.artifactCoverage {
			noneAndWatched++
		} else {
			noneAndUnwatched++
		}
	}
	fmt.Printf("  %5d claims carry no artifact, in a project artifact sources DO observe\n", noneAndWatched)
	fmt.Printf("  %5d claims carry no artifact, in a project they observe not at all\n\n", noneAndUnwatched)

	fmt.Println("HOW LONG A CLAIM HAS TO BE BEFORE IT SPANS AN OBSERVED ABSENCE.")
	fmt.Println("Claims whose presence was never sampled are absent from this table: they")
	fmt.Println("contribute nothing to a rate of observed absence, and counting them as")
	fmt.Println("not-dark would be the collapse this tool exists to avoid.")
	printLengthProfile(rows)
	fmt.Println()

	stages := cascade(rows, o.maxClaim, o.rule)
	fmt.Println("THE CASCADE — each stage is a filter, and the survivors are the ground-truth set.")
	fmt.Printf("  %-26s %6s %10s %9s %6s %6s\n", "stage", "claims", "of all", "duration", "projs", "days")
	all := stages[0].n
	for _, s := range stages {
		fmt.Printf("  %-26s %6d %9.1f%% %9s %6d %6d\n",
			s.name, s.n, 100*float64(s.n)/float64(all), rnd(s.total), s.projects, s.days)
	}

	final := stages[len(stages)-1]
	fmt.Printf("\nSURVIVING SUBSET: %d of %d claim spans (%.1f%%), %s of declared work,\n"+
		"%d projects, %d distinct local days",
		final.n, all, 100*float64(final.n)/float64(all), rnd(final.total), final.projects, final.days)
	if final.n > 0 {
		first, last := final.rows[0].start, final.rows[0].start
		for _, r := range final.rows {
			if r.start.Before(first) {
				first = r.start
			}
			if r.start.After(last) {
				last = r.start
			}
		}
		fmt.Printf(", %s to %s.\n", first.Local().Format("2006-01-02"), last.Local().Format("2006-01-02"))
	} else {
		fmt.Println(".")
	}

	if final.n > 0 {
		fmt.Println("\nWHAT THE SURVIVORS MEASURE — the same three distributions scripts/claimlag")
		fmt.Println("reports, over this subset rather than over every claim.")
		leads, tails, gaps, durs := extract(final.rows)
		fmt.Print("  LEAD    ")
		printDist(leads)
		fmt.Print("  TAIL    ")
		printDist(tails)
		fmt.Print("  SILENCE ")
		printDist(gaps)
		fmt.Print("  LENGTH  ")
		printDist(durs)
	}

	if o.byProject {
		fmt.Println("\nSURVIVORS BY PROJECT")
		printGroups(final.rows, func(r row) string { return r.project })
	}
	if o.byMonth {
		fmt.Println("\nSURVIVORS BY MONTH")
		printGroups(final.rows, func(r row) string { return r.start.Local().Format("2006-01") })
	}

	if o.mutate {
		fmt.Println("\nMUTATION — each stage removed in turn. A stage that changes nothing when")
		fmt.Println("removed is not doing anything, and this is how that gets caught.")
		// The median lead and tail are here rather than only the claim length
		// because they are the parameters PkY6I fits. A stage that changes the
		// population without moving them is a stage that is not buying the fit
		// anything, however defensible it sounds.
		fmt.Printf("  %-26s %8s %10s %11s %11s %10s\n",
			"stage removed", "claims", "duration", "median lead", "median tail", "max len")
		line := func(label string, s stage, delta int) {
			l, tl, _, d := extract(s.rows)
			suffix := ""
			if delta != 0 || label != "(none)" {
				suffix = fmt.Sprintf("  (%+d claims)", delta)
			}
			fmt.Printf("  %-26s %8d %10s %11s %11s %10s%s\n",
				label, s.n, rnd(s.total), rnd(pct(l, 50)), rnd(pct(tl, 50)), rnd(last(d)), suffix)
		}
		base := cascade(rows, o.maxClaim, o.rule)
		bl := base[len(base)-1]
		line("(none)", bl, 0)
		for _, name := range []string{stageEndObserved, stageFinished, stageArtifact, stagePresence, stageShort} {
			s := cascadeWithout(rows, o.maxClaim, o.rule, name)
			f := s[len(s)-1]
			line(name, f, f.n-bl.n)
		}
	}
}

// lengthBucket puts a claim in one of five length classes.
//
// The classes exist so the cutoff on claim length can be DERIVED rather than
// asserted. "Short" is not a criterion; the thing a length cutoff is really a
// proxy for is "held across a break", and the presence sampler measures that
// directly. So the honest way to choose a cutoff is to ask, of the claims where
// presence WAS sampled, how the observed-dark rate rises with length.
func lengthBucket(d time.Duration) string {
	switch {
	case d <= 5*time.Minute:
		return "0-5m"
	case d <= 15*time.Minute:
		return "5-15m"
	case d <= 30*time.Minute:
		return "15-30m"
	case d <= time.Hour:
		return "30-60m"
	default:
		return "60m+"
	}
}

var bucketOrder = []string{"0-5m", "5-15m", "15-30m", "30-60m", "60m+"}

// printLengthProfile derives the cutoff. Only claims whose presence was
// SAMPLED appear: an unsampled claim contributes nothing to a rate of observed
// absence, and counting it as "not dark" would be the collapse this whole tool
// is about.
func printLengthProfile(rows []row) {
	type cell struct{ sampled, dark int }
	cells := map[string]*cell{}
	for _, r := range rows {
		if r.presence == presenceUnsampled {
			continue
		}
		b := lengthBucket(r.dur)
		if cells[b] == nil {
			cells[b] = &cell{}
		}
		cells[b].sampled++
		if r.presence == presenceDark {
			cells[b].dark++
		}
	}
	fmt.Printf("  %-10s %8s %8s %10s\n", "length", "sampled", "dark", "dark rate")
	for _, b := range bucketOrder {
		c := cells[b]
		if c == nil {
			fmt.Printf("  %-10s %8d %8d %10s\n", b, 0, 0, "n/a")
			continue
		}
		fmt.Printf("  %-10s %8d %8d %9.1f%%\n", b, c.sampled, c.dark, 100*float64(c.dark)/float64(c.sampled))
	}
}

func extract(rows []row) (leads, tails, gaps, durs []time.Duration) {
	for _, r := range rows {
		leads = append(leads, r.lead)
		tails = append(tails, r.tail)
		gaps = append(gaps, r.maxGap)
		durs = append(durs, r.dur)
	}
	slices.Sort(leads)
	slices.Sort(tails)
	slices.Sort(gaps)
	slices.Sort(durs)
	return
}

func printGroups(rows []row, key func(row) string) {
	groups := map[string][]row{}
	for _, r := range rows {
		groups[key(r)] = append(groups[key(r)], r)
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := summarise(n, groups[n])
		fmt.Printf("  %-16s %5d claims  %9s  %3d days\n", n, s.n, rnd(s.total), s.days)
	}
}

func printDist(sorted []time.Duration) {
	if len(sorted) == 0 {
		fmt.Println("n=0")
		return
	}
	fmt.Printf("n=%d  min %s  p25 %s  median %s  p75 %s  p95 %s  max %s\n",
		len(sorted), rnd(sorted[0]), rnd(pct(sorted, 25)), rnd(pct(sorted, 50)),
		rnd(pct(sorted, 75)), rnd(pct(sorted, 95)), rnd(last(sorted)))
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[(len(sorted)-1)*p/100]
}

func last(sorted []time.Duration) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[len(sorted)-1]
}

func dayOrAll(t time.Time) string {
	if t.IsZero() {
		return "(unbounded)"
	}
	return t.Format("2006-01-02")
}

// rnd trims sub-second noise, which is never meaningful at this scale.
func rnd(d time.Duration) string {
	return strings.TrimSuffix(d.Round(time.Second).String(), "0s0ms")
}
