// Command dayview reconstructs a single working day for one project by
// overlaying every evidence source on a shared timeline.
//
// This is the spike's answer to "what would the tool actually show you", and
// it exists because no single source is convincing alone. On 2026-07-11 the
// repo saw 1760 asset files written and zero commits, 0.7h of Claude
// activity, and 13.9h of screen-on time — a full day of creative work that
// git cannot see. Only the overlay makes that legible.
//
// The day runs from -boundary to -boundary (default 05:00), so late-night
// work is attributed to the day it belongs to rather than to the next one.
//
// # Three bugs in how this read presence, all fixed 2026-07-29
//
// This is a prototype for the review UI, so it will be read and copied from.
// What it got wrong is worth stating plainly, because the wrong figures reached
// project/private/ground-truth.md and a reader reconciling against that file needs to
// know which numbers moved.
//
//  1. It summed raw /display/isBacklit rows without merging them. knowledgeC
//     writes nested rows — on 2026-07-10 the block 11:38:16–15:20:40 wholly
//     contains a dozen shorter ones — so the overlap was double-counted. This
//     inflated the presence figures in ground-truth.md by up to 3.5%, and
//     same-signed every time, so a bias rather than noise. See
//     project/private/2026-07-29-join-validation.md §5.
//
//  2. It selected ZVALUEINTEGER=1 and treated everything else as darkness. The
//     stream has three states: 1,236 rows assert isBacklit=1, 434 assert
//     isBacklit=0, and what is left after both is time the sampler said nothing
//     about. On 2026-07-14 one such gap runs 1h27m while Biome records dense app
//     switching throughout, so reading it as darkness deletes real work. See
//     project/private/2026-07-29-sleep-asymmetry.md §5.
//
//  3. It kept a presence block if the block STARTED inside the day, which
//     neither clipped a block straddling the 05:00 boundary nor kept the part of
//     one that ran into the day from before it. So an evening session crossing
//     the boundary was charged wholesale to the earlier day and a block that
//     began at 04:50 vanished from both the total and the timeline.
//
// Every presence total here is now merged, clipped, and three-state. They agree
// with scripts/joinday, which is the tool those figures should be taken from.
//
// Usage:
//
//	go run ./scripts/spike/dayview -date 2026-07-11 -repo ~/git/acorn
package main

import (
	"bufio"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bensyverson/timetattle/internal/gitenv"
	_ "modernc.org/sqlite"
)

const coreDataEpoch = 978307200

type block struct{ start, end time.Time }

func (b block) dur() time.Duration { return b.end.Sub(b.start) }

// backlight is the day's presence read in the three states the stream actually
// carries, plus the part of the day the snapshot does not reach.
//
// Holding them apart is the point. A single []block called "screen" invites the
// reader to treat its complement as darkness, which is the bug this type exists
// to make impossible to reintroduce.
type backlight struct {
	lit, off, unobserved, beyond []block
}

// day is the working day being reconstructed, bounded by the configured
// start hour rather than midnight.
type day struct {
	start, end time.Time
}

func (d day) contains(t time.Time) bool {
	return !t.Before(d.start) && t.Before(d.end)
}

func main() {
	date := flag.String("date", "", "working day to reconstruct (YYYY-MM-DD, required)")
	repo := flag.String("repo", "", "repository to reconstruct it for (required)")
	snap := flag.String("snapshot", "", "knowledgeC snapshot, .db or .db.gz (default: the newest in ~/.timetattle/snapshots)")
	boundary := flag.Int("boundary", 5, "hour at which the working day starts")
	flag.Parse()

	if *date == "" || *repo == "" {
		fatal("both -date and -repo are required")
	}
	if *snap == "" {
		found, err := newestSnapshot()
		if err != nil {
			fatal("%v", err)
		}
		*snap = found
	}
	base, err := time.ParseInLocation("2006-01-02", *date, time.Local)
	if err != nil {
		fatal("bad -date: %v", err)
	}
	d := day{
		start: base.Add(time.Duration(*boundary) * time.Hour),
		end:   base.AddDate(0, 0, 1).Add(time.Duration(*boundary) * time.Hour),
	}
	*repo = expand(*repo)

	screen := loadBacklight(*snap, d)
	saves := loadSaves(*repo, d)
	claude := loadClaude(*repo, d)
	commits := loadCommits(*repo, d)

	fmt.Printf("%s  %s  —  %s\n", *date, base.Format("Monday"), *repo)
	fmt.Printf("working day %s → %s\n", d.start.Format("15:04"), d.end.Format("15:04 (Mon)"))
	fmt.Printf("presence    %s\n\n", *snap)

	printTimeline(d, screen, saves, claude, commits)
	printBlocks(screen)
	printSaves(saves)
	printFamilies(saves)
	printSummary(d, screen, saves, claude, commits)
}

