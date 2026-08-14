// Package storefixture builds a populated event store in a temporary
// directory, so that a test asserting BEHAVIOUR does not need one particular
// laptop's ~/.timetattle/timetattle.db.
//
// # Why this exists
//
// Raised 2026-08-02: "the tests shouldn't look for actual folders on my
// drive... Any data they depend on should be anonymized and/or synthetic, and
// contained within the fixtures directory. Even if it has to spin up a test
// database in a temp folder and populate it."
//
// The failure mode being closed is that a real-data test FAILS OPEN. It is
// indistinguishable from a unit test, so `go test ./...` on a machine without
// the corpus reports green while running almost none of them. It fired twice on
// the day this package was written: a pseudonym rename made eight tests skip,
// and merging migration 0011 against a live store at 0010 made several more
// skip. Both times coverage vanished and the suite stayed green.
//
// This package is an ADDITION and not a replacement. CLAUDE.md's rule is that
// constructed cases sit BESIDE real-data checks and neither substitutes for the
// other, and that rule was earned: an over-general supersession rule once passed
// against the entire live Biome stream, deleting nothing, purely because no real
// interval happened to share an endpoint with a bare stop. A fixture cannot
// establish anything about a corpus of 923,000 events. What it CAN do is carry,
// on demand and on any machine, the regimes a real corpus only happens to
// contain.
//
// # The regimes, and why each is in here
//
// The corpus is small and deliberate. Every day in it exists to carry one thing
// that a hand-written eleven-event case does not:
//
//   - Several sources at once, presence and evidence together. Presence
//     (knowledgec-backlit) BOUNDS sittings without creating them; evidence
//     creates them. Scoring the wrong stream as presence produces a units error
//     that reads like a finding.
//   - A concurrent-project day. A quarter of real days carry two or more
//     projects and the maximum is nine, so a corpus of one project per day would
//     let a whole class of allocation bug through.
//   - Observed darkness falling in the MIDDLE of an observed stretch. Real,
//     found 2026-07-28: the backlight goes dark three seconds before the end of
//     the focus interval containing it. The day also carries a timer-written
//     autosave inside the same dark stretch, because darkness now treats the two
//     differently and one fixture has to pose both — the interval and the saves
//     around it run straight through, and the autosave is suppressed. Until
//     2026-08-14 the dark reading cut the sitting short instead; the shape is
//     kept and the expectation inverted, so what changed stays legible.
//   - A points-only day, with no duration-bearing evidence at all. That regime
//     holds 75.7% of engaged time, and an isolated save with no neighbours has
//     no span of its own: without lead-in and tail-out a day of creative work
//     disappears entirely.
//   - A span lying BETWEEN two observations rather than containing either.
//     Two dark stretches inside one cluster left a middle piece that no
//     observation reached, and it went to the database as a sitting nothing
//     evidenced. Found by a CHECK constraint on a real run rather than by a
//     test, while the invariant sat in the code as a comment and was false.
//     Darkness stopped carving clusters on 2026-08-14, so the shape no longer
//     produces a piece at all. It is kept because the INVARIANT is still
//     enforced by that constraint and must not depend on the route by which
//     sittings are built.
//
// Beside those: a superseded horizon (so the canonical read has something to
// collapse), a live horizon and a bare stop (the two unbounded reasons that
// need opposite treatment), an ignored source carrying an overnight interval
// that would manufacture an eight-hour sitting if it were ever clustered, a
// review and a correction.
//
// # What is deliberately NOT here
//
// No figure about anybody's real corpus. A test that reproduces a published
// count from one machine's history belongs beside the live store, not here, and
// converting it to run against this corpus would turn a real claim into a
// tautology about numbers this file made up.
//
// Every name in it is invented. No client, application or path in this corpus
// refers to anything that exists.
package storefixture

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The sources the corpus draws on. They carry the real IDs rather than invented
// ones because the ID is not a name, it is a ROLE: config.Default names
// knowledgec-backlit as presence and knowledgec-islocked as ignored, and a
// fixture using different strings would exercise a configuration nobody ships.
const (
	// EvidenceSource emits focus intervals: something was frontmost, for a
	// span, and that span is duration-bearing evidence.
	EvidenceSource = "biome-appusage"
	// PresenceSource emits the backlight state. It is the ONLY stream this
	// project reads as presence.
	PresenceSource = "knowledgec-backlit"
	// FileSource emits file observations, which are instants.
	FileSource = "fs-observations"
	// VaultSource emits autosaves written by a timer nobody touches. Same
	// Kind and Shape as FileSource, different Cause, and worth measurably
	// less: one real autosave was written during a confirmed break with the
	// screen off. "motion-autosave" is the real built-in manifest id
	// (internal/manifest/builtin/motion.json's own "id" field) — it read
	// "vault-autosave", an invented id no manifest or collector defines,
	// until task 2CwuI corrected it 2026-08-07.
	VaultSource = "motion-autosave"
	// LockedSource is read and then dropped. It carries an overnight
	// interval here precisely so that a derivation which stopped honouring
	// the ignore list would manufacture a visible eight-hour sitting rather
	// than drift by a few minutes.
	LockedSource = "knowledgec-islocked"
)

