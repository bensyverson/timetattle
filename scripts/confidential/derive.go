package main

// WHERE THE LIST COMES FROM, AND WHY IT IS NOT TYPED IN.
//
// This gate takes what to look for as an argument, and a tool that does that is
// only as good as the argument. It reported clean for five days over a tree
// holding fourteen registered directory names, because the list it was handed
// was the four names somebody thought of on one afternoon and nothing connected
// that list to the machine. The same defect then appeared one level over, in the
// OTHER input: a deny list handed a placeholder literal copied out of an example
// command produced a finding against the very redaction that placeholder
// documents. The tool was right both times; the input was wrong both times.
//
// So the list is derived from the two files that cannot go stale, plus the
// keychain:
//
//	~/.timetattle/registered-directories.json  every directory ever blessed,
//	                                           and through each one's marker the
//	                                           client and project it declares —
//	                                           which is exactly what a pasted
//	                                           `metrics` capture prints
//	~/.timetattle/config.json                  reference_projects (the real path
//	                                           behind each published pseudonym)
//	                                           and engagements (the names that
//	                                           carry money)
//	security find-identity                     the signing certificate's common
//	                                           name and team ID, the two
//	                                           developer identifiers that were
//	                                           previously typed from memory
//
// Registering a new client directory therefore widens the gate's list on the
// next run, instead of silently widening the surface it cannot see.
//
// # Two tiers, because a registered name is not automatically a client name
//
// This repository's own directory is registered and its name is in three
// hundred files. A gate that failed on every registered name would be switched
// off within a week, which is the standing objection internal/corpusgate makes
// to a rule needing an exemption for half its own findings.
//
// So a derived name lands in one of two tiers:
//
//	Names       proven sensitive BY CONSTRUCTION and never a human's to wave
//	            through: a path that already has a pseudonym, and a name being
//	            billed. Finding one is a leak.
//	Candidates  registered names whose sensitivity nobody has ruled on. Finding
//	            one is not a leak; it is an UNANSWERED QUESTION, and the gate
//	            says so in those words and stays red until it is answered.
//
// A ruling is recorded in ~/.timetattle/publication-gate.json, outside the
// repository for the same reason every other sensitive particular is: writing
// "<real client> is publishable" into a tracked file would publish the name the
// ruling is about. It is a SEPARATE file from config.json rather than a new key
// in it, because config.Load refuses unknown keys outright — a gate key added
// there would fail every timetattle command on a machine running an older
// build, including the scheduled harvest.
//
// # Absence is never emptiness
//
// Both files' ordinary readers treat a missing file as an empty one, correctly:
// config.Load returns the defaults with a nil error, and registry.List returns
// no markers. For a GATE that tolerance is the false all-clear itself — an
// empty list finds nothing and the run reports clean. Every source below is
// therefore observed in three states, exactly as config.ResolveReferenceProject
// distinguishes them: read (with a count), absent, or unreadable. What the gate
// does with that is in main.go: it refuses to conclude.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/registry"
)

// sourceState is what one derivation source was OBSERVED to be. The three are
// not interchangeable and collapsing any two of them is the defect this whole
// file exists to prevent.
type sourceState int

const (
	// sourceAvailable: it was read. Observed carries the count, which is what
	// makes "this machine registers nothing" a measurement rather than a guess.
	sourceAvailable sourceState = iota
	// sourceAbsent: there is no such file, or no such tool. Nothing was
	// observed, so nothing may be concluded.
	sourceAbsent
	// sourceUnreadable: it is there and could not be understood. Also nothing
	// observed, and for a different reason worth printing.
	sourceUnreadable
)

func (s sourceState) String() string {
	switch s {
	case sourceAvailable:
		return "read"
	case sourceAbsent:
		return "absent"
	default:
		return "unreadable"
	}
}

// source is one place the list came from, and what happened when it was read.
type source struct {
	Name     string
	Path     string
	State    sourceState
	Observed string
	Err      error
	// Required says whether the gate is BLIND without this source. The config,
	// the registry and the keychain are: not reading one means the list is
	// missing a whole class of name and no verdict drawn from it means
	// anything. The classification file is not, because its absence has a
	// defined and safe meaning — nothing has been ruled publishable, so every
	// registered name stays an open question and the gate stays red. Absence
	// that fails closed is a state; absence that fails open is a precondition.
	Required bool
}

