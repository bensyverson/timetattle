package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// orient says what to do next, and what doing it is worth.
//
// # Why the name is orient and not system or overview
//
// This project's own CLAUDE.md opens with "Open by running `job orient`
// (standalone tool, no arguments). At session start, `job orient` is the
// landscape briefing." The primary user of this CLI is an agent that has
// already been told to run `job orient` before it reads a line of this
// codebase, so `timetattle orient` is the one name it can GUESS. That symmetry
// is the strongest argument and it is not the only one.
//
// system and overview both name CONTENT, and content is exactly what this
// command must not be defined by, because two commands already own content:
// status owns the machinery and metrics owns the numbers. A command called
// overview invites every future reader to move a number into it, and the three
// commands converge on half-answering the same question — the failure this
// boundary exists to prevent. orient names a PURPOSE instead, and a purpose is
// something a reviewer can hold a proposed addition against: does this line
// change what somebody should do next? If not, it belongs in one of the other
// two.
//
// # The boundary against status and metrics, argued
//
//	status   is the collection machinery still working?  Reads harvest_run and
//	         capture_run and RETURNS A VERDICT on them, with tolerances derived
//	         from the runs' own history (see statusClock).
//	metrics  what are the numbers?  Engaged time per project, presence, the
//	         observed/inferred split. Everything it prints is HOURS or is in
//	         service of an hours figure.
//	orient   what should I do next, and what is it worth?  The gap between what
//	         the store holds and what it could say if its attribution were
//	         current — expressed as actions, ranked by what each would clear.
//
// Three things follow from that, and each is enforced by a test:
//
//   - THIS COMMAND PRINTS NO TIME AT ALL. Not one figure here goes through
//     internal/sessions, so not one may wear an hours suffix. Counts of
//     observations are what it has and counts of observations are what it says,
//     with the caveat stated rather than implied — a Claude transcript emits
//     orders of magnitude more rows per hour than gitlog does, so a big count
//     is not a lot of work. Hours are metrics' answer and this defers to it.
//   - IT RETURNS NO VERDICT ON COLLECTION HEALTH. It prints the age of the
//     newest harvest run as a bare fact and names status as the thing that
//     judges it. A second opinion on whether a source is late, with a second
//     threshold, is precisely the duplication that makes two commands disagree;
//     status' tolerances were measured against 76 real runs and reproducing
//     even one of them here would be a worse answer than a pointer.
//   - IT WRITES NOTHING AND OPENS THE STORE READ ONLY, like status and metrics.
//     An orientation that changed what it was orienting you to is not one.
//
// # Why the first screen is about attribution rather than about registration
//
// The task this was built for asked for a ranked list of large unregistered
// directories to bless. Measured against the live store on 2026-08-01, that
// pool is empty: of 637,013 observations carrying a path no project owns,
// 620,643 (97.5%) sit inside a directory that is ALREADY REGISTERED and are
// unattributed only because the pass that resolves them has not run since. The
// genuinely registerable remainder was 66 observations, 0.01%. A command built
// to rank a registration backlog would have had nothing true to say, and
// `timetattle metrics` was meanwhile labelling all 437.3h of that presence
// "ACTIONABLE: register the directory".
//
// So the census below is organised by WHAT WOULD CLEAR EACH CLASS, not by size,
// and the next action is whichever remedy clears the most. Those are different
// orderings and only the first survives the pool changing shape.
//
// # What it costs
//
// The census reads store.Store.LocatorCounts, which does the aggregation in
// SQL — a GROUP BY over locator, plus COUNT(*) and MIN/MAX(anchor) — rather
// than by reading every event into a source.Event the way this command first
// shipped. That first version measured at 4.4 seconds against the
// 923,000-event live store and grew linearly with it, because Events() decodes
// eleven columns and parses up to two timestamps per row regardless of which
// two or three fields orient actually needs. Measured on the same store,
// LocatorCounts answers in well under half a second and the improvement grows
// with the store rather than shrinking. Classification still happens here,
// one os.Stat and one registry.ResolveAmong per DISTINCT locator (48 on the
// live store, not 923,000) — internal/store knows nothing of either.
func orient(args []string) error { return orientTo(os.Stdout, args) }

