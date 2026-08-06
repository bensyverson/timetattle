package source_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// at is a readable timestamp on the day we have ground truth for.
func at(hour, min, sec int) time.Time {
	return time.Date(2026, 7, 28, hour, min, sec, 0, time.Local)
}

// stub is a Source that returns whatever it was constructed with. It stands in
// for both styles of implementation, which is the point: the registry must not
// care whether a source parses SEGB segments or reads a manifest.
type stub struct {
	id     string
	kind   source.Kind
	events []source.Event
	err    error
}

func (s stub) ID() string        { return s.id }
func (s stub) Kind() source.Kind { return s.kind }

func (s stub) Collect(_ context.Context, _ time.Time) ([]source.Event, error) {
	return s.events, s.err
}

func TestPointHasNoDuration(t *testing.T) {
	e := source.NewPoint(at(6, 32, 0), source.Identity{App: "com.apple.motionapp"}, "motion-autosave")
	if e.Shape != source.Point {
		t.Errorf("Shape = %q, want %q", e.Shape, source.Point)
	}
	if got := e.Duration(); got != 0 {
		t.Errorf("Duration() = %v, want 0", got)
	}
	if !e.End.Equal(e.Start) {
		t.Errorf("End = %v, want it to equal Start %v", e.End, e.Start)
	}
}

func TestIntervalReportsDuration(t *testing.T) {
	e := source.NewInterval(at(13, 49, 40), at(14, 25, 40), source.Identity{App: "com.apple.motionapp"}, "biome-appusage")
	if e.Shape != source.Interval {
		t.Errorf("Shape = %q, want %q", e.Shape, source.Interval)
	}
	if got, want := e.Duration(), 36*time.Minute; got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}
}

// The discriminator has to be readable without knowing which source produced
// the event; that is the whole reason it lives on the event rather than being
// inferred from the reader.
func TestShapeIsIndependentOfKind(t *testing.T) {
	p := source.NewPoint(at(6, 32, 0), source.Identity{App: "com.apple.motionapp"}, "motion-autosave")
	i := source.NewInterval(at(13, 49, 40), at(14, 25, 40), source.Identity{App: "com.apple.motionapp"}, "biome-appusage")

	if p.Shape == i.Shape {
		t.Fatal("a point and an interval must not share a Shape")
	}
	for _, e := range []source.Event{p, i} {
		if e.Source == "" {
			t.Errorf("event from %q lost its source attribution", e.Identity.App)
		}
	}
}

func TestValidateRejectsBackwardsInterval(t *testing.T) {
	e := source.NewInterval(at(14, 25, 0), at(13, 49, 0), source.Identity{App: "com.apple.motionapp"}, "biome-appusage")
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for an interval ending before it starts")
	}
	if !strings.Contains(err.Error(), "com.apple.motionapp") {
		t.Errorf("Validate() = %q, want the app named so the bad source is identifiable", err)
	}
}

func TestValidateRejectsMissingStart(t *testing.T) {
	e := source.Event{Shape: source.Point, Source: "motion-autosave"}
	if err := e.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error for an event with no timestamp")
	}
}

func TestValidateRejectsUnattributedEvent(t *testing.T) {
	e := source.NewPoint(at(6, 32, 0), source.Identity{App: "com.apple.motionapp"}, "")
	if err := e.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error when no source is named")
	}
}

// Presence is real evidence and has no app, so App cannot be mandatory.
func TestValidateAcceptsPresenceWithoutAnApp(t *testing.T) {
	e := source.NewInterval(at(5, 18, 24), at(19, 16, 24), source.Identity{}, "knowledgec-presence")
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want presence without an app to be valid", err)
	}
}

// A .blend1 backup and a Motion autosave are the same Kind, read by the same
// reader, emitting the same Shape — and they are not worth the same. Measured
// 2026-07-29 (project/private/2026-07-29-join-validation.md §4): Blender's backups
// landed inside their own app's focus intervals 4 of 4, Motion's autosaves 9 of
// 15. Nothing about Kind or Shape says why, so the cause travels on the event.
func TestCauseIsIndependentOfKindAndShape(t *testing.T) {
	save := source.NewPoint(at(6, 32, 0), source.Identity{App: "org.blenderfoundation.blender"}, "blender-backups")
	save.Cause = source.CauseUserAction
	autosave := source.NewPoint(at(17, 53, 0), source.Identity{App: "com.apple.motionapp"}, "motion-autosave")
	autosave.Cause = source.CauseTimer

	if save.Shape != autosave.Shape {
		t.Fatalf("shapes differ (%q and %q); this test is about two events that agree on everything Shape says",
			save.Shape, autosave.Shape)
	}
	if save.Cause == autosave.Cause {
		t.Errorf("both events report cause %q; an explicit save and a wall-clock autosave are different evidence", save.Cause)
	}
	for _, e := range []source.Event{save, autosave} {
		if err := e.Validate(); err != nil {
			t.Errorf("Validate() = %v for a %s artifact", err, e.Cause)
		}
	}
}

