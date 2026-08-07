// Package source defines the contract every evidence source satisfies.
//
// Timetattle reconstructs work from traces left in several unrelated places:
// a binary segment log maintained by macOS, a SQLite database, autosave files
// dropped on disk by a video compositor. Those have nothing in common
// mechanically, so this package fixes the one thing they must share — the
// shape of what they report — and lets each source get there however it must.
//
// Three axes run through the design, and conflating them causes trouble:
//
//   - Kind says how a source is READ. Biome segments need a parser; a Motion
//     autosave vault needs a directory walk and a filename pattern.
//   - Shape says what an event MEANS. A focus interval has a duration; a file
//     save is instantaneous; and a record whose partner was never written has
//     neither, which is a third thing rather than a damaged version of either.
//   - Cause says WHY the evidence exists, for the evidence that was caused by
//     something. A .blend1 backup is written by an explicit save; a Motion
//     autosave is written by a wall-clock timer that does not care whether
//     anyone is at the machine.
//
// These are independent, and the third one is not a refinement of the other
// two. Blender backups and Motion autosaves are the same Kind, read by the same
// reader, emitting the same Shape, and they are worth measurably different
// amounts: 4 of 4 against 9 of 15 against their own applications' focus
// intervals (project/private/2026-07-29-join-validation.md §4). A future source reading
// `git log` is a third Kind emitting the same Shape as Motion autosaves, and a
// SQLite source could emit either. Inference must therefore branch on Shape and
// Cause alone and stay ignorant of formats, which is why both live on the event
// rather than being deduced from whichever reader produced it.
//
// What this package deliberately does NOT do is decide when separate events
// belong to the same working session. Clustering is inference, it is fitted
// against ground truth elsewhere, and the spike measured total engaged time
// varying 2.3x across plausible thresholds. If sources carried their own gap
// parameters that fit could not be made globally, and the resulting numbers
// would be arbitrary rather than merely imprecise.
package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Kind names how a source is read. It exists for reporting and diagnostics —
// callers collect from every source identically and must not branch on it.
type Kind string

const (
	// KindBiome reads macOS's Biome SEGB segment logs.
	KindBiome Kind = "biome"
	// KindKnowledgeC reads a knowledgeC SQLite snapshot.
	KindKnowledgeC Kind = "knowledgec"
	// KindTimestampedFiles reads timestamped files described by a manifest.
	KindTimestampedFiles Kind = "timestamped-files"
)

// Shape says how much of an event's extent was actually observed.
//
// It reflects what a source CLAIMS to have observed and never the duration that
// happened to be measured. knowledgeC and Biome both report a start and an end,
// so their events are Interval even where the two are equal — and many presence
// rows are. A file mtime is one instant, so it is Point.
//
// A collector choosing by duration instead would emit the same observation
// under two shapes, and nothing downstream could recover: shape is part of an
// event's identity in the store, so both would persist as separate observations
// of one thing.
type Shape string

const (
	// Point is an instant: a file was saved, a command was run.
	Point Shape = "point"
	// Interval is a span with a real duration: an app held focus.
	Interval Shape = "interval"
	// Unbounded is an event seen at one edge only. Something happened, and
	// how long it lasted was never observed.
	//
	// This is not a tidiness case. A Point asserts there was no duration
	// and an Interval asserts a particular one, and both are false for a
	// Biome record whose partner macOS never wrote — 6.42h across five
	// confirmed days, 12% of attributable time, hangs off these.
	Unbounded Shape = "unbounded"
)

// Unknown says why an unbounded event is missing an endpoint.
//
// The reason has to travel with the event because the two causes need
// opposite treatment and cannot be told apart afterwards from the row alone.
type Unknown string

