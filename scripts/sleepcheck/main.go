// Command sleepcheck tests the sleep/wake asymmetry that every long focus
// interval in this project depends on.
//
// # The claim under test
//
// internal/biome treats an unpaired stop as a machine waking with an
// application already frontmost, and refuses to give it a duration. That rests
// on a specific belief about what macOS writes across a sleep:
//
//	at sleep, a stop is written for whatever held focus
//	at wake, no start is written, because nothing *became* frontmost
//
// The second half is well evidenced: 382 bare stops exist, and one was
// cross-checked against knowledgeC on 2026-07-20 (see
// project/private/2026-07-28-source-corrections.md §3).
//
// The first half has never been tested, and it is the load-bearing one. If a
// stop is *not* always written at sleep, then a focus interval can begin before
// a sleep and end after it, and its duration silently includes the sleep. That
// is the same inflation the bare-stop rule exists to prevent, arriving through
// the other door — and unlike a bare stop it is invisible, because the interval
// looks perfectly ordinary. Every "longest session" figure and every long
// stretch in a day view would be suspect.
//
// # Why this is not scripts/joinday
//
// joinday reports focus-outside-presence as a single per-day total, computed by
// subtracting merged presence from merged focus. That total answers "do the two
// sources agree", which is what it was built for, but it cannot distinguish the
// two very different things that produce it:
//
//   - An interval with darkness in its INTERIOR — lit at the start, dark in the
//     middle, lit at the end. Only a sleep the interval failed to close can
//     produce this shape. This is the falsifier.
//   - An interval that is dark at one edge, or dark throughout. This is a
//     presence blind spot, not a pairing failure. The backlight can be off
//     while the machine is genuinely in use: a display deliberately dimmed
//     while a long agent run works, an external display, or a Screen Sharing
//     session. This time is work and must not be discarded.
//
// Merging destroys interval identity, so both collapse into the same number.
// Here every focus interval is tested on its own and classified.
//
// # Two independent tests
//
// TEST A uses presence, and is a screen for candidates rather than a verdict.
// It assumes a dark screen implies a sleep, and that assumption is known to be
// false in at least one real case: Ben sometimes dims the display and leaves a
// long autonomous agent run working, which is genuine work at a dark screen.
// So a Test A hit is a question, not a finding.
//
// TEST B needs only Biome, and is the sound one. A bare stop is by construction
// a wake artifact: it exists because the machine resumed with an app frontmost.
// So a bare stop falling strictly inside another application's paired interval
// is a contradiction on its face — that interval claims its app held the front
// across a moment when a different app was frontmost at wake. No interpretation
// of the backlight is involved.
//
// A disagreement between them is the informative case. B firing where A does
// not means presence missed a sleep. A firing where B does not is most likely
// the dimmed display, and would need a third source to settle.
//
// # Reading the result
//
// Zero interior violations does not prove the asymmetry holds universally. It
// proves it held everywhere it could have been caught in the window both
// sources cover, which is the strongest statement the available data supports.
// The counts are printed rather than a pass/fail, because the useful question
// is how much time hangs on the answer.
//
// Like scripts/joinday and scripts/appfocus this is analysis rather than
// product behaviour, and is exempt from the project's red/green TDD rule.
//
// Usage:
//
//	go run ./scripts/sleepcheck
//	go run ./scripts/sleepcheck -min 5m         # only report violations over 5m
//	go run ./scripts/sleepcheck -blindspots     # itemise the edge/dark cases too
//	go run ./scripts/sleepcheck -snapshot path/to/knowledgeC.db.gz
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
	"github.com/bensyverson/timetattle/internal/source"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		snap       = flag.String("snapshot", "", "knowledgeC snapshot (default: newest harvested)")
		dir        = flag.String("biome", "", "Biome AppUsage stream directory (default: the live one)")
		min        = flag.Duration("min", 30*time.Second, "ignore violations shorter than this")
		blindspots = flag.Bool("blindspots", false, "itemise edge-dark and wholly-dark intervals")
		top        = flag.Int("top", 20, "how many offenders to list per category")
	)
	flag.Parse()

	if *snap == "" {
		found, err := newestSnapshot()
		if err != nil {
			fatal("%v", err)
		}
		*snap = found
	}
	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("determine home directory: %v", err)
		}
		*dir = filepath.Join(home, "Library", "Biome", "streams", "restricted",
			"ScreenTime.AppUsage", "local")
	}

	if err := run(*snap, *dir, *min, *blindspots, *top); err != nil {
		fatal("%v", err)
	}
}

