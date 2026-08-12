package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The staleness bound `timetattle status` judges freshness against, derived
// from the schedule that is actually installed.
//
// # The defect this replaces
//
// status bound -stale to a hard-coded 24h and knew nothing about the schedule
// it was checking. The installed agent runs every 12h, so the age of the newest
// run only reached 24h at the instant the next run was already due: a single
// skipped harvest could not be reported, by construction, and it took two in a
// row. On 2026-08-12 the store held no harvest row, no capture and no event
// stamped that day, and status printed "every source ran and succeeded within
// the last 24h00m".
//
// That is the failure mode CLAUDE.md names three times over: a threshold taken
// as an argument that nobody reconciled against the thing it checks, producing
// a clean report indistinguishable from a correct one.
//
// The schedule has since changed underneath this file and this file did not
// change with it: on 2026-08-12 the agent became six StartCalendarInterval
// slots four hours apart, StartInterval was removed, and the bound followed
// from 18h to 6h with nothing here edited. That is the whole argument for
// deriving it.
//
// # Why the installed plist and not the config
//
// Because it cannot go stale. A cadence written into config.json is a record of
// what someone meant to install; ~/Library/LaunchAgents/<label>.plist IS what
// launchd will run. If the two ever disagree, the plist is the one that decides
// whether a harvest happens, so it is the one a watchdog has to read.
//
// # What this deliberately does NOT read
//
// The store's own rhythm. Measured on the live store on 2026-08-12, the 35
// inter-harvest gaps since 2026-07-30 have a mean of 8.49h against a nominal
// 12h interval — which reads like the store contradicting the plist and is not.
// A StartInterval timer cannot fire early: launchd restarts the timer at each
// fire, so every gap between two SCHEDULED runs is at least the interval. A
// mean below the interval therefore proves that most of those harvests were not
// scheduled fires at all — they are RunAtLoad at each login, launchctl
// kickstart, and harvests run by hand. The plist measures the requested
// cadence; harvest_run measures every invocation from any cause. They are
// different populations and cannot contradict each other.
//
// So the bound is not derived from the runs. Deriving it from them would let a
// burst of manual harvests tighten the bound until the check cried wolf, and a
// quiet stretch loosen it exactly when it should not.
//
// # What it still cannot see
//
// status reads harvest_run.started_at, so it reports whether a HARVEST RAN and
// never whether EVIDENCE ARRIVED. A harvest that fires on time and ingests
// nothing advances the staleness clock while the store stands still, and no
// bound derived from any schedule can see that. Watching the store advance is a
// separate check; this one is not it, and must not be quoted as if it were.

// harvestAgentLabel is the launchd label scripts/install-agent.sh installs
// under. It is a second copy of a string that lives in that script, which is
// exactly the kind of copy this project has watched go stale — so the failure
// is made loud rather than silent: if the label ever changes, the plist is not
// found at this path and the report says in words that no schedule is installed
// and names the path it looked at, rather than inventing a bound.
const harvestAgentLabel = "com.bensyverson.timetattle.harvest"

// defaultSchedulePath is where the installed agent's plist sits.
func defaultSchedulePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", harvestAgentLabel+".plist"), nil
}

// scheduleState is what was learned about the installed schedule. Four states
// and not two: "there is a schedule", "there is none", "one could not be read"
// and "none was asked for" are four different facts, and collapsing the last
// three into a default bound is the bug being fixed.
type scheduleState int

const (
	// scheduleNotConsulted is no path having been given at all.
	scheduleNotConsulted scheduleState = iota
	scheduleFound
	// scheduleAbsent is having looked and found no plist: an observation, and a
	// different one from having been unable to look.
	scheduleAbsent
	// scheduleUnscheduled is a plist that parses and schedules nothing. launchd
	// runs such an agent at load and never again, so there is no cadence to be
	// late against — which is not a healthy schedule and must not print as one.
	scheduleUnscheduled
	scheduleUnreadable
)