func (s source) String() string {
	if s.Err != nil {
		return fmt.Sprintf("%s (%s): %s: %v", s.Name, s.State, s.Observed, s.Err)
	}
	return fmt.Sprintf("%s (%s): %s", s.Name, s.State, s.Observed)
}

// derived is the whole list, in tiers, with the provenance of each part.
type derived struct {
	// Names are proven sensitive by construction.
	Names []string
	// Candidates are registered names nobody has classified.
	Candidates []string
	// Deny are literal strings reported wherever they appear.
	Deny []string
	// Refused are inputs the gate declined to hunt, with the reason. A refusal
	// is printed, never swallowed: the placeholder that produced a false
	// finding was accepted silently.
	Refused []string
	Sources []source
}

// derivePaths says where to look. Tests pass temporary paths and a stub
// identity reader; main() passes this machine's own.
type derivePaths struct {
	Config     string
	Registry   string
	Gate       string
	Identities func() (string, error)
}

// errNoSigningTool is what the identity reader returns when there is no
// codesigning tool to ask — a different observation from a keychain that holds
// no identities.
var errNoSigningTool = errors.New("no codesigning tool on this machine")

// derive builds the list from every source, recording what each one was
// observed to be.
func derive(p derivePaths) derived {
	var d derived

	names, csrc := deriveConfigNames(p.Config)
	d.Names = append(d.Names, names...)
	d.Sources = append(d.Sources, csrc)

	registered, rsrc := deriveRegistryNames(p.Registry)
	d.Sources = append(d.Sources, rsrc)

	gate, gsrc := readGateFile(p.Gate)
	d.Sources = append(d.Sources, gsrc)

	sensitive := map[string]bool{}
	for _, n := range d.Names {
		sensitive[strings.ToLower(n)] = true
	}
	for _, n := range registered {
		// A name the config proves sensitive is counted once, in the tier that
		// no ruling can silence.
		if sensitive[strings.ToLower(n)] || gate.publishable(n) {
			continue
		}
		d.Candidates = append(d.Candidates, n)
	}

	identities, isrc := readSigningIdentities(p.Identities)
	d.Sources = append(d.Sources, isrc)
	for _, lit := range append(signingLiterals(identities), gate.DeniedLiterals...) {
		accepted, err := acceptDenyLiteral(lit)
		if err != nil {
			d.Refused = append(d.Refused, err.Error())
			continue
		}
		d.Deny = append(d.Deny, accepted)
	}

	d.Names = dedup(d.Names)
	d.Candidates = dedup(d.Candidates)
	d.Deny = dedup(d.Deny)
	return d
}

// Unavailable lists the REQUIRED sources that were not read. A run with any of
// these is a run that cannot say what it did not find.
func (d derived) Unavailable() []source {
	var out []source
	for _, s := range d.Sources {
		if s.Required && s.State != sourceAvailable {
			out = append(out, s)
		}
	}
	return out
}

// Complete reports whether every source was read.
func (d derived) Complete() bool { return len(d.Unavailable()) == 0 }

// --- the config ----------------------------------------------------------------

// deriveConfigNames reads the two sections of ~/.timetattle/config.json that
// name real things: the paths behind published pseudonyms, and the engagements
// that carry money.
//
// It stats the file before calling config.Load, because Load returns the
// DEFAULTS with a nil error for a file that does not exist — the right
// tolerance for a tool that must run on a fresh machine, and the wrong one for
// a gate, which would otherwise hunt an empty list and call the tree clean.
func deriveConfigNames(path string) ([]string, source) {
	src := source{Name: "config", Path: path, Required: true}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			src.State = sourceAbsent
			src.Observed = fmt.Sprintf("%s does not exist, so no reference-project or engagement name is in this list", path)
		} else {
			src.State = sourceUnreadable
			src.Observed = fmt.Sprintf("%s could not be stat'd", path)
			src.Err = err
		}
		return nil, src
	}

	cfg, err := config.Load(path)
	if err != nil {
		src.State = sourceUnreadable
		src.Observed = fmt.Sprintf("%s exists and could not be parsed", path)
		src.Err = err
		return nil, src
	}

	var names []string
	for _, real := range cfg.ReferenceProjects {
		// The KEY is the pseudonym, which is the approved name and is supposed
		// to be all over the repository. The VALUE is the path carrying the
		// real one.
		if base := filepath.Base(filepath.Clean(real)); base != "" && base != "." && base != string(filepath.Separator) {
			names = append(names, base)
		}
	}
	for name := range cfg.Engagements.Clients {
		names = append(names, name)
	}
	for name := range cfg.Engagements.Projects {
		names = append(names, name)
	}

	src.State = sourceAvailable
	src.Observed = fmt.Sprintf("%s maps %d reference project(s), %d engagement client(s) and %d engagement project(s)",
		path, len(cfg.ReferenceProjects), len(cfg.Engagements.Clients), len(cfg.Engagements.Projects))
	return dedup(names), src
}

