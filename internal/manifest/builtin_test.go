package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
)

// find returns the source with the given id, or fails.
func find(t *testing.T, sources []manifest.Source, id string) manifest.Source {
	t.Helper()
	for _, s := range sources {
		if s.ID == id {
			return s
		}
	}
	var have []string
	for _, s := range sources {
		have = append(have, s.ID)
	}
	t.Fatalf("no source %q; got %v", id, have)
	return manifest.Source{}
}

// writeIn puts a manifest in an existing directory, which the layering tests
// need because they care about several files sharing one directory.
func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestBuiltinShipsMotionAndBlender(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	if len(sources) != 2 {
		var have []string
		for _, s := range sources {
			have = append(have, s.ID)
		}
		t.Fatalf("Builtin() returned %d sources %v, want 2", len(sources), have)
	}
	find(t, sources, "motion-autosave")
	find(t, sources, "blender-backups")
}

func TestBuiltinMotionDescribesTheAutosaveVault(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	s := find(t, sources, "motion-autosave")

	if s.Type != source.KindTimestampedFiles {
		t.Errorf("Type = %q, want %q", s.Type, source.KindTimestampedFiles)
	}
	if s.Emits != source.Point {
		t.Errorf("Emits = %q, want %q", s.Emits, source.Point)
	}
	if s.App != "com.apple.motionapp" {
		t.Errorf("App = %q, want com.apple.motionapp; it is the key the Biome join uses", s.App)
	}
	// The vault is pruned by Motion, so this evidence has to be captured
	// rather than merely read.
	if !s.Perishable {
		t.Error("Perishable = false, want true; Motion prunes the vault")
	}
	if s.Discovery != manifest.DiscoveryGlob {
		t.Errorf("Discovery = %q, want %q", s.Discovery, manifest.DiscoveryGlob)
	}
	if want := filepath.Join("Movies", "Motion Projects", "Autosave Vault"); !strings.HasSuffix(s.Files.Root, want) {
		t.Errorf("Files.Root = %q, want it to end with %q", s.Files.Root, want)
	}
	// Load expands the tilde. A root that still starts with one would be
	// refused by the reader rather than searched.
	if strings.HasPrefix(s.Files.Root, "~") {
		t.Errorf("Files.Root = %q is unexpanded", s.Files.Root)
	}
	if s.Timestamp.From != manifest.FromFilename {
		t.Errorf("Timestamp.From = %q, want %q", s.Timestamp.From, manifest.FromFilename)
	}
	if s.Timestamp.Compiled == nil {
		t.Fatal("Timestamp.Compiled is nil; the built-in was not put through Load")
	}
	if s.Project == nil || s.Project.Compiled == nil {
		t.Fatal("Project is nil; Motion names the composition by its directory")
	}
	if s.Cause != source.CauseTimer {
		t.Errorf("Cause = %q, want %q; Motion autosaves on a wall-clock interval and does not "+
			"wait for anyone to be there", s.Cause, source.CauseTimer)
	}
}

// The two shipped manifests are the same Type, the same Emits and the same
// reader, and they disagree here — which is the whole reason the field exists.
// Measured 2026-07-29 (project/private/2026-07-29-join-validation.md §4): Blender's
// backups landed inside their own app's focus intervals 4 of 4, Motion's
// autosaves 9 of 15, and the one miss that was not clock resolution was written
// at 17:53 with the screen off during a confirmed break.
func TestTheTwoBuiltinsDisagreeAboutWhatCausedTheirArtifacts(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	motion := find(t, sources, "motion-autosave")
	blender := find(t, sources, "blender-backups")

	if motion.Type != blender.Type || motion.Emits != blender.Emits {
		t.Fatalf("the two built-ins differ in type or emits (%q/%q against %q/%q); "+
			"this test is about two sources that agree on everything else",
			motion.Type, motion.Emits, blender.Type, blender.Emits)
	}
	if motion.Cause == blender.Cause {
		t.Errorf("both built-ins report cause %q; an explicit save and an autosave timer "+
			"are not the same evidence", motion.Cause)
	}
}

