package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// registerScopeDir blesses dir with client/project and, when idx is not
// empty, records it in the registry index too — the same two steps
// `timetattle register` takes, done directly so these tests do not need a
// running command.
func registerScopeDir(t *testing.T, idx, dir, client, project string) *registry.Marker {
	t.Helper()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: client, Project: project})
	if err != nil {
		t.Fatalf("register %s: %v", dir, err)
	}
	if idx != "" {
		if err := registry.RecordInIndex(idx, dir); err != nil {
			t.Fatalf("record %s in the index: %v", dir, err)
		}
	}
	return m
}

// emptyIndex is a path to an index file that does not exist, matching
// registry.List's own "not configured yet" tolerance for a missing file —
// the same fixture orient_test.go and register_test.go already use so that
// no test here can read the real machine's ~/.timetattle.
func emptyIndex(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "registered-directories.json")
}

// CRITERION 7Xg. Resolving from inside a registered project's own directory
// scopes to that project, and names no client — showsClient narrows to
// whichever client the report's OWN roll-up credits the project to, given
// only a project; see resolveWorkingScope's doc for why the marker's Client
// field is not asked here.
func TestResolveWorkingScopeInsideARegisteredProjectScopesToItsProject(t *testing.T) {
	dir := t.TempDir()
	registerScopeDir(t, "", dir, "Sondermark", "Holiday Campaign")

	got, err := resolveWorkingScope(dir, emptyIndex(t))
	if err != nil {
		t.Fatalf("resolveWorkingScope(%s) = %v", dir, err)
	}
	if got.Project != "Holiday Campaign" || got.Client != "" {
		t.Errorf("resolveWorkingScope(%s) = %+v, want Project=%q Client=\"\"", dir, got, "Holiday Campaign")
	}
}

// CRITERION 7Xg, the subdirectory half. registry.Resolve climbs, so a
// directory beneath the marker resolves to the same project.
func TestResolveWorkingScopeInsideASubdirectoryOfAProjectScopesToItsProject(t *testing.T) {
	dir := t.TempDir()
	registerScopeDir(t, "", dir, "Sondermark", "Holiday Campaign")
	sub := filepath.Join(dir, "assets", "banners")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}

	got, err := resolveWorkingScope(sub, emptyIndex(t))
	if err != nil {
		t.Fatalf("resolveWorkingScope(%s) = %v", sub, err)
	}
	if got.Project != "Holiday Campaign" {
		t.Errorf("resolveWorkingScope(%s) = %+v, want Project=%q", sub, got, "Holiday Campaign")
	}
}

// CRITERION pcs. A directory that is itself unregistered, but sits above two
// registered projects that agree on their client, scopes to that client.
func TestResolveWorkingScopeAboveSiblingProjectsSharingAClientScopesToTheClient(t *testing.T) {
	parent := t.TempDir()
	idx := emptyIndex(t)
	frontend := filepath.Join(parent, "frontend")
	backend := filepath.Join(parent, "backend")
	for _, dir := range []string{frontend, backend} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	registerScopeDir(t, idx, frontend, "Larkfield", "Frontend")
	registerScopeDir(t, idx, backend, "Larkfield", "Backend")

	got, err := resolveWorkingScope(parent, idx)
	if err != nil {
		t.Fatalf("resolveWorkingScope(%s) = %v", parent, err)
	}
	if got.Client != "Larkfield" || got.Project != "" {
		t.Errorf("resolveWorkingScope(%s) = %+v, want Client=%q Project=\"\"", parent, got, "Larkfield")
	}
}

// CRITERION pcs / xq0 boundary. Two clients beneath one directory is not a
// client directory for either of them, and must refuse rather than guess —
// picking whichever sorts first would be a silent wrong attribution.
func TestResolveWorkingScopeAboveTwoDifferentClientsIsRefused(t *testing.T) {
	parent := t.TempDir()
	idx := emptyIndex(t)
	acme := filepath.Join(parent, "acme")
	otherco := filepath.Join(parent, "otherco")
	for _, dir := range []string{acme, otherco} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	registerScopeDir(t, idx, acme, "Acme", "Website")
	registerScopeDir(t, idx, otherco, "OtherCo", "Library")

	_, err := resolveWorkingScope(parent, idx)
	if err == nil {
		t.Fatalf("resolveWorkingScope(%s) = nil error, want a refusal: two clients sit beneath it", parent)
	}
}

