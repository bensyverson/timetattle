package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
)

// write puts a manifest on disk and returns its path.
func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// motionManifest is the real Motion autosave vault, which is what this schema
// was designed against.
const motionManifest = `{
  "manifest_version": 1,
  "sources": [
    {
      "id": "motion-autosave",
      "type": "timestamped-files",
      "emits": "point", "cause": "timer",
      "app": "com.apple.motionapp",
      "perishable": true,
      "description": "Motion autosaves roughly every 15 minutes while a composition is open.",
      "files": { "root": "~/Movies/Motion Projects/Autosave Vault", "match": "*/*.motn" },
      "timestamp": { "from": "filename", "pattern": "_(\\d{4}-\\d{2}-\\d{2}_\\d{4})\\.motn$", "layout": "2006-01-02_1504" },
      "project": { "from": "path", "pattern": "Autosave Vault/([^/]+)/" }
    }
  ]
}`

func TestLoadReadsTheMotionManifest(t *testing.T) {
	m, err := manifest.Load(write(t, "motion.json", motionManifest))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(m.Sources) != 1 {
		t.Fatalf("loaded %d sources, want 1", len(m.Sources))
	}
	s := m.Sources[0]
	if s.ID != "motion-autosave" {
		t.Errorf("ID = %q", s.ID)
	}
	if s.App != "com.apple.motionapp" {
		t.Errorf("App = %q", s.App)
	}
	if !s.Perishable {
		t.Error("Perishable = false, want true; Motion prunes the vault")
	}
	if s.Timestamp.Layout != "2006-01-02_1504" {
		t.Errorf("Layout = %q", s.Timestamp.Layout)
	}
}

// Tilde has to expand or every manifest hardcodes somebody's username.
func TestRootExpandsTilde(t *testing.T) {
	m, err := manifest.Load(write(t, "motion.json", motionManifest))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	root := m.Sources[0].Files.Root
	if strings.HasPrefix(root, "~") {
		t.Errorf("Root = %q, want the tilde expanded", root)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("Root = %q, want an absolute path", root)
	}
}

func TestDocumentRuleMustExtractExactlyOnePathName(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point", "cause": "timer",
      "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"},
      "document": {"from": "filename", "pattern": "(x)"}}]}`
	_, err := manifest.Load(write(t, "bad-document.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want invalid document rule rejected")
	}
	if !strings.Contains(err.Error(), "document.from") {
		t.Errorf("Load() = %q, want document.from named", err)
	}
}

// Manifests live outside our repo on other people's disks, so we cannot
// migrate them. The version has to be present and checked from the start.
func TestMissingVersionIsRejected(t *testing.T) {
	body := `{"sources": [{"id": "x", "type": "timestamped-files", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "noversion.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for a manifest with no version")
	}
	if !strings.Contains(err.Error(), "manifest_version") {
		t.Errorf("Load() = %q, want the missing field named", err)
	}
}

func TestUnknownVersionIsRejectedRatherThanParsedOptimistically(t *testing.T) {
	body := `{"manifest_version": 99, "sources": []}`
	_, err := manifest.Load(write(t, "future.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for an unsupported version")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("Load() = %q, want the offending version named", err)
	}
}

func TestUnknownTypeIsCaughtAtLoad(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-file", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "badtype.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for an unknown type")
	}
	if !strings.Contains(err.Error(), "timestamped-file") {
		t.Errorf("Load() = %q, want the bad value quoted so the typo is visible", err)
	}
}

func TestUnknownEmitsIsCaughtAtLoad(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "points", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "bademits.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for an unknown emits value")
	}
	if !strings.Contains(err.Error(), "points") {
		t.Errorf("Load() = %q, want the bad value quoted", err)
	}
}

// Cause has no default, and the absence of one is the point. An artifact
// written by an explicit save is evidence that a person was at the keyboard; one
// written by a wall-clock timer is evidence only that an application was
// running — the 2026-07-11 17:53 Motion autosave was written with the screen
// off, during a break. Guessing either way on the author's behalf puts a claim
// into the record that nobody made, so an omission is refused instead.
func TestCauseIsRequiredBecauseNeitherAnswerCanBeAssumed(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "nocause.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for a source that does not say what caused its files")
	}
	if !strings.Contains(err.Error(), "cause") {
		t.Errorf("Load() = %q, want the missing field named", err)
	}
}

