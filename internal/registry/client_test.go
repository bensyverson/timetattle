package registry_test

import (
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// ONE PROJECT NAME BELONGS TO ONE CLIENT, ACROSS EVERY DIRECTORY THAT NAMES IT.
//
// Register has always checked for a conflict WITHIN one directory: re-blessing
// a directory that already names a different client or project is refused
// unless Force. It has never checked ACROSS directories, so nothing stopped
//
//	/Users/you/git/a  ->  client=acme,   project=website
//	/Users/you/git/b  ->  client=globex, project=website
//
// from both registering cleanly. Both then write project='website' onto their
// events, and every report that groups by project — which is every report
// `timetattle metrics` prints today — merges two clients' hours into one row
// with nothing to say it happened.
//
// That is a defect in project-level reporting on its own, independent of the
// client dimension: metrics has always keyed its per-project table on the name
// alone. The rule below is what makes the name mean one thing.
//
// Names here are invented. The live client names are a confidentiality matter
// (task 5nCl4) and none of them appears in this repository.

// blessed registers dir and returns the marker, failing the test if it could
// not, so a fixture's setup does not read like the thing under test.
func blessed(t *testing.T, dir, client, project string, among []*registry.Marker) *registry.Marker {
	t.Helper()
	m, err := registry.Register(registry.Registration{
		Dir: dir, Client: client, Project: project, Among: among,
	})
	if err != nil {
		t.Fatalf("Register(%s, %s/%s): %v", dir, client, project, err)
	}
	return m
}

// The refusal itself. A second client claiming a project name another
// directory already holds is not a registration this tool can honour, because
// there is no answer it could then give to "how many hours did acme get".
func TestRegisterRefusesASecondClientClaimingAnExistingProject(t *testing.T) {
	first := blessed(t, t.TempDir(), "acme", "website", nil)

	_, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "globex", Project: "website",
		Among: []*registry.Marker{first},
	})
	if err == nil {
		t.Fatal("Register() let a second client claim the project name \"website\" = nil error; " +
			"the project name no longer identifies one body of work for one client")
	}
}

// THE MESSAGE IS PART OF THE FEATURE. Three commits in this repository have gone
// on messages that named a conclusion instead of the missing thing. A refusal
// here has to say which OTHER directory holds that project and under which
// client, because the user cannot act on it otherwise without going and
// grepping the filesystem for marker files.
func TestTheRefusalNamesTheDirectoryAndClientTheProjectAlreadyBelongsTo(t *testing.T) {
	held := t.TempDir()
	first := blessed(t, held, "acme", "website", nil)

	_, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "globex", Project: "website",
		Among: []*registry.Marker{first},
	})
	if err == nil {
		t.Fatal("Register() = nil error, want a refusal")
	}
	for _, want := range []string{held, "acme", "website"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so the user cannot act on it without "+
				"grepping for marker files: %v", want, err)
		}
	}
}

// THE CASE THE RULE MUST NOT BREAK, and it is a feature rather than an edge:
// a project spanning several directories — a repository plus a shared asset
// folder plus a scratch render directory — needs a marker in each one, and
// Register's own doc says calling it again with the SAME client and project
// blesses the second directory too. The new check must see that as agreement,
// not as a collision.
func TestASecondDirectoryForTheSameClientAndProjectIsStillBlessed(t *testing.T) {
	first := blessed(t, t.TempDir(), "acme", "website", nil)

	second := blessed(t, t.TempDir(), "acme", "website", []*registry.Marker{first})
	if second.Client != "acme" || second.Project != "website" {
		t.Errorf("the second directory registered as %s / %s, want acme / website",
			second.Client, second.Project)
	}
}

// FORCE OVERRIDES A CROSS-DIRECTORY CONFLICT, AND THE MESSAGE NAMES WHAT IS NOW
// OUT OF STEP.
//
// It has to. A client rename is performed by re-registering the directories
// that name it, one at a time, and the FIRST of those calls collides with all
// the others — so a refusal that Force could not override would leave a rename
// with no route through the tool at all, and would drive a user to hand-editing
// marker files, which is the one thing no check here can see.
//
// The transient state that produces is real and is the reason this decision
// belongs beside the storage decision: halfway through renaming a client that
// spans 33 directories, 17 markers say the new name and 16 say the old. The
// store has to be able to REPRESENT that, and each event has to keep the client
// its own directory named.
func TestForceReassignsThisDirectoryAndNamesTheOnesNowOutOfStep(t *testing.T) {
	stillOld := t.TempDir()
	first := blessed(t, stillOld, "acme", "website", nil)

	dir := t.TempDir()
	second := blessed(t, dir, "acme", "website", []*registry.Marker{first})

	m, err := registry.Register(registry.Registration{
		Dir: dir, Client: "acme-holdings", Project: "website", Force: true,
		Among: []*registry.Marker{first, second},
	})
	if err != nil {
		t.Fatalf("Force did not override a cross-directory client conflict: %v; a client rename "+
			"then has no route through this tool at all", err)
	}
	if m.Client != "acme-holdings" {
		t.Errorf("the directory registered to %s, want acme-holdings", m.Client)
	}
}

