// Command transcripts characterises ~/.claude/projects as an event stream.
//
// It answers three questions the v1 design rests on:
//
//   - Density. What is the distribution of gaps between consecutive events in
//     a session? That distribution, not an average, is what sets the
//     session-clustering threshold. An average is meaningless here because the
//     distribution is bimodal by construction: sub-second gaps within a burst
//     of tool calls, and multi-hour gaps between actual work sittings.
//
//   - Attribution. Directory names encode a working directory by replacing
//     path separators with dashes, which is lossy — a repo whose own name
//     contains a dash is indistinguishable from a nested path. Records carry
//     an explicit cwd field, so this probe measures how often the lossy name
//     would mislead us and confirms cwd as the authoritative source.
//
//   - Cost. Wall-clock time for a full parse, which is a real constraint on
//     the v1 backfill.
//
// It also prints a first, deliberately crude engaged-time estimate per
// project by clustering events into sessions at -gap. That number is not
// calibrated and should not be trusted as hours; it exists to show whether the
// signal is in a plausible range at all.
//
// Usage: go run ./scripts/spike/transcripts [-gap 30m] [-top 25]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// record is the narrow projection of a transcript line we care about. The
// files contain far more (message bodies, tool results), but decoding only
// these fields keeps the parse honest about what the event model needs.
type record struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	CWD         string    `json:"cwd"`
	SessionID   string    `json:"sessionId"`
	GitBranch   string    `json:"gitBranch"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
}

// windowStart/windowEnd bound which events count, set by -since/-until.
var windowStart, windowEnd time.Time

type event struct {
	ts time.Time
	// subagent marks an event from a <sessionId>/subagents/*.jsonl file.
	// Subagent work is concurrent with its parent session, so it must not add
	// wall-clock time — a fact this probe verifies rather than assumes.
	subagent bool
}

// project accumulates everything we learn about one directory under
// ~/.claude/projects.
type project struct {
	dir           string
	sessions      map[string][]event
	cwds          map[string]int
	branches      map[string]int
	types         map[string]int
	events        int
	subagentEvts  int
	subagentFiles int
	meta          int
	bytes         int64
	files         int
	earliest      time.Time
	latest        time.Time
}

func main() {
	gap := flag.Duration("gap", 30*time.Minute, "idle gap that ends a session when clustering events")
	top := flag.Int("top", 25, "how many projects to list in the per-project table")
	root := flag.String("root", "", "transcript root (default ~/.claude/projects)")
	since := flag.String("since", "", "only count events on or after this local date (YYYY-MM-DD)")
	until := flag.String("until", "", "only count events on or before this local date (YYYY-MM-DD)")
	flag.Parse()

	// Window filtering exists to line transcript activity up against a
	// presence source over the same days, which is the only way to see how
	// much of a period's wall clock the artifacts actually account for.
	if *since != "" {
		t, err := time.ParseInLocation("2006-01-02", *since, time.Local)
		if err != nil {
			fatal("bad -since: %v", err)
		}
		windowStart = t
	}
	if *until != "" {
		t, err := time.ParseInLocation("2006-01-02", *until, time.Local)
		if err != nil {
			fatal("bad -until: %v", err)
		}
		windowEnd = t.AddDate(0, 0, 1)
	}

	if *root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("cannot determine home directory: %v", err)
		}
		*root = filepath.Join(home, ".claude/projects")
	}

	dirs, err := os.ReadDir(*root)
	if err != nil {
		fatal("cannot read %s: %v", *root, err)
	}

	start := time.Now()
	projects := scanAll(*root, dirs)
	elapsed := time.Since(start)

	reportTotals(projects, elapsed, *gap)
	reportGapDistribution(projects)
	reportAttribution(projects)
	reportSweep(projects)
	reportProjects(projects, *gap, *top)
	reportSubstance(projects, *gap)
}

// reportSweep varies the clustering threshold to show how much the duration
// model depends on a parameter we would otherwise have to calibrate. If the
// curve is flat across a wide range, the threshold is not a knob worth
// agonising over and calibration effort belongs elsewhere.
func reportSweep(projects []*project) {
	fmt.Println("== THRESHOLD SENSITIVITY ==")
	fmt.Println("(if this curve is flat, the clustering threshold is not the thing to calibrate)")
	fmt.Println()

	thresholds := []time.Duration{
		2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute,
		30 * time.Minute, 45 * time.Minute, time.Hour, 2 * time.Hour,
	}

	var acorn *project
	for _, p := range projects {
		if strings.HasSuffix(p.dir, "-git-acorn") {
			acorn = p
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "THRESHOLD\tTOTAL\tvs 30m\tSITTINGS\tACORN")
	var base float64
	for _, t := range thresholds {
		var total time.Duration
		var sittings int
		for _, p := range projects {
			d, s := engaged(p, t, true)
			total += d
			sittings += s
		}
		if t == 30*time.Minute {
			base = total.Hours()
		}
		acornHours := 0.0
		if acorn != nil {
			h, _ := engaged(acorn, t, true)
			acornHours = h.Hours()
		}
		fmt.Fprintf(w, "%s\t%.1fh\t\t%d\t%.1fh\n", t, total.Hours(), sittings, acornHours)
	}
	w.Flush()
	fmt.Printf("\n(30m baseline = %.1fh)\n\n", base)
}

// scanAll walks every project directory concurrently. Transcript files are
// independent, so the only shared state is the result slice.
func scanAll(root string, dirs []os.DirEntry) []*project {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []*project
		sem     = make(chan struct{}, runtime.GOMAXPROCS(0))
	)

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p := scanProject(filepath.Join(root, name), name)
			if p.events == 0 {
				return
			}
			mu.Lock()
			results = append(results, p)
			mu.Unlock()
		}(d.Name())
	}
	wg.Wait()
	return results
}

func scanProject(path, name string) *project {
	p := &project{
		dir:      name,
		sessions: map[string][]event{},
		cwds:     map[string]int{},
		branches: map[string]int{},
		types:    map[string]int{},
	}

	// Transcripts nest: top-level files are main sessions, and each may have a
	// <sessionId>/subagents/ directory beside it. Walking only the top level
	// silently drops the majority of files.
	filepath.WalkDir(path, func(fp string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			p.bytes += info.Size()
		}
		p.files++

		// .../<projectDir>/<sessionId>/subagents/<file>.jsonl
		parent := filepath.Base(filepath.Dir(fp))
		isSub := parent == "subagents"
		var owner string
		if isSub {
			p.subagentFiles++
			owner = filepath.Base(filepath.Dir(filepath.Dir(fp)))
		}
		scanFile(fp, p, isSub, owner)
		return nil
	})
	return p
}

// scanFile reads one transcript. When isSubagent is set, owner is the parent
// session's id: subagent events are folded into the parent session so that
// concurrent agent work collapses to one span rather than stacking.
func scanFile(path string, p *project, isSubagent bool, owner string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Tool results are embedded inline and can be enormous; the default 64 KB
	// limit silently truncates whole sessions.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		p.types[r.Type]++
		if r.Timestamp.IsZero() {
			// Bookkeeping records (mode, ai-title, last-prompt) carry no
			// timestamp and cannot contribute to a timeline.
			continue
		}

		if !windowStart.IsZero() && r.Timestamp.Before(windowStart) {
			continue
		}
		if !windowEnd.IsZero() && !r.Timestamp.Before(windowEnd) {
			continue
		}

		p.events++
		if isSubagent {
			p.subagentEvts++
		}
		if r.IsMeta {
			p.meta++
		}
		if r.CWD != "" {
			p.cwds[r.CWD]++
		}
		if r.GitBranch != "" {
			p.branches[r.GitBranch]++
		}
		if p.earliest.IsZero() || r.Timestamp.Before(p.earliest) {
			p.earliest = r.Timestamp
		}
		if r.Timestamp.After(p.latest) {
			p.latest = r.Timestamp
		}

		sid := owner
		if sid == "" {
			sid = r.SessionID
		}
		if sid == "" {
			sid = filepath.Base(path)
		}
		p.sessions[sid] = append(p.sessions[sid], event{ts: r.Timestamp, subagent: isSubagent})
	}
}

// gaps returns every inter-event interval within a session, in seconds.
// Subagent events are included: they are concurrent with the main thread, so
// they tighten gaps without adding wall-clock time, which is exactly the
// behaviour we want when choosing a clustering threshold.
func gaps(p *project) []float64 {
	var out []float64
	for _, evs := range p.sessions {
		if len(evs) < 2 {
			continue
		}
		evs := append([]event(nil), evs...)
		sort.Slice(evs, func(i, j int) bool { return evs[i].ts.Before(evs[j].ts) })
		for i := 1; i < len(evs); i++ {
			d := evs[i].ts.Sub(evs[i-1].ts).Seconds()
			if d < 0 {
				// Records are not written in monotonic order; a negative gap
				// is a reordering artifact, not elapsed time.
				continue
			}
			out = append(out, d)
		}
	}
	return out
}

// engaged clusters a project's events into sittings separated by more than
// threshold, and sums their spans. This is the crudest possible duration
// model — no lead-in, no tail-out, no calibration — and single-event sittings
// contribute zero.
func engaged(p *project, threshold time.Duration, includeSubagents bool) (total time.Duration, sittings int) {
	var all []time.Time
	for _, evs := range p.sessions {
		for _, e := range evs {
			if e.subagent && !includeSubagents {
				continue
			}
			all = append(all, e.ts)
		}
	}
	if len(all) == 0 {
		return 0, 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })

	start, prev := all[0], all[0]
	sittings = 1
	for _, ts := range all[1:] {
		if ts.Sub(prev) > threshold {
			total += prev.Sub(start)
			sittings++
			start = ts
		}
		prev = ts
	}
	total += prev.Sub(start)
	return total, sittings
}

// activeDays counts distinct calendar days with at least one event. A better
// proxy for "was this real work" than raw event count, which one runaway
// session can dominate.
func activeDays(p *project) int {
	days := map[string]bool{}
	for _, evs := range p.sessions {
		for _, e := range evs {
			days[e.ts.Local().Format("2006-01-02")] = true
		}
	}
	return len(days)
}

func reportTotals(projects []*project, elapsed time.Duration, gap time.Duration) {
	var events, files, subEvts, subFiles, meta, sessions int
	var bytes int64
	var earliest, latest time.Time
	for _, p := range projects {
		events += p.events
		files += p.files
		subEvts += p.subagentEvts
		subFiles += p.subagentFiles
		meta += p.meta
		sessions += len(p.sessions)
		bytes += p.bytes
		if earliest.IsZero() || (!p.earliest.IsZero() && p.earliest.Before(earliest)) {
			earliest = p.earliest
		}
		if p.latest.After(latest) {
			latest = p.latest
		}
	}

	fmt.Println("== TOTALS ==")
	fmt.Printf("projects       %d\n", len(projects))
	fmt.Printf("files          %d (%d main, %d subagent)\n", files, files-subFiles, subFiles)
	fmt.Printf("bytes          %.2f GB\n", float64(bytes)/(1<<30))
	fmt.Printf("timed events   %d\n", events)
	fmt.Printf("  subagent     %d (%.1f%%, concurrent with parent — should add no wall clock)\n",
		subEvts, pct(subEvts, events))
	fmt.Printf("  meta         %d (%.1f%%, synthetic records)\n", meta, pct(meta, events))
	fmt.Printf("sessions       %d (subagent transcripts folded into their parent)\n", sessions)
	fmt.Printf("span           %s → %s (%.0f days)\n",
		earliest.Local().Format("2006-01-02"), latest.Local().Format("2006-01-02"),
		latest.Sub(earliest).Hours()/24)
	fmt.Printf("parse time     %s (%.0f MB/s)\n", elapsed.Round(time.Millisecond),
		float64(bytes)/(1<<20)/elapsed.Seconds())

	// The concurrency claim, tested rather than asserted: if subagent work is
	// genuinely parallel, folding it in should barely move total wall clock
	// even though it is the majority of events.
	var withSub, withoutSub time.Duration
	for _, p := range projects {
		a, _ := engaged(p, gap, true)
		b, _ := engaged(p, gap, false)
		withSub += a
		withoutSub += b
	}
	fmt.Printf("\nengaged @ %s   %.1fh main only → %.1fh with subagents (+%.1f%%)\n",
		gap, withoutSub.Hours(), withSub.Hours(),
		100*(withSub.Hours()-withoutSub.Hours())/withoutSub.Hours())
	fmt.Println()
}

func reportGapDistribution(projects []*project) {
	var all []float64
	for _, p := range projects {
		all = append(all, gaps(p)...)
	}
	if len(all) == 0 {
		return
	}
	sort.Float64s(all)

	fmt.Println("== INTER-EVENT GAP DISTRIBUTION ==")
	fmt.Printf("(%d gaps; the shoulder between within-sitting and between-sitting is the clustering threshold)\n\n", len(all))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERCENTILE\tGAP")
	for _, q := range []float64{50, 75, 90, 95, 99, 99.5, 99.9} {
		fmt.Fprintf(w, "p%.4g\t%s\n", q, humanSecs(percentile(all, q)))
	}
	w.Flush()
	fmt.Println()

	buckets := []struct {
		label string
		upper float64
	}{
		{"< 10s", 10},
		{"10s – 1m", 60},
		{"1m – 5m", 300},
		{"5m – 15m", 900},
		{"15m – 30m", 1800},
		{"30m – 1h", 3600},
		{"1h – 2h", 7200},
		{"2h – 6h", 21600},
		{"> 6h", 1e18},
	}
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUCKET\tCOUNT\tSHARE\t")
	var lower float64
	for _, b := range buckets {
		n := countBetween(all, lower, b.upper)
		fmt.Fprintf(w, "%s\t%d\t%.2f%%\t%s\n", b.label, n, pct(n, len(all)), bar(pct(n, len(all))))
		lower = b.upper
	}
	w.Flush()
	fmt.Println()
}

// reportAttribution measures how badly the dash-encoded directory name would
// mislead us if we trusted it instead of the cwd field.
func reportAttribution(projects []*project) {
	fmt.Println("== ATTRIBUTION ==")

	var (
		multiCWD   []*project
		lossy      []*project
		noCWD      []*project
		notOnDisk  []string
		subdirCWDs []string
	)

	for _, p := range projects {
		if len(p.cwds) == 0 {
			noCWD = append(noCWD, p)
			continue
		}
		if len(p.cwds) > 1 {
			multiCWD = append(multiCWD, p)
		}
		for cwd := range p.cwds {
			// Does the naive reverse mapping (dashes back to slashes)
			// reproduce the real cwd?
			if naiveDecode(p.dir) != cwd {
				lossy = append(lossy, p)
				break
			}
		}
		for cwd := range p.cwds {
			if _, err := os.Stat(cwd); os.IsNotExist(err) {
				notOnDisk = append(notOnDisk, cwd)
			}
			if root := gitRoot(cwd); root != "" && root != cwd {
				subdirCWDs = append(subdirCWDs, fmt.Sprintf("%s (repo root %s)", cwd, root))
			}
		}
	}

	fmt.Printf("projects whose dir name does NOT naively decode to its cwd: %d of %d\n", len(lossy), len(projects))
	for _, p := range lossy {
		fmt.Printf("  %-44s decodes to %s\n", p.dir, naiveDecode(p.dir))
	}

	fmt.Printf("\nprojects with more than one distinct cwd: %d\n", len(multiCWD))
	sort.Slice(multiCWD, func(i, j int) bool { return len(multiCWD[i].cwds) > len(multiCWD[j].cwds) })
	for _, p := range multiCWD {
		// Cross-project contamination: a session that starts in one repo and
		// cds elsewhere is filed under the directory it started in, so the
		// directory cannot be trusted for attribution at all.
		var foreign []string
		for c := range p.cwds {
			if root := gitRoot(c); root != "" && root != naiveDecode(p.dir) &&
				!strings.HasPrefix(c, naiveDecode(p.dir)+"/") && c != naiveDecode(p.dir) {
				foreign = append(foreign, root)
			}
		}
		note := ""
		if f := dedupe(foreign); len(f) > 0 {
			note = fmt.Sprintf("  ← also holds work in %s", strings.Join(f, ", "))
		}
		fmt.Printf("  %-44s %3d cwds%s\n", p.dir, len(p.cwds), note)
	}

	fmt.Printf("\nprojects with no cwd on any record: %d\n", len(noCWD))
	for _, p := range noCWD {
		fmt.Printf("  %s\n", p.dir)
	}

	fmt.Printf("\ncwds no longer on disk (moved/deleted): %d\n", len(notOnDisk))
	sort.Strings(notOnDisk)
	for _, c := range dedupe(notOnDisk) {
		fmt.Printf("  %s\n", c)
	}

	// Agent worktrees are a distinct normalisation problem: they are real
	// repos in their own right, so gitRoot resolves them to themselves, and
	// their work belongs to the repo they were forked from.
	var worktrees, subdirs int
	allCWDs := map[string]bool{}
	for _, p := range projects {
		for c := range p.cwds {
			allCWDs[c] = true
		}
	}
	for c := range allCWDs {
		if strings.Contains(c, "/.claude/worktrees/") {
			worktrees++
		} else if root := gitRoot(c); root != "" && root != c {
			subdirs++
		}
	}
	fmt.Printf("\ndistinct cwds overall: %d\n", len(allCWDs))
	fmt.Printf("  agent worktrees (.claude/worktrees/): %d — normalise to the forking repo\n", worktrees)
	fmt.Printf("  repo subdirectories:                  %d — normalise to enclosing repo root\n", subdirs)
	fmt.Printf("  sample subdirectory cwds:\n")
	for i, c := range dedupe(subdirCWDs) {
		if i >= 5 {
			fmt.Printf("    … and %d more\n", len(dedupe(subdirCWDs))-5)
			break
		}
		fmt.Printf("    %s\n", c)
	}
	fmt.Println()
}

func reportProjects(projects []*project, gap time.Duration, top int) {
	sort.Slice(projects, func(i, j int) bool {
		a, _ := engaged(projects[i], gap, true)
		b, _ := engaged(projects[j], gap, true)
		return a > b
	})

	fmt.Printf("== PER-PROJECT (clustered at %s; UNCALIBRATED, not hours you can bill) ==\n\n", gap)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tENGAGED\tSITTINGS\tDAYS\tEVENTS\tFIRST\tLAST")
	for i, p := range projects {
		if i >= top {
			fmt.Fprintf(w, "… and %d more\t\t\t\t\t\t\n", len(projects)-top)
			break
		}
		eng, sittings := engaged(p, gap, true)
		fmt.Fprintf(w, "%s\t%.1fh\t%d\t%d\t%d\t%s\t%s\n",
			strings.TrimPrefix(p.dir, "-Users-ben-git-"),
			eng.Hours(), sittings, activeDays(p), p.events,
			p.earliest.Local().Format("2006-01-02"), p.latest.Local().Format("2006-01-02"))
	}
	w.Flush()
	fmt.Println()
}

// reportSubstance splits projects into plausible client work and one-off
// experiments, to size how much the opt-in registry will actually filter.
func reportSubstance(projects []*project, gap time.Duration) {
	const (
		dayThreshold  = 5
		hourThreshold = 4.0
	)
	var substantial, marginal int
	var subHours, margHours float64
	for _, p := range projects {
		eng, _ := engaged(p, gap, true)
		if activeDays(p) >= dayThreshold && eng.Hours() >= hourThreshold {
			substantial++
			subHours += eng.Hours()
		} else {
			marginal++
			margHours += eng.Hours()
		}
	}

	fmt.Println("== SUBSTANCE ==")
	fmt.Printf("(heuristic: ≥%d active days AND ≥%.0f engaged hours)\n\n", dayThreshold, hourThreshold)
	fmt.Printf("substantial  %3d projects  %8.1fh  %.1f%% of engaged time\n",
		substantial, subHours, 100*subHours/(subHours+margHours))
	fmt.Printf("marginal     %3d projects  %8.1fh  %.1f%% of engaged time\n",
		marginal, margHours, 100*margHours/(subHours+margHours))
	fmt.Println()
}

// naiveDecode reverses the dash encoding the way a consumer would if it
// trusted the directory name. It is wrong whenever a path component itself
// contains a dash, which is the point.
func naiveDecode(dir string) string {
	return strings.ReplaceAll(dir, "-", "/")
}

// gitRoot walks up from dir looking for a .git entry, returning the repo root
// or "" if dir is not inside a repo.
func gitRoot(dir string) string {
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	return ""
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q / 100 * float64(len(sorted)-1))
	return sorted[i]
}

func countBetween(sorted []float64, lower, upper float64) int {
	lo := sort.SearchFloat64s(sorted, lower)
	hi := sort.SearchFloat64s(sorted, upper)
	return hi - lo
}

func humanSecs(s float64) string {
	d := time.Duration(s * float64(time.Second))
	switch {
	case d < time.Second:
		return fmt.Sprintf("%.0fms", s*1000)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", s)
	case d < time.Hour:
		return fmt.Sprintf("%.1fm", d.Minutes())
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
}

func bar(p float64) string {
	return strings.Repeat("█", int(p/2))
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
