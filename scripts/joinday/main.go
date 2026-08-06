// Command joinday tests the assumption the whole architecture rests on: that
// machine-wide presence, narrowed by application focus and identified by
// document artifacts, reconstructs what a person would say they worked on.
//
// The spike could not run this test. It recorded the gap as a finding rather
// than a to-do — "the creative work ran 8-12 July and knowledgeC's window
// opens 18 July" — because the only source of application attribution at the
// time reached back nine days. Biome reaches back to 2026-07-05, so the window
// the spike needed is now readable.
//
// # What it measures, and why these numbers rather than others
//
// Three sources are read for each working day and reported side by side.
// Presence comes from knowledgeC /display/isBacklit and is the only direct
// observation of a person at the machine; it is machine-wide, so it is an
// upper bound for any single project. Focus comes from Biome and says which
// application held the front, but never which document. Artifacts come from a
// manifest of timestamped files and say which document, but never for how
// long. No one of them answers "how long on dark-crystal", which is the point:
// the answer is a join or it does not exist.
//
// The interesting quantities are the ones where the sources disagree, so they
// are reported rather than reconciled:
//
//   - Focus outside presence is time Biome says an application held the front
//     while knowledgeC did not say the screen was lit. It should be near zero.
//     If it is not, one of the two is wrong about sleep, and every duration
//     built on either is suspect. It is reported in three parts, because "not
//     lit" turned out to be three different claims — see below.
//   - Presence outside focus is time at a lit screen that no application
//     claims. Some of it is real (reading, a meeting), so it is a measure of
//     how much of a day app attribution can and cannot explain.
//   - An artifact outside every focus interval for its own application is the
//     join failing on its own terms, and is listed individually rather than
//     summarised, because one such case is worth more than the rate.
//
// # The backlight has three states and this tool used to model two
//
// /display/isBacklit carries rows of both values. In the 2026-07-29 snapshot
// 1,236 rows assert isBacklit=1 (264.31h raw) and 434 rows assert isBacklit=0
// (434.75h raw). What is left after BOTH values is time the sampler said
// nothing about, which is an absence of evidence and not evidence of absence.
//
// Until 2026-07-29 this tool selected ZVALUEINTEGER=1 and treated everything
// else as darkness, which conflated the two. It is not academic: on 2026-07-14
// the stream stops emitting between 12:35:20 and 14:02:24 with no row of either
// value, while /notification/usage keeps writing and Biome records dense app
// switching across ten applications throughout. Read as darkness, that deletes
// 1h27m of real work. See project/private/2026-07-29-sleep-asymmetry.md §5.
//
// So the day is now accounted for in four states rather than two:
//
//	observed ON  + observed OFF  + unobserved  + beyond the snapshot = the day
//
// The last one is new here too, and it removes a standing artifact rather than
// adding a subtlety. A snapshot stops where it was taken, so on the day of a
// harvest every focus interval after that instant used to appear as focus in
// darkness — 7.63h of apparent violation on 2026-07-29, which is the age of the
// snapshot and not a finding about sleep.
//
// Columns whose meaning did not change kept their names and their values.
// PRESENCE is observed-ON, exactly what it was before; UNCLAIMED and
// ATTRIBUTABLE are computed from it unchanged, so every figure published in
// project/private/2026-07-29-join-validation.md §3 is still directly comparable. The
// single OUTSIDE column is what split, into DARK, UNOBS and BEYOND.
//
// # Deliberate omissions
//
// It does not cluster, threshold or fit anything. Total engaged time was
// measured to vary 2.3x across plausible clustering parameters, so a tool that
// applied one before the fit exists would produce a number that looks like a
// measurement and is not. Everything here is either observed or arithmetic on
// observations.
//
// It also does not compare against project/private/ground-truth.md. Hardcoding the
// only verified data in the project into a script is how that file and this
// tool drift apart; the comparison belongs in a written conclusion next to the
// figures it cites.
//
// Like scripts/appfocus, this is an analysis tool rather than product
// behaviour, and is exempt from the project's red/green TDD rule.
//
// Usage:
//
//	go run ./scripts/joinday -from 2026-07-08 -to 2026-07-12
//	go run ./scripts/joinday -date 2026-07-11 -app com.apple.motionapp
//	go run ./scripts/joinday -date 2026-07-11 -snapshot path/to/knowledgeC.db.gz
package main

import (
	"compress/gzip"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timestamped"

	_ "modernc.org/sqlite"
)

// coreDataEpoch converts knowledgeC's Core Data timestamps to Unix seconds.
// Biome, confusingly, stores Unix seconds already; mixing the two shifts
// everything by 31 years, which is at least loud when it happens.
const coreDataEpoch = 978307200

func main() {
	from := flag.String("from", "", "first working day to report (YYYY-MM-DD)")
	to := flag.String("to", "", "last working day to report (YYYY-MM-DD, defaults to -from)")
	date := flag.String("date", "", "a single working day; shorthand for -from X -to X")
	snap := flag.String("snapshot", "", "knowledgeC snapshot, .db or .db.gz (default: the newest in ~/.timetattle/snapshots)")
	dir := flag.String("biome", "", "directory of Biome segments (default: the current user's)")
	manifestPath := flag.String("manifest", "", "manifest to read instead of the built-ins plus ~/.timetattle/manifests")
	app := flag.String("app", "", "list every focus interval for this bundle identifier")
	boundary := flag.Int("boundary", 5, "hour at which the working day starts")
	tolerance := flag.Duration("tolerance", time.Second,
		"how far a bare stop may sit from the end of an unclaimed stretch and still bound it")
	flag.Parse()

	if *date != "" {
		*from, *to = *date, *date
	}
	if *to == "" {
		*to = *from
	}
	if *from == "" {
		fatal("give -date, or -from and -to")
	}

	if err := run(*from, *to, *snap, *dir, *manifestPath, *app, *boundary, *tolerance); err != nil {
		fatal("%v", err)
	}
}

