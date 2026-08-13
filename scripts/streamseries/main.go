// Command streamseries reads the preserved knowledgeC snapshot corpus as what
// it actually is: a TIME SERIES OF THE SOURCE'S OWN STATE.
//
// # Why it exists
//
// Every other instrument in this repository measures what Timetattle STORED.
// The store cannot answer questions about the source, because a source can
// stall, roll or trim between two harvests and the store only ever sees the
// result. The snapshots can: each one is a copy of macOS's own database at a
// known instant, so lining them up shows the source changing.
//
// It was written to settle three questions that ad-hoc SQL had been getting
// wrong, and it answers all three by measurement rather than by argument:
//
//   - Does /app/usage's ~10,000-row cap ROLL or FREEZE? The oldest-row column
//     is the direct test. A rolling window's oldest row moves forward; a frozen
//     table's does not.
//
//   - Is 10,000 a hard ceiling? It is not. It is the FLOOR a periodic trim
//     reduces the table to, and the table is routinely larger. A harvest that
//     once reported 10,577 rows was reading a genuinely larger table, not
//     miscounting.
//
//   - How long does a row really survive? The EVICTION DEADLINE section names a
//     specific row that was present in one snapshot and gone from the next, so
//     the answer is a row's observed death rather than an inference from a
//     retained span. The VOLUME section then divides the cap by the daily rate,
//     which is what turns "the floor is volume-based" from a caveat into a
//     number: the deadline shortens exactly when there is most to lose.
//
// # Three readings, never two
//
// Every stream in every snapshot is one of PresenceRows, PresenceNoRows or
// PresenceUnreadable, and no analysis here crosses a hole. Two frozen readings
// either side of a snapshot that would not open are not a witnessed freeze,
// and a snapshot naming no such stream is not a stream with nothing in it.
//
// # What it cannot see, and says so
//
// The corpus begins when snapshots began, not when the source began. Every
// extreme it prints is the extreme WITHIN THE OBSERVED CORPUS, and a longer
// freeze or a shorter window may sit before the first file. The report repeats
// that beside the figures rather than in a footnote, because a clipped window
// hides a worst case silently.
//
// Snapshots also sit anywhere from eleven seconds to two days apart, so a
// freeze shorter than the interval between two of them is invisible here. "No
// freeze found" means none was witnessed, not that none occurred.
//
// Usage:
//
//	go run ./scripts/streamseries                       # the four streams that matter
//	go run ./scripts/streamseries -table                # the whole per-snapshot series
//	go run ./scripts/streamseries -days                 # the per-day histogram behind the rate
//	go run ./scripts/streamseries -streams /app/usage   # one stream
//	go run ./scripts/streamseries -all                  # every stream in the corpus
//	go run ./scripts/streamseries -dir path/to/dir -cap 10000
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bensyverson/timetattle/internal/snapshot"
)

// defaultStreams are the four Timetattle reads. /app/usage is the subject;
// the other three are the control, and without them a stall in the first is
// indistinguishable from a machine that was switched off.
var defaultStreams = []string{
	"/app/usage",
	"/display/isBacklit",
	"/device/isLocked",
	"/media/nowPlaying",
}

