// Command harvestgap measures what the harvester actually did, from
// harvest_run alone, and is the tool behind the figures in
// project/2026-08-12-harvest-gaps.md.
//
// # Why it exists
//
// The gap figures that drove the freshness work were produced by SQL typed at a
// prompt and recorded as "min(started_at) per harvest_run grouped to the
// minute". They do not reproduce from that description: grouping to the minute
// gives 94 gaps and a mean of 3.16h, and the published 35 gaps / 8.49h mean /
// 54.6438h max reproduce only under a TEN-minute bucket — substr(started_at,1,15),
// a minute truncation written one character short. Nothing about the conclusion
// changed, but a figure whose stated method gives a different answer is not a
// measurement, so the method now lives here where it can be re-run and argued
// with. -grid 10m reproduces the original figures exactly, for the reconciliation.
//
// # What an invocation is, and why it is not a clock bucket
//
// harvest_run holds one row per (invocation, source), and nothing in the schema
// says which invocation a row belongs to. A fixed clock grid is the obvious
// stand-in and it is wrong in both directions at once: it SPLITS an invocation
// that straddles a boundary, and it MERGES two invocations that fall inside one.
// Both happen in the live store. The 2026-08-07 invocation ran 19:06:58 to
// 19:10:38 and the 10-minute grid cut it at 19:10, which is why the worst
// recorded outage read 54.6438h from 19:10:22 rather than its true 54.7004h from
// 19:06:58; four invocations on 2026-08-01 and two on 2026-08-03 were merged.
//
// The store can answer the question exactly. Collect runs its sources in
// sequence in one process, so within an invocation each row's started_at equals
// the previous row's finished_at to the microsecond. An invocation is therefore
// a maximal chain of rows joined by that continuity, and the -join threshold
// that defines "continuous" is not a guess: the tool prints the widest delta it
// absorbed and the narrowest it split on, so a reader can see whether the
// threshold sits in an empty band or in the middle of the data.
//
// # Two gap measures, kept apart
//
// EXPOSURE is the previous invocation's end to the next one's start: how long
// no source was being read, which is what the retention floors bite on.
// CADENCE is start to start: what a launchd StartInterval controls. They differ
// by the invocation's own duration — under six minutes here — and the tool
// reports both rather than picking, because the two answer different questions
// and the difference moves gaps across a threshold.
//
// # What this cannot see, and says so
//
// A missed invocation leaves NOTHING. harvest_run records runs that reached the
// point of writing a row, so a gap is consistent with launchd never firing, with
// the machine being asleep, and with a process that died before its first write,
// and no arithmetic here separates those. A FAILED source is the opposite case:
// the harvester ran, the row exists, and one source was lost. The report keeps
// absence, failure and an unfinished row as three distinct observations, because
// collapsing any two of them into "the schedule was fine" is a bug this
// repository has shipped three times.
//
// Nothing is windowed by default. Passing -since or -until makes the tool
// measure the excluded population too and print the widest gap the window hides,
// because a clipped window silently concealing the worst case is the other bug
// this repository has shipped twice.
//
//	go run ./scripts/harvestgap                    # the live store, read-only, unwindowed
//	go run ./scripts/harvestgap -grid 10m          # reproduce the original bucketed figures
//	go run ./scripts/harvestgap -until 2026-08-12T03:00:00Z
//	go run ./scripts/harvestgap -over 4h,8h,12h -top 20
//
// Like scripts/runningcheck and scripts/sleepcheck this is analysis rather than
// product behaviour. The store is opened read-only and nothing is written.
//
// The FAILURES section prints error text straight out of the store, and that
// text carries absolute filesystem paths. Do not paste it into anything that
// ships.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/store"

	// The store package exposes runs but not an event's first_seen_run, and
	// -recovered needs exactly that column. Rather than widen the store's API
	// for one analysis script, this opens a second read-only handle of its own,
	// as scripts/sleepcheck already does for the snapshots it reads.
	_ "modernc.org/sqlite"
)

