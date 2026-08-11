package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The series: day, week and month views over an arbitrary span, and the CSV a
// dashboard reads.
//
// EVERY TEST HERE PINS THE ZONE. A working day is a LOCAL concept and these
// tests run on whatever machine happens to hold them, so a fixture written
// against the developer's zone would pass in Chicago and put the same events on
// different days in Auckland.

// seedThreeWorkingDays returns a store holding evidence on the first and third
// of three consecutive UTC working days, with the middle one EMPTY.
//
// The empty day is the fixture's whole point. A report that simply listed the
// days it found evidence on would pass every other assertion here and would
// silently drop the day nothing happened on, which is the difference between
// "no work" and "no row" — and this project has shipped the collapsed version
// of that distinction three times.
func seedThreeWorkingDays(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	var events []source.Event
	owners := []struct {
		client, project string
		at              time.Time
	}{
		{"Sondermark", "Holiday Campaign", time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)},
		{"Larkfield", "Recruiting Site", time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)},
	}
	for _, o := range owners {
		dir := t.TempDir()
		if _, err := registry.Register(registry.Registration{
			Dir: dir, Client: o.client, Project: o.project,
		}); err != nil {
			t.Fatalf("Register %s: %v", o.project, err)
		}
		for _, offset := range []time.Duration{0, 3 * time.Minute} {
			e := source.NewPoint(o.at.Add(offset), source.Identity{Document: "notes.md"},
				"claude-transcripts")
			e.Locator = dir
			events = append(events, e)
		}
	}

	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	if err := resolveProjects(ctx, st, nil, &strings.Builder{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	return path
}

// runSeries is metrics over the three-day fixture, in UTC, with the flags a
// series needs.
func runSeries(t *testing.T, path string, args ...string) string {
	t.Helper()
	base := []string{"-store", path, "-config", filepath.Join(t.TempDir(), "none.json"),
		"-timezone", "UTC", "-from", "2026-07-19T05:00:00Z", "-to", "2026-07-23T05:00:00Z", "-all"}
	return runMetrics(t, append(base, args...)...)
}

// ABSENCE OF A ROW IS NOT AN OBSERVATION. Every working day the span covers
// gets a row, including the one nothing fell on.
func TestByDayReportsEveryDayInTheSpanIncludingTheEmptyOnes(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day")

	for _, day := range []string{"2026-07-19", "2026-07-20", "2026-07-21", "2026-07-22"} {
		if !strings.Contains(out, day) {
			t.Errorf("the series has no row for %s:\n%s", day, out)
		}
	}
}

// REGRESSION. -to is the window's closing instant and runSeries passes the
// instant a working day BEGINS at (05:00, the fixture's day start) rather than
// one inside the last requested day, exactly as dashboardPeriod's own To does
// (see dashboard.go's doc comment: "half-open instant window: [From, To)").
// seriesOf must therefore treat that instant as the boundary the window ends
// AT rather than a moment inside one more day to report on; a day beyond the
// four the window actually asked for is not "the calendar wearing no
// disguise", it is the series inventing a day nobody requested.
func TestByDayDoesNotReportADayBeyondTheRequestedWindow(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day")

	if strings.Contains(out, "2026-07-23") {
		t.Errorf("the series reports 2026-07-23, one day past the window asked for "+
			"(2026-07-19T05:00:00Z to 2026-07-23T05:00:00Z):\n%s", out)
	}
}

// THE RECONCILIATION, STATED BY THE REPORT ITSELF. The rows are cut out of the
// same segments the ungrouped figures are summed from, so they sum to it
// exactly; printing the difference is what makes that checkable rather than
// asserted.
func TestByDayReconcilesItsRowsAgainstTheUngroupedTotal(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day")

	if !strings.Contains(out, "difference 0s") {
		t.Errorf("the series does not reconcile to zero against the ungrouped total:\n%s", out)
	}
	if strings.Contains(out, "WARNING: the per-period figures do not sum") {
		t.Errorf("the series says its own rows do not sum to the total:\n%s", out)
	}
}

// A day and a week and a month over the same span cover the same clock. They
// are three cuts of one partition and not three computations, so this is an
// equality rather than an approximation.
func TestDayWeekAndMonthCoverTheSameClock(t *testing.T) {
	path := seedThreeWorkingDays(t)

	var totals []string
	for _, unit := range []string{"day", "week", "month"} {
		out := runSeries(t, path, "-by", unit)
		if !strings.Contains(out, "difference 0s") {
			t.Errorf("the %s series does not reconcile:\n%s", unit, out)
		}
		totals = append(totals, seriesTotalLine(t, out))
	}
	for i := 1; i < len(totals); i++ {
		if totals[i] != totals[0] {
			t.Errorf("the series totals differ between units: %q against %q", totals[i], totals[0])
		}
	}
}

// seriesTotalLine pulls the "N.Nh across ..." reconciliation sentence out of a
// report, minus the unit name, so three units can be compared.
func seriesTotalLine(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "against an ungrouped") {
			fields := strings.Fields(line)
			return fields[0] + " " + strings.Join(fields[len(fields)-4:], " ")
		}
	}
	t.Fatalf("the report carries no reconciliation line at all:\n%s", out)
	return ""
}

