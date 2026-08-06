package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
)

// write drops body at a fresh path inside t's own directory and returns it.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the config under test: %v", err)
	}
	return path
}

func TestAMissingConfigIsDefaultsRatherThanAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nothing-here.json")

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load on a missing file: %v", err)
	}
	if want := config.Default(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load on a missing file gave %+v, want the defaults %+v", *got, *want)
	}
}

func TestAnUnreadableConfigIsAnErrorRatherThanDefaults(t *testing.T) {
	// A directory where a file should be is not "no config"; it is a config
	// that cannot be read. Treating every failure as absence is how a
	// mis-typed path silently reverts every parameter to its default.
	dir := t.TempDir()

	if _, err := config.Load(dir); err == nil {
		t.Fatal("Load on a directory returned no error; an unreadable config must not read as an absent one")
	}
}

func TestMalformedJSONIsRefused(t *testing.T) {
	path := write(t, `{"sessions": {`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted truncated JSON")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file it could not read: %v", err)
	}
}

func TestAnUnknownKeyIsRefusedAndNamed(t *testing.T) {
	// The whole reason to refuse rather than ignore: "gap_threshold" is a
	// plausible mis-spelling of "gap", and ignoring it would leave the single
	// most consequential parameter in the system silently at its default while
	// the file on disk says otherwise.
	path := write(t, `{"sessions": {"gap_threshold": "45m"}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "gap_threshold") {
		t.Errorf("the error does not name the key it did not recognise: %v", err)
	}
}

func TestDurationsAreReadAsStrings(t *testing.T) {
	path := write(t, `{"sessions": {"gap": "45m", "lead_in": "90s", "tail_out": "1h30m"}}`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Sessions.Gap.Duration() != 45*time.Minute {
		t.Errorf("gap = %v, want 45m", got.Sessions.Gap.Duration())
	}
	if got.Sessions.LeadIn.Duration() != 90*time.Second {
		t.Errorf("lead_in = %v, want 90s", got.Sessions.LeadIn.Duration())
	}
	if got.Sessions.TailOut.Duration() != 90*time.Minute {
		t.Errorf("tail_out = %v, want 1h30m", got.Sessions.TailOut.Duration())
	}
}

func TestANumericDurationIsRefused(t *testing.T) {
	// 900 could be seconds, milliseconds or nanoseconds, and Go's own
	// time.Duration would read it as nanoseconds — 900ns, which is not what
	// anybody writing 900 into a config file means.
	path := write(t, `{"sessions": {"gap": 900}}`)

	if _, err := config.Load(path); err == nil {
		t.Fatal("Load accepted a bare number as a duration")
	}
}

func TestAnUnparseableDurationIsRefusedAndQuoted(t *testing.T) {
	path := write(t, `{"sessions": {"gap": "15 minutes"}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an unparseable duration")
	}
	if !strings.Contains(err.Error(), "15 minutes") {
		t.Errorf("the error does not quote the value it could not parse: %v", err)
	}
}

func TestUnsetFieldsKeepTheirDefaults(t *testing.T) {
	path := write(t, `{"sessions": {"gap": "45m"}}`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := config.Default()
	if got.Sessions.LeadIn != def.Sessions.LeadIn {
		t.Errorf("lead_in = %v, want the default %v", got.Sessions.LeadIn, def.Sessions.LeadIn)
	}
	if len(got.Sessions.IgnoreSources) != len(def.Sessions.IgnoreSources) {
		t.Errorf("ignore_sources = %v, want the default %v", got.Sessions.IgnoreSources, def.Sessions.IgnoreSources)
	}
}

func TestAnEmptyListOverridesTheDefaultList(t *testing.T) {
	// Distinguishing "said nothing" from "said none" is the same three-valued
	// discipline the event model runs on: an author who writes [] has decided
	// to ignore no sources, and silently restoring the default would overrule
	// them.
	path := write(t, `{"sessions": {"ignore_sources": []}}`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Sessions.IgnoreSources) != 0 {
		t.Errorf("ignore_sources = %v, want none", got.Sessions.IgnoreSources)
	}
}

func TestAFutureConfigVersionIsRefused(t *testing.T) {
	path := write(t, `{"config_version": 99}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a config version this build cannot understand")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the error does not name the version it found: %v", err)
	}
}

func TestValidateRefusesANonPositiveGap(t *testing.T) {
	// A gap of zero makes every observation its own session, which is a
	// defensible thing to ask for and never a thing anybody asks for by
	// writing "0s" in a file. Refusing it costs nothing and catches a typo.
	c := config.Default()
	c.Sessions.Gap = config.Duration(0)

	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a zero gap")
	}
}

func TestValidateRefusesNegativeMargins(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*config.Config)
	}{
		{"lead_in", func(c *config.Config) { c.Sessions.LeadIn = config.Duration(-time.Minute) }},
		{"tail_out", func(c *config.Config) { c.Sessions.TailOut = config.Duration(-time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			tc.set(c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate accepted a negative %s", tc.name)
			}
		})
	}
}

func TestTheDefaultsAreValid(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("the shipped defaults do not pass their own validation: %v", err)
	}
}

func TestTheDefaultLeadInAndTailOutAreNonZero(t *testing.T) {
	// Task J04gF criterion kRK: an isolated observation must produce a
	// non-zero session. With both margins at zero it cannot, whatever the
	// clustering does, so the defaults themselves have to satisfy it.
	def := config.Default()
	if def.Sessions.LeadIn.Duration() <= 0 || def.Sessions.TailOut.Duration() <= 0 {
		t.Fatalf("lead_in %v and tail_out %v: an isolated observation would score zero",
			def.Sessions.LeadIn.Duration(), def.Sessions.TailOut.Duration())
	}
}

func TestADurationRoundTripsThroughJSON(t *testing.T) {
	// Whatever Load reads must be what a writer would produce, or a config
	// printed for a human to copy is not the config the tool would read back.
	raw, err := json.Marshal(config.Duration(15 * time.Minute))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `"15m0s"` {
		t.Errorf("marshalled as %s, want a duration string", raw)
	}
	var back config.Duration
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if back.Duration() != 15*time.Minute {
		t.Errorf("round-tripped to %v, want 15m", back.Duration())
	}
}

func TestDefaultPathLivesUnderTimetattlesOwnDirectory(t *testing.T) {
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory on this machine: %v", err)
	}
	if want := filepath.Join(home, ".timetattle", "config.json"); path != want {
		t.Errorf("DefaultPath = %s, want %s", path, want)
	}
}

func TestLoadReportsNotExistOnlyThroughDefaults(t *testing.T) {
	// Guards the shape of the contract rather than the value: Load must never
	// hand back both defaults and a wrapped fs.ErrNotExist, because a caller
	// checking the error would then discard a perfectly good config.
	path := filepath.Join(t.TempDir(), "absent.json")
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned no config and no error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("Load returned defaults alongside a not-exist error; callers cannot tell what to do with that")
	}
}
