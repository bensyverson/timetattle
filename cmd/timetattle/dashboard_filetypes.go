package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// renderFiletypeBreakdown draws a horizontal stacked bar of file activity by
// category over the period — documentation versus tests versus code versus
// graphics versus config, with an explicit "other" bucket — from the
// filetype task (project/2026-08-07-cli-overhaul-plan.md).
//
// # Pushback, settled here: this does NOT read internal/filedays
//
// The task brief, and this file's own original stub comment, both assumed
// internal/filedays events "land in the event table" with locator/document
// fields to categorize. They do not. internal/filedays.Take only ever
// writes a JSON snapshot to disk (Snapshot.Roots[i].{Raw,Filtered} are
// map[string]int PER-DAY COUNTS with no filename, path or extension
// anywhere in them) and never constructs a source.Event — see that
// package's own walkableRoot doc comment: "Nothing here produces a
// Locator — a filedays snapshot yields no events at all", confirmed by
// cmd/timetattle/harvest.go's own comment that "the filedays snapshot does
// not feed ingestion" and by grepping every source.Register call site in
// this repository (filedays appears in none of them). There is, today, no
// store row with source_id = "filedays" to query, scoped or otherwise, so
// the criterion "totals reconcile with filedays counts" cannot be built
// against filedays literally: there is no filedays total to reconcile
// against.
//
// What DOES carry a per-file Locator with an extension is fs-observations
// (internal/fsobserve), which is a different collector answering a related
// but distinct question (see that package's own doc comment, "Not the same
// job as internal/filedays") — and its events already flow into
// data.Events, the same slice the report itself was computed over, because
// dashboardCmd's own store query (dashboard.go) has no source filter. So
// this section needs no extra Store query at all (contrary to this file's
// own original stub comment, which assumed one the way source freshness
// does): it filters data.Events for Source == fsobserve.ID and reads
// Locator's extension, exactly the mechanism internal/chains already uses
// for extension-aware queries elsewhere in this codebase.
//
// The practical effect for a reader: this section undercounts relative to
// what internal/filedays would show if it were wired up, because
// fs-observations is scoped to REGISTERED project roots walked by the
// fs-observations collector, while filedays scans whatever roots a
// separate, independently-configured roots file names — the two collectors
// need not cover the same directories. That is a real gap, not hidden by
// this comment: the fix is either wiring internal/filedays into ingestion
// (a source.Source of its own, decided by Ben) or accepting that this panel
// is fs-observations' view of file activity, not filedays'. Flagged in the
// task's own job note; not fixed here, since neither option is a one-file
// change.
//
// # Touches, not distinct files
//
// "File activity" is counted as TOUCHES — one per qualifying event, exactly
// as fsobserve.Walker.observe emits one source.Point per birth or mtime
// timestamp actually crossed since the last harvest (see that function's
// own doc comment) — not deduplicated to one count per distinct file.
// Three reasons: (1) the task's own spec says "file-touch activity", which
// names the touch as the unit; (2) fsobserve's `since` filtering is already
// per-timestamp, so a file whose mtime has not changed since the last
// harvest contributes no new event — touches are not inflated by simply
// re-running harvest against unchanged files, the same decay problem
// internal/filedays exists to avoid; (3) a file touched five times in a
// period represents more activity than one touched once, which a
// distinct-file count would flatten to the same 1. Distinct-file counting
// was considered and rejected: it would need a second aggregation this
// section has no other reason to build, over a question ("how many files"
// rather than "how much touching") the task did not ask.
//
// # Category rules
//
// TESTS is decided by filename convention BEFORE extension, using only the
// conventions the task named plus their closest siblings, deliberately
// narrow — see categorizeLocator and isTestFilename. A wrong test
// classification is worse than "other" (the task's own words), so a
// convention with a plausible false positive (e.g. matching ANY extension
// containing ".test." — "notes.test.md" is a real, plausible non-test
// filename) is scoped down to the ecosystem where it is actually a
// near-universal convention (JS/TS) rather than applied unconditionally.
//
// Every extension not claimed by documentation, graphics, config or code
// below — including this codebase's own client ecosystem's creative
// project files, .blend/.blend1/.motn/.aep, which are project containers
// for 3D/animation/compositing work and not image assets — falls to OTHER
// rather than being guessed into "graphics". That is a real judgment call:
// a maintainer who wants a "creative project" bucket of its own can add
// one, but the six categories this task specifies have no slot for it, and
// "other" is exactly the honest answer the task asked for in that case.
//
// THIS FILE, AND dashboard_filetypes_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderFiletypeBreakdown(_ context.Context, out io.Writer, scope metricsScope, period dashboardPeriod,
	cc colorCapability, data *dashboardData) error {
	counts, total := filetypeCounts(data.Events, scope)
	if total == 0 {
		return renderFiletypeNoData(out, period, data.Log)
	}

	order := orderedFiletypeCategories(counts)
	if err := renderFiletypeBar(out, order, counts, total, cc); err != nil {
		return err
	}
	return renderFiletypeLegend(out, order, counts, total, cc)
}

