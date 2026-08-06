package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// plural writes a count with its noun, pluralising the noun when the count is
// not one.
//
// It lives in a file of its own rather than in any one subcommand because six
// files share it: harvest, ingest, chains, sessionize, status and the
// attribution pass. See the package doc in main.go for the rule.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// round trims a duration to the second, so a report reads 12m3s rather than
// 12m3.417291s. Nothing downstream reads these; they are for a person.
//
// Moved here from sessionize.go on its second caller, which is the rule the
// package doc in main.go states.
func round(d time.Duration) time.Duration { return d.Round(time.Second) }

// compactDuration writes a whole number of minutes or hours without the zero
// tail time.Duration prints: 5m rather than 5m0s, 1h rather than 1h0m0s.
//
// Moved here from metrics.go, where it was metricsCompact, on its second caller
// — the rule the package doc in main.go states. review prints the clustering
// parameters at the head of every day, and "gap 15m0s" beside "gap 15m" in
// metrics would be two spellings of one number.
func compactDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

// listOrNone renders a list of source IDs for a report, saying so out loud when
// there are none. An empty column would read as "unsaid" and this is "none".
func listOrNone(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}

// # Fitting a report to the terminal
//
// Every report below eighty columns wide reads as it was laid out; every report
// above it wraps, and a wrapped table is not a table. The four commands a person
// reads — orient, status, metrics, review — all overran an eighty-column
// terminal, review by 46 columns.
//
// Two rules govern the fix, and neither is negotiable.
//
// A PIPE HAS NO WIDTH. columnsOf returns 0 for anything that is not a terminal,
// and 0 means "shorten nothing". The primary reader of this CLI is an agent, and
// an agent pipes: truncating what a caller reads corrupts it silently, and
// `timetattle orient | grep` has to see whole lines. Every width-derived layout
// decision in this package falls back to what it printed before when the width
// is zero, so piped output is what it always was.
//
// A SHORTENED LINE SAYS SO. clip ends what it cut with an ellipsis. This project
// has shipped a clipped view masquerading as a complete one three times —
// scripts/sleepcheck reported "largest gap 1h27m" over a window that excluded a
// 6h23m one — and a report that silently drops its right-hand column is the same
// mistake in a new place. What may be dropped is prose, a name, or a list that
// counts what it left out; never a number. A count clipped to two digits is a
// wrong figure rather than a short one, so every table below shrinks its label
// column and its padding before any figure is at risk.

