package config

// This file models MONEY, and it is the only file in this repository that
// does. Everything here is read from ~/.timetattle/config.json, which is
// deliberately outside every repository for exactly this reason — see this
// package's own doc comment: "a value inside a working tree is a value that
// gets committed by accident." Nothing in this file has a default, nothing in
// it ships with a figure in it, and the tool works completely without any of
// it.
//
// # One entry, one shape, declared and never inferred
//
// Ben's decision, 2026-08-07: an engagement is modelled PER ENGAGEMENT rather
// than as one global billing model, and each entry declares exactly ONE of two
// shapes:
//
//	(a) HOURLY — a nominal rate and a budget of hours per period. The budget
//	    recurs; the question it answers is "am I inside this week's hours".
//	(b) CONTRACT — a fixed total for an expected number of hours. Nothing
//	    recurs; the question it answers is "how much of the contract is left,
//	    and what am I actually earning per hour so far".
//
// An entry carrying BOTH is a validation error stated at load and never a
// silent preference for one of them. There is no defensible tie-break: a
// preference picked here would be the wrong reading of somebody's money about
// half the time, for ever, with no symptom — which is the failure this
// package's unknown-key refusal already exists to prevent one level up.
//
// An entry carrying NEITHER is refused for the same reason. A key that does
// nothing at all is indistinguishable, from the outside, from a key that was
// never typed.
//
// # Absence is a pointer, not a zero
//
// Every money field is a *float64. A rate that was never stated and a rate of
// zero are different facts, and only a pointer can hold the difference — this
// is the absence-of-a-row rule (AGENTS.md) applied to a struct field. The
// dashboard's Allocation & Effective Rate section is built on it: an unstated
// rate is OMITTED from the report, never rendered as $0, which is criterion
// sQI of the task that added this file. A stated zero is refused by Validate,
// so the pointer only ever means "unsaid".
//
// The omitempty and omitzero tags below are the WRITING half of that rule —
// edit.go is what writes this section back. On a POINTER, omitempty omits only
// the nil case: a pointer to zero is still written as 0, so a stated zero stays
// stated and is refused by Validate as it should be. Period and Started need
// the tags most, because neither is a pointer: without them a contract entry is
// written carrying `"period": ""` and an hourly one carrying `"started": ""`,
// which is an empty string standing in for something nobody said — the exact
// confusion the Currency pointer exists to prevent, reintroduced by the writer.
//
// Measured, by removing all nine tags and running the package's tests: an
// hourly entry is then written as `"currency": null, "rate": 150,
// "budgeted_hours": 30, "period": "week", "contract_total": null,
// "expected_hours": null, "started": ""`. That still LOADS — null reads back as
// an unset pointer and "" as an unset date — so the cost is not a refusal but a
// file that states six things nobody typed, three of them about the shape the
// entry did not choose. What CANNOT load is the same file with zeros instead of
// nulls, since `"contract_total": 0` makes the entry declare both shapes.
//
// The tags must NOT be copied on to Config's own fields. Sessions.PresenceSources
// ships with a non-empty default, so a file stating [] means "none" while an
// absent key means "unsaid"; omitempty cannot tell those apart and would
// silently restore knowledgec-backlit the first time such a file was written
// back.
//
// # Naming: engagements, not "clients"
//
// This package's own doc comment said these values would arrive as a "clients"
// section. They arrive as `engagements`, with separate `clients` and
// `projects` maps under it, because scope in this tool is a client OR a
// project (see metricsScope in cmd/timetattle) and the default scope — the
// working directory — resolves to a PROJECT far more often than to a client.
// A single flat map would have had to guess which namespace a name belonged
// to; two maps never do, and a client and a project sharing a name is
// ordinary rather than a conflict.

import (
	"encoding/json"
	"fmt"
	"time"
)

// Engagements is what is owed, and by whom, keyed the two ways this tool
// scopes a report. Both maps are absent by default: a machine with no config
// declares no money, which is the ordinary state and the one the dashboard's
// allocation section renders a single quiet line for.
type Engagements struct {
	// Clients maps a client name — the name the registry resolved and
	// metrics prints, exactly as `-client` takes it — to that client's
	// engagement.
	Clients map[string]Engagement `json:"clients,omitempty"`
	// Projects maps a project name, exactly as `-project` takes it.
	Projects map[string]Engagement `json:"projects,omitempty"`
}

