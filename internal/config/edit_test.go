package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
)

// EVERY NAME AND EVERY FIGURE IN THIS FILE IS INVENTED, for the reason
// engagements_test.go states at its own head: this section configures MONEY,
// and a fixture carrying a real rate would put a real rate in the published
// repository. "Northwind", "Bluebird" and "atlas" are nobody's clients; 150,
// 30, 24000 and 160 are round numbers chosen so the arithmetic can be checked
// by hand.

// missing returns a path inside t's own directory where no file exists. Every
// test here builds its own config from nothing: the suite must run on a machine
// with no Timetattle installed and no ~/.timetattle at all.
func missing(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

// read is the file as it stands on disk, as bytes, because what this file
// tests is largely which BYTES were written.
func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// hourly is a complete, valid shape-(a) submission.
func hourly() config.HourlyInput {
	return config.HourlyInput{Currency: "$", Rate: "150", BudgetedHours: "30", Period: config.BudgetPerWeek}
}

// contract is a complete, valid shape-(b) submission.
func contract() config.ContractInput {
	return config.ContractInput{Currency: "$", ContractTotal: "24000", ExpectedHours: "160"}
}

func TestAnEngagementAddedToAMachineWithNoConfigReadsBack(t *testing.T) {
	// Absence of a config is the ordinary state of an unconfigured machine, and
	// adding the first engagement must not require a file to exist first.
	path := missing(t)

	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open on a missing file: %v", err)
	}
	if !ed.Config().Engagements.IsEmpty() {
		t.Errorf("a missing config declares engagements: %+v", ed.Config().Engagements)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	e, ok := back.Engagements.Client("Northwind")
	if !ok {
		t.Fatalf("the engagement did not survive the write: %s", read(t, path))
	}
	if e.Shape() != config.EngagementHourly {
		t.Errorf("Shape() = %v, want the hourly shape", e.Shape())
	}
	if e.Rate == nil || *e.Rate != 150 {
		t.Errorf("rate came back as %v, want 150", e.Rate)
	}
	if e.BudgetedHours == nil || *e.BudgetedHours != 30 {
		t.Errorf("budgeted_hours came back as %v, want 30", e.BudgetedHours)
	}
	if e.Period != config.BudgetPerWeek {
		t.Errorf("period came back as %q", e.Period)
	}
	if e.CurrencyLabel() != "$" {
		t.Errorf("currency came back as %q", e.CurrencyLabel())
	}
}

func TestABlankCurrencyIsWrittenAsAnOmittedKeyRatherThanAnEmptyString(t *testing.T) {
	// ABSENCE IS A POINTER, NOT A ZERO. A person who typed nothing in the
	// currency box has said nothing, and Validate refuses a currency written as
	// "" precisely so that "tried to say something and said nothing" cannot be
	// confused with "said nothing". A layer that writes "" for a blank box
	// therefore cannot save at all.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	blank := hourly()
	blank.Currency = ""
	if err := ed.SetClient("Northwind", blank); err != nil {
		t.Fatalf("SetClient with a blank currency: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	written := read(t, path)
	if strings.Contains(written, "currency") {
		t.Errorf("a blank currency was written as a key rather than omitted:\n%s", written)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	e, _ := back.Engagements.Client("Northwind")
	if e.Currency != nil {
		t.Errorf("currency came back as %q rather than unstated", *e.Currency)
	}
}

func TestTheOtherShapesFiguresAreOmittedRatherThanWrittenAsZero(t *testing.T) {
	// The numeric half of the same rule, and the one with teeth: an hourly
	// entry carrying "contract_total": 0 declares BOTH shapes, which the loader
	// refuses outright — so a layer that wrote zeros for the fields its shape
	// does not use could never save anything at all.
	for _, tc := range []struct {
		name    string
		input   config.EngagementInput
		absent  []string
		present []string
	}{
		{"hourly", hourly(),
			[]string{"contract_total", "expected_hours", "started"},
			[]string{"rate", "budgeted_hours", "period"}},
		{"contract", contract(),
			[]string{"rate", "budgeted_hours", "period", "started"},
			[]string{"contract_total", "expected_hours"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := missing(t)
			ed, err := config.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := ed.SetProject("atlas", tc.input); err != nil {
				t.Fatalf("SetProject: %v", err)
			}
			if err := ed.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}
			written := read(t, path)
			for _, key := range tc.absent {
				if strings.Contains(written, key) {
					t.Errorf("%q was written for a shape that does not use it:\n%s", key, written)
				}
			}
			for _, key := range tc.present {
				if !strings.Contains(written, key) {
					t.Errorf("%q was not written:\n%s", key, written)
				}
			}
			if _, err := config.Load(path); err != nil {
				t.Errorf("what Save wrote does not load: %v", err)
			}
		})
	}
}

func TestABlankRequiredFigureIsRefusedRatherThanReadAsZero(t *testing.T) {
	// A blank numeric box is not a zero. Reading one as 0 would either be
	// refused by Validate (a stated zero) or, worse, quietly halve somebody's
	// rate; refusing at the input is the only reading that says what happened.
	for _, tc := range []struct {
		name  string
		input config.EngagementInput
		want  string
	}{
		{"a blank rate", config.HourlyInput{Rate: "", BudgetedHours: "30", Period: config.BudgetPerWeek}, "rate"},
		{"a blank budget", config.HourlyInput{Rate: "150", BudgetedHours: "", Period: config.BudgetPerWeek}, "budgeted_hours"},
		{"a blank contract total", config.ContractInput{ContractTotal: "", ExpectedHours: "160"}, "contract_total"},
		{"a blank expectation", config.ContractInput{ContractTotal: "24000", ExpectedHours: ""}, "expected_hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := missing(t)
			ed, err := config.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			err = ed.SetClient("Northwind", tc.input)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}
			// It must be refused AS BLANK. An input layer that invented a 0
			// here would also be refused — by Validate, as a stated zero — so
			// asserting only that something failed would pin the wrong guard.
			// Measured: with the blank read as 0, this becomes
			// `has rate 0; it must be positive`, which tells a person their
			// rate is zero when what they did was type nothing.
			if !strings.Contains(err.Error(), "left blank") {
				t.Errorf("the refusal treats a blank as some other kind of wrong figure: %v", err)
			}
			// A refused edit changes nothing, and nothing is written.
			if !ed.Config().Engagements.IsEmpty() {
				t.Errorf("a refused edit was applied anyway: %+v", ed.Config().Engagements)
			}
		})
	}
}

func TestAStatedZeroIsStillRefusedRatherThanTreatedAsBlank(t *testing.T) {
	// The other half of the same distinction: a person who typed 0 HAS said
	// something, and what they said is refused by name. Collapsing it to
	// "unstated" would silently accept a rate of zero as an engagement with no
	// rate stated.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	zero := hourly()
	zero.Rate = "0"
	err = ed.SetClient("Northwind", zero)
	if err == nil {
		t.Fatalf("a rate of 0 was accepted")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("the refusal does not say the figure must be positive: %v", err)
	}
}

func TestChangingAnEngagementsShapeLeavesNoneOfTheOldShapeBehind(t *testing.T) {
	// THE ONE-SHAPE RULE. The two inputs are separate types precisely so that an
	// entry declaring both cannot be constructed; the consequence on disk is
	// that switching a client from hourly to contract must REPLACE the entry
	// rather than merge into it. A merge would write an entry the loader
	// refuses, leaving the file unloadable until it was hand-repaired.
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"currency": "$", "rate": 150, "budgeted_hours": 30, "period": "week"}}}}`)

	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Northwind", contract()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	written := read(t, path)
	for _, key := range []string{"rate", "budgeted_hours", "period"} {
		if strings.Contains(written, key) {
			t.Errorf("the old hourly shape survived the change: %q is still written\n%s", key, written)
		}
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	e, _ := back.Engagements.Client("Northwind")
	if e.Shape() != config.EngagementContract {
		t.Errorf("Shape() = %v, want the contract shape", e.Shape())
	}
}

func TestAContractsStartDateIsWrittenTheWayTheDateTypeSpellsIt(t *testing.T) {
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dated := contract()
	dated.Started = "2026-01-15"
	if err := ed.SetProject("atlas", dated); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if written := read(t, path); !strings.Contains(written, `"2026-01-15"`) {
		t.Errorf("the start date was not written as the Date type spells it:\n%s", written)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	e, _ := back.Engagements.Project("atlas")
	if want := (config.Date{Year: 2026, Month: time.January, Day: 15}); e.Started != want {
		t.Errorf("started came back as %v, want %v", e.Started, want)
	}
}

func TestAMisspelledStartDateIsRefusedByTheSameParserThatReadsTheFile(t *testing.T) {
	// One date parser, not two. A second spelling accepted here would be a
	// second schema for the same field, which is the thing this whole layer
	// exists to avoid.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bad := contract()
	bad.Started = "15/01/2026"
	err = ed.SetProject("atlas", bad)
	if err == nil {
		t.Fatalf("15/01/2026 was accepted as a date")
	}
	if !strings.Contains(err.Error(), "2026-01-15") {
		t.Errorf("the refusal does not show the accepted spelling: %v", err)
	}
}

func TestSavingRewritesTheEngagementsSectionAndNothingElse(t *testing.T) {
	// The defaults in this package are PROVISIONAL AND UNFITTED — config.go
	// says so — and a writer that re-emitted the whole struct would materialise
	// whichever values this build happens to hold into the user's file, where
	// they would then outlive every later change to them, with no symptom. So
	// the writer replaces one top-level key and copies the rest through
	// verbatim.
	path := write(t, `{
  "sessions": {
    "gap": "45m",
    "presence_sources": []
  }
}`)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	written := read(t, path)
	if !strings.Contains(written, `"45m"`) {
		t.Errorf("the stated gap was not copied through as written:\n%s", written)
	}
	if !strings.Contains(written, `"presence_sources": []`) {
		// An empty list is a STATED "none" and the defaults are a non-empty
		// list; dropping the key would silently restore knowledgec-backlit.
		t.Errorf("the stated empty presence_sources was not copied through:\n%s", written)
	}
	for _, key := range []string{"lead_in", "tail_out", "ignore_sources", "calendar", "config_version"} {
		if strings.Contains(written, key) {
			t.Errorf("the write materialised the default %q into the file:\n%s", key, written)
		}
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	if got := back.Sessions.Gap.Duration(); got != 45*time.Minute {
		t.Errorf("gap came back as %v, want 45m", got)
	}
	if got := back.Sessions.PresenceSources; len(got) != 0 {
		t.Errorf("presence_sources came back as %v, want the stated none", got)
	}
}

func TestAHandEditSinceTheReadIsRefusedRatherThanOverwritten(t *testing.T) {
	// RE-READ BEFORE WRITE. Hand-editing config.json stays legitimate, so a
	// writer that has been holding a copy since it was opened must not win
	// silently. It refuses, names the file, and writes nothing — the hand edit
	// is what survives.
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "week"}}}}`)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetProject("atlas", contract()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	handEdit := `{"engagements": {"clients": {"Bluebird": {
		"rate": 150, "budgeted_hours": 30, "period": "month"}}}}`
	if err := os.WriteFile(path, []byte(handEdit), 0o600); err != nil {
		t.Fatalf("hand-edit the file under test: %v", err)
	}

	err = ed.Save()
	if !errors.Is(err, config.ErrConfigChanged) {
		t.Fatalf("Save over a hand edit returned %v, want ErrConfigChanged", err)
	}
	if got := read(t, path); got != handEdit {
		t.Errorf("the hand edit was overwritten:\n%s", got)
	}
}

