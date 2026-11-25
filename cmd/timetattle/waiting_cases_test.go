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
	"github.com/bensyverson/timetattle/internal/store"
)

// A client is a fact about an instant: work observed before a dated
// reassignment is the previous client's, waiting or not.
func TestWaitingNoticeNamesTheClientAtEachObservationsTime(t *testing.T) {
	dir := t.TempDir()
	f := waitingFixture{dir: dir, index: filepath.Join(t.TempDir(), "index.json"), db: emptyStore(t)}
	base := []string{"-project", "loom", "-path", dir, "-store", f.db, "-index", f.index}
	if _, err := registerOutput(t, append([]string{"-client", "Larkfield"}, base...)...); err != nil {
		t.Fatalf("register: %v", err)
	}
	since := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	if _, err := registerOutput(t,
		append([]string{"-client", "Pellworth", "-force", "-since", since}, base...)...); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	f.observe(t, filepath.Join(dir, "before.motn"))

	if out := runMetrics(t, f.args(t, "-client", "Larkfield")...); !strings.Contains(out, waitingMark) {
		t.Errorf("the client of the time was not told of its waiting evidence:\n%s", out)
	}
	if out := runMetrics(t, f.args(t, "-client", "Pellworth")...); strings.Contains(out, waitingMark) {
		t.Errorf("today's client was told of work done before it:\n%s", out)
	}
}

func TestDashboardShowsHoursThatComeOnlyFromACorrection(t *testing.T) {
	f := newWaitingFixture(t)
	from := time.Now().Add(-48 * time.Hour).Add(-2 * time.Minute)
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", f.db,
		"-from", from.Format(time.RFC3339), "-to", from.Add(10 * time.Minute).Format(time.RFC3339),
		"-assert", "attributed", "-project", "loom", "-origin", "a-person"}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	out := runDashboard(t, f.args(t, "-project", "loom")...)
	if strings.Contains(out, "not yet attributed,") {
		t.Errorf("header hides corrected hours:\n%s", out)
	}
	for _, want := range []string{"h attributed", "Component Stats", waitingMark} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard does not mention %q:\n%s", want, out)
		}
	}
}

// A locator no pass can read stays waiting for good, so the notice may not
// promise that a harvest attributes it.
func TestWaitingNoticeDoesNotPromiseToAttributeWhatCannotBeRead(t *testing.T) {
	f := newWaitingFixture(t)
	f.attribute(t)
	file := filepath.Join(f.dir, "export")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	clip := filepath.Join(file, "clip.mov")
	f.observe(t, clip)

	st, err := store.Open(f.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	registered, err := registry.List(f.index)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var pass bytes.Buffer
	if err := resolveProjects(context.Background(), st, registered, &pass); err == nil ||
		!strings.Contains(pass.String(), "unreadable: "+clip) {
		t.Fatalf("want the pass to say why it could not: %v\n%s", err, pass.String())
	}

	out := runMetrics(t, f.args(t, "-project", "loom")...)
	if !strings.Contains(out, waitingMark) || !strings.Contains(out, "or reports why it cannot") {
		t.Errorf("notice does not allow for evidence harvest cannot read:\n%s", out)
	}
}

func TestReportsSayWhenARegisteredDirectoryCannotBeRead(t *testing.T) {
	f := newWaitingFixture(t)
	f.attribute(t)
	nested := filepath.Join(f.dir, "inner")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	registerScopeDir(t, f.index, nested, "Larkfield", "inner")
	marker := filepath.Join(nested, registry.MarkerFilename)
	if err := os.WriteFile(marker, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("break the marker: %v", err)
	}

	// With nothing waiting, silence would read as a check that found nothing.
	for _, out := range []string{
		runMetrics(t, f.args(t, "-project", "loom")...),
		runDashboard(t, f.args(t, "-project", "loom")...),
	} {
		if !strings.Contains(out, "could not be read") || !strings.Contains(out, marker) {
			t.Errorf("report does not say which registration it could not check:\n%s", out)
		}
	}
}

func TestBareIgnoresFormerPathsOfAMarkerTheIndexDoesNotHold(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, former := t.TempDir(), filepath.Join(t.TempDir(), "old-name")
	declareFormerPath(t, dir, former)
	f := waitingFixture{dir: dir, db: emptyStore(t)}
	f.observe(t, filepath.Join(former, "a.motn"))
	derive(t, f.db)

	// Harvest cannot see the declaration, so nothing is on its way.
	if out := runBare(t, dir, f.db); strings.Contains(out, waitingMark) {
		t.Errorf("promised evidence no harvest will reach:\n%s", out)
	}
}