// A batch is one grouping of harvest_run rows. Under -join it is an invocation,
// recovered from row continuity. Under -grid it is a clock bucket, which is a
// different and worse thing wearing the same shape; the report never calls a
// grid batch an invocation.
type batch struct {
	Start, End time.Time
	Rows       int
	Failed     int
	Unfinished int
}

// A gap is the interval between two batches. It keeps three timestamps rather
// than one duration so that both measures stay derivable from the same object
// and cannot drift apart.
type gap struct {
	PrevStart, PrevEnd, Start time.Time
}

// Exposure is how long no source was being read.
func (g gap) Exposure() time.Duration { return g.Start.Sub(g.PrevEnd) }

// Cadence is start to start, which is what a periodic timer controls.
func (g gap) Cadence() time.Duration { return g.Start.Sub(g.PrevStart) }

func main() {
	var (
		path  = flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
		join  = flag.Duration("join", time.Second, "rows this close to the previous row's finish are the same invocation")
		gsize = flag.Duration("grid", 0, "group by a fixed clock bucket of this size instead of by continuity")
		since = flag.String("since", "", "ignore rows started before this RFC3339 time (default: no lower bound)")
		until = flag.String("until", "", "ignore rows started after this RFC3339 time (default: no upper bound)")
		overs = flag.String("over", "12h,17h,24h", "comma-separated thresholds to count gaps against")
		nom   = flag.Duration("interval", 12*time.Hour, "the interval the installed schedule is believed to promise")
		top   = flag.Int("top", 10, "how many of the widest gaps to list")
		rec   = flag.Bool("recovered", false, "for each of those gaps, count the evidence that survived it")
	)
	flag.Parse()

	if err := run(*path, *join, *gsize, *since, *until, *overs, *nom, *top, *rec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, join, gsize time.Duration, since, until, overs string, nom time.Duration, top int, rec bool) error {
	var err error
	if path == "" {
		if path, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	from, err := parseBound(since, "-since")
	if err != nil {
		return err
	}
	to, err := parseBound(until, "-until")
	if err != nil {
		return err
	}
	thresholds, err := parseThresholds(overs)
	if err != nil {
		return err
	}

	st, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer st.Close()

	// Always read everything. The window is applied afterwards, so that the
	// population a window excludes can be measured rather than assumed empty.
	all, err := st.Runs(context.Background(), store.Filter{})
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("%s holds no harvest_run rows; that is an empty record, not a clean one", path)
	}
	kept := within(all, from, to)

	group := func(runs []store.Run) []batch { return cluster(runs, join) }
	unit, units := "invocation", "invocations"
	rule := fmt.Sprintf("a chain of rows each starting within %s of the previous one finishing", short(join))
	if gsize > 0 {
		group = func(runs []store.Run) []batch { return grid(runs, gsize) }
		unit, units = "clock batch", "clock batches"
		rule = fmt.Sprintf("started_at truncated to a fixed %s grid — NOT an invocation; see the package comment", short(gsize))
	}

	header(path, all, kept, from, to, unit, rule)
	if gsize == 0 {
		separationReport(kept, join)
	}

	batches := group(kept)
	batchReport(batches, units)

	gaps := gapsOf(batches)
	gapReport(gaps, thresholds, units)
	widest(gaps, top)

	if rec {
		if err := recoveredReport(path, gaps, top); err != nil {
			return err
		}
	}

	if !from.IsZero() || !to.IsZero() {
		hidden(group(all), gaps)
	}
	failures(kept)
	absence(gaps, nom, unit)
	return nil
}

func parseBound(s, flagName string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %q is not an RFC3339 time: %w", flagName, s, err)
	}
	return t, nil
}

func parseThresholds(s string) ([]time.Duration, error) {
	var out []time.Duration
	for f := range strings.SplitSeq(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		d, err := time.ParseDuration(f)
		if err != nil {
			return nil, fmt.Errorf("-over: %q is not a duration: %w", f, err)
		}
		out = append(out, d)
	}
	slices.Sort(out)
	return out, nil
}