// The six categories this task specifies. ftOther is never part of
// filetypeOrder — it is always appended last by orderedFiletypeCategories,
// regardless of its own count, per the task's own ordering rule.
const (
	ftDocumentation = "documentation"
	ftTests         = "tests"
	ftCode          = "code"
	ftGraphics      = "graphics"
	ftConfig        = "config"
	ftOther         = "other"
)

// filetypeOrder is the fixed declaration order used to break ties when two
// categories share a count, so the printed order is reproducible rather
// than dependent on Go's randomized map iteration.
var filetypeOrder = []string{ftDocumentation, ftTests, ftCode, ftGraphics, ftConfig}

// filetypeDisplayNames is what the legend prints for each category.
var filetypeDisplayNames = map[string]string{
	ftDocumentation: "Documentation",
	ftTests:         "Tests",
	ftCode:          "Code",
	ftGraphics:      "Graphics",
	ftConfig:        "Config",
	ftOther:         "Other",
}

// filetypeStyle is one category's bar glyph and color.
type filetypeStyle struct {
	// Glyph is what prints under colorNone, and what the bar is built out
	// of under every capability — see the doc comment on renderFiletypeBar
	// for why color layers ON TOP of the glyph rather than replacing it.
	// Fixed per category, never data-dependent, which is what makes it a
	// deterministic glyph rather than an assignment that could shuffle
	// between two runs.
	Glyph rune
	Color RGB
}

// filetypeStyles gives every category, including other, a fixed glyph and
// color — a map literal rather than a slice because lookup is always by
// category name, never by position.
var filetypeStyles = map[string]filetypeStyle{
	ftDocumentation: {'D', RGB{91, 141, 217}},
	ftTests:         {'T', RGB{230, 159, 46}},
	ftCode:          {'C', RGB{68, 170, 100}},
	ftGraphics:      {'G', RGB{175, 105, 210}},
	ftConfig:        {'K', RGB{212, 175, 55}},
	ftOther:         {'O', RGB{140, 140, 140}},
}

// filetypeBarWidth is the stacked bar's total column width.
const filetypeBarWidth = 50

// documentationExtensions are prose formats: read for their content, not
// executed and not a build input.
var documentationExtensions = map[string]bool{
	".md": true, ".mdx": true, ".markdown": true, ".rst": true,
	".txt": true, ".adoc": true, ".org": true,
}

// graphicsExtensions are image assets — raster, vector, or a design tool's
// own document format for one. Deliberately excludes .blend/.motn/.aep: see
// this file's package-level doc comment on why those fall to OTHER instead.
var graphicsExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".bmp": true, ".tiff": true, ".tif": true, ".ico": true, ".webp": true,
	".exr": true, ".psd": true, ".psb": true, ".ai": true, ".heic": true,
}

// configExtensions are structured configuration/serialization formats. Note
// .json is genuinely ambiguous — a data export is also valid JSON — but the
// task's own spec names it as a config example, and this codebase's own
// build/CI/editor configuration is overwhelmingly json/yaml/toml, so it is
// classified here rather than invented a data-versus-config distinction
// nothing else in this section can tell apart.
var configExtensions = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".env": true, ".properties": true,
}

