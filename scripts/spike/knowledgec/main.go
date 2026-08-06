// Command knowledgec characterises macOS's knowledgeC.db as a presence signal.
//
// knowledgeC is the only source that knows you were at the machine when no
// file changed — reading, watching a render, on a call. That makes it the
// denominator for utilisation, and the only evidence that reaches creative
// work in apps that leave no other trace.
//
// It is also aggressively pruned. The probe therefore snapshots before it
// analyses: whatever is in the database today is the deepest history that
// will ever exist, and each run preserves another window into
// local/snapshots (gitignored).
//
// The headline output is the difference between summed and union app time.
// Summed time double-counts overlapping apps and maps to Timetattle's
// "engaged" metric; union time is wall clock and maps to "presence". Both
// come from the same rows.
//
// Usage: go run ./scripts/spike/knowledgec [-snapshot-dir local/snapshots] [-days 14]
package main

import (
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

	_ "modernc.org/sqlite"
)

// coreDataEpoch is 2001-01-01 UTC; Core Data stores seconds from there.
const coreDataEpoch = 978307200

// interval is one app-usage row.
type interval struct {
	bundle     string
	start, end time.Time
}

func (i interval) dur() time.Duration { return i.end.Sub(i.start) }

// creativeApps are the bundles that matter for work leaving no other trace.
// Motion and Photoshop are the reason this probe exists: they do not
// participate in macOS Versions, so app focus is the only duration evidence.
var creativeApps = map[string]string{
	"com.apple.motionapp":           "Motion",
	"com.adobe.Photoshop":           "Photoshop",
	"org.blenderfoundation.blender": "Blender",
	"com.apple.FinalCut":            "Final Cut",
	"com.adobe.AfterEffects":        "After Effects",
	"com.adobe.illustrator":         "Illustrator",
}

func main() {
	src := flag.String("db", "", "source knowledgeC.db (default ~/Library/Application Support/Knowledge/knowledgeC.db)")
	snapDir := flag.String("snapshot-dir", "local/snapshots", "where to preserve snapshots")
	days := flag.Int("days", 14, "how many recent days to break out per-day")
	skipSnap := flag.Bool("no-snapshot", false, "analyse the newest existing snapshot instead of taking a new one")
	flag.Parse()

	if *src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("cannot determine home directory: %v", err)
		}
		*src = filepath.Join(home, "Library/Application Support/Knowledge/knowledgeC.db")
	}

	var snap string
	var err error
	if *skipSnap {
		snap, err = newestSnapshot(*snapDir)
	} else {
		snap, err = takeSnapshot(*src, *snapDir)
	}
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("analysing %s\n\n", snap)

	// mode=ro on a snapshot we own: no risk to the live store either way, but
	// it documents intent and prevents an accidental schema migration.
	db, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		fatal("open snapshot: %v", err)
	}
	defer db.Close()

	reportStreams(db)
	usage := loadUsage(db)
	reportRetention(usage)
	reportPerApp(usage)
	reportPerDay(usage, *days)
	reportCreative(usage)
}

// takeSnapshot copies the database with its WAL and SHM sidecars. Copying the
// db alone silently drops the most recent events, which are exactly the ones
// worth having.
func takeSnapshot(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102")
	dst := filepath.Join(dir, "knowledgeC-"+stamp+".db")

	for _, ext := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(src + ext); err != nil {
			continue // sidecars are not always present
		}
		if err := copyFile(src+ext, dst+ext); err != nil {
			return "", fmt.Errorf("snapshot %s: %w (Full Disk Access granted?)", src+ext, err)
		}
	}
	return dst, nil
}

func newestSnapshot(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "knowledgeC-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no snapshots in %s", dir)
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func reportStreams(db *sql.DB) {
	rows, err := db.Query(`
		SELECT ZSTREAMNAME, COUNT(*)
		FROM ZOBJECT GROUP BY 1 ORDER BY 2 DESC LIMIT 12`)
	if err != nil {
		fatal("query streams: %v", err)
	}
	defer rows.Close()

	fmt.Println("== STREAMS ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STREAM\tROWS")
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			continue
		}
		fmt.Fprintf(w, "%s\t%d\n", name, n)
	}
	w.Flush()
	fmt.Println()
}

func loadUsage(db *sql.DB) []interval {
	rows, err := db.Query(`
		SELECT ZVALUESTRING, ZSTARTDATE, ZENDDATE
		FROM ZOBJECT
		WHERE ZSTREAMNAME = '/app/usage'
		  AND ZVALUESTRING IS NOT NULL
		  AND ZENDDATE > ZSTARTDATE
		ORDER BY ZSTARTDATE`)
	if err != nil {
		fatal("query usage: %v", err)
	}
	defer rows.Close()

	var out []interval
	for rows.Next() {
		var bundle string
		var start, end float64
		if err := rows.Scan(&bundle, &start, &end); err != nil {
			continue
		}
		out = append(out, interval{
			bundle: bundle,
			start:  time.Unix(int64(start)+coreDataEpoch, 0),
			end:    time.Unix(int64(end)+coreDataEpoch, 0),
		})
	}
	return out
}

