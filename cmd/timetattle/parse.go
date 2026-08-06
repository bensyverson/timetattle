package main

import (
	"fmt"
	"strings"
	"time"
)

// parseBoundary reads a -from or -to flag, accepting a bare date as well as a
// full instant. It is shared by every subcommand taking a span, so that
// "2026-07-11" means the same thing to sessionize as it does to chains.
//
// It lives here rather than in either subcommand because it arrived twice,
// written independently and differently: one version accepted three fewer
// layouts and could not name the flag that was wrong. A bare date is taken in the machine's own time zone, because that
// is what somebody typing 2026-07-11 means; the store normalises to UTC.
func parseBoundary(text, name string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("-%s %q is not an instant; write 2026-07-11 or 2026-07-11T09:00:00Z", name, text)
}

// parseUpperBoundary reads a -to flag for a REPORT WINDOW, where a bare date
// names a whole day rather than the instant it begins.
//
// THE DEFECT IT FIXES. -to takes an instant, so `-to 2026-08-03` meant
// 2026-08-03T00:00:00 and silently dropped the whole of the day the user named.
// store.Filter bounds a span INCLUSIVELY at both ends, which makes
// `-from D -to D` — much the most natural way to ask for one day — select only
// whatever landed exactly on midnight, which on this corpus is nothing at all.
// Every date-bounded report inherited it.
//
// WHY THIS IS NOT A VIOLATION OF THE SHARED-PARSER RULE beside it. What
// parseBoundary exists to keep identical is the set of date FORMS every
// subcommand accepts — it arrived twice, written differently, accepting three
// fewer layouts in one of them. This changes what a parsed date MEANS at the
// upper end of a window, which `timetattle review` already does on its own
// account: reviewSpan snaps its -to through cal.Bounds to the end of the
// working day it falls in. So end-of-day at -to is the behaviour this codebase
// already had in the one command that thought about it, and the odd ones out
// were the commands that had not.
//
// WHY ONLY A BARE DATE. A bare date names a DAY; an instant names an INSTANT.
// Somebody who writes `-to 2026-08-03T09:00:00` was precise, and rounding that
// up to midnight would silently widen a window they meant to narrow. The two
// are distinguished by re-parsing, which is cheap and is the only signal there
// is once a time.Time exists.
//
// WHY THE LAST MICROSECOND rather than the following midnight. The filter is
// inclusive, so the following midnight would pull in an event stamped exactly
// 00:00:00.000000 on the next day. Every instant in the store is written at
// microsecond resolution (store.TimeLayout), so backing off one microsecond
// excludes that and can exclude nothing else.
//
// IT IS NOT USED BY `timetattle adjust`, deliberately. That command's -to is
// not a report window: it is the end of an ASSERTION somebody is writing down —
// "I was engaged from 09:00 to 17:00" — and silently making a bare date there
// mean a day-long claim would put a fact in the store that nobody stated.
func parseUpperBoundary(text, name string) (time.Time, error) {
	at, err := parseBoundary(text, name)
	if err != nil || at.IsZero() {
		return at, err
	}
	if _, bare := time.ParseInLocation("2006-01-02", text, time.Local); bare != nil {
		return at, nil
	}
	return at.AddDate(0, 0, 1).Add(-time.Microsecond), nil
}

// splitCommaList turns a comma-separated flag value into a trimmed,
// non-empty slice, shared by -filedays-exclude so a stray space in the flag
// does not turn into a directory name nothing will ever match.
func splitCommaList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