const (
	// UnknownHorizon is an event that had not finished when the harvest
	// ran. Ordinary rather than rare: the newest Biome segment usually
	// ends with a start, because something is frontmost right now.
	UnknownHorizon Unknown = "horizon"
	// UnknownSleep is a machine resuming with an application already
	// frontmost. Nothing became frontmost, so macOS wrote no start, and
	// no later harvest will supply one.
	UnknownSleep Unknown = "sleep"
	// UnknownLost is a partner record that was never written, for a reason
	// nothing in the data reveals. It is distinguished from a horizon
	// because later records already sit past it, so re-reading cannot
	// complete it; and from sleep because sleep names a cause that was
	// actually observed, and claiming it here would corrupt the one signal
	// that tells the two apart.
	//
	// Not yet seen in the wild: 48,047 live records classify as 23,832
	// pairs, 382 bare stops and a single horizon. It exists because the
	// pairing loop has a branch that must report something true if macOS
	// ever writes a sequence it currently does not.
	UnknownLost Unknown = "lost"
)

// Resolves reports whether a later harvest can complete this event.
//
// Only a horizon does. It sits at the edge of what had happened when the
// harvest ran, and completes itself once the missing edge is written. The
// other two are permanent: their repair is bounding against presence, which is
// interval arithmetic across two sources and therefore not a decision any
// single collector is in a position to make.
func (u Unknown) Resolves() bool { return u == UnknownHorizon }

// Cause says what wrote an artifact, and it is the difference between evidence
// of a person and evidence of a running process.
//
// It names a MECHANISM and never a conclusion. What a mechanism is worth is
// inference's judgment, fitted against ground truth; a source is only in a
// position to report how its files come to be written, so that is all this
// says. The same separation is why Shape does not carry a duration model.
//
// Measured 2026-07-29 (project/private/2026-07-29-join-validation.md §4): Blender
// .blend1 backups landed inside their own application's focus intervals 4 of 4,
// Motion autosaves 9 of 15 — same Kind, same Shape, same reader, same join.
// Task 85pGR measured the same split independently over a different span, 5 of 5
// against 1 of 2. The one Motion miss that was not the artifact's own clock
// resolution was written on 2026-07-11 at 17:53 with the screen off, during a
// break Ben independently recalls. So an artifact is not by itself evidence that
// anyone was there, and which artifacts are depends on this field.
//
// It is EMPTY for evidence that is not an artifact: a focus interval and a
// presence row are not caused by anything a manifest could describe. Empty
// therefore means "does not apply" and never "nobody said" — which is why the
// manifest schema requires the field of every source that can declare it rather
// than defaulting it. Collapsing "not applicable" into "not established" is the
// mistake this project has already shipped in three separate tools.
type Cause string

const (
	// CauseUserAction is an artifact written inside the application's own
	// interaction loop, by something the person did: Blender's Save writing a
	// .blend1, an export, a commit. It is the strongest presence signal any
	// artifact carries, and it is still not airtight — a script can drive the
	// same menu action — so it remains evidence to be joined against presence
	// rather than a substitute for it.
	CauseUserAction Cause = "user-action"
	// CauseTimer is an artifact written on a schedule the person does not
	// touch, as Motion autosaves every fifteen minutes. It asserts that the
	// application was running and nothing whatsoever about presence: the
	// 2026-07-11 17:53 autosave was written during a confirmed break with the
	// screen off.
	CauseTimer Cause = "timer"
	// CauseRecovery is an artifact written because something went wrong —
	// abnormal termination, a crash, a relaunch offering to restore work.
	// Photoshop's AutoRecover folder is the shape this exists for.
	//
	// Nothing produces one yet, and it is deliberately its own value rather
	// than folded into either of the others because its evidentiary weight is
	// UNMEASURED: nobody has established whether a recovery file tracks
	// presence the way a save does or nothing at all the way a timer does.
	// Treat it the way this codebase already treats /device/isLocked's
	// polarity — safe to store, not yet safe to draw conclusions from.
	//
	// It ships now because the manifest field is required and the set is
	// closed. An author describing an AutoRecover folder with only the other
	// two values available would have to declare something false, and their
	// manifest lives on a disk we cannot migrate.
	CauseRecovery Cause = "recovery"
)

