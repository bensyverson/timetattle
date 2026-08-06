package main

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRegisteredByProjectDatesEachProjectFromItsMarker(t *testing.T) {
	got, err := registeredByProject([]*registry.Marker{
		{Project: "acorn", Path: "/w/acorn", Registered: at("2026-08-01T13:29:22Z")},
		{Project: "birch", Path: "/w/birch", Registered: at("2026-08-01T15:26:52Z")},
	})
	if err != nil {
		t.Fatalf("registeredByProject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d projects, want 2", len(got))
	}
	if !got["acorn"].Equal(at("2026-08-01T13:29:22Z")) {
		t.Errorf("acorn registered %v, want 2026-08-01T13:29:22Z", got["acorn"])
	}
	if !got["birch"].Equal(at("2026-08-01T15:26:52Z")) {
		t.Errorf("birch registered %v, want 2026-08-01T15:26:52Z", got["birch"])
	}
}

// The simplification this program rests on is that a project has ONE
// registration instant. The registry supports several directories sharing a
// project name, so that is a property of the registry being measured and not of
// the design. It must be checked rather than assumed: picking one silently
// would mis-date the evidence of whichever directory lost, and the error would
// appear only as a number nobody could reproduce.
func TestRegisteredByProjectRefusesAProjectTwoDirectoriesRegisteredAtDifferentInstants(t *testing.T) {
	_, err := registeredByProject([]*registry.Marker{
		{Project: "acorn", Path: "/w/repo", Registered: at("2026-08-01T13:29:22Z")},
		{Project: "acorn", Path: "/w/assets", Registered: at("2026-08-02T09:00:00Z")},
	})
	if err == nil {
		t.Fatal("two directories registered the same project at different instants and it was accepted")
	}
}

func TestRegisteredByProjectAcceptsTwoDirectoriesRegisteredAtTheSameInstant(t *testing.T) {
	got, err := registeredByProject([]*registry.Marker{
		{Project: "acorn", Path: "/w/repo", Registered: at("2026-08-01T13:29:22Z")},
		{Project: "acorn", Path: "/w/assets", Registered: at("2026-08-01T13:29:22Z")},
	})
	if err != nil {
		t.Fatalf("registeredByProject: %v", err)
	}
	if !got["acorn"].Equal(at("2026-08-01T13:29:22Z")) {
		t.Errorf("acorn registered %v, want 2026-08-01T13:29:22Z", got["acorn"])
	}
}

func evt(project string) source.Event {
	return source.Event{Identity: source.Identity{Project: project, Client: "someone"}}
}

func TestClearAfterRemovesTheProjectOfADirectoryNotYetRegistered(t *testing.T) {
	registered := map[string]time.Time{"acorn": at("2026-08-01T13:29:22Z")}
	out, cleared := clearAfter([]source.Event{evt("acorn")}, registered, at("2026-07-15T00:00:00Z"))
	if cleared != 1 {
		t.Fatalf("cleared %d events, want 1", cleared)
	}
	if out[0].Identity.Project != "" {
		t.Errorf("project is %q, want empty: at the instant asked about nothing had registered it",
			out[0].Identity.Project)
	}
	if out[0].Identity.Client != "" {
		t.Errorf("client is %q, want empty: an event with no project can have no client",
			out[0].Identity.Client)
	}
}

func TestClearAfterKeepsTheProjectOfADirectoryAlreadyRegistered(t *testing.T) {
	registered := map[string]time.Time{"acorn": at("2026-08-01T13:29:22Z")}
	out, cleared := clearAfter([]source.Event{evt("acorn")}, registered, at("2026-08-02T00:00:00Z"))
	if cleared != 0 {
		t.Fatalf("cleared %d events, want 0", cleared)
	}
	if out[0].Identity.Project != "acorn" {
		t.Errorf("project is %q, want acorn", out[0].Identity.Project)
	}
}

// Absence of a marker is not an observation that the directory was
// unregistered. A project whose marker could not be read, or whose directory
// has since been unregistered, is UNRECORDED, and clearing it would state as
// observed-unregistered something nothing observed. This is the collapse this
// project has shipped three times.
func TestClearAfterLeavesAProjectNoMarkerSaysAnythingAboutAlone(t *testing.T) {
	registered := map[string]time.Time{"acorn": at("2026-08-01T13:29:22Z")}
	out, cleared := clearAfter([]source.Event{evt("cypress")}, registered, at("2026-07-15T00:00:00Z"))
	if cleared != 0 {
		t.Fatalf("cleared %d events, want 0: nothing is known about when cypress was registered", cleared)
	}
	if out[0].Identity.Project != "cypress" {
		t.Errorf("project is %q, want cypress left alone", out[0].Identity.Project)
	}
}

func TestClearAfterDoesNotMutateTheEventsItWasGiven(t *testing.T) {
	registered := map[string]time.Time{"acorn": at("2026-08-01T13:29:22Z")}
	in := []source.Event{evt("acorn")}
	clearAfter(in, registered, at("2026-07-15T00:00:00Z"))
	if in[0].Identity.Project != "acorn" {
		t.Error("the caller's slice was mutated, so a second as-of would run over the first one's answer")
	}
}

// With no -asof the worst case is the instant just before the EARLIEST
// registration. Anything later hides part of the movement, which is exactly how
// scripts/sleepcheck's clipped window once concealed a 6h23m gap on this
// project.
func TestInstantsToTravelDefaultsToJustBeforeTheEarliestRegistration(t *testing.T) {
	registered := map[string]time.Time{
		"acorn": at("2026-08-01T15:26:52Z"),
		"birch": at("2026-08-01T13:29:22Z"),
	}
	got, err := instantsToTravel("", registered)
	if err != nil {
		t.Fatalf("instantsToTravel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instants, want 1", len(got))
	}
	if want := at("2026-08-01T13:29:21Z"); !got[0].Equal(want) {
		t.Errorf("default instant %v, want %v", got[0], want)
	}
}

func TestInstantsToTravelSortsWhatItWasGiven(t *testing.T) {
	got, err := instantsToTravel("2026-08-02T00:00:00Z,2026-07-01T00:00:00Z", nil)
	if err != nil {
		t.Fatalf("instantsToTravel: %v", err)
	}
	if len(got) != 2 || !got[0].Equal(at("2026-07-01T00:00:00Z")) {
		t.Fatalf("got %v, want the earlier instant first", got)
	}
}
