// Package manifest loads declarative descriptions of evidence sources.
//
// A large class of evidence is simply timestamped files on disk: Motion's
// autosave vault, Blender's .blend1 backups, Photoshop's AutoRecover folder.
// Those differ only in where they live and how the time is encoded, so
// describing them in data rather than code means adding a niche application
// needs no Go at all — which is the extension story for contributors and for
// us.
//
// # Why JSON, and what it costs
//
// JSON is in the standard library and adding a source should not cost a
// dependency. The price is that manifests cannot carry comments, so every
// source takes an optional "description" field to say in prose what a comment
// would have said.
//
// # Versioning is not optional here
//
// Everywhere else Timetattle ignores backward compatibility, because it is
// pre-release and controls every caller. Manifests are the exception: they
// live outside this repository on other people's disks, and we cannot migrate
// what we cannot see. manifest_version costs one field now and cannot be
// added retroactively, so it is required from the first release and an
// unrecognised value is refused rather than parsed hopefully.
//
// # Recursive globs are refused
//
// A manifest saying root "~" with match "**/*.blend1" walks an entire volume.
// That mistake is easy to make and produces a harvester that appears to hang.
// Rather than guess at a safe depth, "**" is rejected outright and broad
// searches are routed through Spotlight, which is indexed and bounded.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bensyverson/timetattle/internal/source"
)

// Version is the manifest schema this build understands.
const Version = 1

// Discovery is how a source finds candidate files.
type Discovery string

const (
	// DiscoveryGlob walks a bounded directory tree. The default.
	DiscoveryGlob Discovery = "glob"
	// DiscoverySpotlight queries the macOS metadata index, which is the
	// supported way to search broadly without walking a volume.
	DiscoverySpotlight Discovery = "spotlight"
)

// TimestampFrom names where an event's time is read from.
type TimestampFrom string

const (
	// FromFilename extracts the time from the name, as Motion encodes it.
	FromFilename TimestampFrom = "filename"
	// FromMtime uses the modification time, for formats like .blend1 that
	// carry no time in the name.
	FromMtime TimestampFrom = "mtime"
	// FromBirthtime uses the creation time.
	FromBirthtime TimestampFrom = "birthtime"
)

// Manifest is one loaded manifest file.
type Manifest struct {
	// Version is the schema version the file declares.
	Version int `json:"manifest_version"`
	// Sources are the sources that validated. Invalid entries are absent
	// and reported through the error Load returns.
	Sources []Source `json:"sources"`
	// Path is where this manifest was read from.
	Path string `json:"-"`
}

// Source describes one declarative evidence source.
type Source struct {
	// ID is the stable identifier events are attributed to.
	ID string `json:"id"`
	// Type is how the source is read. Currently only timestamped-files.
	Type source.Kind `json:"type"`
	// Emits is what the events mean, independent of Type.
	Emits source.Shape `json:"emits"`
	// Cause is what writes these files, and it is required. An artifact a
	// person caused is evidence they were present; one a timer caused is
	// evidence only that the application was running, and no default can be
	// chosen without asserting one of those on the author's behalf.
	Cause source.Cause `json:"cause"`
	// App is the bundle identifier this evidence belongs to. It is the key
	// that joins file evidence to focus intervals, so it is not decoration.
	App string `json:"app"`
	// Perishable marks a source whose record only ever shrinks, and which
	// therefore needs preserving on a schedule rather than merely reading at
	// the end. There are two ways that happens and both count: the
	// application prunes the files, as Motion does to its autosave vault; or
	// the evidence is destroyed in place, as an mtime is when a later save
	// moves a .blend1 off the day it used to attest to.
	Perishable bool `json:"perishable"`
	// Description stands in for the comments JSON cannot carry.
	Description string `json:"description"`
	// Discovery is how candidate files are found. Defaults to glob.
	Discovery Discovery `json:"discovery"`
	// Files says where to look.
	Files Files `json:"files"`
	// Timestamp says how to read the time.
	Timestamp Timestamp `json:"timestamp"`
	// Project optionally extracts a name from the path that corroborates the
	// document derived from the filename. It no longer sets an event's
	// Identity.Project directly: for Motion this is the composition folder,
	// which is a document identity, not the client project a resolution pass
	// attributes the evidence to later.
	Project *Extract `json:"project"`
	// Document optionally overrides the default document-identity rule
	// (the filename with its timestamp match and extension removed) for a
	// source whose variable part is not a timestamp, such as a counter or a
	// UUID.
	Document *Extract `json:"document"`
}

// Files locates candidate files, either by bounded glob or by Spotlight.
type Files struct {
	// Root is the directory to search. A leading ~ is expanded at load.
	Root string `json:"root"`
	// Match is a glob relative to Root. "**" is refused; use Spotlight.
	Match string `json:"match"`
	// Query is a Spotlight query, used when Discovery is spotlight.
	Query string `json:"query"`
}

