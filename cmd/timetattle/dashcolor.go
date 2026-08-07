package main

import (
	"fmt"
	"io"
	"strings"
)

// Color capability, detected ONCE per run and threaded through every section.
//
// dashboard is free to be visually ambitious in a way metrics never is (see
// the package doc in main.go and project/2026-08-07-cli-overhaul-plan.md,
// design principle 5), but every section paints through this one type rather
// than deciding for itself, for the same reason columnsOf is one function and
// not one per report: a section that detected its own capability could
// disagree with its neighbour about what the terminal can take, and a report
// half in escape codes and half not is worse than one that is plain
// throughout.
//
// # A pipe has no color, whatever the environment claims
//
// This is columnsOf's own rule, reapplied. dashboard is captured in tests and
// in scripts exactly as metrics is — see runMetrics and its siblings — and a
// capture with raw ESC sequences in it is corrupted for every reader that is
// not a terminal, not merely ugly for one that is. So TTY-ness gates color
// before the environment is even read, using columnsOf's own detection
// (Columns() > 0 for a fake, a real char device otherwise) rather than a new
// dependency: mattn/go-isatty is already in go.mod as an indirect dependency
// and promoting it to direct was one option, but columnsOf already answers
// exactly this question, tested, and answering it twice with two libraries is
// the two-overlapping-systems failure this project's own notes warn against
// elsewhere (see series.go's package doc). Reusing it here keeps this the ONE
// place TTY-ness is decided, and keeps go.mod exactly as it was.
type colorCapability int

const (
	// colorNone is plain text: no terminal, NO_COLOR, or a terminal that
	// declared neither COLORTERM nor a 256color TERM.
	colorNone colorCapability = iota
	// color256 approximates a requested colour to the nearest cell in the
	// ANSI 6x6x6 cube (indices 16-231) or the grayscale ramp (232-255).
	color256
	// colorTruecolor sends the requested 24-bit colour exactly.
	colorTruecolor
)

func (c colorCapability) String() string {
	switch c {
	case colorTruecolor:
		return "truecolor"
	case color256:
		return "256color"
	default:
		return "none"
	}
}

// RGB is one colour, eight bits per channel — enough for a truecolor escape
// and the input to the 256-color approximation.
type RGB struct{ R, G, B uint8 }

func (c RGB) String() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// detectColorCapability decides what out can take, from its own TTY-ness
// (via columnsOf's Columns()-int convention, so a test pins it exactly the
// way format_test.go's atWidth already does) and the environment lookup
// passed in — os.LookupEnv in production, a fixed map in a test, so the
// detection is deterministic and does not depend on who is running the suite
// or what their terminal happens to be.
//
// The order of the checks is fixed and each one short-circuits the rest:
//  1. Not a terminal at all → colorNone, before the environment is read.
//  2. NO_COLOR set (to anything, per the convention at no-color.org) →
//     colorNone, even if COLORTERM also claims truecolor — an explicit
//     refusal outranks a capability claim.
//  3. COLORTERM is truecolor or 24bit → colorTruecolor.
//  4. TERM contains "256color" → color256.
//  5. Otherwise → colorNone. A terminal that says nothing about its color
//     depth is not assumed to have any; guessing wrong here paints escape
//     codes as literal text on whatever the reader's real terminal is.
func detectColorCapability(out io.Writer, lookupEnv func(string) (string, bool)) colorCapability {
	if columnsOf(out) == 0 {
		return colorNone
	}
	if _, ok := lookupEnv("NO_COLOR"); ok {
		return colorNone
	}
	if v, ok := lookupEnv("COLORTERM"); ok && (v == "truecolor" || v == "24bit") {
		return colorTruecolor
	}
	if v, ok := lookupEnv("TERM"); ok && strings.Contains(v, "256color") {
		return color256
	}
	return colorNone
}

// Paint returns the escape sequence that sets the foreground colour to rgb
// under this capability, or "" when there is none — so a section writes
// cc.Paint(rgb)+text+cc.Reset() unconditionally and a plain terminal (or a
// pipe, or a test) gets the bare text back with no capability branch of its
// own written seven times over, once per section.
func (c colorCapability) Paint(rgb RGB) string {
	switch c {
	case colorTruecolor:
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb.R, rgb.G, rgb.B)
	case color256:
		return fmt.Sprintf("\x1b[38;5;%dm", ansi256(rgb))
	default:
		return ""
	}
}

// PaintBG is Paint for the background — the half-block glyph design principle
// 5 names (▀, foreground the upper cell and background the lower) needs both.
func (c colorCapability) PaintBG(rgb RGB) string {
	switch c {
	case colorTruecolor:
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", rgb.R, rgb.G, rgb.B)
	case color256:
		return fmt.Sprintf("\x1b[48;5;%dm", ansi256(rgb))
	default:
		return ""
	}
}

// Reset ends whatever Paint or PaintBG began, or "" under colorNone — the
// same unconditional-write rule as Paint, so a section never has to ask
// whether it painted anything before deciding whether to reset it.
func (c colorCapability) Reset() string {
	if c == colorNone {
		return ""
	}
	return "\x1b[0m"
}

// ansi256 maps rgb to the nearest cell in the standard 256-color palette's
// 6x6x6 cube, indices 16 to 231. Each channel is rounded to one of the cube's
// six levels (0, 95, 135, 175, 215, 255) by nearest level rather than a flat
// division by 51, which is what makes black (0,0,0) land exactly on index 16
// and white (255,255,255) exactly on 231 rather than drifting into the
// grayscale ramp — see TestColor256PaintsTheCubeApproximation.
//
// The grayscale ramp (232-255, a finer approximation for R=G=B) is not used
// here: a caller wanting true grays is a future refinement no section needs
// yet, and the cube alone already reproduces every color a ramp entry would
// only approximate more finely.
func ansi256(rgb RGB) int {
	return 16 + 36*cubeLevel(rgb.R) + 6*cubeLevel(rgb.G) + cubeLevel(rgb.B)
}

// cubeLevel rounds one 0-255 channel to its nearest of the cube's six steps
// and returns the step's INDEX (0-5), not its value.
func cubeLevel(v uint8) int {
	steps := [6]int{0, 95, 135, 175, 215, 255}
	best, bestDist := 0, 1<<30
	for i, s := range steps {
		d := int(v) - s
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}
