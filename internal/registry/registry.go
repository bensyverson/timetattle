// Package registry attributes filesystem paths to the client and project
// they belong to.
//
// Harvested evidence carries only where it was found — a cwd, an autosave's
// path, a bundle identifier — never who it was for. This package closes that
// gap for the sources that carry a locatable path, by letting a person bless
// a directory once and have every event beneath it resolve to the same
// identity. It does not decide when two events belong to the same session,
// and it does not know what a client's contract is worth; both live
// elsewhere, deliberately.
//
// # Directory-shaped, not git-shaped
//
// The evidence that made the strongest case for this project — Motion,
// Blender and Photoshop files from a Saturday git recorded nothing about —
// lives in plain paths with no repository at all. A registry keyed on git
// remotes or commits could not attribute any of it, so identity is a marker
// file dropped into an ordinary directory, and resolution (see Resolve) is
// nothing more than "walk up until you find one."
//
// # Identity is a token in the directory, not a path kept elsewhere
//
// This package keeps no central table mapping paths to projects. Each
// blessed directory carries its own marker, so moving or cloning it carries
// the identity along for free, and there is nothing external to keep in
// sync. The cost is that a project spanning several directories — a repo
// plus a shared asset folder plus a scratch render directory — needs a
// marker in each one; Register places one, and calling it again against a
// second directory with the same client and project blesses that directory
// too, with no extra bookkeeping. See Resolve for how a git worktree, which
// is its own repository but usually not its own project, is kept from
// resolving to itself.
//
// # What a marker deliberately does not hold
//
// A marker carries only client and project identity. Contract terms — a
// rate, a budget, what kind of engagement it is — live in global config and
// never reach a file this package writes, because that file sits inside a
// client's repository and can be committed by accident; a rate leaking into
// a client's own git history is a materially worse mistake than a
// misattributed hour. See Register for the one refusal this implies beyond
// simply not writing the fields: a directory a manifest already declares as
// a shared application cache can never be blessed, because a cache holding
// every client's Motion compositions has no single client to belong to.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/manifest"
)

// Version is the marker schema this build understands.
//
// Versioned for the same reason a manifest is: a marker lives on a client's
// disk, inside their repository, outside this program's control, and a
// build that changes what this file means cannot silently reinterpret one
// already committed somewhere.
const Version = 1

// MarkerFilename is what Register writes and Resolve looks for in every
// directory it climbs through.
const MarkerFilename = ".timetattle-project.json"

// Marker is the identity Register writes into a blessed directory.
type Marker struct {
	// Version is the schema version the marker declares.
	Version int `json:"marker_version"`
	// Client is who the work in this directory is for.
	Client string `json:"client"`
	// Project groups a client's work into the body of work being billed
	// or reported on.
	Project string `json:"project"`
	// FormerPaths are absolute paths this directory used to occupy, declared
	// by Consolidate after a rename. They are identity in the same sense
	// client and project are — "this project is also what used to live
	// there" — which is why they belong here rather than in a table
	// elsewhere; see Consolidate for why a rename cannot be handled by
	// registering the old location, and ResolveAmong for how they are read.
	//
	// Omitted when empty, so a marker written by Register alone is byte for
	// byte what it always was, and adding this field needed no Version bump:
	// readMarker requires an EXACT version match, so bumping would make every
	// marker already on disk unreadable, and an optional field an older build
	// ignores is not the kind of change versioning exists for.
	FormerPaths []string `json:"former_paths,omitempty"`
	// Registered is when this directory was blessed with the client and
	// project it now names. It is provenance, not identity: it is not
	// compared when deciding whether two markers name the same project, and
	// Consolidate deliberately leaves it alone when adding a former path,
	// because a rename declared months later is not a re-registration and
	// overwriting the date would lose the only record of when tracking
	// actually started.
	Registered time.Time `json:"registered"`
	// Path is the directory this marker was read from or written to. It is
	// not serialised — a marker that travels with a moved or cloned
	// directory must not carry a stale path inside itself, which is the
	// entire reason identity lives in a token rather than a location.
	Path string `json:"-"`
}

