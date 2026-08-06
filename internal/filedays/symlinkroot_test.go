package filedays

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// symlinkedTree returns a populated directory, a symlink pointing at it, and
// the counts a correct scan of either must produce.
func symlinkedTree(t *testing.T) (real, link string, want map[string]int) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "actual")
	day1 := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 7, 12, 3, 30, 0, 0, time.Local)
	touch(t, filepath.Join(real, "a.txt"), day1)
	touch(t, filepath.Join(real, "b.txt"), day1)
	touch(t, filepath.Join(real, "sub", "c.txt"), day2)

	link = filepath.Join(base, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	return real, link, map[string]int{"2026-07-11": 2, "2026-07-12": 1}
}

// TestScanWalksARootThatIsItselfASymlink is the core regression, and this
// package's variant was the worst of the three it was found in.
// filepath.WalkDir calls os.Lstat on its own root argument and never follows a
// symlink there, so the walk callback saw the symlink itself as a single
// non-directory entry, counted it, and descended nowhere: a root of three
// files scanned through a symlink returned 1, dated to the day the LINK was
// created. A zero might be noticed; a plausible 1 will not be.
func TestScanWalksARootThatIsItselfASymlink(t *testing.T) {
	real, link, want := symlinkedTree(t)

	viaReal, err := Scan(real, nil)
	if err != nil {
		t.Fatalf("Scan via the real path: %v", err)
	}
	if !reflect.DeepEqual(viaReal, want) {
		t.Fatalf("the real path scanned %v, want %v; the fixture is wrong, not the walk", viaReal, want)
	}

	viaLink, err := Scan(link, nil)
	if err != nil {
		t.Fatalf("Scan via the symlinked root: %v", err)
	}
	if !reflect.DeepEqual(viaLink, viaReal) {
		t.Fatalf("scanning through a symlinked root gave %v, want the %v the real path yields",
			viaLink, viaReal)
	}
}

// TestScanDoesNotCountASymlinkAsAFileItObserved is a SEPARATE defect from the
// root case above, and the fabrication one. It is about entries met DURING a
// walk: a symlink is not a file whose mtime records work someone did, it is a
// pointer whose mtime records when the pointer was made. Counting one invents
// evidence for a day nothing happened on -- worse than the silence this
// project has already shipped twice, because it looks like data.
//
// A symlink to a DIRECTORY is included deliberately: d.IsDir() is false for
// it, so the naive walk counted it as a file too, which is how a link to a
// large tree became exactly one fabricated observation.
func TestScanDoesNotCountASymlinkAsAFileItObserved(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "real-one.txt"), day)
	touch(t, filepath.Join(root, "real-two.txt"), day)
	touch(t, filepath.Join(root, "elsewhere", "target.txt"), day)

	if err := os.Symlink(filepath.Join(root, "real-one.txt"), filepath.Join(root, "link-to-file")); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "link-to-dir")); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	got, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := map[string]int{"2026-07-11": 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan counted %v, want %v; a symlink is not a file this scan observed, "+
			"and counting one fabricates evidence for the day the link was made", got, want)
	}
}

// TestScanDoesNotCountASocketAsAFileItObserved widens the same rule beyond
// symlinks, which is the question the symlink case raises and does not answer:
// nothing that is not a REGULAR file carries an mtime recording somebody's
// work. A unix socket left in a tree by a running program is not an
// observation, and neither is a fifo or a device node.
//
// The root is os.MkdirTemp rather than t.TempDir because t.TempDir names the
// directory after the test: 111 bytes here, against the 104 darwin allows in
// sockaddr_un.sun_path. Binding under it fails with EINVAL, so this assertion
// skipped on every macOS machine — including the one that owns the corpus —
// from the day it was written until 2026-08-02.
func TestScanDoesNotCountASocketAsAFileItObserved(t *testing.T) {
	root, err := os.MkdirTemp("", "sockscan")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "real.txt"), day)

	ln, err := net.Listen("unix", filepath.Join(root, "sock"))
	if err != nil {
		t.Skipf("net.Listen on a unix socket in %s failed: %v", root, err)
	}
	defer ln.Close()

	got, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := map[string]int{"2026-07-11": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan counted %v, want %v; a socket is not a file whose mtime records work", got, want)
	}
}

