package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

// `timetattle version` exists so a released binary is traceable to a commit —
// the reproducibility requirement from task 11hMe. Rather than threading a
// commit hash through ldflags (which would mean the release script hardcodes
// something), it reads the VCS stamp Go has embedded automatically since
// 1.18 whenever a binary is built from within a clean git checkout. That
// stamp is already present and correct in every binary this project builds
// today; version.go only has to surface it.
//
// readBuildInfo is a package var precisely so these tests can supply a fixed
// debug.BuildInfo rather than depending on the real one, which varies with
// working-tree state and would make the tests flaky.

func withBuildInfo(t *testing.T, info *debug.BuildInfo, ok bool) {
	t.Helper()
	prev := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
	t.Cleanup(func() { readBuildInfo = prev })
}

func TestVersionReportsTheCommitFromTheVCSStamp(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-20260802062251-1f5392d2348b"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1f5392d2348b88294e4d19d0c9efa83b09130395"},
			{Key: "vcs.time", Value: "2026-08-02T06:22:51Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}, true)

	var buf bytes.Buffer
	if err := versionTo(&buf, nil); err != nil {
		t.Fatalf("versionTo: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "1f5392d2348b88294e4d19d0c9efa83b09130395") {
		t.Errorf("output does not name the commit:\n%s", out)
	}
	if !strings.Contains(out, "2026-08-02T06:22:51Z") {
		t.Errorf("output does not name the build time:\n%s", out)
	}
	if strings.Contains(out, "dirty") {
		t.Errorf("a clean build must not be reported dirty:\n%s", out)
	}
}

func TestVersionFlagsABuildFromADirtyWorkingTree(t *testing.T) {
	// A binary built from an uncommitted tree is not reproducible from the
	// commit alone, and a release script that ships one is the failure mode
	// this exists to catch. The word "dirty" has to survive into the output
	// literally, because that is what a bug report or a release checklist
	// greps for.
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-20260802062251-1f5392d2348b"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1f5392d2348b88294e4d19d0c9efa83b09130395"},
			{Key: "vcs.time", Value: "2026-08-02T06:22:51Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	var buf bytes.Buffer
	if err := versionTo(&buf, nil); err != nil {
		t.Fatalf("versionTo: %v", err)
	}
	if !strings.Contains(buf.String(), "dirty") {
		t.Errorf("a dirty build must say so:\n%s", buf.String())
	}
}

func TestVersionWithoutVCSStampSaysSoRatherThanGuessing(t *testing.T) {
	// go build -buildvcs=false, or a binary built outside a git checkout
	// (go install of a tarball, for instance), leaves no VCS settings. That
	// is a real, distinct state from "clean at some known commit" and must
	// not be reported as either — it must say the commit is unknown.
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}, true)

	var buf bytes.Buffer
	if err := versionTo(&buf, nil); err != nil {
		t.Fatalf("versionTo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "unknown") {
		t.Errorf("output should admit the commit is unknown:\n%s", out)
	}
}

func TestVersionWhenReadBuildInfoFails(t *testing.T) {
	// debug.ReadBuildInfo's second return is false when the binary was not
	// built by the Go toolchain in the ordinary way (e.g. a plugin). This is
	// the same "nothing was recorded" case as the test above, reached by a
	// different door, and must not crash.
	withBuildInfo(t, nil, false)

	var buf bytes.Buffer
	if err := versionTo(&buf, nil); err != nil {
		t.Fatalf("versionTo: %v", err)
	}
	if !strings.Contains(buf.String(), "unknown") {
		t.Errorf("output should admit the commit is unknown:\n%s", buf.String())
	}
}