// Validate reports whether this is a cause anything recognises.
//
// The empty cause is accepted, because most evidence is not an artifact. An
// unrecognised one is refused rather than passed through: inference branches on
// this value, and a typo that reaches a switch is counted as whatever the
// default arm assumed — silently, and in the direction nobody chose.
func (c Cause) Validate() error {
	switch c {
	case "", CauseUserAction, CauseTimer, CauseRecovery:
		return nil
	default:
		return fmt.Errorf("unknown cause %q; supported values are %q, %q and %q",
			c, CauseUserAction, CauseTimer, CauseRecovery)
	}
}

// Identity is what an event is about, and it is what makes evidence joinable.
//
// Biome knows Motion held focus from 13:49 to 14:25 but not which document was
// open. A Motion autosave knows dark-crystal-v2 was written at 14:01 but not
// for how long. Neither answers "how long on dark-crystal-v2" alone. App is
// the key that lets one be attributed to the other, so it is not a label —
// it is load-bearing.
type Identity struct {
	// App is the bundle identifier, e.g. com.apple.motionapp. Empty for
	// evidence that is not about an application, such as presence.
	App string
	// Document names the specific file or composition, when known.
	Document string
	// Project groups documents belonging to the same body of work.
	Project string
	// Client is who that body of work is for.
	//
	// IT IS NEVER STORED ON AN EVENT and no column corresponds to it. A
	// project's client has 38 distinct values across ~973,000 events, so
	// writing it per row would be ~927,599 updates to record something the
	// size of a small table, and the attribution pass would rewrite all of
	// them on every harvest. It lives in the append-only client_project log
	// (migration 0012) and is stamped here on the way out, by folding that log
	// and asking which client the project belonged to AT THIS EVENT'S OWN
	// INSTANT — see internal/clientmap.
	//
	// That last part is why this is a field rather than a lookup the consumer
	// does: a client association is a fact about a moment in time, so two
	// events of one project can honestly carry different clients if the
	// project was reassigned between them. Empty means no client is known for
	// this project at this instant, which is three different facts —
	// unasserted, not yet effective, or claimed by two directories at once —
	// and internal/clientmap is where they are told apart.
	Client string
}

