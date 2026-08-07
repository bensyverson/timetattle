package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/store"
)

// adjust records a human correction, or reports the ones already recorded.
//
// Every helper in this file is prefixed adjust, for the reason main.go's
// package doc gives: Go package scope spans every file here, and two agents
// working in parallel have already collided on a bare verb.
func adjust(args []string) error { return adjustTo(os.Stdout, args) }

// adjustTo is adjust with the report redirected, so that what the command
// prints can be asserted on rather than merely not crashing.
func adjustTo(stdout io.Writer, args []string) error {
	return executeSubcommand(adjustCmd(stdout), args)
}

func adjustCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adjust",
		Short: "Correct a span by hand, or say you reviewed it",
		Long: `Records a human correction over a span of wall clock, or reports the
corrections already recorded.

CORRECTIONS ARE AN OVERLAY, NOT AN EDIT. Nothing here touches the events, and
nothing here touches derived_session. ` + "`timetattle sessionize`" + ` deletes and
rebuilds every session row on every run, and a correction recorded through this
command survives all of it, because it is keyed to wall clock and to nothing
else. The machine's answer stays beside yours, and the difference between the
two is what the clustering parameters get fitted against.

THE LOG IS APPEND-ONLY. There is no edit and no delete, by construction: the
database refuses both. Changed your mind? Record the new judgment over the same
span. The later one wins for the instants it covers, the earlier one stays
visible, and the fact that you changed your mind is itself worth knowing.

Six corrections, five assertions:

  -assert engaged        this span WAS work — extend a sitting, or confirm one
  -assert not-engaged    this span was NOT work — trim, delete, or split at a
                         real gap
  -assert boundary       one sitting ended and another began at -from, with no
                         gap; splits without changing any total
  -assert attributed     this span belongs to -project
  -assert unattributed   this span belongs to NO project, which is a thing you
                         are saying rather than a thing nobody has said

-review records that you LOOKED at the span, which is not the same as what you
found. A day you reviewed and could not remember and a day you have never
opened are different facts, and only -review unremembered can say the first
one. It removes nothing: the machine's answer stands, marked unconfirmed.

  -review recalled       you looked, and you remember the span
  -review unremembered   you looked, and you could not remember it

-review may be combined with -assert, and the correction is then recorded as
having come out of that review — unless the verdict is unremembered, which
would be a review saying the span cannot be recalled and a correction saying
exactly what happened in it.

Examples:

  timetattle adjust -from "2026-07-28 12:00" -to "2026-07-28 13:00" \
      -assert not-engaged -note "lunch"

  timetattle adjust -from 2026-07-28T09:00:00 -to 2026-07-28T11:00:00 \
      -assert attributed -project some-project

  timetattle adjust -from 2026-07-28 -to 2026-07-29 -review unremembered

  timetattle adjust -from 2026-07-28 -to 2026-07-29 -list`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to record into (default: ~/.timetattle/timetattle.db)")
	from := fs.String("from", "", `start of the span, as 2026-07-28, "2026-07-28 09:00" or 2026-07-28T09:00:00Z`)
	to := fs.String("to", "", "end of the span, in the same forms as -from")
	assert := fs.String("assert", "",
		"what the span was: engaged, not-engaged, boundary, attributed, unattributed")
	project := fs.String("project", "", "project named by -assert attributed")
	review := fs.String("review", "", "record that the span was looked at: recalled or unremembered")
	origin := fs.String("origin", "", "who or what is making this correction (default: $USER)")
	note := fs.String("note", "", "why, in your own words; kept verbatim and never parsed")
	list := fs.Bool("list", false, "report what has already been recorded over the span, and write nothing")
	cmd.RunE = func(*cobra.Command, []string) error {
		start, err := parseBoundary(*from, "from")
		if err != nil {
			return err
		}
		end, err := parseBoundary(*to, "to")
		if err != nil {
			return err
		}

		path := *storePath
		if path == "" {
			if path, err = store.DefaultPath(); err != nil {
				return err
			}
		}

		if *list {
			if *assert != "" || *review != "" {
				return errors.New("-list reports and writes nothing; drop -assert and -review, or drop -list")
			}
			st, err := store.OpenReadOnly(path)
			if err != nil {
				return err
			}
			defer st.Close()
			return adjustReport(stdout, st, start, end)
		}

		if *assert == "" && *review == "" {
			return errors.New("nothing to record: give -assert to say what a span was, " +
				"-review to say you looked at it, or -list to see what is already there")
		}
		if start.IsZero() {
			return errors.New("-from is required; a correction is keyed to a wall-clock span and to nothing else")
		}

		who := *origin
		if who == "" {
			who = os.Getenv("USER")
		}
		// This refusal and the -to one below are for the MESSAGE, not for the
		// outcome. Measured: removing either breaks no test, because
		// adjustments.Adjustment.Validate — which Store.RecordAdjustment calls
		// before touching SQL — already refuses an empty origin and a zero-width
		// assertion. They are kept because "$USER is unset" is a thing a person can
		// act on and "an adjustment must say who or what made it" is not.
		if who == "" {
			return errors.New("-origin is required here, because $USER is unset; " +
				"a correction that names nobody cannot be traced back and re-examined")
		}

		st, err := store.Open(path)
		if err != nil {
			return err
		}
		defer st.Close()

		// One clock reading for the whole call, so a review and the correction that
		// came out of it are recorded at the same instant rather than at two.
		now := time.Now().UTC()
		ctx := context.Background()

		var reviewID int64
		if *review != "" {
			if end.IsZero() {
				return errors.New("-to is required with -review; a review covers a span, not an instant")
			}
			r := adjustments.Review{
				Start: start, End: end, Verdict: adjustments.Verdict(*review),
				Origin: who, RecordedAt: now, Note: *note,
			}
			if reviewID, err = st.RecordReview(ctx, r); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "recorded: looked at %s and %s\n", adjustSpan(start, end), r.Verdict)
		}

		if *assert == "" {
			return nil
		}

		assertion := adjustments.Assertion(*assert)
		if assertion == adjustments.Boundary {
			if !end.IsZero() && !end.Equal(start) {
				return errors.New("a boundary is one instant: give -from and leave -to off, " +
					"or record not-engaged if there was a real gap between the two sittings")
			}
			end = start
		} else if end.IsZero() {
			return errors.New("-to is required; an assertion about no time at all asserts nothing")
		}

		a := adjustments.Adjustment{
			ReviewID: reviewID,
			Start:    start, End: end,
			Assertion: assertion, Project: *project,
			Origin: who, RecordedAt: now, Note: *note,
		}
		if _, err := st.RecordAdjustment(ctx, a); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "recorded: %s %s\n", adjustSpan(start, end), adjustClaim(a))
		return nil
	}
	return cmd
}