// --- the registry ---------------------------------------------------------------

// deriveRegistryNames reads every registered directory's name, and through its
// marker the client and project it declares.
//
// It stats the index for the same reason deriveConfigNames stats the config:
// registry.List treats a missing index as an empty one.
func deriveRegistryNames(indexPath string) ([]string, source) {
	src := source{Name: "registry index", Path: indexPath, Required: true}
	if _, err := os.Stat(indexPath); err != nil {
		if os.IsNotExist(err) {
			src.State = sourceAbsent
			src.Observed = fmt.Sprintf("%s does not exist, so no registered directory, client or project name is in this list", indexPath)
		} else {
			src.State = sourceUnreadable
			src.Observed = fmt.Sprintf("%s could not be stat'd", indexPath)
			src.Err = err
		}
		return nil, src
	}

	markers, err := registry.List(indexPath)
	if err != nil {
		// List returns the markers it COULD read alongside the errors for the
		// ones it could not, so the names are still worth having — but a
		// partial read is not a read, and the state says so.
		src.State = sourceUnreadable
		src.Observed = fmt.Sprintf("%s lists directories whose markers could not all be read", indexPath)
		src.Err = err
	} else {
		src.State = sourceAvailable
		src.Observed = fmt.Sprintf("%s resolves %d registered director(ies)", indexPath, len(markers))
	}

	var names []string
	for _, m := range markers {
		if base := filepath.Base(m.Path); base != "" && base != "." {
			names = append(names, base)
		}
		if m.Client != "" {
			names = append(names, m.Client)
		}
		if m.Project != "" {
			names = append(names, m.Project)
		}
	}
	return dedup(names), src
}

// --- the classification file ------------------------------------------------------

// gateFile is ~/.timetattle/publication-gate.json: the record of which
// registered names a human has ruled publishable, and any literal the machine
// cannot derive on its own.
type gateFile struct {
	Version          int      `json:"gate_version"`
	PublishableNames []string `json:"publishable_names"`
	DeniedLiterals   []string `json:"denied_literals"`

	classified map[string]bool
}

func (g gateFile) publishable(name string) bool { return g.classified[strings.ToLower(name)] }

// gateFileVersion is the shape this build writes and understands, for the same
// reason the config and the registry index each carry one.
const gateFileVersion = 1

func readGateFile(path string) (gateFile, source) {
	src := source{Name: "classification", Path: path}
	g := gateFile{Version: gateFileVersion, classified: map[string]bool{}}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			src.State = sourceAbsent
			src.Observed = fmt.Sprintf("%s does not exist, so no registered name has been classified", path)
		} else {
			src.State = sourceUnreadable
			src.Observed = fmt.Sprintf("%s could not be read", path)
			src.Err = err
		}
		return g, src
	}

	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		src.State = sourceUnreadable
		src.Observed = fmt.Sprintf("%s exists and could not be parsed", path)
		src.Err = err
		return gateFile{classified: map[string]bool{}}, src
	}
	if g.Version != gateFileVersion {
		src.State = sourceUnreadable
		src.Observed = fmt.Sprintf("%s declares gate_version %d and this build understands %d", path, g.Version, gateFileVersion)
		return gateFile{classified: map[string]bool{}}, src
	}

	g.classified = map[string]bool{}
	for _, n := range g.PublishableNames {
		g.classified[strings.ToLower(strings.TrimSpace(n))] = true
	}
	src.State = sourceAvailable
	src.Observed = fmt.Sprintf("%s classifies %d name(s) publishable and adds %d denied literal(s)",
		path, len(g.PublishableNames), len(g.DeniedLiterals))
	return g, src
}

