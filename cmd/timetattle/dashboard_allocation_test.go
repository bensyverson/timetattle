package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The allocation and effective-rate panel — THE ONE SECTION THAT PRINTS MONEY.
//
// EVERY NAME AND EVERY FIGURE BELOW IS INVENTED. "Northwind", "Atlas" and
// "Bluebird" are nobody's clients; 150, 30, 24000 and 160 are round numbers
// chosen so the arithmetic can be checked by hand. A real rate written into a
// fixture is a real rate published, which is criterion 6vM and the reason
// `go run ./scripts/confidential -names <names> -gate .` exists.
//
// Assertions are on PLAIN text under colorNone, which is what a captured run
// produces (dashboard_test.go's own note on why a buffer is never a terminal),
// except the one test that exists to prove painting happens at all. A section
// is free to paint; it is never allowed to make painting load-bearing for what
// it says.

// allocDay is the fixed instant these fixtures are dated against, so no test
// depends on the day it is run.
var allocDay = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// f64 is an inline *float64, which is how config.Engagement spells "stated"
// as against "unsaid" — see internal/config/engagements.go on why every money
// field is a pointer. A currency is spelled new("$") directly; a figure needs
// this, because new(150) is a *int.
func f64(v float64) *float64 { return new(v) }

// hourlyEngagement is shape (a): 30h a week at 150 an hour.
func hourlyEngagement() config.Engagement {
	return config.Engagement{
		Currency:      new("$"),
		Rate:          f64(150),
		BudgetedHours: f64(30),
		Period:        config.BudgetPerWeek,
	}
}

// contractEngagement is shape (b): 24000 fixed for 160 expected hours.
func contractEngagement() config.Engagement {
	return config.Engagement{
		Currency:      new("$"),
		ContractTotal: f64(24000),
		ExpectedHours: f64(160),
	}
}

// allocConfig is the default config with engagements bolted on, so every other
// parameter (the calendar above all, which the pro-ration is cut by) is
// exactly what a real run uses.
func allocConfig(clients, projects map[string]config.Engagement) *config.Config {
	c := config.Default()
	c.Engagements = config.Engagements{Clients: clients, Projects: projects}
	return c
}

// allocInterval is one observed span of work on a project — an interval and
// not a point, because an interval's engaged total is its own length plus the
// configured lead-in and tail-out (5m each by default), which makes every
// expected figure below derivable rather than guessed: a 110-minute interval
// is 2.0 engaged hours.
func allocInterval(start time.Time, d time.Duration, client, project string) source.Event {
	e := source.NewInterval(start, start.Add(d), source.Identity{Client: client, Project: project}, gitlog.CommitsID)
	e.Locator = "/w/" + project
	e.Cause = source.CauseUserAction
	return e
}

// allocData assembles the handle the way dashboardCmd does for a window whose
// events are all in hand: Report is metrics.Compute over exactly Events, and
// Filter states the same window.
func allocData(t *testing.T, cfg *config.Config, p dashboardPeriod, events []source.Event) *dashboardData {
	t.Helper()
	params := sessions.Params{
		Gap:             cfg.Sessions.Gap.Duration(),
		LeadIn:          cfg.Sessions.LeadIn.Duration(),
		TailOut:         cfg.Sessions.TailOut.Duration(),
		PresenceSources: cfg.Sessions.PresenceSources,
		IgnoreSources:   cfg.Sessions.IgnoreSources,
	}
	report, err := metrics.Compute(events, params)
	if err != nil {
		t.Fatalf("metrics.Compute: %v", err)
	}
	return &dashboardData{
		Config: cfg,
		Params: params,
		Filter: store.Filter{From: p.From, To: p.To},
		Events: events,
		Report: report,
		Log:    collectedThroughout(p),
	}
}

func renderAllocation(t *testing.T, scope metricsScope, p dashboardPeriod, cc colorCapability,
	data *dashboardData) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderAllocationRate(context.Background(), &out, scope, p, cc, data); err != nil {
		t.Fatalf("renderAllocationRate: %v", err)
	}
	return out.String()
}