func TestAConfigThatAppearedSinceTheReadIsAlsoADivergence(t *testing.T) {
	// Absence is not emptiness. A file that did not exist when it was opened
	// and exists now is somebody else's writing, and overwriting it would
	// destroy a file this editor never read.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open on a missing file: %v", err)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}

	appeared := `{"engagements": {"projects": {"atlas": {"contract_total": 24000, "expected_hours": 160}}}}`
	if err := os.WriteFile(path, []byte(appeared), 0o600); err != nil {
		t.Fatalf("write the file under test: %v", err)
	}

	err = ed.Save()
	if !errors.Is(err, config.ErrConfigChanged) {
		t.Fatalf("Save over a file that appeared returned %v, want ErrConfigChanged", err)
	}
	if got := read(t, path); got != appeared {
		t.Errorf("the file that appeared was overwritten:\n%s", got)
	}
}

func TestSavingWhatWasAlreadyOnDiskWritesNothingAtAll(t *testing.T) {
	// A save that would produce the bytes already there is a save with nothing
	// to say. Rewriting the file anyway would touch its mtime for no reason and
	// would replace a hand-written layout with this package's own.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	body := read(t, path)

	if err := ed.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Errorf("a save with nothing to write rewrote the file (%v then %v)", first.ModTime(), second.ModTime())
	}
	if got := read(t, path); got != body {
		t.Errorf("a save with nothing to write changed the bytes:\n%s", got)
	}
}