func run(from, to, snap, dir, manifestPath, app string, boundary int, tolerance time.Duration) error {
	first, err := time.ParseInLocation("2006-01-02", from, time.Local)
	if err != nil {
		return fmt.Errorf("bad -from: %w", err)
	}
	last, err := time.ParseInLocation("2006-01-02", to, time.Local)
	if err != nil {
		return fmt.Errorf("bad -to: %w", err)
	}
	if last.Before(first) {
		return fmt.Errorf("-to %s is before -from %s", to, from)
	}

	if snap == "" {
		if snap, err = newestSnapshot(); err != nil {
			return err
		}
	}
	if dir == "" {
		if dir, err = biome.DefaultAppUsageDir(); err != nil {
			return err
		}
	}

	light, err := loadPresence(snap)
	if err != nil {
		return err
	}
	focus, bare, horizons, err := loadFocus(dir)
	if err != nil {
		return err
	}
	artifacts, err := loadArtifacts(manifestPath)
	if err != nil {
		return err
	}

	fmt.Printf("presence  %s\n", snap)
	fmt.Printf("          reaches %s → %s\n",
		light.reach.start.Format("2006-01-02 15:04:05"), light.reach.end.Format("2006-01-02 15:04:05"))
	fmt.Printf("focus     %s\n", dir)
	fmt.Printf("artifacts %s\n\n", artifactSource(manifestPath))

	var days []summary
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		w := window(d, boundary)
		days = append(days, report(w, d, light, focus, bare, horizons, artifacts, app, tolerance))
	}
	printSummary(days)
	printBareCensus(days, tolerance)
	return nil
}

// window is the span a working day occupies. The boundary is 05:00 rather than
// midnight because a 01:23 autosave belongs to the evening that produced it,
// and the ground truth days were confirmed by a person who thinks that way.
func window(d time.Time, boundary int) span {
	return span{
		start: d.Add(time.Duration(boundary) * time.Hour),
		end:   d.AddDate(0, 0, 1).Add(time.Duration(boundary) * time.Hour),
	}
}

// ---------------------------------------------------------------- intervals

type span struct{ start, end time.Time }

func (s span) dur() time.Duration { return s.end.Sub(s.start) }

// merge normalises a set of spans into disjoint, sorted spans.
//
// Every total below runs through here first. Focus intervals and presence
// blocks both overlap in the raw data — knowledgeC writes a block per
// backlight change and they abut or repeat — so summing durations directly
// double-counts, and does so silently in the direction that flatters the tool.
func merge(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	cp := append([]span(nil), in...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].start.Before(cp[j].start) })

	out := []span{cp[0]}
	for _, s := range cp[1:] {
		tail := &out[len(out)-1]
		if s.start.After(tail.end) {
			out = append(out, s)
			continue
		}
		if s.end.After(tail.end) {
			tail.end = s.end
		}
	}
	return out
}

// intersect returns the parts of a that are also in b. Both must be merged.
func intersect(a, b []span) []span {
	var out []span
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		start := laterOf(a[i].start, b[j].start)
		end := earlierOf(a[i].end, b[j].end)
		if start.Before(end) {
			out = append(out, span{start, end})
		}
		if a[i].end.Before(b[j].end) {
			i++
		} else {
			j++
		}
	}
	return out
}

// subtract returns the parts of a that are not in b. Both must be merged.
func subtract(a, b []span) []span {
	var out []span
	for _, s := range a {
		cur := s
		for _, cut := range b {
			if !cut.end.After(cur.start) {
				continue
			}
			if !cut.start.Before(cur.end) {
				break
			}
			if cut.start.After(cur.start) {
				out = append(out, span{cur.start, cut.start})
			}
			if cut.end.After(cur.start) {
				cur.start = cut.end
			}
			if !cur.start.Before(cur.end) {
				break
			}
		}
		if cur.start.Before(cur.end) {
			out = append(out, cur)
		}
	}
	return out
}

// clip trims spans to a window, keeping the parts that fall inside it. An
// interval running across the 05:00 boundary belongs partly to each day, and
// assigning it wholesale to one of them is how a long evening session gets
// counted twice or not at all.
func clip(in []span, w span) []span {
	var out []span
	for _, s := range in {
		start, end := laterOf(s.start, w.start), earlierOf(s.end, w.end)
		if start.Before(end) {
			out = append(out, span{start, end})
		}
	}
	return out
}