// codeExtensions are source files in ecosystems either this repository
// (Go) or its client work (per fsobserve's own package doc: Blender/Motion/
// Photoshop projects, which are themselves scripted) plausibly contains.
// Kept to unambiguous programming-language extensions; anything not listed
// falls to OTHER rather than being guessed at.
var codeExtensions = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".mjs": true, ".cjs": true, ".java": true, ".c": true, ".h": true, ".cpp": true,
	".cc": true, ".hpp": true, ".cs": true, ".rb": true, ".rs": true, ".swift": true,
	".kt": true, ".kts": true, ".m": true, ".mm": true, ".php": true, ".pl": true,
	".sh": true, ".bash": true, ".zsh": true, ".lua": true, ".sql": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".vue": true, ".r": true, ".jl": true, ".scala": true, ".clj": true,
	".ex": true, ".exs": true, ".erl": true, ".hs": true,
}

// testMarkedExtensions is the ecosystem where a bare ".test." or ".spec."
// infix is a near-universal test-file convention (Jest, Vitest, Mocha,
// Jasmine). Scoped to these extensions rather than applied to any file
// whatsoever, because outside this ecosystem the same infix is a plausible
// ordinary filename — "notes.test.md" is not a test.
var testMarkedExtensions = map[string]bool{
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true,
}

// categorizeLocator assigns one of the six categories to a file, given its
// absolute path. Tests are decided first, by filename convention; anything
// left over falls through the extension tables in a fixed order, and
// anything none of them claims is OTHER. See this file's own package doc
// for the reasoning behind every rule here.
func categorizeLocator(locator string) string {
	base := strings.ToLower(filepath.Base(locator))
	ext := strings.ToLower(filepath.Ext(base))

	if isTestFilename(base, ext) {
		return ftTests
	}
	switch {
	case documentationExtensions[ext]:
		return ftDocumentation
	case graphicsExtensions[ext]:
		return ftGraphics
	case configExtensions[ext]:
		return ftConfig
	case codeExtensions[ext]:
		return ftCode
	default:
		return ftOther
	}
}

// isTestFilename applies the filename conventions the task named —
// `_test.go`, `test_*.py`, `.spec.*` — plus each one's closest sibling
// (`*_test.py`, `.test.*`, and Ruby's `_spec.rb`/`_test.rb`), and nothing
// broader. A convention not listed here classifies as its ordinary
// extension category (or OTHER), never as a guess at "tests".
func isTestFilename(base, ext string) bool {
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case ext == ".py" && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
		return true
	case testMarkedExtensions[ext] && (strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")):
		return true
	case strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "_test.rb"):
		return true
	default:
		return false
	}
}

// filetypeInScope reports whether an event's stamped client/project belongs
// under scope, mirroring metricsScope's own showsClient/showsProject
// filtering rule for a raw event rather than a Report row: an empty
// scope.Client or scope.Project narrows nothing, exactly as metricsScope's
// own doc comment describes "filter what you print" for a Report.
func filetypeInScope(scope metricsScope, client, project string) bool {
	if scope.Client != "" && client != scope.Client {
		return false
	}
	if scope.Project != "" && project != scope.Project {
		return false
	}
	return true
}

// filetypeCounts is the pure aggregation this section renders from: every
// fs-observations event in events, scoped, counted as one touch under its
// category — see this file's own package doc for why fs-observations rather
// than filedays, and touches rather than distinct files. The returned
// counts always cover exactly the six categories (zero included), so a
// caller can range over filetypeOrder plus ftOther without a missing-key
// zero value surprising it.
func filetypeCounts(events []source.Event, scope metricsScope) (map[string]int, int) {
	counts := map[string]int{
		ftDocumentation: 0, ftTests: 0, ftCode: 0, ftGraphics: 0, ftConfig: 0, ftOther: 0,
	}
	total := 0
	for _, e := range events {
		if e.Source != fsobserve.ID || e.Locator == "" {
			continue
		}
		if !filetypeInScope(scope, e.Identity.Client, e.Identity.Project) {
			continue
		}
		counts[categorizeLocator(e.Locator)]++
		total++
	}
	return counts, total
}