// Registration is what Register needs to bless a directory.
type Registration struct {
	// Dir is the directory to bless. Relative paths are resolved against
	// the current working directory.
	Dir string
	// Client is who the work belongs to.
	Client string
	// Project is the body of work within Client.
	Project string
	// Force allows Register to overwrite a marker that already names a
	// different client or project, and to override a cross-directory client
	// conflict against Among. Without it, re-registering a directory
	// that already belongs to someone else is refused rather than silently
	// reassigning it — cheap to reverse, since project is a derived cache
	// and not a stored fact, but still worth a deliberate flag rather than
	// a typo'd path doing it by accident.
	//
	// IT MUST OVERRIDE THE CROSS-DIRECTORY CHECK TOO, and the reason is the
	// workflow rather than the flag: a client rename is performed by
	// re-registering the directories that name it one at a time, so the FIRST
	// of those calls collides with every one of the others. A refusal Force
	// could not override would leave a rename with no route through this tool
	// at all, and would drive a user to hand-editing marker files — the one
	// thing no check here can see.
	//
	// What it deliberately does NOT do is reassign the other directories.
	// Register writes ONE marker, in the directory it was pointed at; silently
	// rewriting three others the caller did not name would be a much larger
	// action than the flag advertises. The refusal instead enumerates them, so
	// the caller knows exactly which ones must follow.
	Force bool
	// Among is the other registrations to check this one against, and it is
	// what makes the project name mean one thing.
	//
	// All directories naming the same PROJECT must agree on the CLIENT. Without
	// that, two directories can register project "website" under two different
	// clients, both cleanly, and every report that groups by project — which is
	// every report `timetattle metrics` prints — merges two clients' hours into
	// one row with nothing to say it happened. That is a defect in
	// project-level reporting on its own, independent of any client roll-up.
	//
	// It is passed in rather than read here, exactly as ResolveAmong takes its
	// registered markers and for the same two reasons. Register is settled,
	// widely called code that touches no machine-global path, and folding a
	// read of ~/.timetattle/registered-directories.json into it would make
	// every test in this package read the real machine's registry — the
	// pollution RecordInIndex's own doc explains at length. And a caller that
	// has already enumerated the registrations should not pay for a second
	// enumeration.
	//
	// Passing nil gives the per-directory check alone, which is what Register
	// has always done and what every caller with no registry to consult wants.
	Among []*Marker
}

// Register blesses a directory with a client and project identity.
//
// Calling it again with the same client and project is a no-op that returns
// the marker already on disk, unchanged, so a script can register
// unconditionally on every run without rewriting a file that has not
// changed. Calling it with a different client or project on an already
// blessed directory is refused unless Force is set.
func Register(reg Registration) (*Marker, error) {
	client := strings.TrimSpace(reg.Client)
	project := strings.TrimSpace(reg.Project)
	if client == "" {
		return nil, errors.New("register: client is required")
	}
	if project == "" {
		return nil, errors.New("register: project is required")
	}

	dir, err := filepath.Abs(reg.Dir)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	dir = filepath.Clean(dir)

	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("register: %s is not a directory", dir)
	}

	if root, blocked := blessedCacheConflict(dir); blocked {
		return nil, fmt.Errorf("register: %s is, or contains, or sits inside a shared application cache "+
			"declared by a manifest (%s); blessing it would attribute every client's work in that cache to "+
			"one project, so it has to be attributed by the manifest's document-name join instead, not "+
			"registered here", dir, root)
	}

	existing, found, err := readMarker(dir)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	if found {
		if existing.Client == client && existing.Project == project {
			return existing, nil
		}
		if !reg.Force {
			return nil, fmt.Errorf("register: %s is already registered to %s / %s; pass Force to reassign it to %s / %s",
				dir, existing.Client, existing.Project, client, project)
		}
	}

	if err := projectClientConflict(dir, client, project, reg.Among); err != nil && !reg.Force {
		return nil, err
	}

	m := &Marker{
		Version:    Version,
		Client:     client,
		Project:    project,
		Registered: time.Now().UTC(),
		Path:       dir,
	}
	if found {
		// A former path records where this DIRECTORY used to be, not who
		// currently owns it, so a reassignment corrects the client and
		// project and leaves that history alone. Rewriting the marker is the
		// one place it could be dropped without anyone noticing.
		m.FormerPaths = existing.FormerPaths
	}
	if err := writeMarker(m); err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	return m, nil
}

// projectClientConflict reports whether any OTHER registered directory already
// holds this project name for a different client, and names every one that
// does.
//
// THE MESSAGE IS THE FEATURE. A refusal saying only "that project belongs to
// someone else" leaves the user grepping the filesystem for marker files to
// find out who, and this repository has spent three commits on messages that
// named a conclusion instead of the missing thing. So it names the directory,
// the client holding it, and — when several disagree — all of them, because
// under Force the caller is midway through a rename and the list is exactly the
// set of directories still to be re-registered.
//
// The directory being registered is excluded by path: re-registering a
// directory that is itself in Among is the ordinary case for a rename, and
// colliding with oneself would make Force mandatory for a no-op.
func projectClientConflict(dir, client, project string, among []*Marker) error {
	var held []string
	for _, m := range among {
		if m == nil || m.Project != project || m.Client == client {
			continue
		}
		if filepath.Clean(m.Path) == dir {
			continue
		}
		held = append(held, fmt.Sprintf("%s (%s)", m.Path, m.Client))
	}
	if len(held) == 0 {
		return nil
	}
	return fmt.Errorf("register: project %q is already held for a different client by %s, and every "+
		"directory naming one project has to agree on its client — otherwise a report grouped by "+
		"project silently merges two clients' hours. Register %s under a project name of its own, "+
		"or pass Force to move this directory to %s and re-register the %s listed above",
		project, strings.Join(held, ", "), dir, client, plural(len(held), "directory", "directories"))
}