// A unit nobody implemented must be refused by name rather than silently
// falling back to one that was.
func TestByRefusesAUnitItDoesNotKnow(t *testing.T) {
	var out strings.Builder
	err := metricsTo(&out, []string{"-store", seedThreeWorkingDays(t),
		"-config", filepath.Join(t.TempDir(), "none.json"), "-by", "fortnight"})
	if err == nil {
		t.Fatal("metrics accepted -by fortnight; it must refuse a unit it cannot bucket")
	}
	if !strings.Contains(err.Error(), "fortnight") {
		t.Errorf("the error does not name what was asked for: %v", err)
	}
}

// The CSV is a data interchange format and the web dashboard is its consumer,
// so it carries a header and one row per period per grouping.
func TestTheCSVCarriesAHeaderAndOneRowPerPeriodPerGrouping(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day", "-csv")

	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	if len(rows) < 2 {
		t.Fatalf("the CSV has %d rows; it carries no data at all:\n%s", len(rows), out)
	}
	head := rows[0]
	for _, want := range []string{"period", "start", "end", "unit", "level", "name", "seconds"} {
		if !hasColumn(head, want) {
			t.Errorf("the CSV header has no %q column: %v", want, head)
		}
	}
	// Every period gets a presence row whether or not anything happened in it.
	presence := map[string]bool{}
	for _, row := range rows[1:] {
		if cell(head, row, "level") == "presence" {
			presence[cell(head, row, "period")] = true
		}
	}
	for _, day := range []string{"2026-07-19", "2026-07-20", "2026-07-21", "2026-07-22"} {
		if !presence[day] {
			t.Errorf("the CSV has no presence row for %s:\n%s", day, out)
		}
	}
}

// THE CSV AND THE TABLE MUST BE THE SAME NUMBERS. Two renderings of one report
// that nobody has subtracted are two figures, and this project's rule is that
// two tools measuring the same thing are reconciled numerically.
func TestTheCSVCarriesTheSameFiguresAsTheTable(t *testing.T) {
	path := seedThreeWorkingDays(t)
	table := runSeries(t, path, "-by", "day")
	out := runSeries(t, path, "-by", "day", "-csv")

	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	var presence time.Duration
	for _, row := range rows[1:] {
		if cell(head, row, "level") != "presence" {
			continue
		}
		presence += parseSeconds(t, cell(head, row, "seconds"))
	}
	// The table quotes hours to one decimal, so the comparison is made at the
	// table's precision rather than the CSV's: the CSV may not lose precision
	// and is allowed to carry more.
	want := metricsHours(presence)
	if !strings.Contains(table, want+" across") {
		t.Errorf("the CSV sums to %s of presence, which the table's reconciliation line does not "+
			"carry:\n%s", want, table)
	}
}

// A ZERO AND A FIGURE THAT DOES NOT APPLY ARE DIFFERENT CELLS. A presence row
// has no allocation, which is not an allocation of none, and a consumer that
// read the second as the first would build a chart of confident zeroes.
func TestTheCSVLeavesAFigureThatDoesNotApplyEmptyRatherThanZero(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day", "-csv")

	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	for _, row := range rows[1:] {
		if cell(head, row, "level") != "presence" {
			continue
		}
		if got := cell(head, row, "allocated_seconds"); got != "" {
			t.Errorf("a presence row carries %q in allocated_seconds; presence is not allocated to "+
				"anybody and a figure there would be an invention", got)
		}
		return
	}
	t.Fatal("the CSV holds no presence row at all")
}

