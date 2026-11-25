package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

const waitingMark = "NOT YET ATTRIBUTED"

// waitingFixture is a registered project with stored evidence no attribution
// pass has run over yet: what a report sees while a harvest is still working.
type waitingFixture struct {
	dir, db, index string
	marker         *registry.Marker
}

func newWaitingFixture(t *testing.T) waitingFixture {
	t.Helper()
	f := waitingFixture{dir: t.TempDir(), index: filepath.Join(t.TempDir(), "index.json")}
	f.marker = registerScopeDir(t, f.index, f.dir, "Larkfield", "loom")
	f.db = emptyStore(t)
	f.observe(t, f.dir, filepath.Join(f.dir, "a.motn"))
	return f
}

// observe stores one recent observation per locator. They are placed relative
// to now because the dashboard and the headline report trailing windows.
func (f waitingFixture) observe(t *testing.T, locators ...string) {
	t.Helper()
	st, err := store.Open(f.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	var events []source.Event
	for i, l := range locators {
		e := source.NewPoint(base.Add(time.Duration(i)*time.Minute+time.Duration(len(l))*time.Millisecond),
			source.Identity{Document: filepath.Base(l)}, "claude-transcripts")
		e.Locator = l
		events = append(events, e)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func (f waitingFixture) attribute(t *testing.T) {
	t.Helper()
	st, err := store.Open(f.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	registered, err := registry.List(f.index)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := resolveProjects(context.Background(), st, registered, &strings.Builder{}); err != nil {
		t.Fatalf("resolveProjects: %v", err)
	}
}

func (f waitingFixture) args(t *testing.T, more ...string) []string {
	t.Helper()
	return append([]string{"-store", f.db, "-config", noConfig(t), "-index", f.index}, more...)
}

func TestScopedMetricsNamesEvidenceWaitingOnAttribution(t *testing.T) {
	f := newWaitingFixture(t)

	out := runMetrics(t, f.args(t, "-project", "loom")...)
	for _, want := range []string{waitingMark, "2 stored observations", "timetattle harvest"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no evidence here carries that name") {
		t.Errorf("report says there is no evidence for a project whose evidence is waiting:\n%s", out)
	}

	f.attribute(t)
	if out := runMetrics(t, f.args(t, "-project", "loom")...); strings.Contains(out, waitingMark) {
		t.Errorf("still waiting once attributed:\n%s", out)
	}
}

func TestMetricsScopedToAClientNamesItsWaitingEvidence(t *testing.T) {
	f := newWaitingFixture(t)
	if out := runMetrics(t, f.args(t, "-client", "Larkfield")...); !strings.Contains(out, waitingMark) {
		t.Errorf("no waiting notice for the client:\n%s", out)
	}
	if out := runMetrics(t, f.args(t, "-client", "somebody else")...); strings.Contains(out, waitingMark) {
		t.Errorf("another client was told of this one's waiting evidence:\n%s", out)
	}
}

// An unscoped report claims nothing about any one project, and unregistered
// paths are ordinary rather than waiting.
func TestUnscopedAndUnregisteredEvidenceIsNotWaiting(t *testing.T) {
	f := newWaitingFixture(t)
	if out := runMetrics(t, f.args(t, "-all")...); strings.Contains(out, waitingMark) {
		t.Errorf("an unscoped report named a waiting state:\n%s", out)
	}
	f.attribute(t)
	f.observe(t, filepath.Join(t.TempDir(), "elsewhere.motn"))
	if out := runMetrics(t, f.args(t, "-project", "loom")...); strings.Contains(out, waitingMark) {
		t.Errorf("evidence outside every registered directory was called waiting:\n%s", out)
	}
}

func TestEvidenceUnderANestedProjectIsNotTheOuterOnesWaiting(t *testing.T) {
	f := newWaitingFixture(t)
	f.attribute(t)
	nested := filepath.Join(f.dir, "inner")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	registerScopeDir(t, f.index, nested, "Larkfield", "inner")
	f.observe(t, filepath.Join(nested, "b.motn"))

	if out := runMetrics(t, f.args(t, "-project", "loom")...); strings.Contains(out, waitingMark) {
		t.Errorf("the outer project claimed the nested one's evidence:\n%s", out)
	}
	if out := runMetrics(t, f.args(t, "-project", "inner")...); !strings.Contains(out, waitingMark) {
		t.Errorf("the nested project was not told of its waiting evidence:\n%s", out)
	}
}

func TestPartlyAttributedReportKeepsItsFiguresAndNamesTheRest(t *testing.T) {
	f := newWaitingFixture(t)
	f.attribute(t)
	f.observe(t, filepath.Join(f.dir, "new.motn"))

	out := runMetrics(t, f.args(t, "-project", "loom")...)
	if !strings.Contains(out, waitingMark) || !strings.Contains(out, "1 stored observation ") ||
		!strings.Contains(out, "ENGAGED, per project") {
		t.Errorf("want the figures and the waiting count together:\n%s", out)
	}
}

// A CSV is read by a program, so the notice cannot go into it.
func TestWaitingNoticeStaysOutOfCSV(t *testing.T) {
	f := newWaitingFixture(t)
	var out, errOut bytes.Buffer
	if err := metricsWatching(&out, &errOut, f.args(t, "-project", "loom", "-by", "day", "-csv")); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if strings.Contains(out.String(), waitingMark) || !strings.Contains(errOut.String(), waitingMark) {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
	}
}

func TestScopedDashboardNamesWaitingEvidenceInsteadOfZeros(t *testing.T) {
	f := newWaitingFixture(t)

	out := runDashboard(t, f.args(t, "-project", "loom")...)
	for _, want := range []string{waitingMark, "2 stored observations", "not yet attributed"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard does not mention %q:\n%s", want, out)
		}
	}
	for _, s := range dashboardSections {
		if strings.Contains(out, s.Title) {
			t.Errorf("dashboard drew %q over evidence it has not attributed:\n%s", s.Title, out)
		}
	}

	f.attribute(t)
	out = runDashboard(t, f.args(t, "-project", "loom")...)
	if strings.Contains(out, waitingMark) || !strings.Contains(out, "Component Stats") {
		t.Errorf("want the ordinary dashboard once attributed:\n%s", out)
	}

	f.observe(t, filepath.Join(f.dir, "new.motn"))
	out = runDashboard(t, f.args(t, "-project", "loom")...)
	if !strings.Contains(out, waitingMark) || !strings.Contains(out, "Component Stats") {
		t.Errorf("want sections and the notice once some is attributed:\n%s", out)
	}
}

func TestBareNamesWaitingEvidenceInsteadOfNoObservation(t *testing.T) {
	f := newWaitingFixture(t)
	derive(t, f.db)

	out := runBare(t, f.dir, f.db)
	if !strings.Contains(out, waitingMark) {
		t.Errorf("no waiting notice:\n%s", out)
	}
	if strings.Contains(out, "no observation of this project") {
		t.Errorf("headline says nothing was recorded for evidence that is waiting:\n%s", out)
	}

	f.attribute(t)
	if out := runBare(t, f.dir, f.db); strings.Contains(out, waitingMark) {
		t.Errorf("still waiting once attributed:\n%s", out)
	}
}

func TestHarvestSaysWhenAttributionStartedAndHowLongItTook(t *testing.T) {
	var out bytes.Buffer
	clock := []time.Time{
		time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 9, 20, 5, 0, time.UTC),
	}
	next := func() time.Time {
		now := clock[0]
		clock = clock[1:]
		return now
	}
	err := timedAttribution(&out, next, func() error {
		if !strings.Contains(out.String(), "attribution: started at 2026-07-14T09:00:00Z") {
			t.Errorf("the start was not written before the pass ran:\n%s", out.String())
		}
		return os.ErrPermission
	})
	if err != os.ErrPermission {
		t.Errorf("err = %v, want the pass's own", err)
	}
	if !strings.Contains(out.String(), "attribution: finished after 20m5s") {
		t.Errorf("no duration:\n%s", out.String())
	}
}

func TestHarvestReportsTheAttributionStep(t *testing.T) {
	dir := t.TempDir()
	src := populatedSnapshot(t)
	out, err := printed(t, func() error {
		return harvest([]string{
			"-source", src,
			"-dir", filepath.Join(dir, "snapshots"),
			"-biome", filepath.Join(dir, "no-such-biome"),
			"-motion-vault", filepath.Join(dir, "no-such-vault"),
			"-store", filepath.Join(dir, "store", "timetattle.db"),
			"-index", filepath.Join(dir, "index.json"),
		})
	})
	if err != nil {
		// A source this machine cannot read is not this test's business; the
		// attribution step runs whatever ingestion managed.
		t.Logf("harvest reported: %v", err)
	}
	started := strings.Index(out, "attribution: started at ")
	finished := strings.Index(out, "attribution: finished after ")
	if started < 0 || finished < started {
		t.Errorf("harvest does not bracket the attribution step:\n%s", out)
	}
}