// IsEmpty reports whether anything is configured at all. It is the whole of
// the question the dashboard asks before deciding to print a placeholder line
// rather than a panel.
func (e Engagements) IsEmpty() bool { return len(e.Clients) == 0 && len(e.Projects) == 0 }

// Client is the engagement configured for a client, if any.
func (e Engagements) Client(name string) (Engagement, bool) {
	v, ok := e.Clients[name]
	return v, ok
}

// Project is the engagement configured for a project, if any.
func (e Engagements) Project(name string) (Engagement, bool) {
	v, ok := e.Projects[name]
	return v, ok
}

// CurrencyLabel is what should be printed before an amount, or "" when nothing
// was stated. It is the ONE way anything outside this package reads Currency,
// so no caller has to hold the pointer's two empty cases in its head.
func (e Engagement) CurrencyLabel() string {
	if e.Currency == nil {
		return ""
	}
	return *e.Currency
}

// Engagement is one client's or one project's money.
//
// See this file's own doc comment for why exactly one of the two shapes may be
// stated, and why every figure is a pointer.
type Engagement struct {
	// Currency is written exactly as it should appear before an amount —
	// "$", "£", "EUR ". It is a LABEL and never a conversion: Timetattle
	// converts nothing, compares nothing across currencies, and would be
	// asserting a rate it has never seen if it did.
	//
	// Unstated means amounts print bare. There is no default, for the reason
	// Sessions.Exclude gives for shipping empty: a default of "$" would be
	// this tool asserting a fact about somebody's money that nobody told it.
	//
	// It is a pointer for the same reason the money fields are: a key written
	// as "" is a person having tried to say something and said nothing, and
	// Validate refuses one, where an absent key is simply unstated. Read it
	// through CurrencyLabel, which flattens both to "".
	Currency *string `json:"currency,omitempty"`

	// Rate is the nominal hourly rate — shape (a). It is what is CHARGED per
	// hour, which the contract shape's effective rate deliberately is not.
	Rate *float64 `json:"rate,omitempty"`
	// BudgetedHours is how many hours Period is meant to hold — shape (a).
	BudgetedHours *float64 `json:"budgeted_hours,omitempty"`
	// Period is what BudgetedHours recurs over.
	Period BudgetPeriod `json:"period,omitempty"`

	// ContractTotal is the whole fixed value of the engagement — shape (b).
	// It does not recur and it is not per period.
	ContractTotal *float64 `json:"contract_total,omitempty"`
	// ExpectedHours is how many hours ContractTotal was priced against —
	// shape (b). The dashboard burns this down against hours to date, which
	// is why it is a total and never a per-period figure.
	ExpectedHours *float64 `json:"expected_hours,omitempty"`
	// Started is the civil day the contract began — shape (b) only, and
	// optional there.
	//
	// It exists because a contract burns down over ITS OWN life rather than
	// over a reporting window, so the hours it consumes must be counted from
	// a wider span than the dashboard's window. Stating it BOUNDS that wider
	// query to the contract's own span instead of the whole store, and it
	// lets the report say when the store's collection log begins after the
	// contract did — which makes the burn-down a stated lower bound rather
	// than a silently incomplete figure.
	//
	// Unstated means the query reaches as far back as the store goes.
	Started Date `json:"started,omitzero"`
}

// EngagementShape names which of the two shapes an entry declared.
type EngagementShape int

const (
	// EngagementUnstated is an entry with no money in it at all. Validate
	// refuses one, so it is only ever seen on a hand-built Config.
	EngagementUnstated EngagementShape = iota
	// EngagementHourly is a nominal rate and a recurring budget of hours.
	EngagementHourly
	// EngagementContract is a fixed total for an expected number of hours.
	EngagementContract
)

func (s EngagementShape) String() string {
	switch s {
	case EngagementHourly:
		return "hourly"
	case EngagementContract:
		return "contract"
	default:
		return "unstated"
	}
}