// TestBuiltinMotionKeepsWholeProjectNames is the regression test for the
// standing trap in this project: a Motion project is a directory name, and
// several real ones contain spaces and dashes that are part of the name.
// Deriving the project by splitting on a dash would turn "dark-crystal-apex"
// into "dark", and the mistake is invisible in a report.
func TestBuiltinMotionKeepsWholeProjectNames(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	s := find(t, sources, "motion-autosave")

	// Every one of these exists in the real vault.
	cases := []struct {
		file    string
		project string
		stamp   string
	}{
		{"dark-crystal/dark-crystal_2026-07-10_1426.motn", "dark-crystal", "2026-07-10_1426"},
		{"dark-crystal-apex/dark-crystal-apex_2026-07-10_1804.motn", "dark-crystal-apex", "2026-07-10_1804"},
		{"scanline-card-denied/scanline-card-denied_2026-07-12_0123.motn", "scanline-card-denied", "2026-07-12_0123"},
		{"scanline2-card-granted/scanline2-card-granted_2026-07-28_0632.motn", "scanline2-card-granted", "2026-07-28_0632"},
		{"Infinite Cake Title/Infinite Cake Title_2024-11-01_1242.motn", "Infinite Cake Title", "2024-11-01_1242"},
		{"Thirdy Cap 01 - Basic/Thirdy Cap 01 - Basic_2026-02-10_1626.motn", "Thirdy Cap 01 - Basic", "2026-02-10_1626"},
	}
	for _, c := range cases {
		path := filepath.Join(s.Files.Root, c.file)

		got := s.Project.Compiled.FindStringSubmatch(path)
		if got == nil {
			t.Errorf("project pattern did not match %s", path)
		} else if got[1] != c.project {
			t.Errorf("project of %s = %q, want %q", c.file, got[1], c.project)
		}

		stamp := s.Timestamp.Compiled.FindStringSubmatch(filepath.Base(path))
		if stamp == nil {
			t.Errorf("timestamp pattern did not match %s", filepath.Base(path))
		} else if stamp[1] != c.stamp {
			t.Errorf("timestamp of %s = %q, want %q", c.file, stamp[1], c.stamp)
		}
	}
}

// TestBuiltinMotionIgnoresTheVaultsDSStore guards against the manifest
// widening to catch the .DS_Store that sits beside the project directories.
func TestBuiltinMotionIgnoresTheVaultsDSStore(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	s := find(t, sources, "motion-autosave")

	dir := t.TempDir()
	writeIn(t, filepath.Join(dir, "dark-crystal"), "dark-crystal_2026-07-10_1426.motn", "x")
	writeIn(t, dir, ".DS_Store", "x")

	matches, err := filepath.Glob(filepath.Join(dir, s.Files.Match))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matched %v, want only the .motn file", matches)
	}
}

func TestBuiltinBlenderSearchesBySpotlightAndReadsMtime(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	s := find(t, sources, "blender-backups")

	if s.App != "org.blenderfoundation.blender" {
		t.Errorf("App = %q, want org.blenderfoundation.blender", s.App)
	}
	// .blend1 files sit wherever the .blend does, and the schema refuses "**"
	// precisely so a scattered source reaches an index rather than a volume.
	if s.Discovery != manifest.DiscoverySpotlight {
		t.Errorf("Discovery = %q, want %q; .blend1 backups are scattered", s.Discovery, manifest.DiscoverySpotlight)
	}
	if !strings.Contains(s.Files.Query, "blend1") {
		t.Errorf("Files.Query = %q, want it to name .blend1", s.Files.Query)
	}
	if s.Files.Match != "" {
		t.Errorf("Files.Match = %q, want none; a Spotlight source is found by query", s.Files.Match)
	}
	if s.Timestamp.From != manifest.FromMtime {
		t.Errorf("Timestamp.From = %q, want %q; a .blend1 carries no time in its name", s.Timestamp.From, manifest.FromMtime)
	}
	if !s.Perishable {
		t.Error("Perishable = false, want true; a .blend1 is overwritten by the next save and its mtime moves with it")
	}
	if s.Cause != source.CauseUserAction {
		t.Errorf("Cause = %q, want %q; a .blend1 exists because somebody pressed save", s.Cause, source.CauseUserAction)
	}
}

