package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Three separate agents, none able to see the others' work, each reached past
// internal/store to the database file: sessionize and chains opened a second
// *sql.DB to write their derived tables, and status opened a read-only one to
// read harvest_run with SQL it spelled out itself. Each flagged it; each was
// right to; and the seam was only visible once all three existed.
//
// This test is the guard so that a fourth does not have to be written before
// anyone notices. It is deliberately about IMPORTS rather than about a grep for
// "sql.Open": a command that has no way to name a *sql.DB and no way to
// register a driver cannot open a handle to the store, cannot hold a
// transaction, and has nowhere to put a raw statement. The property is
// structural rather than a spelling.
//
// Test files are exempt and stay exempt. Asserting on what a command wrote by
// reading the columns back through the driver is how a bug in a writer is kept
// from hiding behind the reader that shares its code — see derivedCount in
// chains_test.go, which says so in as many words.
func TestNoCommandReachesPastTheStoreToTheDatabase(t *testing.T) {
	forbidden := map[string]string{
		"database/sql":       "a command that can name a *sql.DB or a *sql.Tx can open its own handle to the store",
		"modernc.org/sqlite": "a command that registers the driver can open the store file behind internal/store's back",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the command's own directory: %v", err)
	}

	fset := token.NewFileSet()
	var checked int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unreadable import %s", name, spec.Path.Value)
			}
			if why, bad := forbidden[path]; bad {
				t.Errorf("%s imports %q: %s.\n"+
					"Everything a command needs from the database belongs behind a method on "+
					"internal/store.Store — see Store.ReplaceDerived, Store.Runs and store.OpenReadOnly.",
					name, path, why)
			}
		}
	}
	// A sanity check on the test itself: the package really does have several
	// non-test files, so a filter that accidentally excluded all of them would
	// show up here rather than as a permanent pass.
	if checked < 3 {
		t.Fatalf("only %d non-test files were checked; this test cannot see the files it is about", checked)
	}
}
