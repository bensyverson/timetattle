// Package corpusgate keeps the untagged test suite from reading the machine it
// happens to be running on.
//
// The suite FAILS OPEN by construction. A test that cannot find the corpus it
// needs calls t.Skip, and `go test` prints "ok" whether it ran or not, so a
// real-data test and a unit test are indistinguishable from the outside. That
// is not a hypothetical: on 2026-08-02 alone it fired three times. A pseudonym
// rename pointed eight fixtures at a directory that does not exist; migration
// 0011 landing against a live store still at 0010 unhooked several more; and
// neither turned anything red. Coverage vanished and the suite stayed green.
//
// The fix is `//go:build realdata`. A test whose assertion is a claim about one
// particular machine's corpus goes behind the tag, so `go test ./...` is
// machine-independent and `go test -tags realdata ./...` is the deliberate
// corpus run. This package is what stops that boundary from eroding: Scan
// refuses an untagged test file that CALLS one of the resolvers below.
//
// # What it catches, and what it does not
//
// It matches syntax, not text: a resolver named in a comment or a string
// literal is not a call, and a gate that fired on its own documentation would
// be switched off within a week. It matches the BARE function name, because
// these resolvers are called unqualified from inside their own package's tests
// -- internal/biome's live tests call DefaultAppUsageDir() with no selector.
//
// Two coupling axes are covered, because both have shipped here:
//
//   - a root derived from the user's HOME, which is every Default* resolver;
//   - a root derived from the working DIRECTORY, which is os.Getwd. The census
//     under a redirected HOME cannot see this one. internal/jobs' live tests
//     walk up to the checkout and then read every SIBLING directory's .jobs.db,
//     so on this laptop they open unrelated repositories' databases and pass,
//     while on a fresh clone they would find nothing and skip.
//
// A hard-coded absolute path is NOT covered. Twenty-eight untagged test files
// hold a "/Users/..." literal and nearly all of them are locator strings that
// touch no disk, so a rule on that axis would be almost entirely false
// positives. scripts/foreignsuite is the complementary instrument: it runs the
// suite against an empty HOME and prints what skipped, which catches the
// hard-coded path this gate cannot see. Neither substitutes for the other.
//
// config.Default() is also not covered. It resolves the MACHINE's timezone, so
// a test built on it moves every day boundary with the runner's location -- a
// real coupling, on a third axis, and one no test in this repo currently trips
// because internal/storefixture carries its own calendar. It is named here so
// the omission is a decision rather than an oversight.
package corpusgate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"go/build/constraint"
)

// A Reach is one way a test can get hold of a path belonging to whoever is
// running it, rather than to the repository.
type Reach struct {
	// Symbol is the bare function name, with no package qualifier.
	Symbol string
	// Why says what it resolves. This is printed in the gate's failure, and it
	// is the only place a contributor learns why the test was refused.
	Why string
}

// Reaches is the closed set. Adding a resolver to the codebase and forgetting
// to add it here silently widens what an untagged test may read, so a new
// Default* function is a reason to revisit this list.
var Reaches = []Reach{
	{"DefaultPath", "the machine's own store at ~/.timetattle/timetattle.db, or its config"},
	{"DefaultDir", "the machine's harvested snapshots under ~/.timetattle/snapshots"},
	{"DefaultSourcePath", "the machine's live knowledgeC.db"},
	{"DefaultBiomeRoot", "the machine's live Biome stream under ~/Library/Biome"},
	{"DefaultBiomeDir", "the machine's captured Biome segments"},
	{"DefaultAppUsageDir", "the machine's live ScreenTime.AppUsage stream"},
	{"DefaultRoot", "the machine's Claude transcript corpus under ~/.claude/projects"},
	{"DefaultIndexPath", "the machine's registered-directories index"},
	{"DefaultRootsFile", "the machine's filedays roots file"},
	{"Builtin", "the shipped manifests, whose roots are real vaults on this machine and whose Spotlight discovery queries this machine's index"},
	{"ResolveReferenceProject", "this machine's ~/.timetattle/config.json reference_projects mapping, and the real directory it names"},
	{"UserHomeDir", "the home directory of whoever is running the suite"},
	{"Getwd", "the directory the suite was started from, and therefore whatever else is checked out beside it"},
}