// Sources are the sources that actually write events into the corpus, in the
// order they are harvested.
var Sources = []string{EvidenceSource, PresenceSource, LockedSource, FileSource, VaultSource}

// IgnoredSources are the sources config.Default drops. knowledgec-mediausage is
// named here, as it is in the shipped configuration, even though nothing in this
// corpus emits it: the list is what a derivation was configured with, not a
// census of what happened to be present.
var IgnoredSources = []string{"knowledgec-islocked", "knowledgec-mediausage"}

// The applications and projects. Invented, and deliberately unlike anything
// real: the tree was scrubbed of client names on 2026-08-02 and a fixture is the
// easiest place for one to creep back in.
const (
	appForge  = "com.example.forge"
	appMosaic = "com.example.mosaic"
	appAtlas  = "com.example.atlas"

	// ProjectQuarry, ProjectLantern and ProjectMeadow share the concurrent
	// day. ProjectVellum owns the points-only day alone.
	ProjectQuarry  = "quarry"
	ProjectLantern = "lantern"
	ProjectMeadow  = "meadow"
	ProjectVellum  = "vellum"

	// The clients above them, and the SHAPE is the point rather than the
	// names. ClientAtelier owns three projects and ClientBindery owns one,
	// which reproduces in miniature the only distribution that can tell a
	// roll-up from a sum: in the live registry every client is 1:1 with its
	// project except one, which spans 33 of them. A corpus in which every
	// client owned one project would let a client roll-up computed by ADDING
	// its projects' absolute engaged time pass every check here, because
	// adding one row is the same as unioning one span.
	ClientAtelier = "atelier"
	ClientBindery = "bindery"
)

// clientOf is the project-to-client map this corpus registers, and it is the
// fixture's stand-in for a marker's client field.
//
// It is a map here and a per-directory marker in production for the reason
// 0012_event_client.sql sets out: the dependency the registry actually has is
// directory -> (client, project), and project -> client holds only because
// registry.Register now enforces it.
func clientOf(project string) string {
	if project == ProjectVellum {
		return ClientBindery
	}
	return ClientAtelier
}

// The documents. Named rather than written inline at each use, so that Words
// and Events draw on ONE list: two lists would drift, and the allowlist would
// then be describing a corpus that no longer exists.
const (
	docBrief     = "brief.md"
	docOutline   = "outline.md"
	docSpec      = "spec.md"
	docSurvey    = "survey.md"
	docDraft     = "draft.md"
	docLetter    = "letter.md"
	docNotes     = "notes.md"
	docChapter1  = "chapter-one.md"
	docChapter2  = "chapter-two.md"
	docTitles    = "titles.comp"
	docPlates    = "plates.comp"
	docReference = "reference"
)

// Root is the directory every locator in the corpus sits under. It is an
// absolute path that does not exist and is never opened: locators are matched by
// prefix and nothing in the store stats them.
//
// Deliberately not under /Users or any home directory. A corpus reaching into
// one would be machine-coupled even if every name in it were invented.
const Root = "/synthetic/work"

// vaultDir is the shared autosave cache, which no project owns — exactly as the
// real one does not. Its events are unattributed, which is a fact rather than a
// gap in the fixture.
const vaultDir = Root + "/vault"