func run(snap, dir string, min time.Duration, blindspots bool, top int) error {
	lit, off, err := loadPresence(snap)
	if err != nil {
		return err
	}
	lit = merge(lit)

	// A positive ON observation beats an overlapping OFF row. The stream nests
	// contradictory rows — on 2026-07-14 a single OFF row from 11:05:24 to
	// 12:29:16 has a dozen short ON rows inside it — and of the two, "the
	// screen was on at this moment" is the more specific claim.
	off = subtract(merge(off), lit)

	// What the sampler covered at all, either way. The gaps in this are the
	// third state, and they are the reason this tool was rewritten.
	covered := merge(append(append([]span{}, lit...), off...))

	intervals, bare, horizons, err := loadFocus(dir)
	if err != nil {
		return err
	}

	// Both sources are clipped to the window they genuinely share. Biome is
	// read live and runs to this moment; the snapshot's presence stops when it
	// was taken. Without this, every focus interval since the last harvest
	// looks like focus in darkness — on 2026-07-29 that alone accounted for 7h
	// of apparent violation in joinday's OUTSIDE column, which is an artefact
	// of snapshot age and not a finding about sleep.
	w, ok := overlap(covered, intervals)
	if !ok {
		return fmt.Errorf("presence and focus windows do not overlap; is %s older than the Biome stream?", snap)
	}

	fmt.Printf("presence  %s\n", snap)
	fmt.Printf("focus     %s\n", dir)
	fmt.Printf("window    %s → %s  (%s, the span both sources cover)\n\n",
		w.start.Format("2006-01-02 15:04:05"), w.end.Format("2006-01-02 15:04:05"),
		hours(w.dur()))

	inWindow := clipIntervals(intervals, w)
	litIn := clip(lit, w)
	offIn := clip(off, w)
	unknown := subtract([]span{w}, covered)

	fmt.Printf("PAIRING CENSUS\n")
	fmt.Printf("   paired intervals   %6d  in window, of %d in the whole stream\n",
		countIntervals(inWindow), countIntervals(intervals))
	fmt.Printf("   bare stops         %6d  in window, of %d  ← wake artefacts\n",
		countIn(bare, w), countInstants(bare))
	fmt.Printf("   horizons           %6d  in window, of %d  ← still-open, resolve next harvest\n\n",
		countIn(horizons, w), countInstants(horizons))

	// The long OFF stretches carry the argument. Nobody keeps working through
	// eleven hours of recorded darkness, so a positive OFF observation of this
	// length is a machine sleep, and these are the cases where a failure to
	// write a stop at sleep would be most visible and least deniable.
	const certain = 30 * time.Minute
	var sure []span
	for _, d := range offIn {
		if d.dur() >= certain {
			sure = append(sure, d)
		}
	}

	fmt.Printf("BACKLIGHT CENSUS — three states, not two\n")
	fmt.Printf("   observed ON        %6s  in %d blocks (%s of the window)\n",
		hours(total(litIn)), len(litIn), pct(total(litIn), w.dur()))
	fmt.Printf("   observed OFF       %6s  in %d blocks (%s)  ← positive darkness\n",
		hours(total(offIn)), len(offIn), pct(total(offIn), w.dur()))
	fmt.Printf("   UNOBSERVED         %6s  in %d gaps   (%s)  ← the sampler said nothing\n",
		hours(total(unknown)), len(unknown), pct(total(unknown), w.dur()))
	fmt.Printf("   of the OFF blocks, %d run over %v, totalling %s; longest %s\n\n",
		len(sure), certain, hours(total(sure)), longest(offIn).Round(time.Second))

	// Test A is now run against positive darkness only. Running it against
	// "not observed ON", as the two-state query forced, mixes a real
	// observation with the absence of one.
	interior, edge, whole := classify(inWindow, offIn)
	unkInterior, unkEdge, unkWhole := classify(inWindow, unknown)

	reportA(interior, min, top, sure)
	reportB(inWindow, bare, min, top)
	reportBlindspots(edge, whole, min, top, blindspots)
	reportUnobserved(unkInterior, unkEdge, unkWhole, unknown, min, top, blindspots)
	verdict(interior, inWindow, bare, unkInterior, unkEdge, unkWhole)
	return nil
}