// A window of exactly n whole working days ending on allocDay.
func allocPeriod(t *testing.T, days int) dashboardPeriod {
	t.Helper()
	return dashboardPeriodOf(days, mustCalendar(t), allocDay)
}

// ─── nothing configured ──────────────────────────────────────────────────────

// MOST PROJECTS ARE UNBILLED PERSONAL WORK. The registered mix says so, and
// the section a person sees on the overwhelming majority of runs is this one:
// a single quiet line. Not a zero budget, not a zero rate, and not an error.
func TestAllocationSaysNothingIsConfiguredRatherThanPrintingZeros(t *testing.T) {
	p := allocPeriod(t, 11)
	out := renderAllocation(t, metricsScope{}, p, colorNone, allocData(t, config.Default(), p, nil))

	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("an unconfigured scope rendered more than one line:\n%s", out)
	}
	if !strings.Contains(out, "no engagement") {
		t.Errorf("the placeholder does not say no engagement is configured:\n%s", out)
	}
	for _, forbidden := range []string{"0.0h", "$0", "0%"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the placeholder prints %q — an unconfigured scope owes nothing and is owed nothing:\n%s",
				forbidden, out)
		}
	}
}

// An engagement configured for somebody else is not this scope's engagement.
// Scope selects what prints, using metricsScope's own predicates.
func TestAllocationPrintsOnlyTheEngagementsInScope(t *testing.T) {
	p := allocPeriod(t, 11)
	cfg := allocConfig(nil, map[string]config.Engagement{
		"atlas":    hourlyEngagement(),
		"bluebird": contractEngagement(),
	})
	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, allocData(t, cfg, p, events))
	if !strings.Contains(out, "atlas") {
		t.Errorf("the scoped project's own engagement is missing:\n%s", out)
	}
	if strings.Contains(out, "bluebird") {
		t.Errorf("another project's engagement printed under -project atlas:\n%s", out)
	}
}

// ─── the hourly shape ────────────────────────────────────────────────────────

// THE PRO-RATION RULE, stated as arithmetic: a 30h-per-week budget over an
// eleven-day window is 30 × 11/7 = 47.14h. The window is counted in the
// CALENDAR's own working days — the same days every other section is tiled by
// — and never in weekday-versus-weekend days, which this calendar has no
// concept of and this tool exists to disprove the relevance of.
func TestAllocationProRatesAWeeklyBudgetByTheWindowsWorkingDays(t *testing.T) {
	p := allocPeriod(t, 11)
	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)

	out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone, allocData(t, cfg, p, nil))
	if !strings.Contains(out, "47.1h") {
		t.Errorf("an 11-day window against 30h/week does not read 47.1h budgeted:\n%s", out)
	}
}

// A MONTH IS NOT A FIXED NUMBER OF DAYS. A window covering exactly February
// consumes exactly one February's budget, and so does one covering exactly
// January — an averaged 30.44-day month would report 611.7h for January and
// 551.9h for February against the same 600h budget, and both would be wrong.
func TestAllocationProRatesAMonthlyBudgetByTheRealMonthLength(t *testing.T) {
	cal := mustCalendar(t)
	monthly := config.Engagement{Currency: new("$"), Rate: f64(150), BudgetedHours: f64(600),
		Period: config.BudgetPerMonth}
	cfg := allocConfig(map[string]config.Engagement{"Northwind": monthly}, nil)

	for _, tc := range []struct {
		name  string
		days  int
		ends  time.Time
		wants string
	}{
		{"february", 28, time.Date(2026, 2, 28, 12, 0, 0, 0, time.Local), "600.0h"},
		{"january", 31, time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local), "600.0h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := dashboardPeriodOf(tc.days, cal, tc.ends)
			out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone,
				allocData(t, cfg, p, nil))
			if !strings.Contains(out, tc.wants) {
				t.Errorf("a window covering exactly %s does not read %s budgeted:\n%s",
					tc.name, tc.wants, out)
			}
		})
	}
}

