package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
)

// EVERY NAME AND EVERY FIGURE IN THIS FILE IS INVENTED. "Northwind", "Atlas"
// and "Bluebird" are not clients of anybody's, and 150, 30, 24000 and 160 are
// round numbers chosen for arithmetic that can be checked by hand. This file
// configures MONEY, and a fixture that carried a real rate would put a real
// rate in the published repository — see the confidentiality rule in AGENTS.md
// and `go run ./scripts/confidential -names <names> -gate .`, which is the
// check.

func TestTheDefaultConfigDeclaresNoEngagementAtAll(t *testing.T) {
	// A machine nobody has configured owes nobody anything. This is what the
	// dashboard's allocation section renders its quiet placeholder from, and
	// it must be an emptiness rather than a zero budget at a zero rate.
	c := config.Default()

	if !c.Engagements.IsEmpty() {
		t.Errorf("the default config declares engagements: %+v", c.Engagements)
	}
	if _, ok := c.Engagements.Client("Northwind"); ok {
		t.Error("the default config knows a client engagement")
	}
	if _, ok := c.Engagements.Project("atlas"); ok {
		t.Error("the default config knows a project engagement")
	}
}

func TestAnHourlyEngagementLoadsWithItsRateBudgetAndPeriod(t *testing.T) {
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"currency": "$", "rate": 150, "budgeted_hours": 30, "period": "week"}}}}`)

	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := c.Engagements.Client("Northwind")
	if !ok {
		t.Fatalf("the client engagement did not load: %+v", c.Engagements)
	}
	if got := e.Shape(); got != config.EngagementHourly {
		t.Errorf("Shape() = %v, want the hourly shape", got)
	}
	if e.Rate == nil || *e.Rate != 150 {
		t.Errorf("rate = %v, want 150", e.Rate)
	}
	if e.BudgetedHours == nil || *e.BudgetedHours != 30 {
		t.Errorf("budgeted_hours = %v, want 30", e.BudgetedHours)
	}
	if e.Period != config.BudgetPerWeek {
		t.Errorf("period = %q, want %q", e.Period, config.BudgetPerWeek)
	}
	if e.CurrencyLabel() != "$" {
		t.Errorf("currency = %q, want %q", e.CurrencyLabel(), "$")
	}
}

func TestAContractEngagementLoadsWithItsTotalAndExpectedHours(t *testing.T) {
	path := write(t, `{"engagements": {"projects": {"atlas": {
		"currency": "$", "contract_total": 24000, "expected_hours": 160, "started": "2026-01-15"}}}}`)

	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := c.Engagements.Project("atlas")
	if !ok {
		t.Fatalf("the project engagement did not load: %+v", c.Engagements)
	}
	if got := e.Shape(); got != config.EngagementContract {
		t.Errorf("Shape() = %v, want the contract shape", got)
	}
	if e.ContractTotal == nil || *e.ContractTotal != 24000 {
		t.Errorf("contract_total = %v, want 24000", e.ContractTotal)
	}
	if e.ExpectedHours == nil || *e.ExpectedHours != 160 {
		t.Errorf("expected_hours = %v, want 160", e.ExpectedHours)
	}
	want := config.Date{Year: 2026, Month: time.January, Day: 15}
	if e.Started != want {
		t.Errorf("started = %v, want %v", e.Started, want)
	}
}

// THE ONE RULE BEN STATED IN SO MANY WORDS: an entry declares exactly ONE of
// the two shapes, and declaring both is refused at load rather than resolved
// by a silent preference. Whichever shape a silent preference picked would be
// the wrong one roughly half the time, for ever, with no symptom.
func TestAnEngagementDeclaringBothShapesIsRefusedAtLoad(t *testing.T) {
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "week",
		"contract_total": 24000, "expected_hours": 160}}}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an entry declaring both an hourly rate and a fixed contract")
	}
	if !strings.Contains(err.Error(), "Northwind") {
		t.Errorf("the error does not name the entry it is about: %v", err)
	}
}

func TestAnEngagementDeclaringNothingIsRefused(t *testing.T) {
	// An entry with no money in it does nothing at all, for ever, with no
	// symptom — the same failure the unknown-key refusal exists to prevent one
	// level up.
	path := write(t, `{"engagements": {"projects": {"atlas": {}}}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an engagement entry that declares no money at all")
	}
	if !strings.Contains(err.Error(), "atlas") {
		t.Errorf("the error does not name the entry it is about: %v", err)
	}
}