func total(in []span) time.Duration {
	var d time.Duration
	for _, s := range in {
		d += s.dur()
	}
	return d
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ---------------------------------------------------------------- loading

// backlight is the three-state reading of /display/isBacklit, plus the reach of
// the snapshot it came from.
//
// Every field is merged and disjoint, and lit, off, unobserved-within-reach and
// beyond-reach partition all of time. Holding them together rather than
// returning a bare []span is what stops a caller reconstructing "not lit" and
// calling it darkness again.
type backlight struct {
	// lit and off are the two positive observations. off has had lit
	// subtracted from it, so they are disjoint and lit is authoritative
	// wherever the two rows contradict each other.
	lit, off []span
	// covered is lit ∪ off: everything the sampler spoke about, either way.
	// The gaps inside reach are the third state.
	covered []span
	// contested is the time an ON row and an OFF row both assert, before lit
	// won. It is reported rather than hidden because the tie-break is a
	// judgement and the reader is entitled to see how much rides on it.
	contested []span
	// reach is the first to the last row of either value. Outside it the
	// snapshot is not silent about the backlight, it simply does not extend
	// that far, and the two must not be added together.
	reach span
}

// loadPresence reads all three backlight states from a knowledgeC snapshot.
//
// This duplicates a query that also lives in scripts/sleepcheck and
// scripts/spike/dayview because there is still no internal/knowledgec package.
// When one exists, this is the query that should move into it, and the
// ZVALUEINTEGER=1 version is the one that should not.
//
// A positive ON observation wins where an ON row and an OFF row overlap. The
// stream nests contradictory rows — on 2026-07-14 a single OFF row from
// 11:05:24 to 12:29:16 has a dozen short ON rows inside it — and of the two,
// "the screen was on at this moment" is the more specific claim. The reverse
// nesting also occurs and is far rarer: 10 OFF rows totalling 0.97h sit inside a
// longer ON row, where this rule discards a positive observation. That is what
// the contested figure exists to expose.
func loadPresence(path string) (backlight, error) {
	db, done, err := openSnapshot(path)
	if err != nil {
		return backlight{}, err
	}
	defer done()
	defer db.Close()

	rows, err := db.Query(`
		SELECT ZSTARTDATE, ZENDDATE, ZVALUEINTEGER FROM ZOBJECT
		WHERE ZSTREAMNAME='/display/isBacklit'
		ORDER BY ZSTARTDATE`)
	if err != nil {
		return backlight{}, fmt.Errorf("query presence from %s: %w", path, err)
	}
	defer rows.Close()

	var lit, off []span
	for rows.Next() {
		var s, e float64
		var value int
		if err := rows.Scan(&s, &e, &value); err != nil {
			return backlight{}, fmt.Errorf("read a presence row: %w", err)
		}
		row := span{
			start: time.Unix(int64(s)+coreDataEpoch, 0),
			end:   time.Unix(int64(e)+coreDataEpoch, 0),
		}
		if value == 1 {
			lit = append(lit, row)
			continue
		}
		off = append(off, row)
	}
	if err := rows.Err(); err != nil {
		return backlight{}, fmt.Errorf("read presence rows: %w", err)
	}
	if len(lit) == 0 && len(off) == 0 {
		return backlight{}, fmt.Errorf("%s holds no /display/isBacklit rows; is it a knowledgeC snapshot?", path)
	}

	b := backlight{lit: merge(lit), off: merge(off)}
	b.contested = intersect(b.lit, b.off)
	b.off = subtract(b.off, b.lit)
	b.covered = merge(append(append([]span{}, b.lit...), b.off...))
	b.reach = span{b.covered[0].start, b.covered[len(b.covered)-1].end}
	return b, nil
}

// openSnapshot opens a snapshot, transparently decompressing a .gz. Harvested
// snapshots are gzipped and SQLite cannot read those in place, so the
// alternative is asking every caller to gunzip by hand and remember to clean up.
func openSnapshot(path string) (*sql.DB, func(), error) {
	noop := func() {}
	if !strings.HasSuffix(path, ".gz") {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			return nil, noop, fmt.Errorf("open snapshot %s: %w", path, err)
		}
		return db, noop, nil
	}

	in, err := os.Open(path)
	if err != nil {
		return nil, noop, fmt.Errorf("open snapshot %s: %w", path, err)
	}
	defer in.Close()

	zr, err := gzip.NewReader(in)
	if err != nil {
		return nil, noop, fmt.Errorf("decompress snapshot %s: %w", path, err)
	}
	defer zr.Close()

	tmp, err := os.CreateTemp("", "joinday-*.db")
	if err != nil {
		return nil, noop, fmt.Errorf("stage snapshot: %w", err)
	}
	cleanup := func() { os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, zr); err != nil {
		tmp.Close()
		cleanup()
		return nil, noop, fmt.Errorf("stage snapshot %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, noop, fmt.Errorf("stage snapshot %s: %w", path, err)
	}

	db, err := sql.Open("sqlite", "file:"+tmp.Name()+"?mode=ro")
	if err != nil {
		cleanup()
		return nil, noop, fmt.Errorf("open staged snapshot: %w", err)
	}
	return db, cleanup, nil
}

// newestSnapshot picks the most recent harvested snapshot. Names are
// timestamped, so lexical order is chronological order.
func newestSnapshot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".timetattle", "snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read snapshots from %s: %w\n\nRun `timetattle harvest` first, or pass -snapshot.", dir, err)
	}

	var best string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "knowledgeC-") {
			continue
		}
		if e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s holds no snapshots; run `timetattle harvest` first", dir)
	}
	return filepath.Join(dir, best), nil
}

// loadFocus reads Biome focus intervals, separating the ones that carry a
// duration from the ones that do not.
//
// The zero-duration events are not noise and are not dropped. Each is a stop
// with no start before it, which happens when the machine wakes with an
// application already frontmost. They say a real thing happened and refuse to
// say for how long, and dating them from the record before would charge sleep
// to work.
//
// A horizon is separated from a bare stop even though both are Unbounded,
// because their observed endpoint is the opposite edge. A bare stop is a stop
// and a horizon is a start still waiting for one, so feeding a horizon to
// endsAtBareStop would let a stretch that ends at a *start* be recovered as
// though its beginning had been lost. There is one horizon in the live stream,
// so the effect is small and the reasoning would still have been wrong.
func loadFocus(dir string) (byApp map[string][]span, bare, horizons map[string][]time.Time, err error) {
	events, err := biome.NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, nil, fmt.Errorf("%s holds no focus events", dir)
	}

	byApp = map[string][]span{}
	bare = map[string][]time.Time{}
	horizons = map[string][]time.Time{}
	for _, e := range events {
		// internal/biome deliberately returns UTC; knowledgeC and the
		// manifest readers return local. Instants compare correctly across
		// locations, so the join works either way, but every printed time and
		// anything that buckets by calendar day does not. Normalising here
		// means the rest of this file cannot get it wrong. scripts/appfocus
		// solves the same problem by calling .Local() at each print site.
		// Unbounded is the unpaired case, and it is keyed on Shape rather
		// than on a zero duration: an unbounded event has only one endpoint,
		// so the instant to record is whichever one it is.
		if e.Shape == source.Unbounded {
			at := e.Anchor().Local()
			if e.Unknown == source.UnknownHorizon {
				horizons[e.Identity.App] = append(horizons[e.Identity.App], at)
				continue
			}
			bare[e.Identity.App] = append(bare[e.Identity.App], at)
			continue
		}
		byApp[e.Identity.App] = append(byApp[e.Identity.App], span{e.Start.Local(), e.End.Local()})
	}
	for app, spans := range byApp {
		byApp[app] = merge(spans)
	}
	return byApp, bare, horizons, nil
}

