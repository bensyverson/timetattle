// Command filedays counts file modifications per day under a directory.
//
// It exists because raw file counts over a repository worked on by agents are
// badly inflated. Each agent worktree holds a complete copy of the tree, so
// one edited file can be counted twenty-one times, and a day's apparent
// "volume of work" becomes a measure of how many worktrees happened to exist.
//
// The spike's finding that 11 July 2026 touched 1,773 files in the acorn
// repository was derived without that exclusion. This tool reports filtered
// and unfiltered counts side by side so the inflation is visible rather than
// argued about.
//
// File mtimes remain useful as DAY MARKERS — evidence that work happened on a
// date git cannot see — but the counts should not be read as volume even after
// filtering, since a single save can rewrite many files.
//
// The scan itself lives in internal/filedays, because `timetattle harvest`
// needs the identical logic to capture these counts on a schedule (see job
// task xONGg): a count with no observation time attached decays invisibly,
// the same way the mtimes it is built from do, so it has to be captured
// rather than only read on demand the way this command reads it.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bensyverson/timetattle/internal/filedays"
)

func main() {
	root := flag.String("root", ".", "directory to scan")
	exclude := flag.String("exclude", strings.Join(filedays.DefaultExcludes, ","),
		"comma-separated directory names to skip entirely")
	flag.Parse()

	excludes := strings.Split(*exclude, ",")

	raw, err := scan(*root, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filedays: %v\n", err)
		os.Exit(1)
	}
	filtered, err := scan(*root, excludes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filedays: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("scanning %s\n", *root)
	fmt.Printf("excluding: %s\n\n", strings.Join(excludes, ", "))
	fmt.Printf("%-12s %10s %10s %10s\n", "DAY", "RAW", "FILTERED", "INFLATION")

	days := filedays.SortedDays(raw)

	var totalRaw, totalFiltered int
	for _, d := range days {
		r, f := raw[d], filtered[d]
		totalRaw += r
		totalFiltered += f
		fmt.Printf("%-12s %10d %10d %10s\n", d, r, f, ratio(r, f))
	}
	fmt.Printf("\n%-12s %10d %10d %10s\n", "TOTAL", totalRaw, totalFiltered, ratio(totalRaw, totalFiltered))
	fmt.Printf("\n%d days with activity after filtering\n", countNonZero(filtered))
}

// ratio expresses how much the unfiltered count overstates the filtered one.
func ratio(raw, filtered int) string {
	if filtered == 0 {
		if raw == 0 {
			return "-"
		}
		return "all"
	}
	return fmt.Sprintf("%.1fx", float64(raw)/float64(filtered))
}

func countNonZero(days map[string]int) int {
	var n int
	for _, c := range days {
		if c > 0 {
			n++
		}
	}
	return n
}

// scan delegates to internal/filedays, which the harvester also uses so both
// tools count a day of work the same way. Kept as a thin wrapper, rather than
// calling filedays.Scan directly at each call site, purely so this package's
// existing tests keep working unmodified — see main_test.go, which predates
// this refactor and exercises scan() as the scanning logic itself.
func scan(root string, excludes []string) (map[string]int, error) {
	return filedays.Scan(root, excludes)
}
