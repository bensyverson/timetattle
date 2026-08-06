package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
)

// Calendar says when a working day begins, and when a week does.
//
// A day is not a date. Work that ran from 21:33 to 01:37 was one evening, and
// the boundary is what lets a report say so. See internal/sessions'
// calendar.go for the whole argument, including what happens when the boundary
// lands in an hour that daylight saving repeated or skipped.
type Calendar struct {
	// Timezone is an IANA name — "America/Chicago", "UTC" — resolved through
	// the zoneinfo database. Empty means the machine's own zone.
	//
	// A fixed offset is deliberately not accepted, and "-05:00" is refused
	// rather than parsed. An offset cannot say WHEN it changes, and the only
	// hard part of this problem is that it changes twice a year: a working day
	// spanning a transition is 23 or 25 hours long, and a constant offset
	// would report 24 and be wrong by an hour on both sides.
	Timezone string `json:"timezone"`

	// DayStart is the wall clock a working day begins at, written "05:00".
	//
	// The default is 05:00 rather than something later because across the live
	// corpus the earliest morning sitting begins at 05:06:24 local. That is
	// only 6m24s of clearance, and the sitting's start already includes a
	// five-minute lead-in, so a lead-in much larger than that would push a
	// real morning back across the boundary into the day before.
	DayStart Clock `json:"day_start"`

	// WeekStart is the weekday a week begins on, written "monday". The week
	// begins at DayStart on that day, not at midnight, or the first hours of
	// every week would belong to the week before it.
	WeekStart Weekday `json:"week_start"`
}

// defaultCalendar is the calendar of a machine with no config file: days from
// 05:00, weeks from Monday (ISO 8601), in whatever zone the machine is set to.
func defaultCalendar() Calendar {
	return Calendar{
		Timezone:  "",
		DayStart:  Clock{Hour: 5},
		WeekStart: Weekday(time.Monday),
	}
}

// Sessions builds the calendar internal/sessions works in, loading the zone.
//
// The location is loaded here rather than stored, so that the zoneinfo
// database is the authority on every offset and nothing in Timetattle ever
// holds an offset as a value.
func (c Calendar) Sessions() (sessions.Calendar, error) {
	loc := time.Local
	if c.Timezone != "" {
		var err error
		if loc, err = time.LoadLocation(c.Timezone); err != nil {
			return sessions.Calendar{}, fmt.Errorf(
				"calendar.timezone %q is not a zone this machine knows: %w; "+
					"write an IANA name such as \"America/Chicago\" or \"UTC\", not an offset",
				c.Timezone, err)
		}
	}
	cal := sessions.Calendar{
		Location:  loc,
		DayStart:  sessions.Clock{Hour: c.DayStart.Hour, Minute: c.DayStart.Minute},
		WeekStart: c.WeekStart.Weekday(),
	}
	if err := cal.Validate(); err != nil {
		return sessions.Calendar{}, err
	}
	return cal, nil
}

// validate reports why a calendar section cannot be used. It loads the zone,
// because a name nothing resolves is a mistake that should fail at the config
// file rather than at the first report that needed a day.
func (c Calendar) validate() error {
	_, err := c.Sessions()
	return err
}

// Clock is a wall-clock time of day written "05:00".
//
// It is a string rather than a bare hour so that half-past is expressible, and
// so that the file reads as the thing it configures. A bare 5 would also be
// ambiguous in the one direction that matters: 5 could be read as an hour or
// as a duration, and this file already carries durations.
type Clock struct{ Hour, Minute int }

// String renders the clock the way it is written in the file.
func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// MarshalJSON writes the clock as "HH:MM", so a config this package prints is
// a config it can read back.
func (c Clock) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// UnmarshalJSON reads "05:00" and refuses everything else, including a bare 5.
func (c *Clock) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("%s is not a time of day; write one as a quoted \"HH:MM\", such as \"05:00\"", raw)
	}
	at, err := time.Parse("15:04", text)
	if err != nil {
		return fmt.Errorf("%q is not a time of day; write one as \"05:00\" or \"04:30\"", text)
	}
	c.Hour, c.Minute = at.Hour(), at.Minute()
	return nil
}

// Weekday is a day of the week written the way a person writes one.
//
// A number would be worse than unreadable, it would be ambiguous: Go counts
// weeks from Sunday, ISO 8601 counts them from Monday, and a 1 in a config
// file means a different day depending on which convention the reader has in
// mind. A name has no convention to get wrong.
type Weekday time.Weekday

// Weekday is the value as the standard library's type.
func (w Weekday) Weekday() time.Weekday { return time.Weekday(w) }

// String renders the weekday the way it is written in the file.
func (w Weekday) String() string { return strings.ToLower(time.Weekday(w).String()) }

// MarshalJSON writes the weekday as its lower-case name.
func (w Weekday) MarshalJSON() ([]byte, error) { return json.Marshal(w.String()) }

// UnmarshalJSON reads "monday" in any case, and refuses a number.
func (w *Weekday) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("%s is not a weekday; write one as a quoted name such as \"monday\". "+
			"A number is refused because Go counts weeks from Sunday and ISO 8601 from Monday", raw)
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(text, d.String()) {
			*w = Weekday(d)
			return nil
		}
	}
	return fmt.Errorf("%q is not a weekday; write the full name, such as \"monday\" or \"sunday\"", text)
}