// ------------------------------------------------------------------ tests

// occurrence is one focus interval that overlaps darkness, with the dark part
// that overlapped it. The interval is kept whole rather than trimmed, because
// the shape of the overlap against the interval's own edges is the finding.
type occurrence struct {
	app      string
	interval span
	dark     span
}

func (o occurrence) String() string {
	return fmt.Sprintf("%-16s %s–%s (%s)  dark %s–%s (%s)",
		short(o.app),
		o.interval.start.Format("01-02 15:04:05"), o.interval.end.Format("15:04:05"),
		o.interval.dur().Round(time.Second),
		o.dark.start.Format("15:04:05"), o.dark.end.Format("15:04:05"),
		o.dark.dur().Round(time.Second))
}

// classify splits every focus interval that meets darkness into the three
// shapes that matter. Only the first is evidence about pairing.
//
// The distinction is strict inequality at both edges. A dark span touching an
// interval's start or end is an interval that began or ended around a display
// state change, which is ordinary. A dark span with lit time on both sides of
// it, inside one interval, is an interval that outlived a sleep.
// The second argument is the condition being looked for — observed darkness, or
// unobserved time — and the overlap is an INTERSECTION with it. Subtracting a
// lit set would work only while the model had two states; with three, "not
// observed lit" is precisely the conflation this tool exists to undo.
func classify(intervals map[string][]span, condition []span) (interior, edge, whole []occurrence) {
	for app, spans := range intervals {
		for _, f := range spans {
			gaps := intersect([]span{f}, condition)
			if len(gaps) == 0 {
				continue
			}
			for _, g := range gaps {
				o := occurrence{app: app, interval: f, dark: g}
				switch {
				case !g.start.After(f.start) && !g.end.Before(f.end):
					whole = append(whole, o)
				case g.start.After(f.start) && g.end.Before(f.end):
					interior = append(interior, o)
				default:
					edge = append(edge, o)
				}
			}
		}
	}
	return interior, edge, whole
}

func reportA(interior []occurrence, min time.Duration, top int, sure []span) {
	fmt.Printf("TEST A — darkness strictly inside a paired interval (presence-based screen)\n")

	over := filter(interior, min)
	fmt.Printf("   candidates         %6d  over %s, of %d at any duration\n",
		len(over), min, len(interior))
	fmt.Printf("   time enclosed      %6s  darkness an interval would charge to an app\n",
		hours(darkTotal(interior)))

	if len(interior) == 0 {
		fmt.Printf("\n   No paired interval encloses darkness. Every interval that meets a dark\n")
		fmt.Printf("   stretch meets it at an edge, which is what closing at sleep looks like.\n")
		fmt.Printf("\n   This also covers the case TEST B structurally cannot see — a sleep with\n")
		fmt.Printf("   the SAME app frontmost on both sides, which leaves no bare stop and so no\n")
		fmt.Printf("   contradiction to find. %d of the dark stretches here are long enough to be\n", len(sure))
		fmt.Printf("   certain sleeps (%s in total), and not one of them falls inside an\n", hours(total(sure)))
		fmt.Printf("   interval. An unclosed overnight sleep is the most visible failure this\n")
		fmt.Printf("   data could contain, and it does not occur.\n\n")
		return
	}
	fmt.Println()
	list(over, top)
}

