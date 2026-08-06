package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// What the BARE command says: the project the current directory belongs to,
// and then the usage it has always printed.
//
// # Why both, and why the usage never disappears
//
// `timetattle` with no arguments is the one invocation somebody makes before
// they know anything, and it is also the one an agent makes to find out what is
// here. Answering the directory's question INSTEAD of printing the usage would
// make the bare command mean one thing inside a registered project and a
// different thing outside one — the worst property a bare command can have. So
// it does both, always, in that order: the answer if there is one, then the
// map.
//
// It does not overload a verb for the same reason. `metrics` says what the
// numbers are, `status` says whether collection is working, `orient` says what
// to do; none of them grew a new meaning, and the bare command took the one job
// none of them had.
//
// # It must be CHEAP, and that is what shapes the whole of it
//
// A person typing `timetattle` to see the command list may not wait fifteen
// seconds, and fifteen seconds is what a report costs: `timetattle metrics`
// over the fourteen days to 2026-08-04 takes 14.9s on the live store, of which
// the event read is 0.9s and the rest is clustering 212,094 observations to
// establish presence.
//
// So this does not establish presence. It reads the derivation `sessionize`
// already stored — 1,370 sittings in 13ms — and measures the one project
// against it with internal/metrics.Against, which reduces one name through the
// same code a whole report reduces every name through. The consequence is
// stated in the output rather than hidden: the figures are AS OF THE STORED
// DERIVATION, they name the instant it was computed, and a derivation computed
// under different parameters from the ones configured now is called stale by
// name.
//
// # Three things it refuses to collapse
//
// A directory that is not registered, a project with no observations in the
// window, and a store with no derivation in it produce three different
// paragraphs. All three would otherwise print as an absence of hours, and this
// project has shipped the collapsed version of that distinction three times.

// headlineWindow is how far back the bare command looks.
//
// A fortnight rather than the whole corpus, because the question the bare
// command answers is "what is going on here", and because the figure has to
// arrive quickly. It is named in the output on every run: a bare "32.8h" that
// does not say over what is not a measurement.
const headlineWindow = 14 * 24 * time.Hour

// bareTo prints the headline for dir and then the usage.
//
// It never fails on account of the world being empty. A missing store, an
// underived one and an unregistered directory are all ANSWERS here, and each is
// printed as one; an error would take the usage down with it, which is the one
// thing this invocation must always deliver.
func bareTo(out io.Writer, dir, storePath, configPath string) error {
	cols := columnsOf(out)
	if err := headline(clipTo(out, cols), dir, storePath, configPath, cols); err != nil {
		return err
	}
	fmt.Fprint(out, "\n")
	usageTo(out)
	return nil
}