// orientTo is orient with the report redirected, so what the command prints can
// be asserted on rather than merely not crashing.
func orientTo(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("orient", flag.ExitOnError)
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	indexPath := fs.String("index", "",
		"registry index to resolve against (default: ~/.timetattle/registered-directories.json)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: timetattle orient

Says what the state is and what to do about it. Takes no arguments: it is meant
to be the first thing run in a session, by someone or something with no memory
of the last one.

It reports every observation carrying a path that no project owns, grouped by
WHAT WOULD CLEAR IT rather than by size, and names the single remedy that would
clear the most. A directory is only ever recommended if `+"`timetattle register`"+`
would actually accept it, and a subdirectory is never recommended separately
from the directory whose marker would already cover it.

EVERY FIGURE IT PRINTS IS A COUNT OF OBSERVATIONS AND NONE OF THEM IS TIME.
Sources emit rows at wildly different rates, so a large count is not a large
number of hours. Run "timetattle metrics" for time, and "timetattle status" for
whether collection itself is still working; this command answers neither and
points at both.

Nothing is written and the store is opened read-only.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var err error
	if *storePath == "" {
		if *storePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	if *indexPath == "" {
		if *indexPath, err = registry.DefaultIndexPath(); err != nil {
			return err
		}
	}

	registered, err := registry.List(*indexPath)
	if err != nil {
		return fmt.Errorf("read the registry: %w", err)
	}

	// Read-only for the same reason status and metrics are: a command that
	// created or migrated the store would be reporting on a schema no harvest
	// had ever run against.
	st, err := store.OpenReadOnly(*storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	counts, err := st.LocatorCounts(ctx)
	if err != nil {
		return err
	}
	runs, err := st.Runs(ctx, store.Filter{})
	if err != nil {
		return err
	}

	census := orientCensusOf(counts, registered)
	census.Store = *storePath
	census.Registered = len(registered)
	census.LastHarvest = orientLastHarvest(runs)
	// The report is laid out for the terminal it is going into, and clipped as
	// a backstop. columnsOf returns 0 for a pipe, which leaves both alone.
	cols := columnsOf(stdout)
	orientReport(clipTo(stdout, cols), census, time.Now(), cols)
	return nil
}

// orientRemedy is what has to happen for a class of unattributed evidence to
// become attributed, and it is the axis the census is organised on. Sorting by
// volume instead is what produces a command that recommends the same impossible
// action every time it is run — the task this was built for records exactly
// that outcome for a directory that no longer exists.
type orientRemedy int

const (
	// orientPending: the path already sits inside a registered directory and
	// will attribute the next time the resolution pass runs. Nothing to decide.
	orientPending orientRemedy = iota
	// orientStranded: the path is gone from disk. No marker can be written
	// into a directory that does not exist, so registration cannot reach it and
	// `timetattle consolidate` is the remedy.
	orientStranded
	// orientRegisterable: a directory that exists, is unregistered, and
	// Register would accept.
	orientRegisterable
	// orientContainer: a directory sitting ABOVE one already registered.
	// Nothing should clear this; see orientClassify.
	orientContainer
	// orientCache: inside a shared application cache a manifest declares.
	// Register refuses it by design.
	orientCache
	// orientUnknown: the path could not be classified, which is not the same
	// as it having no remedy.
	orientUnknown
)

// orientCandidate is a directory, and the observations one action against it
// would clear.
type orientCandidate struct {
	Dir          string
	Observations int
}

// orientCensus is the whole of what the report prints.
type orientCensus struct {
	Store       string
	Registered  int
	LastHarvest time.Time

	// Observations is every observation in the store, and Earliest/Latest the
	// span they cover.
	Observations     int
	Earliest, Latest time.Time

	// WithPath is every observation carrying a locator that no project owns,
	// and is the population the buckets below divide exactly.
	WithPath int
	// NoPath is evidence from a source that carries no path at all. It can
	// never be attributed by registering anything, so it is never folded into
	// a figure that carries an action.
	NoPath int

	Pending      int
	Stranded     int
	Registerable int
	Container    int
	Cache        int
	Unknown      int
	// UnknownWhy is the first reason a locator could not be classified, kept
	// so "nothing was learned about these" is distinguishable from "these were
	// observed to have no remedy".
	UnknownWhy error

	// StrandedRoots and RegisterableRoots are rolled up so that one action
	// covers a directory and everything beneath it, ranked by what each is
	// worth.
	StrandedRoots     []orientCandidate
	RegisterableRoots []orientCandidate
}

// orientCensusOf divides every observation the store holds by what would
// attribute it.
//
// counts already carries the store's total size, its span, and — among
// unattributed observations — how many carry no locator against how many
// distinct locators appear and how many observations back each one; see
// store.Store.LocatorCounts. What is left here is the part that reaches past
// internal/store on purpose: asking the registry and the filesystem what each
// DISTINCT locator resolves to, which is why this loop runs once per locator
// (48 on the live store) and not once per observation (923,000).
func orientCensusOf(counts store.LocatorCounts, registered []*registry.Marker) orientCensus {
	c := orientCensus{
		Observations: counts.Observations,
		Earliest:     counts.Earliest,
		Latest:       counts.Latest,
		NoPath:       counts.NoPath,
	}

	stranded := map[string]int{}
	registerable := map[string]int{}
	for locator, n := range counts.ByLocator {
		c.WithPath += n
		remedy, dir, err := orientClassify(locator, registered)
		switch remedy {
		case orientPending:
			c.Pending += n
		case orientStranded:
			c.Stranded += n
			stranded[dir] += n
		case orientRegisterable:
			c.Registerable += n
			registerable[dir] += n
		case orientContainer:
			c.Container += n
		case orientCache:
			c.Cache += n
		default:
			c.Unknown += n
			if c.UnknownWhy == nil {
				c.UnknownWhy = err
			}
		}
	}
	c.StrandedRoots = orientSubsume(stranded)
	c.RegisterableRoots = orientSubsume(registerable)
	return c
}

// orientClassify says what would attribute one locator, and names the directory
// the action would be taken against.
//
// The order of the checks is the whole of the logic and each one is ordered
// where it is for a reason:
//
//  1. RESOLVES ALREADY. Asked of registry.ResolveAmong — the real resolver,
//     with the real registry, exactly as the attribution pass asks it — rather
//     than by comparing the locator against the index's strings. A path is
//     "already registered" if and only if the code that does the attributing
//     says so, and the difference is not academic: the resolver climbs, follows
//     declared former paths, and knows about git worktrees.
//  2. THE PATH IS GONE. Checked before anything else about the directory,
//     because attributionDirOf cannot tell a vanished directory from a file and
//     strips a level from it — so a vanished /Users/ben/git/theaitrellis would
//     otherwise be classified against /Users/ben/git, which is the container
//     above nearly every registered directory on this machine. Consolidate is
//     the remedy and it takes the UNSTRIPPED path, for the same reason
//     ResolveAmong does.
//  3. IT IS A CONTAINER ABOVE SOMETHING ALREADY REGISTERED. Refused as a
//     recommendation, and the argument is internal/registry's own: "blessing a
//     container directory would silently shadow every rename declared beneath
//     it, and quietly attributing renamed work to a catch-all is the
//     wrong-attribution-is-worse-than-none failure". A marker at "/" — which
//     1,311 observations on the live store name as their cwd — would become the
//     owner of every unregistered directory on the volume.
//  4. REGISTER WOULD REFUSE IT AS A SHARED CACHE. Asked of
//     registry.CacheConflict rather than reimplemented, so this can never
//     recommend what Register rejects.
//
// Anything left is a directory that exists, is unregistered, is not a container
// and is not a cache: something Register would accept.
//
// One known imprecision, stated rather than hidden. A locator naming a FILE
// that has been deleted from a directory that still exists is classified
// stranded, because nothing on disk distinguishes it from a vanished directory.
// Consolidate is a valid remedy for it, so the recommendation is not wrong,
// merely not the only option; registering the surviving directory would work
// too. Preferring the safe misclassification is deliberate — the other
// direction recommends blessing a container.
func orientClassify(locator string, registered []*registry.Marker) (orientRemedy, string, error) {
	if _, found, err := registry.ResolveAmong(attributionDirOf(locator), locator, registered); err != nil {
		return orientUnknown, "", fmt.Errorf("resolve %s: %w", locator, err)
	} else if found {
		return orientPending, "", nil
	}

	if _, err := os.Stat(locator); err != nil {
		if os.IsNotExist(err) {
			return orientStranded, filepath.Clean(locator), nil
		}
		// Unreadable is not absent, and this project has shipped that
		// collapse more than once. Report it as unclassified.
		return orientUnknown, "", fmt.Errorf("stat %s: %w", locator, err)
	}

	dir := attributionDirOf(locator)
	for _, m := range registered {
		if m != nil && orientContains(dir, m.Path) {
			return orientContainer, dir, nil
		}
	}
	if _, refused := registry.CacheConflict(dir); refused {
		return orientCache, dir, nil
	}
	return orientRegisterable, filepath.Clean(dir), nil
}

// orientContains reports whether outer is a directory strictly above inner.
//
// Strictly: a directory that IS the registered one is not a container above it,
// and in any case never reaches this check, because the resolver would already
// have attributed it.
//
// The separator is appended only when outer does not already end in one, and
// that is not a tidiness detail — it is the whole of a bug this shipped with for
// one run. filepath.Clean("/") is "/", so the obvious
// HasPrefix(inner, outer+"/") asks whether inner starts with "//" and nothing
// does. The filesystem root therefore passed the container check, and the first
// run against the live store recommended `timetattle register -dir /` as the
// highest-value action on the machine — 1,311 Claude transcript observations
// really do carry "/" as their cwd. A marker there would have owned every
// unregistered directory on the volume. internal/registry.isAncestorOrSame had
// the identical defect and is corrected in the same change.
func orientContains(outer, inner string) bool {
	outer, inner = filepath.Clean(outer), filepath.Clean(inner)
	if outer == inner {
		return false
	}
	if !strings.HasSuffix(outer, string(filepath.Separator)) {
		outer += string(filepath.Separator)
	}
	return strings.HasPrefix(inner, outer)
}

// orientSubsume folds every candidate into the shallowest candidate above it,
// and ranks what is left by what one action against it is worth.
//
// This is the part a flat ranked list gets wrong, in both directions the census
// uses it:
//
//   - For a registration, registry.Resolve climbs to the NEAREST marker, so a
//     marker in the parent already covers the child. Recommending the child
//     first — which a ranking by count would, since a subdirectory can easily
//     carry more evidence than its root — leaves the parent's own observations
//     unattributed and costs a second registration to fix.
//   - For a consolidation, a declared former path matches by PREFIX, so
//     `-former` on the vanished root recovers everything recorded beneath it.
//     On the live store /Users/ben/git/theaitrellis carries 9,924 observations
//     and theaitrellis/stacktrace another 4,294; naming them separately would
//     be two declarations for one rename, and the second would be refused
//     because the first already claims the prefix.
//
// It subsumes only among candidates that are actually present. It never invents
// a common ancestor of two siblings, because the ancestor of two project
// directories is a container, and blessing a container is the failure
// orientClassify refuses on its own account.
func orientSubsume(counts map[string]int) []orientCandidate {
	dirs := make([]string, 0, len(counts))
	for dir := range counts {
		dirs = append(dirs, filepath.Clean(dir))
	}
	// Shallowest first, so the first match found is the outermost one and
	// nothing can be folded into a candidate that is itself about to be folded
	// away. Length is a valid proxy for depth here because every path has been
	// cleaned and they are all absolute.
	sort.Slice(dirs, func(i, j int) bool {
		if len(dirs[i]) != len(dirs[j]) {
			return len(dirs[i]) < len(dirs[j])
		}
		return dirs[i] < dirs[j]
	})

	rolled := map[string]int{}
	var roots []string
	for _, dir := range dirs {
		owner := ""
		for _, root := range roots {
			if orientContains(root, dir) {
				owner = root
				break
			}
		}
		if owner == "" {
			owner = dir
			roots = append(roots, dir)
		}
		rolled[owner] += counts[dir]
	}

	out := make([]orientCandidate, 0, len(rolled))
	for dir, n := range rolled {
		out = append(out, orientCandidate{Dir: dir, Observations: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Observations != out[j].Observations {
			return out[i].Observations > out[j].Observations
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// orientLastHarvest is when the newest run in the store began.
//
// Store.Runs returns rows in the order the runs began and explicitly declines
// to decide what "the latest run" means, so the choice is made here: the newest
// STARTED_AT, including a run that failed or is still going. A run that started
// is evidence the schedule fired, which is the only thing this line claims —
// whether it worked is status' question and this command does not answer it.
func orientLastHarvest(runs []store.Run) time.Time {
	var newest time.Time
	for _, r := range runs {
		if r.StartedAt.After(newest) {
			newest = r.StartedAt
		}
	}
	return newest
}

// orientReport prints the briefing.
func orientReport(out io.Writer, c orientCensus, now time.Time, cols int) {
	fmt.Fprintf(out, "%s\n", c.Store)
	fmt.Fprintf(out, "%s", plural(c.Observations, "observation"))
	if !c.Earliest.IsZero() {
		fmt.Fprintf(out, " from %s to %s",
			c.Earliest.Local().Format("2006-01-02"), c.Latest.Local().Format("2006-01-02"))
	}
	fmt.Fprintf(out, "; %d registered %s\n", c.Registered, orientDirectories(c.Registered))
	if c.LastHarvest.IsZero() {
		fmt.Fprint(out, "no harvest has ever run against this store\n")
	} else {
		// Wrapped, not hand-broken, because the elapsed time interpolated into
		// it has no fixed width — that is the whole reason wrap exists. It is
		// safe to wrap only because wrap keeps "`timetattle status`'s" in one
		// piece; splitting that across two lines leaves neither half runnable,
		// and TestOrientDoesNotRestateStatusOrMetrics asserts on it.
		wrap(out, "", fmt.Sprintf("last harvest %s, %v ago — whether that is a problem is "+
			"`timetattle status`'s question, and this command does not answer it",
			c.LastHarvest.Local().Format("2006-01-02 15:04"), round(now.Sub(c.LastHarvest))), cols)
	}

	orientNext(out, c, cols)
	orientBreakdown(out, c, cols)

	if c.NoPath > 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "", fmt.Sprintf("%s carry NO PATH AT ALL. knowledgeC and Biome "+
			"observe the machine rather than a file, so no registration can ever reach them, "+
			"however many directories are blessed. They are counted here and nowhere above.",
			plural(c.NoPath, "observation")), cols)
	}

	fmt.Fprint(out, "\n")
	wrap(out, "", "EVERY FIGURE ABOVE IS A COUNT OF OBSERVATIONS AND NOT TIME. Sources emit rows "+
		"at wildly different rates — an hour of Claude transcript outweighs a week of git "+
		"commits — so a big count is not a lot of work. None of these went through the "+
		"clustering and none is an estimate of hours; `timetattle metrics` is.", cols)
}

// orientNext names the single remedy that would clear the most, or says there
// is none.
//
// Ranking the three actionable remedies against each other by observation count
// is the one comparison that is honest without going through the clustering:
// they are counts of the same thing, measured the same way. Ranking them by
// importance would need a judgement this command has no basis for.
func orientNext(out io.Writer, c orientCensus, cols int) {
	best := orientPending
	n := c.Pending
	if c.Stranded > n {
		best, n = orientStranded, c.Stranded
	}
	if c.Registerable > n {
		best, n = orientRegisterable, c.Registerable
	}
	if n == 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "", "NOTHING TO DO. Every observation carrying a path is attributed to a "+
			"project, or carries a path no action can reach. See the breakdown below.", cols)
		return
	}

	worth := fmt.Sprintf("%s, %s,", plural(n, "observation"), orientShare(n, c.Observations))
	switch best {
	case orientPending:
		fmt.Fprint(out, "\nNEXT  timetattle harvest\n")
		wrap(out, "      ", worth+" already sit inside a registered directory and "+
			"attribute the moment the resolution pass runs. It runs ONLY as a phase of "+
			"harvest, so nothing else clears them, and until it does every per-project "+
			"figure metrics prints is understated.", cols)
	case orientStranded:
		top := c.StrandedRoots[0]
		fmt.Fprintf(out, "\nNEXT  timetattle consolidate -former %s \\\n"+
			"            -path <the directory that work lives in now>\n", top.Dir)
		wrap(out, "      ", fmt.Sprintf("%s name paths that are gone from disk, %d of "+
			"them under the one above. No marker can be written into a directory that does "+
			"not exist, so registering cannot reach these; declaring the rename can. Where "+
			"the work moved TO is not something this command can know, and if nothing owns "+
			"it any more there is nothing to declare.", worth, top.Observations), cols)
	case orientRegisterable:
		top := c.RegisterableRoots[0]
		fmt.Fprintf(out, "\nNEXT  timetattle register -dir %s \\\n"+
			"            -client <client> -project <project>\n", top.Dir)
		wrap(out, "      ", fmt.Sprintf("%s sit in directories that exist, are "+
			"unregistered, and that register would accept; %d of them under the one above. "+
			"Whether a directory is a PROJECT is a judgement this command cannot make — a "+
			"scratch directory is as registerable as a repository — so the count is what "+
			"says whether it earns a marker.", worth, top.Observations), cols)
	}
}

// orientShare renders what fraction of the store a figure is.
//
// A bare count has no scale — 325 observations is an afternoon or a rounding
// error depending on what else is in the store — and the share is the only
// honest way to say which without converting to time, which this command may
// not do. It is also what replaces a materiality threshold: rather than
// inventing a floor below which an action is not worth recommending, the action
// is stated with its share and the judgement is left where it belongs.
// A share that ROUNDS to zero is rendered "<0.1%" rather than "0.0%", because
// zero is a different claim from "smaller than one decimal place can show", and
// printing the second as the first is the same collapse this project keeps
// finding in its data.
func orientShare(n, total int) string {
	if total == 0 {
		return "of an empty store"
	}
	pct := 100 * float64(n) / float64(total)
	if n > 0 && pct < 0.05 {
		return "<0.1% of the store"
	}
	return fmt.Sprintf("%.1f%% of the store", pct)
}

// orientBreakdown prints the census, which reconciles exactly with the
// population it divides.
//
// Every class is printed, including the ones with nothing in them, and that is
// deliberate: a section that vanishes when its count is zero reads as "not
// measured" rather than as "measured, and none". This project has collapsed
// those two before.
func orientBreakdown(out io.Writer, c orientCensus, cols int) {
	if c.WithPath == 0 {
		fmt.Fprint(out, "\nNo observation in this store carries a path that is unattributed.\n")
		return
	}
	fmt.Fprint(out, "\n")
	wrap(out, "", fmt.Sprintf("UNATTRIBUTED, CARRYING A PATH: %s, %s. Five different "+
		"things have to happen to these, and each count below is what ONE action against "+
		"it is worth:",
		plural(c.WithPath, "observation"), orientShare(c.WithPath, c.Observations)), cols)

	fmt.Fprintf(out, "  %8d  inside a registered directory already   timetattle harvest\n", c.Pending)

	fmt.Fprintf(out, "  %8d  the path is gone from disk              timetattle consolidate\n", c.Stranded)
	orientRoots(out, c.StrandedRoots)

	fmt.Fprintf(out, "  %8d  a directory register would accept       timetattle register\n", c.Registerable)
	orientRoots(out, c.RegisterableRoots)

	fmt.Fprintf(out, "  %8d  above a directory already registered    nothing\n"+
		"            a marker there would own every unregistered directory beneath it,\n"+
		"            and a wrong attribution is worse than none\n", c.Container)
	fmt.Fprintf(out, "  %8d  inside a shared application cache       nothing\n"+
		"            register refuses it, because the cache holds every client's work;\n"+
		"            the manifest's document-name join is what attributes these\n", c.Cache)
	if c.Unknown > 0 {
		fmt.Fprintf(out, "  %8d  COULD NOT BE CLASSIFIED\n"+
			"            nothing was learned about these, which is not the same as their\n"+
			"            having no remedy: %v\n", c.Unknown, c.UnknownWhy)
	}
}

// orientDirectories pluralises the one noun in this report that plural() gets
// wrong, since it appends an s and "directorys" is not a word.
func orientDirectories(n int) string {
	if n == 1 {
		return "directory"
	}
	return "directories"
}

// orientRoots names the directories one action would be taken against, most
// valuable first, and never more than a screen of them.
func orientRoots(out io.Writer, roots []orientCandidate) {
	const most = 3
	for i, r := range roots {
		if i == most {
			fmt.Fprintf(out, "            and %d more\n", len(roots)-most)
			return
		}
		fmt.Fprintf(out, "            %s (%d)\n", r.Dir, r.Observations)
	}
}
