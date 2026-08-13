// Package config holds the parameters inference is fitted with.
//
// Timetattle's numbers are not read off a source; they are derived from one,
// and the derivation has knobs. The idle gap that separates one sitting from
// the next moves total engaged time by 2.3x across the plausible range —
// 530.4h at two minutes against 1199.9h at two hours, measured in
// project/private/2026-07-28-spike-findings.md. A parameter with that much leverage
// cannot live in a constant, because fitting it is the entire point of the
// calibration phase and a constant cannot be fitted.
//
// # Why JSON, and why in the home directory
//
// The file is ~/.timetattle/config.json, beside the event store, the registry
// index and the snapshots. It is JSON because encoding/json is in the standard
// library and this project has twice declined a TOML dependency for exactly
// this file (internal/filedays and internal/manifest both say so in their own
// doc comments). config.toml is named aspirationally in the v1 plan and in the
// README; this is that file, in the format the rest of ~/.timetattle already
// uses.
//
// Nothing here is secret in the cryptographic sense, but it is deliberately
// outside every repository: client contract values will live in this file
// (task mjalb), and a value inside a working tree is a value that gets
// committed by accident.
//
// # A missing file is defaults; a broken file is an error
//
// Those two cases are not the same and must never be collapsed. Nobody has to
// write a config to run Timetattle, so absence is ordinary and yields the
// defaults. But a file that exists and cannot be understood is a person having
// tried to say something, and quietly ignoring it means every number afterwards
// is computed from parameters the file on disk contradicts — with no symptom,
// for ever.
//
// That is why an unknown key is refused rather than skipped. "gap_threshold"
// is a plausible mis-spelling of "gap"; ignoring it would leave the most
// consequential parameter in the system at its default while the file says
// 45m. The cost is that a config written for a newer build fails on an older
// one, which config_version turns into a sentence rather than a puzzle.
//
// # Adding a section
//
// Config is a struct of named sections, and a new one is a new field. Client
// contract values arrived exactly that way and needed nothing here to change:
// they are the "engagements" section (engagements.go), split into clients and
// projects because this tool scopes a report to either one. That section is
// also why the durations are their own type rather than a bare int64 of
// seconds — the same discipline that refuses a bare 900 for a gap is what
// refuses a bare number for a contract's start date.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
)

// currentVersion is the config schema this build understands.
//
// It exists for the same reason internal/registry's index carries one: this
// file is read back by a different build than the one that wrote it, on the
// same machine, whenever somebody upgrades between editing it and running a
// command. A file from the future is refused by number rather than by an
// unknown-key error that would not say why.
const currentVersion = 1

// Config is everything Timetattle is configured with.
type Config struct {
	// ConfigVersion is the schema this file was written against. A file that
	// omits it is assumed to be current, which is what a hand-written one
	// looks like.
	ConfigVersion int `json:"config_version"`
	// Sessions parameterises clustering. See internal/sessions.
	Sessions Sessions `json:"sessions"`
	// Calendar says where a working day begins and where a week does. See
	// internal/sessions' calendar.go.
	Calendar Calendar `json:"calendar"`
	// ReferenceProjects maps a pseudonym used in test fixtures and
	// published project/ figures — "acorn" and siblings — to the real
	// absolute path that carries the data behind it on THIS machine. It
	// exists solely so //go:build realdata corpus checks can resolve a
	// pseudonym without the real name ever being written into the
	// repository; see reference.go and project/2026-08-02-confidentiality-
	// pass.md. Absent by default: a machine with no config maps nothing,
	// which ResolveReferenceProject distinguishes from every other failure.
	ReferenceProjects map[string]string `json:"reference_projects"`
	// Engagements is what is owed and by whom: per client and per project, an
	// hourly rate with a budget of hours per period, or a fixed contract total
	// with the hours it was priced against. THIS IS THE ONE SECTION THAT HOLDS
	// MONEY. It is absent by default and the tool works entirely without it;
	// see engagements.go for the shapes and why an entry may declare only one
	// of them.
	Engagements Engagements `json:"engagements"`
}