func (s scheduleState) String() string {
	switch s {
	case scheduleNotConsulted:
		return "not consulted"
	case scheduleFound:
		return "found"
	case scheduleAbsent:
		return "absent"
	case scheduleUnscheduled:
		return "unscheduled"
	default:
		return "unreadable"
	}
}

// schedule is the installed cadence, or the reason there is not one.
type schedule struct {
	state scheduleState
	path  string
	// key names the plist key the cadence came from, so that a reader can tell
	// which schedule produced which verdict.
	key string
	// interval is the WIDEST gap between two consecutive fires, and narrowest
	// the smallest. They are equal for StartInterval and for an evenly spaced
	// calendar; where they differ, see uneven below.
	interval  time.Duration
	narrowest time.Duration
	err       error
}

// bound is how old the newest run may be before the schedule is called stale,
// and whether there is any basis for the question at all.
//
// One interval and a half. The multiple is the whole point of this file, so it
// is worth being exact about why it is not two: at twice the interval the age
// only reaches the bound at the instant the next fire falls due, so one missed
// run is invisible by construction and it takes two — which is the defect. At
// one and a half, a missed fire is reported half an interval after it should
// have happened and half an interval before the next one is due, so a single
// miss trips and the report still has six hours of slack on a 12h schedule for
// launchd deferring a run over a sleep.
//
// The slack is not for the work: the longest whole harvest ever recorded here
// is 76.17s. It is for launchd deferring a run, and it is why the bound is not
// tighter still. Against the 35 inter-harvest gaps measured on 2026-08-12 under
// the 12h interval this replaced, a bound of 18h would have reported the 6 gaps
// over 17h and a bound of 13h up to 13 of the 35.
//
// # What the bound is NOT
//
// It is not a promise about wall-clock time, and the report must never read it
// as one. launchd fires a missed calendar slot within a second of the machine
// waking — measured against com.apple.newsyslog, which slept through its 13:30
// slot and ran at 13:34:57.652, 0.65s after a dark wake — so on a machine that
// is up, the age of the newest run really is bounded by the cadence. It cannot
// run at all while the machine is off, and nothing in launchd or in this file
// bounds how long a laptop stays shut.
//
// Two things keep that from turning the check into a wolf-crier. A hole that
// opens while the machine is asleep is closed within a second of the wake, and
// this report can only be read on a machine that is running — so by the time
// anyone looks, the catch-up run has already advanced the newest run. And where
// the age is genuinely past the bound, the verdict states the age and says in
// so many words that harvest_run cannot tell a missed harvest from a machine
// that was not running. It reports the reading; it does not attribute a fault.
func (s schedule) bound() (time.Duration, bool) {
	if s.state != scheduleFound || s.interval <= 0 {
		return 0, false
	}
	return s.interval + s.interval/2, true
}

// uneven says whether the fires are spaced differently through the cycle, which
// is the honest limit of a single derived bound.
//
// A calendar schedule at 09:00, 12:00 and 15:00 is three hours apart in the day
// and eighteen apart overnight. The bound has to clear the widest gap or the
// report would call every night stale — and that means a run skipped in the
// tight stretch is not reported until the wide one has elapsed, which is the
// blindness being fixed, wearing a calendar. The report says so in words for
// that reason. The tighter check is to compare the newest run against the most
// recent scheduled fire TIME rather than against any single duration; it is not
// built here, and this is the flag that says when it would matter.
func (s schedule) uneven() bool {
	return s.state == scheduleFound && s.narrowest > 0 && s.narrowest < s.interval
}

