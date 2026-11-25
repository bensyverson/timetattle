package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// Every fixture here uses invented names, for the same reason main_test.go
// does: the derivation must not carry the real symbols in its own source, or
// the auditor could never be published beside the repository it audits.
//
// WHAT THESE TESTS ARE ABOUT. The gate's answer has only ever been as good as
// the list it was handed, and the list was a human's memory: four names
// somebody thought of on one afternoon. Fourteen registered directory names
// sat in the shipped tree for five days while the gate reported clean, because
// nothing connected the registry to the list. So the list is DERIVED from the
// two files on this machine that cannot go stale — the registry index and the
// config — and the states in which it CANNOT be derived are enumerated here
// beside the states in which it can, because "no names were found" and "no
// names exist" are different observations and the gate has to say which.

// --- the config: reference projects and engagements ---------------------------

// writeConfig writes a config file holding exactly the two sections the
// derivation reads, through json.Marshal rather than a string literal so the
// fixture cannot drift from the shape config.Load accepts.
func writeConfig(t *testing.T, refs map[string]string, clients, projects []string) string {
	t.Helper()
	rate := 1.0
	hours := 1.0
	eng := map[string]any{}
	mk := func(names []string) map[string]any {
		m := map[string]any{}
		for _, n := range names {
			m[n] = map[string]any{"rate": rate, "budgeted_hours": hours, "period": "week"}
		}
		return m
	}
	if len(clients) > 0 {
		eng["clients"] = mk(clients)
	}
	if len(projects) > 0 {
		eng["projects"] = mk(projects)
	}

	doc := map[string]any{}
	if refs != nil {
		doc["reference_projects"] = refs
	}
	if len(eng) > 0 {
		doc["engagements"] = eng
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal config fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

// The pseudonym is the APPROVED name — it is supposed to be all over the
// repository — and the path it maps to carries the real one. Hunting the
// pseudonym would fail the tree for having been scrubbed correctly.
func TestConfigNamesAreTheRealPathsAndNeverThePseudonyms(t *testing.T) {
	path := writeConfig(t, map[string]string{"acorn": "/opt/fixture/flonk"}, nil, nil)

	names, src := deriveConfigNames(path)

	if !slices.Contains(names, "flonk") {
		t.Errorf("deriveConfigNames = %v, want the real directory name behind the pseudonym", names)
	}
	if slices.Contains(names, "acorn") {
		t.Errorf("deriveConfigNames = %v, want the pseudonym itself left out", names)
	}
	if src.State != sourceAvailable {
		t.Errorf("source state = %v, want available", src.State)
	}
}

// The engagements section is the one place a REAL client name is written down
// on this machine outside the registry, and it is the section that also holds
// money. A name being billed is a client name by construction, so it needs no
// human to classify it.
func TestEngagementClientAndProjectNamesAreDerived(t *testing.T) {
	path := writeConfig(t, nil, []string{"Flonk"}, []string{"Wombat"})

	names, _ := deriveConfigNames(path)

	for _, want := range []string{"Flonk", "Wombat"} {
		if !slices.Contains(names, want) {
			t.Errorf("deriveConfigNames = %v, want it to contain %q", names, want)
		}
	}
}

// THE CENTRAL CASE. config.Load returns the DEFAULTS with a nil error when the
// file does not exist, so a caller that only checks the error cannot tell a
// machine with no config from a machine that configured nothing. Collapsing
// those two is how a gate reports clean while hunting an empty list.
func TestAnAbsentConfigIsObservedAsAbsentRatherThanEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.json")

	names, src := deriveConfigNames(missing)

	if len(names) != 0 {
		t.Errorf("deriveConfigNames = %v, want none", names)
	}
	if src.State != sourceAbsent {
		t.Fatalf("source state = %v, want absent — a missing file is not an empty one", src.State)
	}
	if !strings.Contains(src.Observed, missing) {
		t.Errorf("observation %q does not name the file that is missing", src.Observed)
	}
}

// The other side of the same coin: a config that exists and genuinely maps
// nothing is a COUNTED observation, and must not be reported as a missing
// precondition.
func TestAConfigThatMapsNothingIsAvailableAndCountsZero(t *testing.T) {
	path := writeConfig(t, map[string]string{}, nil, nil)

	names, src := deriveConfigNames(path)

	if len(names) != 0 {
		t.Errorf("deriveConfigNames = %v, want none", names)
	}
	if src.State != sourceAvailable {
		t.Errorf("source state = %v, want available: the file was read and says zero", src.State)
	}
	if !strings.Contains(src.Observed, "0") {
		t.Errorf("observation %q does not count what it found", src.Observed)
	}
}

// --- the registry: every directory that has ever been blessed -----------------

// registerDir builds a registered directory and records it in indexPath,
// returning the index path so a test can derive from it.
func registerDir(t *testing.T, indexPath, base, client, project string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: client, Project: project}); err != nil {
		t.Fatalf("register %s: %v", dir, err)
	}
	if err := registry.RecordInIndex(indexPath, dir); err != nil {
		t.Fatalf("record %s: %v", dir, err)
	}
}

