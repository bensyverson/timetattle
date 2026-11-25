package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/chains"
)

// TestStoredGeneratedFilesStopCounting: the records are permanent, so every
// command that reports work has to leave them out with nothing configured.
func TestStoredGeneratedFilesStopCounting(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/buildonly/.build/debug/main.o",
		"/tmp/seed/buildonly/.venv/lib/other.py")

	metrics := excludeMetrics(t, path)
	if strings.Contains(metrics, "buildonly") {
		t.Errorf("a project whose only evidence is generated files was credited time:\n%s", metrics)
	}
	if !strings.Contains(metrics, "realwork") {
		t.Errorf("the real project disappeared along with the generated files:\n%s", metrics)
	}
	sessionize := excludeSessionize(t, path, "-dry-run")
	if !strings.Contains(sessionize, "2 in generated directories") {
		t.Errorf("the report does not count what the generated-file rule took:\n%s", sessionize)
	}
}

// TestReviewDoesNotDrawAProjectRowMadeOfGeneratedFiles: review reads events
// separately from metrics.
func TestReviewDoesNotDrawAProjectRowMadeOfGeneratedFiles(t *testing.T) {
	path := excludeSeeded(t, "/tmp/seed/buildonly/.build/debug/main.o")
	cfg := writeConfig(t, reviewConfig)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize the seed: %v", err)
	}

	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-print")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if strings.Contains(out, "buildonly") {
		t.Errorf("the day view shows a project made of generated files:\n%s", out)
	}
	if !strings.Contains(out, "realwork") {
		t.Errorf("the day view lost the project that really was worked on:\n%s", out)
	}
}

// TestChainsSaysHowManyGeneratedFilesItLeftOut.
func TestChainsSaysHowManyGeneratedFilesItLeftOut(t *testing.T) {
	var out bytes.Buffer
	reportChains(&out, chains.Result{Generated: 1}, "subject")
	if !strings.Contains(out.String(), "1 observation in generated directories not considered") {
		t.Errorf("chains does not say what it left out:\n%s", out.String())
	}
}
