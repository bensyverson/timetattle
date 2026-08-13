package config_test

import (
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/config"
)

// TestExcludeShipsEmpty. The tool cannot know that a .log is noise: Ben's are
// noise because Easley is a server he runs, and another user's could be the
// deliverable from a scraper or a test corpus. Shipping his personal noise as a
// default would be the tool deciding something it has no way to know.
func TestExcludeShipsEmpty(t *testing.T) {
	if got := config.Default().Sessions.Exclude; len(got) != 0 {
		t.Errorf("sessions.exclude ships as %q; it must ship empty", got)
	}
}

// TestExcludeIsReadFromTheFile.
func TestExcludeIsReadFromTheFile(t *testing.T) {
	path := write(t, `{"sessions": {"exclude": ["*.log.bz2", "/Users/you/git/Easley/reports/"]}}`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"*.log.bz2", "/Users/you/git/Easley/reports/"}
	if len(got.Sessions.Exclude) != len(want) {
		t.Fatalf("read %q, want %q", got.Sessions.Exclude, want)
	}
	for i := range want {
		if got.Sessions.Exclude[i] != want[i] {
			t.Errorf("pattern %d is %q, want %q", i, got.Sessions.Exclude[i], want[i])
		}
	}
}

// TestAnUnusablePatternIsRefusedWhereItWasWritten. A relative path can never
// match a locator, which is always absolute, so accepting it would leave a
// person's configuration doing nothing at all with no symptom.
func TestAnUnusablePatternIsRefusedWhereItWasWritten(t *testing.T) {
	path := write(t, `{"sessions": {"exclude": ["logs/"]}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a relative exclude pattern; it can never match anything")
	}
	if !strings.Contains(err.Error(), "logs/") {
		t.Errorf("the error does not name the pattern it refused: %v", err)
	}
}

// TestAnUnknownKeyPointsAtTheBuild is the hazard this field creates.
//
// config.Load refuses an unknown key, which is right, and a config naming a
// field a newer build added therefore makes EVERY command fail — including the
// scheduled launchd harvest, which is how this project lost 33 hours of
// collection on 2026-07-30. The error must say that an old binary is a possible
// cause, or the person reading it has a mis-spelling to hunt for and no reason
// to suspect their install.
func TestAnUnknownKeyPointsAtTheBuild(t *testing.T) {
	path := write(t, `{"sessions": {"a_field_from_the_future": true}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "a_field_from_the_future") {
		t.Errorf("the error does not name the key: %v", err)
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("the error does not offer an out-of-date binary as a cause, which is the one "+
			"cause a person cannot guess from the key alone: %v", err)
	}
}

// TestAFutureVersionIsRefusedByNumberEvenWhenItAlsoHoldsAnUnknownKey.
//
// config_version exists to turn a file from the future into a sentence rather
// than a puzzle, and every real file from the future ALSO carries the keys that
// version added. Checking the version after the strict decode means the version
// check can never fire on the case it was written for: the unknown key wins and
// the number nobody has to guess at goes unread.
func TestAFutureVersionIsRefusedByNumberEvenWhenItAlsoHoldsAnUnknownKey(t *testing.T) {
	path := write(t, `{"config_version": 99, "sessions": {"a_field_from_the_future": true}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a config from a version this build does not understand")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the error does not report the version it read, so config_version did nothing: %v", err)
	}
}
