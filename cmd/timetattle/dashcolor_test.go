package main

import (
	"bytes"
	"testing"
)

// noEnv looks up nothing, ever — the zero-configuration terminal.
func noEnv(string) (string, bool) { return "", false }

// envOf builds a lookup function from a fixed map, for tests that need one or
// two variables set without reaching into the real process environment. A
// real environment would make this test's answer depend on who is running it.
func envOf(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

// A PIPE HAS NO COLOR, whatever the environment claims. dashboard is a human
// command, but it is captured in tests and in scripts exactly as metrics is,
// and a capture with raw escape sequences in it is corrupted for every reader
// that is not a terminal. atWidth{cols: 0} is format_test.go's own stand-in
// for "not a terminal" — the same fake columnsOf's own tests already rely on.
func TestColorCapabilityNoneWhenNotATerminal(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 0}
	env := envOf(map[string]string{"COLORTERM": "truecolor"})
	if got := detectColorCapability(out, env); got != colorNone {
		t.Errorf("a non-terminal writer with COLORTERM=truecolor detected %v, want colorNone", got)
	}
}

func TestColorCapabilityNoneWhenNoColorIsSet(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 80}
	env := envOf(map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"})
	if got := detectColorCapability(out, env); got != colorNone {
		t.Errorf("NO_COLOR set alongside COLORTERM=truecolor detected %v, want colorNone", got)
	}
}

func TestColorCapabilityTruecolorFromColorterm(t *testing.T) {
	for _, value := range []string{"truecolor", "24bit"} {
		out := atWidth{&bytes.Buffer{}, 80}
		env := envOf(map[string]string{"COLORTERM": value})
		if got := detectColorCapability(out, env); got != colorTruecolor {
			t.Errorf("COLORTERM=%s detected %v, want colorTruecolor", value, got)
		}
	}
}

func TestColorCapability256FromTerm(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 80}
	env := envOf(map[string]string{"TERM": "xterm-256color"})
	if got := detectColorCapability(out, env); got != color256 {
		t.Errorf("TERM=xterm-256color detected %v, want color256", got)
	}
}

func TestColorCapabilityNoneWithoutEitherSignal(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 80}
	env := envOf(map[string]string{"TERM": "xterm"})
	if got := detectColorCapability(out, env); got != colorNone {
		t.Errorf("plain TERM=xterm detected %v, want colorNone", got)
	}
}

func TestColorCapabilityTruecolorTakesPriorityOver256(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 80}
	env := envOf(map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"})
	if got := detectColorCapability(out, env); got != colorTruecolor {
		t.Errorf("COLORTERM=truecolor and TERM=xterm-256color together detected %v, want colorTruecolor",
			got)
	}
}

func TestColorCapabilityNoneWhenNothingIsSet(t *testing.T) {
	out := atWidth{&bytes.Buffer{}, 80}
	if got := detectColorCapability(out, noEnv); got != colorNone {
		t.Errorf("a terminal with no color signal at all detected %v, want colorNone", got)
	}
}

// A paint call under colorNone is always the empty string, so a section can
// write paint(rgb)+text+reset() unconditionally and a non-color terminal (or
// a pipe, or a test) gets plain text back with no branch of its own.
func TestColorNonePaintsNothing(t *testing.T) {
	red := RGB{255, 0, 0}
	if got := colorNone.Paint(red); got != "" {
		t.Errorf("colorNone.Paint(%v) = %q, want empty", red, got)
	}
	if got := colorNone.PaintBG(red); got != "" {
		t.Errorf("colorNone.PaintBG(%v) = %q, want empty", red, got)
	}
	if got := colorNone.Reset(); got != "" {
		t.Errorf("colorNone.Reset() = %q, want empty", got)
	}
}

func TestColorTruecolorPaintsA24BitEscape(t *testing.T) {
	got := colorTruecolor.Paint(RGB{255, 128, 0})
	want := "\x1b[38;2;255;128;0m"
	if got != want {
		t.Errorf("colorTruecolor.Paint(255,128,0) = %q, want %q", got, want)
	}
	got = colorTruecolor.PaintBG(RGB{0, 128, 255})
	want = "\x1b[48;2;0;128;255m"
	if got != want {
		t.Errorf("colorTruecolor.PaintBG(0,128,255) = %q, want %q", got, want)
	}
}

func TestColorResetIsTheSameForAnyNonNoneCapability(t *testing.T) {
	for _, c := range []colorCapability{color256, colorTruecolor} {
		if got := c.Reset(); got != "\x1b[0m" {
			t.Errorf("%v.Reset() = %q, want \\x1b[0m", c, got)
		}
	}
}

// 256-color is the ANSI 6x6x6 cube (indices 16-231): pure red, green and blue
// land on the cube's own corners, which are the values every terminal's
// palette agrees on regardless of its particular 256-color theme.
func TestColor256PaintsTheCubeApproximation(t *testing.T) {
	for _, c := range []struct {
		rgb  RGB
		want string
	}{
		{RGB{255, 0, 0}, "\x1b[38;5;196m"},
		{RGB{0, 255, 0}, "\x1b[38;5;46m"},
		{RGB{0, 0, 255}, "\x1b[38;5;21m"},
		{RGB{0, 0, 0}, "\x1b[38;5;16m"},
		{RGB{255, 255, 255}, "\x1b[38;5;231m"},
	} {
		if got := color256.Paint(c.rgb); got != c.want {
			t.Errorf("color256.Paint(%v) = %q, want %q", c.rgb, got, c.want)
		}
	}
}
