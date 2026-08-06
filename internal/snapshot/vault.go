package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// vaultDigestLength mirrors digestLength: enough of a SHA-256 to make a
// collision not worth worrying about, named separately because the two
// capture mechanisms are allowed to change independently of one another.
const vaultDigestLength = 32

// vaultCaptureSuffix marks a finished vault capture. It is deliberately its
// own constant rather than reusing captureSuffix (".segb.gz"): that name
// asserts the bytes inside are a Biome SEGB segment, which is false for a
// preserved .motn file and would mislead anyone reading the capture
// directory by hand.
const vaultCaptureSuffix = ".gz"

// VaultResult describes what one PreserveVault run did.
type VaultResult struct {
	// Captured lists the files this run wrote.
	Captured []string
	// Unchanged counts files whose exact contents were already held.
	Unchanged int
	// Bytes is the compressed size of what this run wrote.
	Bytes int64
}

// PreserveVault copies every file under root into dir, skipping any whose
// exact contents are already held.
//
// # Why this exists apart from the events a manifest source already reads
//
// internal/timestamped's Reader already turns Motion's autosave vault into
// Point events — a timestamp, a document name, a locator — the moment
// harvest runs. That is sufficient for "when did this composition last
// autosave", and it is not what this function is for.
//
// What it discards is the composition itself: the actual bytes Motion wrote.
// Job task g7v07's controlled test on 2026-07-31 found that a clean close and
// a force-quit both currently leave a composition's autosaves intact, but the
// actual pruning trigger remains unconfirmed — autosaves from 2022 survive on
// one machine while a two-hour session the same week left none at all. Until
// that trigger is understood, any autosave can vanish before the next
// harvest for a reason nothing in the derived Event would have recorded. So
// this preserves the vault the same way Take preserves knowledgeC and
// TakeStreams preserves Biome: as insurance, kept separate from
// interpretation, so a bug in a reader cannot cost history already in hand.
//
// # Why content, not the path, is the dedup key
//
// Biome's TakeStreams already established why: a path or a name can be
// revisited with different bytes behind it, and deduplicating on either
// would silently keep only the last version. Nothing about Motion's vault
// rules that out — the pruning mechanism is unconfirmed — so the same
// content-addressed capture applies here. A file is skipped only when a
// capture already on disk holds the identical bytes; otherwise it is written
// under a name that embeds its digest, so two different versions that ever
// shared a filename both survive.
//
// # What is tolerated
//
// A root that does not exist means Motion, or its vault, is not on this
// machine — the common case for anyone who does not use Motion — and
// PreserveVault reports it the same way TakeStreams reports a stream nobody
// has: (nil, nil), not an error. A file that vanishes between being listed
// and being read is the vault being pruned while this runs, which is
// ordinary for a perishable source and costs only that one file.
//
// # A root that is itself a symlink, and the zero it used to return
//
// filepath.WalkDir calls os.Lstat on its own root argument and does not follow
// a symlink there, so a vault root that IS a symlink used to arrive at the
// callback as a non-directory entry: the walk never descended, and this
// returned a VaultResult with nothing captured and no error. Measured
// 2026-08-01 — 3 files captured by the real path, 0 through a symlink to it.
// Of the three collectors this defect was found in, this one has the most at
// stake, because Motion's vault is perishable and what a "successful" run
// fails to copy is gone for good. A vault living on an external volume with a
// link left behind in ~/Movies is not an exotic setup. vaultWalkRoot resolves
// that one case, and its resolved form is fed to filepath.WalkDir alone.
//
// Following a symlink here is safe in the way it would not be for a function
// that writes into its root: nothing is ever written under root, only read
// from it. Every byte this produces is written under dir.
//
// # Where the resolved path must NOT reach
//
// A capture's destination is dir joined with the file's path RELATIVE TO WHAT
// WAS WALKED, which is this function's equivalent of the Locator invariant
// internal/fsobserve keeps. Computing that relative path against the caller's
// original root while walking the resolved one returns a climb of ".."
// segments — on macOS /var resolves to /private/var, so any vault under a
// temporary directory takes exactly that shape — and the capture would be
// written outside dir altogether, scattering preserved evidence across the
// filesystem instead of collecting it. See
// TestPreserveVaultCapturesUnderTheDestinationWhenRootIsASymlink, which fails
// with a captured path outside the destination if the Rel call below is
// changed back to use root.
//
// # An unwalkable root is not an empty one
//
// A root that exists but cannot be walked as a directory tree — a symlink
// whose target is gone, a symlink to a file, a plain file, a directory that
// refuses to be read — is returned as an error naming the root, not as a
// successful capture of nothing. An existing, readable, empty vault is the
// ordinary zero and returns a VaultResult with nothing in it. A root that is
// simply absent remains (nil, nil): the nil result is itself what
// distinguishes "no Motion on this machine" from "captured nothing", and
// cmd/timetattle already prints nothing at all for it.
func PreserveVault(root, dir string) (*VaultResult, error) {
	walkRoot, ok, err := vaultWalkRoot(root)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	res := &VaultResult{}
	walkErr := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == walkRoot {
				// The root itself refusing to be read is not the vault
				// changing under us; it is the whole capture failing to
				// happen, and reporting it as a successful capture of
				// nothing is the defect this guard exists for.
				return fmt.Errorf("cannot read %s: %w", root, err)
			}
			// A directory that stops being readable mid-walk is the vault
			// changing under us, which is what a perishable source does;
			// losing that one subtree is not worth losing the rest of the
			// run over.
			return nil //nolint:nilerr // deliberate: keep walking
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// The file existed when WalkDir listed it and is gone or
			// unreadable now — Motion pruning it out from under this run,
			// which is the exact race this function exists to lose gracefully
			// rather than fail loudly over.
			return nil
		}

		// Relative to what was WALKED, not to what the caller passed: see
		// "Where the resolved path must NOT reach" above. The two are the
		// same string except when the root was a symlink.
		rel, err := filepath.Rel(walkRoot, path)
		if err != nil {
			return fmt.Errorf("compute a path for %s relative to %s: %w", path, root, err)
		}

		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])[:vaultDigestLength]
		ext := filepath.Ext(rel)
		base := strings.TrimSuffix(rel, ext)
		dst := filepath.Join(dir, base+"-"+digest+ext+vaultCaptureSuffix)

		switch _, err := os.Stat(dst); {
		case err == nil:
			res.Unchanged++
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("cannot check for an existing capture at %s: %w", dst, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("create capture directory %s: %w", filepath.Dir(dst), err)
		}
		n, err := writeCapture(dst, data)
		if err != nil {
			return err
		}
		res.Captured = append(res.Captured, dst)
		res.Bytes += n
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("preserve vault %s: %w", root, walkErr)
	}
	return res, nil
}