// readSchedule reads the installed plist and says what cadence it asks for.
//
// It never returns a default. Every way of failing to find a cadence is
// reported as itself.
func readSchedule(path string) schedule {
	s := schedule{path: path}
	if path == "" {
		s.state = scheduleNotConsulted
		return s
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = scheduleAbsent
		return s
	}
	if err != nil {
		s.state, s.err = scheduleUnreadable, err
		return s
	}
	root, err := decodePlist(data)
	if err != nil {
		s.state, s.err = scheduleUnreadable, err
		return s
	}
	if root.kind != "dict" {
		s.state, s.err = scheduleUnreadable, fmt.Errorf("the plist holds a <%s> rather than a <dict>", root.kind)
		return s
	}

	// Both keys at once is what a half-finished reinstall leaves behind, and
	// launchd does not document which one wins. The wider of the two is taken,
	// because a bound that is too wide only delays the news while one that is
	// too tight discredits every other line in the report — and both keys are
	// named, so that a reader can see the plist has two schedules in it.
	var keys []string
	for _, k := range []struct {
		name string
		read func(plistValue) (time.Duration, time.Duration, error)
	}{
		{"StartInterval", readStartInterval},
		{"StartCalendarInterval", readStartCalendarInterval},
	} {
		v, ok := root.dict[k.name]
		if !ok {
			continue
		}
		widest, narrowest, err := k.read(v)
		if err != nil {
			s.state, s.err = scheduleUnreadable, fmt.Errorf("%s: %w", k.name, err)
			return s
		}
		keys = append(keys, k.name)
		if widest > s.interval {
			s.interval = widest
		}
		if s.narrowest == 0 || narrowest < s.narrowest {
			s.narrowest = narrowest
		}
	}
	if len(keys) == 0 {
		s.state = scheduleUnscheduled
		return s
	}
	s.state, s.key = scheduleFound, strings.Join(keys, " and ")
	return s
}

// readStartInterval is the simple shape: one number of seconds between runs.
func readStartInterval(v plistValue) (time.Duration, time.Duration, error) {
	n, ok := v.integer()
	if !ok {
		return 0, 0, fmt.Errorf("a <%s> of %q rather than an <integer>", v.kind, v.text)
	}
	if n <= 0 {
		// Not a cadence, and a bound derived from it would call every store
		// stale for ever.
		return 0, 0, fmt.Errorf("%d is not an interval", n)
	}
	d := time.Duration(n) * time.Second
	return d, d, nil
}

// calendarSpec is one StartCalendarInterval entry, with -1 for every field
// launchd treats as a wildcard.
//
// Wildcards are the reason this is enumerated rather than read off. A dict of
// {Hour: 9} with no Minute does not mean 09:00 — launchd fires every minute of
// that hour, sixty times — and a parser that assumed otherwise would report a
// cadence the machine does not have.
type calendarSpec struct {
	minute, hour, day, weekday, month int
}

// readStartCalendarInterval derives a cadence from fixed times of day.
//
// The value is a dict or an array of dicts; both shapes are launchd's and both
// are read, because a parser that handled only the array would read a
// once-a-day schedule as no schedule at all.
func readStartCalendarInterval(v plistValue) (time.Duration, time.Duration, error) {
	var entries []plistValue
	switch v.kind {
	case "dict":
		entries = []plistValue{v}
	case "array":
		entries = v.array
	default:
		return 0, 0, fmt.Errorf("a <%s> rather than a <dict> or an <array>", v.kind)
	}
	if len(entries) == 0 {
		return 0, 0, errors.New("an empty array schedules nothing")
	}

	specs := make([]calendarSpec, 0, len(entries))
	for _, e := range entries {
		spec, err := readCalendarSpec(e)
		if err != nil {
			return 0, 0, err
		}
		specs = append(specs, spec)
	}
	return calendarGaps(specs)
}