// Vocabulary is every invented name this corpus is allowed to use.
//
// It exists so that a test can ask "is this name one the fixture invented"
// rather than "is this name one of a list of real ones". The second question
// cannot be asked without writing the real names down, which is how an auditor
// comes to publish the thing it audits — see scripts/confidential's doc
// comment, which makes the same argument about the scanner itself.
//
// It is also strictly the stronger check. A denylist catches only what somebody
// thought to name; this catches ANY string that is not from here.
type Vocabulary struct {
	// Projects, Apps and Documents are matched exactly. The empty string is
	// allowed everywhere and is not a name: presence readings are about no
	// application, and a focus interval belongs to no project until a
	// resolution pass says so.
	Projects  []string
	Apps      []string
	Documents []string
	// Locators are directory prefixes. Every located event must sit under
	// one of them.
	Locators []string
}

// Words is the corpus's declared vocabulary.
//
// Adding a name to Events without adding it here is meant to fail. The
// vocabulary is the authority on what this fixture may say, so an undeclared
// name is an unreviewed one — which is the whole property being protected.
func Words() Vocabulary {
	return Vocabulary{
		Projects: []string{ProjectQuarry, ProjectLantern, ProjectMeadow, ProjectVellum},
		Apps:     []string{appForge, appMosaic, appAtlas},
		Documents: []string{
			docBrief, docOutline, docSpec, docSurvey, docDraft, docLetter, docNotes,
			docChapter1, docChapter2, docTitles, docPlates, docReference,
		},
		Locators: []string{
			Root + "/" + ProjectQuarry + "/",
			Root + "/" + ProjectLantern + "/",
			Root + "/" + ProjectMeadow + "/",
			Root + "/" + ProjectVellum + "/",
			vaultDir + "/",
		},
	}
}

// Fixture is a populated store and everything a test needs to say which part of
// it it is talking about.
//
// The days are exported by ROLE rather than by date. A test that says
// f.DarknessDay keeps its meaning if the corpus is ever re-dated; one that
// hard-codes 2025-03-05 does not, and the reader cannot tell which day matters.
type Fixture struct {
	// Store is open for writing and closed when the test ends.
	Store *store.Store
	// Path is the file, for a test that wants a second handle on the same
	// database rather than a second reading through the same code.
	Path string

	// Calendar is UTC, always. config.Default resolves the machine's own
	// zone, which would move every day boundary in this corpus depending on
	// where the test ran.
	Calendar sessions.Calendar
	// Params is the derivation the stored sittings were computed with.
	Params sittings.Params
	// Corpus is what they were drawn from: everything, excluding nothing.
	Corpus sittings.Corpus
	// ComputedAt is the fixed instant the derivation records. Fixed rather
	// than time.Now, so that two builds are byte-identical.
	ComputedAt time.Time

	// PlainDay is one project, one presence stretch, nothing unusual.
	PlainDay sessions.Day
	// ConcurrentDay carries three projects interleaved.
	ConcurrentDay sessions.Day
	// DarknessDay carries a dark stretch that begins three seconds before
	// the end of the focus interval containing it, and a timer-written
	// autosave inside it. The interval and the saves around it survive the
	// darkness; the autosave is the one thing it still suppresses.
	DarknessDay sessions.Day
	// PointsOnlyDay carries instants and no duration-bearing evidence at all.
	PointsOnlyDay sessions.Day
	// BetweenDay carries a cluster split by two dark stretches, leaving a
	// middle piece that lies between two observations rather than containing
	// either.
	BetweenDay sessions.Day
}

// New builds the corpus in the test's own temporary directory.
//
// One call. The store is closed for the caller when the test finishes.
func New(t *testing.T) *Fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle", "timetattle.db")
	f, err := Build(path)
	if err != nil {
		t.Fatalf("build the synthetic store at %s: %v", path, err)
	}
	t.Cleanup(func() { f.Store.Close() })
	return f
}

// Build is New without a *testing.T, for the handful of callers that are not a
// test — a script, or a benchmark that wants the store outside a test's
// lifetime. The caller closes the store.
func Build(path string) (*Fixture, error) {
	st, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	f := &Fixture{
		Store:    st,
		Path:     path,
		Calendar: sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday},
		Params: sittings.Params{
			Gap:             15 * time.Minute,
			LeadIn:          5 * time.Minute,
			TailOut:         5 * time.Minute,
			PresenceSources: []string{PresenceSource},
			IgnoreSources:   IgnoredSources,
		},
		Corpus:     sittings.Corpus{},
		ComputedAt: time.Date(2025, time.March, 8, 9, 0, 0, 0, time.UTC),

		PlainDay:      sessions.Day{Year: 2025, Month: time.March, Date: 3},
		ConcurrentDay: sessions.Day{Year: 2025, Month: time.March, Date: 4},
		DarknessDay:   sessions.Day{Year: 2025, Month: time.March, Date: 5},
		PointsOnlyDay: sessions.Day{Year: 2025, Month: time.March, Date: 6},
		BetweenDay:    sessions.Day{Year: 2025, Month: time.March, Date: 7},
	}
	if err := f.write(); err != nil {
		st.Close()
		return nil, err
	}
	return f, nil
}