// columnsOf reports how many columns out has to lay a report out in, and 0 when
// out is not a terminal.
//
// The terminal test is os.Stat and ModeCharDevice from the standard library
// rather than a dependency. The width itself needs the TIOCGWINSZ ioctl, which
// golang.org/x/sys/unix wraps; that module is already compiled into this binary
// as an indirect dependency of modernc.org/sqlite, so using it directly moves
// one line of go.mod and downloads nothing.
//
// A terminal that will not say how wide it is falls back to eighty rather than
// to unlimited. A real terminal is some finite width, and guessing eighty is
// wrong by a few columns where guessing infinity is wrong by the whole design.
func columnsOf(out io.Writer) int {
	// A writer that knows its own width says so. Tests pin a width this way:
	// one that read the real terminal would pass in a wide window and fail in a
	// narrow one, and would pass under `go test` — where stdout is a pipe — for
	// entirely the wrong reason.
	if w, ok := out.(interface{ Columns() int }); ok {
		return w.Columns()
	}
	f, ok := out.(*os.File)
	if !ok {
		return 0
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}

// clip shortens text to width columns, marking what it removed with an
// ellipsis. A width of zero or less leaves the text exactly as it is.
//
// It counts RUNES rather than bytes: a project name is arbitrary text, and
// slicing one mid-rune produces a column that is the right length in bytes and
// the wrong length on the screen.
func clip(text string, width int) string {
	if width <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

// pad renders text in exactly width columns, shortening it when it is too long
// and padding it with spaces when it is too short, so that whatever comes after
// it starts in the same place on every row.
func pad(text string, width int) string {
	text = clip(text, width)
	return text + strings.Repeat(" ", max(0, width-len([]rune(text))))
}

// wrap writes text at indent, broken to fit width columns.
//
// It exists because every interesting sentence in these reports has a count
// interpolated into it, and a count's width is not known when the prose is
// written: 325 and 620,643 wrap differently, and a report hand-wrapped against
// one is over the margin against the other. That is not cosmetic — the reader
// these commands are designed for acts on the first screen, and a line that
// wraps in the terminal costs a line of that screen.
//
// A unit longer than the margin is emitted on its own overlong line rather than
// broken. That unit is always a path or a command, and neither can be copied
// into a shell once it has a line break through the middle of it. clipTo marks
// the overlong line instead.
//
// Width 0 is eighty, which is what every hand-wrapped paragraph in this package
// was written against and what a pipe therefore has to keep receiving.
//
// Moved here from orient.go, where it was orientWrap, on its second caller —
// the rule the package doc in main.go states. metrics and review both have
// sentences whose numbers sit at the right-hand end, where clipping would
// delete a figure and wrapping does not.
func wrap(out io.Writer, indent, text string, width int) {
	if width <= 0 {
		width = 80
	}
	line := indent
	for _, word := range wrapUnits(text) {
		switch {
		case line == indent:
			line += word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			fmt.Fprintln(out, line)
			line = indent + word
		}
	}
	if line != indent {
		fmt.Fprintln(out, line)
	}
}

// wrapUnits splits text into the things wrap may put a line break between.
//
// A run of words inside BACKTICKS is one unit. These reports name commands the
// reader is meant to run — "`timetattle orient` separates them" — and wrapping
// at eighty columns really did split that one across two lines on the live
// store, leaving neither half runnable and no grep for it matching. The same
// hazard broke a test: orient's own line naming `timetattle status` is asserted
// on by TestOrientDoesNotRestateStatusOrMetrics.
//
// Odd-versus-even backticks rather than a parser, deliberately. It keeps the
// trailing punctuation attached — "`timetattle status`'s" stays one unit — and
// an unclosed backtick simply runs to the end of the sentence, which is the
// safe direction: too much held together only ever costs a short line.
func wrapUnits(text string) []string {
	var out []string
	var held string
	for word := range strings.FieldsSeq(text) {
		if held != "" {
			held += " " + word
			if strings.Count(held, "`")%2 == 0 {
				out, held = append(out, held), ""
			}
			continue
		}
		if strings.Count(word, "`")%2 == 1 {
			held = word
			continue
		}
		out = append(out, word)
	}
	if held != "" {
		out = append(out, held)
	}
	return out
}

// table is one report table and the room it has.
//
// It lives here rather than in any one subcommand because three tables share
// it: status prints one per source and one per capture kind, and metrics prints
// one per project. Moved out on the second caller, which is the rule the
// package doc in main.go states.
type table struct {
	// indent is written before every row, gutter between two columns (two
	// spaces when it is empty), head names the columns (nil for a table with no
	// heading), and body is the rows.
	indent string
	gutter string
	head   []string
	body   [][]string
	// natural is the width each column was written with. colContent means "as
	// wide as its widest cell" and colFlexible means "whatever is left, and
	// never padded", which is what a trailing prose column wants.
	natural []int
	// right names the columns that are figures, which are right-aligned.
	right []bool
	// floor is how short the first column may get. Below it the rows stop
	// telling each other apart, which is worse than a line that wrapped.
	floor int
	// cols is the terminal's width, and 0 is a pipe: lay the table out exactly
	// as natural says and shorten nothing.
	cols int
}

const (
	colContent  = 0
	colFlexible = -1
)

// proseFloor is how much of a trailing explanation is worth keeping. Below it
// the column says nothing at all, and the label is the better use of the room.
const proseFloor = 12

// writeColumns lays a table out for the room there is.
//
// The order it gives up its slack in is the whole point. EVERY COLUMN BETWEEN
// THE FIRST AND A TRAILING PROSE ONE IS A FIGURE, and a count clipped to two
// digits is a wrong number rather than a short one — so:
//
//  1. the widths the table was written with, whenever they fit. A pipe has no
//     width and therefore always gets exactly these.
//  2. every column shrunk to its own widest cell, which loses nothing at all.
//  3. the trailing prose column, down to proseFloor.
//  4. the label, down to floor.
//
// Anything still over the margin is left to clipTo, which marks it.
func writeColumns(out io.Writer, t table) {
	if t.gutter == "" {
		t.gutter = "  "
	}
	n := len(t.natural)
	width := make([]int, n)
	content := func(i int) int {
		w := 0
		if t.head != nil {
			w = len([]rune(t.head[i]))
		}
		for _, r := range t.body {
			if i < len(r) {
				w = max(w, len([]rune(r[i])))
			}
		}
		return w
	}
	// natural is a MINIMUM, the way printf's %-14s is: a cell wider than the
	// column widens the column rather than losing a character. Without a
	// terminal that is the whole layout, so a pipe gets exactly what the format
	// strings these tables replaced produced, overhang and all.
	for i := range width {
		width[i] = max(t.natural[i], content(i))
	}
	total := func() int {
		w := len([]rune(t.indent)) + len([]rune(t.gutter))*(n-1)
		for _, c := range width {
			w += c
		}
		return w
	}
	if t.cols > 0 && total() > t.cols {
		for i := range width {
			width[i] = content(i)
		}
		over := max(0, total()-t.cols)
		if last := n - 1; t.natural[last] == colFlexible {
			give := min(over, max(0, width[last]-proseFloor))
			width[last], over = width[last]-give, over-give
		}
		width[0] = max(t.floor, width[0]-over)
	}

	if t.head != nil {
		writeRow(out, t, t.head, width)
	}
	for _, r := range t.body {
		writeRow(out, t, r, width)
	}
}

// writeRow writes one row. A row shorter than the heading stops after its last
// cell rather than padding on: metrics' sum row has no share and no sitting
// count, and trailing blanks under those headings would read as zeroes.
func writeRow(out io.Writer, t table, cells []string, width []int) {
	var line strings.Builder
	line.WriteString(t.indent)
	for i, cell := range cells {
		if i > 0 {
			line.WriteString(t.gutter)
		}
		switch {
		case t.right[i]:
			cell = clip(cell, width[i])
			line.WriteString(strings.Repeat(" ", max(0, width[i]-len([]rune(cell)))) + cell)
		case t.natural[i] == colFlexible:
			line.WriteString(clip(cell, width[i]))
		default:
			line.WriteString(pad(cell, width[i]))
		}
	}
	fmt.Fprintln(out, line.String())
}

// clipTo wraps out so that no line reaching it exceeds width columns, and
// returns out untouched when width is zero.
//
// Untouched rather than wrapped-with-no-limit, deliberately: the guarantee that
// piped output is unaltered is then structural, rather than resting on a branch
// inside the writer being right.
//
// It is a backstop, not the plan. Each report below lays itself out to fit
// first, because only the report knows which of its columns is a count and
// which is prose. This catches what no layout budgeted for — a project name
// nobody anticipated, an error message, a path — and marks it.
func clipTo(out io.Writer, width int) io.Writer {
	if width <= 0 {
		return out
	}
	return &clipper{out: out, width: width}
}

// clipper is the writer clipTo returns. It works a rune at a time and holds
// nothing back beyond one rune, because a prompt is written WITHOUT a trailing
// newline and has to reach the screen before the key it asks for is read — a
// writer that buffered whole lines would make an interactive session look hung.
//
// The one rune it does hold is the invariant a caller has to know about: a line
// written without a trailing newline must stay UNDER width columns, or its last
// character waits on the screen until something follows it. review's prompts are
// the only such lines in this package and reviewPrompt keeps them one column
// short for exactly this reason.
type clipper struct {
	out   io.Writer
	width int
	// col is how many columns of the current line have been emitted, dropping
	// is whether the rest of this line is being swallowed, and pending holds
	// the one rune of lookahead that tells a line of exactly width columns
	// (which fits, and must be printed whole) from a longer one (which does
	// not, and must end in an ellipsis).
	col      int
	dropping bool
	pending  []byte
	// carry is the front of a multi-byte rune that arrived split across two
	// Write calls. fmt writes whole formatted strings so this is rare, but a
	// rune cut in half here would reach the terminal as mojibake.
	carry []byte
}

func (c *clipper) Write(p []byte) (int, error) {
	n := len(p)
	if len(c.carry) > 0 {
		p = append(c.carry, p...)
		c.carry = nil
	}
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(p) {
			c.carry = append(c.carry, p...)
			break
		}
		if err := c.rune(p[:size], r); err != nil {
			return n, err
		}
		p = p[size:]
	}
	return n, nil
}

func (c *clipper) rune(b []byte, r rune) error {
	switch {
	case r == '\n' || r == '\r':
		if err := c.flush(); err != nil {
			return err
		}
		c.col, c.dropping = 0, false
		return c.emit(b)
	case c.dropping:
		return nil
	case c.col < c.width-1:
		c.col++
		return c.emit(b)
	case c.pending == nil:
		// This rune would land in the last column. Whether it belongs there
		// depends on whether anything follows it on this line, which is not
		// known yet.
		c.pending = append([]byte(nil), b...)
		return nil
	default:
		// Something followed, so the line is longer than the terminal and the
		// last column belongs to the mark that says so.
		c.pending, c.dropping = nil, true
		c.col++
		return c.emit([]byte("…"))
	}
}

// flush writes the held-back rune, which turns out to have been the last one on
// its line and so fits after all.
func (c *clipper) flush() error {
	if c.pending == nil {
		return nil
	}
	b := c.pending
	c.pending, c.col = nil, c.col+1
	return c.emit(b)
}

func (c *clipper) emit(b []byte) error {
	_, err := c.out.Write(b)
	return err
}