// loadArtifacts reads the document evidence the manifests describe.
//
// With no -manifest this is manifest.LoadAll: the built-in Motion and Blender
// manifests with anything in ~/.timetattle/manifests layered over them, which
// is the same set a real harvest would collect. Naming a file reads only that
// file, for testing a manifest before installing it.
func loadArtifacts(path string) ([]source.Event, error) {
	var specs []manifest.Source

	// Both loaders return the sources that validated alongside an error
	// describing the ones that did not, so an error here is a warning unless it
	// left nothing to read.
	var err error
	if path == "" {
		specs, err = manifest.LoadAll("")
	} else {
		var m *manifest.Manifest
		if m, err = manifest.Load(path); m != nil {
			specs = m.Sources
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "joinday: %v\n", err)
		if len(specs) == 0 {
			return nil, err
		}
	}

	var out []source.Event
	for _, spec := range specs {
		r, err := timestamped.New(spec)
		if err != nil {
			return nil, err
		}
		events, err := r.Collect(context.Background(), time.Time{})
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
}

func artifactSource(path string) string {
	if path == "" {
		return "built-in manifests + ~/.timetattle/manifests"
	}
	return path
}

// ---------------------------------------------------------------- reporting

type summary struct {
	date time.Time

	// The four backlight states, which partition the working day. presence
	// keeps its name and its old value: it is observed-ON and nothing else,
	// which is exactly what the ZVALUEINTEGER=1 query returned.
	presence   time.Duration
	off        time.Duration
	unobserved time.Duration
	beyond     time.Duration
	contested  time.Duration

	// Focus, split by the backlight state it fell in. The four parts sum to
	// focus, which report checks rather than assumes.
	focus        time.Duration
	focusInside  time.Duration
	focusDark    time.Duration
	focusUnobs   time.Duration
	focusBeyond  time.Duration
	unexplained  time.Duration
	recovered    time.Duration
	recoveredBad time.Duration

	// Where the bare stops fell. A bare stop outside a lit block has no
	// presence block to bound it, so it recovers nothing — this is the census
	// that says how often that happens.
	bareLit, bareOff, bareUnobs, bareBeyond int

	// nearMiss is unclaimed time that was NOT recovered, keyed by how many
	// whole seconds separate the stretch's end from the nearest bare stop. The
	// tolerance is the only fitted number in this tool and this is what says
	// whether it is fitted to the right value.
	nearMiss map[int]time.Duration

	artifacts int
	joined    int
}

// attributable is focus time plus the unclaimed presence that ends at a bare
// stop, which is a focus interval whose start macOS never wrote down.
//
// Bounding such an interval by presence rather than by the preceding record is
// what makes it safe. The corrections document warns that back-dating a bare
// stop to the record before it charged 5m09s of an idle machine to Safari in
// place of 2s of use; presence refuses to cover the sleep, so the same case
// bounds to 5s. The two parts are disjoint by construction — unclaimed
// presence is exactly the presence no focus interval covers — so this sum
// double-counts nothing.
//
// The three-state reading leaves this unchanged, and deliberately so. It bounds
// against observed-ON, which is the same set the two-state query returned, so
// every recovered figure is bit-identical to the ones already published. What
// the third state adds is a known error direction: a bare stop landing in an
// unobserved gap, or in recorded darkness, has no lit block to bound it and
// recovers nothing at all. It under-counts where coverage is missing, which is
// the right way to fail. printBareCensus measures how often that happens rather
// than leaving it as an expectation, and the answer over 2026-07-05 to 07-29 is
// 4 stops of 385 in an unobserved gap — so that particular worry is real and
// negligible. The under-count that is NOT negligible has a different cause and
// should not be closed; see endsAtBareStop.
func (s summary) attributable() time.Duration { return s.focus + s.recovered }

func report(w span, d time.Time, light backlight, focus map[string][]span,
	bare, horizons map[string][]time.Time, artifacts []source.Event, only string,
	tolerance time.Duration) summary {

	// The four states of the working day. Observed ON is what PRESENCE has
	// always meant. Observed OFF is positive darkness. Unobserved is the gap
	// the sampler left inside the span the snapshot reaches. Beyond is the part
	// of the day the snapshot does not extend to at all, which is a property of
	// when it was taken and not an observation of anything.
	dayPresence := clip(light.lit, w)
	dayOff := clip(light.off, w)
	dayReach := intersect([]span{w}, []span{light.reach})
	dayUnobs := subtract(dayReach, light.covered)
	dayBeyond := subtract([]span{w}, []span{light.reach})

	// Every application's spans are merged already, so the union across
	// applications is what "some application held the front" means. Focus is
	// exclusive, so overlap between applications should be nil; measuring the
	// union rather than assuming that keeps a wrong assumption from becoming a
	// wrong total.
	var all []span
	perApp := map[string][]span{}
	for app, spans := range focus {
		day := clip(spans, w)
		if len(day) == 0 {
			continue
		}
		perApp[app] = day
		all = append(all, day...)
	}
	dayFocus := merge(all)

	unexplained := subtract(dayPresence, dayFocus)

	s := summary{
		date:        d,
		presence:    total(dayPresence),
		off:         total(dayOff),
		unobserved:  total(dayUnobs),
		beyond:      total(dayBeyond),
		contested:   total(clip(light.contested, w)),
		focus:       total(dayFocus),
		focusInside: total(intersect(dayFocus, dayPresence)),
		focusDark:   total(intersect(dayFocus, dayOff)),
		focusUnobs:  total(intersect(dayFocus, dayUnobs)),
		focusBeyond: total(intersect(dayFocus, dayBeyond)),
		unexplained: total(unexplained),
	}
	s.bareLit, s.bareOff, s.bareUnobs, s.bareBeyond =
		censusBare(bare, w, dayPresence, dayOff, dayUnobs, dayBeyond)

	fmt.Printf("── %s %s ─────────────────────────────────────────\n",
		d.Format("2006-01-02"), d.Format("Monday"))
	fmt.Printf("   working day %s → %s\n\n", w.start.Format("15:04"), w.end.Format("15:04 Mon"))

	if len(dayPresence) == 0 && len(dayFocus) == 0 {
		fmt.Printf("   no evidence from any source\n\n")
		return s
	}

	// The four states are printed before anything derived from them, because
	// every figure below is only meaningful against how much of the day the
	// sampler actually spoke about.
	fmt.Printf("   BACKLIGHT — the four states of a day, which sum to %s\n", hours(w.dur()))
	fmt.Printf("   observed ON        %6s  in %d blocks  %s of the day  ← this is PRESENCE\n",
		hours(s.presence), len(dayPresence), pct(s.presence, w.dur()))
	fmt.Printf("   observed OFF       %6s  in %d blocks  %s  ← positive darkness\n",
		hours(s.off), len(dayOff), pct(s.off, w.dur()))
	fmt.Printf("   unobserved         %6s  in %d gaps    %s  ← the sampler said nothing\n",
		hours(s.unobserved), len(dayUnobs), pct(s.unobserved, w.dur()))
	fmt.Printf("   beyond the snapshot%6s  %s  ← the snapshot does not reach here\n",
		hours(s.beyond), pct(s.beyond, w.dur()))
	if s.contested > 0 {
		fmt.Printf("   contested          %6s  an ON row and an OFF row both assert; ON won\n",
			hours(s.contested))
	}
	if drift := s.presence + s.off + s.unobserved + s.beyond - w.dur(); drift != 0 {
		fmt.Printf("   !! the four states miss the day by %s — the partition is broken\n", drift)
	}
	fmt.Println()

	fmt.Printf("   app focus          %6s  %s of presence\n", hours(s.focus), pct(s.focus, s.presence))
	fmt.Printf("   focus in presence  %6s  %s of focus  ← the two sources agreeing\n",
		hours(s.focusInside), pct(s.focusInside, s.focus))
	fmt.Printf("   focus in darkness  %6s  %s of focus  ← focus while a row says the screen was OFF\n",
		hours(s.focusDark), pct(s.focusDark, s.focus))
	fmt.Printf("   focus unobserved   %6s  %s of focus  ← focus while the sampler was silent: unknown, NOT dark\n",
		hours(s.focusUnobs), pct(s.focusUnobs, s.focus))
	fmt.Printf("   focus past reach   %6s  %s of focus  ← after the snapshot was taken; says nothing\n",
		hours(s.focusBeyond), pct(s.focusBeyond, s.focus))
	fmt.Printf("   presence unclaimed %6s  %s of presence  ← lit screen, no app in front\n",
		hours(s.unexplained), pct(s.unexplained, s.presence))
	if drift := s.focusInside + s.focusDark + s.focusUnobs + s.focusBeyond - s.focus; drift != 0 {
		fmt.Printf("   !! the four focus parts miss focus by %s — the split is broken\n", drift)
	}
	fmt.Println()

	printApps(perApp, bare, horizons, w, s.presence)
	s.recovered, s.recoveredBad, s.nearMiss = printUnclaimed(unexplained, bare, light.contested, tolerance)
	s.artifacts, s.joined = printArtifacts(artifacts, focus, w)
	if only != "" {
		printIntervals(only, perApp[only])
	}
	return s
}

// censusBare says which backlight state each bare stop landed in.
//
// The bare-stop recovery bounds a stop by the lit block containing it, so only
// the first of these four can be recovered at all. The other three are the
// recovery's known under-count, counted rather than assumed.
func censusBare(bare map[string][]time.Time, w span, lit, off, unobs, beyond []span) (nLit, nOff, nUnobs, nBeyond int) {
	for _, times := range bare {
		for _, t := range times {
			if t.Before(w.start) || !t.Before(w.end) {
				continue
			}
			switch {
			case covers(lit, t):
				nLit++
			case covers(off, t):
				nOff++
			case covers(unobs, t):
				nUnobs++
			case covers(beyond, t):
				nBeyond++
			}
		}
	}
	return nLit, nOff, nUnobs, nBeyond
}

// covers reports whether any span contains an instant. Both endpoints are
// inclusive, because a bare stop lands exactly on a block edge more often than
// not — the screen coming on is what produced the wake it records.
func covers(spans []span, t time.Time) bool {
	for _, s := range spans {
		if !t.Before(s.start) && !t.After(s.end) {
			return true
		}
	}
	return false
}

func printApps(perApp map[string][]span, bare, horizons map[string][]time.Time, w span, presence time.Duration) {
	type row struct {
		app      string
		spans    []span
		total    time.Duration
		longest  time.Duration
		unpaired int
	}
	var rows []row
	for app, spans := range perApp {
		r := row{app: app, spans: spans, total: total(spans)}
		for _, s := range spans {
			if s.dur() > r.longest {
				r.longest = s.dur()
			}
		}
		rows = append(rows, r)
	}
	// The census counts every durationless event, bare stops and horizons
	// alike, because the column says what it says: this application was
	// recorded and no duration came with it.
	for _, unpaired := range []map[string][]time.Time{bare, horizons} {
		for app, times := range unpaired {
			n := 0
			for _, t := range times {
				if !t.Before(w.start) && t.Before(w.end) {
					n++
				}
			}
			if n == 0 {
				continue
			}
			found := false
			for i := range rows {
				if rows[i].app == app {
					rows[i].unpaired += n
					found = true
					break
				}
			}
			if !found {
				rows = append(rows, row{app: app, unpaired: n})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].total == rows[j].total {
			return rows[i].app < rows[j].app
		}
		return rows[i].total > rows[j].total
	})

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   APPLICATION\tFOCUS\tSHARE\tSESSIONS\tLONGEST\tNO DURATION")
	for _, r := range rows {
		if r.total < time.Minute && r.unpaired == 0 {
			continue
		}
		fmt.Fprintf(tw, "   %s\t%s\t%s\t%d\t%s\t%s\n",
			short(r.app), hours(r.total), pct(r.total, presence),
			len(r.spans), r.longest.Round(time.Second), count(r.unpaired))
	}
	tw.Flush()
	fmt.Println()
}

// printUnclaimed lists the longest stretches of lit screen that no application
// claims.
//
// The total alone is not enough to act on. A day of thirty-second slivers
// between focus intervals is bookkeeping noise; a single unbroken hour is a
// meeting, a render watched from the couch, or a hole in the stream — and
// those are the difference between app attribution being able to carry a
// billing decision and merely decorating one.
// The mechanism it tests for is specific. When the machine wakes with an
// application already frontmost, nothing "became" frontmost and macOS writes no
// start; the application's time goes unrecorded until it resigns, at which
// point a stop appears with nothing to pair it to. So an unclaimed stretch that
// ends at one of those bare stops is not missing evidence — it is a focus
// interval whose beginning was never written down, and its duration is
// recoverable in principle even though the current reader must refuse to guess.
//
// The second return is the part of the recovered time that a positive OFF row
// also claims, which is the one route by which the three-state reading could
// make this recovery over-count. A recovered stretch lies inside a merged lit
// block; a lit block can enclose an OFF row that lost the tie-break; so a
// recovered stretch can in principle include recorded darkness. It is returned
// separately rather than corrected, because the correction depends on which of
// the two contradictory rows is believed and that is not this tool's call.
func printUnclaimed(gaps []span, bare map[string][]time.Time, contested []span,
	tolerance time.Duration) (time.Duration, time.Duration, map[int]time.Duration) {

	if len(gaps) == 0 {
		return 0, 0, nil
	}
	sorted := append([]span(nil), gaps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].dur() > sorted[j].dur() })

	shown := min(len(sorted), 5)
	var tail time.Duration
	for _, g := range sorted[shown:] {
		tail += g.dur()
	}

	var explained int
	var explainedTime time.Duration
	var recovered []span
	nearMiss := map[int]time.Duration{}
	for _, g := range gaps {
		if _, ok := endsAtBareStop(g, bare, tolerance); ok {
			explained++
			explainedTime += g.dur()
			recovered = append(recovered, g)
			continue
		}
		if d, ok := nearestBareStop(g, bare); ok {
			nearMiss[int(d/time.Second)] += g.dur()
		}
	}
	disputed := total(intersect(merge(recovered), contested))

	fmt.Printf("   longest stretches of presence no application claims:\n")
	for _, g := range sorted[:shown] {
		note := ""
		if app, ok := endsAtBareStop(g, bare, tolerance); ok {
			note = fmt.Sprintf("  ← ends at a bare %s stop", short(app))
		}
		fmt.Printf("     %s–%s  %s%s\n",
			g.start.Format("15:04:05"), g.end.Format("15:04:05"), g.dur().Round(time.Second), note)
	}
	if len(sorted) > shown {
		fmt.Printf("     and %d shorter, totalling %s\n", len(sorted)-shown, tail.Round(time.Second))
	}
	fmt.Printf("     %d of %d stretches (%s of %s) end at a bare stop\n",
		explained, len(gaps), hours(explainedTime), hours(total(gaps)))
	if disputed > 0 {
		fmt.Printf("     of which %s is contested — an OFF row also claims it\n", hours(disputed))
	}
	fmt.Println()
	return explainedTime, disputed, nearMiss
}