// THE SCOPE OF THE RULE, PINNED, because the argument for storing the client on
// the event row originally overstated it and this is the test that says by how
// much.
//
// The claim was that renaming a client forces a cross-directory collision,
// because the first directory re-registered disagrees with all the others. That
// is FALSE whenever the client's directories carry DISTINCT project names, and
// on the live registry all 38 do — see client_realdata_test.go. The check is
// project-scoped, exactly as specified: it skips every marker whose project
// differs, so re-registering the only directory naming a project finds nothing
// to disagree with.
//
// Force is still needed, but for the PER-DIRECTORY refusal that predates all of
// this and that a pivot table would face identically. The two refusals are
// distinguished here by their messages, because "Force was required" is true of
// both and says nothing about which rule fired.
func TestRenamingAClientWhoseProjectsAreDistinctNeverCollidesAcrossDirectories(t *testing.T) {
	loom := t.TempDir()
	kiln := t.TempDir()
	first := blessed(t, loom, "atelier", "loom", nil)
	second := blessed(t, kiln, "atelier", "kiln", []*registry.Marker{first})
	among := []*registry.Marker{first, second}

	// Without Force the refusal is the PER-DIRECTORY one, and it must not be
	// the cross-directory one: nothing else names project "loom".
	_, err := registry.Register(registry.Registration{
		Dir: loom, Client: "atelier-studio", Project: "loom", Among: among,
	})
	if err == nil {
		t.Fatal("re-registering a directory to a new client without Force = nil error")
	}
	if !strings.Contains(err.Error(), "is already registered to") {
		t.Errorf("the refusal is not the per-directory one: %v", err)
	}
	if strings.Contains(err.Error(), "already held for a different client by") {
		t.Errorf("a cross-directory conflict was reported for a project no other directory "+
			"names; the rule is project-scoped and this client's projects are distinct: %v", err)
	}

	// The rename itself: one directory at a time, Force for the per-directory
	// rule, no cross-directory collision at any point.
	renamed := blessedForce(t, loom, "atelier-studio", "loom", among)
	among = []*registry.Marker{renamed, second}

	// THE HALFWAY STATE. One directory names the new client and one the old.
	// Registering a further directory under either client succeeds, provided it
	// names a project of its own — which is the whole of what the rule asks.
	if _, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "atelier-studio", Project: "press", Among: among,
	}); err != nil {
		t.Errorf("registering a new project under the half-renamed client was refused: %v", err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "atelier", Project: "folio", Among: among,
	}); err != nil {
		t.Errorf("registering a new project under the not-yet-renamed client was refused: %v", err)
	}

	// And finishing the rename is the same one-directory move, still with no
	// cross-directory conflict to override.
	blessedForce(t, kiln, "atelier-studio", "kiln", among)
}

// blessedForce is blessed with Force, for the per-directory refusal that any
// reassignment has to get past.
func blessedForce(t *testing.T, dir, client, project string, among []*registry.Marker) *registry.Marker {
	t.Helper()
	m, err := registry.Register(registry.Registration{
		Dir: dir, Client: client, Project: project, Force: true, Among: among,
	})
	if err != nil {
		t.Fatalf("Register(%s, %s/%s, Force): %v", dir, client, project, err)
	}
	return m
}

// THE NARROW CASE THAT DOES PRODUCE AN AMBIGUOUS PROJECT, and it is the
// many-directories-one-project arrangement the registry documents and supports:
// a repository plus a shared asset folder plus a scratch render directory, all
// naming one project. Rename that client one directory at a time and the
// project genuinely maps to two clients partway through.
//
// This is what the storage argument actually rests on — not renaming in
// general. It does not describe any client in the live registry today, where
// every project name is held by exactly one directory.
func TestRenamingAProjectHeldBySeveralDirectoriesIsAmbiguousPartway(t *testing.T) {
	repo := t.TempDir()
	assets := t.TempDir()
	first := blessed(t, repo, "atelier", "loom", nil)
	second := blessed(t, assets, "atelier", "loom", []*registry.Marker{first})
	among := []*registry.Marker{first, second}

	// Rename the first of the two. Force is needed twice over here: for the
	// per-directory refusal, AND for the cross-directory one, because the other
	// directory still names project "loom" for the old client.
	renamed := blessedForce(t, repo, "atelier-studio", "loom", among)
	among = []*registry.Marker{renamed, second}

	// Partway through, one project name is held by two clients. A fresh
	// directory naming it is refused against BOTH, which is the state a
	// project-keyed mapping has no row shape for.
	_, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "bindery", Project: "loom", Among: among,
	})
	if err == nil {
		t.Fatal("project \"loom\" is held by two clients and a third claim was accepted")
	}
	for _, want := range []string{"atelier-studio", "atelier"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not show the split: %v", want, err)
		}
	}
}

// A conflict against a directory that is no longer registered is not a
// conflict. Among is what the caller enumerated; a marker that has since been
// deleted is absent from it, and the check must rest on that rather than on a
// remembered list. Passing nil is the plain per-directory check and must stay
// exactly what it always was, because every existing caller passes nothing.
func TestWithNoOtherRegistrationsTheCheckIsTheOneItAlwaysWas(t *testing.T) {
	if _, err := registry.Register(registry.Registration{
		Dir: t.TempDir(), Client: "globex", Project: "website",
	}); err != nil {
		t.Errorf("Register with no Among = %v, want the per-directory check alone", err)
	}
}