// loadBacklight reads presence in all three states the stream carries, clipped
// to the working day. This is the presence spine: it is the only source that
// knows you were at the machine while nothing was being written.
//
// A positive ON observation wins where an ON row and an OFF row overlap, because
// the stream nests contradictory rows and "the screen was on at this moment" is
// the more specific claim. scripts/joinday reports how much time that tie-break
// decides; this tool does not, and a reader who needs that number should use
// joinday.
func loadBacklight(path string, d day) backlight {
	db, done, err := openSnapshot(path)
	if err != nil {
		fatal("%v", err)
	}
	defer done()
	defer db.Close()

	rows, err := db.Query(`
		SELECT ZSTARTDATE, ZENDDATE, ZVALUEINTEGER FROM ZOBJECT
		WHERE ZSTREAMNAME='/display/isBacklit'
		ORDER BY ZSTARTDATE`)
	if err != nil {
		fatal("query screen: %v", err)
	}
	defer rows.Close()

	var lit, off []block
	for rows.Next() {
		var s, e float64
		var value int
		if err := rows.Scan(&s, &e, &value); err != nil {
			continue
		}
		b := block{
			start: time.Unix(int64(s)+coreDataEpoch, 0),
			end:   time.Unix(int64(e)+coreDataEpoch, 0),
		}
		if value == 1 {
			lit = append(lit, b)
			continue
		}
		off = append(off, b)
	}
	if len(lit) == 0 && len(off) == 0 {
		fatal("%s holds no /display/isBacklit rows; is it a knowledgeC snapshot?", path)
	}

	lit, off = merge(lit), merge(off)
	off = subtract(off, lit)
	covered := merge(append(append([]block{}, lit...), off...))
	reach := block{covered[0].start, covered[len(covered)-1].end}
	whole := []block{{d.start, d.end}}

	return backlight{
		lit: clip(lit, d),
		off: clip(off, d),
		// Unobserved is only unobserved inside the span the snapshot reaches.
		// Beyond that the snapshot is not silent about the backlight, it simply
		// does not extend that far, and adding the two together would report the
		// age of the snapshot as a sampler failure.
		unobserved: subtract(intersect(whole, []block{reach}), covered),
		beyond:     subtract(whole, []block{reach}),
	}
}

// ------------------------------------------------------- interval arithmetic

// merge normalises blocks into disjoint, sorted blocks. Every total below runs
// through here first: knowledgeC writes nested rows, so summing raw durations
// double-counts, silently, in the direction that flatters the tool.
func merge(in []block) []block {
	if len(in) == 0 {
		return nil
	}
	cp := append([]block(nil), in...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].start.Before(cp[j].start) })

	out := []block{cp[0]}
	for _, b := range cp[1:] {
		tail := &out[len(out)-1]
		if b.start.After(tail.end) {
			out = append(out, b)
			continue
		}
		if b.end.After(tail.end) {
			tail.end = b.end
		}
	}
	return out
}

// subtract returns the parts of a that are not in b. Both must be merged.
func subtract(a, b []block) []block {
	var out []block
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
				out = append(out, block{cur.start, cut.start})
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

// intersect returns the parts of a that are also in b. Both must be merged.
func intersect(a, b []block) []block {
	var out []block
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		start, end := laterOf(a[i].start, b[j].start), earlierOf(a[i].end, b[j].end)
		if start.Before(end) {
			out = append(out, block{start, end})
		}
		if a[i].end.Before(b[j].end) {
			i++
		} else {
			j++
		}
	}
	return out
}