// TestBuiltinBlenderDescriptionCarriesTheMtimeCaveat checks the one thing JSON
// cannot say for itself. An mtime records only a file's LAST modification, so
// a count for a past day shrinks as later work rewrites those files — a
// measured case fell from 1,773 files to 513 within hours. Anyone reading this
// manifest has to know these are day markers and never volume, and the
// description field exists for exactly that.
func TestBuiltinBlenderDescriptionCarriesTheMtimeCaveat(t *testing.T) {
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	s := find(t, sources, "blender-backups")

	if s.Description == "" {
		t.Fatal("Description is empty")
	}
	lower := strings.ToLower(s.Description)
	for _, want := range []string{"marker", "volume"} {
		if !strings.Contains(lower, want) {
			t.Errorf("Description does not mention %q; it reads %q", want, s.Description)
		}
	}
}

func TestUserDirSitsBesideTheRestOfTheConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := manifest.UserDir()
	if err != nil {
		t.Fatalf("UserDir() = %v", err)
	}
	if want := filepath.Join(home, ".timetattle", "manifests"); dir != want {
		t.Errorf("UserDir() = %q, want %q", dir, want)
	}
}

func TestLoadAllWithoutUserManifestsIsJustTheBuiltins(t *testing.T) {
	builtin, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	got, err := manifest.LoadAll(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	if len(got) != len(builtin) {
		t.Fatalf("LoadAll() returned %d sources, want the %d built-ins", len(got), len(builtin))
	}
}

// TestLoadAllToleratesAMissingUserDirectory: nobody has written a manifest yet
// is the normal state, not a failure.
func TestLoadAllToleratesAMissingUserDirectory(t *testing.T) {
	got, err := manifest.LoadAll(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("LoadAll() returned nothing; the built-ins should still be there")
	}
}

// TestUserSourceReplacesABuiltinWholesale pins the override rule. A user source
// sharing an id replaces the built-in entirely rather than merging field by
// field, because a partial merge that silently keeps half of a built-in cannot
// be reasoned about from the file in front of you.
func TestUserSourceReplacesABuiltinWholesale(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "motion.json", `{
  "manifest_version": 1,
  "sources": [
    {
      "id": "motion-autosave",
      "type": "timestamped-files",
      "emits": "point", "cause": "timer",
      "app": "com.apple.motionapp",
      "description": "Motion, relocated and read from the filesystem.",
      "files": { "root": "/Volumes/Work/Autosave Vault", "match": "*/*.motn" },
      "timestamp": { "from": "mtime" }
    }
  ]
}`)

	got, err := manifest.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	s := find(t, got, "motion-autosave")

	if s.Files.Root != "/Volumes/Work/Autosave Vault" {
		t.Errorf("Files.Root = %q, want the user's", s.Files.Root)
	}
	if s.Timestamp.From != manifest.FromMtime {
		t.Errorf("Timestamp.From = %q, want the user's %q", s.Timestamp.From, manifest.FromMtime)
	}
	// The built-in sets both of these. Either surviving would mean a merge.
	if s.Project != nil {
		t.Errorf("Project = %+v, want nil; the built-in's project rule leaked through", s.Project)
	}
	if s.Perishable {
		t.Error("Perishable = true, want false; the built-in's flag leaked through")
	}
	if s.Timestamp.Pattern != "" {
		t.Errorf("Timestamp.Pattern = %q, want empty; the built-in's pattern leaked through", s.Timestamp.Pattern)
	}
}