func main() {
	dir := flag.String("dir", "", "snapshot directory (default: ~/.timetattle/snapshots)")
	streams := flag.String("streams", strings.Join(defaultStreams, ","), "comma-separated streams")
	all := flag.Bool("all", false, "every stream found in the newest readable snapshot")
	table := flag.Bool("table", false, "print the whole per-snapshot series")
	days := flag.Bool("days", false, "print the per-day row histogram behind the volume figure")
	cap := flag.Int("cap", 10000, "nominal row cap to test /app/usage against")
	flag.Parse()

	opt := options{table: *table, days: *days, cap: *cap}
	if err := run(*dir, splitStreams(*streams), *all, opt, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func splitStreams(s string) []string {
	var out []string
	for f := range strings.SplitSeq(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// options are the reporting choices, kept together so adding one does not
// change every signature between main and the report.
type options struct {
	table bool
	days  bool
	cap   int
}

func run(dir string, want []string, all bool, opt options, out io.Writer) error {
	if dir == "" {
		d, err := snapshot.DefaultDir()
		if err != nil {
			return err
		}
		dir = d
	}
	paths, err := filepath.Glob(filepath.Join(dir, "knowledgeC-*.db.gz"))
	if err != nil {
		return fmt.Errorf("list snapshots in %s: %w", dir, err)
	}
	if len(paths) == 0 {
		// Nothing to read is not a series with nothing in it.
		return fmt.Errorf("no knowledgeC snapshots in %s; there is no series to read", dir)
	}
	sort.Strings(paths) // the stamped names sort chronologically

	if all {
		found, err := allStreams(paths)
		if err != nil {
			return err
		}
		want = found
	}
	if len(want) == 0 {
		return fmt.Errorf("no streams named; there is nothing to measure")
	}

	// series[stream] is that stream's observations in capture order, holding
	// one entry per snapshot INCLUDING the ones that would not open. A hole in
	// the corpus has to stay visible in the series or the analysis will close
	// it silently.
	series := map[string][]Observation{}
	var unreadable []string
	for _, p := range paths {
		obs, err := readSnapshot(p, want)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", filepath.Base(p), err))
			captured, _ := captureTime(p)
			for _, s := range want {
				series[s] = append(series[s], Observation{
					Snapshot: filepath.Base(p), Captured: captured,
					Stream: s, Presence: PresenceUnreadable,
				})
			}
			continue
		}
		for _, o := range obs {
			series[o.Stream] = append(series[o.Stream], o)
		}
	}

	reportCorpus(out, dir, paths, unreadable)
	for _, s := range want {
		reportStream(out, s, series[s], opt)
	}
	return nil
}

// allStreams is the union of every stream name in every readable snapshot, not
// just the newest: a stream macOS retired part way through the corpus is still
// part of the series, and taking the newest snapshot's list alone would drop it
// without saying so.
func allStreams(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	var readable int
	for _, p := range paths {
		names, err := streamNames(p)
		if err != nil {
			continue
		}
		readable++
		for _, n := range names {
			seen[n] = struct{}{}
		}
	}
	if readable == 0 {
		return nil, fmt.Errorf("no snapshot in the corpus could be read, so no stream list could be built")
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func reportCorpus(out io.Writer, dir string, paths []string, unreadable []string) {
	first, _ := captureTime(paths[0])
	last, _ := captureTime(paths[len(paths)-1])
	fmt.Fprintf(out, "SNAPSHOT SERIES  %s\n", dir)
	fmt.Fprintf(out, "  %d snapshots, %s to %s (local)\n",
		len(paths), first.Format(time.DateTime), last.Format(time.DateTime))
	fmt.Fprintf(out, "  The corpus begins when SNAPSHOTS began, not when the source began.\n")
	fmt.Fprintf(out, "  Every extreme below is the extreme within these files. A longer stall or\n")
	fmt.Fprintf(out, "  a shorter retention window may sit before the first of them, unobserved.\n")
	if len(unreadable) > 0 {
		fmt.Fprintf(out, "  %d snapshots could NOT be read, and are carried through the series as\n", len(unreadable))
		fmt.Fprintf(out, "  holes rather than dropped, so nothing is measured across them:\n")
		for _, u := range unreadable {
			fmt.Fprintf(out, "    %s\n", u)
		}
	} else {
		fmt.Fprintf(out, "  All %d were readable.\n", len(paths))
	}
	fmt.Fprintln(out)
}

func reportStream(out io.Writer, stream string, obs []Observation, opt options) {
	fmt.Fprintf(out, "== %s ==\n", stream)

	var rows, noRows, unread int
	for _, o := range obs {
		switch o.Presence {
		case PresenceRows:
			rows++
		case PresenceNoRows:
			noRows++
		default:
			unread++
		}
	}
	fmt.Fprintf(out, "  %d snapshots held rows for it, %d held none, %d could not be read.\n",
		rows, noRows, unread)
	if noRows > 0 {
		fmt.Fprintf(out, "  \"held none\" means the snapshot was read and names no such row. It does\n")
		fmt.Fprintf(out, "  NOT distinguish an empty stream from one this macOS does not have.\n")
	}
	if rows == 0 {
		fmt.Fprintf(out, "  Nothing was observed, so nothing below is measured.\n\n")
		return
	}

	if opt.table {
		printTable(out, obs)
	}

	reportRoll(out, obs)
	reportCap(out, obs, opt.cap)
	prof := reportCadence(out, obs)
	reportStalls(out, obs, prof)
	reportHorizon(out, obs, opt)
	fmt.Fprintln(out)
}

func printTable(out io.Writer, obs []Observation) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  captured\trows\toldest\tnewest\thorizon")
	for _, o := range obs {
		if o.Presence != PresenceRows {
			fmt.Fprintf(w, "  %s\t%s\t\t\t\n", o.Captured.Format(time.DateTime), o.Presence)
			continue
		}
		undated := ""
		if o.Undated > 0 {
			undated = fmt.Sprintf(" (%d undated)", o.Undated)
		}
		fmt.Fprintf(w, "  %s\t%d%s\t%s\t%s\t%.2fd\n",
			o.Captured.Format(time.DateTime), o.Rows, undated,
			o.Oldest.Local().Format(time.DateTime), o.Newest.Local().Format(time.DateTime),
			o.Captured.Sub(o.Oldest).Hours()/24)
	}
	w.Flush()
	fmt.Fprintln(out)
}

// reportRoll is the direct roll-versus-freeze test.
func reportRoll(out io.Writer, obs []Observation) {
	tr := trims(obs)
	fmt.Fprintf(out, "  ROLL TEST — does the oldest row move forward?\n")
	if len(tr) == 0 {
		fmt.Fprintf(out, "    NO. The oldest row never advanced across any consecutive readable\n")
		fmt.Fprintf(out, "    pair, so nothing was observed leaving this stream.\n")
		return
	}
	var total time.Duration
	for _, t := range tr {
		total += t.Discarded()
	}
	first, last := firstLastRows(obs)
	fmt.Fprintf(out, "    YES. The oldest row advanced on %d of %d consecutive readable pairs,\n",
		len(tr), readablePairs(obs))
	fmt.Fprintf(out, "    from %s to %s — %.1f days of retained span discarded in total.\n",
		first.Local().Format(time.DateTime), last.Local().Format(time.DateTime),
		total.Hours()/24)
	fmt.Fprintf(out, "    A window whose oldest row moves forward is ROLLING, not frozen.\n")
	fmt.Fprintf(out, "    Largest single eviction: ")
	big := tr[0]
	for _, t := range tr {
		if t.Discarded() > big.Discarded() {
			big = t
		}
	}
	fmt.Fprintf(out, "%.1f days of span, %d rows -> %d rows,\n", big.Discarded().Hours()/24,
		big.RowsBefore, big.RowsAfter)
	fmt.Fprintf(out, "    somewhere between %s and %s.\n",
		big.Before.Format(time.DateTime), big.After.Format(time.DateTime))
}

func reportCap(out io.Writer, obs []Observation, cap int) {
	var counts []int
	atCap, over := 0, 0
	high := 0
	var highAt Observation
	for _, o := range obs {
		if o.Presence != PresenceRows {
			continue
		}
		counts = append(counts, o.Rows)
		if o.Rows == cap {
			atCap++
		}
		if o.Rows > cap {
			over++
		}
		if o.Rows > high {
			high, highAt = o.Rows, o
		}
	}
	sort.Ints(counts)
	fmt.Fprintf(out, "  ROW COUNT against a nominal cap of %d\n", cap)
	fmt.Fprintf(out, "    min %d, median %d, max %d across %d readings\n",
		counts[0], counts[len(counts)/2], counts[len(counts)-1], len(counts))
	fmt.Fprintf(out, "    %d readings sat at EXACTLY %d; %d sat ABOVE it, the largest %d at %s.\n",
		atCap, cap, over, high, highAt.Captured.Format(time.DateTime))
	if over > 0 {
		fmt.Fprintf(out, "    %d is therefore NOT a ceiling. It is the FLOOR a periodic trim reduces\n", cap)
		fmt.Fprintf(out, "    the table to, and the table grows past it between trims. A harvest that\n")
		fmt.Fprintf(out, "    reads more than %d rows is reading a genuinely larger table.\n", cap)
	}
}

// reportCadence measures the stream's own idea of normal, from the union of
// every distinct row instant the corpus ever held.
//
// It exists because a freeze means nothing on its own. A stream that writes a
// row every seven seconds and one that writes a row a week both stop looking
// identical in a table of newest timestamps, and the only thing that separates
// "it has stopped" from "it has nothing to say" is the stream's own spacing.
func reportCadence(out io.Writer, obs []Observation) gapProfile {
	var all []time.Time
	for _, o := range obs {
		if o.Presence == PresenceRows {
			all = append(all, o.Starts...)
		}
	}
	p := profileGaps(all)
	fmt.Fprintf(out, "  CADENCE — how this stream is normally spaced\n")
	if p.Gaps == 0 {
		fmt.Fprintf(out, "    %d distinct instants across the corpus, so no gap was measured. A\n", p.Instants)
		fmt.Fprintf(out, "    freeze in this stream cannot be judged against anything.\n")
		return p
	}
	fmt.Fprintf(out, "    %d distinct instants, %d gaps between them\n", p.Instants, p.Gaps)
	fmt.Fprintf(out, "    median %s, p95 %s, p99 %s, longest %s\n",
		round(p.Median), round(p.P95), round(p.P99), round(p.Max))
	fmt.Fprintf(out, "    gaps over 1h: %d, over 12h: %d, over 24h: %d\n",
		countOver(p, time.Hour), countOver(p, 12*time.Hour), countOver(p, 24*time.Hour))
	fmt.Fprintf(out, "    This is measured from the SOURCE's own rows across every snapshot, not\n")
	fmt.Fprintf(out, "    from what was stored, so it is not clipped to when collection began.\n")
	fmt.Fprintf(out, "    CAUTION: the longest gap is not by itself a picture of normal. If this\n")
	fmt.Fprintf(out, "    stream has ever stalled, its longest gap IS that stall, and judging a\n")
	fmt.Fprintf(out, "    new freeze against it compares a fault to a fault. p99 is the tail of\n")
	fmt.Fprintf(out, "    ordinary behaviour; the counts above say how much sits beyond it.\n")
	return p
}

func reportStalls(out io.Writer, obs []Observation, prof gapProfile) {
	st := stalls(obs)
	fmt.Fprintf(out, "  FREEZES — runs of snapshots whose newest row did not move\n")
	if len(st) == 0 {
		fmt.Fprintf(out, "    None witnessed. That is not the same as none having occurred: a freeze\n")
		fmt.Fprintf(out, "    shorter than the interval between two snapshots leaves no trace here.\n")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "    frozen at\tfirst seen\tlast seen\twitnessed\tsnapshots\tvs p99\tvs longest")
	for _, s := range st {
		p99, longest := "n/a", "n/a"
		if prof.P99 > 0 {
			p99 = fmt.Sprintf("%.0fx", s.Observed().Seconds()/prof.P99.Seconds())
		}
		if prof.Max > 0 {
			longest = fmt.Sprintf("%.2fx", s.Observed().Seconds()/prof.Max.Seconds())
		}
		fmt.Fprintf(w, "    %s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Newest.Local().Format(time.DateTime),
			s.FirstSeen.Format(time.DateTime), s.LastSeen.Format(time.DateTime),
			s.Observed().Round(time.Minute), s.Snapshots, p99, longest)
	}
	w.Flush()
	fmt.Fprintf(out, "    \"witnessed\" spans the first and last snapshot that SAW the freeze, so the\n")
	fmt.Fprintf(out, "    real freeze started before the first and ended after the last.\n")
	fmt.Fprintf(out, "    Read the p99 column. The \"vs longest\" column is shown so its weakness is\n")
	fmt.Fprintf(out, "    visible rather than hidden: when a stream's longest gap is one of the\n")
	fmt.Fprintf(out, "    freezes in this very table, that ratio is a fault divided by a fault and\n")
	fmt.Fprintf(out, "    it will always look reassuring.\n")
}

func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute)
	case d >= time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(time.Millisecond)
	}
}

// reportHorizon is the perishability measurement.
func reportHorizon(out io.Writer, obs []Observation, opt options) {
	h := summarize(horizons(obs))
	fmt.Fprintf(out, "  RETAINED SPAN — age of the oldest row the source still held\n")
	if h.N == 0 {
		fmt.Fprintf(out, "    Nothing dated was observed, so no span was measured.\n")
		return
	}
	fmt.Fprintf(out, "    shortest %.2f days, median %.2f, longest %.2f, across %d readings\n",
		h.Min.Hours()/24, h.Median.Hours()/24, h.Max.Hours()/24, h.N)
	fmt.Fprintf(out, "    This bounds retention from BELOW only: rows of that age were seen alive.\n")
	fmt.Fprintf(out, "    It is NOT the deadline, and reading it as one is the trap this section\n")
	fmt.Fprintf(out, "    was rebuilt to avoid — a quiet stream holds nothing old because nothing\n")
	fmt.Fprintf(out, "    old was ever written, which looks identical to aggressive eviction.\n")

	// The deadline proper. Only a row observed present and then absent
	// establishes an upper bound, and only a named row does it honestly.
	ev := evictions(obs)
	fmt.Fprintf(out, "  EVICTION DEADLINE — rows observed present in one snapshot and gone from the next\n")
	if len(ev) == 0 {
		fmt.Fprintf(out, "    No row was ever observed leaving this stream, so the corpus sets NO\n")
		fmt.Fprintf(out, "    upper bound on how long a row survives. That is an absence of evidence,\n")
		fmt.Fprintf(out, "    not evidence that rows are kept for ever.\n")
	} else {
		tightest := ev[0]
		var rows int
		for _, e := range ev {
			rows += e.Rows
			if e.GoneBy() < tightest.GoneBy() {
				tightest = e
			}
		}
		fmt.Fprintf(out, "    %d evictions observed, %d rows discarded in total.\n", len(ev), rows)
		fmt.Fprintf(out, "    TIGHTEST: the row starting %s was still present at %s\n",
			tightest.Youngest.Local().Format(time.DateTime), tightest.Before.Format(time.DateTime))
		fmt.Fprintf(out, "    (aged %.2f days) and gone by %s (aged %.2f days).\n",
			tightest.SurvivedAtLeast().Hours()/24, tightest.After.Format(time.DateTime),
			tightest.GoneBy().Hours()/24)
		fmt.Fprintf(out, "    So a row of this stream has been SEEN to die at %.2f days. Anything not\n",
			tightest.GoneBy().Hours()/24)
		fmt.Fprintf(out, "    harvested inside that window is at risk, and the figure is an upper\n")
		fmt.Fprintf(out, "    bound on the guarantee rather than a promise: a busier stretch than any\n")
		fmt.Fprintf(out, "    in this corpus would shorten it further.\n")
	}

	// The volume reading. A count-capped stream's deadline is the cap divided
	// by the arrival rate, so the busiest day on record is the shortest
	// deadline the corpus can justify.
	days := map[string]int{}
	for _, o := range obs {
		if o.Presence != PresenceRows {
			continue
		}
		for d, n := range o.Days {
			if n > days[d] {
				days[d] = n
			}
		}
	}
	day, peak := busiestDay(days)
	if opt.days {
		printDays(out, days)
	}
	if peak == 0 {
		fmt.Fprintf(out, "    No dated rows, so no arrival rate and no volume deadline.\n")
		return
	}
	fmt.Fprintf(out, "  VOLUME — what the cap costs when the stream is busy\n")
	typical := medianCount(days)
	fmt.Fprintf(out, "    Typical day: %d rows. Busiest day on record: %s, %d rows.\n", typical, day, peak)

	// The reconciliation. If a count cap is what evicts rows, then cap divided
	// by the typical daily rate has to land near the retained span the corpus
	// actually shows. Two figures that agree for an understood reason are
	// worth more than either alone; two that nobody has subtracted are not.
	if d, ok := impliedHorizon(opt.cap, float64(typical)); ok && h.N > 0 {
		fmt.Fprintf(out, "    CROSS-CHECK: %d rows at %d a day is %.2f days, against a median retained\n",
			opt.cap, typical, d.Hours()/24)
		fmt.Fprintf(out, "    span of %.2f days actually observed — a difference of %.2f days. Those\n",
			h.Median.Hours()/24, (h.Median-d).Hours()/24)
		fmt.Fprintf(out, "    agreeing is what says the ROW CAP, not an age limit, is what evicts\n")
		fmt.Fprintf(out, "    from this stream. A stream expiring by age would not track it at all.\n")
	}
	if d, ok := impliedHorizon(opt.cap, float64(peak)); ok {
		fmt.Fprintf(out, "    AT THE BUSIEST RATE a %d-row cap holds only %.2f days.\n",
			opt.cap, d.Hours()/24)
		fmt.Fprintf(out, "    This is the figure a harvest schedule has to beat, and it is a\n")
		fmt.Fprintf(out, "    property of the WORKLOAD rather than of the calendar: the deadline\n")
		fmt.Fprintf(out, "    shortens exactly when there is most to lose.\n")
	}
	// A day at the edge of the corpus is clipped by the retention window
	// itself, so its count understates the true rate.
	fmt.Fprintf(out, "    Days at either edge of a snapshot's retained span are partial, so the\n")
	fmt.Fprintf(out, "    peak is a LOWER bound on the busiest real day.\n")
}

// printDays is the histogram behind the peak, so the peak is auditable rather
// than asserted. Each day shows the LARGEST count any single snapshot held for
// it, which is the best view the corpus has of that day before eviction began
// taking rows out of it.
func printDays(out io.Writer, days map[string]int) {
	keys := make([]string, 0, len(days))
	for d := range days {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "    day (UTC)\trows")
	for _, d := range keys {
		fmt.Fprintf(w, "    %s\t%d\n", d, days[d])
	}
	w.Flush()
}

func readablePairs(obs []Observation) int {
	n := 0
	for i := 1; i < len(obs); i++ {
		if obs[i-1].Presence == PresenceRows && obs[i].Presence == PresenceRows {
			n++
		}
	}
	return n
}

func firstLastRows(obs []Observation) (time.Time, time.Time) {
	var first, last time.Time
	for _, o := range obs {
		if o.Presence != PresenceRows || o.Oldest.IsZero() {
			continue
		}
		if first.IsZero() {
			first = o.Oldest
		}
		last = o.Oldest
	}
	return first, last
}