// A CLAMPED WINDOW OPENS AT AN INSTANT, not at a day boundary — see
// clampPeriodToFirstObservation — so the first day is a FRACTION of a day and
// must be pro-rated as one. Counting it whole would overstate the budget by up
// to a day, which on a short window is enough to move an over-budget verdict
// to an under-budget one.
func TestAllocationProRatesAPartialFirstDayAsAFraction(t *testing.T) {
	cal := mustCalendar(t)
	whole := dashboardPeriodOf(2, cal, allocDay)
	// Open the window exactly halfway through its first working day.
	first := cal.DayOf(whole.From)
	start, end := cal.Bounds(first)
	half := whole
	half.From = start.Add(end.Sub(start) / 2)
	half.Clamped = true

	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)
	out := renderAllocation(t, metricsScope{Client: "Northwind"}, half, colorNone, allocData(t, cfg, half, nil))

	// 1.5 days of a 7-day week at 30h: 6.43h, not the 8.57h two whole days
	// would give.
	if !strings.Contains(out, "6.4h") {
		t.Errorf("a window opening mid-day does not pro-rate its first day as a fraction:\n%s", out)
	}
	if strings.Contains(out, "8.6h") {
		t.Errorf("a window opening mid-day counted that day whole:\n%s", out)
	}
}

// CRITERION Ab5, the under half: legible without any color at all.
func TestAllocationMarksAnUnderBudgetWindowInPlainText(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": hourlyEngagement()})
	// 30h budgeted for the week; 2.0h worked.
	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, allocData(t, cfg, p, events))
	if !strings.Contains(out, "under by") {
		t.Errorf("2.0h against a 30h budget does not read as under:\n%s", out)
	}
	if strings.Contains(out, "over by") || strings.Contains(out, "OVER") {
		t.Errorf("2.0h against a 30h budget reads as over:\n%s", out)
	}
	if !strings.Contains(out, "2.0h of 30.0h") {
		t.Errorf("the hours and the budget are not both stated:\n%s", out)
	}
}

// CRITERION Ab5, the over half — and the one that matters, since overwork must
// be visible rather than rewarded (design principle 5). The overrun is marked
// PAST the end of a full bar, so a glance at the bar's own shape says it,
// with no color and no reading of the number.
func TestAllocationMarksAnOverBudgetWindowInPlainText(t *testing.T) {
	p := allocPeriod(t, 7)
	small := hourlyEngagement()
	small.BudgetedHours = f64(1)
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": small})
	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, allocData(t, cfg, p, events))
	if !strings.Contains(out, "OVER by") {
		t.Errorf("2.0h against a 1.0h weekly budget does not read as over:\n%s", out)
	}
	if !strings.Contains(out, allocationOverrunGlyph) {
		t.Errorf("an over-budget bar carries no overrun mark past its end:\n%s", out)
	}
	if strings.Contains(out, "under by") {
		t.Errorf("an over-budget window also reads as under:\n%s", out)
	}
}

// The same distinction again, in color: under and over must not be painted the
// same. This is the only test here that asserts on escape codes, and it
// asserts a DIFFERENCE rather than a particular color, so the palette stays
// free to change.
func TestAllocationPaintsOverBudgetDifferentlyFromUnder(t *testing.T) {
	p := allocPeriod(t, 7)
	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}

	roomy := allocConfig(nil, map[string]config.Engagement{"atlas": hourlyEngagement()})
	tight := hourlyEngagement()
	tight.BudgetedHours = f64(1)
	cramped := allocConfig(nil, map[string]config.Engagement{"atlas": tight})

	under := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorTruecolor, allocData(t, roomy, p, events))
	over := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorTruecolor, allocData(t, cramped, p, events))

	if !strings.Contains(under, "\x1b[") || !strings.Contains(over, "\x1b[") {
		t.Fatalf("truecolor rendered no escape at all:\nunder:\n%s\nover:\n%s", under, over)
	}
	if allocEscapes(under) == allocEscapes(over) {
		t.Errorf("under and over budget paint identically:\nunder:\n%q\nover:\n%q",
			allocEscapes(under), allocEscapes(over))
	}
}