// Sessions parameterises how observations are clustered into sittings.
//
// EVERY VALUE HERE IS PROVISIONAL AND UNFITTED. They are starting points that
// let the tool run, not answers; see Default for what each one is resting on,
// which in most cases is nothing but symmetry and a round number.
type Sessions struct {
	// Gap is how long a silence has to be before it separates one sitting
	// from the next.
	Gap Duration `json:"gap"`
	// LeadIn is how long before its first observation a sitting is assumed
	// to have started. Work precedes the artifact it produces: a file is
	// saved after it has been edited, a commit is written after the code is.
	LeadIn Duration `json:"lead_in"`
	// TailOut is how long after its last observation a sitting is assumed to
	// have continued.
	TailOut Duration `json:"tail_out"`
	// PresenceSources name the sources read as presence rather than as
	// evidence of work. They bound sessions and never create them: a lit
	// screen is not work, and clustering it as if it were would make an
	// evening of reading indistinguishable from an evening of building.
	PresenceSources []string `json:"presence_sources"`
	// IgnoreSources name sources that contribute neither evidence nor
	// bounds. A source lands here when its meaning is unestablished, not
	// when it is uninteresting.
	IgnoreSources []string `json:"ignore_sources"`
	// Exclude names files that are machine activity rather than work, by
	// path. IgnoreSources drops a whole SOURCE; this drops individual
	// OBSERVATIONS out of a source that is otherwise good evidence.
	//
	// A pattern with no slash is a glob matched against the file's base name
	// ("*.log.bz2"); a pattern with a slash is an absolute path prefix
	// ("/Users/you/project/reports/"). See internal/sessions' exclude.go.
	//
	// IT SHIPS EMPTY AND MUST STAY THAT WAY. Whether a .log is noise is not
	// something the tool can know: on the machine this was written for, a
	// project called Easley syncs a server's access logs into its own working
	// tree, and 1,025 rotated .log.bz2 files were being counted as 135.2h of
	// work — 11.2% of all attributed time, on 513 days, with 0.0h
	// corroborated. On somebody else's machine the same files are a scraper's
	// deliverable. Shipping one person's noise as everybody's default is the
	// tool asserting a fact about a directory it has never seen.
	//
	// The exclusion applies where events are READ, not where they are
	// collected, so it is retroactive over everything already stored and is
	// undone by deleting the line. Nothing is ever deleted from the store.
	Exclude []string `json:"exclude"`
}