// The registry is the thing that cannot go stale: registering a directory is
// how a new client arrives on this machine, so that is where the candidate
// names have to come from. All three strings a registration carries are
// candidates — the directory's own name, and the client and project it
// declares, which are exactly what a pasted `metrics` capture prints.
func TestRegistryCandidatesComeFromDirectoryNamesAndMarkers(t *testing.T) {
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	registerDir(t, index, "flonk", "Wombat Industries", "kestrel")

	got, src := deriveRegistryNames(index)

	for _, want := range []string{"flonk", "Wombat Industries", "kestrel"} {
		if !slices.Contains(got, want) {
			t.Errorf("deriveRegistryNames = %v, want it to contain %q", got, want)
		}
	}
	if src.State != sourceAvailable {
		t.Errorf("source state = %v, want available", src.State)
	}
}

// registry.List treats a missing index as an EMPTY index — the right tolerance
// for a fresh machine that has registered nothing, and the wrong one for a gate,
// which would then hunt an empty candidate list and call the tree clean. The
// derivation has to look at the file itself.
func TestAnAbsentRegistryIndexIsObservedAsAbsentRatherThanEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "registered-directories.json")

	got, src := deriveRegistryNames(missing)

	if len(got) != 0 {
		t.Errorf("deriveRegistryNames = %v, want none", got)
	}
	if src.State != sourceAbsent {
		t.Fatalf("source state = %v, want absent", src.State)
	}
	if !strings.Contains(src.Observed, missing) {
		t.Errorf("observation %q does not name the file that is missing", src.Observed)
	}
}

// --- classification -----------------------------------------------------------

func writeGateFile(t *testing.T, publishable, denied []string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"gate_version":      1,
		"publishable_names": publishable,
		"denied_literals":   denied,
	})
	if err != nil {
		t.Fatalf("marshal gate fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "publication-gate.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write gate fixture: %v", err)
	}
	return path
}

// Not every registered name is a client name — this repository's own directory
// is registered, and its name is in three hundred files. A ruling that a name
// is publishable has to be RECORDED somewhere the gate reads, or it is a
// human's memory again and the whole exercise repeats.
func TestAClassifiedNameIsNoLongerACandidate(t *testing.T) {
	gate := writeGateFile(t, []string{"flonk"}, nil)

	g, src := readGateFile(gate)

	if !g.publishable("flonk") {
		t.Errorf("readGateFile did not record flonk as classified publishable")
	}
	if g.publishable("wombat") {
		t.Errorf("readGateFile treated an unmentioned name as classified")
	}
	if src.State != sourceAvailable {
		t.Errorf("source state = %v, want available", src.State)
	}
}