// TestScanReportsARootItCouldNotWalk is the half of the fix that matters more
// than the walk. This package answers with an error rather than a log warning
// because Scan already returns one for a missing root, Take already collects
// per-root errors and keeps the roots that did scan, and cmd/timetattle
// already prints "filedays: capture lost (...)". See
// TestScanEmptyRootIsNotAnError for the other side of the same line.
func TestScanReportsARootItCouldNotWalk(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(t *testing.T, base string) string
	}{
		{
			name: "a path that is not there at all",
			root: func(t *testing.T, base string) string {
				return filepath.Join(base, "does-not-exist")
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
				file := filepath.Join(base, "not-a-directory.txt")
				touch(t, file, time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
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
				file := filepath.Join(base, "not-a-directory.txt")
				touch(t, file, time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
				return file
			},
		},
		{
			name: "a directory that refuses to be read",
			root: func(t *testing.T, base string) string {
				if os.Geteuid() == 0 {
					t.Skip("running as root; permission bits do not deny this process")
				}
				root := filepath.Join(base, "unreadable")
				touch(t, filepath.Join(root, "a.txt"), time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
				if err := os.Chmod(root, 0o000); err != nil {
					t.Fatalf("Chmod: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
				return root
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t, t.TempDir())
			got, err := Scan(root, nil)
			if err == nil {
				t.Fatalf("a root that could not be walked returned %v and no error; "+
					"'zero because unwalkable' is indistinguishable from 'zero because empty'", got)
			}
			if !strings.Contains(err.Error(), root) {
				t.Errorf("the error does not name the root it could not walk: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("a root that could not be walked still returned counts: %v", got)
			}
		})
	}
}

// TestScanEmptyRootIsNotAnError is the constraint that stops the lazy fix of
// erroring whenever a scan finds nothing. An existing, readable, empty
// directory is an ordinary, correct zero.
func TestScanEmptyRootIsNotAnError(t *testing.T) {
	got, err := Scan(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("an empty but perfectly readable root was reported as a problem: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an empty directory produced counts: %v", got)
	}
}

// TestTakeRecordsTheConfiguredRootNotTheResolvedOne is this package's
// equivalent of fsobserve's Locator re-rooting.
//
// There is no Locator here -- a filedays snapshot produces no events, and
// harvest's own help text says so. What plays the same role is RootScan.Root:
// it is the only thing in a snapshot that says WHICH root a day count belongs
// to, and it is compared against the configured roots by anyone reading the
// snapshots back. Resolving a symlinked root and recording what was walked
// would silently rewrite it -- on macOS /var resolves to /private/var, so
// every root under a temporary directory changes -- and two snapshots of the
// same configured root would stop lining up with each other.
//
// MUTATION PROOF: make Take (or Scan) hand the resolved path onward instead of
// keeping the caller's string, and this test reports a Root under
// /private/var that no configuration file anywhere names.
func TestTakeRecordsTheConfiguredRootNotTheResolvedOne(t *testing.T) {
	_, link, want := symlinkedTree(t)
	dir := t.TempDir()

	res, err := Take([]string{link}, DefaultExcludes, dir, time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if res == nil || len(res.Snapshot.Roots) != 1 {
		t.Fatalf("Take recorded %+v, want exactly one root", res)
	}
	got := res.Snapshot.Roots[0]
	if got.Root != link {
		t.Errorf("snapshot records root %q, want the configured path %q; "+
			"a resolved path is not what any configuration names", got.Root, link)
	}
	if !reflect.DeepEqual(got.Raw, want) {
		t.Errorf("snapshot raw counts are %v, want %v", got.Raw, want)
	}
}
