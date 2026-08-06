// Command env reports whether the evidence stores Timetattle wants to harvest
// are present and readable on this machine.
//
// It exists because "we couldn't read it" and "it isn't there" are very
// different findings, and macOS conflates them: TCC (Full Disk Access) lets
// stat succeed while open fails, so a naive existence check reports a
// populated database as missing. Every target is therefore stat'd *and*
// opened.
//
// Usage: go run ./scripts/spike/env
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
)

type target struct {
	name string
	path string
	note string
}

// verdict is what we learned about one target.
type verdict struct {
	status string // ok | denied | missing | error
	detail string
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	targets := []target{
		{
			name: "knowledgeC",
			path: filepath.Join(home, "Library/Application Support/Knowledge/knowledgeC.db"),
			note: "app-focus intervals; presence signal. Needs Full Disk Access.",
		},
		{
			name: "knowledgeC-wal",
			path: filepath.Join(home, "Library/Application Support/Knowledge/knowledgeC.db-wal"),
			note: "write-ahead log; must be copied alongside the db or recent events are lost.",
		},
		{
			name: "revisions-data",
			path: "/System/Volumes/Data/.DocumentRevisions-V100",
			note: "macOS Versions store on the data volume; intermediate save history.",
		},
		{
			name: "revisions-root",
			path: "/.DocumentRevisions-V100",
			note: "same store seen through the firmlink; expected to resolve to revisions-data.",
		},
		{
			name: "claude-projects",
			path: filepath.Join(home, ".claude/projects"),
			note: "Claude Code transcripts; densest artifact source. No special permissions.",
		},
		{
			name: "acorn-repo",
			path: filepath.Join(home, "git/acorn"),
			note: "the acceptance-test project: Motion and Photoshop work, weak git signal.",
		},
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TARGET\tSTATUS\tDETAIL")
	for _, t := range targets {
		v := inspect(t.path)
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.name, v.status, v.detail)
	}
	w.Flush()

	fmt.Println()
	for _, t := range targets {
		fmt.Printf("%-16s %s\n%-16s %s\n", t.name, t.path, "", t.note)
	}

	fmt.Println()
	reportSQLite()
}

// inspect distinguishes absence from refusal. A stat that succeeds proves
// nothing about readability under TCC, so we always attempt an open.
func inspect(path string) verdict {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return verdict{"missing", "no such path"}
	case errors.Is(err, fs.ErrPermission):
		return verdict{"denied", "stat refused; grant Full Disk Access"}
	case err != nil:
		return verdict{"error", err.Error()}
	}

	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if errors.Is(err, fs.ErrPermission) {
			return verdict{"denied", "directory listing refused"}
		}
		if err != nil {
			return verdict{"error", err.Error()}
		}
		return verdict{"ok", fmt.Sprintf("directory, %d entries", len(entries))}
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrPermission) {
		return verdict{"denied", fmt.Sprintf("exists (%s) but open refused", byteCount(info.Size()))}
	}
	if err != nil {
		return verdict{"error", err.Error()}
	}
	defer f.Close()

	buf := make([]byte, 16)
	if _, err := f.Read(buf); err != nil && info.Size() > 0 {
		return verdict{"error", fmt.Sprintf("open succeeded but read failed: %v", err)}
	}
	return verdict{"ok", byteCount(info.Size())}
}

// reportSQLite records which sqlite3 we'd be shelling out to. The spike
// deliberately avoids a Go SQLite driver: adding a dependency is a decision
// for the v1 plan, not for throwaway probes.
func reportSQLite() {
	path, err := exec.LookPath("sqlite3")
	if err != nil {
		fmt.Println("sqlite3:         NOT FOUND on PATH — probes that read databases will fail")
		return
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		fmt.Printf("sqlite3:         %s (version check failed: %v)\n", path, err)
		return
	}
	fmt.Printf("sqlite3:         %s\n                 %s", path, out)
}

func byteCount(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for i := n / unit; i >= unit; i /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