func TestEveryCauseTimetattleShipsIsAccepted(t *testing.T) {
	for _, c := range []source.Cause{source.CauseUserAction, source.CauseTimer, source.CauseRecovery} {
		if err := c.Validate(); err != nil {
			t.Errorf("Cause(%q).Validate() = %v, want it accepted", c, err)
		}
	}
}

// The empty cause is how evidence that is not an artifact at all reports itself:
// a focus interval and a presence row are not caused by anything a manifest
// could describe. It is therefore valid, and it is NOT a fourth category of
// artifact.
func TestTheEmptyCauseIsAcceptedBecauseMostEvidenceIsNotAnArtifact(t *testing.T) {
	if err := source.Cause("").Validate(); err != nil {
		t.Errorf("Cause(\"\").Validate() = %v, want the absent cause accepted", err)
	}
	presence := source.NewInterval(at(5, 18, 24), at(19, 16, 24), source.Identity{}, "knowledgec-presence")
	if err := presence.Validate(); err != nil {
		t.Errorf("Validate() = %v for presence, which no manifest causes", err)
	}
}

// A value nothing recognises must not reach inference, where it would fall
// through a switch and be counted as whatever the default arm assumed.
func TestAnUnrecognisedCauseIsRefused(t *testing.T) {
	err := source.Cause("human").Validate()
	if err == nil {
		t.Fatal("Cause(\"human\").Validate() = nil, want an unknown cause refused")
	}
	if !strings.Contains(err.Error(), "human") {
		t.Errorf("Validate() = %q, want the bad value quoted so the typo is visible", err)
	}
	if !strings.Contains(err.Error(), string(source.CauseUserAction)) {
		t.Errorf("Validate() = %q, want the accepted values named", err)
	}
}

func TestValidateRefusesAnEventWhoseCauseIsNotOneWeKnow(t *testing.T) {
	e := source.NewPoint(at(6, 32, 0), source.Identity{App: "com.apple.motionapp"}, "motion-autosave")
	e.Cause = "autosave"
	err := e.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an event with an unknown cause refused")
	}
	if !strings.Contains(err.Error(), "autosave") {
		t.Errorf("Validate() = %q, want the bad value quoted", err)
	}
}

func TestRegistryReturnsRegisteredSources(t *testing.T) {
	r := source.NewRegistry()
	biome := stub{id: "biome-appusage", kind: source.KindBiome}
	motion := stub{id: "motion-autosave", kind: source.KindTimestampedFiles}

	if err := r.Register(biome); err != nil {
		t.Fatalf("Register(biome) = %v", err)
	}
	if err := r.Register(motion); err != nil {
		t.Fatalf("Register(motion) = %v", err)
	}

	got := r.Sources()
	if len(got) != 2 {
		t.Fatalf("Sources() returned %d sources, want 2", len(got))
	}
	// Registration order is the reporting order; a map would make harvest
	// output shuffle between runs for no reason.
	if got[0].ID() != "biome-appusage" || got[1].ID() != "motion-autosave" {
		t.Errorf("Sources() = %q, %q, want registration order preserved", got[0].ID(), got[1].ID())
	}
}

func TestRegistryRejectsDuplicateID(t *testing.T) {
	r := source.NewRegistry()
	if err := r.Register(stub{id: "motion-autosave"}); err != nil {
		t.Fatalf("first Register = %v", err)
	}
	err := r.Register(stub{id: "motion-autosave"})
	if err == nil {
		t.Fatal("Register() = nil for a duplicate ID, want an error")
	}
	if !strings.Contains(err.Error(), "motion-autosave") {
		t.Errorf("Register() = %q, want the colliding ID named", err)
	}
}

func TestRegistryRejectsAnonymousSource(t *testing.T) {
	r := source.NewRegistry()
	if err := r.Register(stub{id: ""}); err == nil {
		t.Fatal("Register() = nil for an empty ID, want an error")
	}
}

