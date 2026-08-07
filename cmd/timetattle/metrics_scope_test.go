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

// seedScopeFixture builds a store spanning two independent clients:
// Sondermark owns one project in its own directory, and Larkfield owns two
// projects (Frontend, Backend) in SIBLING directories beneath one shared
// parent that is ITSELF left unregistered — the shape resolveWorkingScope's
// downward search needs to find a client directory at all.
//
// Only frontendDir and backendDir are recorded in the returned index.
// sondermarkDir is deliberately left out of it: project-scoping goes through
// registry.Resolve's upward climb, which reads markers directly and needs no
// index, so a test asserting on it proves the climb rather than the index.
func seedScopeFixture(t *testing.T) (storePath, indexPath, sondermarkDir, larkfieldParent, frontendDir, backendDir string) {
	t.Helper()
	storePath = filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	sondermarkDir = t.TempDir()
	larkfieldParent = t.TempDir()
	frontendDir = filepath.Join(larkfieldParent, "frontend")
	backendDir = filepath.Join(larkfieldParent, "backend")
	for _, dir := range []string{frontendDir, backendDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	ctx := context.Background()
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	owners := []struct{ dir, client, project string }{
		{sondermarkDir, "Sondermark", "Site"},
		{frontendDir, "Larkfield", "Frontend"},
		{backendDir, "Larkfield", "Backend"},
	}
	var events []source.Event
	var registered []*registry.Marker
	for i, o := range owners {
		m, err := registry.Register(registry.Registration{
			Dir: o.dir, Client: o.client, Project: o.project, Among: registered,
		})
		if err != nil {
			t.Fatalf("register %s/%s: %v", o.client, o.project, err)
		}
		registered = append(registered, m)
		e := source.NewPoint(base.Add(time.Duration(i)*3*time.Minute),
			source.Identity{Document: "notes.md"}, "claude-transcripts")
		e.Locator = o.dir
		events = append(events, e)
	}

	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	if err := resolveProjects(ctx, st, registered, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}

	indexPath = filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(indexPath, frontendDir); err != nil {
		t.Fatalf("record frontend in the index: %v", err)
	}
	if err := registry.RecordInIndex(indexPath, backendDir); err != nil {
		t.Fatalf("record backend in the index: %v", err)
	}
	return storePath, indexPath, sondermarkDir, larkfieldParent, frontendDir, backendDir
}

// CRITERION 7Xg. Run inside a registered project, metrics reports only that
// project — and nothing narrowed it explicitly, so this is the DEFAULT.
func TestMetricsInAProjectDirectoryReportsOnlyThatProject(t *testing.T) {
	storePath, indexPath, sondermarkDir, _, _, _ := seedScopeFixture(t)
	t.Chdir(sondermarkDir)

	out := runMetrics(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath)

	named := rowNames(out)
	if !named["Site"] {
		t.Errorf("running inside Sondermark's own directory dropped its row:\n%s", out)
	}
	for _, unwanted := range []string{"Frontend", "Backend"} {
		if named[unwanted] {
			t.Errorf("running inside Sondermark's directory still shows %q, from a "+
				"different client entirely:\n%s", unwanted, out)
		}
	}
}

// CRITERION 7Xg, the subdirectory half: registry.Resolve climbs, so a
// directory beneath the project's own resolves the same way.
func TestMetricsInASubdirectoryOfAProjectReportsOnlyThatProject(t *testing.T) {
	storePath, indexPath, sondermarkDir, _, _, _ := seedScopeFixture(t)
	sub := filepath.Join(sondermarkDir, "assets", "banners")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	t.Chdir(sub)

	out := runMetrics(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath)

	if !rowNames(out)["Site"] {
		t.Errorf("running inside a subdirectory of Sondermark's project dropped its row:\n%s", out)
	}
}

// CRITERION pcs. Run inside a client directory — itself unregistered, with
// registered projects beneath it sharing one client — metrics reports that
// client's projects, and only them.
func TestMetricsInAClientDirectoryReportsThatClientsProjects(t *testing.T) {
	storePath, indexPath, _, larkfieldParent, _, _ := seedScopeFixture(t)
	t.Chdir(larkfieldParent)

	out := runMetrics(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath)

	named := rowNames(out)
	for _, want := range []string{"Frontend", "Backend"} {
		if !named[want] {
			t.Errorf("running inside the Larkfield parent dropped %q:\n%s", want, out)
		}
	}
	if named["Site"] {
		t.Errorf("running inside the Larkfield parent still shows Sondermark's project:\n%s", out)
	}
}

// CRITERION xq0. Run in a directory with no registration above or around it:
// metrics errors, naming both ways out, rather than silently reporting
// everything.
func TestMetricsInAnUnregisteredDirectoryErrors(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := metricsTo(&out, []string{"-store", storePath, "-config", noConfig(t), "-index", indexPath})
	if err == nil {
		t.Fatalf("metrics in an unregistered directory returned no error, and printed:\n%s", out.String())
	}
	msg := err.Error()
	if !strings.Contains(msg, "register") {
		t.Errorf("the error %q does not name `register` as a fix", msg)
	}
	if !strings.Contains(msg, "-all") {
		t.Errorf("the error %q does not name -all as a fix", msg)
	}
	if out.Len() != 0 {
		t.Errorf("metrics wrote a report before failing: %q", out.String())
	}
}

// A directory above MORE THAN ONE CLIENT'S projects is refused the same way
// an unregistered directory is — see resolveWorkingScope's clientBeneath:
// picking whichever client sorts first would be a silent wrong attribution,
// so this is not a client directory for either of them.
func TestMetricsAboveTwoDifferentClientsErrors(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	// A directory that is an ancestor of BOTH clients' work, built explicitly
	// rather than relied on from seedScopeFixture's own directories: those are
	// independent t.TempDir() calls, so nothing guarantees one's parent is an
	// ancestor of the other.
	commonParent := t.TempDir()
	acme := filepath.Join(commonParent, "acme")
	otherco := filepath.Join(commonParent, "otherco")
	for _, dir := range []string{acme, otherco} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if _, err := registry.Register(registry.Registration{Dir: acme, Client: "Acme", Project: "Website"}); err != nil {
		t.Fatalf("register acme: %v", err)
	}
	if _, err := registry.Register(registry.Registration{Dir: otherco, Client: "OtherCo", Project: "Library"}); err != nil {
		t.Fatalf("register otherco: %v", err)
	}
	if err := registry.RecordInIndex(indexPath, acme); err != nil {
		t.Fatalf("record acme in the index: %v", err)
	}
	if err := registry.RecordInIndex(indexPath, otherco); err != nil {
		t.Fatalf("record otherco in the index: %v", err)
	}
	t.Chdir(commonParent)

	var out bytes.Buffer
	err := metricsTo(&out, []string{"-store", storePath, "-config", noConfig(t), "-index", indexPath})
	if err == nil {
		t.Fatalf("metrics above two different clients' directories returned no error, and printed:\n%s",
			out.String())
	}
}

// Explicit -client or -project still bypasses directory scoping entirely,
// even from a directory that has no registration at all — a reader who
// already said what they want is not asked to also stand somewhere specific.
func TestMetricsExplicitClientBypassesDirectoryScopingWhenUnregistered(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	t.Chdir(t.TempDir())

	out := runMetrics(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath,
		"-client", "Larkfield")

	named := rowNames(out)
	if !named["Frontend"] || !named["Backend"] {
		t.Errorf("-client from an unregistered directory did not report Larkfield's projects:\n%s", out)
	}
}

// CRITERION rxQ. -all reports every client and project regardless of the
// working directory — including from a directory that would otherwise
// refuse outright — and its output does not depend on which directory it was
// run from at all.
func TestMetricsAllIgnoresTheWorkingDirectory(t *testing.T) {
	storePath, indexPath, sondermarkDir, larkfieldParent, _, _ := seedScopeFixture(t)
	unrelated := t.TempDir()

	var outputs []string
	for _, dir := range []string{sondermarkDir, larkfieldParent, unrelated} {
		t.Chdir(dir)
		outputs = append(outputs, runMetrics(t, "-store", storePath, "-config", noConfig(t),
			"-index", indexPath, "-all"))
	}
	for i := 1; i < len(outputs); i++ {
		if outputs[i] != outputs[0] {
			t.Errorf("-all's report differed between directories (run %d against run 0):\n%s\n---\n%s",
				i, outputs[i], outputs[0])
		}
	}

	named := rowNames(outputs[0])
	for _, want := range []string{"Site", "Frontend", "Backend"} {
		if !named[want] {
			t.Errorf("-all dropped %q, which is not scoping — it is supposed to see everything:\n%s",
				want, outputs[0])
		}
	}
}