// adjustReport prints the log over a span, and what it folds to.
//
// It says out loud which spans were reviewed and not remembered, because that
// is the state a report is most likely to lose: it produces no correction, so a
// listing of corrections alone would show it as nothing at all.
func adjustReport(stdout io.Writer, st *store.Store, from, to time.Time) error {
	ctx := context.Background()
	reviews, err := st.Reviews(ctx, from, to)
	if err != nil {
		return err
	}
	adjs, err := st.Adjustments(ctx, from, to)
	if err != nil {
		return err
	}

	if len(reviews) == 0 && len(adjs) == 0 {
		fmt.Fprintf(stdout, "%s: nothing has been reviewed and nothing has been corrected.\n",
			adjustSpan(from, to))
		fmt.Fprint(stdout, "That is not the same as a span nobody found anything wrong with.\n")
		return nil
	}

	if len(reviews) > 0 {
		fmt.Fprintf(stdout, "Reviewed (%s):\n", plural(len(reviews), "span"))
		for _, r := range reviews {
			fmt.Fprintf(stdout, "  %s  %-12s  %s at %s%s\n", adjustSpan(r.Start, r.End),
				r.Verdict, r.Origin, r.RecordedAt.Format(time.RFC3339), adjustNote(r.Note))
		}
	}
	if len(adjs) > 0 {
		fmt.Fprintf(stdout, "Corrections (%s), oldest first:\n", plural(len(adjs), "row"))
		for _, a := range adjs {
			fmt.Fprintf(stdout, "  %s  %-28s  %s at %s%s\n", adjustSpan(a.Start, a.End),
				adjustClaim(a), a.Origin, a.RecordedAt.Format(time.RFC3339), adjustNote(a.Note))
		}
	}

	o := adjustments.Fold(reviews, adjs)
	if bad := o.Unreadable(); len(bad) > 0 {
		fmt.Fprintf(stdout, "%s could not be interpreted and were left out of the fold below.\n",
			plural(len(bad), "correction"))
	}

	var engaged, notEngaged, unremembered time.Duration
	for _, a := range adjs {
		switch a.Assertion {
		case adjustments.Engaged:
			engaged += a.End.Sub(a.Start)
		case adjustments.NotEngaged:
			notEngaged += a.End.Sub(a.Start)
		}
	}
	for _, r := range reviews {
		if r.Verdict == adjustments.Unremembered {
			unremembered += r.End.Sub(r.Start)
		}
	}

	fmt.Fprintf(stdout, "Asserted work: %v. Asserted not work: %v. Reviewed and not remembered: %v.\n",
		round(engaged), round(notEngaged), round(unremembered))
	fmt.Fprint(stdout, "These are the raw assertions, before the later-wins fold; "+
		"any span outside them is one nobody has said anything about.\n")
	return nil
}

// adjustClaim renders what one adjustment says, so a listing reads as a
// sentence rather than as a column of enum values.
func adjustClaim(a adjustments.Adjustment) string {
	switch a.Assertion {
	case adjustments.Engaged:
		return "was work"
	case adjustments.NotEngaged:
		return "was not work"
	case adjustments.Boundary:
		return "is a boundary between sittings"
	case adjustments.Attributed:
		return "belongs to " + a.Project
	case adjustments.Unattributed:
		return "belongs to no project"
	}
	return string(a.Assertion)
}

// adjustSpan renders a span for a person. An unbounded end is said out loud
// rather than printed as a zero time decades in the past.
func adjustSpan(from, to time.Time) string {
	switch {
	case from.IsZero() && to.IsZero():
		return "the whole store"
	case from.IsZero():
		return "everything up to " + to.Format(time.RFC3339)
	case to.IsZero() || to.Equal(from):
		return from.Format(time.RFC3339)
	}
	return from.Format(time.RFC3339) + ".." + to.Format(time.RFC3339)
}

func adjustNote(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "  — " + note
}