// allocEscapes is every SGR sequence in out, in order — the painting, with the
// text stripped out.
func allocEscapes(out string) string {
	var b strings.Builder
	for i := 0; i < len(out); i++ {
		if out[i] != 0x1b {
			continue
		}
		j := i
		for j < len(out) && out[j] != 'm' {
			j++
		}
		b.WriteString(out[i:min(j+1, len(out))])
	}
	return b.String()
}

// The nominal rate is a stated fact and prints as one.
func TestAllocationShowsTheNominalRateOnAnHourlyEngagement(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)

	out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone, allocData(t, cfg, p, nil))
	if !strings.Contains(out, "$150.00/h") {
		t.Errorf("the nominal rate is not stated:\n%s", out)
	}
}

// CRITERION sQI, first half. An HOURLY engagement has no effective rate, and
// the section must not invent one. Dividing the budget's money by the hours
// worked would answer "what did you earn per hour" only if over-budget hours
// went unbilled — which is a fact about a contract nobody recorded here.
// Absence of a row is not an observation; that rule applies to money too.
func TestAllocationOmitsAnEffectiveRateOnAnHourlyEngagement(t *testing.T) {
	p := allocPeriod(t, 7)
	tight := hourlyEngagement()
	tight.BudgetedHours = f64(1)
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": tight})
	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, allocData(t, cfg, p, events))
	if strings.Contains(out, "effective rate") {
		t.Errorf("an hourly engagement printed an effective rate, which nothing here measured:\n%s", out)
	}
}

// A configured engagement with no hours in an OBSERVED window is an observed
// zero, and prints as one. The budget is still real; the hours against it are
// genuinely nil.
func TestAllocationPrintsAnObservedZeroAgainstABudget(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)

	out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone, allocData(t, cfg, p, nil))
	if !strings.Contains(out, "0.0h of 30.0h") {
		t.Errorf("an observed window with no hours does not read as an observed zero:\n%s", out)
	}
	if strings.Contains(out, "unrecorded") {
		t.Errorf("collection ran across this window, so this is a measurement:\n%s", out)
	}
}

// The other half of the same distinction, and the one this project has shipped
// backwards three times: nothing was collected, so there are no hours to set
// against the budget and a 0 would be an assertion nobody made.
func TestAllocationSaysUnrecordedRatherThanZeroWhenNothingWasCollected(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)
	data := allocData(t, cfg, p, nil)
	data.Log = collectionLog{}

	out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone, data)
	if !strings.Contains(out, "unrecorded") {
		t.Errorf("an uncollected window does not say so:\n%s", out)
	}
	if strings.Contains(out, "0.0h of") {
		t.Errorf("an uncollected window printed 0 hours against the budget:\n%s", out)
	}
}

// An hourly budget never reaches outside the window, so nothing about it needs
// the store. Passing no store at all proves it: a section that queried would
// panic here.
func TestAllocationNeverTouchesTheStoreForAnHourlyEngagement(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(map[string]config.Engagement{"Northwind": hourlyEngagement()}, nil)
	data := allocData(t, cfg, p, nil)
	data.Store = nil

	out := renderAllocation(t, metricsScope{Client: "Northwind"}, p, colorNone, data)
	if !strings.Contains(out, "budgeted") {
		t.Errorf("the hourly panel did not render:\n%s", out)
	}
}

// ─── the contract shape ──────────────────────────────────────────────────────

