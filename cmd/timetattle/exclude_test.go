package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// excludeSeeded is a store holding one day's evidence for two projects: real
// work in "realwork", and a single SQLite sidecar touch in "sidecarrepo".
//
// That second project is the shape this whole task was raised for. On
// 2026-08-01 seven unrelated repositories each showed a whole working day built
// out of nothing but a WAL checkpoint, every stamp inside the same second.
func excludeSeeded(t *testing.T, locators ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "fs-observations", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}

	base := time.Date(2026, 7, 11, 14, 0, 0, 0, time.UTC)
	var events []source.Event
	// Two saves a minute apart in the real project, so its sitting has an
	// extent of its own rather than being only lead-in and tail-out.
	for _, offset := range []time.Duration{0, time.Minute} {
		e := source.NewPoint(base.Add(offset), source.Identity{Document: "main"}, "fs-observations")
		e.Locator = "/tmp/seed/realwork/main.go"
		events = append(events, e)
	}
	for i, locator := range locators {
		e := source.NewPoint(base.Add(time.Duration(i)*time.Second),
			source.Identity{Document: filepath.Base(locator)}, "fs-observations")
		e.Locator = locator
		events = append(events, e)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	// store.Write refuses a project: the column is a recomputable cache
	// resolved from the locator, so the seed runs the same attribution pass
	// harvest does. The project is the first path element under /tmp/seed.
	if _, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		rest, ok := strings.CutPrefix(locator, "/tmp/seed/")
		if !ok {
			return store.Attributed{}, false, nil
		}
		name, _, found := strings.Cut(rest, "/")
		return store.Attributed{Project: name}, found, nil
	}); err != nil {
		t.Fatalf("attribute the seed: %v", err)
	}
	st.Close()
	return path
}

func excludeSessionize(t *testing.T, path string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := sessionizeTo(&out, append([]string{"-store", path, "-config",
		filepath.Join(t.TempDir(), "absent.json")}, args...)); err != nil {
		t.Fatalf("sessionize: %v", err)
	}
	return out.String()
}

func excludeMetrics(t *testing.T, path string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	// -all: this helper's callers want the whole report, not whatever project
	// the test binary's own working directory happens to resolve to.
	if err := metricsTo(&out, append([]string{"-store", path, "-config",
		filepath.Join(t.TempDir(), "absent.json"), "-all"}, args...)); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	return out.String()
}

// TestStoredSidecarsStopCountingWithNoConfigurationAtAll is the retroactive
// half of the fix, and the reason the rule is applied at READ time.
//
// internal/store exposes no delete, so the 86 sidecar observations already in
// the live store are permanent. Changing fsobserve.skipFile stops the next
// harvest collecting more and moves the existing figures by exactly nothing.
func TestStoredSidecarsStopCountingWithNoConfigurationAtAll(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/sidecarrepo/.jobs.db-wal", "/tmp/seed/sidecarrepo/.jobs.db-shm")

	out := excludeMetrics(t, path)
	if strings.Contains(out, "sidecarrepo") {
		t.Errorf("a project whose only evidence is a SQLite sidecar was credited engaged time:\n%s", out)
	}
	if !strings.Contains(out, "realwork") {
		t.Errorf("the real project disappeared along with the sidecars:\n%s", out)
	}
}

// TestSessionizeSaysHowManyObservationsItExcluded. A derivation that dropped
// observations without saying how many, or by which rule, cannot be reconciled
// against the store it came out of.
func TestSessionizeSaysHowManyObservationsItExcluded(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/sidecarrepo/.jobs.db-wal",
		"/tmp/seed/logproject/access.log.bz2")

	out := excludeSessionize(t, path, "-dry-run", "-exclude", "*.log.bz2")
	if !strings.Contains(out, "excluded") {
		t.Fatalf("the report never mentions exclusion at all:\n%s", out)
	}
	if !strings.Contains(out, "1 SQLite sidecar") {
		t.Errorf("the report does not count the sidecar the code rule took:\n%s", out)
	}
	if !strings.Contains(out, `*.log.bz2`) {
		t.Errorf("the report does not name the pattern that took the rotated log:\n%s", out)
	}
}

// TestSessionizeSaysWhenAPatternMatchedNothing. A pattern nobody's files match
// is a person having said something the tool did nothing with, and it must not
// look the same as a pattern that was never written. This project has shipped
// the collapsed version of that distinction three times.
func TestSessionizeSaysWhenAPatternMatchedNothing(t *testing.T) {
	path := excludeSeeded(t)

	out := excludeSessionize(t, path, "-dry-run", "-exclude", "*.nothing-here")
	if !strings.Contains(out, "*.nothing-here") {
		t.Errorf("a pattern that matched nothing was not reported:\n%s", out)
	}
}

// TestTheExcludeFlagOverridesTheFileWithoutWritingIt, the same contract every
// other parameter has, and what makes the effect of a proposed list measurable
// before anybody commits it to their config.
func TestTheExcludeFlagOverridesTheFileWithoutWritingIt(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/logproject/access.log.bz2")
	cfg := writeConfig(t, `{"sessions": {"exclude": []}}`)

	var out bytes.Buffer
	if err := metricsTo(&out, []string{"-store", path, "-config", cfg,
		"-exclude", "*.log.bz2", "-all"}); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if strings.Contains(out.String(), "logproject") {
		t.Errorf("-exclude did not override the empty list in the config file:\n%s", out.String())
	}

	// And the file is still what it was. A swept parameter that rewrote the
	// config would make an experiment permanent by accident.
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("re-read the config: %v", err)
	}
	if !strings.Contains(string(body), `"exclude": []`) {
		t.Errorf("the config file was rewritten by a flag: %s", body)
	}
}

// TestAnExcludedProjectKeepsEveryDayItHadOtherEvidenceFor is the safety
// property, stated as a test rather than as a hope.
//
// Easley is a REAL project — Ben builds a dashboard in it — that also runs a
// sync writing rotated logs into its own tree. Excluding the logs must remove
// the days that were only ever the sync, and must not touch a day the same
// project has a commit or a real file save on.
func TestAnExcludedProjectKeepsEveryDayItHadOtherEvidenceFor(t *testing.T) {
	path := excludeSeeded(t,
		"/tmp/seed/realwork/access.log.bz2",
		"/tmp/seed/logproject/access.log.bz2")

	out := excludeMetrics(t, path, "-exclude", "*.log.bz2")
	if strings.Contains(out, "logproject") {
		t.Errorf("a project whose only evidence was a rotated log was still credited:\n%s", out)
	}
	if !strings.Contains(out, "realwork") {
		t.Errorf("a project lost a day it had other evidence on; the exclusion reached past "+
			"the files it names:\n%s", out)
	}
}

// TestReviewDoesNotDrawAProjectRowMadeOfASidecar is the bug as a human met it.
// One observation is enough to put a project row on a day's review screen, so a
// handful of WAL checkpoints fabricated seven repositories' working days.
func TestReviewDoesNotDrawAProjectRowMadeOfASidecar(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/sidecarrepo/.jobs.db-wal")
	cfg := writeConfig(t, reviewConfig)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize the seed: %v", err)
	}

	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-print")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if strings.Contains(out, "sidecarrepo") {
		t.Errorf("the day view still shows a project whose whole working day is a WAL "+
			"checkpoint:\n%s", out)
	}
	if !strings.Contains(out, "realwork") {
		t.Errorf("the day view lost the project that really was worked on:\n%s", out)
	}
}