// reportRetention is the finding that constrains the whole design: how far
// back this source actually reaches.
func reportRetention(usage []interval) {
	if len(usage) == 0 {
		fmt.Println("== RETENTION ==\nno /app/usage rows")
		return
	}
	earliest, latest := usage[0].start, usage[0].end
	for _, i := range usage {
		if i.start.Before(earliest) {
			earliest = i.start
		}
		if i.end.After(latest) {
			latest = i.end
		}
	}
	span := latest.Sub(earliest)

	fmt.Println("== RETENTION ==")
	fmt.Printf("rows      %d\n", len(usage))
	fmt.Printf("earliest  %s\n", earliest.Format("2006-01-02 15:04"))
	fmt.Printf("latest    %s\n", latest.Format("2006-01-02 15:04"))
	fmt.Printf("span      %.1f days\n", span.Hours()/24)
	fmt.Println()
	fmt.Printf("*** This source cannot backfill beyond %s. Everything earlier is\n", earliest.Format("2006-01-02"))
	fmt.Printf("*** already gone. A harvester is mandatory, not an optimisation.\n")
	fmt.Println()
}

func reportPerApp(usage []interval) {
	byApp := map[string]time.Duration{}
	counts := map[string]int{}
	for _, i := range usage {
		byApp[i.bundle] += i.dur()
		counts[i.bundle]++
	}

	type row struct {
		bundle string
		d      time.Duration
		n      int
	}
	var rows []row
	for b, d := range byApp {
		rows = append(rows, row{b, d, counts[b]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].d > rows[j].d })

	fmt.Println("== APPS BY SUMMED TIME ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUNDLE\tROWS\tHOURS")
	for i, r := range rows {
		if i >= 15 {
			break
		}
		fmt.Fprintf(w, "%s\t%d\t%.2f\n", r.bundle, r.n, r.d.Hours())
	}
	w.Flush()
	fmt.Println()
}

// reportPerDay demonstrates the engaged/presence distinction on real data.
// Summed time counts every app separately; union time merges overlaps into
// wall clock. Their ratio is the parallelism factor.
func reportPerDay(usage []interval, days int) {
	byDay := map[string][]interval{}
	for _, i := range usage {
		d := i.start.Format("2006-01-02")
		byDay[d] = append(byDay[d], i)
	}

	var keys []string
	for d := range byDay {
		keys = append(keys, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if len(keys) > days {
		keys = keys[:days]
	}

	fmt.Println("== PER DAY: SUMMED (engaged) vs UNION (presence) ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DAY\tSUMMED\tPRESENCE\tRATIO\tAPPS\tFIRST\tLAST")
	for _, d := range keys {
		ivs := byDay[d]
		var summed time.Duration
		apps := map[string]bool{}
		first, last := ivs[0].start, ivs[0].end
		for _, i := range ivs {
			summed += i.dur()
			apps[i.bundle] = true
			if i.start.Before(first) {
				first = i.start
			}
			if i.end.After(last) {
				last = i.end
			}
		}
		presence := union(ivs)
		fmt.Fprintf(w, "%s\t%.1fh\t%.1fh\t%.2fx\t%d\t%s\t%s\n",
			d, summed.Hours(), presence.Hours(), summed.Hours()/presence.Hours(),
			len(apps), first.Format("15:04"), last.Format("15:04"))
	}
	w.Flush()
	fmt.Println()
}

// union merges overlapping intervals and returns total wall-clock coverage.
func union(ivs []interval) time.Duration {
	if len(ivs) == 0 {
		return 0
	}
	sorted := append([]interval(nil), ivs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })

	var total time.Duration
	curStart, curEnd := sorted[0].start, sorted[0].end
	for _, i := range sorted[1:] {
		if i.start.After(curEnd) {
			total += curEnd.Sub(curStart)
			curStart, curEnd = i.start, i.end
			continue
		}
		if i.end.After(curEnd) {
			curEnd = i.end
		}
	}
	return total + curEnd.Sub(curStart)
}

// reportCreative answers the question the acorn/ acceptance test depends on:
// is there any app-focus evidence for work that leaves no file trace?
func reportCreative(usage []interval) {
	fmt.Println("== CREATIVE APPS ==")
	found := false
	for bundle, name := range creativeApps {
		var d time.Duration
		var n int
		var first, last time.Time
		for _, i := range usage {
			if i.bundle != bundle {
				continue
			}
			d += i.dur()
			n++
			if first.IsZero() || i.start.Before(first) {
				first = i.start
			}
			if i.end.After(last) {
				last = i.end
			}
		}
		if n == 0 {
			continue
		}
		found = true
		fmt.Printf("%-14s %5.2fh across %d intervals (%s → %s)\n",
			name, d.Hours(), n, first.Format("01-02 15:04"), last.Format("01-02 15:04"))
	}
	if !found {
		fmt.Println("none present in the retained window")
	}
	fmt.Println()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
