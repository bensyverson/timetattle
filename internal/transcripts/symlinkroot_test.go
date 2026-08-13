package transcripts

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCorpus fills root with a small but structurally complete transcript
// tree: one project directory, one main session, and one subagent file under
// it, so a walk that stops at the top level is as visible as one that never
// starts.
func writeCorpus(t *testing.T, root string) int {
	t.Helper()
	proj := filepath.Join(root, "-Users-ben-git-acorn")
	writeFile(t, filepath.Join(proj, "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"start work"`),
		assistantLine("a1", "2026-07-28T10:00:05.000Z", "/Users/you/git/acorn", "s1"),
	})
	writeFile(t, filepath.Join(proj, "s1", "subagents", "agent-1.jsonl"), []string{
		userLine("sub1", "2026-07-28T10:00:01.000Z", "/Users/you/git/acorn", "s1", `"dispatched task"`),
	})
	return 3
}

// symlinkedCorpus returns a populated corpus directory, a symlink pointing at
// it, and how many events the corpus holds.
//
// The symlink is deliberately the ROOT itself rather than something inside the
// tree: a root CONTAINING a symlink is a different case that already worked,
// and testing only that is exactly how this defect survived in
// internal/fsobserve until task TAKiT.
func symlinkedCorpus(t *testing.T) (real, link string, want int) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "actual")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want = writeCorpus(t, real)
	link = filepath.Join(base, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	return real, link, want
}

// readerWithWarnings returns a Reader over root plus the buffer its warnings
// land in, so a test can assert on what was REPORTED and not only on what was
// returned. Zero events with a warning and zero events without one are
// different outcomes, and confusing them is this defect's whole shape.
func readerWithWarnings(t *testing.T, root string) (*Reader, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	r := NewReader(root)
	r.Log = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return r, buf
}

// TestCollectWalksARootThatIsItselfASymlink is the core regression.
// filepath.WalkDir calls os.Lstat on its own root argument and never follows a
// symlink there, so a root that IS a symlink to the corpus arrived at the walk
// callback as a non-directory entry: the walk never descended, listFiles
// returned nothing, and Collect returned no events, no error and no warning.
//
// Measured 2026-08-01 against this machine's real corpus: 722,463 events via
// ~/.claude/projects, 0 via a symlink to it, err=nil. Symlinking ~/.claude
// into a dotfiles repository is a common setup, and it would have silently
// zeroed the largest source in the project.
func TestCollectWalksARootThatIsItselfASymlink(t *testing.T) {
	real, link, want := symlinkedCorpus(t)

	viaReal, err := NewReader(real).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect via the real path: %v", err)
	}
	if len(viaReal) != want {
		t.Fatalf("the real path yielded %d events, want %d; the fixture is wrong, not the walk", len(viaReal), want)
	}

	viaLink, err := NewReader(link).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect via the symlinked root: %v", err)
	}
	if len(viaLink) != len(viaReal) {
		t.Fatalf("got %d events through a symlinked root, want the %d the real path yields",
			len(viaLink), len(viaReal))
	}
}

// TestCollectReportsTheSameLocatorsThroughASymlinkedRoot pins this package's
// equivalent of fsobserve's re-rooting invariant.
//
// It is deliberately NOT the same mechanism. fsobserve derives a Locator from
// the walked path, so walking a resolved root would rewrite every Locator (on
// macOS /var resolves to /private/var, which is where every one of these
// fixtures lives). This package never does: every Locator comes from the
// record's own cwd field, so there is nothing to map back and no
// registeredPath equivalent to write. This test is what makes that claim
// falsifiable rather than an assertion in a comment -- if a future change ever
// starts deriving a Locator from the file's path, this fails.
func TestCollectReportsTheSameLocatorsThroughASymlinkedRoot(t *testing.T) {
	real, link, _ := symlinkedCorpus(t)

	viaReal, err := NewReader(real).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect via the real path: %v", err)
	}
	viaLink, err := NewReader(link).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect via the symlinked root: %v", err)
	}
	if len(viaLink) != len(viaReal) {
		t.Fatalf("got %d events through the symlink, %d through the real path", len(viaLink), len(viaReal))
	}
	for i := range viaReal {
		if viaReal[i] != viaLink[i] {
			t.Fatalf("event %d differs by route:\n via real: %+v\n via link: %+v", i, viaReal[i], viaLink[i])
		}
		if viaLink[i].Locator != "/Users/you/git/acorn" {
			t.Errorf("Locator = %q, want the record's own cwd", viaLink[i].Locator)
		}
	}
}