// CRITERION xq0. A directory with no registration above or around it errors,
// and the error names both ways out: register, or -all.
func TestResolveWorkingScopeInAnUnregisteredUnrelatedDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	err := func() error {
		_, err := resolveWorkingScope(dir, emptyIndex(t))
		return err
	}()
	if err == nil {
		t.Fatalf("resolveWorkingScope(%s) = nil error, want a refusal", dir)
	}
	msg := err.Error()
	if !strings.Contains(msg, "register") {
		t.Errorf("error %q does not name `register` as a fix", msg)
	}
	if !strings.Contains(msg, "-all") {
		t.Errorf("error %q does not name -all as a fix", msg)
	}
}

// The nesting case the brief asked to have verified: registry.Register does
// NOT refuse a directory nested inside an already-registered ancestor that
// belongs to someone else entirely — the only cross-directory check it makes
// is same-project-name-different-client, which nesting under a DIFFERENT
// project name never triggers. So "a directory maps to one project but is
// also an ancestor of another client's project" is possible, not impossible,
// and resolveWorkingScope must still answer correctly for it: registry.Resolve
// climbs nearest-marker-first, so the nearer, inner marker always wins over
// the outer one, in both directions.
func TestNestingADifferentClientsProjectInsideAnotherIsNotRefused(t *testing.T) {
	outer := t.TempDir()
	registerScopeDir(t, "", outer, "Acme", "Website")

	inner := filepath.Join(outer, "vendor", "library")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", inner, err)
	}
	if _, err := registry.Register(registry.Registration{Dir: inner, Client: "OtherCo", Project: "Library"}); err != nil {
		t.Fatalf("nesting a different client's project inside an already-registered directory was refused: %v", err)
	}

	got, err := resolveWorkingScope(inner, emptyIndex(t))
	if err != nil {
		t.Fatalf("resolveWorkingScope(%s) = %v", inner, err)
	}
	if got.Project != "Library" {
		t.Errorf("resolveWorkingScope(%s) = %+v, want the NEARER marker, Project=%q", inner, got, "Library")
	}

	elsewhereInOuter := filepath.Join(outer, "src")
	if err := os.MkdirAll(elsewhereInOuter, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", elsewhereInOuter, err)
	}
	got, err = resolveWorkingScope(elsewhereInOuter, emptyIndex(t))
	if err != nil {
		t.Fatalf("resolveWorkingScope(%s) = %v", elsewhereInOuter, err)
	}
	if got.Project != "Website" {
		t.Errorf("resolveWorkingScope(%s) = %+v, want the outer project, Project=%q", elsewhereInOuter, got, "Website")
	}
}

// clientBeneath is exercised directly too, isolated from Resolve, so a
// regression in the downward walk cannot hide behind a passing
// resolveWorkingScope test that happened to take the upward branch instead.
func TestClientBeneathIgnoresRegisteredDirectoriesNotBeneathDir(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	mElsewhere, err := registry.Register(registry.Registration{Dir: elsewhere, Client: "Elsewhere", Project: "Unrelated"})
	if err != nil {
		t.Fatalf("register elsewhere: %v", err)
	}

	if _, ok := clientBeneath(dir, []*registry.Marker{mElsewhere}); ok {
		t.Errorf("clientBeneath(%s) found a client from a directory that is not beneath it", dir)
	}
}

// clientBeneath must not treat dir itself as being "beneath" dir — that is
// registry.Resolve's job (via climb), and resolveWorkingScope only reaches
// clientBeneath after Resolve has already answered no for dir and everything
// above it.
func TestClientBeneathExcludesDirItself(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "Acme", Project: "Website"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, ok := clientBeneath(dir, []*registry.Marker{m}); ok {
		t.Errorf("clientBeneath(%s) treated dir's own marker as beneath it", dir)
	}
}