// A PERIOD WITH NOTHING IN IT IS AMBIGUOUS AND THE REPORT MUST SAY SO. A zero
// can mean nobody worked or can mean nothing was collected, and the store knows
// which only where a capture or a harvest left a row. Where it left none, the
// report says the question is unanswered rather than printing a bare zero.
func TestAPeriodWithNoCollectionRecordSaysSoRatherThanPrintingABareZero(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day")

	if !strings.Contains(out, "unrecorded") {
		t.Errorf("the series does not say that collection was unrecorded on days no run covers:\n%s", out)
	}
	if !strings.Contains(out, "cannot be told apart") {
		t.Errorf("the series does not warn that a zero on such a period is ambiguous:\n%s", out)
	}
}

// And where a capture DID run, the period says so — otherwise the column would
// carry one state and pretend to carry three.
func TestAPeriodACaptureRanInSaysCollectionWasWorking(t *testing.T) {
	path := seedThreeWorkingDays(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	at := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	if _, err := st.RecordCapture(context.Background(), store.Capture{
		Kind: "knowledgec", Input: "/tmp/knowledgeC.db", StartedAt: at,
		FinishedAt: at.Add(time.Second), Status: store.CaptureOK,
	}); err != nil {
		t.Fatalf("record a capture: %v", err)
	}
	st.Close()

	out := runSeries(t, path, "-by", "day")
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "2026-07-21") {
			continue
		}
		if strings.Contains(line, "unrecorded") {
			t.Errorf("the day a capture ran on still reads as unrecorded: %q", line)
		}
		return
	}
	t.Fatalf("no row for 2026-07-21 at all:\n%s", out)
}

// A FILTER MATCHING NOTHING MUST NOT THEN PRINT A SERIES OF ZEROES.
//
// The unfiltered report already refuses to collapse "this project was engaged
// for no time", "this project has no evidence in this span" and "there is no
// such project" into one table of zeroes, and says which it is. A series
// printed underneath that sentence would put the collapsed answer back on the
// screen immediately below the corrected one.
func TestASeriesIsNotPrintedForAScopeThatMatchedNothing(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day", "-project", "Nobody's Project")

	if !strings.Contains(out, "nothing in this span was attributed to") {
		t.Fatalf("the report does not say the filter matched nothing:\n%s", out)
	}
	if strings.Contains(out, "BY WORKING DAY") {
		t.Errorf("a series of zeroes was printed for a project that does not exist:\n%s", out)
	}
}

// The CSV honours the same filter the table does, and for the same reason: it
// selects rows and recomputes nothing, so presence stays the whole span's wall
// clock while the project rows are the ones asked for.
func TestTheCSVHonoursTheSameFilterTheTableDoes(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "day", "-csv",
		"-project", "Recruiting Site")

	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	var presence, kept int
	for _, row := range rows[1:] {
		switch cell(head, row, "level") {
		case "presence":
			presence++
		case "project":
			kept++
			if got := cell(head, row, "name"); got != "Recruiting Site" {
				t.Errorf("the CSV carries a project row for %q under a filter for another", got)
			}
		}
	}
	if kept == 0 {
		t.Error("the CSV carries no project row at all under a filter that matches one")
	}
	if presence == 0 {
		t.Error("the CSV dropped its presence rows under a project filter; presence is the whole " +
			"span's wall clock and is not a project's to filter")
	}
}

// A period the span only partly covers is not comparable with a whole one, and
// saying so is the difference between "8h that month" and "8h across the two
// days of it we asked for".
func TestAPartlyCoveredPeriodSaysThatItIsOne(t *testing.T) {
	out := runSeries(t, seedThreeWorkingDays(t), "-by", "month")

	if !strings.Contains(out, "partly covered") {
		t.Errorf("a month cut down to four days does not say it is partial:\n%s", out)
	}
}

// REGRESSION. The partial-period line pluralised the noun by handing plural()
// the string "working day is", so plural() appended its s to "is" and two
// partial periods read "2 working day iss". The count is right and the sentence
// carrying it is not a sentence, which is the kind of defect that survives
// because no assertion ever looked past the two words it matched on.
func TestAPartlyCoveredPeriodPluralisesTheNounAndNotTheVerb(t *testing.T) {
	for _, unit := range []string{"day", "week", "month"} {
		out := runSeries(t, seedThreeWorkingDays(t), "-by", unit)
		if strings.Contains(out, " iss") {
			t.Errorf("-by %s pluralises the verb rather than the noun:\n%s", unit, out)
		}
	}
}