// Timestamp says where an event's time comes from.
type Timestamp struct {
	// From selects the strategy.
	From TimestampFrom `json:"from"`
	// Pattern captures the timestamp text, when From is filename.
	Pattern string `json:"pattern"`
	// Layout parses the captured text, in Go reference-time form.
	Layout string `json:"layout"`
	// Compiled is Pattern, compiled at load so a bad regexp is caught then
	// rather than producing zero events at harvest time.
	Compiled *regexp.Regexp `json:"-"`
}

// Extract pulls a name out of a path.
type Extract struct {
	// From is the part of the record to read; currently only "path".
	From string `json:"from"`
	// Pattern must capture exactly one group.
	Pattern string `json:"pattern"`
	// Compiled is Pattern, compiled at load.
	Compiled *regexp.Regexp `json:"-"`
}

// Load reads and validates a manifest.
//
// A source that fails validation is left out and described in the returned
// error, while the sources that validated are still returned. Manifests are
// hand-edited, and a typo in the fifth entry should not silently disable the
// other four. A file that cannot be read or parsed at all, or that declares a
// version we do not understand, yields no manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return parse(path, data)
}

// parse validates already-read manifest bytes. It exists so the manifests
// embedded in the binary go through exactly the same checks as the ones on a
// user's disk: a built-in that only works because nothing validated it would
// be the worst possible example to ship.
//
// path names the manifest in error messages and is not read from.
func parse(path string, data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	m.Path = path

	if m.Version == 0 {
		return nil, fmt.Errorf("manifest %s: manifest_version is required; add \"manifest_version\": %d", path, Version)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("manifest %s declares manifest_version %d, but this build understands %d",
			path, m.Version, Version)
	}

	var (
		valid []Source
		errs  []error
		seen  = make(map[string]bool)
	)
	for i, s := range m.Sources {
		if err := s.normalise(); err != nil {
			errs = append(errs, fmt.Errorf("manifest %s: source %s: %w", path, describe(s, i), err))
			continue
		}
		if seen[s.ID] {
			errs = append(errs, fmt.Errorf("manifest %s: source %q appears more than once", path, s.ID))
			continue
		}
		seen[s.ID] = true
		valid = append(valid, s)
	}
	m.Sources = valid
	return &m, errors.Join(errs...)
}

// describe names a source in an error. An entry that failed because it has no
// ID cannot be named by one, so fall back to its position.
func describe(s Source, i int) string {
	if s.ID != "" {
		return fmt.Sprintf("%q", s.ID)
	}
	return fmt.Sprintf("at position %d", i+1)
}

// normalise validates a source and fills in defaults, expanding paths and
// compiling patterns so that every failure surfaces at load rather than as a
// silent absence of events later.
func (s *Source) normalise() error {
	if s.ID == "" {
		return errors.New("has no id; every source needs one to attribute its events")
	}
	if s.Type != source.KindTimestampedFiles {
		return fmt.Errorf("has unknown type %q; supported types are %q",
			s.Type, source.KindTimestampedFiles)
	}
	switch s.Emits {
	case source.Point, source.Interval:
	default:
		return fmt.Errorf("has unknown emits %q; supported values are %q and %q",
			s.Emits, source.Point, source.Interval)
	}
	// A timestamped-files source reads file METADATA — a name, an mtime, a
	// birth time — and that carries exactly one instant per file. The only
	// way to get a span out of metadata alone is to pair consecutive files,
	// and that is inference: fitting the gap a "session" tolerates, which
	// 1PyhK forbids any source from doing, because that threshold has to be
	// calibrated globally rather than chosen per source.
	//
	// This does not close the door on a file genuinely carrying an interval
	// — a log with start and end timestamps in its contents, a project file
	// with an embedded session record. That is recoverable in principle, but
	// not declaratively: it needs a reader that parses the file's contents
	// in a format-specific way, which makes it a Go source.Source with its
	// own Kind, not a manifest entry. It would never reach this branch.
	//
	// The check stays inside the timestamped-files case rather than becoming
	// a blanket rule for the same reason: a future sqlite or command source
	// reads something other than bare file metadata and could legitimately
	// emit intervals.
	if s.Type == source.KindTimestampedFiles && s.Emits != source.Point {
		return fmt.Errorf("emits %q, but a timestamped-files source only reads a file's metadata "+
			"(name, mtime or birth time), which carries exactly one instant per file — describing a span "+
			"would mean pairing consecutive files, which is inference and belongs in the inference layer, "+
			"not in a source. If this evidence genuinely has a duration recorded inside the file itself, "+
			"that has to be read by a Go source.Source with its own format-specific reader, not declared "+
			"in a manifest; only %q is supported here",
			s.Emits, source.Point)
	}

	// Required, and with no default, unlike Discovery below. Discovery has an
	// obvious safe answer — a bounded glob — and getting it wrong shows up as an
	// error. A cause has no safe answer: guessing "timer" silently discards a
	// presence signal that was really there, and guessing "user-action" credits
	// work to time nobody was at the machine, which is precisely what the
	// 2026-07-11 17:53 autosave would have done. Cause is also unrecoverable
	// afterwards: the mtime of a .blend1 and of an autosave are the same fact,
	// and a perishable source's files may be gone before anyone notices.
	//
	// The check is unconditional rather than per-type because every source this
	// schema can describe today produces artifacts. When a declarative kind
	// arrives whose events are not artifacts, this moves into the per-type
	// branch, the way the point-only rule above already sits in one.
	if s.Cause == "" {
		return fmt.Errorf("sets no cause; say what writes these files: %q for something a person "+
			"did in the application, %q for a periodic autosave, %q for crash recovery. There is no "+
			"default, because an artifact a person caused is evidence they were at the machine and one "+
			"a timer caused is not",
			source.CauseUserAction, source.CauseTimer, source.CauseRecovery)
	}
	if err := s.Cause.Validate(); err != nil {
		return err
	}

	if s.Discovery == "" {
		s.Discovery = DiscoveryGlob
	}
	switch s.Discovery {
	case DiscoveryGlob:
		if err := s.normaliseGlob(); err != nil {
			return err
		}
	case DiscoverySpotlight:
		if s.Files.Query == "" {
			return errors.New("uses spotlight discovery but sets no files.query")
		}
		// The root is optional here, but when set it reaches `mdfind
		// -onlyin`. Left unexpanded it searches a directory literally named
		// "~", reports nothing, and never explains why.
		if s.Files.Root != "" {
			root, err := expand(s.Files.Root)
			if err != nil {
				return err
			}
			s.Files.Root = root
		}
	default:
		return fmt.Errorf("has unknown discovery %q; supported values are %q and %q",
			s.Discovery, DiscoveryGlob, DiscoverySpotlight)
	}

	if err := s.normaliseTimestamp(); err != nil {
		return err
	}
	if s.Project != nil {
		if s.Project.From != "path" {
			return fmt.Errorf("project.from is %q; only \"path\" is supported", s.Project.From)
		}
		compiled, err := regexp.Compile(s.Project.Pattern)
		if err != nil {
			return fmt.Errorf("project.pattern is not a valid regexp: %w", err)
		}
		if compiled.NumSubexp() != 1 {
			return fmt.Errorf("project.pattern must capture exactly one group, it captures %d", compiled.NumSubexp())
		}
		s.Project.Compiled = compiled
	}
	if s.Document != nil {
		if s.Document.From != "path" {
			return fmt.Errorf("document.from is %q; only \"path\" is supported", s.Document.From)
		}
		compiled, err := regexp.Compile(s.Document.Pattern)
		if err != nil {
			return fmt.Errorf("document.pattern is not a valid regexp: %w", err)
		}
		if compiled.NumSubexp() != 1 {
			return fmt.Errorf("document.pattern must capture exactly one group, it captures %d", compiled.NumSubexp())
		}
		s.Document.Compiled = compiled
	}
	return nil
}