func readCalendarSpec(v plistValue) (calendarSpec, error) {
	if v.kind != "dict" {
		return calendarSpec{}, fmt.Errorf("an entry that is a <%s> rather than a <dict>", v.kind)
	}
	spec := calendarSpec{minute: -1, hour: -1, day: -1, weekday: -1, month: -1}
	fields := []struct {
		key       string
		into      *int
		low, high int
	}{
		{"Minute", &spec.minute, 0, 59},
		{"Hour", &spec.hour, 0, 23},
		{"Day", &spec.day, 1, 31},
		// launchd takes both 0 and 7 for Sunday.
		{"Weekday", &spec.weekday, 0, 7},
		{"Month", &spec.month, 1, 12},
	}
	for _, f := range fields {
		e, ok := v.dict[f.key]
		if !ok {
			continue
		}
		n, ok := e.integer()
		if !ok {
			return spec, fmt.Errorf("%s is a <%s> of %q rather than an <integer>", f.key, e.kind, e.text)
		}
		// Out of range is reported rather than ignored. A mistyped Hour of 25
		// would otherwise silently become a schedule that never fires, which is
		// the absence collapse this file exists to prevent.
		if n < f.low || n > f.high {
			return spec, fmt.Errorf("%s is %d, outside %d-%d", f.key, n, f.low, f.high)
		}
		*f.into = n
	}
	return spec, nil
}

// calendarReference is the instant the enumeration below starts from: midnight
// on a Monday, at the start of a leap-year cycle, so that both weekday and
// day-of-month schedules are walked over whole cycles of themselves.
var calendarReference = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// calendarGaps walks one whole cycle of a calendar schedule and reports the
// widest and narrowest gaps between consecutive fires, and how many fires there
// are in the cycle.
//
// The cycle is the shortest period the schedule repeats over: a day when only
// Minute and Hour are constrained, a week once a Weekday is, and four years
// once a Day or a Month is — four rather than one so that a 29 February is
// included. A Weekday combined with a Day or a Month is not exactly periodic in
// four years and is approximated; scripts/install-agent.sh does not produce
// that combination, and every field here is ANDed, which is launchd's reading
// rather than cron's (cron ORs Day against Weekday).
//
// The wrap from the last fire of the cycle to the first fire of the next one is
// counted as a gap like any other. Without it a schedule that fires once a day
// would have no gaps at all and would produce no bound.
func calendarGaps(specs []calendarSpec) (time.Duration, time.Duration, error) {
	days := 1
	for _, s := range specs {
		if s.weekday >= 0 && days < 7 {
			days = 7
		}
		if s.day >= 0 || s.month >= 0 {
			days = 1461
		}
	}

	var (
		fires     int
		first     time.Time
		prev      time.Time
		widest    time.Duration
		narrowest time.Duration
	)
	for day := range days {
		date := calendarReference.AddDate(0, 0, day)
		for _, minute := range calendarMinutes(specs, date) {
			at := date.Add(time.Duration(minute) * time.Minute)
			fires++
			if fires == 1 {
				first = at
				prev = at
				continue
			}
			gap := at.Sub(prev)
			if gap > widest {
				widest = gap
			}
			if narrowest == 0 || gap < narrowest {
				narrowest = gap
			}
			prev = at
		}
	}
	if fires == 0 {
		return 0, 0, errors.New("nothing in it ever fires")
	}
	// The wrap: from the last fire of this cycle to the same schedule's first
	// fire in the next one.
	cycle := calendarReference.AddDate(0, 0, days)
	wrap := cycle.Sub(prev) + first.Sub(calendarReference)
	if wrap > widest {
		widest = wrap
	}
	if narrowest == 0 || wrap < narrowest {
		narrowest = wrap
	}
	return widest, narrowest, nil
}

// calendarMinutes is every minute of a day at which any of the specs fires, in
// ascending order.
func calendarMinutes(specs []calendarSpec, date time.Time) []int {
	var minutes []int
	seen := map[int]bool{}
	for _, s := range specs {
		if !calendarMatchesDate(s, date) {
			continue
		}
		for hour := range 24 {
			if s.hour >= 0 && s.hour != hour {
				continue
			}
			for minute := range 60 {
				if s.minute >= 0 && s.minute != minute {
					continue
				}
				if at := hour*60 + minute; !seen[at] {
					seen[at] = true
					minutes = append(minutes, at)
				}
			}
		}
	}
	sort.Ints(minutes)
	return minutes
}