func headline(out io.Writer, dir, storePath, configPath string, cols int) error {
	marker, found, err := registry.Resolve(dir)
	if err != nil {
		return err
	}
	if !found {
		// SAID OUT LOUD, because no headline and an empty headline are
		// different facts and only one of them is about this directory.
		wrap(out, "", fmt.Sprintf("%s is not a registered directory, and neither is anything above "+
			"it, so there is no project to report here. That is not a project with no hours in it. "+
			"`timetattle register -h` says how a directory is blessed with a client and a project; "+
			"`timetattle metrics` reports every project that already is.", dir), cols)
		return nil
	}

	// The store is opened ONCE, before anything is printed, because the client
	// comes out of it too — a client association lives in the client-project
	// log and never on the marker alone. Opening it twice would read one
	// database through two connections for one line of output.
	st, err := store.OpenReadOnly(storePath)
	if err != nil {
		fmt.Fprintf(out, "%s\n  %s\n", marker.Project, marker.Path)
		wrap(out, "  ", fmt.Sprintf("no store could be read at %s, so nothing can be reported "+
			"about this project yet — which is a fact about this machine and not about the work. "+
			"`timetattle harvest` creates one. (%v)", storePath, err), cols)
		return nil
	}
	defer st.Close()

	fmt.Fprintf(out, "%s", marker.Project)
	client, why := headlineClient(st, marker)
	if client != "" {
		fmt.Fprintf(out, " for %s", client)
	}
	fmt.Fprintf(out, "\n  %s\n", marker.Path)
	if why != "" {
		wrap(out, "  ", why, cols)
	}

	to := time.Now()
	from := to.Add(-headlineWindow)
	fmt.Fprintf(out, "  over the %d days to %s:\n",
		int(headlineWindow/(24*time.Hour)), to.Format(time.RFC3339))

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	p := sessions.Params{
		Gap:             cfg.Sessions.Gap.Duration(),
		LeadIn:          cfg.Sessions.LeadIn.Duration(),
		TailOut:         cfg.Sessions.TailOut.Duration(),
		PresenceSources: cfg.Sessions.PresenceSources,
		IgnoreSources:   cfg.Sessions.IgnoreSources,
	}

	ctx := context.Background()
	// THE STORED DERIVATION, not a fresh one. See the file comment: clustering
	// the corpus is what a report costs, and the bare command may not cost it.
	derived, err := st.Sittings(ctx, from, to)
	if err != nil {
		return err
	}
	if derived.ComputedAt.IsZero() {
		wrap(out, "    ", "nothing has been derived from this store yet, so there are no sittings to "+
			"measure against. That is not a fortnight in which no work happened. Run `timetattle "+
			"sessionize` to cluster the observations into sittings, and this will have an answer.", cols)
		return nil
	}

	events, err := st.Events(ctx, store.Filter{From: from, To: to})
	if err != nil {
		return err
	}
	excludes, err := sessionExcludesFrom(cfg, "", false)
	if err != nil {
		return err
	}
	events, _ = sessions.Retain(events, excludes)

	presence := make([]timespan.Span, 0, len(derived.Sittings))
	for _, s := range derived.Sittings {
		presence = append(presence, s.Span())
	}
	w, err := metrics.Against(marker.Project, events, presence, p)
	if err != nil {
		return err
	}

	if w.Events == 0 {
		wrap(out, "    ", fmt.Sprintf("no observation of this project at all, across %v of presence "+
			"in the window. That is not a figure of zero hours: nothing carrying this project's name "+
			"was recorded here. `timetattle status` says whether collection was working.",
			metricsHours(w.Presence)), cols)
	} else {
		fmt.Fprintf(out, "    engaged  %8s absolute, across %s of its own, on %s\n",
			metricsHours(w.Project.Engaged.Total), plural(w.Sittings.Count, "sitting"),
			plural(w.Events, "observation"))
		fmt.Fprintf(out, "    presence %8s at the machine, of which this project reaches %.0f%%\n",
			metricsHours(w.Presence), 100*shareOf(w.Project.Engaged.Total, w.Presence))
		fmt.Fprint(out, "\n")
		wrap(out, "  ", "That percentage is NOT an allocated share. Absolute engaged time counts a "+
			"minute two projects were both active in fully for each, so it cannot be added up "+
			"across projects; `timetattle metrics -project` computes the allocated split, over "+
			"every project at once, which is the only way it can be computed.", cols)
	}

	fmt.Fprint(out, "\n")
	stale := ""
	if !derived.Params.Equal(p) {
		stale = " STALE: those are not the parameters configured now, so re-run `timetattle sessionize`"
	}
	wrap(out, "  ", fmt.Sprintf("From the derivation stored on %s at gap %v, lead-in %v, tail-out %v.%s "+
		"Nothing here re-clusters the corpus, which is what makes it answer at once; `timetattle "+
		"metrics` recomputes from the observations and is the figure to quote.",
		derived.ComputedAt.Format(time.RFC3339), compactDuration(derived.Params.Gap),
		compactDuration(derived.Params.LeadIn), compactDuration(derived.Params.TailOut), stale), cols)

	// THE SNAPSHOT'S EDGE, MEASURED RATHER THAN INFERRED. The derivation
	// clustered what was in the store when sessionize ran; anything harvested
	// since is in no sitting, so it contributes nothing to the figures above and
	// the most recent work reads as no work. Counting the observations that are
	// actually newer says how much is being left out, where comparing the
	// derivation's timestamp with the window's end would only say that something
	// might be.
	if newer := countNewerThan(events, derived.ComputedAt); newer > 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", fmt.Sprintf("%s in this window %s newer than the derivation, and therefore "+
			"in no sitting at all: that evidence adds nothing to the hours above, and the stretch "+
			"it covers reads here as no work rather than as work not yet clustered. Re-run "+
			"`timetattle sessionize` to fold it in.", plural(newer, "observation"),
			map[bool]string{true: "are", false: "is"}[newer != 1]), cols)
	}
	return nil
}

// countNewerThan is how many observations arrived after the derivation was
// computed. It counts the OBSERVATIONS rather than the clock they cover,
// because clock they cover is exactly what has not been derived.
func countNewerThan(events []source.Event, at time.Time) int {
	var n int
	for _, e := range events {
		if e.Anchor().After(at) {
			n++
		}
	}
	return n
}

// shareOf is a fraction, and nothing over nothing.
func shareOf(part, whole time.Duration) float64 {
	if whole == 0 {
		return 0
	}
	return part.Seconds() / whole.Seconds()
}

// headlineClient is who the CLIENT-PROJECT LOG says this project belonged to at
// this instant, and why it says nothing when it does not.
//
// THE MARKER IS NOT THE AUTHORITY and is deliberately not consulted for this. A
// client association is a fact about a moment in time, kept in an append-only
// log (migration 0012); the marker carries whatever was written the day the
// directory was blessed and has no history. Reading the marker would report the
// old client for work done after a reassignment, confidently.
func headlineClient(st *store.Store, marker *registry.Marker) (name, why string) {
	cm, err := st.ClientMap(context.Background(), time.Time{})
	if err != nil {
		return "", ""
	}
	if client, ok := cm.At(marker.Project, time.Now()); ok {
		return client, ""
	}
	// Three ways a project can have no client, and they call for three
	// different actions. See internal/clientmap.
	for _, c := range cm.Collisions() {
		if c.Project == marker.Project {
			return "", "No client is credited: two directories claim this project for different " +
				"clients, and its hours go to neither rather than to whichever registration was " +
				"written last. Re-register one of them, or give it a project name of its own."
		}
	}
	if !cm.Asserted(marker.Project) {
		return "", "No client has ever been asserted for this project, which is not the same as its " +
			"having none: `timetattle register` records one."
	}
	return "", "A client is asserted for this project, but not as of now — the assertion takes " +
		"effect later than this instant."
}

// bareDefaults resolves the store and config paths the bare command reads when
// it is not given them, which only the tests are.
func bareDefaults(storePath, configPath string) (string, string, error) {
	var err error
	if storePath == "" {
		if storePath, err = store.DefaultPath(); err != nil {
			return "", "", err
		}
	}
	if configPath == "" {
		if configPath, err = config.DefaultPath(); err != nil {
			return "", "", err
		}
	}
	return storePath, configPath, nil
}

// bare is the entry point main dispatches to.
func bare(stdout io.Writer, storePath, configPath string) error {
	storePath, configPath, err := bareDefaults(storePath, configPath)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return bareTo(stdout, dir, storePath, configPath)
}