func (s *Source) normaliseGlob() error {
	if s.Files.Root == "" {
		return errors.New("sets no files.root")
	}
	if s.Files.Match == "" {
		return errors.New("sets no files.match")
	}
	// Refusing "**" is not pedantry. From a shallow root it walks the whole
	// volume, and the resulting harvester looks hung rather than broken.
	if strings.Contains(s.Files.Match, "**") {
		return fmt.Errorf("files.match %q uses a recursive glob, which can walk an entire volume; "+
			"use \"discovery\": \"spotlight\" with a files.query instead", s.Files.Match)
	}
	root, err := expand(s.Files.Root)
	if err != nil {
		return err
	}
	s.Files.Root = root
	return nil
}

func (s *Source) normaliseTimestamp() error {
	switch s.Timestamp.From {
	case FromFilename:
		if s.Timestamp.Pattern == "" {
			return errors.New("timestamp.from is \"filename\" but no timestamp.pattern is set")
		}
		if s.Timestamp.Layout == "" {
			return errors.New("timestamp.from is \"filename\" but no timestamp.layout is set")
		}
		compiled, err := regexp.Compile(s.Timestamp.Pattern)
		if err != nil {
			return fmt.Errorf("timestamp.pattern is not a valid regexp: %w", err)
		}
		if compiled.NumSubexp() != 1 {
			return fmt.Errorf("timestamp.pattern must capture exactly one group, it captures %d", compiled.NumSubexp())
		}
		s.Timestamp.Compiled = compiled
	case FromMtime, FromBirthtime:
		if s.Timestamp.Pattern != "" {
			return fmt.Errorf("timestamp.from is %q, which reads the time from the filesystem, "+
				"but a timestamp.pattern is also set", s.Timestamp.From)
		}
	case "":
		return errors.New("sets no timestamp.from")
	default:
		return fmt.Errorf("has unknown timestamp.from %q; supported values are %q, %q and %q",
			s.Timestamp.From, FromFilename, FromMtime, FromBirthtime)
	}
	return nil
}

// expand resolves a leading ~ so manifests are shareable rather than
// hardcoding whoever wrote them.
func expand(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	if !strings.HasPrefix(path, "~/") {
		// ~otheruser is not something we can resolve portably, and
		// silently treating it as a relative path would be worse.
		return "", fmt.Errorf("cannot expand %q; only ~/ is supported", path)
	}
	return filepath.Join(home, path[2:]), nil
}