// plural picks a word form for a count, so a refusal naming one directory does
// not say "1 directories". It is here rather than shared because
// cmd/timetattle's own plural formats the number too, and this message reads
// better without it.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// writeMarker serialises m into MarkerFilename inside m.Path. Register and
// Consolidate both go through it so there is one place that decides what a
// marker file looks like on disk.
func writeMarker(m *Marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(m.Path, MarkerFilename), data, 0o644)
}

// cacheSourceRoots lists the directories manifests declare as shared
// application caches. It is a package variable, rather than a plain
// function call inlined into blessedCacheConflict, so tests can exercise the
// containment logic against a directory that does not depend on what
// manifests happen to be installed on the machine running them.
var cacheSourceRoots = func() ([]string, error) {
	sources, err := manifest.LoadAll("")
	roots := make([]string, 0, len(sources))
	for _, s := range sources {
		if s.Files.Root != "" {
			roots = append(roots, s.Files.Root)
		}
	}
	return roots, err
}

// blessedCacheConflict reports whether dir is, contains, or sits inside a
// directory a manifest declares as a shared application cache.
//
// Containment is checked both directions. Blessing the cache directly
// attributes every client's compositions to whoever ran the command;
// blessing an ancestor of it does the same thing one hop later, because
// Resolve walks upward and would find that marker from inside the cache just
// the same; blessing a directory inside the cache (a single composition's
// autosave folder, say) is refused too, for the same reason in reverse — see
// TestBlessedCacheConflictRefusesADeclaredCacheRoot for the three cases this
// checks.
//
// Best effort: a manifest that fails to parse elsewhere does not block
// registration, the same way LoadAll already tolerates one bad file without
// losing the others. A typo in an unrelated manifest costing every
// registration in the meantime would be worse than a guard that occasionally
// runs with an incomplete list.
func blessedCacheConflict(dir string) (string, bool) {
	roots, _ := cacheSourceRoots()
	for _, root := range roots {
		root = filepath.Clean(root)
		if isAncestorOrSame(dir, root) || isAncestorOrSame(root, dir) {
			return root, true
		}
	}
	return "", false
}

// CacheConflict reports whether Register would refuse dir because it is,
// contains, or sits inside a directory a manifest declares as a shared
// application cache, and names the cache root when it would.
//
// It exists for a caller that RECOMMENDS a registration rather than performing
// one. `timetattle orient` ranks directories carrying unattributed evidence and
// names the best one to bless; a recommendation Register will refuse is an
// instruction that fails every time it is followed, and the only honest way to
// avoid making it is to ask the same question Register asks rather than a
// second question that resembles it. Reimplementing the containment rule in the
// caller — walk the manifest roots, compare prefixes — is how the two answers
// start to disagree the first time either changes, which is the same reason
// FormerPathOwner is exported beside ResolveAmong instead of being rewritten by
// whoever needs it.
//
// It is not the whole of what Register refuses, and does not pretend to be:
// Register also rejects a path that is not a directory and a directory already
// blessed to a different project. Those two are cheap for a caller to establish
// on its own and neither depends on what manifests are installed; this one is
// the refusal that does.
func CacheConflict(dir string) (root string, refused bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		// The same tolerance blessedCacheConflict already gives an
		// unreadable manifest: an unusable path is not evidence of a cache,
		// and Register will fail on it for its own reasons a moment later.
		return "", false
	}
	return blessedCacheConflict(filepath.Clean(abs))
}

// isAncestorOrSame reports whether a is b, or a directory somewhere above b.
//
// The separator is appended only when a does not already end in one. That looks
// like a tidiness detail and is not: filepath.Clean("/") is "/", so the
// unconditional form asks whether b starts with "//", which nothing does, and
// the filesystem root silently contained NOTHING. Found on 2026-08-01 by
// `timetattle orient`, which classifies a locator by asking whether it sits
// above a registered directory and was told that "/" does not — see
// TestIsAncestorOrSameAnswersForTheFilesystemRoot for the three refusals in this
// package that the defect quietly disabled.
func isAncestorOrSame(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	if !strings.HasSuffix(a, string(filepath.Separator)) {
		a += string(filepath.Separator)
	}
	return strings.HasPrefix(b, a)
}
