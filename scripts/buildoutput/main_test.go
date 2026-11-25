package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/source"
)

// TestTallyCountsEachDirectoryApartAndNoLocator: the old rule's figures must
// stay readable beside the new names', and nothing printed may be a locator.
func TestTallyCountsEachDirectoryApartAndNoLocator(t *testing.T) {
	at := func(locator string) source.Event {
		e := source.NewPoint(time.Unix(0, 0), source.Identity{}, generated.Source)
		e.Locator = locator
		return e
	}
	var out bytes.Buffer
	tally(&out, []source.Event{
		at("/Secret/app/main.py"),
		at("/Secret/app/.venv/a.py"),
		at("/Secret/app/.venv/a.py"),
		at("/Secret/app/.venv/lib/__pycache__/a.pyc"),
		at("/Secret/app/__pycache__/b.pyc"),
		// Under two names, and .build's rule came first.
		at("/Secret/app/.venv/x/.build/c.o"),
	})
	got := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(got, "Secret") {
		t.Errorf("a locator reached the output:\n%s", out.String())
	}
	for _, want := range []string{
		"all stored 6 5",
		"under .build 1 1",
		"under .derived 0 0",
		"under .venv 3 2",
		"under __pycache__ 1 1",
		"counted 1 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

const header = "level,name,seconds,observed_seconds,allocated_seconds\n"

// TestComparePrintsTotalsAndNoName: the output is pasted into a public
// document, so a client or project name must never reach it.
func TestComparePrintsTotalsAndNoName(t *testing.T) {
	before := header +
		"presence,,36000,18000,\n" +
		"unattributed,unregistered,3600,,\n" +
		"client,SecretClient,7200,,7200\n" +
		"project,SecretKept,7200,,3600\n" +
		"project,SecretGone,3600,,3600\n"
	after := header +
		"presence,,32400,18000,\n" +
		"unattributed,unregistered,3600,,\n" +
		"client,SecretClient,3600,,3600\n" +
		"project,SecretKept,3600,,3600\n"

	var out bytes.Buffer
	if err := compare(&out, strings.NewReader(before), strings.NewReader(after)); err != nil {
		t.Fatalf("compare: %v", err)
	}
	got := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(got, "Secret") {
		t.Errorf("a name reached the output:\n%s", out.String())
	}
	for _, want := range []string{
		"presence 10.0 9.0 -1.0",
		"unattributed: unregistered 1.0 1.0 +0.0",
		"engaged, summed over projects 3.0 1.0 -2.0",
		"allocated, summed over projects 2.0 1.0 -1.0",
		"projects with a row: 2 before, 1 after",
		"changed: 2, of which 1 no longer appear",
		"largest fall in one project's engaged hours: 1.0",
		"largest rise in one project's engaged hours: 0.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