// A machine that has classified nothing has not thereby declared everything
// safe. Absent means zero classifications, said out loud.
func TestAnAbsentGateFileClassifiesNothingAndSaysSo(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "publication-gate.json")

	g, src := readGateFile(missing)

	if g.publishable("flonk") {
		t.Errorf("an absent classification file classified something")
	}
	if src.State != sourceAbsent {
		t.Errorf("source state = %v, want absent", src.State)
	}
}

// --- denied literals ----------------------------------------------------------

// THE SECOND INSTANCE OF THE SAME DEFECT. The deny list was handed a
// placeholder copied out of an example command, and the gate dutifully reported
// a finding against the very redaction that placeholder documents. The tool was
// right and the input was wrong, so the input is what has to be checked.
func TestAPlaceholderShapedDenyLiteralIsRefusedRatherThanHunted(t *testing.T) {
	for _, in := range []string{"<team id>", "TEAMID", "your team", "Someone", "XXXXXXXXXX"} {
		if _, err := acceptDenyLiteral(in); err == nil {
			t.Errorf("acceptDenyLiteral(%q) accepted a placeholder; hunting it reports the redaction as the leak", in)
		}
	}
}

// A deny literal is matched as a plain substring anywhere in a line, so a short
// one is not a check, it is a noise generator: this module's own import path
// contains the account name.
func TestAShortDenyLiteralIsRefused(t *testing.T) {
	if _, err := acceptDenyLiteral("ben"); err == nil {
		t.Error("acceptDenyLiteral accepted a three-character literal, which matches inside ordinary identifiers")
	}
}

func TestARealLookingDenyLiteralIsAccepted(t *testing.T) {
	got, err := acceptDenyLiteral("  W0MB4TK3S7  ")
	if err != nil {
		t.Fatalf("acceptDenyLiteral = %v, want it accepted", err)
	}
	if got != "W0MB4TK3S7" {
		t.Errorf("acceptDenyLiteral = %q, want it trimmed", got)
	}
}

// The team ID and the certificate common name are both developer identifiers,
// both are on this machine's keychain, and neither has ever been derived: they
// were typed from memory into a -deny flag. `security find-identity` prints
// them in one line each.
func TestSigningIdentitiesYieldTheirCommonNameAndTeamID(t *testing.T) {
	out := `  1) AB12CD34 "Apple Development: Wombat Industries LLC (W0MB4TK3S7)"
  2) EF56GH78 "Developer ID Application: Someone (K3S7W0MB4T)"
     2 valid identities found`

	got := signingLiterals(out)

	for _, want := range []string{"Wombat Industries LLC", "W0MB4TK3S7", "K3S7W0MB4T"} {
		if !slices.Contains(got, want) {
			t.Errorf("signingLiterals = %v, want it to contain %q", got, want)
		}
	}
	// Row 2's common name is a documented placeholder and its team ID is not.
	// Vetting the row as a whole would have thrown the real literal away with
	// the placeholder beside it.
	if slices.Contains(got, "Someone") {
		t.Errorf("signingLiterals = %v, want the placeholder common name left out", got)
	}
}

// scripts/sign.sh documents the placeholder form of exactly this line, so the
// parser must not turn the documentation into a hunted literal.
func TestPlaceholderSigningIdentitiesYieldNoLiterals(t *testing.T) {
	out := `  1) ABC123 "Developer ID Application: Someone (TEAMID)"`

	if got := signingLiterals(out); len(got) != 0 {
		t.Errorf("signingLiterals = %v, want none from a documented placeholder", got)
	}
}

// --- the precondition ---------------------------------------------------------

// The whole point of the enumeration: a run that could not read a source must
// be able to say WHICH, and the gate must refuse to conclude anything from a
// list it could not build.
func TestUnavailableSourcesAreReportedIndividually(t *testing.T) {
	tmp := t.TempDir()
	d := derive(derivePaths{
		Config:   filepath.Join(tmp, "config.json"),
		Registry: filepath.Join(tmp, "registered-directories.json"),
		Gate:     filepath.Join(tmp, "publication-gate.json"),
		Identities: func() (string, error) {
			return "", errNoSigningTool
		},
	})

	missing := d.Unavailable()
	if len(missing) != 3 {
		t.Fatalf("Unavailable() = %v, want the config, the registry index and the signing identity", missing)
	}
	if d.Complete() {
		t.Error("Complete() reported a complete list built from three unreadable sources")
	}
}