func calendarMatchesDate(s calendarSpec, date time.Time) bool {
	if s.month >= 0 && s.month != int(date.Month()) {
		return false
	}
	if s.day >= 0 && s.day != date.Day() {
		return false
	}
	// launchd takes both 0 and 7 for Sunday, and time.Weekday has Sunday at 0.
	if s.weekday >= 0 && s.weekday%7 != int(date.Weekday()) {
		return false
	}
	return true
}

// staleBound is the age the report judges freshness against, and where it came
// from. ok is false when there is no basis for the judgement at all, which is
// reported and never quietly replaced with a default.
type staleBound struct {
	d        time.Duration
	ok       bool
	explicit bool
	sched    schedule
}

// resolveStale works out what the report may judge freshness against.
//
// An explicit -stale wins, and the schedule is still read so that the report
// can say what the flag overrode. When the flag is given, an unreadable
// schedule is no longer load-bearing and does not set the exit status: the
// report can do its job without it.
func resolveStale(opt statusOptions) staleBound {
	b := staleBound{sched: readSchedule(opt.plist)}
	if opt.stale > 0 {
		b.d, b.ok, b.explicit = opt.stale, true, true
		return b
	}
	b.d, b.ok = b.sched.bound()
	return b
}

// unchecked is why freshness could not be judged, in words, and empty when it
// could.
func (b staleBound) unchecked() string {
	if b.ok {
		return ""
	}
	switch b.sched.state {
	case scheduleAbsent:
		return fmt.Sprintf("no schedule is installed at %s, and no -stale was given", b.sched.path)
	case scheduleUnreadable:
		return fmt.Sprintf("the schedule at %s could not be read (%v)", b.sched.path, b.sched.err)
	case scheduleUnscheduled:
		return fmt.Sprintf("%s carries neither StartInterval nor StartCalendarInterval, so launchd "+
			"runs it at load and never again", b.sched.path)
	default:
		return "no installed schedule was consulted, and no -stale was given"
	}
}

// shortReason is the same fact as unchecked, for the header line that sits
// directly under the schedule line and does not need to name the path twice.
func (b staleBound) shortReason() string {
	switch b.sched.state {
	case scheduleAbsent:
		return "nothing is installed there to derive a bound from"
	case scheduleUnreadable:
		return "that schedule could not be read"
	case scheduleUnscheduled:
		return "that plist asks for no cadence at all"
	default:
		return "no installed schedule was consulted and no -stale was given"
	}
}

// failure is the error an unreadable schedule sets, and nil for every other way
// of having no bound.
//
// Only the unreadable case, on the same footing as an unreadable capture_run
// table: the report was unable to take a reading it is supposed to take. Having
// looked and found no schedule installed is an observation rather than a
// failure, and gets the same tolerance a source that has never run gets — a
// machine that harvests by hand is a machine with no installed schedule, and a
// check that cries wolf is a check nobody reads.
func (b staleBound) failure() error {
	if b.explicit || b.sched.state != scheduleUnreadable {
		return nil
	}
	return fmt.Errorf("the installed schedule at %s could not be read: %w", b.sched.path, b.sched.err)
}