// Shape reports which shape this entry declared. On a validated config the
// two groups of fields are never both populated, so this reads whichever one
// is.
func (e Engagement) Shape() EngagementShape {
	switch {
	case e.hourlyStated():
		return EngagementHourly
	case e.contractStated():
		return EngagementContract
	default:
		return EngagementUnstated
	}
}

// hourlyStated reports whether ANY shape-(a) key was written — not whether a
// complete one was. Validate is what insists on completeness; this is what
// notices that the shape was reached for at all, so that an entry reaching for
// both can be named as the error it is.
func (e Engagement) hourlyStated() bool {
	return e.Rate != nil || e.BudgetedHours != nil || e.Period != ""
}

// contractStated is hourlyStated for shape (b). Started counts: it is a
// contract-only key, so writing one is reaching for the contract shape, and
// writing it beside an hourly budget must be an error rather than a key that
// silently does nothing.
func (e Engagement) contractStated() bool {
	return e.ContractTotal != nil || e.ExpectedHours != nil || !e.Started.IsZero()
}

// BudgetPeriod is what a shape-(a) budget of hours recurs over.
//
// The set is deliberately tiny and closed. A budget period is not a Duration:
// "month" is not a fixed number of hours, and the whole point of naming the
// period rather than writing a duration is that the pro-ration can then ask
// the calendar how long each month actually was — see DaysIn.
type BudgetPeriod string

const (
	BudgetPerWeek  BudgetPeriod = "week"
	BudgetPerMonth BudgetPeriod = "month"
)

// DaysIn is how many days this period holds, for a period containing the given
// civil month.
//
// A WEEK IS ALWAYS SEVEN DAYS AND A MONTH NEVER IS. Pro-rating a monthly
// budget against an averaged month length is wrong by up to 10% between
// February and January — on a 160h monthly budget that is 16 hours, which is
// easily enough to move an under-budget window to an over-budget one. So the
// month's real length is asked for, per month, and a window spanning several
// months is pro-rated day by day against whichever month each day fell in.
//
// The result is a float64 because it is only ever a divisor.
func (p BudgetPeriod) DaysIn(year int, month time.Month) float64 {
	switch p {
	case BudgetPerWeek:
		return 7
	case BudgetPerMonth:
		// Day 0 of the following month is the last day of this one, which is
		// the standard library's own way of asking a calendar how long a month
		// was, leap years included.
		return float64(time.Date(year, month+1, 0, 12, 0, 0, 0, time.UTC).Day())
	default:
		return 0
	}
}

// Date is a civil day: no time, no zone, and therefore not an instant until
// something supplies a location — the same distinction sessions.Clock draws
// for a wall-clock time, for the same reason.
//
// A contract does not begin at an instant in UTC; it begins on a day, in
// whatever zone the person signing it lives in. Storing an instant here would
// bake in a zone at parse time, and the zone that matters is the one the
// report is tiled by, which is read from Calendar and is not known here.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// IsZero reports whether no date was stated.
func (d Date) IsZero() bool { return d == Date{} }

// In is the first instant of this day in loc.
func (d Date) In(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// String renders the date the way it is written in the file.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// dateLayout is the one accepted spelling. It is ISO 8601's calendar date and
// nothing else: "2026-1-15" is refused along with "15/01/2026", because a
// format that accepts several spellings has to guess at 03/04/2026 and would
// be wrong somewhere in the world either way.
const dateLayout = "2006-01-02"

// MarshalJSON writes the date as a string, so a config this package prints is
// a config it can read back. A zero date writes as "", which reads back as
// zero.
func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON reads "2026-01-15", and refuses a bare number.
func (d *Date) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("%s is not a date; write one as a quoted day such as \"2026-01-15\"", raw)
	}
	if text == "" {
		*d = Date{}
		return nil
	}
	parsed, err := time.Parse(dateLayout, text)
	if err != nil {
		return fmt.Errorf("%q is not a date; write one as \"2026-01-15\", four digits, two and two", text)
	}
	*d = Date{Year: parsed.Year(), Month: parsed.Month(), Day: parsed.Day()}
	return nil
}

