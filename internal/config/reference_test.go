package config_test

// Task paDFg. The pseudonym scrub renamed the reference-project symbol in
// test fixtures to "acorn", but Ben's real directory on disk kept its real
// name, so every //go:build realdata test that resolved
// $HOME/git/<real-name> found nothing and went dark. The fix is Option 2
// from the task: resolve the pseudonym to a real path through
// ~/.timetattle/config.json, which lives outside the repository entirely
// (task 5nCl4 already argues the mapping belongs there).
//
// The three tests below pin the property docs/content/docs/testing.md calls
// "absence of a row is not an observation": a skip must name what was
// missing, and there are three different things that can be missing here —
// no config file, a config with no mapping for this pseudonym, and a mapped
// path that is not on disk — and they must never collapse into one message.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/config"
)

// writeReferenceConfig drops a config.json holding exactly the given
// reference_projects mapping (built through json.Marshal rather than string
// concatenation, so a $TMPDIR path with an unusual character cannot corrupt
// the fixture).
func writeReferenceConfig(t *testing.T, mapping map[string]string) string {
	t.Helper()
	body, err := json.Marshal(struct {
		ReferenceProjects map[string]string `json:"reference_projects"`
	}{mapping})
	if err != nil {
		t.Fatalf("marshal the test config: %v", err)
	}
	return write(t, string(body))
}

func TestResolveReferenceProjectWhenNoConfigFileExistsAtAll(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	_, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err == nil {
		t.Fatal("resolved a pseudonym with no config file present at all")
	}
	var rpErr *config.ReferenceProjectError
	if !errors.As(err, &rpErr) {
		t.Fatalf("error is %T, want *config.ReferenceProjectError: %v", err, err)
	}
	if rpErr.Kind != config.ReferenceProjectNoConfigFile {
		t.Errorf("Kind = %v, want ReferenceProjectNoConfigFile", rpErr.Kind)
	}
	if !strings.Contains(err.Error(), configPath) {
		t.Errorf("the error does not name the config path it looked for: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the error does not wrap the stat failure that produced it: %v", err)
	}
}

func TestResolveReferenceProjectWhenTheConfigHasNoMappingForThisPseudonym(t *testing.T) {
	configPath := writeReferenceConfig(t, map[string]string{"birch": "/somewhere"})

	_, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err == nil {
		t.Fatal("resolved a pseudonym the config does not map")
	}
	var rpErr *config.ReferenceProjectError
	if !errors.As(err, &rpErr) {
		t.Fatalf("error is %T, want *config.ReferenceProjectError: %v", err, err)
	}
	if rpErr.Kind != config.ReferenceProjectNoMapping {
		t.Errorf("Kind = %v, want ReferenceProjectNoMapping", rpErr.Kind)
	}
	if rpErr.MappedCount != 1 {
		t.Errorf("MappedCount = %d, want 1 (the config maps one pseudonym, just not this one)", rpErr.MappedCount)
	}
	// This is the regression the task exists to fix: the old message read
	// "$HOME/git/acorn is not present on this machine", which implies the
	// DATA is missing when what is actually missing is the NAME MAPPING.
	if strings.Contains(err.Error(), "is not present on this machine") {
		t.Errorf("a missing mapping must not be phrased like a missing directory: %v", err)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("the error does not count the mapping(s) the config does carry: %v", err)
	}
}

func TestResolveReferenceProjectWhenTheConfigHasNoMappingsAtAll(t *testing.T) {
	// A distinct sub-case of "no mapping": the section is present and empty,
	// as opposed to present and non-empty-but-missing-this-key. Both are
	// ReferenceProjectNoMapping, but the count differs and the message
	// should not claim a mapping exists that does not.
	configPath := writeReferenceConfig(t, map[string]string{})

	_, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err == nil {
		t.Fatal("resolved a pseudonym against a config with no mappings at all")
	}
	var rpErr *config.ReferenceProjectError
	if !errors.As(err, &rpErr) {
		t.Fatalf("error is %T, want *config.ReferenceProjectError: %v", err, err)
	}
	if rpErr.Kind != config.ReferenceProjectNoMapping {
		t.Errorf("Kind = %v, want ReferenceProjectNoMapping", rpErr.Kind)
	}
	if rpErr.MappedCount != 0 {
		t.Errorf("MappedCount = %d, want 0", rpErr.MappedCount)
	}
}

