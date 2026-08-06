// Command motionjoin measures whether Motion Autosave Vault composition names
// can be joined safely to project .motn documents by filename stem.
//
// It deliberately reports three states separately: a matching document was
// observed, no matching document was observed below the supplied roots, and a
// vault name is ambiguous because documents under different supplied project
// roots share the stem. Multiple copies below one root (for example, Claude
// worktrees) still resolve to that one project and are a positive join.
// The second state is not evidence that a document never existed: roots are a
// measured search boundary, not a claim about the whole machine.
//
// Usage:
//
//	go run ./scripts/motionjoin -root '/path/to/Motion Projects'
//
// Repeat -root for every directory that contains known project documents.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type roots []string

func (r *roots) String() string { return strings.Join(*r, ", ") }

func (r *roots) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func main() {
	var searchRoots roots
	vault := flag.String("vault", filepath.Join(homeDir(), "Movies", "Motion Projects", "Autosave Vault"), "Motion Autosave Vault directory")
	flag.Var(&searchRoots, "root", "known project-document root to search (repeatable; required)")
	flag.Parse()
	if len(searchRoots) == 0 {
		fatal("give at least one -root containing known project .motn files")
	}

	vaultNames, err := vaultCompositions(*vault)
	if err != nil {
		fatal("read vault: %v", err)
	}
	documents, err := documentsBelow(searchRoots, *vault)
	if err != nil {
		fatal("read project roots: %v", err)
	}

	matched, missing, ambiguous := 0, 0, 0
	for _, name := range vaultNames {
		byRoot := documents[name]
		roots := sortedRoots(byRoot)
		switch len(roots) {
		case 0:
			missing++
			fmt.Printf("NOT OBSERVED\t%s\n", name)
		case 1:
			matched++
			fmt.Printf("MATCH\t%s\t%s\n", name, strings.Join(byRoot[roots[0]], " | "))
		default:
			ambiguous++
			fmt.Printf("AMBIGUOUS\t%s\t%s\n", name, strings.Join(roots, " | "))
		}
	}

	fmt.Printf("\nVault compositions: %d\n", len(vaultNames))
	fmt.Printf("Match: %d\n", matched)
	fmt.Printf("Not observed below supplied roots: %d\n", missing)
	fmt.Printf("Ambiguous: %d\n", ambiguous)
	if len(vaultNames) > 0 {
		fmt.Printf("Unambiguous hit rate: %.1f%% (%d/%d)\n", 100*float64(matched)/float64(len(vaultNames)), matched, len(vaultNames))
	}
}

func vaultCompositions(vault string) ([]string, error) {
	entries, err := os.ReadDir(vault)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(vault, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if !file.IsDir() && strings.EqualFold(filepath.Ext(file.Name()), ".motn") {
				names = append(names, entry.Name())
				break
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func documentsBelow(searchRoots []string, vault string) (map[string]map[string][]string, error) {
	documents := make(map[string]map[string][]string)
	vault, err := filepath.EvalSymlinks(vault)
	if err != nil {
		return nil, err
	}
	for _, root := range searchRoots {
		root, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && path == vault {
				return filepath.SkipDir
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".motn") {
				return nil
			}
			stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if documents[stem] == nil {
				documents[stem] = make(map[string][]string)
			}
			documents[stem][root] = append(documents[stem][root], path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, byRoot := range documents {
		for _, paths := range byRoot {
			sort.Strings(paths)
		}
	}
	return documents, nil
}

func sortedRoots(byRoot map[string][]string) []string {
	roots := make([]string, 0, len(byRoot))
	for root := range byRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "motionjoin: "+format+"\n", args...)
	os.Exit(1)
}
