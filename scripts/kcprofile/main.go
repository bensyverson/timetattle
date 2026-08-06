// Command kcprofile reports what a knowledgeC database or Timetattle
// snapshot actually contains: per-stream retention, how current each stream
// is, and an inventory of sensitive fields.
//
// It exists to be re-run. Retention windows drift, new streams appear after
// macOS upgrades, and /app/usage has been observed to stall — it sat at
// exactly 2026-07-27 14:28 for over a day while other streams stayed current
// to within hours. That stall is the largest standing risk to app
// attribution, and a lag that ever exceeded the ~9-day retention would mean
// rows are pruned before they are ever written. This is how we watch for it.
//
// Like the probes in scripts/spike, this is an analysis tool rather than
// product behaviour and is exempt from the project's red/green TDD rule.
//
// Usage:
//
//	go run ./scripts/kcprofile                      # newest snapshot
//	go run ./scripts/kcprofile -db path/to/kc.db    # a specific database
//	go run ./scripts/kcprofile -db snap.db.gz       # a gzipped snapshot
package main

import (
	"compress/gzip"
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

	"github.com/bensyverson/timetattle/internal/snapshot"
)

// coreDataEpoch is 2001-01-01 UTC; Core Data stores seconds from there.
const coreDataEpoch = 978307200

// sensitiveColumns are ZSTRUCTUREDMETADATA fields a time tracker has no use
// for. Timetattle's snapshots drop this table entirely; this inventory is
// what justifies that, and flags anything new that appears.
var sensitiveColumns = []string{
	"Z_DKDIGITALHEALTHMETADATAKEY__WEBPAGEURL",
	"Z_DKDIGITALHEALTHMETADATAKEY__WEBDOMAIN",
	"Z_DKINTENTMETADATAKEY__SERIALIZEDINTERACTION",
	"Z_DKINTENTMETADATAKEY__RELATEDCONTACTIDENTIFIERS",
	"Z_DKNOTIFICATIONUSAGEMETADATAKEY__IDENTIFIER",
	"Z_DKAPPMEDIAUSAGEMETADATAKEY__URL",
}

func main() {
	dbPath := flag.String("db", "", "database or .gz snapshot to profile (default: newest snapshot)")
	flag.Parse()

	path := *dbPath
	if path == "" {
		var err error
		if path, err = newestSnapshot(); err != nil {
			fatal("%v", err)
		}
	}

	if strings.HasSuffix(path, ".gz") {
		plain, cleanup, err := decompress(path)
		if err != nil {
			fatal("decompress %s: %v", path, err)
		}
		defer cleanup()
		fmt.Printf("profiling %s (decompressed)\n\n", path)
		path = plain
	} else {
		fmt.Printf("profiling %s\n\n", path)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	reportStreams(db)
	reportSensitive(db)
}

func newestSnapshot() (string, error) {
	dir, err := snapshot.DefaultDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w (run `timetattle harvest` first)", dir, err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db.gz") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no snapshots in %s (run `timetattle harvest` first)", dir)
	}
	sort.Strings(names) // timestamped names sort chronologically
	return filepath.Join(dir, names[len(names)-1]), nil
}

func decompress(path string) (string, func(), error) {
	in, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer in.Close()

	zr, err := gzip.NewReader(in)
	if err != nil {
		return "", nil, err
	}
	defer zr.Close()

	out, err := os.CreateTemp("", "kcprofile-*.db")
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", nil, err
	}
	out.Close()
	return out.Name(), func() { os.Remove(out.Name()) }, nil
}

// reportStreams is the stall monitor. The AGE column is what matters: a
// stream whose newest row is a day old while others are current is not
// batching, it has stopped.
func reportStreams(db *sql.DB) {
	rows, err := db.Query(`
		SELECT ZSTREAMNAME, COUNT(*), MIN(ZSTARTDATE), MAX(ZSTARTDATE)
		FROM ZOBJECT WHERE ZSTREAMNAME IS NOT NULL
		GROUP BY 1 ORDER BY COUNT(*) DESC`)
	if err != nil {
		fatal("query streams: %v", err)
	}
	defer rows.Close()

	fmt.Println("== STREAMS ==")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STREAM\tROWS\tOLDEST\tNEWEST\tSPAN\tAGE")
	now := time.Now()
	for rows.Next() {
		var name string
		var n int
		var oldest, newest float64
		if err := rows.Scan(&name, &n, &oldest, &newest); err != nil {
			continue
		}
		o := time.Unix(int64(oldest)+coreDataEpoch, 0)
		nw := time.Unix(int64(newest)+coreDataEpoch, 0)
		age := now.Sub(nw)

		flag := ""
		if age > 24*time.Hour {
			flag = "  <-- STALLED?"
		}
		// Include the year: /discoverability/signals spans back to 2024, and
		// without it the oldest row appears to postdate the newest.
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%.1fd\t%.1fh%s\n",
			name, n, o.Format("2006-01-02 15:04"), nw.Format("2006-01-02 15:04"),
			nw.Sub(o).Hours()/24, age.Hours(), flag)
	}
	w.Flush()
	fmt.Println()
	fmt.Println("AGE is how stale a stream is. A stream over 24h old while others are")
	fmt.Println("current has stopped writing. If a stall ever exceeds the retention")
	fmt.Println("window, rows are pruned before they are written and the source is dead.")
	fmt.Println()
}

// reportSensitive inventories what a raw database holds and a snapshot does
// not. On a Timetattle snapshot every count should be absent.
func reportSensitive(db *sql.DB) {
	fmt.Println("== SENSITIVE FIELDS (dropped from Timetattle snapshots) ==")

	var present int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ZSTRUCTUREDMETADATA'`,
	).Scan(&present); err != nil || present == 0 {
		fmt.Println("ZSTRUCTUREDMETADATA absent — this is a Timetattle snapshot, as intended.")
		fmt.Println()
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FIELD\tROWS")
	for _, col := range sensitiveColumns {
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM ZSTRUCTUREDMETADATA WHERE "%s" IS NOT NULL`, col)
		if err := db.QueryRow(q).Scan(&n); err != nil {
			continue // column absent on this macOS version
		}
		if n > 0 {
			fmt.Fprintf(w, "%s\t%d\n", strings.TrimPrefix(col, "Z_DK"), n)
		}
	}
	w.Flush()
	fmt.Println()
	fmt.Println("This is a raw database. Timetattle snapshots discard all of the above.")
	fmt.Println()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