// reportB is the Biome-only test. It needs no view of the backlight at all: a
// bare stop is a wake artefact by construction, so one inside another app's
// interval means that interval spanned a wake, and therefore a sleep.
func reportB(intervals map[string][]span, bare map[string][]time.Time, min time.Duration, top int) {
	fmt.Printf("TEST B — a bare stop strictly inside another app's paired interval (Biome-only falsifier)\n")

	var found []occurrence
	for app, spans := range intervals {
		for _, f := range spans {
			for bareApp, stops := range bare {
				for _, t := range stops {
					if !t.After(f.start) || !t.Before(f.end) {
						continue
					}
					// A bare stop for the same app that owns the interval is
					// not a contradiction: the app can regain focus, sleep,
					// and resign again. The conflict is a *different* app
					// having been frontmost at wake.
					if bareApp == app {
						continue
					}
					found = append(found, occurrence{
						app:      app,
						interval: f,
						dark:     span{t, t},
					})
				}
			}
		}
	}

	over := filterByInterval(found, min)
	fmt.Printf("   violations         %6d  over %s, of %d at any duration\n",
		len(over), min, len(found))

	if len(found) == 0 {
		fmt.Printf("\n   No bare stop falls inside another application's interval. The two tests\n")
		fmt.Printf("   agree, and this one assumed nothing about what a dark screen means.\n\n")
		return
	}
	fmt.Println()
	sort.Slice(over, func(i, j int) bool {
		return over[i].interval.dur() > over[j].interval.dur()
	})
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i, o := range over {
		if i >= top {
			fmt.Fprintf(w, "   \tand %d more\n", len(over)-top)
			break
		}
		fmt.Fprintf(w, "   %s\t%s–%s (%s)\twake at %s\n", short(o.app),
			o.interval.start.Format("01-02 15:04:05"), o.interval.end.Format("15:04:05"),
			o.interval.dur().Round(time.Second), o.dark.start.Format("15:04:05"))
	}
	w.Flush()
	fmt.Println()
}

// reportBlindspots accounts for the darkness that is NOT evidence about
// pairing, so that the presence-based total in joinday's OUTSIDE column is
// fully explained rather than left as a residue.
func reportBlindspots(edge, whole []occurrence, min time.Duration, top int, itemise bool) {
	// Counted per (interval, dark gap) pair rather than per interval, because an
	// interval can be dark at both edges with a lit blip between — the backlight
	// flickering inside one focus session. Both gaps are reported.
	fmt.Printf("NOT EVIDENCE ABOUT PAIRING — presence blind spots\n")
	fmt.Printf("   dark at an edge    %6d  gaps, totalling %s\n", len(edge), hours(darkTotal(edge)))
	fmt.Printf("   dark throughout    %6d  intervals, totalling %s\n", len(whole), hours(darkTotal(whole)))
	fmt.Printf("\n   Focus recorded while the backlight was observed OFF. The machine can be\n")
	fmt.Printf("   genuinely in use with a dark built-in panel — an external display, Screen\n")
	fmt.Printf("   Sharing, a display dimmed while a long agent run works — so this is time to\n")
	fmt.Printf("   examine rather than discard. It says nothing about pairing either way.\n\n")

	byApp := map[string]time.Duration{}
	byDay := map[string]time.Duration{}
	for _, o := range append(append([]occurrence{}, edge...), whole...) {
		byApp[o.app] += o.dark.dur()
		byDay[o.dark.start.Format("2006-01-02")] += o.dark.dur()
	}
	fmt.Printf("   worst days:\n")
	printRanked(byDay, 5)
	fmt.Printf("   by application:\n")
	printRanked(byApp, 5)
	fmt.Println()

	if itemise {
		fmt.Printf("   every wholly-dark interval over %s:\n", min)
		list(filter(whole, min), top)
		fmt.Printf("   every edge-dark interval over %s:\n", min)
		list(filter(edge, min), top)
	}
}