// A WORKING DAY IS A LOCAL CONCEPT AND ITS ENDPOINTS MUST READ AS ONE. The
// period label is the local date, so a start rendered in UTC beside it invites
// a consumer to read "2026-07-20" and "2026-07-20T10:00:00Z" as disagreeing
// about the same day. Both are the same instant; only one of them says so
// legibly.
func TestTheCSVRendersPeriodEndpointsInTheCalendarsOwnZone(t *testing.T) {
	out := runMetrics(t, "-store", seedThreeWorkingDays(t),
		"-config", filepath.Join(t.TempDir(), "none.json"),
		"-timezone", "America/Chicago", "-by", "day", "-csv",
		"-from", "2026-07-19T05:00:00Z", "-to", "2026-07-23T05:00:00Z", "-all")

	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	start := cell(head, rows[1], "start")
	if !strings.HasSuffix(start, "-05:00") && !strings.HasSuffix(start, "-06:00") {
		t.Errorf("the period starts at %q, which is not written in the calendar's zone", start)
	}
	// And the label agrees with it, rather than naming a different date.
	if !strings.HasPrefix(start, cell(head, rows[1], "period")) {
		t.Errorf("the period is called %q and starts at %q; the two disagree about the date",
			cell(head, rows[1], "period"), start)
	}
}

func hasColumn(head []string, name string) bool {
	return slices.Contains(head, name)
}

func cell(head, row []string, name string) string {
	for i, h := range head {
		if h == name && i < len(row) {
			return row[i]
		}
	}
	return ""
}

func parseSeconds(t *testing.T, text string) time.Duration {
	t.Helper()
	if text == "" {
		return 0
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		t.Fatalf("the CSV carries %q where a number of seconds belongs: %v", text, err)
	}
	return time.Duration(f * float64(time.Second))
}

// A RE-SLICE MUST NOT LOSE A REASON THE REPORT KEEPS, and the export least of
// all: a program adding the unattributed rows up has no way to notice that a
// person's own accounting was quietly filed as clock nobody recorded.
//
// The asserted row is written on every period whether or not anything was
// corrected. A row that appeared only sometimes would make the export's schema
// a property of the data, and a program reading it would break the first time
// somebody reviewed a day.
func TestTheCSVCarriesAssertedUnattributedAsAReasonOfItsOwn(t *testing.T) {
	path := seedThreeWorkingDays(t)

	clean := runSeries(t, path, "-by", "day", "-csv")
	if names := csvReasonNames(t, clean); !slices.Contains(names, "asserted") {
		t.Fatalf("an export over a store nobody has corrected omits the asserted reason "+
			"altogether, so the schema depends on the data: %v", names)
	}

	// Half an hour of 2026-07-20, said by a person to have belonged to nobody.
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", path,
		"-from", "2026-07-20T10:00:00Z", "-to", "2026-07-20T10:30:00Z",
		"-assert", "unattributed", "-origin", "a-person"}); err != nil {
		t.Fatalf("adjust -assert unattributed: %v", err)
	}

	out := runSeries(t, path, "-by", "day", "-csv")
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	var asserted, bridged time.Duration
	for _, row := range rows[1:] {
		if cell(head, row, "level") != "unattributed" {
			continue
		}
		switch cell(head, row, "name") {
		case "asserted":
			asserted += parseSeconds(t, cell(head, row, "seconds"))
		case "bridged":
			bridged += parseSeconds(t, cell(head, row, "seconds"))
		}
	}
	if asserted == 0 {
		t.Errorf("the export carries no asserted time at all after half an hour was said to "+
			"belong to nobody:\n%s", out)
	}
	if bridged != 0 {
		t.Errorf("%v was exported as bridged silence; a person's assertion has been folded into "+
			"clock nothing was recorded in", bridged)
	}
}

// csvReasonNames is every unattributed reason the export writes, in order.
func csvReasonNames(t *testing.T, out string) []string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out)
	}
	head := rows[0]
	var names []string
	for _, row := range rows[1:] {
		if cell(head, row, "level") != "unattributed" {
			continue
		}
		if name := cell(head, row, "name"); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}
