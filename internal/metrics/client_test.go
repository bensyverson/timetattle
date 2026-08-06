package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// A CLIENT ROLL-UP IS A UNION AND NEVER A SUM, and these are the cases that
// separate the two.
//
// The distinction is invisible on a client whose projects are 1:1 with it —
// summing one row gives the same answer as taking the union of one span — and
// every client in the live registry is 1:1 except one, which spans 33 projects.
// So the whole of the risk sits on that one shape, and a fixture that does not
// reproduce it proves nothing. Every fixture here therefore gives one client
// SEVERAL projects whose evidence OVERLAPS in time.
//
// Names are invented. The live client names are a confidentiality matter (task
// 5nCl4) and none of them appears here.

// clientPoint is an attributed instant carrying both levels of identity, which
// is what the attribution pass writes: one marker resolves to one client and
// one project, together, in one call.
func clientPoint(when time.Time, client, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Client: client, Project: project}, "claude-transcripts")
	e.Locator = "/Users/ben/git/" + project
	return e
}

func clientOf(t *testing.T, r metrics.Report, name string) metrics.Client {
	t.Helper()
	c, ok := r.Client(name)
	if !ok {
		t.Fatalf("no client %q in the report; it has %d", name, len(r.Clients))
	}
	return c
}

// CRITERION 4i3, and the reason it is not a formality. Three projects of one
// client work through the same sitting, so their engaged spans overlap almost
// entirely. ABSOLUTE engaged time bills a shared minute in full to every
// project active in it — that is what makes it the invoice figure — so adding
// the three project rows up credits the client three times for the same clock.
//
// The client's own figure has to be the UNION of its projects' active spans,
// re-derived from the segments rather than accumulated from the rows above it.
func TestAClientsEngagedTimeIsTheUnionOfItsProjectsAndNeverTheirSum(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 3), "atelier", "kiln"),
		clientPoint(at(9, 6), "atelier", "press"),
		clientPoint(at(9, 9), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
	})

	c := clientOf(t, r, "atelier")

	var summed time.Duration
	for _, p := range r.Projects {
		summed += p.Engaged.Total
	}
	if summed <= c.Engaged.Total {
		t.Fatalf("the three projects sum to %v and the client rolls up to %v; the fixture does not "+
			"overlap and so cannot tell a union from a sum", summed, c.Engaged.Total)
	}

	// The union, re-derived independently: every segment in which ANY of this
	// client's projects was active, summed once.
	var union time.Duration
	for _, seg := range r.Segments {
		for _, name := range seg.Projects {
			if name == "loom" || name == "kiln" || name == "press" {
				union += seg.Duration()
				break
			}
		}
	}
	if c.Engaged.Total != union {
		t.Errorf("the client is credited %v against %v of union across its projects' segments; "+
			"a roll-up that is not the union is double-counting shared clock", c.Engaged.Total, union)
	}
}

// The client names the projects it rolled up, so that a figure one level up can
// be traced to the rows it came from rather than being taken on trust.
func TestAClientNamesTheProjectsItRolledUp(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 3), "atelier", "kiln"),
		clientPoint(at(9, 6), "atelier", "press"),
		clientPoint(at(14, 0), "bindery", "folio"),
	})

	if got := clientOf(t, r, "atelier").Projects; len(got) != 3 ||
		got[0] != "kiln" || got[1] != "loom" || got[2] != "press" {
		t.Errorf("atelier rolled up %v, want [kiln loom press] sorted", got)
	}
	if got := clientOf(t, r, "bindery").Projects; len(got) != 1 || got[0] != "folio" {
		t.Errorf("bindery rolled up %v, want [folio]", got)
	}
}

// ALLOCATION SPLITS BY CLIENT, NOT BY PROJECT, and the two give different
// answers on exactly the shape criterion 4i3 is about.
//
// One minute in which two of atelier's projects and one of bindery's are all
// active divides three ways by project — atelier's side taking two thirds — and
// two ways by client. Allocation is the CAPACITY figure, and capacity asks how
// the day was split between the people being worked for; a client with more
// repositories does not thereby consume more of the day.
func TestClientAllocationSplitsByClientAndNotByProject(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 0), "atelier", "kiln"),
		clientPoint(at(9, 0), "bindery", "folio"),
	})

	atelier, bindery := clientOf(t, r, "atelier"), clientOf(t, r, "bindery")
	if atelier.Allocated != bindery.Allocated {
		t.Errorf("atelier is allocated %v and bindery %v; two clients active across the same clock "+
			"must halve it however many repositories each keeps", atelier.Allocated, bindery.Allocated)
	}

	var atelierProjects time.Duration
	for _, p := range r.Projects {
		if p.Name == "loom" || p.Name == "kiln" {
			atelierProjects += p.Allocated
		}
	}
	if atelierProjects == atelier.Allocated {
		t.Errorf("atelier's client allocation (%v) equals the sum of its projects' (%v); the "+
			"per-project split was reused instead of a per-client one, and a client with more "+
			"repositories would read as consuming more of the day", atelier.Allocated, atelierProjects)
	}
}