func TestUnknownCauseIsCaughtAtLoad(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point",
	  "cause": "human", "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "badcause.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an error for an unknown cause")
	}
	if !strings.Contains(err.Error(), "human") {
		t.Errorf("Load() = %q, want the bad value quoted so the typo is visible", err)
	}
	if !strings.Contains(err.Error(), string(source.CauseTimer)) {
		t.Errorf("Load() = %q, want the accepted values named", err)
	}
}

// A recursive glob from a shallow root walks the whole volume. That mistake
// was made during development with a find(1) that had to be killed, and a
// harvester that hangs is worse than one that refuses.
func TestRecursiveGlobIsRefusedAndPointsAtSpotlight(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "blender", "type": "timestamped-files", "emits": "point", "cause": "user-action",
	  "files": {"root": "~", "match": "**/*.blend1"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "blender.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want a recursive glob refused")
	}
	if !strings.Contains(err.Error(), "spotlight") {
		t.Errorf("Load() = %q, want the error to name the supported alternative", err)
	}
}

// Spotlight is the sanctioned way to search broadly, so it must be accepted
// where a bare recursive glob is not.
func TestSpotlightDiscoveryIsAccepted(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "blender", "type": "timestamped-files", "emits": "point", "cause": "user-action",
	  "app": "org.blenderfoundation.blender", "discovery": "spotlight",
	  "files": {"query": "kMDItemFSName == '*.blend1'"}, "timestamp": {"from": "mtime"}}]}`
	m, err := manifest.Load(write(t, "blender.json", body))
	if err != nil {
		t.Fatalf("Load() = %v, want spotlight discovery accepted", err)
	}
	if m.Sources[0].Discovery != manifest.DiscoverySpotlight {
		t.Errorf("Discovery = %q, want %q", m.Sources[0].Discovery, manifest.DiscoverySpotlight)
	}
}

// A Spotlight root is optional but, when given, is passed to mdfind -onlyin.
// Left unexpanded it searches a directory literally named "~" and reports
// nothing, forever — a silent wrong answer, which is the failure mode load-time
// validation exists to prevent.
func TestSpotlightRootIsAlsoTildeExpanded(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "blender", "type": "timestamped-files", "emits": "point", "cause": "user-action",
	  "discovery": "spotlight", "files": {"root": "~/Documents", "query": "kMDItemFSName == '*.blend1'"},
	  "timestamp": {"from": "mtime"}}]}`
	m, err := manifest.Load(write(t, "blender.json", body))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	root := m.Sources[0].Files.Root
	if strings.HasPrefix(root, "~") {
		t.Errorf("Root = %q, want the tilde expanded for spotlight too", root)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("Root = %q, want an absolute path", root)
	}
}

// A timestamped-files source reads file metadata (a name, an mtime, a birth
// time), which carries exactly one instant per file. The only way to make an
// interval out of metadata alone is to pair files up, which is inference and
// is explicitly not a source's job — thresholds like that have to be
// calibrated globally, not chosen per source. This does not forbid a file
// from ever carrying a genuine interval: one recorded inside a file's own
// contents (a log with start/end timestamps, an embedded session record) is
// recoverable in principle, but not declaratively — it needs a Go
// source.Source with its own format-specific reader, which is a different
// type than timestamped-files and never reaches this check.
//
// The rule is per-type, not global: a future sqlite or command source could
// legitimately emit intervals.
func TestTimestampedFilesCannotEmitIntervals(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "interval", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "interval.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want a file source claiming intervals refused")
	}
	if !strings.Contains(err.Error(), "interval") {
		t.Errorf("Load() = %q, want the offending value named", err)
	}
	// Pinned so the explanation cannot erode into a bare "not allowed": the
	// error has to say WHY (metadata carries one instant, an interval would
	// mean pairing files, which is inference) and name the escape hatch (a
	// format-specific Go source.Source for evidence genuinely inside a file).
	for _, want := range []string{"metadata", "pairing", "inference", "source.Source"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() = %q, want it to explain the refusal (missing %q)", err, want)
		}
	}
}