// orderedFiletypeCategories sorts the five named categories by count
// descending, breaking ties by filetypeOrder's own fixed declaration order
// for reproducibility, then appends OTHER last unconditionally — the task's
// own rule, independent of how large other's count is.
func orderedFiletypeCategories(counts map[string]int) []string {
	order := make([]string, len(filetypeOrder))
	copy(order, filetypeOrder)
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	return append(order, ftOther)
}

// filetypeBarWidths distributes width columns across order proportionally
// to counts, using the largest-remainder method: floor each category's
// exact share, then hand the leftover columns one at a time to the
// categories with the largest fractional remainder, breaking ties by
// order's own position (stable sort over order's original sequence) so the
// result is reproducible rather than dependent on floating-point rounding
// direction. Every category's width sums to exactly width, including a
// zero-count category contributing exactly zero.
func filetypeBarWidths(order []string, counts map[string]int, total, width int) map[string]int {
	widths := make(map[string]int, len(order))
	type remainder struct {
		cat string
		rem float64
	}
	remainders := make([]remainder, 0, len(order))
	used := 0
	for _, cat := range order {
		exact := float64(counts[cat]) / float64(total) * float64(width)
		w := int(math.Floor(exact))
		widths[cat] = w
		used += w
		remainders = append(remainders, remainder{cat, exact - float64(w)})
	}
	sort.SliceStable(remainders, func(i, j int) bool { return remainders[i].rem > remainders[j].rem })
	for i := 0; i < width-used && i < len(remainders); i++ {
		widths[remainders[i].cat]++
	}
	return widths
}

// renderFiletypeBar draws the stacked bar as a sequence of each category's
// glyph, repeated to its proportional width, painted under cc — never bare
// color blocks. The glyph is what makes colorNone legible at all (design
// requirement: "deterministic colorNone glyphs"), and printing it
// unconditionally rather than swapping in a plain block under color keeps
// one code path instead of two, and keeps a screen reader or a
// color-blind reader's plain-text capture identical in content to what a
// truecolor terminal shows, just without the paint.
func renderFiletypeBar(out io.Writer, order []string, counts map[string]int, total int, cc colorCapability) error {
	widths := filetypeBarWidths(order, counts, total, filetypeBarWidth)
	var b strings.Builder
	b.WriteString("  ")
	for _, cat := range order {
		w := widths[cat]
		if w == 0 {
			continue
		}
		style := filetypeStyles[cat]
		b.WriteString(cc.Paint(style.Color))
		b.WriteString(strings.Repeat(string(style.Glyph), w))
		b.WriteString(cc.Reset())
	}
	b.WriteString("\n")
	_, err := io.WriteString(out, b.String())
	return err
}

// renderFiletypeLegend prints one line per category in order — including
// zero-count categories, and ALWAYS including other — so a category is
// never silently omitted from the printed record regardless of its count.
func renderFiletypeLegend(out io.Writer, order []string, counts map[string]int, total int, cc colorCapability) error {
	for _, cat := range order {
		n := counts[cat]
		pct := float64(n) / float64(total) * 100
		style := filetypeStyles[cat]
		if _, err := fmt.Fprintf(out, "  %s%c%s %s: %d (%.1f%%)\n",
			cc.Paint(style.Color), style.Glyph, cc.Reset(), filetypeDisplayNames[cat], n, pct); err != nil {
			return err
		}
	}
	return nil
}

// renderFiletypeNoData is what prints instead of a bar when there is no
// file-touch activity at all — design principle 3: "no data" must never
// render as "zero work". data.Log.during distinguishes three cases, the
// same three the absence-of-a-row rule (AGENTS.md) names: an observed zero
// (collection ran, found nothing), collection that never ran in this
// period at all, and — new here — collection that ran but LOST data, which
// is neither "zero" nor "nothing recorded" but a genuine gap.
func renderFiletypeNoData(out io.Writer, period dashboardPeriod, log collectionLog) error {
	state := log.during(period.From, period.To)
	var msg string
	switch state {
	case collectionUnrecorded:
		msg = "  no data: nothing was being collected for this scope and period\n"
	case collectionLost:
		msg = "  no data: collection ran but lost data in this period; file-touch activity is unknown, not zero\n"
	default:
		msg = "  no data: collection ran and observed no file-touch activity for this scope and period\n"
	}
	_, err := io.WriteString(out, msg)
	return err
}