// reportUnobserved is focus recorded while the backlight sampler was silent.
// This is the category the two-state query could not express, and it is where
// the 2026-07-14 case actually belongs.
func reportUnobserved(interior, edge, whole []occurrence, gaps []span, min time.Duration, top int, itemise bool) {
	all := append(append(append([]occurrence{}, interior...), edge...), whole...)

	fmt.Printf("FOCUS WHILE THE BACKLIGHT WAS UNOBSERVED\n")
	fmt.Printf("   inside an interval %6d  gaps, totalling %s\n", len(interior), hours(darkTotal(interior)))
	fmt.Printf("   at an edge         %6d  gaps, totalling %s\n", len(edge), hours(darkTotal(edge)))
	fmt.Printf("   throughout         %6d  intervals, totalling %s\n", len(whole), hours(darkTotal(whole)))
	fmt.Printf("\n   Biome recorded focus and the backlight stream recorded nothing at all —\n")
	fmt.Printf("   neither ON nor OFF. This is NOT darkness and must not be read as absence.\n")
	fmt.Printf("   Treating it as screen-off would delete real work; treating it as screen-on\n")
	fmt.Printf("   would invent presence. It is a third state and the store needs to hold it.\n\n")

	// Every gap is listed rather than ranked. There are few enough to read, and
	// the question of what causes the sampler to fall silent is open — a list
	// of the actual occasions is what a future investigation needs.
	fmt.Printf("   every gap in backlight coverage, with the focus recorded during it:\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "     GAP\tLENGTH\tFOCUS IN IT\tAPPS")
	for _, g := range gaps {
		var busy time.Duration
		seen := map[string]bool{}
		var apps []string
		for _, o := range all {
			if o.dark.start.Before(g.start) || o.dark.end.After(g.end) {
				continue
			}
			busy += o.dark.dur()
			if !seen[o.app] {
				seen[o.app] = true
				apps = append(apps, short(o.app))
			}
		}
		sort.Strings(apps)
		label := "—"
		if len(apps) > 0 {
			label = strings.Join(apps, " ")
		}
		fmt.Fprintf(w, "     %s–%s\t%s\t%s\t%s\n",
			g.start.Format("01-02 15:04:05"), g.end.Format("15:04:05"),
			g.dur().Round(time.Second), hours(busy), label)
	}
	w.Flush()
	fmt.Println()

	if itemise {
		fmt.Printf("   every unobserved stretch over %s:\n", min)
		list(filter(all, min), top)
	}
}

func verdict(interior []occurrence, intervals map[string][]span, bare map[string][]time.Time,
	unkInterior, unkEdge, unkWhole []occurrence) {
	fmt.Printf("VERDICT\n")

	n := 0
	for _, spans := range intervals {
		n += len(spans)
	}
	nBare := 0
	for _, stops := range bare {
		nBare += len(stops)
	}

	if len(interior) == 0 {
		fmt.Printf("   The asymmetry holds everywhere it could have been caught: %d paired\n", n)
		fmt.Printf("   intervals tested, %d bare stops accounted for, and no interval encloses\n", nBare)
		fmt.Printf("   a sleep. Durations taken from paired intervals do not include sleep, so\n")
		fmt.Printf("   internal/biome's refusal to date a bare stop is the only repair needed.\n")
		fmt.Printf("\n   This is not proof for all time. It is proof over the window above, which\n")
		fmt.Printf("   is every day both sources still retain. It is now measured against\n")
		fmt.Printf("   POSITIVE darkness rather than against 'not observed lit', which is the\n")
		fmt.Printf("   stronger form of the test.\n")

		unk := darkTotal(unkInterior) + darkTotal(unkEdge) + darkTotal(unkWhole)
		fmt.Printf("\n   Note what this does NOT license. %s of focus falls where the backlight\n", hours(unk))
		fmt.Printf("   was never sampled. That time is unknown, not dark: intersecting focus with\n")
		fmt.Printf("   observed presence would silently delete it, and the largest single case is\n")
		fmt.Printf("   90 minutes of dense interactive app switching. Presence is a floor for\n")
		fmt.Printf("   engaged time, not a ceiling — for this reason rather than the one first\n")
		fmt.Printf("   recorded. Do not define engaged time as an intersection with it.\n")
		return
	}
	fmt.Printf("   The asymmetry does NOT hold universally. %d paired intervals enclose\n", len(interior))
	fmt.Printf("   darkness, totalling %s that would be charged to an application.\n", hours(darkTotal(interior)))
	fmt.Printf("   Any duration built on a paired interval is an upper bound, not a\n")
	fmt.Printf("   measurement, and the store's canonical view must bound focus by presence\n")
	fmt.Printf("   rather than trust the interval it was given.\n")
}

// ------------------------------------------------------------------ helpers

func filter(in []occurrence, min time.Duration) []occurrence {
	var out []occurrence
	for _, o := range in {
		if o.dark.dur() >= min {
			out = append(out, o)
		}
	}
	return out
}

func filterByInterval(in []occurrence, min time.Duration) []occurrence {
	var out []occurrence
	for _, o := range in {
		if o.interval.dur() >= min {
			out = append(out, o)
		}
	}
	return out
}

func darkTotal(in []occurrence) time.Duration {
	var d time.Duration
	for _, o := range in {
		d += o.dark.dur()
	}
	return d
}

func list(in []occurrence, top int) {
	if len(in) == 0 {
		fmt.Printf("     none\n\n")
		return
	}
	sort.Slice(in, func(i, j int) bool { return in[i].dark.dur() > in[j].dark.dur() })
	for i, o := range in {
		if i >= top {
			fmt.Printf("     and %d more\n", len(in)-top)
			break
		}
		fmt.Printf("     %s\n", o)
	}
	fmt.Println()
}

func printRanked(m map[string]time.Duration, n int) {
	type kv struct {
		k string
		d time.Duration
	}
	var all []kv
	for k, d := range m {
		all = append(all, kv{k, d})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].d > all[j].d })
	if len(all) == 0 {
		fmt.Printf("     none\n")
		return
	}
	for i, e := range all {
		if i >= n {
			break
		}
		fmt.Printf("     %-20s %s\n", short(e.k), hours(e.d))
	}
}

