package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// fakePass hands each locator to the resolver, as store.ResolveProjects does.
type fakePass []string

func (f fakePass) ResolveProjects(_ context.Context, resolve store.Resolver) (store.Attribution, error) {
	att := store.Attribution{Locators: len(f)}
	for _, l := range f {
		_, found, err := resolve(l)
		switch {
		case err != nil:
			att.Skipped++
			att.Skips = append(att.Skips, store.Skip{Locator: l, Events: 1, Err: err})
		case found:
			att.Attributed++
		default:
			att.Unattributed++
		}
	}
	return att, nil
}

func TestReportCountsLookupsAndNamesNoPathOrProject(t *testing.T) {
	root := t.TempDir()
	// The relative locator below resolves against the working directory, which
	// must not be this repository, itself a registered one on some machines.
	t.Chdir(root)
	secret := filepath.Join(root, "sentinelclient")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: secret, Client: "sentinelclientname", Project: "sentinelproject",
	}); err != nil {
		t.Fatal(err)
	}
	// A symlink cycle, so one locator is skipped and its error names a path.
	a, b := filepath.Join(root, "sentinela"), filepath.Join(root, "sentinelb")
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	pass := fakePass{
		filepath.Join(secret, "one.txt"),
		filepath.Join(secret, "two.txt"),
		filepath.Join(root, "elsewhere", "sentinelfile"),
		filepath.Join(a, "sentinelfile"),
		"sentinel-relative",
	}

	rep, err := profile(context.Background(), pass, nil, registry.CachePerResolver)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	var out strings.Builder
	rep.write(&out)
	text := out.String()

	if strings.Contains(text, "sentinel") || strings.Contains(text, root) {
		t.Errorf("report names a path or a project:\n%s", text)
	}
	if rep.att.Locators != 5 || rep.att.Attributed != 2 || rep.att.Skipped != 1 {
		t.Errorf("attribution = %+v, want 5 locators, 2 attributed, 1 skipped", rep.att)
	}
	if rep.relative != 1 {
		t.Errorf("relative = %d, want 1", rep.relative)
	}
	// One per locator, and one for each of the two unmatched directories, which
	// the resolver checks before it would run git there.
	if got := rep.lookups[registry.LookupStat].calls; got != 7 {
		t.Errorf("stat calls = %d, want 7", got)
	}
	if rep.lookups[registry.LookupMarker].calls == 0 {
		t.Error("no marker reads were counted")
	}
	for _, want := range []string{"list locators", "database updates", "commit", "stat", "marker read", "git rev-parse", "total"} {
		if !strings.Contains(text, want) {
			t.Errorf("report has no %q line:\n%s", want, text)
		}
	}
}

func TestProfileDoesNotRepeatAStoreErrorThatCanNameALocator(t *testing.T) {
	_, err := profile(context.Background(), failingPass{}, nil, registry.CachePerResolver)
	if err == nil {
		t.Fatal("profile = nil error, want the failure surfaced")
	}
	if strings.Contains(err.Error(), "sentinel") {
		t.Errorf("error repeats a locator: %v", err)
	}
}

type failingPass struct{}

func (failingPass) ResolveProjects(context.Context, store.Resolver) (store.Attribution, error) {
	return store.Attribution{}, errors.New("attribute /sentinel/path: disk full")
}

func pairsDB(t *testing.T, name string, rows [][2]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE event (locator TEXT, project TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO event VALUES (?, ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCompareFindsEveryKindOfDifference(t *testing.T) {
	before := pairsDB(t, "a.db", [][2]any{
		{"/same", "p"}, {"/same", "p"}, {"/none", nil}, {"/moved", "p"}, {"/lost", "p"}, {nil, "ignored"},
	})
	after := pairsDB(t, "b.db", [][2]any{
		{"/same", "p"}, {"/none", nil}, {"/moved", "q"}, {"/lost", nil},
	})

	got, err := comparePairs(before, after)
	if err != nil {
		t.Fatalf("comparePairs: %v", err)
	}
	want := comparison{pairsA: 4, pairsB: 4, onlyA: 2, onlyB: 2, attributedA: 3, attributedB: 2}
	if got != want {
		t.Errorf("comparePairs = %+v, want %+v", got, want)
	}

	same, err := comparePairs(before, before)
	if err != nil {
		t.Fatalf("comparePairs with itself: %v", err)
	}
	if same.onlyA != 0 || same.onlyB != 0 {
		t.Errorf("a store differs from itself: %+v", same)
	}
}

func TestCompareRefusesAMissingStore(t *testing.T) {
	present := pairsDB(t, "a.db", nil)
	if _, err := comparePairs(present, filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Error("comparePairs of a missing store = nil error; an absent store must not compare as empty")
	}
}

func TestNoCacheAsksMoreOftenThanOnePassCache(t *testing.T) {
	root := t.TempDir()
	pass := fakePass{filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")}
	reads := func(c registry.Caching) int {
		t.Helper()
		rep, err := profile(context.Background(), pass, nil, c)
		if err != nil {
			t.Fatalf("profile: %v", err)
		}
		return rep.lookups[registry.LookupMarker].calls
	}
	if cached, uncached := reads(registry.CachePerResolver), reads(registry.NoCache); cached >= uncached {
		t.Errorf("marker reads: %d cached, %d uncached, want fewer cached", cached, uncached)
	}
}