// write harvests every source, attributes the located events, derives the
// sittings and records the one review and one correction.
func (f *Fixture) write() error {
	ctx := context.Background()
	bySource := map[string][]source.Event{}
	for _, e := range Events() {
		bySource[e.Source] = append(bySource[e.Source], e)
	}
	for _, id := range Sources {
		run, err := f.Store.BeginRun(ctx, store.RunSpec{SourceID: id, Input: Root + "/" + id})
		if err != nil {
			return fmt.Errorf("begin the %s run: %w", id, err)
		}
		if _, err := run.Write(ctx, bySource[id]); err != nil {
			return fmt.Errorf("write the %s events: %w", id, err)
		}
		if err := run.Finish(ctx); err != nil {
			return fmt.Errorf("finish the %s run: %w", id, err)
		}
	}

	// Attribution is a pass over locators, not something a source declares:
	// Run.Write deliberately drops a source-supplied Identity.Project, because
	// for a source that word means the folder a document sits in and here it
	// means the client project a resolution pass decided on.
	if _, err := f.Store.ResolveProjects(ctx, Resolve); err != nil {
		return fmt.Errorf("attribute the located events: %w", err)
	}

	events, err := f.Store.Events(ctx, store.Filter{})
	if err != nil {
		return fmt.Errorf("read the corpus back: %w", err)
	}
	res, err := sessions.Cluster(events, f.Params)
	if err != nil {
		return fmt.Errorf("cluster the corpus: %w", err)
	}
	if err := f.Store.ReplaceDerived(ctx, sessions.Replace(res, f.Corpus, f.ComputedAt)); err != nil {
		return fmt.Errorf("store the derivation: %w", err)
	}

	// One day looked at and one span corrected, so that a reader of the
	// overlay has something to read. The correction reattributes an hour of
	// the concurrent day, which is the case a single-project corpus cannot
	// pose at all.
	start, _ := f.Calendar.Bounds(f.ConcurrentDay)
	reviewID, err := f.Store.RecordReview(ctx, adjustments.Review{
		Start:      start.Add(3 * time.Hour),
		End:        start.Add(12 * time.Hour),
		Verdict:    adjustments.Recalled,
		Shown:      adjustments.Derivation,
		Origin:     "storefixture",
		RecordedAt: f.ComputedAt,
		Note:       "synthetic",
	})
	if err != nil {
		return fmt.Errorf("record the review: %w", err)
	}
	if _, err := f.Store.RecordAdjustment(ctx, adjustments.Adjustment{
		ReviewID:   reviewID,
		Start:      start.Add(5 * time.Hour),
		End:        start.Add(6 * time.Hour),
		Assertion:  adjustments.Attributed,
		Project:    ProjectMeadow,
		Origin:     "storefixture",
		RecordedAt: f.ComputedAt,
		Note:       "synthetic",
	}); err != nil {
		return fmt.Errorf("record the correction: %w", err)
	}
	return nil
}

// Resolve maps a locator to the client and project it belongs to, the way a
// registry would, by prefix.
//
// It is exported so a test can hand the same rule to a second store, and it
// reads a path rather than a document name because that is what
// Store.ResolveProjects does today. It returns BOTH halves because a marker
// holds both and one resolve produces both; see store.Attributed.
func Resolve(locator string) (store.Attributed, bool, error) {
	for _, p := range []string{ProjectQuarry, ProjectLantern, ProjectMeadow, ProjectVellum} {
		if strings.HasPrefix(locator, Root+"/"+p+"/") {
			return store.Attributed{Client: clientOf(p), Project: p}, true, nil
		}
	}
	// The vault is a shared cache no project owns, exactly as the real one
	// is. Its events are unattributed, which is a fact and not a gap.
	return store.Attributed{}, false, nil
}

// at is a wall clock on one of the corpus's days, in UTC.
func at(day, hour, min, sec int) time.Time {
	return time.Date(2025, time.March, day, hour, min, sec, 0, time.UTC)
}