// writeSchedule is the two header lines that say which schedule produced the
// verdict and what bound came out of it. A report that printed a verdict
// without them would be a clean report indistinguishable from a correct one.
func writeSchedule(out io.Writer, b staleBound) {
	s := b.sched
	switch s.state {
	case scheduleNotConsulted:
		fmt.Fprintf(out, "schedule:  not consulted\n")
	case scheduleFound:
		fmt.Fprintf(out, "schedule:  every %s, from %s in %s\n", short(s.interval), s.key, s.path)
		if s.uneven() {
			fmt.Fprintf(out, "           it fires unevenly, %s apart at the closest and %s at the widest, "+
				"so a run skipped in the close stretch is not reported until the wide one has passed\n",
				short(s.narrowest), short(s.interval))
		}
	case scheduleAbsent:
		fmt.Fprintf(out, "schedule:  none installed at %s\n", s.path)
	case scheduleUnscheduled:
		fmt.Fprintf(out, "schedule:  %s schedules nothing: it carries neither StartInterval nor "+
			"StartCalendarInterval\n", s.path)
	case scheduleUnreadable:
		fmt.Fprintf(out, "schedule:  unreadable at %s (%v)\n", s.path, s.err)
	}

	switch {
	case !b.ok:
		fmt.Fprintf(out, "stale:     not checked: %s\n", b.shortReason())
	case b.explicit && s.state == scheduleFound:
		derived, _ := s.bound()
		fmt.Fprintf(out, "stale:     %s, given with -stale, overriding the %s that schedule implies\n",
			short(b.d), short(derived))
	case b.explicit:
		fmt.Fprintf(out, "stale:     %s, given with -stale\n", short(b.d))
	default:
		fmt.Fprintf(out, "stale:     %s, an interval and a half of that schedule\n", short(b.d))
	}
}

// plistValue is the subset of the plist grammar this file needs: dicts, arrays
// and leaves kept as their text.
//
// encoding/xml rather than a dependency or /usr/bin/plutil. A dependency for
// this would be four hundred lines of somebody else's plist grammar to read one
// integer; a subprocess would make the whole test suite depend on being run on
// macOS, and would put a fork between the check and the answer at exactly the
// moment the machine is already unhappy.
type plistValue struct {
	kind  string
	text  string
	dict  map[string]plistValue
	array []plistValue
}

// integer is the value as a number, and whether it was one.
func (v plistValue) integer() (int, bool) {
	if v.kind != "integer" {
		return 0, false
	}
	n, err := strconv.Atoi(v.text)
	if err != nil {
		return 0, false
	}
	return n, true
}

// decodePlist reads an XML property list down to its root value.
func decodePlist(data []byte) (plistValue, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return plistValue{}, errors.New("no <plist> element")
		}
		if err != nil {
			return plistValue{}, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "plist" {
			continue
		}
		for {
			tok, err := d.Token()
			if err != nil {
				return plistValue{}, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				return decodePlistValue(d, t)
			case xml.EndElement:
				return plistValue{}, errors.New("the <plist> element is empty")
			}
		}
	}
}

func decodePlistValue(d *xml.Decoder, start xml.StartElement) (plistValue, error) {
	v := plistValue{kind: start.Name.Local}
	switch v.kind {
	case "dict":
		v.dict = map[string]plistValue{}
		var (
			key     string
			haveKey bool
		)
		for {
			tok, err := d.Token()
			if err != nil {
				return v, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var s string
					if err := d.DecodeElement(&s, &t); err != nil {
						return v, err
					}
					key, haveKey = strings.TrimSpace(s), true
					continue
				}
				if !haveKey {
					return v, fmt.Errorf("a <%s> with no <key> before it", t.Name.Local)
				}
				child, err := decodePlistValue(d, t)
				if err != nil {
					return v, err
				}
				v.dict[key] = child
				haveKey = false
			case xml.EndElement:
				return v, nil
			}
		}
	case "array":
		for {
			tok, err := d.Token()
			if err != nil {
				return v, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				child, err := decodePlistValue(d, t)
				if err != nil {
					return v, err
				}
				v.array = append(v.array, child)
			case xml.EndElement:
				return v, nil
			}
		}
	default:
		// Every leaf, including <true/> and <false/>, which decode to the empty
		// string and are consumed whole.
		var s string
		if err := d.DecodeElement(&s, &start); err != nil {
			return v, err
		}
		v.text = strings.TrimSpace(s)
		return v, nil
	}
}
