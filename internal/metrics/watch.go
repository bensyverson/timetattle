package metrics

// Reporting what a long computation is doing, and how long each part of it
// took.
//
// # Why this is here rather than a stopwatch in the command
//
// The expensive stages are all INSIDE Corrected — the clustering, the two
// re-clusterings underneath the per-project and per-client rows, and the
// segmentation. A caller holding a stopwatch outside can only time the whole
// call, which is precisely the measurement that was already available and
// already useless: it says a run takes some number of seconds and cannot say
// which stage they went to. Every figure this package has ever recorded about
// its own cost before this existed came from an agent temporarily editing
// metrics.go, which is a measurement nobody else can reproduce and nobody can
// re-run after a change.
//
// # A watcher may never change an answer
//
// It is handed no events, no spans and no report; it is told a phase name, a
// count and nothing else, and it cannot reach anything it could perturb. That
// is deliberate — instrumentation is the code added under time pressure and
// reviewed least — and internal/metrics' TestAWatcherChangesNoFigureInTheReport
// asserts the property directly, by computing the same corpus with and without
// one and comparing every field.
//
// # Phase is a bare string so a caller can add its own
//
// cmd/timetattle's metrics command times four stages this package never sees —
// the store read, the exclusion pass, the client stamp and the formatting — and
// they belong in the same table as the four below, because a reader asking
// where a run goes wants one table and not two. Phase is therefore an open
// vocabulary rather than an enumeration.

// Phase names one stage of a metrics computation.
type Phase string

const (
	// PhaseCluster is the global clustering: every event, once, into the
	// sittings every other figure is an intersection against.
	PhaseCluster Phase = "clustering"
	// PhaseGroup is the pass that files each event under its project and its
	// client, and separates the presence readings out from the evidence.
	PhaseGroup Phase = "grouping evidence"
	// PhaseProjects is the per-project pass: each project's OWN evidence
	// re-clustered under the same parameters. It steps once per project.
	PhaseProjects Phase = "per-project time"
	// PhaseClients is the client roll-up, which re-clusters for the reason
	// Client's doc comment gives — a client's hours are the UNION of its
	// projects' spans and never their sum, so they cannot be added up from the
	// rows below. It steps once per client.
	PhaseClients Phase = "client roll-up"
	// PhaseFamilies is the activity mix: each KIND of evidence stream's own
	// observations re-clustered, for the reason families.go gives — a family's
	// clock is a union and cannot be added up from anything already computed.
	// It steps once per family, and there are at most eight of them however
	// many projects and clients the store holds.
	PhaseFamilies Phase = "activity mix"
	// PhaseSegments is carving presence at every boundary and summing every
	// figure in the report out of the pieces.
	PhaseSegments Phase = "segmenting"
)

// Watcher is told which phase of a computation is underway and how far through
// it is.
//
// Every phase that Begins also Dones, including on the paths that return early,
// so a display can trust that an open phase is a phase still running rather
// than one that ended without saying so. A phase reports its own progress with
// Step, counting up to the steps it announced; a phase that cannot say how much
// work it has announces zero steps and never calls Step.
//
// A nil Watcher is the ordinary case. Compute and Corrected both pass one, and
// every call site below is guarded, so an uninstrumented run does no work at
// all for this.
type Watcher interface {
	// Begin says a phase has started, and how many units of work it will get
	// through. Zero means the phase cannot count itself.
	Begin(p Phase, steps int)
	// Step says done units of the phase are finished. It counts absolutely
	// rather than incrementally, so a dropped call cannot make the total drift.
	Step(p Phase, done int)
	// Done says the phase has finished.
	Done(p Phase)
}

// watch wraps a Watcher that may be nil, so that the computation reads as
// though one is always there. Every method is a no-op on a nil watcher, and the
// nil check sits here once rather than at each of the dozen call sites.
type watch struct{ w Watcher }

func (v watch) begin(p Phase, steps int) {
	if v.w != nil {
		v.w.Begin(p, steps)
	}
}

func (v watch) step(p Phase, done int) {
	if v.w != nil {
		v.w.Step(p, done)
	}
}

func (v watch) done(p Phase) {
	if v.w != nil {
		v.w.Done(p)
	}
}
