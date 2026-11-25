// Command buildoutput measures what leaving Swift build output out of a store's
// figures changes. It prints counts and hours only, never a locator, project or
// client name, so its output can be published.
//
// Two independent parts, each optional.
//
// -store counts filesystem observations, and how many of them are build output:
//
//	go run ./scripts/buildoutput -store copy.db [-from 2026-08-27]
//
// -before and -after compare two `timetattle metrics -all -by span -csv`
// captures of the same store over the same window, one from a binary built
// before the rule and one from a binary built with it:
//
//	go run ./scripts/buildoutput -before before.csv -after after.csv
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/bensyverson/timetattle/internal/buildoutput"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	storePath := flag.String("store", "", "event store to count filesystem observations in")
	from := flag.String("from", "", "count observations from this date (2006-01-02), local time")
	before := flag.String("before", "", "metrics CSV from before the rule")
	after := flag.String("after", "", "metrics CSV from after the rule")
	flag.Parse()
	if err := run(os.Stdout, *storePath, *from, *before, *after); err != nil {
		fmt.Fprintln(os.Stderr, "buildoutput:", err)
		os.Exit(1)
	}
}

func run(out io.Writer, storePath, from, before, after string) error {
	if storePath == "" && (before == "" || after == "") {
		return fmt.Errorf("give -store, or both -before and -after")
	}
	if storePath != "" {
		if err := count(out, storePath, from); err != nil {
			return err
		}
	}
	if before == "" && after == "" {
		return nil
	}
	b, err := os.Open(before)
	if err != nil {
		return err
	}
	defer b.Close()
	a, err := os.Open(after)
	if err != nil {
		return err
	}
	defer a.Close()
	return compare(out, b, a)
}

func count(out io.Writer, storePath, from string) error {
	filter := store.Filter{Source: buildoutput.Source}
	if from != "" {
		at, err := time.ParseInLocation(time.DateOnly, from, time.Local)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		filter.From = at
	}
	st, err := store.OpenReadOnly(storePath)
	if err != nil {
		return err
	}
	defer st.Close()
	events, err := st.Events(context.Background(), filter)
	if err != nil {
		return err
	}

	all, built := map[string]bool{}, map[string]bool{}
	var builtEvents int
	for _, e := range events {
		all[e.Locator] = true
		if buildoutput.Is(e) {
			builtEvents++
			built[e.Locator] = true
		}
	}
	fmt.Fprintf(out, "%-32s %10s %10s %10s\n", "filesystem observations", "before", "after", "build")
	fmt.Fprintf(out, "%-32s %10d %10d %10d\n", "observations", len(events), len(events)-builtEvents, builtEvents)
	fmt.Fprintf(out, "%-32s %10d %10d %10d\n", "distinct files", len(all), len(all)-len(built), len(built))
	return nil
}

// totals is one metrics capture reduced to what can be printed, plus the
// per-project hours needed to count what moved.
type totals struct {
	rows     map[string]float64
	projects map[string]float64
}

const (
	engagedRow   = "engaged, summed over projects"
	allocatedRow = "allocated, summed over projects"
)

func read(r io.Reader) (totals, error) {
	t := totals{rows: map[string]float64{}, projects: map[string]float64{}}
	records, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return t, err
	}
	if len(records) == 0 {
		return t, fmt.Errorf("an empty capture")
	}
	column := map[string]int{}
	for i, name := range records[0] {
		column[name] = i
	}
	for _, name := range []string{"level", "name", "seconds", "allocated_seconds"} {
		if _, ok := column[name]; !ok {
			return t, fmt.Errorf("no %q column; this is not a `metrics -csv` capture", name)
		}
	}
	hours := func(record []string, name string) float64 {
		// An empty cell is a figure the level does not have.
		seconds, _ := strconv.ParseFloat(record[column[name]], 64)
		return seconds / 3600
	}
	for _, record := range records[1:] {
		name := record[column["name"]]
		switch record[column["level"]] {
		case "presence":
			t.rows["presence"] += hours(record, "seconds")
		case "unattributed":
			// These names are the report's own bucket labels, not anybody's.
			t.rows["unattributed: "+name] += hours(record, "seconds")
		case "project":
			t.projects[name] += hours(record, "seconds")
			t.rows[engagedRow] += hours(record, "seconds")
			t.rows[allocatedRow] += hours(record, "allocated_seconds")
		}
	}
	return t, nil
}

func compare(out io.Writer, before, after io.Reader) error {
	b, err := read(before)
	if err != nil {
		return fmt.Errorf("before: %w", err)
	}
	a, err := read(after)
	if err != nil {
		return fmt.Errorf("after: %w", err)
	}

	labels := maps.Clone(b.rows)
	maps.Copy(labels, a.rows)
	fmt.Fprintf(out, "%-36s %10s %10s %10s\n", "hours", "before", "after", "change")
	for _, label := range slices.Sorted(maps.Keys(labels)) {
		fmt.Fprintf(out, "%-36s %10.1f %10.1f %+10.1f\n", label, b.rows[label], a.rows[label], a.rows[label]-b.rows[label])
	}

	names := maps.Clone(b.projects)
	maps.Copy(names, a.projects)
	var changed, gone int
	var fall, rise float64
	for name := range names {
		delta := a.projects[name] - b.projects[name]
		// A tenth of an hour is the resolution every figure here is printed at.
		if math.Abs(delta) >= 0.05 {
			changed++
		}
		if _, ok := a.projects[name]; !ok {
			gone++
		}
		fall, rise = max(fall, -delta), max(rise, delta)
	}
	fmt.Fprintf(out, "\nprojects with a row: %d before, %d after\n", len(b.projects), len(a.projects))
	fmt.Fprintf(out, "changed: %d, of which %d no longer appear\n", changed, gone)
	fmt.Fprintf(out, "largest fall in one project's engaged hours: %.1f\n", fall)
	fmt.Fprintf(out, "largest rise in one project's engaged hours: %.1f\n", rise)
	return nil
}