// vaultWalkRoot returns the path to hand filepath.WalkDir for root, reporting
// false where there is simply no vault here to preserve.
//
// Three outcomes, and keeping them apart is the point: a resolved path to walk;
// (false, nil) for a root that is absent, which is what a machine without
// Motion looks like; and an error naming the root for every way a root can
// exist and still not be walkable. See PreserveVault's doc for why the absent
// case is the only silent one.
func vaultWalkRoot(root string) (string, bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// os.Stat cannot tell an absent path from a symlink whose target
			// is gone; os.Lstat can, and they mean different things. Nobody
			// pointed a link at a vault by accident.
			if link, lerr := os.Lstat(root); lerr == nil && link.Mode()&os.ModeSymlink != 0 {
				return "", false, fmt.Errorf("the vault root %s is a symlink to something that no longer exists; nothing was preserved", root)
			}
			return "", false, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return "", false, fmt.Errorf("cannot read %s: %w\n\n"+
				"This is almost always a missing Full Disk Access grant. Open\n"+
				"System Settings > Privacy & Security > Full Disk Access, add the\n"+
				"program running Timetattle (your terminal, or the timetattle\n"+
				"binary itself when run from launchd), then restart it.", root, err)
		}
		return "", false, fmt.Errorf("cannot read %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("the vault root %s is not a directory; nothing was preserved", root)
	}

	// os.Stat above followed the symlink; os.Lstat is what says there was one
	// to follow, and filepath.WalkDir would have used exactly this view.
	link, err := os.Lstat(root)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		return root, true, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false, fmt.Errorf("the vault root %s is a symlink that could not be resolved: %w", root, err)
	}
	return resolved, true, nil
}

// VaultDir is where preserved Motion vault captures live under a harvest's
// snapshot directory, alongside BiomeDir, so one path under -dir remains the
// whole of what a harvest has preserved.
func VaultDir(dir string) string { return filepath.Join(dir, "motion-vault") }