func TestAnHourlyEngagementNeedsBothItsRateAndItsBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"rate alone", `{"engagements": {"clients": {"Northwind": {"rate": 150}}}}`, "budgeted_hours"},
		{"budget alone", `{"engagements": {"clients": {"Northwind": {"budgeted_hours": 30, "period": "week"}}}}`, "rate"},
		{"budget with no period", `{"engagements": {"clients": {"Northwind": {"rate": 150, "budgeted_hours": 30}}}}`, "period"},
		{"period with no budget", `{"engagements": {"clients": {"Northwind": {"rate": 150, "period": "week"}}}}`, "budgeted_hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatalf("Load accepted a half-stated hourly engagement: %s", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not name the missing key %q: %v", tc.want, err)
			}
		})
	}
}

func TestAContractEngagementNeedsBothItsTotalAndItsExpectedHours(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"total alone", `{"engagements": {"projects": {"atlas": {"contract_total": 24000}}}}`, "expected_hours"},
		{"hours alone", `{"engagements": {"projects": {"atlas": {"expected_hours": 160}}}}`, "contract_total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatalf("Load accepted a half-stated contract: %s", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not name the missing key %q: %v", tc.want, err)
			}
		})
	}
}

// A ZERO IS NOT AN UNSTATED VALUE, which is why every money field is a pointer
// and why a stated zero is refused rather than read as absence. Nobody bills
// at nothing, and a budget of zero hours makes every window infinitely over.
func TestAStatedZeroIsRefusedRatherThanReadAsUnstated(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"rate", `{"engagements": {"clients": {"Northwind": {"rate": 0, "budgeted_hours": 30, "period": "week"}}}}`},
		{"budgeted_hours", `{"engagements": {"clients": {"Northwind": {"rate": 150, "budgeted_hours": 0, "period": "week"}}}}`},
		{"contract_total", `{"engagements": {"projects": {"atlas": {"contract_total": 0, "expected_hours": 160}}}}`},
		{"expected_hours", `{"engagements": {"projects": {"atlas": {"contract_total": 24000, "expected_hours": 0}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatalf("Load accepted a zero %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("the error does not name the field it refused: %v", err)
			}
		})
	}
}

func TestAnUnstatedRateIsNilRatherThanZero(t *testing.T) {
	// The distinction criterion sQI rests on, at the level the config can
	// express it: nothing said about money reads as nothing said, never as
	// nothing owed.
	path := write(t, `{"engagements": {"projects": {"atlas": {"contract_total": 24000, "expected_hours": 160}}}}`)

	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, _ := c.Engagements.Project("atlas")
	if e.Rate != nil {
		t.Errorf("an unstated rate loaded as %v; it must be nil, so nothing can print it as 0", *e.Rate)
	}
	if e.BudgetedHours != nil {
		t.Errorf("unstated budgeted_hours loaded as %v; it must be nil", *e.BudgetedHours)
	}
}

func TestAnUnknownBudgetPeriodIsRefusedAndQuoted(t *testing.T) {
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "fortnight"}}}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a budget period nothing can pro-rate")
	}
	if !strings.Contains(err.Error(), `"fortnight"`) {
		t.Errorf("the error does not quote the period it refused: %v", err)
	}
}

func TestAStartDateOnlyMeansSomethingOnAContract(t *testing.T) {
	// started bounds the wider query a contract's all-time burn-down needs.
	// An hourly budget is per-period and never reaches outside the window, so
	// a start date on one would be a key that does nothing.
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "week", "started": "2026-01-15"}}}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a start date on an hourly engagement, where nothing reads it")
	}
	if !strings.Contains(err.Error(), "started") {
		t.Errorf("the error does not name the key it refused: %v", err)
	}
}

func TestAnEmptyEngagementNameIsRefused(t *testing.T) {
	path := write(t, `{"engagements": {"projects": {"": {"contract_total": 24000, "expected_hours": 160}}}}`)

	if _, err := config.Load(path); err == nil {
		t.Fatal("Load accepted an engagement keyed by an empty name, which names no project")
	}
}

func TestAnEmptyCurrencyIsRefusedRatherThanTreatedAsUnstated(t *testing.T) {
	path := write(t, `{"engagements": {"projects": {"atlas": {
		"currency": "", "contract_total": 24000, "expected_hours": 160}}}}`)

	if _, err := config.Load(path); err == nil {
		t.Fatal("Load accepted a currency key written as an empty string")
	}
}

func TestADateIsWrittenAsAQuotedDay(t *testing.T) {
	for _, bad := range []string{`"15/01/2026"`, `"2026-1-15"`, `20260115`, `"tuesday"`} {
		body := `{"engagements": {"projects": {"atlas": {"contract_total": 24000,` +
			`"expected_hours": 160, "started": ` + bad + `}}}}`
		if _, err := config.Load(write(t, body)); err == nil {
			t.Errorf("Load accepted %s as a start date", bad)
		}
	}
}

func TestADateResolvesThroughTheZoneItIsAskedFor(t *testing.T) {
	// A date is a civil day and not an instant. It becomes one only against a
	// location, which is why Date carries no zone of its own — the calendar
	// the rest of the report is tiled by supplies it.
	d := config.Date{Year: 2026, Month: time.January, Day: 15}
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("no zoneinfo on this machine: %v", err)
	}

	got := d.In(loc)
	if want := time.Date(2026, time.January, 15, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("In(America/Chicago) = %v, want %v", got, want)
	}
	if utc := d.In(time.UTC); utc.Equal(got) {
		t.Error("the same date resolved to the same instant in two different zones")
	}
	if (config.Date{}).IsZero() != true {
		t.Error("the zero Date does not report itself as zero")
	}
	if d.IsZero() {
		t.Error("a stated date reports itself as zero")
	}
}

// A MONTH IS NOT A FIXED NUMBER OF DAYS, and pro-rating a monthly budget as if
// it were is wrong by up to 10% between February and January. DaysIn asks the
// calendar rather than averaging.
func TestABudgetPeriodKnowsHowManyDaysItHolds(t *testing.T) {
	for _, tc := range []struct {
		period config.BudgetPeriod
		year   int
		month  time.Month
		want   float64
	}{
		{config.BudgetPerWeek, 2026, time.February, 7},
		{config.BudgetPerWeek, 2026, time.January, 7},
		{config.BudgetPerMonth, 2026, time.January, 31},
		{config.BudgetPerMonth, 2026, time.February, 28},
		{config.BudgetPerMonth, 2028, time.February, 29},
		{config.BudgetPerMonth, 2026, time.April, 30},
	} {
		if got := tc.period.DaysIn(tc.year, tc.month); got != tc.want {
			t.Errorf("%q.DaysIn(%d, %v) = %v, want %v", tc.period, tc.year, tc.month, got, tc.want)
		}
	}
}

func TestAnEngagementRoundTripsThroughJSON(t *testing.T) {
	// A config this package prints is a config it can read back — Duration's
	// own rule, applied to the two scalar types this section adds.
	path := write(t, `{"engagements": {"projects": {"atlas": {
		"currency": "$", "contract_total": 24000, "expected_hours": 160, "started": "2026-01-15"}}}}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back config.Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal what Marshal wrote: %v", err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("what Marshal wrote does not validate: %v", err)
	}
	got, ok := back.Engagements.Project("atlas")
	if !ok {
		t.Fatalf("the round trip lost the engagement: %s", raw)
	}
	if got.Started != (config.Date{Year: 2026, Month: time.January, Day: 15}) {
		t.Errorf("started came back as %v", got.Started)
	}
	if got.ContractTotal == nil || *got.ContractTotal != 24000 {
		t.Errorf("contract_total came back as %v", got.ContractTotal)
	}
}

func TestAClientAndAProjectMayShareAName(t *testing.T) {
	// The two maps are separate namespaces, and a client called Bluebird with
	// a project called Bluebird under it is ordinary rather than a conflict.
	path := write(t, `{"engagements": {
		"clients": {"Bluebird": {"rate": 150, "budgeted_hours": 30, "period": "week"}},
		"projects": {"Bluebird": {"contract_total": 24000, "expected_hours": 160}}}}`)

	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	client, ok := c.Engagements.Client("Bluebird")
	if !ok || client.Shape() != config.EngagementHourly {
		t.Errorf("the client entry is %+v, %v; want the hourly shape", client, ok)
	}
	project, ok := c.Engagements.Project("Bluebird")
	if !ok || project.Shape() != config.EngagementContract {
		t.Errorf("the project entry is %+v, %v; want the contract shape", project, ok)
	}
}
