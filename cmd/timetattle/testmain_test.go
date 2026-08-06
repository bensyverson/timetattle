package main

import (
	"os"
	"testing"
)

// TestMain isolates every test in this package from the real machine's home
// directory.
//
// This package's tests call register() and harvest() as the actual CLI
// entry points, and several flags resolve a default under the caller's home
// directory exactly the way the real binary does for a real user — -store,
// -dir, and now -index. TestTheStoreFlagRedirectsWrites (harvest_test.go)
// already documents the property this exists to protect: "Nothing here may
// touch ~/.timetattle/timetattle.db... a test that pollutes the machine's
// own store is worse than no test." That property held only by every test
// happening to pass an explicit override for the one flag that mattered.
// -index broke it on the first run written against it: register_test.go's
// existing tests call register() without -index, since -index did not exist
// when they were written, and each one silently wrote a handful of
// temp-directory paths into this developer's real
// ~/.timetattle/registered-directories.json — caught only by inspecting the
// real file afterwards, not by any test failing.
//
// Redirecting HOME for the whole binary, once, closes that class of bug for
// every current and future flag that resolves a ~/.timetattle default,
// rather than requiring every test that exercises register() or harvest()
// to remember to isolate itself individually.
// realHome is the HOME this binary was started with, recorded before the
// redirect below throws it away.
//
// Nothing on the untagged side of the realdata tag may use it, and nothing
// does. It exists for the corpus checks in *_realdata_test.go, which have to
// read something this machine actually harvested, and which restore it through
// t.Setenv for the length of one test at a time. Without it those tests could
// not run at all: TestIngestingTheNewestRealSnapshotTwiceAddsNothing resolved
// its snapshot directory inside the temp home and skipped on every run,
// including on the machine holding the snapshots, from the day TestMain landed
// until 2026-08-02.
var realHome = os.Getenv("HOME")

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "timetattle-test-home")
	if err != nil {
		panic("cmd/timetattle TestMain: create an isolated HOME: " + err.Error())
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