// --- denied literals ---------------------------------------------------------------

// identityLine matches one row of `security find-identity -v -p codesigning`.
// The example is written in the placeholder form scripts/sign.sh already uses,
// because this file is audited by the gate it feeds and a realistic-looking
// certificate common name in a comment is a finding:
//
//	AB12CD34 "Developer ID Application: Someone (TEAMID)"
var identityLine = regexp.MustCompile(`"([^"]+)"`)

// identityCommonName splits a certificate's common name into the name and the
// parenthesised team ID.
var identityCommonName = regexp.MustCompile(`^[^:]+:\s*(.+?)\s*\(([A-Za-z0-9]+)\)\s*$`)

// readSigningIdentities asks the machine what it can sign with. A keychain
// holding no identities is a COUNTED observation and perfectly fine; having no
// tool to ask is not, and the two are different states.
func readSigningIdentities(read func() (string, error)) (string, source) {
	src := source{Name: "signing identity", Path: "security find-identity -v -p codesigning", Required: true}
	if read == nil {
		read = securityFindIdentity
	}
	out, err := read()
	if err != nil {
		src.State = sourceAbsent
		src.Observed = "no codesigning identity could be listed, so no developer identifier is in this list"
		src.Err = err
		return "", src
	}
	src.State = sourceAvailable
	src.Observed = fmt.Sprintf("the keychain lists %d usable signing identit(ies)", len(identityLine.FindAllString(out, -1)))
	return out, src
}

func securityFindIdentity() (string, error) {
	path, err := exec.LookPath("security")
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoSigningTool, err)
	}
	// find-identity exits non-zero on some macOS versions when nothing is
	// found, which is an answer rather than a failure; the output is what
	// matters, so only a total absence of output is treated as an error.
	out, err := exec.Command(path, "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("run security find-identity: %w", err)
	}
	return string(out), nil
}

// signingLiterals pulls the certificate common names and team IDs out of
// find-identity's output. Placeholder rows are dropped here rather than
// refused: scripts/sign.sh's own documentation quotes this exact line shape,
// and a parser that turned documentation into a hunted literal would recreate
// the false finding this file exists to stop.
func signingLiterals(out string) []string {
	var lits []string
	for _, quoted := range identityLine.FindAllStringSubmatch(out, -1) {
		m := identityCommonName.FindStringSubmatch(quoted[1])
		if m == nil {
			continue
		}
		// Each literal is vetted on its own. A row whose common name is a
		// documented placeholder may still carry a real team ID beside it,
		// and dropping the whole row would lose it.
		for _, lit := range m[1:] {
			if placeholderRe.MatchString(lit) {
				continue
			}
			lits = append(lits, lit)
		}
	}
	return dedup(lits)
}

// minDenyLiteral is the shortest literal worth substring-matching. A deny
// literal is matched anywhere in a line, with no word boundary, so a short one
// is not a check but a noise generator — this module's own import path contains
// the account name it would otherwise hunt.
const minDenyLiteral = 6

// acceptDenyLiteral vets one literal before the gate hunts for it. It is the
// direct answer to a placeholder copied out of an example command producing a
// finding against the redaction that placeholder documents.
func acceptDenyLiteral(s string) (string, error) {
	v := strings.TrimSpace(s)
	switch {
	case v == "":
		return "", errors.New("refused an empty denied literal")
	case strings.ContainsAny(v, "<>"):
		return "", fmt.Errorf("refused the denied literal %q: it is a placeholder, and hunting it reports the redaction as the leak", v)
	case placeholderRe.MatchString(v):
		return "", fmt.Errorf("refused the denied literal %q: it is one of the placeholder forms this repository's documentation uses on purpose", v)
	case len(v) < minDenyLiteral:
		return "", fmt.Errorf("refused the denied literal %q: shorter than %d characters, and it is matched as a bare substring",
			v, minDenyLiteral)
	}
	return v, nil
}

// --- helpers ------------------------------------------------------------------------

// dedup returns the distinct entries, case-insensitively, keeping the first
// spelling seen and sorting for a stable report.
func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		k := strings.ToLower(v)
		if v == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