// Event is one observation. Sources produce these; everything downstream
// consumes them without knowing where they came from.
type Event struct {
	// Start is when the event happened, or began. It is the zero time on
	// an Unbounded event whose beginning was never observed.
	Start time.Time
	// End equals Start for a Point. It is the zero time on an Unbounded
	// event that had not finished when it was observed.
	End time.Time
	// Shape says how much of the extent was observed.
	Shape Shape
	// Unknown says why an endpoint is missing, and is empty unless Shape
	// is Unbounded.
	Unknown Unknown
	// Identity is what the event is about.
	Identity Identity
	// State is the scalar reading a source took, where it takes one, and is
	// empty otherwise. /display/isBacklit reports "on" or "off"; the same
	// shape fits /device/isLocked and /bluetooth/isConnected.
	//
	// It is part of an event's identity in the store, for a measured reason.
	// knowledgeC writes contradictory overlapping rows: on 2026-07-14 at
	// 09:01:12 there exist both an isBacklit=1 and an isBacklit=0 row with
	// identical endpoints. Those are two observations, and without the
	// reading in the key they collapse into one.
	//
	// It is deliberately not part of Identity, which is compared by struct
	// equality to recognise one document seen in two places. A reading is not
	// what an event is about — lit and dark are two observations of the same
	// backlight — so putting it there would break that join.
	//
	// Storing the reading rather than only the rows that read "on" is what
	// keeps three states available. A table of lit intervals cannot say "the
	// sampler was silent here", and the difference decided whether 90 minutes
	// of confirmed work on 2026-07-14 counted: observed off is a row,
	// observed on is a row, and never sampled is the absence of one.
	State string
	// Source is the ID of the source that reported this, kept so a
	// suspicious number can be traced back to the thing that produced it.
	Source string
	// Locator is the absolute path the evidence was read from, and is
	// empty for evidence that observes no file — a Biome focus interval
	// knows a bundle identifier, and presence is not about a file at all.
	//
	// It is provenance rather than identity, which is the finer-grained
	// form of what Source does, and it is deliberately NOT part of
	// Identity. event_identity (0001_initial.sql) keys on source_id,
	// shape, the endpoints, app, document and state — never locator — and
	// that is the rule this field follows, not a gap in it.
	//
	// Today, store.Store.ResolveProjects reads only Locator: it climbs the
	// filesystem from the path and matches it against the directories a
	// project has blessed. It does not look at Document at all, so an
	// autosave-vault copy is unattributed for exactly the reason the vault
	// is a shared cache no registry entry names.
	//
	// Keeping Locator out of Identity is what preserves the OTHER route:
	// the autosave-vault copy and the project-directory copy of one
	// composition compare equal on every Identity field, which is the
	// precondition for resolving the vault copy by matching its Document
	// name against a project's own document instead of against a path.
	// scripts/motionjoin validates that this name join is usable — 9 of 15
	// real vault composition names matched a document under one registered
	// root, with zero cross-project ambiguity
	// (project/private/2026-07-31-motion-vault-validation.md) — but that validation
	// is an offline analysis script, not something ResolveProjects or any
	// other part of the ingest pipeline does today. A path inside Identity
	// would foreclose ever wiring it in: every copy of a document would
	// become a different document, and there would be nothing left to
	// match by name.
	//
	// What this does NOT guarantee: two events that agree on every
	// Identity field AND share a source_id, but were read from different
	// locations, collapse to one stored row, because event_identity has no
	// way to keep both. Store.Write's ON CONFLICT DO NOTHING (internal/store/run.go)
	// keeps whichever one reached the database first, and that is an
	// accident of collector or ingestion order rather than anything about
	// the two observations. See
	// TestDuplicateLocatorCollisionKeepsWhicheverWasInsertedFirst, which
	// forces the collision and shows the surviving Locator is decided by
	// insertion order alone. Since Attribution reads whatever Locator the
	// surviving row happens to carry, the practical exposure is: if the
	// losing copy was the one inside a registered project directory and
	// the winner was inside an unregistered shared cache (the Autosave
	// Vault is exactly that — no single project owns it), the observation
	// silently attributes to nothing. Measured against real data
	// (project/private/2026-07-31-locator-identity.md), this requires two events
	// from the SAME source to land on the exact same instant from
	// different paths — cross-source collisions cannot happen at all,
	// because source_id is itself part of the key, which is also why the
	// vault (motion-autosave) and a project's own copy (fs-observations)
	// can never collide under the current collector wiring regardless of
	// Locator: they simply coexist as two permanent rows.
	Locator string
	// Payload is what the source knows about this observation that no column
	// on an event describes, as a JSON object, and is empty where there is
	// nothing extra to say.
	//
	// It exists because two of the facts Timetattle was asked for are
	// per-record figures with nowhere else to go: which Claude Code SESSION a
	// transcript record belonged to, and what the model SPENT on it. See
	// internal/usage for the shape and migration 0014 for the schema
	// reasoning.
	//
	// IT IS DELIBERATELY OUTSIDE IDENTITY, AND THAT IS THE WHOLE POINT OF THE
	// FIELD. State is in the store's dedup key, for a measured reason
	// (knowledgeC writes two contradictory readings with identical endpoints),
	// and a per-record token count carried there would make every record its
	// own identity: the same transcript re-read on the next harvest would
	// insert a second row for every record it holds, for ever. Payload is
	// named by no part of event_identity, so re-reading a transcript writes
	// nothing at all — which is the property TestHarvestingTheSameEventsTwice
	// WithPayloadsChangesNothing pins in internal/store.
	//
	// It is likewise not part of Identity, which is compared by struct
	// equality to recognise one document seen in two places. What a record
	// cost is not what it is about.
	//
	// A payload is written ONCE, when the observation is first stored. An
	// event that already exists is not rewritten to carry one, because events
	// are observations and never change — so rows stored before a source began
	// emitting payloads carry none, permanently, and that is "nothing was
	// recorded" rather than zero.
	//
	// IT IS EMPTY ON EVERY EVENT READ BACK OUT OF THE STORE, and that is a
	// deliberate asymmetry rather than a gap. store.Events does not select
	// the column: a whole-corpus read is a million events, and carrying ~60
	// MB of payload text through it would pay for something no reader of
	// events wants — the figures are aggregated in SQL by store.Store.Usage,
	// which never materialises a payload in Go at all. So an empty Payload on
	// an event that came from the store says nothing whatever about whether
	// one was recorded, and no caller may read it that way.
	Payload string
	// Cause is what wrote the artifact this event observes, and is empty for
	// evidence that is not an artifact — a Biome focus interval and a presence
	// row are not caused by anything a manifest can describe.
	//
	// It is asserted once, by the source, at collect time. It is not derived
	// later from Source, even though a source declares one cause for every file
	// it reads: deriving it would mean every read of the store had to join back
	// to manifest metadata that may since have been edited, and the answer would
	// change under rows that were correct when they were written.
	//
	// It is deliberately NOT part of an event's identity in the store. Unlike
	// State, it is not a reading taken per observation — it is a standing
	// declaration about the source, so source_id already carries it, and a
	// manifest author who corrects a mis-stated cause would otherwise turn every
	// file already harvested into a second observation of itself.
	Cause Cause
}

