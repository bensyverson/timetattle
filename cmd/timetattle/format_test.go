package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// atWidth pins how wide a report thinks its output is, so that a test asserts
// on a layout rather than on the window the suite happened to run in.
//
// A test that read the real terminal would pass in a maximised window and fail
// in a narrow one, and would pass under `go test` — where stdout is a pipe —
// for the wrong reason entirely.
type atWidth struct {
	io.Writer
	cols int
}

func (a atWidth) Columns() int { return a.cols }

// widest is the longest line in a report, counted in RUNES.
//
// Bytes would be wrong by a factor of three over the timeline: the block glyph
// is three bytes and one column, so a 60-cell timeline measures 180 bytes.
func widest(report string) (int, string) {
	var n int
	var worst string
	for line := range strings.SplitSeq(report, "\n") {
		if w := len([]rune(line)); w > n {
			n, worst = w, line
		}
	}
	return n, worst
}

// fits fails the test naming every line of report wider than cols.
func fits(t *testing.T, cols int, what, report string) {
	t.Helper()
	for line := range strings.SplitSeq(report, "\n") {
		if w := len([]rune(line)); w > cols {
			t.Errorf("%s emits a %d-column line into a %d-column terminal, so it wraps:\n%s",
				what, w, cols, line)
		}
	}
}

// A pipe has no width, and that MUST read as "do not shorten anything" rather
// than as some default. The primary reader of this CLI is an agent, and an
// agent pipes.
func TestOutputThatIsNotATerminalHasNoWidth(t *testing.T) {
	if got := columnsOf(&bytes.Buffer{}); got != 0 {
		t.Errorf("a buffer reports %d columns, want 0 — nothing may be truncated into a pipe", got)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	if got := columnsOf(w); got != 0 {
		t.Errorf("a pipe reports %d columns, want 0", got)
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if got := columnsOf(f); got != 0 {
		t.Errorf("a regular file reports %d columns, want 0", got)
	}
}

func TestClipMarksWhatItRemoved(t *testing.T) {
	for _, c := range []struct {
		name       string
		text       string
		cols, want int
		wantText   string
	}{
		{name: "shorter is left alone", text: "abc", cols: 10, wantText: "abc"},
		{name: "exactly the width is left alone", text: "abcde", cols: 5, wantText: "abcde"},
		{name: "longer says so", text: "abcdef", cols: 5, wantText: "abcd…"},
		{name: "no width means no clipping", text: "abcdef", cols: 0, wantText: "abcdef"},
		// Runes, not bytes: slicing a project name mid-rune produces a column
		// that is the right length in bytes and the wrong length on the screen.
		{name: "counts runes", text: "ααααββββ", cols: 5, wantText: "αααα…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := clip(c.text, c.cols); got != c.wantText {
				t.Errorf("clip(%q, %d) = %q, want %q", c.text, c.cols, got, c.wantText)
			}
		})
	}
}