// TestUserOverrideKeepsTheBuiltinsPosition keeps the collection order stable,
// so overriding one source does not shuffle a harvest's output.
func TestUserOverrideKeepsTheBuiltinsPosition(t *testing.T) {
	builtin, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	var want []string
	for _, s := range builtin {
		want = append(want, s.ID)
	}

	dir := t.TempDir()
	writeIn(t, dir, "blender.json", `{
  "manifest_version": 1,
  "sources": [
    {
      "id": "blender-backups",
      "type": "timestamped-files",
      "emits": "point", "cause": "user-action",
      "app": "org.blenderfoundation.blender",
      "files": { "root": "/Volumes/Work/blender", "match": "*.blend1" },
      "timestamp": { "from": "mtime" }
    }
  ]
}`)

	got, err := manifest.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestUserManifestCanAddASourceOfItsOwn(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "photoshop.json", `{
  "manifest_version": 1,
  "sources": [
    {
      "id": "photoshop-autorecover",
      "type": "timestamped-files",
      "emits": "point", "cause": "recovery",
      "app": "com.adobe.Photoshop",
      "perishable": true,
      "description": "Photoshop drops recovery files while a document is open.",
      "files": { "root": "~/Library/Application Support/Adobe/Adobe Photoshop 2026/AutoRecover", "match": "*.psb" },
      "timestamp": { "from": "mtime" }
    }
  ]
}`)

	got, err := manifest.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	s := find(t, got, "photoshop-autorecover")
	if s.App != "com.adobe.Photoshop" {
		t.Errorf("App = %q", s.App)
	}
	// A new source is appended, so the built-ins keep their places.
	if got[len(got)-1].ID != "photoshop-autorecover" {
		t.Errorf("added source landed at position %d of %d, want last", len(got), len(got))
	}
	find(t, got, "motion-autosave")
	find(t, got, "blender-backups")
}

// TestABrokenUserManifestDoesNotCostTheBuiltins: manifests are hand-edited, and
// one typo should not disable evidence collection wholesale. The failure is
// reported and everything that loaded is still returned.
func TestABrokenUserManifestDoesNotCostTheBuiltins(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "broken.json", `{ "manifest_version": 1, "sources": [ `)
	writeIn(t, dir, "good.json", `{
  "manifest_version": 1,
  "sources": [
    {
      "id": "lightroom-catalog",
      "type": "timestamped-files",
      "emits": "point", "cause": "timer",
      "app": "com.adobe.Lightroom",
      "files": { "root": "~/Pictures", "match": "*.lrcat" },
      "timestamp": { "from": "mtime" }
    }
  ]
}`)

	got, err := manifest.LoadAll(dir)
	if err == nil {
		t.Fatal("LoadAll() = nil error, want the broken manifest reported")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("error does not name the file: %v", err)
	}
	find(t, got, "motion-autosave")
	find(t, got, "blender-backups")
	find(t, got, "lightroom-catalog")
}

// TestLoadAllReadsUserManifestsInNameOrder makes layering deterministic: two
// files claiming an id have to resolve the same way on every machine, and
// directory order is not a promise any filesystem makes.
func TestLoadAllReadsUserManifestsInNameOrder(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "01-first.json", `{
  "manifest_version": 1,
  "sources": [
    { "id": "motion-autosave", "type": "timestamped-files", "emits": "point", "cause": "timer", "app": "com.apple.motionapp",
      "files": { "root": "/first", "match": "*.motn" }, "timestamp": { "from": "mtime" } }
  ]
}`)
	writeIn(t, dir, "02-second.json", `{
  "manifest_version": 1,
  "sources": [
    { "id": "motion-autosave", "type": "timestamped-files", "emits": "point", "cause": "timer", "app": "com.apple.motionapp",
      "files": { "root": "/second", "match": "*.motn" }, "timestamp": { "from": "mtime" } }
  ]
}`)

	got, err := manifest.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	if s := find(t, got, "motion-autosave"); s.Files.Root != "/second" {
		t.Errorf("Files.Root = %q, want /second; the last manifest by name should win", s.Files.Root)
	}
}

// TestLoadAllOnlyReadsJSON keeps a README or an editor's backup file in the
// manifests directory from being reported as a broken manifest.
func TestLoadAllOnlyReadsJSON(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "README.md", "Drop manifests here.")
	writeIn(t, dir, "motion.json.bak", "not json at all")

	got, err := manifest.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	find(t, got, "motion-autosave")
}

// TestLoadAllDefaultsToTheUserDirectory means a caller cannot forget to layer
// user manifests in and silently get built-ins only.
func TestLoadAllDefaultsToTheUserDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeIn(t, filepath.Join(home, ".timetattle", "manifests"), "extra.json", `{
  "manifest_version": 1,
  "sources": [
    { "id": "sketch-autosave", "type": "timestamped-files", "emits": "point", "cause": "timer", "app": "com.bohemiancoding.sketch3",
      "files": { "root": "~/Documents", "match": "*.sketch" }, "timestamp": { "from": "mtime" } }
  ]
}`)

	got, err := manifest.LoadAll("")
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	find(t, got, "sketch-autosave")
}