// endsAtBareStop reports whether an unclaimed stretch finishes at a focus stop
// that had no start to pair with. A tolerance is needed because presence and
// focus come from different subsystems writing different clocks; it defaults to
// one second, which is the value every published figure was computed with.
//
// One second looks arbitrary and turns out to be load-bearing, which is why
// there is now a flag and printBareCensus prints the distribution rather than a
// comment asserting anything. Across 2026-07-05 to 07-29 the offsets cluster at
// 0s (236 stops), 1s (25), 2s (17) and 3s (15), then stop dead — one at 9s and
// nothing else inside 30s. A cliff like that is a mechanism, and at -tolerance
// 4s the recovery finds 30.22h instead of 19.19h.
//
// It should nevertheless stay at one second, because the two sides of the cliff
// are not the same event:
//
//   - At 0s the stop lands while the screen is still lit, so the stretch is an
//     application resigning focus during use. Median length 30s, and 139 of the
//     236 run under a minute.
//   - At 1-3s the stop lands just after the display goes dark: all 74 bare stops
//     that sit inside a recorded-OFF block are within 4s of that block's leading
//     edge. Median length 5m24s, only 6 of 57 under a minute, with a cluster in
//     the 5-9m band. That is the shape of a display-sleep idle timeout, so these
//     stretches END in the machine sitting untouched waiting to sleep.
//
// Presence cannot exclude that idle tail: the backlight is genuinely on until it
// goes off. So widening the tolerance recovers idle time as work, and it shows:
// at 4s, mean absolute error against Ben's four estimated days rises from 4.9%
// to 8.4%. The tight tolerance is not a mis-calibration, it is selecting the
// clean population. Recovering the rest needs an idle signal, not a wider window.
//
// Horizons must not be passed here. A horizon's observed endpoint is a start,
// so a stretch "ending" at one is a stretch running up to the beginning of a
// still-open interval, which is the ordinary case and not a lost start.
func endsAtBareStop(g span, unpaired map[string][]time.Time, tolerance time.Duration) (string, bool) {
	for app, times := range unpaired {
		for _, t := range times {
			if d := t.Sub(g.end); d > -tolerance && d < tolerance {
				return app, true
			}
		}
	}
	return "", false
}

