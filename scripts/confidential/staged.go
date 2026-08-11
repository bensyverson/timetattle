package main

// THE STAGED SCAN, AND WHY IT MEASURES AGAINST HEAD.
//
// The whole-tree scan answers "is this repository publishable", which is a
// release question. A pre-commit hook has to answer a narrower one — "does this
// commit ADD anything unpublishable" — for two reasons.
//
// First, correctness: a commit records the INDEX, not the working tree. Half a
// file can be staged, so the copy on disk can be clean while the bytes about to
// be committed are not, and the reverse. Every read below is therefore of a git
// object, never of a file on disk.
//
// Second, adoptability, which is the reason the previous hook wiring never
// existed. The tracked tree today holds hundreds of findings — 236 of them a
// developer home path in test fixtures, which is a real finding of a real class
// this gate added on 2026-08-07 and nobody has scrubbed. A hook that failed on
// all of them would block every unrelated commit until somebody had time to fix
// the backlog, and a hook that blocks unrelated work is bypassed with
// --no-verify and then deleted. That is not a hypothetical failure mode in this
// repository; it is the standing argument internal/corpusgate gives for
// matching syntax instead of text, and docs/content/docs/testing.md gives for
// keeping the corpus suite out of the hook.
//
// So the hook fails on ADDITIONS and PRINTS what it carried. The baseline is
// the same file's own content at HEAD, which is DERIVED rather than a
// checked-in list of exemptions: there is nothing to keep up to date, nothing
// to prune, and no way for it to go stale — the same principle derive.go
// applies to the name list. What it cannot do is shrink the backlog; that is
// the release gate's job, and `make gate` is where the whole tree is judged.

import (
	"fmt"
	"strings"
)

// stagedResult is what one staged scan observed. Carried is as load-bearing as
// Added: a run that printed nothing about the findings it walked past would be
// reporting a clean COMMIT as a clean TREE, which is the confusion this whole
// task exists to end.
type stagedResult struct {
	// Added are findings in the staged content that the file's HEAD version
	// does not account for.
	Added []Finding
	// Carried are findings this commit inherits unchanged.
	Carried []Finding
	// Paths are the staged files that were scanned, and Files how many of them
	// were readable text.
	Paths []string
	Files int
}

// scanStaged audits the content this commit would record: every added, copied,
// modified or renamed path in the index, read out of the index rather than off
// disk, and compared with the same path at HEAD.
func scanStaged(root string, sc Scanner) (stagedResult, error) {
	var res stagedResult

	paths, err := stagedPaths(root)
	if err != nil {
		return res, err
	}
	res.Paths = paths

	for _, rel := range paths {
		staged, ok, err := blobAt(root, ":", rel)
		if err != nil {
			return res, err
		}
		if !ok || looksBinary([]byte(staged)) {
			continue
		}
		res.Files++

		// A path with no version at HEAD -- a new file, or the very first
		// commit -- has no baseline, so everything in it is this commit's
		// doing. That is the shape the leaks this gate was built for actually
		// took: a new project/ document holding a pasted capture.
		var base []Finding
		if head, ok, err := blobAt(root, "HEAD:", rel); err != nil {
			return res, err
		} else if ok && !looksBinary([]byte(head)) {
			base = scanText(rel, head, sc)
		}

		added, carried := splitAgainstBaseline(scanText(rel, staged, sc), base)
		res.Added = append(res.Added, added...)
		res.Carried = append(res.Carried, carried...)
	}
	return res, nil
}

// splitAgainstBaseline separates the findings a file did not already have from
// the ones it did.
//
// A finding is matched to the baseline by KIND, MATCH and the ANCHOR of the
// line it sits on -- never by line number, which is exactly the rot that makes
// a path:line exemption list dangerous: inserting one line above a leak would
// otherwise present every finding below it as new, and the hook would cry wolf
// on the first reformat. Counted as a multiset, so a SECOND copy of a line that
// already exists is still an addition.
func splitAgainstBaseline(found, base []Finding) (added, carried []Finding) {
	remaining := map[string]int{}
	for _, f := range base {
		remaining[findingSignature(f)]++
	}
	for _, f := range found {
		sig := findingSignature(f)
		if remaining[sig] > 0 {
			remaining[sig]--
			carried = append(carried, f)
			continue
		}
		added = append(added, f)
	}
	return added, carried
}

// findingSignature identifies a finding across revisions of a file without
// carrying its content: what was found, and a digest of the line it was on.
func findingSignature(f Finding) string {
	return f.Kind + "\x00" + strings.ToLower(f.Match) + "\x00" + f.Anchor
}

// stagedPaths lists what the index holds for this commit: added, copied,
// modified and renamed paths. Deletions are left out -- there is no content to
// audit, and a delete is the direction this gate wants.
func stagedPaths(root string) ([]string, error) {
	b, err := gitCommand("-C", root, "diff", "--cached", "--name-only",
		"--diff-filter=ACMR", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	var out []string
	for p := range strings.SplitSeq(strings.TrimRight(string(b), "\x00"), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// blobAt reads path at a revision -- ":" for the index, "HEAD:" for the last
// commit. A path the revision does not hold is reported as absent rather than
// as an error: a new file has no HEAD version, and an unborn branch has no HEAD
// at all, both of which are ordinary states with a specific meaning.
//
// EXISTENCE IS ASKED SEPARATELY, by the command whose whole job is existence.
// Taking any failure of `git show` as "not there" would fold a real read
// failure -- a corrupt object, a broken repository -- into the same silent
// answer, and a gate that treats an unreadable object as an empty one is the
// false all-clear this tool exists to prevent, wearing yet another disguise.
func blobAt(root, rev, path string) (string, bool, error) {
	if _, err := gitCommand("-C", root, "rev-parse", "--verify", "--quiet", rev+path).Output(); err != nil {
		return "", false, nil
	}
	out, err := gitCommand("-C", root, "show", rev+path).Output()
	if err != nil {
		return "", false, fmt.Errorf("read %s at %q, which git says exists: %w", path, rev, err)
	}
	return string(out), true, nil
}
