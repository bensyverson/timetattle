package corpusgate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/corpusgate"
)

// write puts one source file into a temp tree and returns the tree root.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// only scans root and asserts exactly one finding, which it returns.
func only(t *testing.T, root string) corpusgate.Finding {
	t.Helper()
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(found), found)
	}
	return found[0]
}

func TestAQualifiedResolverInAnUntaggedTestIsAFinding(t *testing.T) {
	root := write(t, map[string]string{"a/a_test.go": `package a

import "github.com/bensyverson/timetattle/internal/store"

func TestX(t *testing.T) { p, _ := store.DefaultPath(); _ = p }
`})
	f := only(t, root)
	if f.Symbol != "DefaultPath" {
		t.Errorf("Symbol = %q, want DefaultPath", f.Symbol)
	}
	if f.File != filepath.Join("a", "a_test.go") {
		t.Errorf("File = %q, want a/a_test.go", f.File)
	}
	if f.Line != 5 {
		t.Errorf("Line = %d, want 5", f.Line)
	}
}

// The resolvers are called unqualified from inside their own package's tests --
// internal/biome's live tests call DefaultAppUsageDir() with no selector -- so a
// rule keyed on "biome.DefaultAppUsageDir" would miss exactly the files this
// gate exists for.
func TestAnUnqualifiedResolverIsAlsoAFinding(t *testing.T) {
	root := write(t, map[string]string{"b_test.go": `package b

func TestX(t *testing.T) { d, _ := DefaultAppUsageDir(); _ = d }
`})
	if f := only(t, root); f.Symbol != "DefaultAppUsageDir" {
		t.Errorf("Symbol = %q, want DefaultAppUsageDir", f.Symbol)
	}
}

// A test that only NAMES a resolver in prose reaches nothing. Matching text
// rather than syntax would make the gate fire on its own documentation, which
// is how a check gets switched off.
func TestAResolverInACommentIsNotAFinding(t *testing.T) {
	root := write(t, map[string]string{"c_test.go": `package c

// store.DefaultPath() is deliberately not called here.
func TestX(t *testing.T) {}
`})
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing; the resolver appears only in a comment", found)
	}
}

func TestAResolverInAStringLiteralIsNotAFinding(t *testing.T) {
	root := write(t, map[string]string{"c_test.go": `package c

func TestX(t *testing.T) { _ = "call store.DefaultPath() to find it" }
`})
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing; the resolver appears only in a string", found)
	}
}

// The whole point of the tag: a file the default build does not compile cannot
// read anything, so it is not this gate's business.
func TestAFileBehindTheRealdataTagIsNotAFinding(t *testing.T) {
	root := write(t, map[string]string{"d_test.go": `//go:build realdata

package d

func TestX(t *testing.T) { p, _ := DefaultPath(); _ = p }
`})
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing; the file is behind //go:build realdata", found)
	}
}

// realdata combined with another constraint still excludes the file from the
// default build, and a gate that only understood the bare form would demand a
// pointless exemption for every platform-specific corpus test.
func TestARealdataFileWithAFurtherConstraintIsNotAFinding(t *testing.T) {
	root := write(t, map[string]string{"e_test.go": `//go:build realdata && darwin

package e

func TestX(t *testing.T) { p, _ := DefaultPath(); _ = p }
`})
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing", found)
	}
}

// A constraint that is not about realdata leaves the file in the default build,
// so it is still gated. Treating any constraint as an excuse would let
// `//go:build darwin` smuggle a corpus read back in.
func TestANonRealdataConstraintDoesNotExemptAFile(t *testing.T) {
	root := write(t, map[string]string{"f_test.go": `//go:build darwin

package f

func TestX(t *testing.T) { p, _ := DefaultPath(); _ = p }
`})
	if f := only(t, root); f.Symbol != "DefaultPath" {
		t.Errorf("Symbol = %q, want DefaultPath", f.Symbol)
	}
}

func TestANonTestFileIsNotScanned(t *testing.T) {
	root := write(t, map[string]string{"g.go": `package g

func Use() { p, _ := DefaultPath(); _ = p }
`})
	found, err := corpusgate.Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing; production code may resolve the machine's own paths", found)
	}
}

// An exemption is a claim about one file, and it must be spent: a file listed
// but no longer reaching is reported too. An exemption nobody can revoke is how
// a gate's allowlist grows until it means nothing.
func TestAnExemptedFileIsNotAFinding(t *testing.T) {
	root := write(t, map[string]string{"h/h_test.go": `package h

func TestX(t *testing.T) { p, _ := DefaultPath(); _ = p }
`})
	exempt := map[string]string{filepath.Join("h", "h_test.go"): "asserts the string, reads nothing"}
	found, err := corpusgate.Scan(root, exempt)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("got %v, want nothing", found)
	}
}

func TestAnUnspentExemptionIsReported(t *testing.T) {
	root := write(t, map[string]string{"i/i_test.go": `package i

func TestX(t *testing.T) {}
`})
	unspent, err := corpusgate.Unspent(root, map[string]string{
		filepath.Join("i", "i_test.go"):    "no longer true",
		filepath.Join("i", "gone_test.go"): "the file was deleted",
	})
	if err != nil {
		t.Fatalf("Unspent: %v", err)
	}
	if len(unspent) != 2 {
		t.Fatalf("got %v, want both the no-longer-reaching file and the deleted one", unspent)
	}
}

// Every reach carries a sentence saying what it resolves, because the gate's
// failure message is the only place a contributor learns why their test was
// refused.
func TestEveryReachExplainsItself(t *testing.T) {
	if len(corpusgate.Reaches) == 0 {
		t.Fatal("no reaches are declared; the gate would pass on anything")
	}
	seen := map[string]bool{}
	for _, r := range corpusgate.Reaches {
		if r.Symbol == "" || r.Why == "" {
			t.Errorf("reach %+v is missing a symbol or a reason", r)
		}
		if strings.Contains(r.Symbol, ".") {
			t.Errorf("reach %q is qualified; the scan matches the bare function name", r.Symbol)
		}
		if seen[r.Symbol] {
			t.Errorf("reach %q is declared twice", r.Symbol)
		}
		seen[r.Symbol] = true
	}
}