// TestCollectSeesSubagentFilesThroughASymlinkedRoot guards the one thing about
// this package that a naive "just walk the resolved path" could still get
// wrong: subagent-ness is decided by the walked path's immediate parent
// directory name, so a resolved walk must still land inside a subagents/
// directory and classify what it finds there the same way.
func TestCollectSeesSubagentFilesThroughASymlinkedRoot(t *testing.T) {
	_, link, want := symlinkedCorpus(t)

	r := NewReader(link)
	files, err := r.listFiles()
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("listed %d transcript files through a symlinked root, want 2 (one main, one subagent)", len(files))
	}
	var subagents int
	for _, f := range files {
		if f.isSubagent {
			subagents++
		}
	}
	if subagents != 1 {
		t.Errorf("classified %d of %d files as subagent transcripts, want exactly 1", subagents, len(files))
	}

	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != want {
		t.Errorf("got %d events, want %d", len(events), want)
	}
}

// TestCollectReportsARootItCouldNotWalk is the half of the fix that matters
// more than the walk. Every way a root can yield nothing BECAUSE IT COULD NOT
// BE READ has to say so and name itself; see
// TestCollectDoesNotReportAnEmptyRoot for the other side of the same line.
func TestCollectReportsARootItCouldNotWalk(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(t *testing.T, base string) string
	}{
		{
			name: "a path that is not there at all",
			root: func(t *testing.T, base string) string {
				return filepath.Join(base, "no-such-corpus")
			},
		},
		{
			name: "a symlink whose target has been deleted",
			root: func(t *testing.T, base string) string {
				link := filepath.Join(base, "dangling")
				if err := os.Symlink(filepath.Join(base, "gone"), link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a symlink to a file rather than a directory",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.jsonl")
				writeFile(t, file, []string{userLine("u1", "2026-07-28T10:00:00.000Z", "/x", "s1", `"hi"`)})
				link := filepath.Join(base, "alias")
				if err := os.Symlink(file, link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a plain file given as the root",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.jsonl")
				writeFile(t, file, []string{userLine("u1", "2026-07-28T10:00:00.000Z", "/x", "s1", `"hi"`)})
				return file
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t, t.TempDir())
			r, logged := readerWithWarnings(t, root)
			events, err := r.Collect(context.Background(), time.Time{})
			if err != nil {
				t.Fatalf("Collect returned an error rather than tolerating and reporting: %v", err)
			}
			if len(events) != 0 {
				t.Errorf("got %d events from a root that cannot be walked as a directory", len(events))
			}
			if logged.Len() == 0 {
				t.Fatal("a root that could not be walked collected nothing and said nothing; " +
					"'zero because unwalkable' is indistinguishable from 'zero because empty'")
			}
			if !bytes.Contains(logged.Bytes(), []byte(root)) {
				t.Errorf("the warning does not name the root it could not walk: %s", logged.String())
			}
		})
	}
}

// TestCollectReportsAnUnreadableRoot covers the fifth unwalkable case, which
// this package answers with an ERROR rather than a warning and has since it
// was written: unlike one project directory further down, a root that refuses
// to be read has no "the rest of the corpus is still fine" to fall back on.
// What matters for this task is that it is not silent and that it names the
// root -- which form that takes is the package's existing, deliberate choice.
func TestCollectReportsAnUnreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits do not deny this process")
	}
	root := filepath.Join(t.TempDir(), "unreadable")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	writeCorpus(t, root)
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	r, logged := readerWithWarnings(t, root)
	events, err := r.Collect(context.Background(), time.Time{})
	if len(events) != 0 {
		t.Errorf("got %d events from an unreadable root", len(events))
	}
	if err == nil && logged.Len() == 0 {
		t.Fatal("an unreadable root collected nothing, returned no error and logged nothing")
	}
	if err != nil && !bytes.Contains([]byte(err.Error()), []byte(root)) {
		t.Errorf("the error does not name the root it could not read: %v", err)
	}
}

// TestCollectDoesNotReportAnEmptyRoot is the constraint that stops the lazy
// fix of warning whenever a root yields no events. A machine where Claude Code
// has been installed but has held no conversation yet is an ordinary, correct
// zero and must stay silent, or the warning that means "this root was never
// read" becomes noise nobody looks at.
func TestCollectDoesNotReportAnEmptyRoot(t *testing.T) {
	r, logged := readerWithWarnings(t, t.TempDir())
	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from an empty directory", len(events))
	}
	if logged.Len() != 0 {
		t.Errorf("an empty but perfectly readable root was reported as a problem: %s", logged.String())
	}
}