// Default is the configuration of a machine with no config file.
//
// Read the reasoning for each value as a placeholder rather than a finding.
//
//   - Gap 15m sits in the middle of the range the spike measured and is
//     otherwise arbitrary. At 15m the spike's transcript corpus totalled
//     780.5h; at 2m 530.4h and at 2h 1199.9h. Nothing has yet chosen among
//     them against ground truth, and this value must not be read as if
//     something had.
//
//   - LeadIn and TailOut 5m each exist so that an isolated observation scores
//     something rather than nothing — a single Motion save with no neighbours
//     otherwise has no span at all and a day of creative work disappears. They
//     are equal because nobody has measured them to differ, not because
//     anybody expects them to be the same.
//
//   - PresenceSources is knowledgec-backlit alone. It is the only stream this
//     project treats as observing presence: /device/isLocked's polarity is
//     inferred from the field's name and is documented as not yet safe to draw
//     presence from, and Biome's focus intervals record which application was
//     frontmost, which continues perfectly well with the screen off — the
//     2026-07-11 17:53 Motion autosave was written during a confirmed break.
//
//   - IgnoreSources holds knowledgec-islocked and knowledgec-mediausage, both
//     for reasons of unestablished meaning rather than low value, and both
//     with a measured cost of getting it wrong. A 'locked' interval in the
//     live store runs to 15h32m; clustered as evidence of work it would
//     manufacture a fifteen-hour sitting out of a night's sleep.
//     /app/mediaUsage is 88% Safari and documented as "nothing should be
//     inferred from it until somebody measures what it tracks", so inferring
//     from it here would be exactly that.
//
//   - Exclude is EMPTY, and that is a finding rather than an oversight. See
//     the field's own comment: the one rule that holds on every machine —
//     SQLite's -wal and -shm sidecars — is in code, in
//     fsobserve.IsDatabaseSidecar, precisely so that nothing personal has to
//     ship as a default here.
//
// knowledgec-appusage is deliberately NOT ignored, even though it is a
// cross-check rather than the attribution path: it observes application use,
// which is evidence of work, and sessions are wall-clock spans, so a source
// that agrees with Biome adds no time and one that reaches somewhere Biome
// missed adds real coverage.
func Default() *Config {
	return &Config{
		ConfigVersion: currentVersion,
		Sessions: Sessions{
			Gap:             Duration(15 * time.Minute),
			LeadIn:          Duration(5 * time.Minute),
			TailOut:         Duration(5 * time.Minute),
			PresenceSources: []string{"knowledgec-backlit"},
			IgnoreSources:   []string{"knowledgec-islocked", "knowledgec-mediausage"},
		},
		Calendar: defaultCalendar(),
	}
}

// DefaultPath is the one config file, beside the store it configures reads of.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".timetattle", "config.json"), nil
}

// Load reads path, filling in the defaults for everything it does not mention.
//
// A file that is not there is not an error: it is the ordinary state of a
// machine nobody has configured, and it yields Default. Every other failure —
// a directory where a file should be, a permission refusal, a truncated file,
// a key nothing recognises — is returned, because each of them is a person
// having tried to configure something and not succeeded.
//
// The returned config has been validated. A file that parses into nonsense
// fails here rather than at the first place the nonsense matters.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the config at %s: %w", path, err)
	}
	return parse(raw, path)
}

// parse is the half of Load that does not touch the filesystem: everything
// from the version check to Validate, over bytes that are already in hand.
//
// It is separate so that a WRITE can be checked against the same reader that
// will have to load it — see edit.go, which parses what it is about to write
// and refuses rather than leaving a config on disk that this build cannot read
// back. path is carried only to name the file in the errors.
func parse(raw []byte, path string) (*Config, error) {
	c := Default()

	// THE VERSION IS READ FIRST, and it has to be. config_version exists to
	// turn a file from the future into a sentence rather than a puzzle — and
	// every real file from the future ALSO carries the keys that version
	// added, so a strict decode run first would always fail on an unknown key
	// and the number nobody has to guess at would never be read at all. This
	// pass ignores unknown keys deliberately; the strict one below is what
	// refuses them.
	var declared struct {
		ConfigVersion int `json:"config_version"`
	}
	if err := json.Unmarshal(raw, &declared); err == nil && declared.ConfigVersion > currentVersion {
		return nil, fmt.Errorf("the config at %s is version %d and this build understands version %d; "+
			"it was written by a newer Timetattle", path, declared.ConfigVersion, currentVersion)
	}

	// Decoding INTO the defaults is what makes a partial file partial: a key
	// the file does not mention is left holding whatever Default put there.
	// It also means a JSON [] genuinely means "none", while an absent list
	// means "unsaid" — the same three-valued discipline the event model runs
	// on, in the one place a config file can express it.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		// An unknown key has two causes and only one of them is a typo. The
		// other is a config written for a build that is not installed yet,
		// which makes EVERY command fail — including the scheduled harvest,
		// and silent collection death has already cost this project 33 hours
		// once. A person reading "unknown field" alone has a spelling mistake
		// to hunt for and no reason whatever to suspect their binary. The
		// sentence is added only for that case: truncated JSON has nothing to
		// do with the build, and saying so would be a guess.
		if strings.Contains(err.Error(), "unknown field") {
			return nil, fmt.Errorf("read the config at %s: %w; that key is either mis-spelled or "+
				"belongs to a newer Timetattle than the one installed", path, err)
		}
		return nil, fmt.Errorf("read the config at %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("the config at %s: %w", path, err)
	}
	return c, nil
}