// NewPoint records something that happened at an instant.
func NewPoint(at time.Time, id Identity, sourceID string) Event {
	return Event{Start: at, End: at, Shape: Point, Identity: id, Source: sourceID}
}

// NewInterval records something that occupied a span of time.
func NewInterval(start, end time.Time, id Identity, sourceID string) Event {
	return Event{Start: start, End: end, Shape: Interval, Identity: id, Source: sourceID}
}

// NewOpenStart records something observed only as it ended.
//
// The canonical case is a bare Biome stop: the machine woke with the
// application already frontmost, so there was no moment at which it became
// frontmost for macOS to record.
func NewOpenStart(end time.Time, why Unknown, id Identity, sourceID string) Event {
	return Event{End: end, Shape: Unbounded, Unknown: why, Identity: id, Source: sourceID}
}

// NewOpenEnd records something observed only as it began.
//
// The canonical case is whatever held focus when the harvest ran.
func NewOpenEnd(start time.Time, why Unknown, id Identity, sourceID string) Event {
	return Event{Start: start, Shape: Unbounded, Unknown: why, Identity: id, Source: sourceID}
}

// Duration is how long the event lasted; always zero for a Point.
//
// An Unbounded event reports zero as well, because the span it occupied was
// never observed. Inventing one from the surrounding records is the mistake
// this package exists to refuse: on the one case measured against knowledgeC
// it would have charged 5m09s of an idle machine to Safari in place of 2s of
// real use.
func (e Event) Duration() time.Duration {
	if e.Shape == Unbounded {
		return 0
	}
	return e.End.Sub(e.Start)
}

// Contains reports whether other happened within e.
//
// This is deliberately a containment test and nothing more. It answers "was
// this save made while that app held focus", which is mechanical. It is not
// sessionization, and it applies no tolerance or threshold — anything of that
// sort belongs in inference, where it can be calibrated.
func (e Event) Contains(other Event) bool {
	if e.Shape != Interval {
		return false
	}
	if e.Identity.App != other.Identity.App {
		return false
	}
	return !other.Start.Before(e.Start) && !other.Start.After(e.End)
}