func TestResolveReferenceProjectWhenTheMappedPathIsNotOnDisk(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist-on-this-machine")
	configPath := writeReferenceConfig(t, map[string]string{"acorn": missing})

	_, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err == nil {
		t.Fatal("resolved a pseudonym whose mapped path is not on disk")
	}
	var rpErr *config.ReferenceProjectError
	if !errors.As(err, &rpErr) {
		t.Fatalf("error is %T, want *config.ReferenceProjectError: %v", err, err)
	}
	if rpErr.Kind != config.ReferenceProjectPathNotOnDisk {
		t.Errorf("Kind = %v, want ReferenceProjectPathNotOnDisk", rpErr.Kind)
	}
	if rpErr.Path != missing {
		t.Errorf("Path = %q, want %q", rpErr.Path, missing)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the error does not wrap the stat failure: %v", err)
	}
	if !strings.Contains(err.Error(), "is not present on this machine") {
		t.Errorf("a mapped-but-absent path should read as a missing directory: %v", err)
	}
}

func TestResolveReferenceProjectSucceedsWhenTheMappedPathExists(t *testing.T) {
	real := t.TempDir()
	configPath := writeReferenceConfig(t, map[string]string{"acorn": real})

	got, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err != nil {
		t.Fatalf("ResolveReferenceProjectAt: %v", err)
	}
	if got != real {
		t.Errorf("resolved %q, want %q", got, real)
	}
}

// TestTheThreeReferenceProjectFailureStatesProduceDistinctMessages is
// criterion FDT itself, as a property rather than a single example: the
// three missing preconditions this task distinguishes must never read as
// the same sentence, however they are worded.
func TestTheThreeReferenceProjectFailureStatesProduceDistinctMessages(t *testing.T) {
	noFile := filepath.Join(t.TempDir(), "config.json")
	noMapping := writeReferenceConfig(t, map[string]string{"birch": "/somewhere"})
	missing := filepath.Join(t.TempDir(), "gone")
	pathMissing := writeReferenceConfig(t, map[string]string{"acorn": missing})

	_, err1 := config.ResolveReferenceProjectAt("acorn", noFile)
	_, err2 := config.ResolveReferenceProjectAt("acorn", noMapping)
	_, err3 := config.ResolveReferenceProjectAt("acorn", pathMissing)
	for _, e := range []error{err1, err2, err3} {
		if e == nil {
			t.Fatal("expected all three resolutions to fail")
		}
	}

	msgs := []string{err1.Error(), err2.Error(), err3.Error()}
	for i := range msgs {
		for j := range msgs {
			if i != j && msgs[i] == msgs[j] {
				t.Errorf("state %d and %d produced the same message: %q", i, j, msgs[i])
			}
		}
	}
}

func TestResolveReferenceProjectAtDoesNotReadTheRealHomeDirectory(t *testing.T) {
	// Guards the shape of the split between ResolveReferenceProject (the
	// $HOME-resolving entry point) and ResolveReferenceProjectAt (the
	// testable one): the latter must never fall back to the real config
	// path when given an explicit one, or every test above would be
	// silently coupled to whatever is on the machine running them.
	real := t.TempDir()
	configPath := writeReferenceConfig(t, map[string]string{"acorn": real})

	got, err := config.ResolveReferenceProjectAt("acorn", configPath)
	if err != nil {
		t.Fatalf("ResolveReferenceProjectAt: %v", err)
	}
	if got != real {
		t.Errorf("resolved %q, want the path named at the explicit config path, %q", got, real)
	}
}

func TestValidateRefusesAnEmptyReferenceProjectPseudonym(t *testing.T) {
	c := config.Default()
	c.ReferenceProjects = map[string]string{"": "/somewhere"}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a reference project with an empty pseudonym")
	}
}

func TestValidateRefusesAnEmptyReferenceProjectPath(t *testing.T) {
	c := config.Default()
	c.ReferenceProjects = map[string]string{"acorn": ""}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a reference project mapped to an empty path")
	}
}

func TestValidateRefusesARelativeReferenceProjectPath(t *testing.T) {
	// The whole point is a real, resolvable location; a relative path is
	// relative to whatever the current working directory happens to be when
	// a test runs, which reintroduces the exact machine-coupling by
	// accident this mapping exists to remove.
	c := config.Default()
	c.ReferenceProjects = map[string]string{"acorn": "git/acorn"}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a relative reference project path")
	}
}

func TestAReferenceProjectsMappingRoundTripsThroughLoad(t *testing.T) {
	real := t.TempDir()
	configPath := writeReferenceConfig(t, map[string]string{"acorn": real, "birch": "/somewhere/else"})

	got, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ReferenceProjects["acorn"] != real {
		t.Errorf(`ReferenceProjects["acorn"] = %q, want %q`, got.ReferenceProjects["acorn"], real)
	}
	if got.ReferenceProjects["birch"] != "/somewhere/else" {
		t.Errorf(`ReferenceProjects["birch"] = %q, want "/somewhere/else"`, got.ReferenceProjects["birch"])
	}
}