// The call site collects; it does not know or care that one of these parses a
// binary segment format and the other reads files off disk.
func TestRegistryCollectsFromEverySourceWithoutSpecialCasing(t *testing.T) {
	r := source.NewRegistry()
	mustRegister(t, r, stub{
		id:   "biome-appusage",
		kind: source.KindBiome,
		events: []source.Event{
			source.NewInterval(at(13, 49, 40), at(14, 25, 40), source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		},
	})
	mustRegister(t, r, stub{
		id:   "motion-autosave",
		kind: source.KindTimestampedFiles,
		events: []source.Event{
			source.NewPoint(at(14, 1, 0), source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave"),
		},
	})

	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Collect() returned %d events, want 2", len(events))
	}
}

// One broken source must not cost us the others' data. A missing app or a
// renamed directory is normal, and losing a whole harvest over it would be
// the same permanent loss the harvester exists to prevent.
func TestRegistryKeepsGoingWhenOneSourceFails(t *testing.T) {
	r := source.NewRegistry()
	boom := errors.New("vault directory does not exist")
	mustRegister(t, r, stub{id: "broken", kind: source.KindTimestampedFiles, err: boom})
	mustRegister(t, r, stub{
		id:   "biome-appusage",
		kind: source.KindBiome,
		events: []source.Event{
			source.NewInterval(at(13, 49, 40), at(14, 25, 40), source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		},
	})

	events, err := r.Collect(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("Collect() = nil error, want the failure reported")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Collect() error = %v, want it to wrap the source failure", err)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("Collect() error = %q, want the failing source named", err)
	}
	if len(events) != 1 {
		t.Fatalf("Collect() returned %d events, want the healthy source's 1", len(events))
	}
}

func TestRegistryDropsInvalidEvents(t *testing.T) {
	r := source.NewRegistry()
	mustRegister(t, r, stub{
		id:   "biome-appusage",
		kind: source.KindBiome,
		events: []source.Event{
			source.NewInterval(at(14, 25, 0), at(13, 49, 0), source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		},
	})

	events, err := r.Collect(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("Collect() = nil error, want the invalid event reported")
	}
	if len(events) != 0 {
		t.Errorf("Collect() returned %d events, want the invalid one dropped", len(events))
	}
}

func TestCollectStopsOnCancelledContext(t *testing.T) {
	r := source.NewRegistry()
	mustRegister(t, r, stub{id: "biome-appusage", kind: source.KindBiome})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Collect(ctx, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() = %v, want context.Canceled", err)
	}
}

// The join is the product. Biome knows Motion was frontmost 13:49-14:25 but
// not which document; the autosave knows dark-crystal-v2 was saved at 14:01
// but not for how long. Neither is useful alone, so the event model has to
// carry enough identity to put them together.
func TestFileEvidenceJoinsToFocusIntervalsByApp(t *testing.T) {
	focus := source.NewInterval(at(13, 49, 40), at(14, 25, 40), source.Identity{App: "com.apple.motionapp"}, "biome-appusage")
	otherApp := source.NewInterval(at(13, 50, 0), at(14, 20, 0), source.Identity{App: "org.blenderfoundation.blender"}, "biome-appusage")
	save := source.NewPoint(at(14, 1, 0), source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")
	outside := source.NewPoint(at(6, 32, 0), source.Identity{App: "com.apple.motionapp", Document: "scanline2-card-granted"}, "motion-autosave")

	if !focus.Contains(save) {
		t.Error("the 14:01 save should fall inside the 13:49-14:25 Motion interval")
	}
	if otherApp.Contains(save) {
		t.Error("a Motion save must not attach to a Blender interval")
	}
	if focus.Contains(outside) {
		t.Error("the 06:32 save is outside the interval and must not attach")
	}
	if save.Identity.Document == "" {
		t.Error("file evidence must carry the document, or the join answers nothing useful")
	}
}

// Adding a source should mean adding a file, not editing one. A package-level
// registry that sources join from their own init lets a new collector be wired
// in with a blank import, the way database/sql drivers are, so no existing
// file has to grow a line for every source we add.
func TestPackageRegistryAcceptsSelfRegistration(t *testing.T) {
	source.Register(stub{id: "test-selfregistered", kind: source.KindBiome})

	var found bool
	for _, s := range source.Sources() {
		if s.ID() == "test-selfregistered" {
			found = true
		}
	}
	if !found {
		t.Error("a source registered with source.Register did not appear in source.Sources()")
	}
}

func TestPackageRegistryPanicsOnDuplicate(t *testing.T) {
	source.Register(stub{id: "test-duplicate", kind: source.KindBiome})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register() did not panic on a duplicate ID")
		}
		if msg, ok := r.(string); ok && !strings.Contains(msg, "test-duplicate") {
			t.Errorf("panic = %q, want the colliding ID named", msg)
		}
	}()
	source.Register(stub{id: "test-duplicate", kind: source.KindBiome})
}

func mustRegister(t *testing.T, r *source.Registry, s source.Source) {
	t.Helper()
	if err := r.Register(s); err != nil {
		t.Fatalf("Register(%s) = %v", s.ID(), err)
	}
}