func TestSpotlightDiscoveryRequiresAQuery(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "blender", "type": "timestamped-files", "emits": "point", "cause": "user-action",
	  "discovery": "spotlight", "files": {"root": "~/x"}, "timestamp": {"from": "mtime"}}]}`
	_, err := manifest.Load(write(t, "blender.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want spotlight without a query refused")
	}
}

// Filename timestamps need both halves; one without the other silently
// produces zero events, which is the failure mode this check exists to stop.
func TestFilenameTimestampRequiresPatternAndLayout(t *testing.T) {
	for _, tc := range []struct{ name, ts string }{
		{"no layout", `{"from": "filename", "pattern": "(\\d{4})"}`},
		{"no pattern", `{"from": "filename", "layout": "2006"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point", "cause": "timer",
			  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": ` + tc.ts + `}]}`
			if _, err := manifest.Load(write(t, "x.json", body)); err == nil {
				t.Fatalf("Load() = nil, want %s refused", tc.name)
			}
		})
	}
}

func TestUnparseableTimestampPatternIsRejected(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"},
	  "timestamp": {"from": "filename", "pattern": "([unclosed", "layout": "2006"}}]}`
	_, err := manifest.Load(write(t, "x.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want an invalid regexp refused at load")
	}
}

func TestMtimeTimestampNeedsNoPattern(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.blend1"}, "timestamp": {"from": "mtime"}}]}`
	if _, err := manifest.Load(write(t, "x.json", body)); err != nil {
		t.Fatalf("Load() = %v, want mtime accepted without a pattern", err)
	}
}

// One bad source must not cost us the good ones. A manifest is hand-edited,
// so a typo in the fifth entry should not silently disable the other four.
func TestOneBadSourceDoesNotAbortTheOthers(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [
	  {"id": "good", "type": "timestamped-files", "emits": "point", "cause": "timer",
	   "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}},
	  {"id": "bad", "type": "nonsense", "emits": "point", "cause": "timer",
	   "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}
	]}`
	m, err := manifest.Load(write(t, "mixed.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want the bad source reported")
	}
	if m == nil {
		t.Fatal("Load() returned no manifest; the good source should survive")
	}
	if len(m.Sources) != 1 || m.Sources[0].ID != "good" {
		t.Fatalf("loaded %d sources, want only the good one", len(m.Sources))
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("Load() = %q, want the failing source named", err)
	}
}

// An error that does not say which file it came from is useless once a user
// has several manifests installed.
func TestErrorsNameTheFile(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "nonsense", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	path := write(t, "distinctive-name.json", body)
	_, err := manifest.Load(path)
	if err == nil {
		t.Fatal("Load() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "distinctive-name.json") {
		t.Errorf("Load() = %q, want the manifest filename in the message", err)
	}
}

func TestDuplicateIDsWithinAManifestAreRejected(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [
	  {"id": "same", "type": "timestamped-files", "emits": "point", "cause": "timer",
	   "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}},
	  {"id": "same", "type": "timestamped-files", "emits": "point", "cause": "timer",
	   "files": {"root": "~/y", "match": "*.motn"}, "timestamp": {"from": "mtime"}}
	]}`
	_, err := manifest.Load(write(t, "dupes.json", body))
	if err == nil {
		t.Fatal("Load() = nil, want duplicate IDs refused")
	}
}

func TestSourceWithoutAnIDIsRejected(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"type": "timestamped-files", "emits": "point", "cause": "timer",
	  "files": {"root": "~/x", "match": "*.motn"}, "timestamp": {"from": "mtime"}}]}`
	if _, err := manifest.Load(write(t, "noid.json", body)); err == nil {
		t.Fatal("Load() = nil, want a source without an ID refused")
	}
}

func TestMalformedJSONNamesTheFile(t *testing.T) {
	_, err := manifest.Load(write(t, "broken.json", `{"manifest_version": 1,`))
	if err == nil {
		t.Fatal("Load() = nil, want malformed JSON refused")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("Load() = %q, want the filename named", err)
	}
}

func TestMissingFileIsReported(t *testing.T) {
	_, err := manifest.Load(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("Load() = nil, want a missing manifest reported")
	}
}