// Validate reports why an event cannot be trusted.
//
// A source that emits nonsense should lose that event, not poison a whole
// harvest, so this exists to let the registry drop bad events individually
// while naming what was wrong.
func (e Event) Validate() error {
	if e.Source == "" {
		return fmt.Errorf("event at %s names no source", e.Anchor().Format(time.RFC3339))
	}
	// A relative locator does not fail loudly downstream. It simply never
	// prefix-matches a registered directory, so the work attributes to
	// nothing and disappears — which is the failure this project exists to
	// catch, so it is refused here instead.
	if e.Locator != "" && !filepath.IsAbs(e.Locator) {
		return fmt.Errorf("event from %s for %s was found at %q, which is a relative path; "+
			"attribution matches locators against registered directories and a relative one can never match",
			e.Source, e.subject(), e.Locator)
	}
	// Checked here as well as at manifest load, because a manifest is not the
	// only way a source is built and the store's CHECK fails the whole batch
	// rather than the one event. The registry can drop one event; a failed batch
	// costs a source its entire harvest.
	if err := e.Cause.Validate(); err != nil {
		return fmt.Errorf("event from %s for %s reports an %w", e.Source, e.subject(), err)
	}
	// A payload that is not a JSON object does not fail loudly downstream. The
	// store reads it back with json_extract, which returns NULL for every path
	// of a value that is not an object — so a malformed payload is
	// indistinguishable, in every figure computed from it, from a source that
	// recorded nothing. Refused here so that the registry loses the one event
	// rather than a whole source's harvest, and refused again by the store's
	// own trigger (migration 0014) for a writer that never came through here.
	if e.Payload != "" && !isJSONObject(e.Payload) {
		return fmt.Errorf("event from %s for %s carries a payload that is not a JSON object; "+
			"the store reads payloads back by JSON path, and anything else reads as though nothing was recorded",
			e.Source, e.subject())
	}
	switch e.Shape {
	case Point, Interval:
		if e.Start.IsZero() {
			return fmt.Errorf("event from %s for %s has no timestamp", e.Source, e.subject())
		}
		if e.Unknown != "" {
			return fmt.Errorf("%s event from %s for %s claims a missing endpoint (%q) but has both",
				e.Shape, e.Source, e.subject(), e.Unknown)
		}
		if e.Shape == Point && !e.End.Equal(e.Start) {
			return fmt.Errorf("point event from %s for %s has an end that differs from its start", e.Source, e.subject())
		}
		if e.Shape == Interval && e.End.Before(e.Start) {
			return fmt.Errorf("interval from %s for %s ends %s before it starts",
				e.Source, e.subject(), e.Start.Sub(e.End))
		}
	case Unbounded:
		// Exactly one edge, or this is not the shape it claims to be:
		// with both it is an interval, and with neither there is no
		// instant at which anything was observed to happen.
		if !e.Start.IsZero() && !e.End.IsZero() {
			return fmt.Errorf("unbounded event from %s for %s has both endpoints; it is an interval",
				e.Source, e.subject())
		}
		if e.Start.IsZero() && e.End.IsZero() {
			return fmt.Errorf("unbounded event from %s for %s has no timestamp at either end",
				e.Source, e.subject())
		}
		if e.Unknown == "" {
			return fmt.Errorf("unbounded event from %s for %s does not say why its endpoint is missing; "+
				"without that the store cannot tell one it should wait to complete from one it must bound against presence",
				e.Source, e.subject())
		}
	default:
		return fmt.Errorf("event from %s for %s has unknown shape %q", e.Source, e.subject(), e.Shape)
	}
	return nil
}

// isJSONObject reports whether text is a well-formed JSON object.
//
// json.Valid alone is not enough: `7`, `"a"` and `[1,2]` are all valid JSON and
// none of them answers a `$.session` lookup, so each would store cleanly and
// then read back as an absence. The leading brace is checked after validity so
// that the cheap test does not have to reason about leading whitespace on its
// own.
func isJSONObject(text string) bool {
	if !json.Valid([]byte(text)) {
		return false
	}
	trimmed := strings.TrimLeft(text, " \t\r\n")
	return strings.HasPrefix(trimmed, "{")
}

// Anchor is whichever endpoint was actually observed.
//
// Every event has one, and which field holds it varies by shape. Callers that
// order events, bucket them by day, or key them for deduplication need that
// instant without caring where it lives — sorting on Start alone puts every
// bare stop at the zero time, decades before the data it belongs among, and
// the same mistake in a day bucket silently empties a day.
func (e Event) Anchor() time.Time {
	if e.Start.IsZero() {
		return e.End
	}
	return e.Start
}