// overlap is the window both sources cover: from the later of the two starts to
// the earlier of the two ends.
func overlap(presence []span, intervals map[string][]span) (span, bool) {
	if len(presence) == 0 {
		return span{}, false
	}
	pStart, pEnd := presence[0].start, presence[len(presence)-1].end

	var fStart, fEnd time.Time
	for _, spans := range intervals {
		for _, s := range spans {
			if fStart.IsZero() || s.start.Before(fStart) {
				fStart = s.start
			}
			if s.end.After(fEnd) {
				fEnd = s.end
			}
		}
	}
	if fStart.IsZero() {
		return span{}, false
	}

	w := span{laterOf(pStart, fStart), earlierOf(pEnd, fEnd)}
	if !w.start.Before(w.end) {
		return span{}, false
	}
	return w, true
}

func clipIntervals(in map[string][]span, w span) map[string][]span {
	out := map[string][]span{}
	for app, spans := range in {
		// Only intervals wholly inside the window are kept. Clipping one that
		// straddles the edge would manufacture an artificial endpoint, and an
		// artificial endpoint is exactly what this tool is looking for.
		for _, s := range spans {
			if !s.start.Before(w.start) && !s.end.After(w.end) {
				out[app] = append(out[app], s)
			}
		}
	}
	return out
}

func countIntervals(m map[string][]span) int {
	n := 0
	for _, spans := range m {
		n += len(spans)
	}
	return n
}

func countInstants(m map[string][]time.Time) int {
	n := 0
	for _, ts := range m {
		n += len(ts)
	}
	return n
}

func countIn(m map[string][]time.Time, w span) int {
	n := 0
	for _, ts := range m {
		for _, t := range ts {
			if !t.Before(w.start) && !t.After(w.end) {
				n++
			}
		}
	}
	return n
}

func longest(in []span) time.Duration {
	var d time.Duration
	for _, s := range in {
		if s.dur() > d {
			d = s.dur()
		}
	}
	return d
}

type span struct{ start, end time.Time }

func (s span) dur() time.Duration { return s.end.Sub(s.start) }