// validate reports why an engagements section cannot be used.
//
// Every check here is a value that would otherwise fail silently: a key that
// does nothing, a shape half-stated so that nothing can be drawn from it, or a
// zero standing where a person meant to say nothing at all.
func (e Engagements) validate() error {
	for name, entry := range e.Clients {
		if err := entry.validate("engagements.clients", name); err != nil {
			return err
		}
	}
	for name, entry := range e.Projects {
		if err := entry.validate("engagements.projects", name); err != nil {
			return err
		}
	}
	return nil
}

// validate reports why one entry cannot be used. section and name are carried
// through every message so that a config with a dozen engagements says WHICH
// one is wrong — a validation error that names no entry sends a reader to read
// the whole file.
func (e Engagement) validate(section, name string) error {
	if name == "" {
		return fmt.Errorf("%s has an entry with an empty name, which names nothing to report against", section)
	}
	at := fmt.Sprintf("%s[%q]", section, name)

	hourly, contract := e.hourlyStated(), e.contractStated()
	switch {
	case hourly && contract:
		return fmt.Errorf("%s declares both an hourly engagement (rate, budgeted_hours, period) and a "+
			"fixed contract (contract_total, expected_hours, started); an engagement is one or the other, "+
			"and nothing here can choose between them on your behalf", at)
	case !hourly && !contract:
		return fmt.Errorf("%s declares no money at all; give it either an hourly rate with budgeted_hours "+
			"and a period, or a contract_total with expected_hours, or remove the entry", at)
	}

	if e.Currency != nil && *e.Currency == "" {
		return fmt.Errorf("%s writes currency as an empty string; write the symbol or code that should "+
			"appear before an amount, or leave the key out entirely to print amounts bare", at)
	}

	if hourly {
		return e.validateHourly(at)
	}
	return e.validateContract(at)
}

func (e Engagement) validateHourly(at string) error {
	if e.Rate == nil {
		return fmt.Errorf("%s budgets hours but states no rate; add a rate, or state a contract_total "+
			"instead if the money is fixed rather than hourly", at)
	}
	if e.BudgetedHours == nil {
		return fmt.Errorf("%s states a rate but no budgeted_hours; there is nothing for the hours to be "+
			"measured against without one", at)
	}
	if e.Period == "" {
		return fmt.Errorf("%s budgets hours but names no period; write \"period\": %q or %q, so the budget "+
			"can be pro-rated to a reporting window", at, BudgetPerWeek, BudgetPerMonth)
	}
	if e.Period != BudgetPerWeek && e.Period != BudgetPerMonth {
		return fmt.Errorf("%s has period %q; it must be %q or %q", at, e.Period, BudgetPerWeek, BudgetPerMonth)
	}
	if err := positive(at, "rate", e.Rate); err != nil {
		return err
	}
	if err := positive(at, "budgeted_hours", e.BudgetedHours); err != nil {
		return err
	}
	if !e.Started.IsZero() {
		return fmt.Errorf("%s states started on an hourly engagement, where nothing reads it; a start date "+
			"bounds a fixed contract's burn-down, and an hourly budget recurs per period instead", at)
	}
	return nil
}

func (e Engagement) validateContract(at string) error {
	if e.ContractTotal == nil {
		return fmt.Errorf("%s expects hours but states no contract_total; add one, or state a rate with "+
			"budgeted_hours instead if the money is hourly rather than fixed", at)
	}
	if e.ExpectedHours == nil {
		return fmt.Errorf("%s states a contract_total but no expected_hours; there is nothing to burn "+
			"the contract down against without one", at)
	}
	if err := positive(at, "contract_total", e.ContractTotal); err != nil {
		return err
	}
	return positive(at, "expected_hours", e.ExpectedHours)
}

// positive refuses a stated zero or a negative figure. It never sees an
// unstated one: a nil pointer means the key was not written, which every
// caller above has already decided about.
func positive(at, key string, v *float64) error {
	if v == nil {
		return nil
	}
	if *v <= 0 {
		return fmt.Errorf("%s has %s %v; it must be positive — a zero here is not the same as saying "+
			"nothing, and saying nothing is what leaving the key out does", at, key, *v)
	}
	return nil
}