func TestASecondEditSavesOnTopOfTheFirstWithoutReopening(t *testing.T) {
	// The divergence check compares against what this editor last saw, which
	// includes what it last wrote. An editor that only ever remembered the
	// bytes it opened would refuse its own second save.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := ed.SetProject("atlas", contract()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := back.Engagements.Client("Northwind"); !ok {
		t.Error("the first edit did not survive the second save")
	}
	if _, ok := back.Engagements.Project("atlas"); !ok {
		t.Error("the second edit was not written")
	}
}

func TestSavingNothingOnAnUnconfiguredMachineCreatesNoFile(t *testing.T) {
	// A machine with no config and no engagement to add has nothing to say, and
	// a file containing {} says it no better than no file does. Load reads the
	// absence as the defaults either way, so creating one would only leave a
	// reader wondering what it was for.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a save with nothing to say created %s: %v", path, err)
	}
}

func TestAFileWithADuplicateKeyIsNotWrittenBackWithTwo(t *testing.T) {
	// JSON permits a repeated key and Go's decoder takes the last one, so a
	// hand-edited file can carry two `engagements` blocks without failing to
	// load. Writing both back — each holding the new section — would leave a
	// file that says the same thing twice and diverges the moment somebody
	// edits one of them.
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "week"}}},
		"engagements": {"projects": {"atlas": {
		"contract_total": 24000, "expected_hours": 160}}}}`)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Bluebird", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := strings.Count(read(t, path), `"engagements"`); got != 1 {
		t.Errorf("the file was written back with %d engagements keys:\n%s", got, read(t, path))
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	if _, ok := back.Engagements.Client("Bluebird"); !ok {
		t.Errorf("the edit was lost: %+v", back.Engagements)
	}
}

func TestRemovingTheLastEngagementLeavesNoEmptySectionBehind(t *testing.T) {
	path := write(t, `{"engagements": {"clients": {"Northwind": {
		"rate": 150, "budgeted_hours": 30, "period": "week"}}}}`)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !ed.RemoveClient("Northwind") {
		t.Fatal("RemoveClient reported nothing to remove")
	}
	if ed.RemoveClient("Northwind") {
		t.Error("RemoveClient reported removing something twice")
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if written := read(t, path); strings.Contains(written, "engagements") {
		t.Errorf("an empty engagements section was left behind:\n%s", written)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load what Save wrote: %v", err)
	}
	if !back.Engagements.IsEmpty() {
		t.Errorf("the removal did not survive the write: %+v", back.Engagements)
	}
}

func TestAnEngagementWithNoNameIsRefusedByTheSameValidatorTheLoaderUses(t *testing.T) {
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = ed.SetClient("", hourly())
	if err == nil {
		t.Fatalf("an engagement with no name was accepted")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("the refusal is not the loader's own: %v", err)
	}
}

func TestWhatWasWrittenIsIndentedJSONRatherThanOneLine(t *testing.T) {
	// The file stays hand-editable, which is the whole reason the divergence
	// check exists. A single line of JSON is a file nobody will hand-edit
	// again.
	path := missing(t)
	ed, err := config.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ed.SetClient("Northwind", hourly()); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := ed.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	written := read(t, path)
	if !strings.HasSuffix(written, "\n") {
		t.Errorf("the file does not end in a newline: %q", written)
	}
	if strings.Count(written, "\n") < 5 {
		t.Errorf("the file was written as one line:\n%s", written)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(written), &probe); err != nil {
		t.Fatalf("what was written is not a JSON object: %v", err)
	}
}