func lit(day, h1, m1, s1, h2, m2, s2 int) source.Event {
	e := source.NewInterval(at(day, h1, m1, s1), at(day, h2, m2, s2), source.Identity{}, PresenceSource)
	e.State = "on"
	return e
}

func dark(day, h1, m1, s1, h2, m2, s2 int) source.Event {
	e := source.NewInterval(at(day, h1, m1, s1), at(day, h2, m2, s2), source.Identity{}, PresenceSource)
	e.State = "off"
	return e
}

// focus is a span an application held the front, which is evidence and carries
// no locator: a focus interval knows a bundle identifier and not a file.
func focus(day, h1, m1, h2, m2 int, app, doc string) source.Event {
	return source.NewInterval(at(day, h1, m1, 0), at(day, h2, m2, 0),
		source.Identity{App: app, Document: doc}, EvidenceSource)
}

// saved is a file written by something the person did.
func saved(day, h, m int, project, name, app string) source.Event {
	e := source.NewPoint(at(day, h, m, 0), source.Identity{App: app, Document: name}, FileSource)
	e.Locator = Root + "/" + project + "/" + name
	e.Cause = source.CauseUserAction
	return e
}

// autosaved is a file written by a timer. It asserts the application was
// running and nothing whatsoever about presence.
func autosaved(day, h, m int, name, app string) source.Event {
	e := source.NewPoint(at(day, h, m, 0), source.Identity{App: app, Document: name}, VaultSource)
	e.Locator = vaultDir + "/" + name
	e.Cause = source.CauseTimer
	return e
}