// clip trims blocks to the working day. Selecting on the start alone charged an
// interval crossing 05:00 wholesale to one day and dropped one that began just
// before the boundary from both.
func clip(in []block, d day) []block {
	var out []block
	for _, b := range in {
		start, end := laterOf(b.start, d.start), earlierOf(b.end, d.end)
		if start.Before(end) {
			out = append(out, block{start, end})
		}
	}
	return out
}

func totalOf(in []block) time.Duration {
	var t time.Duration
	for _, b := range in {
		t += b.dur()
	}
	return t
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

// openSnapshot opens a snapshot, transparently decompressing a .gz. Harvested
// snapshots are gzipped and SQLite cannot read those in place.
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

	tmp, err := os.CreateTemp("", "dayview-*.db")
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
// timestamped, so lexical order is chronological order. The old hardcoded
// default pointed at a file under local/ that no longer exists.
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

type save struct {
	at     time.Time
	path   string
	kind   string
	origin string // "born" or "saved"
}

// loadSaves walks the repo and emits an observation per timestamp a file
// carries, not per file. APFS keeps st_birthtime alongside mtime, so a file
// created at 06:00 and last written at 06:50 is two separate pieces of
// evidence that something happened.
//
// Deliberately NOT treated as an interval. Of 512 files touched on
// 2026-07-11, only 11 had a birth-to-mtime gap over a minute, and those gaps
// were mostly enormous — lightbar.psd spans 09:55 to 23:41. Summing them
// would claim 70+ hours on a 13.9-hour day. The gap means "touched at least
// twice", so each end is an observation and the middle is unknown.
//
// mtime still only preserves the last write, so this remains a lower bound.
func loadSaves(repo string, d day) []save {
	var out []save
	filepath.WalkDir(repo, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == ".git" || e.Name() == "node_modules" || e.Name() == "worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		kind := classify(p)
		mod := info.ModTime()
		if d.contains(mod) {
			out = append(out, save{at: mod, path: p, kind: kind, origin: "saved"})
		}
		// Exported assets are written once, so birth and mtime coincide and
		// recording both would double-count a single write.
		if born, ok := birthTime(info); ok && d.contains(born) && born.Unix() != mod.Unix() {
			out = append(out, save{at: born, path: p, kind: kind, origin: "born"})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

func birthTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), true
}

// printFamilies reconstructs save-as chains. Blender and Motion are used by
// branching rather than editing — lightbar.blend becomes lightbar-top.blend
// becomes lightbar-top-filmic.blend — so each file is born and modified at
// the same instant and carries no span of its own. The editing history is
// spread across sibling filenames instead.
//
// This is the version history .DocumentRevisions-V100 turned out not to
// hold: it is not in the Versions store, it is in the filenames.
func printFamilies(saves []save) {
	groups := map[string][]famMember{}

	for _, s := range saves {
		if s.kind != "source" || s.origin != "born" && !bornEqualsSaved(saves, s) {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(s.path), filepath.Ext(s.path))
		key := filepath.Dir(s.path) + "|" + filepath.Ext(s.path)
		groups[key] = append(groups[key], famMember{stem, s.at})
	}

	fmt.Println("SAVE-AS CHAINS (branching history recovered from filenames)")
	printed := false
	for _, members := range groups {
		fams := clusterStems(members)
		for _, fam := range fams {
			if len(fam) < 2 {
				continue
			}
			printed = true
			sort.Slice(fam, func(i, j int) bool { return fam[i].at.Before(fam[j].at) })
			fmt.Printf("  %s\n", commonPrefix(fam[0].name, fam[len(fam)-1].name))
			var prev time.Time
			for _, m := range fam {
				delta := ""
				if !prev.IsZero() {
					delta = fmt.Sprintf("  (+%.0fm)", m.at.Sub(prev).Minutes())
				}
				fmt.Printf("      %s  %s%s\n", m.at.Format("15:04"), m.name, delta)
				prev = m.at
			}
		}
	}
	if !printed {
		fmt.Println("  none")
	}
	fmt.Println()
}

func bornEqualsSaved(saves []save, s save) bool {
	for _, o := range saves {
		if o.path == s.path && o.origin != s.origin {
			return false // it has both, so the "born" row already covers it
		}
	}
	return true
}

type famMember struct {
	name string
	at   time.Time
}

// clusterStems groups filenames that share a substantial common prefix.
// A plain split on "-" fails the same way Claude's project directories do:
// it cannot tell a separator from a dash inside a real name like
// dark-crystal. Requiring a prefix that is both long enough in absolute
// terms and a large fraction of the shorter name avoids grouping unrelated
// files that happen to start with the same few letters.
func clusterStems(ms []famMember) [][]famMember {
	parent := make([]int, len(ms))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}

	for i := range ms {
		for j := i + 1; j < len(ms); j++ {
			cp := len(commonPrefix(ms[i].name, ms[j].name))
			shorter := min(len(ms[i].name), len(ms[j].name))
			if cp >= 5 && float64(cp) >= 0.4*float64(shorter) {
				parent[find(i)] = find(j)
			}
		}
	}

	byRoot := map[int][]famMember{}
	for i, m := range ms {
		r := find(i)
		byRoot[r] = append(byRoot[r], m)
	}
	var out [][]famMember
	for _, g := range byRoot {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return strings.TrimRight(a[:n], "-_ .")
}

// classify splits source documents from generated output. The distinction
// matters: a thousand exported frames is one action, while a handful of
// source saves is a day of decisions.
func classify(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".motn", ".psd", ".psb", ".blend", ".ai", ".aep":
		return "source"
	case ".webp", ".png", ".jpg", ".jpeg", ".mov", ".mp4", ".gif", ".exr", ".tif":
		return "output"
	default:
		return "other"
	}
}

// loadClaude reads only this project's transcripts, which is fast enough to
// run per-day — unlike a full scan of every project.
func loadClaude(repo string, d day) []time.Time {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Claude encodes the cwd by replacing separators with dashes.
	dir := filepath.Join(home, ".claude/projects", strings.ReplaceAll(repo, "/", "-"))

	var out []time.Time
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			var r struct {
				Timestamp time.Time `json:"timestamp"`
			}
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.Timestamp.IsZero() {
				continue
			}
			if d.contains(r.Timestamp) {
				out = append(out, r.Timestamp)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func loadCommits(repo string, d day) []time.Time {
	// gitenv.Command strips GIT_DIR, GIT_WORK_TREE and their relatives from
	// the inherited environment: git exports them into every hook and
	// subprocess it runs, and they silently override -C, so an unscrubbed
	// invocation here would read whichever repository invoked this process
	// instead of repo. See internal/gitenv's package doc; this is the same
	// rule internal/gitlog, internal/registry, scripts/confidential and
	// scripts/pseudonymize each already apply for the identical reason. This
	// was found unstripped -- and fixed -- during the audit that added
	// TestLoadCommitsDoesNotLeakGitDirIntoADecoyRepo.
	cmd := gitenv.Command("git", "-C", repo, "log", "--all", "--pretty=%aI",
		"--since", d.start.Format(time.RFC3339), "--until", d.end.Format(time.RFC3339))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ts []time.Time
	for line := range strings.FieldsSeq(string(out)) {
		if t, err := time.Parse(time.RFC3339, line); err == nil {
			ts = append(ts, t)
		}
	}
	return ts
}

// printTimeline draws one row per hour of the working day. Reading down the
// columns shows which sources agree and, more usefully, where they disagree.
//
// The bar is drawn from observed-ON only, and the UNOBS column is the minutes in
// that hour the backlight sampler said nothing about. A short bar with a large
// UNOBS is not a quiet hour, it is an hour with no presence evidence either way,
// and the two must not look the same on a screen a person makes decisions from.
func printTimeline(d day, screen backlight, saves []save, claude []time.Time, commits []time.Time) {
	fmt.Println("HOUR   SCREEN                    UNOBS  CLAUDE  SRC  OUT  COMMITS")
	for h := range 24 {
		hourStart := d.start.Add(time.Duration(h) * time.Hour)
		hourEnd := hourStart.Add(time.Hour)
		hour := []block{{hourStart, hourEnd}}

		on := totalOf(intersect(screen.lit, hour))
		gap := totalOf(intersect(screen.unobserved, hour))
		nClaude := countIn(claude, hourStart, hourEnd)
		nCommit := countIn(commits, hourStart, hourEnd)
		var src, outp int
		for _, s := range saves {
			if !s.at.Before(hourStart) && s.at.Before(hourEnd) {
				switch s.kind {
				case "source":
					src++
				case "output":
					outp++
				}
			}
		}
		if on == 0 && gap == 0 && nClaude == 0 && src == 0 && outp == 0 && nCommit == 0 {
			continue
		}

		bar := strings.Repeat("█", int(on.Minutes())/3) + strings.Repeat("?", int(gap.Minutes())/3)
		fmt.Printf("%s  %-24s  %5s  %6s  %3s  %3s  %s\n",
			hourStart.Format("15:04"), bar, minutes(gap),
			dash(nClaude), dash(src), dash(outp), dash(nCommit))
	}
	fmt.Println()
}

func printBlocks(screen backlight) {
	fmt.Println("SCREEN-ON BLOCKS (>5m, merged)")
	for _, b := range screen.lit {
		if b.dur() < 5*time.Minute {
			continue
		}
		fmt.Printf("  %s – %s   %4.0fm\n",
			b.start.Format("15:04"), b.end.Format("15:04"), b.dur().Minutes())
	}
	fmt.Printf("  total present: %.2fh in %d blocks\n\n",
		totalOf(screen.lit).Hours(), len(screen.lit))

	if len(screen.unobserved) == 0 {
		return
	}
	fmt.Println("BACKLIGHT COVERAGE GAPS — no row of either value")
	for _, b := range screen.unobserved {
		fmt.Printf("  %s – %s   %s  ← unknown, NOT dark\n",
			b.start.Format("15:04:05"), b.end.Format("15:04:05"), b.dur().Round(time.Second))
	}
	fmt.Println()
}

func printSaves(saves []save) {
	fmt.Println("SOURCE DOCUMENT SAVES (the decisions, not the exports)")
	n := 0
	for _, s := range saves {
		if s.kind != "source" {
			continue
		}
		n++
		fmt.Printf("  %s  %s\n", s.at.Format("15:04"), filepath.Base(s.path))
	}
	if n == 0 {
		fmt.Println("  none")
	}
	fmt.Println()
}

func printSummary(d day, screen backlight, saves []save, claude, commits []time.Time) {
	present := totalOf(screen.lit)
	var src, outp int
	for _, s := range saves {
		switch s.kind {
		case "source":
			src++
		case "output":
			outp++
		}
	}
	fmt.Printf("SUMMARY  present %.2fh · claude events %d · source saves %d · outputs %d · commits %d\n",
		present.Hours(), len(claude), src, outp, len(commits))
	fmt.Printf("         backlight: ON %.2fh · OFF %.2fh · unobserved %.2fh · beyond the snapshot %.2fh\n",
		present.Hours(), totalOf(screen.off).Hours(),
		totalOf(screen.unobserved).Hours(), totalOf(screen.beyond).Hours())
	if drift := present + totalOf(screen.off) + totalOf(screen.unobserved) +
		totalOf(screen.beyond) - d.end.Sub(d.start); drift != 0 {
		fmt.Printf("         !! the four states miss the day by %s — the partition is broken\n", drift)
	}
	if len(commits) == 0 && present > 4*time.Hour {
		fmt.Println("         ^ a substantial day with no commits: git alone would show nothing here")
	}
}

func minutes(d time.Duration) string {
	if d == 0 {
		return "·"
	}
	return fmt.Sprintf("%.0f", d.Minutes())
}

func countIn(ts []time.Time, start, end time.Time) int {
	n := 0
	for _, t := range ts {
		if !t.Before(start) && t.Before(end) {
			n++
		}
	}
	return n
}

func dash(n int) string {
	if n == 0 {
		return "·"
	}
	return fmt.Sprint(n)
}

func expand(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