// Allocation's defining property, one level up: the shares sum to EXACTLY the
// presence they were split from, including the nanoseconds an even division
// would drop. The project-level version of this is
// TestAllocationSharesSumExactlyToTheAttributedPresence; a second divisor is a
// second place the remainder can be lost.
func TestClientAllocationSharesSumExactlyToTheAttributedPresence(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 2), "atelier", "kiln"),
		clientPoint(at(9, 4), "bindery", "folio"),
		clientPoint(at(14, 0), "bindery", "quire"),
		clientPoint(at(14, 1), "atelier", "press"),
	})

	var byClient, byProject time.Duration
	for _, c := range r.Clients {
		byClient += c.Allocated
	}
	for _, p := range r.Projects {
		byProject += p.Allocated
	}
	attributed := r.Presence.Total - r.Unattributed.Presence.Total

	if byClient != attributed {
		t.Errorf("the client shares come to %v against %v of attributed presence, off by %v",
			byClient, attributed, byClient-attributed)
	}
	// The two divisors partition the same clock, so they must agree on the
	// total while disagreeing on the split. A discrepancy here is a stretch
	// one level counted and the other did not.
	if byClient != byProject {
		t.Errorf("the client shares come to %v and the project shares to %v; both divide the same "+
			"attributed presence and must agree on its total", byClient, byProject)
	}
}

// No client may be credited more clock than the presence it sits inside. The
// project-level guard is TestNoProjectIsCreditedMoreThanThePresenceItSitsIn;
// the union that produces a client's figure is a second computation and needs
// its own.
func TestNoClientIsCreditedMoreThanThePresenceItSitsIn(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 3), "atelier", "kiln"),
		clientPoint(at(9, 6), "atelier", "press"),
		clientPoint(at(9, 9), "bindery", "folio"),
	})

	for _, c := range r.Clients {
		if c.Engaged.Total > r.Presence.Total {
			t.Errorf("%s is engaged %v inside %v of presence", c.Name, c.Engaged.Total, r.Presence.Total)
		}
		if c.Engaged.Observed > c.Engaged.Total {
			t.Errorf("%s observed %v of %v engaged", c.Name, c.Engaged.Observed, c.Engaged.Total)
		}
	}
}

// A client and a project may carry the SAME NAME, and today one does: the live
// registry has a client and a project both named for this tool. Keying both
// roll-ups out of one map would silently merge them into a row that is neither.
func TestAClientAndAProjectSharingANameAreNotMerged(t *testing.T) {
	r := compute(t, []source.Event{
		clientPoint(at(9, 0), "quarry", "quarry"),
		clientPoint(at(9, 3), "quarry", "seam"),
		clientPoint(at(14, 0), "bindery", "quarry"),
	})

	c := clientOf(t, r, "quarry")
	if len(c.Projects) != 2 {
		t.Errorf("client quarry rolled up %v; it owns the projects quarry and seam and nothing "+
			"of bindery's", c.Projects)
	}
	p, ok := r.Project("quarry")
	if !ok {
		t.Fatal("no project quarry in the report")
	}
	if p.Engaged.Total == c.Engaged.Total && len(r.Clients) > 0 {
		t.Errorf("project quarry and client quarry are both %v; the two levels share a name and "+
			"were merged", p.Engaged.Total)
	}
}

// Evidence attributed to a project but carrying no client is neither a client's
// nor a defect this package may invent an answer for. It must not be filed
// under an empty-named client, which would read in a report as a real one.
func TestEvidenceWithNoClientProducesNoNamelessClientRow(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(9, 0), "loom"),
		clientPoint(at(9, 3), "atelier", "kiln"),
	})

	for _, c := range r.Clients {
		if c.Name == "" {
			t.Errorf("the report carries a client with no name, across %v; a project whose client "+
				"was never resolved is not a client", c.Engaged.Total)
		}
	}
	if _, ok := r.Project("loom"); !ok {
		t.Error("the clientless project vanished from the report entirely")
	}
}