// The clipping writer is the guarantee that nothing wraps. It has to hold
// across write boundaries, because a report is built out of many Fprintf calls
// and the writer sees whatever pieces they arrive in.
func TestTheClippingWriterShortensEveryLineAndSaysSo(t *testing.T) {
	var buf bytes.Buffer
	w := clipTo(&buf, 10)
	io.WriteString(w, "0123456789abcdef\nshort\n")
	want := "012345678…\nshort\n"
	if buf.String() != want {
		t.Errorf("clipped to\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestTheClippingWriterHoldsAcrossWriteBoundaries(t *testing.T) {
	var buf bytes.Buffer
	w := clipTo(&buf, 10)
	for _, piece := range []string{"01234", "56789ab", "cdef", "\n", "tail"} {
		n, err := io.WriteString(w, piece)
		if err != nil {
			t.Fatalf("write %q: %v", piece, err)
		}
		// fmt reports a short write as an error, so the writer must always
		// claim it consumed everything it swallowed.
		if n != len(piece) {
			t.Errorf("writing %q reported %d of %d bytes consumed", piece, n, len(piece))
		}
	}
	want := "012345678…\ntail"
	if buf.String() != want {
		t.Errorf("clipped to\n%q\nwant\n%q", buf.String(), want)
	}
}

// A prompt is written without a trailing newline and has to appear before the
// reader is asked for a key. A writer that buffered whole lines would swallow
// it and the session would look hung.
func TestTheClippingWriterDoesNotHoldBackAPromptThatFits(t *testing.T) {
	var buf bytes.Buffer
	w := clipTo(&buf, 20)
	io.WriteString(w, "\n[Enter] right  > ")
	if got := buf.String(); got != "\n[Enter] right  > " {
		t.Errorf("the prompt came out as %q; a prompt must be written before the key is read", got)
	}
}

// A multibyte rune split across two writes must not be cut in half.
func TestTheClippingWriterDoesNotSplitARune(t *testing.T) {
	var buf bytes.Buffer
	w := clipTo(&buf, 10)
	glyph := []byte("░")
	io.WriteString(w, "abc")
	w.Write(glyph[:1])
	w.Write(glyph[1:])
	io.WriteString(w, "def\n")
	if got := buf.String(); got != "abc░def\n" {
		t.Errorf("a rune split across writes came out as %q, want %q", got, "abc░def\n")
	}
}

// Zero columns is a pipe, and a pipe gets every byte.
func TestTheClippingWriterIsNotInstalledWithoutAWidth(t *testing.T) {
	var buf bytes.Buffer
	w := clipTo(&buf, 0)
	if w != io.Writer(&buf) {
		t.Errorf("clipTo wrapped a widthless writer; piped output must go through untouched")
	}
}

func TestWrapBreaksProseAtTheWidthItIsGiven(t *testing.T) {
	var buf bytes.Buffer
	wrap(&buf, "  ", "one two three four five six seven eight nine ten", 20)
	fits(t, 20, "wrap", buf.String())
	if !strings.Contains(buf.String(), "one two three four") {
		t.Errorf("wrap did not fill its lines:\n%s", buf.String())
	}
	// No width is 80, which is what every hand-wrapped paragraph in this
	// package was written against and what a pipe must keep getting.
	var wide bytes.Buffer
	wrap(&wide, "", strings.Repeat("word ", 40), 0)
	if n, _ := widest(wide.String()); n > 80 || n < 70 {
		t.Errorf("wrap with no width made %d-column lines, want 80", n)
	}
}

// A word longer than the margin is left overlong rather than broken, because
// that word is always a path and a broken path cannot be pasted into the
// command it is an argument to. The clipping writer marks it instead.
func TestWrapNeverBreaksAWord(t *testing.T) {
	var buf bytes.Buffer
	wrap(&buf, "", "see /a/very/long/path/that/will/not/fit/anywhere/at/all now", 20)
	if !strings.Contains(buf.String(), "/a/very/long/path/that/will/not/fit/anywhere/at/all") {
		t.Errorf("wrap broke a path:\n%s", buf.String())
	}
}

// A COMMAND IN BACKTICKS IS ONE UNIT. Wrapping at eighty columns really did
// split "`timetattle orient` separates them" across two lines on the live
// store, leaving neither half runnable and no grep for it matching.
func TestWrapNeverBreaksACommandItIsTellingYouToRun(t *testing.T) {
	for _, c := range []struct {
		text, whole string
		cols        int
	}{
		{text: "which this command reads neither of. `timetattle orient` separates them.",
			whole: "`timetattle orient`", cols: 40},
		// The trailing possessive stays attached, so the phrase reads whole.
		{text: "whether that is a problem is `timetattle status`'s question, and this " +
			"command does not answer it", whole: "`timetattle status`'s", cols: 60},
	} {
		var buf bytes.Buffer
		wrap(&buf, "", c.text, c.cols)
		var found bool
		for line := range strings.SplitSeq(buf.String(), "\n") {
			found = found || strings.Contains(line, c.whole)
		}
		if !found {
			t.Errorf("wrap at %d columns split %s across lines:\n%s", c.cols, c.whole, buf.String())
		}
	}
}