// subject names what an event is about for error messages. Presence events
// legitimately have no app, so this degrades rather than printing an empty
// string into the middle of a sentence.
func (e Event) subject() string {
	switch {
	case e.Identity.Document != "":
		return e.Identity.Document
	case e.Identity.App != "":
		return e.Identity.App
	default:
		return "unattributed evidence"
	}
}

// Source is one place evidence comes from.
//
// Implementations range from a binary parser to a directory walk driven by a
// manifest. The registry treats them identically; if a caller ever needs to
// know which is which in order to work, this interface is wrong.
type Source interface {
	// ID is a stable identifier, e.g. "biome-appusage". It appears on every
	// event the source produces.
	ID() string
	// Kind reports how this source is read, for diagnostics only.
	Kind() Kind
	// Collect returns events that had not finished before since. A source
	// with nothing new returns no events and no error.
	//
	// The boundary is deliberately inclusive, and it is not a watermark in
	// the usual sense. An observation still running at the last harvest is
	// not old news, so re-emitting it is the point: the second reading is
	// the same real event at greater completeness, and dropping it would
	// leave the first, unbounded reading as the permanent record of a
	// session that has since ended. An event whose end was never observed
	// is returned whatever since is, because it has reached no instant at
	// which it could be considered past.
	//
	// The cost is that the same evidence legitimately arrives more than
	// once, so deduplication cannot be positional and must be by content at
	// the store. Since does not make a run idempotent; it only keeps a run
	// from re-reading history it has already finished with.
	Collect(ctx context.Context, since time.Time) ([]Event, error)
}

// Registry holds the sources a run will collect from.
type Registry struct {
	sources []Source
	seen    map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{seen: make(map[string]bool)}
}

// Register adds a source. Registration order is preserved, so harvest output
// reads the same way twice rather than shuffling with map iteration.
func (r *Registry) Register(s Source) error {
	id := s.ID()
	if id == "" {
		return errors.New("source has an empty ID; every source needs one to attribute its events")
	}
	if r.seen[id] {
		return fmt.Errorf("source %q is already registered", id)
	}
	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	r.seen[id] = true
	r.sources = append(r.sources, s)
	return nil
}

// Sources returns the registered sources in registration order.
func (r *Registry) Sources() []Source {
	out := make([]Source, len(r.sources))
	copy(out, r.sources)
	return out
}

// Collect gathers events from every source.
//
// A failing source is reported but does not stop the others. An uninstalled
// app or a renamed directory is ordinary, and losing an entire harvest over
// one of them would be the same permanent data loss the harvester exists to
// prevent. The returned error joins every failure; the returned events are
// everything that succeeded.
func (r *Registry) Collect(ctx context.Context, since time.Time) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var (
		events []Event
		errs   []error
	)
	for _, s := range r.sources {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		collected, err := s.Collect(ctx, since)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %q: %w", s.ID(), err))
			continue
		}
		for _, e := range collected {
			if err := e.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("source %q produced an unusable event: %w", s.ID(), err))
				continue
			}
			events = append(events, e)
		}
	}
	return events, errors.Join(errs...)
}

// std is the registry sources join from their own init, so adding a collector
// means adding a file and a blank import rather than editing existing code.
var std = NewRegistry()

// Register adds a source to the package-level registry.
//
// This panics rather than returning an error because it is meant to be called
// from init, where there is no caller to hand a failure to. A duplicate ID is
// a programming mistake, and failing at startup is better than silently
// collecting from one of two sources that think they are the same.
func Register(s Source) {
	if err := std.Register(s); err != nil {
		panic("source.Register: " + err.Error())
	}
}

// Sources returns the package-level registry's sources.
func Sources() []Source { return std.Sources() }

// Collect gathers events from the package-level registry.
func Collect(ctx context.Context, since time.Time) ([]Event, error) {
	return std.Collect(ctx, since)
}