// Validate reports why a configuration cannot be used.
//
// Everything here is a value that would otherwise fail silently or absurdly
// rather than loudly. A zero gap makes every observation its own sitting; a
// negative margin makes a session end before it starts; a source named as both
// presence and ignored has no defined role, and picking one would be a coin
// toss made on the user's behalf.
func (c *Config) Validate() error {
	s := c.Sessions
	if s.Gap.Duration() <= 0 {
		return fmt.Errorf("sessions.gap is %v; it must be positive, or every observation becomes its own session",
			s.Gap.Duration())
	}
	if s.LeadIn.Duration() < 0 {
		return fmt.Errorf("sessions.lead_in is %v; it cannot be negative", s.LeadIn.Duration())
	}
	if s.TailOut.Duration() < 0 {
		return fmt.Errorf("sessions.tail_out is %v; it cannot be negative", s.TailOut.Duration())
	}
	for _, id := range s.PresenceSources {
		if slices.Contains(s.IgnoreSources, id) {
			return fmt.Errorf("source %q is named in both sessions.presence_sources and sessions.ignore_sources; "+
				"it cannot both bound sessions and be unread", id)
		}
	}
	// A pattern that can never match anything is refused where it was
	// written. The alternative is a line in the config doing nothing at all,
	// for ever, with no symptom — which is the failure this whole file's
	// unknown-key refusal exists to prevent, one level down.
	if _, err := sessions.NewExcludes(s.Exclude); err != nil {
		return fmt.Errorf("sessions.exclude: %w", err)
	}
	// The calendar is validated by building it, which loads the zone. A zone
	// name nothing resolves must fail here rather than at the first report
	// that needed to know where a day begins.
	if err := c.Calendar.validate(); err != nil {
		return err
	}
	// An empty pseudonym or empty path is not a mapping to anything, and a
	// relative path is relative to whatever directory a command happens to
	// be run from — exactly the machine-coupling-by-accident this field
	// exists to remove, reintroduced through the back door.
	for pseudonym, real := range c.ReferenceProjects {
		if pseudonym == "" {
			return fmt.Errorf("reference_projects has an entry with an empty pseudonym")
		}
		if real == "" {
			return fmt.Errorf("reference_projects[%q] is empty; it must name a path", pseudonym)
		}
		if !filepath.IsAbs(real) {
			return fmt.Errorf("reference_projects[%q] is %q, which is not an absolute path", pseudonym, real)
		}
	}
	// An engagement half-stated, or stated twice over, is money that cannot be
	// reported honestly. It fails here rather than at the one section that
	// would have had to guess — see engagements.go.
	return c.Engagements.validate()
}

// Duration is a time.Duration written the way a person writes one.
//
// JSON has no duration type, and the two obvious encodings are both traps. A
// bare number is ambiguous — 900 could be seconds or milliseconds, and Go's own
// time.Duration would read it as 900 nanoseconds, which is nobody's intent and
// fails without a symptom. A struct of {"minutes": 15} is unreadable at a
// glance and cannot express 90s. So: the string time.ParseDuration accepts,
// which is also what time.Duration prints, so a value read back is spelled the
// way it was written.
type Duration time.Duration

// Duration is the value as the standard library's type.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the value the way it is written in the file.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a string, so a config this package prints
// is a config it can read back.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON reads "15m", and refuses 900.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("%s is not a duration; write one as a quoted string such as \"15m\" or \"90s\"", raw)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("%q is not a duration; write one as \"15m\", \"90s\" or \"1h30m\"", text)
	}
	*d = Duration(parsed)
	return nil
}