func within(runs []store.Run, from, to time.Time) []store.Run {
	var out []store.Run
	for _, r := range runs {
		if !from.IsZero() && r.StartedAt.Before(from) {
			continue
		}
		if !to.IsZero() && r.StartedAt.After(to) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// end is the last thing a row observed. A row still marked running has no
// finished_at, and its started_at is the last moment anything is known to have
// been happening — not a zero time, and not a reason to drop the row.
func end(r store.Run) time.Time {
	if r.FinishedAt.IsZero() {
		return r.StartedAt
	}
	return r.FinishedAt
}

func sorted(runs []store.Run) []store.Run {
	out := append([]store.Run(nil), runs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// cluster recovers invocations from row continuity. A row opens a new
// invocation when it starts more than join after everything before it has
// finished; the running maximum matters rather than the previous row's end,
// because one slow source must not be cut off by a fast one that started later.
func cluster(runs []store.Run, join time.Duration) []batch {
	var out []batch
	for _, r := range sorted(runs) {
		if n := len(out); n > 0 && !r.StartedAt.After(out[n-1].End.Add(join)) {
			b := &out[n-1]
			if e := end(r); e.After(b.End) {
				b.End = e
			}
			b.Rows++
			count(b, r)
			continue
		}
		b := batch{Start: r.StartedAt, End: end(r), Rows: 1}
		count(&b, r)
		out = append(out, b)
	}
	return out
}

// grid groups by a fixed clock bucket. It exists to reproduce figures that were
// produced this way and to show what the method costs; it is not the default
// for the reasons in the package comment.
func grid(runs []store.Run, size time.Duration) []batch {
	var out []batch
	var key time.Time
	for _, r := range sorted(runs) {
		k := r.StartedAt.Truncate(size)
		if n := len(out); n > 0 && k.Equal(key) {
			b := &out[n-1]
			if e := end(r); e.After(b.End) {
				b.End = e
			}
			b.Rows++
			count(b, r)
			continue
		}
		key = k
		b := batch{Start: r.StartedAt, End: end(r), Rows: 1}
		count(&b, r)
		out = append(out, b)
	}
	return out
}

// count keeps failed and unfinished rows countable after grouping. They are
// separate fields and not one "not ok" total: a source that failed is a loss
// with a recorded reason, and a row left running is a harvest that never
// reported either way.
func count(b *batch, r store.Run) {
	switch r.Status {
	case store.StatusFailed:
		b.Failed++
	case store.StatusRunning:
		b.Unfinished++
	}
}

func gapsOf(b []batch) []gap {
	var out []gap
	for i := 1; i < len(b); i++ {
		out = append(out, gap{PrevStart: b[i-1].Start, PrevEnd: b[i-1].End, Start: b[i].Start})
	}
	return out
}

// separation is the evidence that -join is not doing arbitrary work. It returns
// the widest delta the threshold absorbed into an invocation and the narrowest
// it split on. If those two are close, the threshold is inventing the answer.
func separation(runs []store.Run, join time.Duration) (absorbed, split time.Duration) {
	var prev time.Time
	first := true
	for _, r := range sorted(runs) {
		if !first {
			d := r.StartedAt.Sub(prev)
			switch {
			case d <= join:
				if d > absorbed {
					absorbed = d
				}
			case split == 0 || d < split:
				split = d
			}
		}
		if e := end(r); first || e.After(prev) {
			prev = e
		}
		first = false
	}
	return absorbed, split
}

// sourceCount is how much evidence one source contributed to a total, biggest
// first when sorted.
type sourceCount struct {
	Source string
	N      int
}

// recovered counts the evidence a gap did NOT cost: events whose own timestamp
// falls inside the gap and which no harvest had seen until after the gap ended.
//
// It reads the `event` table rather than the canonical_event view on purpose.
// The question is provenance — what did we see and when did we first see it —
// which is what the table is for; the view collapses an observation a later
// harvest completed and would answer a different question.
//
// This is what separates latency from loss, and it can only ever be evidence of
// the first. A source whose evidence expired unharvested leaves nothing to
// count, so a large number here proves survival and a small one proves nothing
// at all. See the document for why that asymmetry matters for mtime sources.
func recovered(db *sql.DB, g gap) ([]sourceCount, int, error) {
	rows, err := db.Query(`
		SELECT e.source_id, COUNT(*)
		FROM event e JOIN harvest_run r ON r.id = e.first_seen_run
		WHERE e.started_at >= ? AND e.started_at < ? AND r.started_at >= ?
		GROUP BY e.source_id`,
		stamp(g.PrevEnd), stamp(g.Start), stamp(g.Start))
	if err != nil {
		return nil, 0, fmt.Errorf("count evidence recovered across the gap ending %s: %w", stamp(g.Start), err)
	}
	defer rows.Close()

	var out []sourceCount
	total := 0
	for rows.Next() {
		var c sourceCount
		if err := rows.Scan(&c.Source, &c.N); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
		total += c.N
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N == out[j].N {
			return out[i].Source < out[j].Source
		}
		return out[i].N > out[j].N
	})
	return out, total, nil
}

func over(gaps []gap, limit time.Duration, measure func(gap) time.Duration) int {
	n := 0
	for _, g := range gaps {
		if measure(g) > limit {
			n++
		}
	}
	return n
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func mean(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	var total time.Duration
	for _, v := range d {
		total += v
	}
	return total / time.Duration(len(d))
}

func measured(gaps []gap, f func(gap) time.Duration) []time.Duration {
	out := make([]time.Duration, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, f(g))
	}
	return out
}

// excluded compares a windowed set of gaps against the whole population and
// returns how many were left out and the widest of those by exposure. A window
// that cannot say what it left out is the failure mode scripts/sleepcheck
// shipped: it clipped to the span two sources shared, reported "1.88h of gaps,
// largest 1h27m", and had dropped a 6h23m gap that ended 26 seconds before its
// window opened.
func excluded(all, kept []gap) (int, gap) {
	type key struct{ prev, start int64 }
	in := make(map[key]bool, len(kept))
	for _, g := range kept {
		in[key{g.PrevStart.UnixNano(), g.Start.UnixNano()}] = true
	}
	n := 0
	var widest gap
	for _, g := range all {
		if in[key{g.PrevStart.UnixNano(), g.Start.UnixNano()}] {
			continue
		}
		n++
		if g.Exposure() > widest.Exposure() {
			widest = g
		}
	}
	return n, widest
}

func header(path string, all, kept []store.Run, from, to time.Time, unit, rule string) {
	fmt.Printf("store:       %s\n", path)
	fmt.Printf("rows:        %d harvest_run rows, one per source per invocation\n", len(all))
	if from.IsZero() && to.IsZero() {
		fmt.Printf("population:  UNWINDOWED — every row in the store, %s .. %s\n",
			stamp(all[0].StartedAt), stamp(all[len(all)-1].StartedAt))
	} else {
		fmt.Printf("population:  WINDOWED — %d of %d rows kept; every figure below is the WINDOW's\n",
			len(kept), len(all))
		fmt.Printf("             -since %s  -until %s\n", bound(from), bound(to))
		if len(kept) > 0 {
			fmt.Printf("             kept rows span %s .. %s\n",
				stamp(kept[0].StartedAt), stamp(kept[len(kept)-1].StartedAt))
		}
	}
	fmt.Printf("%-12s %s\n", unit+":", rule)
	fmt.Println()
}

func separationReport(runs []store.Run, join time.Duration) {
	absorbed, split := separation(runs, join)
	fmt.Println("SEPARATION  how much room the -join threshold had")
	fmt.Printf("  widest delta absorbed into an invocation:  %s\n", absorbed)
	fmt.Printf("  narrowest delta split into a new one:      %s\n", split)
	switch {
	case split == 0:
		fmt.Println("  nothing was split, so this population holds one invocation and no cadence")
	case absorbed == 0:
		fmt.Println("  no delta was absorbed: every row here begins its own invocation")
	default:
		fmt.Printf("  the threshold sits in an empty band %.0fx wide, so it is not deciding the answer\n",
			float64(split)/float64(absorbed))
	}
	fmt.Println()
}

// batchReport counts the three outcomes SEPARATELY rather than partitioning.
// One batch can hold both a failed source and an unfinished row — the grid
// merges exactly such a batch on 2026-08-01 — and a partition silently drops
// whichever case the switch tests second.
func batchReport(b []batch, units string) {
	var clean, withFailure, withUnfinished int
	var longest time.Duration
	for _, x := range b {
		if x.Failed == 0 && x.Unfinished == 0 {
			clean++
		}
		if x.Failed > 0 {
			withFailure++
		}
		if x.Unfinished > 0 {
			withUnfinished++
		}
		if d := x.End.Sub(x.Start); d > longest {
			longest = d
		}
	}
	fmt.Printf("%s  %d   (the three counts below overlap)\n", strings.ToUpper(units), len(b))
	fmt.Printf("  every source read through:      %d\n", clean)
	fmt.Printf("  at least one source FAILED:     %d   the harvester ran; a source was lost, with a reason\n", withFailure)
	fmt.Printf("  at least one row UNFINISHED:    %d   started, never reported either way\n", withUnfinished)
	fmt.Printf("  longest, start to end:          %s\n", longest.Round(time.Millisecond))
	fmt.Println()
}

func gapReport(gaps []gap, thresholds []time.Duration, units string) {
	fmt.Printf("GAPS  %d, between %d %s\n", len(gaps), len(gaps)+1, units)
	if len(gaps) == 0 {
		fmt.Println("  no gap: one batch is not evidence of a cadence")
		fmt.Println()
		return
	}
	fmt.Println("  exposure is prev end -> next start, the time nothing was being read")
	fmt.Println("  cadence  is prev start -> next start, what a periodic timer controls")
	fmt.Printf("  %-9s %10s %10s %10s", "measure", "mean", "median", "max")
	for _, t := range thresholds {
		fmt.Printf(" %7s", "over "+short(t))
	}
	fmt.Println()
	for _, m := range []struct {
		name string
		f    func(gap) time.Duration
	}{{"exposure", gap.Exposure}, {"cadence", gap.Cadence}} {
		d := measured(gaps, m.f)
		fmt.Printf("  %-9s %10s %10s %10s", m.name, hours(mean(d)), hours(median(d)), hours(longest(d)))
		for _, t := range thresholds {
			fmt.Printf(" %7d", over(gaps, t, m.f))
		}
		fmt.Println()
	}
	fmt.Println()
}

// longest is the widest of a set of durations.
func longest(d []time.Duration) time.Duration {
	var m time.Duration
	for _, v := range d {
		if v > m {
			m = v
		}
	}
	return m
}

func widest(gaps []gap, top int) {
	if len(gaps) == 0 {
		return
	}
	s := append([]gap(nil), gaps...)
	sort.Slice(s, func(i, j int) bool { return s[i].Exposure() > s[j].Exposure() })
	if top > len(s) {
		top = len(s)
	}
	fmt.Printf("WIDEST %d  by exposure\n", top)
	fmt.Printf("  %10s %10s   %s\n", "exposure", "cadence", "from the previous start .. to the next start")
	for _, g := range s[:top] {
		fmt.Printf("  %10s %10s   %s .. %s\n",
			hours(g.Exposure()), hours(g.Cadence()), stamp(g.PrevStart), stamp(g.Start))
	}
	fmt.Println()
}

func recoveredReport(path string, gaps []gap, top int) error {
	if len(gaps) == 0 {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s to count recovered evidence: %w", path, err)
	}
	defer db.Close()

	s := append([]gap(nil), gaps...)
	sort.Slice(s, func(i, j int) bool { return s[i].Exposure() > s[j].Exposure() })
	if top > len(s) {
		top = len(s)
	}

	fmt.Printf("RECOVERED  evidence dated inside each of those %d gaps that no run had seen until after it ended\n", top)
	fmt.Println("  a large count proves that evidence SURVIVED the gap, so the gap cost latency and not data")
	fmt.Println("  a small count proves NOTHING: evidence that expired unharvested leaves nothing to count,")
	fmt.Println("  and an mtime source that was overwritten during the gap is invisible here by construction")
	for _, g := range s[:top] {
		byS, total, err := recovered(db, g)
		if err != nil {
			return err
		}
		fmt.Printf("  %10s ending %s  %6d events\n", hours(g.Exposure()), stamp(g.Start), total)
		var parts []string
		for _, c := range byS {
			parts = append(parts, fmt.Sprintf("%s %d", c.Source, c.N))
		}
		if len(parts) == 0 {
			parts = append(parts, "no source contributed anything dated inside this gap")
		}
		fmt.Printf("             %s\n", strings.Join(parts, ", "))
	}
	fmt.Println()
	return nil
}

func hidden(allBatches []batch, kept []gap) {
	n, w := excluded(gapsOf(allBatches), kept)
	fmt.Println("HIDDEN BY THE WINDOW  measured, not assumed")
	if n == 0 {
		fmt.Println("  none: the window excludes no gap the unwindowed store contains")
		fmt.Println()
		return
	}
	fmt.Printf("  %d gap(s) fall outside the window and are in NO figure above\n", n)
	fmt.Printf("  the widest of them is %s exposure, %s .. %s\n",
		hours(w.Exposure()), stamp(w.PrevEnd), stamp(w.Start))
	fmt.Println("  quote the windowed figures only alongside this line")
	fmt.Println()
}

func failures(runs []store.Run) {
	var failed, unfinished []store.Run
	for _, r := range runs {
		switch r.Status {
		case store.StatusFailed:
			failed = append(failed, r)
		case store.StatusRunning:
			unfinished = append(unfinished, r)
		}
	}
	fmt.Println("FAILURES  rows that exist and record a loss — a different observation from a missing run")
	fmt.Println("          error text carries absolute filesystem paths; do not paste this into anything published")
	if len(failed) == 0 {
		fmt.Println("  no failed row in this population")
	}
	for _, r := range failed {
		fmt.Printf("  FAILED     %s  %-22s %s\n", stamp(r.StartedAt), r.SourceID, oneLine(r.Error, 72))
	}
	if len(unfinished) == 0 {
		fmt.Println("  no unfinished row in this population")
	}
	for _, r := range unfinished {
		fmt.Printf("  UNFINISHED %s  %-22s no finished_at: the process never reported either way\n",
			stamp(r.StartedAt), r.SourceID)
	}
	fmt.Println()
}

func absence(gaps []gap, nom time.Duration, unit string) {
	n := over(gaps, nom, gap.Cadence)
	fmt.Println("WHAT IS NOT IN THE RECORD")
	fmt.Printf("  %d of %d gaps exceed the %s this schedule is believed to promise.\n", n, len(gaps), short(nom))
	fmt.Println("  That interval is an ARGUMENT to this tool (-interval), not something read from launchd,")
	fmt.Println("  so it can go stale against the installed plist without this output changing.")
	fmt.Println("  A gap is an ABSENCE. harvest_run records runs that reached the point of writing a row,")
	fmt.Println("  so a gap is equally consistent with the timer never firing, the machine being asleep,")
	fmt.Println("  and a process that died before its first write. Nothing here distinguishes those, and")
	fmt.Println("  no absence is a run that FAILED — a failure is a row, and every one is listed above.")
	fmt.Println("  Nothing in harvest_run records what INVOKED a run either, so a scheduled harvest and a")
	fmt.Println("  hand-run one are indistinguishable here and the mean mixes both.")
}

// hours prints a duration the way the figures are quoted elsewhere, so a number
// in a document can be matched against this output character for character.
func hours(d time.Duration) string {
	return fmt.Sprintf("%.4fh", d.Hours())
}

// short drops the zero tail time.Duration always prints, so a 12h threshold
// heads a column as "12h" rather than "12h0m0s". Trimming the suffix off the
// string is what this used to do, and it turned "10m0s" into "1".
func short(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return strings.TrimSuffix(d.String(), "0s")
	default:
		return d.String()
	}
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// bound prints a window edge, and prints an absent one as an absence rather
// than as a zero time forty-five years before the first row.
func bound(t time.Time) string {
	if t.IsZero() {
		return "(none)"
	}
	return stamp(t)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