// allocStore writes every event through one harvest run and hands back the
// open store, the way dashboard_stats_test.go's own usage fixtures do.
//
// THE ATTRIBUTION PASS IS NOT OPTIONAL HERE. An event's project is not carried
// on the row a harvest writes: the store's own `project` column is populated
// only by ResolveProjects, which is how a real pipeline attributes a locator to
// a registered directory. Skipping it produces a store full of events that read
// back with an empty Identity.Project — which reports zero hours for every
// project, silently, and is exactly how the first draft of these tests read
// 0.0h against a contract with six hours on it.
func allocStore(t *testing.T, events []source.Event) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: gitlog.CommitsID, Input: "/synthetic-repo"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// allocInterval writes its locator as "/w/<project>", so the resolver is
	// that convention read back.
	if _, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		project, ok := strings.CutPrefix(locator, "/w/")
		if !ok {
			return store.Attributed{}, false, nil
		}
		return store.Attributed{Project: project}, true, nil
	}); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	return st
}

// A CONTRACT BURNS DOWN OVER ITS OWN LIFE, NOT PER WINDOW. Six hours of this
// contract were worked two months before the window opened; a burn-down that
// ignored them would say 154h remained when 152h do, and would price the
// effective rate at three times what it is.
func TestAllocationBurnsAContractDownAgainstAllTimeHours(t *testing.T) {
	p := allocPeriod(t, 7)
	inside := allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas") // 2.0h
	before := allocInterval(p.From.Add(-60*24*time.Hour), 350*time.Minute, "", "atlas")

	started := contractEngagement()
	started.Started = config.Date{Year: 2026, Month: time.January, Day: 15}
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": started})

	data := allocData(t, cfg, p, []source.Event{inside})
	data.Store = allocStore(t, []source.Event{inside, before})

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	if !strings.Contains(out, "8.0h of 160.0h expected") {
		t.Errorf("the burn-down does not count the 6.0h worked before the window:\n%s", out)
	}
	if !strings.Contains(out, "152.0h remaining") {
		t.Errorf("the remaining hours are not stated from the all-time figure:\n%s", out)
	}
	// The window figure is still said, so a reader is never left guessing
	// which span a figure describes.
	if !strings.Contains(out, "2.0h of that in this window") {
		t.Errorf("the window's own share of the contract is not stated:\n%s", out)
	}
}

// The effective rate is the contract divided by the hours it has actually
// taken: 24000 over 8.0h is 3000.00/h.
func TestAllocationDividesTheContractByItsAllTimeHours(t *testing.T) {
	p := allocPeriod(t, 7)
	inside := allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")
	before := allocInterval(p.From.Add(-60*24*time.Hour), 350*time.Minute, "", "atlas")

	started := contractEngagement()
	started.Started = config.Date{Year: 2026, Month: time.January, Day: 15}
	data := allocData(t, allocConfig(nil, map[string]config.Engagement{"atlas": started}), p,
		[]source.Event{inside})
	data.Store = allocStore(t, []source.Event{inside, before})

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	if !strings.Contains(out, "$3,000.00/h") {
		t.Errorf("24000 over 8.0h does not read $3,000.00/h:\n%s", out)
	}
}

// THE NUMBER THAT FALLS AS HOURS ACCUMULATE. This is the whole point of an
// effective rate on a fixed contract, and it is asserted as the property
// rather than as a pair of magic numbers: the same contract, more hours, a
// lower rate.
func TestAllocationsEffectiveRateFallsAsHoursAccumulate(t *testing.T) {
	p := allocPeriod(t, 7)
	inside := allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")
	before := allocInterval(p.From.Add(-60*24*time.Hour), 350*time.Minute, "", "atlas")
	more := allocInterval(p.From.Add(-30*24*time.Hour), 230*time.Minute, "", "atlas")

	started := contractEngagement()
	started.Started = config.Date{Year: 2026, Month: time.January, Day: 15}
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": started})

	lean := allocData(t, cfg, p, []source.Event{inside})
	lean.Store = allocStore(t, []source.Event{inside, before})
	fat := allocData(t, cfg, p, []source.Event{inside})
	fat.Store = allocStore(t, []source.Event{inside, before, more})

	leanRate := allocRateFrom(t, renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, lean))
	fatRate := allocRateFrom(t, renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, fat))
	if !(fatRate < leanRate) {
		t.Errorf("the effective rate did not fall as hours accumulated: %v then %v", leanRate, fatRate)
	}
}