// And the opposite, or the check above would pass on a tool that always
// reports incomplete.
func TestAFullyReadableMachineDerivesACompleteList(t *testing.T) {
	tmp := t.TempDir()
	index := filepath.Join(tmp, "registered-directories.json")
	registerDir(t, index, "flonk", "Wombat Industries", "kestrel")

	d := derive(derivePaths{
		Config:   writeConfig(t, map[string]string{"acorn": "/opt/fixture/borealis"}, []string{"Hollow Oak"}, nil),
		Registry: index,
		Gate:     writeGateFile(t, []string{"flonk"}, nil),
		Identities: func() (string, error) {
			return `  1) AB12CD34 "Developer ID Application: Someone (W0MB4TK3S7)"`, nil
		},
	})

	if !d.Complete() {
		t.Fatalf("Complete() = false; unavailable: %v", d.Unavailable())
	}
	if !slices.Contains(d.Names, "borealis") || !slices.Contains(d.Names, "Hollow Oak") {
		t.Errorf("Names = %v, want the config's real project and client names", d.Names)
	}
	if !slices.Contains(d.Candidates, "kestrel") {
		t.Errorf("Candidates = %v, want the registered project name", d.Candidates)
	}
	if slices.Contains(d.Candidates, "flonk") {
		t.Errorf("Candidates = %v, want the classified name left out", d.Candidates)
	}
	if !slices.Contains(d.Deny, "W0MB4TK3S7") {
		t.Errorf("Deny = %v, want the team ID from the signing identity", d.Deny)
	}
}

// A name proven sensitive by the config must never be demoted to a candidate by
// also being registered, and a classification must never silence it: only the
// registry tier is a human's to rule on.
func TestAConfigNameStaysSensitiveEvenWhenClassifiedPublishable(t *testing.T) {
	tmp := t.TempDir()
	index := filepath.Join(tmp, "registered-directories.json")
	registerDir(t, index, "borealis", "Hollow Oak", "borealis")

	d := derive(derivePaths{
		Config:     writeConfig(t, map[string]string{"acorn": "/opt/fixture/borealis"}, nil, nil),
		Registry:   index,
		Gate:       writeGateFile(t, []string{"borealis"}, nil),
		Identities: func() (string, error) { return "", nil },
	})

	if !slices.Contains(d.Names, "borealis") {
		t.Errorf("Names = %v, want the config's real name regardless of classification", d.Names)
	}
	if slices.Contains(d.Candidates, "borealis") {
		t.Errorf("Candidates = %v, want it counted once, as sensitive", d.Candidates)
	}
}

// A signing certificate can carry nothing more than the author's own name,
// which belongs in a copyright line. The ruling that clears a registered name
// has to clear that literal too, or the license cannot be committed.
func TestAClassifiedNameIsNotHuntedAsASigningLiteral(t *testing.T) {
	d := derive(derivePaths{
		Config:   writeConfig(t, nil, nil, nil),
		Registry: filepath.Join(t.TempDir(), "absent.json"),
		Gate:     writeGateFile(t, []string{"Pat Fixture"}, nil),
		Identities: func() (string, error) {
			return `  1) AB12 "Apple Development: Pat Fixture (QQ11WW22EE)"`, nil
		},
	})

	if slices.Contains(d.Deny, "Pat Fixture") {
		t.Errorf("Deny = %v, want the classified name left out", d.Deny)
	}
	if !slices.Contains(d.Deny, "QQ11WW22EE") {
		t.Errorf("Deny = %v, want the unclassified team ID still hunted", d.Deny)
	}
}