// Events is the whole corpus, in one place, so that what it holds can be read
// rather than reconstructed from the store.
//
// It is a function returning a fresh slice rather than a package variable: a
// caller that sorted or appended to a shared one would change every later
// build, and "the corpus is deterministic" is a property this package asserts.
func Events() []source.Event {
	var out []source.Event

	// --- The plain day. One project, one lit stretch, two focus intervals ten
	// minutes apart, which is inside the gap and so is one sitting rather than
	// two.
	out = append(out,
		lit(3, 8, 55, 0, 12, 10, 0),
		focus(3, 9, 0, 10, 30, appForge, docBrief),
		focus(3, 10, 40, 11, 50, appForge, docBrief),
		saved(3, 9, 40, ProjectQuarry, docBrief, appForge),
		saved(3, 10, 20, ProjectQuarry, docBrief, appForge),
		saved(3, 11, 30, ProjectQuarry, docOutline, appForge),
	)

	// The overnight lock, from the ignored source. Eight and a half hours: if a
	// derivation ever stopped honouring the ignore list this would show up as a
	// sitting nobody could miss, which is the point of putting it here rather
	// than a tidy ten minutes.
	locked := source.NewInterval(at(2, 22, 0, 0), at(3, 6, 30, 0), source.Identity{}, LockedSource)
	locked.State = "on"
	out = append(out, locked)

	// --- The concurrent day. Three projects interleaved inside one lit
	// stretch, split into two sittings by an hour at midday.
	out = append(out,
		lit(4, 8, 0, 0, 17, 15, 0),
		focus(4, 8, 10, 9, 5, appForge, docSpec),
		focus(4, 9, 10, 10, 0, appMosaic, docTitles),
		focus(4, 10, 5, 11, 15, appForge, docSurvey),
		focus(4, 11, 20, 12, 30, appMosaic, docTitles),
		focus(4, 13, 30, 15, 0, appForge, docSpec),
		focus(4, 15, 10, 17, 0, appMosaic, docPlates),
		saved(4, 8, 40, ProjectQuarry, docSpec, appForge),
		saved(4, 9, 40, ProjectLantern, docTitles, appMosaic),
		saved(4, 10, 50, ProjectMeadow, docSurvey, appForge),
		saved(4, 14, 20, ProjectQuarry, docSpec, appForge),
		saved(4, 16, 5, ProjectMeadow, docPlates, appMosaic),
		autosaved(4, 11, 0, docTitles, appMosaic),
		autosaved(4, 11, 15, docTitles, appMosaic),
	)

	// A horizon and the interval that completed it. Same source, same start,
	// same identity, so canonical_event hides the horizon and
	// EventsIncludingSuperseded keeps it. Both rows are real observations: the
	// harvest that ran at 17:00 saw something frontmost and no end for it.
	out = append(out,
		source.NewOpenEnd(at(4, 17, 0, 0), source.UnknownHorizon,
			source.Identity{App: appAtlas, Document: docReference}, EvidenceSource),
		focus(4, 17, 0, 17, 20, appAtlas, docReference),
	)

	// --- The darkness day. The dark stretch begins three seconds before the
	// end of the focus interval containing it, which is the shape found in the
	// real corpus on 2026-07-28.
	//
	// It carries BOTH cases darkness now has to be told apart, because the
	// difference between them is the whole of the rule:
	//
	//   - The autosave at 11:45 is written by a wall-clock timer inside the
	//     dark stretch. It asserts that Motion was running and nothing about
	//     whether work was happening, so it is suppressed and counted.
	//   - The focus interval running to 11:20 crosses into the same darkness,
	//     and the saves either side of it are user-action. None of them is
	//     touched: the sitting runs straight through the dark stretch, because
	//     a dark backlight observed a closed lid and a closed lid is not an
	//     observation that nobody was working.
	//
	// Until 2026-08-14 darkness cut the sitting short inside that still-running
	// interval, and this day existed to demonstrate it. The shape is kept and
	// the expectation inverted, so what changed stays legible.
	out = append(out,
		lit(5, 9, 30, 0, 11, 19, 57),
		dark(5, 11, 19, 57, 12, 40, 0),
		lit(5, 12, 40, 0, 14, 0, 0),
		focus(5, 10, 0, 11, 20, appForge, docDraft),
		focus(5, 12, 45, 13, 40, appMosaic, docPlates),
		saved(5, 10, 45, ProjectQuarry, docDraft, appForge),
		autosaved(5, 11, 45, docDraft, appForge),
		saved(5, 13, 20, ProjectMeadow, docPlates, appMosaic),
	)

	// --- The points-only day. No presence readings AT ALL, which is a third
	// thing from lit and from dark: nothing sampled the backlight, and a reader
	// that treats that as darkness deletes the day. No intervals either, so
	// every minute this day scores comes from the lead-in and tail-out.
	out = append(out,
		saved(6, 13, 2, ProjectVellum, docChapter1, appForge),
		saved(6, 13, 14, ProjectVellum, docChapter1, appForge),
		saved(6, 13, 26, ProjectVellum, docChapter2, appForge),
		saved(6, 13, 38, ProjectVellum, docChapter2, appForge),
		autosaved(6, 13, 30, docChapter2, appForge),
		// An isolated save with no neighbour inside the gap. It is its own
		// sitting, ten minutes long, made entirely of margin.
		saved(6, 15, 10, ProjectVellum, docNotes, appForge),
	)

	// --- The between day.
	//
	// Two observations twelve minutes apart, so one cluster; expanded they run
	// 08:55..09:37 with a two-minute hole at 09:15..09:17 that neither reaches.
	// Two dark stretches carve that hole into its own piece, and that piece
	// lies BETWEEN two observations rather than containing either. It must be
	// dropped rather than emitted as a sitting nothing evidenced.
	out = append(out,
		lit(7, 8, 0, 0, 9, 14, 0),
		dark(7, 9, 14, 0, 9, 15, 30),
		lit(7, 9, 15, 30, 9, 16, 0),
		dark(7, 9, 16, 0, 9, 16, 45),
		lit(7, 9, 16, 45, 11, 0, 0),
		focus(7, 9, 0, 9, 10, appForge, docLetter),
		focus(7, 9, 22, 9, 32, appForge, docLetter),
		saved(7, 9, 5, ProjectLantern, docLetter, appForge),
	)

	// A bare stop: the machine woke with something already frontmost, so macOS
	// never wrote the moment it became frontmost and no later harvest will
	// supply one. Sleep rather than horizon, and the difference decides whether
	// a later read tries to complete it.
	out = append(out,
		source.NewOpenStart(at(7, 6, 45, 0), source.UnknownSleep,
			source.Identity{App: appAtlas, Document: docReference}, EvidenceSource),
	)

	// A live horizon at the edge of the corpus: whatever held focus when the
	// last harvest ran. Nothing completes it, so it survives the canonical
	// read, which is what distinguishes it from the superseded one above.
	out = append(out,
		source.NewOpenEnd(at(7, 16, 30, 0), source.UnknownHorizon,
			source.Identity{App: appMosaic, Document: docPlates}, EvidenceSource),
	)

	return out
}
