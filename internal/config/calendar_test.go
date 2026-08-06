package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
)

func TestTheDefaultWorkingDayBeginsAtFiveInTheMorning(t *testing.T) {
	c := config.Default()
	if got := c.Calendar.DayStart.String(); got != "05:00" {
		t.Errorf("the default day start is %q, want \"05:00\"", got)
	}
	if got := c.Calendar.WeekStart.Weekday(); got != time.Monday {
		t.Errorf("the default week starts on %v, want Monday", got)
	}
	if c.Calendar.Timezone != "" {
		t.Errorf("the default timezone is %q, want empty, meaning the machine's own zone",
			c.Calendar.Timezone)
	}
}

// An empty timezone is the machine's own, resolved through the zoneinfo
// database rather than baked in as an offset.
func TestAnUnsetTimezoneIsTheMachinesOwn(t *testing.T) {
	c := config.Default()
	cal, err := c.Calendar.Sessions()
	if err != nil {
		t.Fatalf("build a calendar from the defaults: %v", err)
	}
	if cal.Location != time.Local {
		t.Errorf("the calendar's location is %v, want time.Local", cal.Location)
	}
}

func TestTheCalendarSectionIsReadFromTheFile(t *testing.T) {
	path := write(t, `{
		"calendar": {"timezone": "America/Chicago", "day_start": "04:30", "week_start": "sunday"}
	}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Calendar.DayStart.String(); got != "04:30" {
		t.Errorf("day_start read as %q, want \"04:30\"", got)
	}
	if got := c.Calendar.WeekStart.Weekday(); got != time.Sunday {
		t.Errorf("week_start read as %v, want Sunday", got)
	}
	cal, err := c.Calendar.Sessions()
	if err != nil {
		t.Fatalf("build a calendar: %v", err)
	}
	if cal.Location.String() != "America/Chicago" {
		t.Errorf("the location is %v, want America/Chicago", cal.Location)
	}
	if cal.DayStart.Hour != 4 || cal.DayStart.Minute != 30 {
		t.Errorf("the calendar's day start is %v, want 04:30", cal.DayStart)
	}
}

// The gap threshold is not the only value with leverage. A misread boundary
// moves a whole evening to the wrong day, so a boundary that cannot be
// understood is refused rather than replaced with the default.
func TestAnUnreadableDayStartIsRefusedAndQuoted(t *testing.T) {
	for _, bad := range []string{`"5"`, `"25:00"`, `"05:60"`, `"five"`, `5`, `"05:00:00"`, `""`} {
		path := write(t, `{"calendar": {"day_start": `+bad+`}}`)
		_, err := config.Load(path)
		if err == nil {
			t.Errorf("day_start %s was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), strings.Trim(bad, `"`)) && bad != `5` {
			t.Errorf("day_start %s was refused with %q, which does not quote the value", bad, err)
		}
	}
}

func TestAnUnreadableWeekStartIsRefusedAndQuoted(t *testing.T) {
	for _, bad := range []string{`"funday"`, `"mon"`, `1`, `""`} {
		path := write(t, `{"calendar": {"week_start": `+bad+`}}`)
		if _, err := config.Load(path); err == nil {
			t.Errorf("week_start %s was accepted", bad)
		}
	}
}

// A weekday is written the way a person writes one, and case is not a way to
// get a different answer.
func TestWeekStartAcceptsAnyCase(t *testing.T) {
	for text, want := range map[string]time.Weekday{
		"monday": time.Monday, "Monday": time.Monday, "MONDAY": time.Monday,
		"sunday": time.Sunday, "saturday": time.Saturday,
	} {
		path := write(t, `{"calendar": {"week_start": "`+text+`"}}`)
		c, err := config.Load(path)
		if err != nil {
			t.Errorf("week_start %q: %v", text, err)
			continue
		}
		if got := c.Calendar.WeekStart.Weekday(); got != want {
			t.Errorf("week_start %q read as %v, want %v", text, got, want)
		}
	}
}

// A zone that is not in the zoneinfo database is a person's mistake, and a
// fixed offset is not a zone at all: it cannot say when the offset changes,
// which is the only hard part of the problem.
func TestAnUnknownTimezoneIsRefused(t *testing.T) {
	for _, bad := range []string{"Mars/Olympus", "-05:00", "CDT", "UTC-5"} {
		path := write(t, `{"calendar": {"timezone": "`+bad+`"}}`)
		if _, err := config.Load(path); err == nil {
			t.Errorf("timezone %q was accepted", bad)
		}
	}
}

// UTC is a real zone in the database and must keep working, for a machine that
// wants no local convention at all.
func TestUTCIsAnAcceptableTimezone(t *testing.T) {
	path := write(t, `{"calendar": {"timezone": "UTC"}}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("timezone UTC: %v", err)
	}
	cal, err := c.Calendar.Sessions()
	if err != nil {
		t.Fatalf("build a calendar: %v", err)
	}
	// A nil *time.Location stringifies as "UTC", so the name alone cannot tell
	// a loaded UTC from a zone that was never set.
	if cal.Location == nil {
		t.Fatal("the calendar has no location; a nil one reads as UTC and would hide the difference")
	}
	if cal.Location.String() != "UTC" {
		t.Errorf("the location is %v, want UTC", cal.Location)
	}
	if err := cal.Validate(); err != nil {
		t.Errorf("the UTC calendar was refused: %v", err)
	}
}

// A file naming only one calendar value keeps the defaults for the others, the
// same way the sessions section already does.
func TestAPartialCalendarSectionKeepsTheOtherDefaults(t *testing.T) {
	path := write(t, `{"calendar": {"day_start": "06:00"}}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Calendar.DayStart.String(); got != "06:00" {
		t.Errorf("day_start is %q, want \"06:00\"", got)
	}
	if got := c.Calendar.WeekStart.Weekday(); got != time.Monday {
		t.Errorf("week_start is %v, want the default Monday", got)
	}
}

// A config this package prints is a config it can read back, so a boundary
// survives a round trip through the file it lives in.
func TestTheCalendarSectionRoundTripsThroughJSON(t *testing.T) {
	path := write(t, `{"calendar": {"timezone": "Australia/Lord_Howe", "day_start": "05:30", "week_start": "sunday"}}`)
	first, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := config.Load(write(t, string(encoded)))
	if err != nil {
		t.Fatalf("re-Load what was written: %v\n%s", err, encoded)
	}
	if first.Calendar != second.Calendar {
		t.Errorf("the calendar came back as %+v, want %+v", second.Calendar, first.Calendar)
	}
}
