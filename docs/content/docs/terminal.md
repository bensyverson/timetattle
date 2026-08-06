---
title: "Fitting the terminal"
weight: 12
description: "Why reports shorten themselves to the terminal, why a pipe is never shortened at all, and the order a table gives up its room in."
---

# Fitting the terminal

Every report a person reads — `orient`, `status`, `metrics`, `review` — fits
itself to the terminal it is printing into. Measured on the live store on
2026-08-01, before this existed, against the 80-column terminal it was being
read in:

| command | widest line | |
|---|---|---|
| `timetattle orient` | 80 | hard-wrapped at a hardcoded 80 |
| `timetattle status` | 90 | every row of the source table wrapped |
| `timetattle metrics` | 84 | every row of the project table wrapped |
| `timetattle review -date … -print` | 126 | overran by 46 |

A wrapped table is not a table. Reproduce the figures with
`timetattle <command> | awk '{print length}' | sort -rn | head -1`, remembering
that `awk` counts *bytes*: the timeline's block glyph is three bytes and one
column, so a 60-cell timeline measures 180.

## A pipe has no width, and nothing is shortened into one

The primary reader of this CLI is an agent, and an agent pipes. Truncating what
a caller reads corrupts it silently, and `timetattle orient | grep` has to see
whole lines.

So the terminal test is `os.Stdout.Stat()` and `ModeCharDevice`, and anything
that is not a character device reports **zero** columns. Zero means "shorten
nothing": the clipping writer is not installed at all — not installed with an
unlimited budget, so the guarantee is structural rather than resting on a branch
being right — and every width-derived layout decision falls back to the width it
had before. Piped output is what it always was.

A real terminal that will not say how wide it is falls back to 80 rather than to
unlimited. A terminal is some finite width, and guessing 80 is wrong by a few
columns where guessing infinity is wrong by the whole design.

## A shortened line says so

This project has shipped a clipped view masquerading as a complete one three
times. `scripts/sleepcheck` reported "1.88h of gaps, largest 1h27m" over a
window that excluded a 6h23m gap ending 26 seconds before the window opened. A
report that quietly drops its right-hand column is the same mistake in a new
place.

Anything shortened therefore ends in `…`, and a list that dropped items counts
them: a block's sources render as `biome-appusage and 4 more`, never as
`biome-appusage, claude…`.

## What may be shortened, and in what order

**Never a number.** A count clipped to two digits is a wrong figure rather than
a short one, and the load-bearing columns sit at the right-hand end of exactly
the lines that overran — the sittings count in `metrics`, the last of the four
absence figures, the fifth count in `review -list`'s summary.

So a table gives up its room in a fixed order, and only reaches the last step
when the ones before it were not enough:

1. **The widths the table was written with**, whenever they fit. A pipe always
   stops here.
2. **Every column shrunk to its own widest cell**, and the gutters between them
   narrowed. This loses nothing at all.
3. **The trailing prose column**, where there is one — `status`'s capture
   outcome, `review`'s source list. Prose is the only thing on those rows that
   is not a measurement.
4. **The label**, and never below a floor. Four knowledgeC streams shortened to
   twelve columns are four identical rows, and a reader who cannot tell which
   one is broken is worse off than one whose terminal wrapped.

Prose that carries a figure **wraps** rather than shortening, because wrapping
loses nothing. Prose that must stay quotable does neither: `orient` still
hand-breaks the line naming `` `timetattle status` ``, because wrapping it at an
arbitrary margin splits the command across two lines, where neither half can be
pasted and no grep for it matches.

A word longer than the margin is left overlong rather than broken. That word is
always a path, and a path split across two lines cannot be copied into the
command it is an argument to; the clipping writer marks it instead.

## Per command

`orient` wraps its prose to the terminal instead of to a hardcoded 80.

`status` and `metrics` lay their tables out by the four steps above.

`review` draws its timeline as wide as there is room for beside the row labels.
`-width N` pins it and wins over the terminal; without the flag a pipe still
gets the 60 cells it always drew, because piped output may not depend on a
window nobody is looking at. Prompts wrap rather than shorten — the keys are the
whole content of the line, and one clipped at 80 columns hides `[s]` and `[q]`
altogether.

## Where the width comes from

`golang.org/x/sys/unix` and `TIOCGWINSZ`. It was already compiled into this
binary as an indirect dependency of `modernc.org/sqlite`, so using it directly
moved one line of `go.mod` and downloaded nothing. Whether the output is a
terminal at all is answered by the standard library, without it.