// A Finding is one call to a Reach from a test file the default build compiles.
type Finding struct {
	File   string // slash-free, relative to the scanned root
	Line   int
	Symbol string
	Why    string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d calls %s(), which resolves %s", f.File, f.Line, f.Symbol, f.Why)
}

// Scan reports every call to a Reach made from a *_test.go file under root that
// the default build compiles. Files named in exempt are skipped; the map is
// keyed by the same relative path Finding.File carries, and its values are the
// reasons, which Scan does not read but Unspent does.
func Scan(root string, exempt map[string]string) ([]Finding, error) {
	byName := make(map[string]string, len(Reaches))
	for _, r := range Reaches {
		byName[r.Symbol] = r.Why
	}

	var found []Finding
	err := walkTests(root, func(rel, path string, src []byte) error {
		if _, ok := exempt[rel]; ok {
			return nil
		}
		gated, err := isGated(src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if gated {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			// A file the parser cannot read is not evidence of anything; say so
			// rather than reporting it clean.
			return fmt.Errorf("%s: %w", rel, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call.Fun)
			why, ok := byName[name]
			if !ok {
				return true
			}
			found = append(found, Finding{
				File:   rel,
				Line:   fset.Position(call.Lparen).Line,
				Symbol: name,
				Why:    why,
			})
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].File != found[j].File {
			return found[i].File < found[j].File
		}
		return found[i].Line < found[j].Line
	})
	return found, nil
}

// Unspent reports the exemptions that no longer buy anything: a file that has
// been deleted, or one that has stopped reaching for the machine. An allowlist
// nobody can shrink grows until it means nothing, and this is what shrinks it.
func Unspent(root string, exempt map[string]string) ([]string, error) {
	if len(exempt) == 0 {
		return nil, nil
	}
	reaching := make(map[string]bool)
	found, err := Scan(root, nil)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		reaching[f.File] = true
	}

	var unspent []string
	for file := range exempt {
		if reaching[file] {
			continue
		}
		unspent = append(unspent, file)
	}
	sort.Strings(unspent)
	return unspent, nil
}

// walkTests visits every *_test.go under root, outside the directories a Go
// toolchain ignores.
func walkTests(root string, visit func(rel, path string, src []byte) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "vendor" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return visit(rel, path, src)
	})
}

// isGated reports whether the default build leaves this file out. Only a
// constraint that is FALSE with realdata unset counts: a bare `//go:build
// darwin` still compiles here, and treating any constraint as an excuse would
// let one smuggle a corpus read back into the untagged suite.
func isGated(src []byte) (bool, error) {
	for line := range strings.SplitSeq(leadingComments(string(src)), "\n") {
		line = strings.TrimSpace(line)
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, err
		}
		return !expr.Eval(defaultBuildTag), nil
	}
	return false, nil
}

// defaultBuildTag answers as `go test` with no -tags does: the platform's own
// tags hold, and nothing else -- realdata least of all.
func defaultBuildTag(tag string) bool {
	switch tag {
	case "realdata":
		return false
	case runtime.GOOS, runtime.GOARCH:
		return true
	case "unix":
		return runtime.GOOS != "windows" && runtime.GOOS != "plan9"
	}
	return false
}

// leadingComments returns the part of a file before its package clause, which
// is the only place a //go:build line counts.
func leadingComments(src string) string {
	if before, _, ok := strings.Cut(src, "\npackage "); ok {
		return before
	}
	if strings.HasPrefix(src, "package ") {
		return ""
	}
	return src
}

// calleeName is the bare name of whatever a call expression calls: f() and
// pkg.f() both yield "f". Anything else -- a call through a variable, a method
// value, a conversion -- yields "".
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