// nearestBareStop is how far the closest bare stop is from a stretch's end,
// regardless of the tolerance. It is what turns "the tolerance is one second"
// from an assertion into a measurement.
func nearestBareStop(g span, unpaired map[string][]time.Time) (time.Duration, bool) {
	best := time.Duration(-1)
	for _, times := range unpaired {
		for _, t := range times {
			d := t.Sub(g.end)
			if d < 0 {
				d = -d
			}
			if best < 0 || d < best {
				best = d
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// printArtifacts is the join itself. An artifact and a focus interval meet
// only when they name the same application, which is why Identity.App is
// load-bearing rather than a label.
func printArtifacts(artifacts []source.Event, focus map[string][]span, w span) (int, int) {
	var day []source.Event
	for _, a := range artifacts {
		if !a.Start.Before(w.start) && a.Start.Before(w.end) {
			day = append(day, a)
		}
	}
	if len(day) == 0 {
		fmt.Printf("   no artifacts\n\n")
		return 0, 0
	}
	sort.Slice(day, func(i, j int) bool { return day[i].Start.Before(day[j].Start) })

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   ARTIFACT\tPROJECT\tOWN APP HELD FOCUS\tOTHERWISE FRONTMOST\tNEAREST OWN FOCUS")
	joined := 0
	for _, a := range day {
		host, ok := containing(focus[a.Identity.App], a.Start)
		if ok {
			joined++
			fmt.Fprintf(tw, "   %s\t%s\t%s–%s (%s)\t\t\n",
				a.Start.Format("15:04"), a.Identity.Project,
				host.start.Format("15:04:05"), host.end.Format("15:04:05"),
				host.dur().Round(time.Second))
			continue
		}
		// A miss is the join failing on its own terms, so it gets named rather
		// than counted. Two facts settle nearly every case: what did hold the
		// front instead, and how far the nearest interval for this artifact's
		// own application was. A save while another application is frontmost
		// is an autosave timer firing in the background, which is ordinary. A
		// save hours from any focus at all is not.
		fmt.Fprintf(tw, "   %s\t%s\tNO\t%s\t%s\n",
			a.Start.Format("15:04"), a.Identity.Project,
			frontmost(focus, a.Start), nearest(focus[a.Identity.App], a.Start))
	}
	tw.Flush()
	fmt.Printf("\n   %d of %d artifacts fall inside a focus interval for their own application\n\n",
		joined, len(day))
	return len(day), joined
}

func containing(spans []span, at time.Time) (span, bool) {
	for _, s := range spans {
		if !at.Before(s.start) && !at.After(s.end) {
			return s, true
		}
	}
	return span{}, false
}

// frontmost names whichever application held the front at an instant. Focus is
// exclusive, so there should be at most one; if there is more than one, that
// is worth seeing rather than hiding behind a first match.
func frontmost(focus map[string][]span, at time.Time) string {
	var found []string
	for app, spans := range focus {
		if _, ok := containing(spans, at); ok {
			found = append(found, short(app))
		}
	}
	if len(found) == 0 {
		return "nothing — no app held focus"
	}
	sort.Strings(found)
	return strings.Join(found, " + ")
}

// nearest reports the gap to the closest focus interval for the artifact's own
// application, which distinguishes "saved a few minutes after switching away"
// from "saved with the application nowhere near the front".
func nearest(spans []span, at time.Time) string {
	best := time.Duration(-1)
	var when time.Time
	for _, s := range spans {
		gap, edge := at.Sub(s.end), s.end
		if at.Before(s.start) {
			gap, edge = s.start.Sub(at), s.start
		}
		if best < 0 || gap < best {
			best, when = gap, edge
		}
	}
	if best < 0 {
		return "the app never held focus at all"
	}
	return fmt.Sprintf("%s away (%s)", best.Round(time.Second), when.Format("15:04:05 Jan 2"))
}

func printIntervals(app string, spans []span) {
	if len(spans) == 0 {
		fmt.Printf("   %s held focus at no point today\n\n", short(app))
		return
	}
	fmt.Printf("   every %s focus interval:\n", short(app))
	for _, s := range spans {
		fmt.Printf("     %s–%s  %s\n",
			s.start.Format("15:04:05"), s.end.Format("15:04:05"), s.dur().Round(time.Second))
	}
	fmt.Println()
}

func printSummary(days []summary) {
	if len(days) < 2 {
		return
	}
	fmt.Println("── across the span ──────────────────────────────────────────")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   DAY\tPRESENCE\tFOCUS\tRECOVERED\tATTRIBUTABLE\tDARK\tUNOBS\tBEYOND\tUNCLAIMED\tARTIFACTS\tJOINED")
	var p, f, r, u time.Duration
	var dk, un, by time.Duration
	var a, j int
	for _, d := range days {
		fmt.Fprintf(tw, "   %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
			d.date.Format("2006-01-02 Mon"), hours(d.presence), hours(d.focus),
			hours(d.recovered), hours(d.attributable()),
			hours(d.focusDark), hours(d.focusUnobs), hours(d.focusBeyond),
			hours(d.unexplained-d.recovered), d.artifacts, d.joined)
		p, f, r = p+d.presence, f+d.focus, r+d.recovered
		u = u + d.unexplained - d.recovered
		dk, un, by = dk+d.focusDark, un+d.focusUnobs, by+d.focusBeyond
		a, j = a+d.artifacts, j+d.joined
	}
	fmt.Fprintf(tw, "   total\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
		hours(p), hours(f), hours(r), hours(f+r),
		hours(dk), hours(un), hours(by), hours(u), a, j)
	tw.Flush()
	fmt.Println("\n   PRESENCE is the backlight observed ON, which is what this column has")
	fmt.Println("   always meant; RECOVERED, ATTRIBUTABLE and UNCLAIMED are unchanged and are")
	fmt.Println("   directly comparable with project/private/2026-07-29-join-validation.md §3.")
	fmt.Println("   ATTRIBUTABLE is focus plus unclaimed presence ending at a bare stop —")
	fmt.Println("   focus intervals whose start macOS never wrote, bounded by presence")
	fmt.Println("   rather than by the preceding record. UNCLAIMED is what remains.")
	fmt.Println()
	fmt.Println("   What used to be one OUTSIDE column is now three, because \"not observed lit\"")
	fmt.Println("   was three different claims. DARK is focus while a row asserts isBacklit=0 —")
	fmt.Println("   the only one that is evidence of anything. UNOBS is focus while the sampler")
	fmt.Println("   emitted no row of either value: unknown, and reading it as darkness deletes")
	fmt.Println("   real work. BEYOND is focus after the snapshot was taken, which is the age of")
	fmt.Println("   the snapshot and not a finding. OUTSIDE was their sum.")
	fmt.Println()

	fmt.Println("── the backlight, four states per day ───────────────────────")
	bw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(bw, "   DAY\tON\tOFF\tUNOBSERVED\tBEYOND\tCONTESTED\tDAY LENGTH")
	var on, offT, unob, beyo, cont, elapsed time.Duration
	for _, d := range days {
		day := d.presence + d.off + d.unobserved + d.beyond
		fmt.Fprintf(bw, "   %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.date.Format("2006-01-02 Mon"), hours(d.presence), hours(d.off),
			hours(d.unobserved), hours(d.beyond), hours(d.contested), hours(day))
		on, offT, unob = on+d.presence, offT+d.off, unob+d.unobserved
		beyo, cont, elapsed = beyo+d.beyond, cont+d.contested, elapsed+day
	}
	fmt.Fprintf(bw, "   total\t%s\t%s\t%s\t%s\t%s\t%s\n",
		hours(on), hours(offT), hours(unob), hours(beyo), hours(cont), hours(elapsed))
	bw.Flush()
	fmt.Println("\n   The first four columns partition the day, so they sum to DAY LENGTH.")
	fmt.Println("   CONTESTED is time an ON row and an OFF row both assert; ON wins, so it is")
	fmt.Println("   already counted inside ON and is shown to say how much rides on that")
	fmt.Println("   tie-break. Reconcile ON, OFF and UNOBSERVED against scripts/sleepcheck,")
	fmt.Println("   which reports the same three over the window both sources share.")
}

// printBareCensus answers whether the bare-stop recovery is still sound now that
// the backlight has three states rather than two.
//
// The recovery bounds a bare stop by the lit block containing it, so a stop
// falling anywhere else recovers nothing. That is a floor, not a bias: it loses
// time it cannot bound and never invents any. This prints the split so the claim
// is measured rather than assumed, and so a change in the sampler's behaviour
// would show up as the ON column shrinking.
func printBareCensus(days []summary, tolerance time.Duration) {
	var lit, off, unobs, beyond int
	var disputed time.Duration
	missed := map[int]time.Duration{}
	for _, d := range days {
		lit, off = lit+d.bareLit, off+d.bareOff
		unobs, beyond = unobs+d.bareUnobs, beyond+d.bareBeyond
		disputed += d.recoveredBad
		for secs, dur := range d.nearMiss {
			missed[secs] += dur
		}
	}
	all := lit + off + unobs + beyond
	if all == 0 {
		return
	}

	fmt.Println("\n── where the bare stops fell ────────────────────────────────")
	fmt.Printf("   in observed ON     %6d  %s  ← a lit block bounds these; recoverable\n",
		lit, share(lit, all))
	fmt.Printf("   in observed OFF    %6d  %s  ← recorded darkness; nothing to bound against\n",
		off, share(off, all))
	fmt.Printf("   in unobserved      %6d  %s  ← the sampler was silent; nothing to bound against\n",
		unobs, share(unobs, all))
	fmt.Printf("   beyond the snapshot%6d  %s  ← outside its reach\n",
		beyond, share(beyond, all))
	fmt.Printf("\n   Only the first can be recovered. The other %d are the recovery's\n", all-lit)
	fmt.Printf("   under-count, and it is an under-count rather than an error: with no lit\n")
	fmt.Printf("   block to bound against, the stretch is not recovered at all.\n")
	if disputed > 0 {
		fmt.Printf("\n   %s of recovered time is contested — it sits inside a lit block that\n", hours(disputed))
		fmt.Printf("   encloses an OFF row. This is the one direction in which three states\n")
		fmt.Printf("   could make the recovery over-count, and its size is the whole of the risk.\n")
	} else {
		fmt.Printf("\n   No recovered time is contested by an OFF row, so the three-state reading\n")
		fmt.Printf("   introduces no over-count here at all.\n")
	}

	printNearMiss(missed, tolerance)
}

// printNearMiss shows how much unclaimed time sits just outside the tolerance.
//
// The tolerance is the only fitted number in this tool, so leaving it unmeasured
// would be leaving the one thing that could quietly move every ATTRIBUTABLE
// figure. If the histogram falls away smoothly there is no right answer and the
// current value is as good as any; if it has a cliff, the cliff is the answer
// and the value should sit on the far side of it.
func printNearMiss(missed map[int]time.Duration, tolerance time.Duration) {
	if len(missed) == 0 {
		return
	}
	var secs []int
	for s := range missed {
		secs = append(secs, s)
	}
	sort.Ints(secs)

	fmt.Printf("\n   UNRECOVERED unclaimed time by its distance to the nearest bare stop\n")
	fmt.Printf("   (tolerance is %v; anything inside it was recovered above)\n", tolerance)
	var cum time.Duration
	var far time.Duration
	for _, s := range secs {
		if s > 30 {
			far += missed[s]
			continue
		}
		cum += missed[s]
		fmt.Printf("     %3ds  %6s   cumulative %s\n", s, hours(missed[s]), hours(cum))
	}
	if far > 0 {
		fmt.Printf("     >30s  %6s   ← no bare stop nearby; genuinely unexplained presence\n", hours(far))
	}
	fmt.Printf("\n   A cliff here is a mechanism, not a tail: a stop landing 1-3s after the\n")
	fmt.Printf("   display goes dark is a stop at sleep, and its stretch ends in the machine\n")
	fmt.Printf("   sitting idle waiting to sleep. Presence cannot exclude that — the backlight\n")
	fmt.Printf("   is on until it is off — so widening -tolerance recovers idle as work and\n")
	fmt.Printf("   agreement with ground truth gets worse, not better. See endsAtBareStop.\n")
}

func share(n, of int) string {
	if of == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}

// ---------------------------------------------------------------- formatting

func hours(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fh", d.Hours())
}

func pct(part, whole time.Duration) string {
	if whole == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(part)/float64(whole))
}

func count(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprint(n)
}

// short drops the reverse-DNS prefix so a table of bundle identifiers can be
// read at a glance. The full identifier is what joins, so this is display only.
func short(bundle string) string {
	if i := strings.LastIndex(bundle, "."); i >= 0 && i < len(bundle)-1 {
		return bundle[i+1:]
	}
	return bundle
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "joinday: "+format+"\n", args...)
	os.Exit(1)
}