// allocRateFrom pulls the printed effective rate back out as a number, so a
// test can assert on the figure rather than on its spelling.
func allocRateFrom(t *testing.T, out string) float64 {
	t.Helper()
	const marker = "effective rate $"
	_, after, ok := strings.Cut(out, marker)
	if !ok {
		t.Fatalf("no effective rate in:\n%s", out)
	}
	rest := after
	before, _, ok := strings.Cut(rest, "/h")
	if !ok {
		t.Fatalf("the effective rate has no /h in:\n%s", out)
	}
	digits := strings.ReplaceAll(before, ",", "")
	v, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		t.Fatalf("cannot read the rate %q: %v", digits, err)
	}
	return v
}

// CRITERION sQI, second half AND the case the criterion is worded for: no
// hours are recorded, so there is nothing to divide by. A rate of $0.00/h
// would assert that this contract earns nothing per hour, which nobody
// measured — the figure is omitted and the omission is explained.
func TestAllocationOmitsTheEffectiveRateWhenNoHoursAreRecorded(t *testing.T) {
	p := allocPeriod(t, 7)
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": contractEngagement()})
	data := allocData(t, cfg, p, nil)
	data.Store = allocStore(t, nil)

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	for _, forbidden := range []string{"$0.00/h", "$0/h", "0.00/h"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the panel printed %q as an effective rate:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "nothing to divide by") {
		t.Errorf("the panel does not say why the effective rate is absent:\n%s", out)
	}
	// The contract itself is still stated: what is unknown is the rate, not
	// the contract.
	if !strings.Contains(out, "$24,000.00") {
		t.Errorf("the contract total is not stated:\n%s", out)
	}
}

// A contract whose whole life fits inside the window needs no wider query at
// all, and must not make one — which is the cost half of the all-time
// decision. No store is passed, so a query would panic.
func TestAllocationSkipsTheWiderQueryWhenTheWindowAlreadyCoversTheContract(t *testing.T) {
	p := allocPeriod(t, 7)
	cal := mustCalendar(t)
	// A contract that began after the window opened.
	begins := cal.DayOf(p.From.Add(48 * time.Hour))
	inside := contractEngagement()
	inside.Started = config.Date{Year: begins.Year, Month: begins.Month, Day: begins.Date}
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": inside})

	events := []source.Event{allocInterval(p.From.Add(50*time.Hour), 110*time.Minute, "", "atlas")}
	data := allocData(t, cfg, p, events)
	data.Store = nil

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	if !strings.Contains(out, "expected") {
		t.Errorf("the contract panel did not render:\n%s", out)
	}
}

// COLLECTION IS NOT THE CONTRACT. A store that began collecting after the
// contract started cannot see the contract's early hours at all, so the
// burn-down is a lower bound and the effective rate an upper one. Saying so is
// the absence-of-a-row rule applied to money — and the alternative, a
// confident figure resting on a span nobody observed, is exactly what this
// project has shipped as a bug three times.
func TestAllocationSaysWhenCollectionBeganAfterTheContractDid(t *testing.T) {
	p := allocPeriod(t, 7)
	early := contractEngagement()
	early.Started = config.Date{Year: 2026, Month: time.January, Day: 15}
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": early})

	events := []source.Event{allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")}
	data := allocData(t, cfg, p, events)
	data.Store = allocStore(t, events)
	// Collection log begins well after 2026-01-15: collectedThroughout only
	// covers the window itself.

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	if !strings.Contains(out, "lower bound") {
		t.Errorf("a contract predating collection is not flagged as a lower bound:\n%s", out)
	}
	if !strings.Contains(out, "2026-01-15") {
		t.Errorf("the flag does not name the date the contract started:\n%s", out)
	}
}