// merge normalises a set of spans into disjoint, sorted spans. knowledgeC
// writes nested /display/isBacklit rows, so subtracting unmerged presence would
// leave phantom darkness inside a lit period — see
// project/private/2026-07-29-join-validation.md §5.
func merge(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]span{}, in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })

	out := []span{sorted[0]}
	for _, s := range sorted[1:] {
		last := &out[len(out)-1]
		if !s.start.After(last.end) {
			if s.end.After(last.end) {
				last.end = s.end
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

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

// intersect returns the parts of a that are also in b. Both must be sorted; b
// must also be disjoint, which is why every caller passes a merged set.
func intersect(a, b []span) []span {
	var out []span
	for _, s := range a {
		for _, t := range b {
			if !t.end.After(s.start) {
				continue
			}
			if !t.start.Before(s.end) {
				break
			}
			start, end := laterOf(s.start, t.start), earlierOf(s.end, t.end)
			if start.Before(end) {
				out = append(out, span{start, end})
			}
		}
	}
	return out
}

// clip trims spans to a window, keeping the parts that fall inside it.
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

func hours(d time.Duration) string { return fmt.Sprintf("%.2fh", d.Hours()) }

func pct(part, whole time.Duration) string {
	if whole == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", 100*float64(part)/float64(whole))
}

func short(bundle string) string {
	if i := strings.LastIndex(bundle, "."); i >= 0 && i < len(bundle)-1 {
		return bundle[i+1:]
	}
	return bundle
}

// ------------------------------------------------------------------ loading

// loadPresence reads the backlight stream, keeping the OFF rows as well as the
// ON rows.
//
// Every other query in this repository — scripts/spike/dayview,
// scripts/joinday, and the first version of this tool — selects
// `ZVALUEINTEGER=1` and treats everything else as darkness. That conflates two
// completely different things. The stream carries 434 explicit `isBacklit=0`
// rows totalling 434.75h, which are *observations* that the screen was off;
// what is left over after both values is time the sampler said nothing about at
// all, which is an absence of evidence and not evidence of absence.
//
// The distinction is load-bearing rather than pedantic. On 2026-07-14 the
// stream stops emitting between 12:35:20 and 14:02:24 — no row of either value
// covers it — while /notification/usage keeps writing and Biome records dense
// app switching throughout. Reading that gap as "screen off" would delete 87
// minutes of work; reading it as "unknown" does not.
//
// When internal/knowledgec exists (task ycBP1) this is the query that should
// move into it, and the two-state version is the one that should not.
func loadPresence(path string) (lit, off []span, err error) {
	db, done, err := openSnapshot(path)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	defer db.Close()

	rows, err := db.Query(`
		SELECT ZSTARTDATE, ZENDDATE, ZVALUEINTEGER FROM ZOBJECT
		WHERE ZSTREAMNAME='/display/isBacklit'
		ORDER BY ZSTARTDATE`)
	if err != nil {
		return nil, nil, fmt.Errorf("query presence from %s: %w", path, err)
	}
	defer rows.Close()

	// Core Data stores seconds since 2001-01-01 UTC.
	const epoch = 978307200

	for rows.Next() {
		var start, end float64
		var value int
		if err := rows.Scan(&start, &end, &value); err != nil {
			return nil, nil, fmt.Errorf("read a presence row: %w", err)
		}
		s := span{
			time.Unix(int64(start+epoch), 0).Local(),
			time.Unix(int64(end+epoch), 0).Local(),
		}
		if value == 1 {
			lit = append(lit, s)
			continue
		}
		off = append(off, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read presence rows: %w", err)
	}
	if len(lit) == 0 && len(off) == 0 {
		return nil, nil, fmt.Errorf("%s holds no /display/isBacklit rows; is it a knowledgeC snapshot?", path)
	}
	return lit, off, nil
}

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

	tmp, err := os.CreateTemp("", "sleepcheck-*.db")
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

// loadFocus separates the three event shapes internal/biome produces. Unlike
// joinday's loader this does NOT merge the intervals: merging fuses adjacent
// intervals of the same app into one span, which would manufacture exactly the
// long interval this tool exists to look for.
func loadFocus(dir string) (intervals map[string][]span, bare, horizons map[string][]time.Time, err error) {
	events, err := biome.NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, nil, fmt.Errorf("%s holds no focus events", dir)
	}

	intervals = map[string][]span{}
	bare = map[string][]time.Time{}
	horizons = map[string][]time.Time{}
	for _, e := range events {
		// internal/biome returns UTC; knowledgeC returns local. Instants
		// compare correctly either way, but every printed time does not.
		if e.Shape == source.Unbounded {
			switch e.Unknown {
			case source.UnknownHorizon:
				horizons[e.Identity.App] = append(horizons[e.Identity.App], e.Anchor().Local())
			default:
				bare[e.Identity.App] = append(bare[e.Identity.App], e.Anchor().Local())
			}
			continue
		}
		intervals[e.Identity.App] = append(intervals[e.Identity.App],
			span{e.Start.Local(), e.End.Local()})
	}
	for app, spans := range intervals {
		sort.Slice(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })
		intervals[app] = spans
	}
	return intervals, bare, horizons, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sleepcheck: "+format+"\n", args...)
	os.Exit(1)
}
