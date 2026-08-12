// Command confidential audits a repository for material that must not reach a
// public mirror: client-identifying symbols, contract figures, and secret-shaped
// assignments. It is a pre-publication gate, not a rewriter — it never edits
// anything, it only reports, and with -gate it fails.
//
// # The names live outside the repository, and are DERIVED rather than recalled
//
// The symbols to hunt are never compiled in. A gate that hard-coded the real
// client names could not report itself clean, and publishing the auditor would
// publish the very thing it audits. This mirrors the rule internal/registry
// already argues for contract values: identity may live in the repo, the
// sensitive particulars may not.
//
// They are also no longer typed in. -names and -deny still work and are still
// added to the list, but the list itself is built from this machine's registry
// index, config and keychain — see derive.go, which carries the argument in
// full. The short version: a tool that takes what to look for as an argument is
// only as good as the argument, and this one reported clean for five days over
// a tree holding fourteen registered directory names, because the list it was
// handed was the four somebody thought of on one afternoon.
//
// Derived names arrive in two tiers. A name from the config is sensitive BY
// CONSTRUCTION — it has a pseudonym already, or it is being billed — and is
// reported as a client finding. A registered name nobody has classified is
// reported as a CANDIDATE: not a leak, an unanswered question, which stays open
// until a human records the answer in ~/.timetattle/publication-gate.json.
//
// # What runs it
//
// Nothing did, for the first week of its life, and three client-name leaks
// landed in published documents in one three-day gap between hand runs. Two
// triggers now exist, and they ask DIFFERENT questions:
//
//	scripts/git-hooks/pre-commit   -staged: does this commit ADD anything
//	                               unpublishable, measured against the same
//	                               files at HEAD?
//	make gate                      -git: is the repository publishable AS IT
//	                               STANDS, working tree and every object in
//	                               .git, including unreachable ones?
//
// Why a hook and not CI is argued in the hook itself, and the argument is not
// the usual one: the names deliberately live outside the repository, so the
// only machine that can run this check is the one holding ~/.timetattle. See
// docs/content/docs/confidentiality.md.
//
// # Three exits, because there are three outcomes
//
//	0  it ran and found nothing
//	1  -gate, and it found something
//	2  it could not build its list, so it has judged NOTHING
//
// The third is the whole point. A gate that silently passes when it cannot find
// its names is worse than no gate, because it reports clean; three commits in
// this repository have already gone out on a skip that named a conclusion
// instead of a precondition. A -gate run that cannot build its list does not
// scan, prints no count of findings, and exits 2.
//
// # Why this is not a grep
//
// The obvious measurement, `grep -rio '\bname\b'`, is wrong in both directions.
//
// It can OVER-report: a short client name may also be ordinary English. Note
// that a word-boundary grep is already narrower than it looks — for a client
// named "cat" it does not match "catalog" or "category", because there is no
// word boundary inside either — so the usual statement of that hazard overstates
// it. What survives is the genuine standalone English use, which a machine
// cannot distinguish from a client reference and a human must adjudicate. Once
// adjudicated, -skip path:line:anchor clears that one site explicitly at run
// time — the same shape scripts/pseudonymize already uses for the identical
// problem — rather than the tool silently guessing or a real leak on a
// neighbouring line going unreported because the whole file got waved through.
//
// The anchor is what stops that exemption rotting. A bare path:line slides onto
// a DIFFERENT line the instant something is inserted above it, and a slid
// exemption does not stop working: it silently clears a line nobody reviewed,
// which is indistinguishable from a pass. So a site carries a digest of the
// line that was reviewed, and a mismatch clears nothing and reports itself.
// -gate refuses an unanchored skip outright.
//
// It also UNDER-reports, which is the more dangerous direction: a word boundary
// cannot see realFoo, fooHours or TestFoo, all of which are client references
// and none of which `\bfoo\b` matches. This tool splits every identifier into
// camelCase and underscore components and compares components, so those are
// caught while an English word that merely contains the name is not.
//
// It also missed a name written inside a regex escape, such as `\bfoo\b` --
// exactly how this project's own audit commands are quoted in project/
// documents. A naive scan reads the 'b' of \b as an ordinary identifier
// character and tokenises the whole thing as "bfoo", so no finding is ever
// emitted for the name beside it. internal/symbolscan.Deescape treats a
// letter immediately preceded by a backslash as a boundary rather than an
// identifier character, and scripts/pseudonymize uses the same rule, so the
// auditor and the rewriter cannot drift apart on this one case.
//
// # What gets scanned
//
// -dir defaults to auditing what git would actually publish: tracked files,
// plus untracked files that are not gitignored (so a brand new file nobody
// has run `git add` on yet is not invisible just because staging hasn't
// happened). It deliberately does NOT walk the raw filesystem: a machine
// that has run an agent accumulates .claude/worktrees/, gitignored and full
// of stale, unpublishable copies of whatever every past agent touched, and a
// raw walk over that can never come back clean. If -dir does not point at a
// git worktree -- for instance a plain directory populated by exporting
// `git ls-files` elsewhere first -- scanning falls back to a full walk of
// that directory, since there is no git index to ask.
//
// # Usage
//
//	go run ./scripts/confidential -dir .              # the tree, list derived
//	go run ./scripts/confidential -staged             # what this commit records
//	go run ./scripts/confidential -classify           # the classification file to review and save
//	make gate                                         # the publication gate: tree + every git object
//	go run ./scripts/confidential -dir . -names a,b,c # extra names, added to the derived list
//
// The -git mode reads every object the store holds, reachable or not, which is
// the only way to see an amended-away commit that is still sitting in
// .git/objects. It is the mode that matters before publication: the working tree
// will look clean long before the repository is.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bensyverson/timetattle/internal/gitenv"
	"github.com/bensyverson/timetattle/internal/symbolscan"
)

// Finding kinds.
const (
	KindClient = "client"
	KindRate   = "rate"
	KindSecret = "secret"
	// KindIdentity is a developer identifier: a team ID, a certificate common
	// name, a real home-directory path.
	KindIdentity = "identity"
	// KindCandidate is a REGISTERED name nobody has classified. It is not a
	// leak; it is an unanswered question, and the gate says so in those words.
	KindCandidate = "candidate"
	// KindSkip is a -skip site whose anchor no longer matches the line there.
	// The exemption was not applied and the site is reported: a rotted skip
	// must fail closed, loudly.
	KindSkip = "skip"
)

// Scanner carries the sensitive particulars, which are supplied at run time and
// never compiled in. Names are client symbols; Deny is a list of literal values
// — a developer team ID, an account name — that must be reported wherever they
// appear. Skip is the set of path:line sites a human has already reviewed and
// cleared, such as a genuine English use of a short client name; see -skip.
type Scanner struct {
	Names []string
	// Candidates are registered names whose sensitivity nobody has ruled on;
	// see derive.go for why they are a separate tier from Names.
	Candidates []string
	Deny       []string
	Skip       map[Site]bool
	// Anchor is the digest of the line each skipped site was reviewed ON. A
	// site with an anchor is cleared only while that anchor still matches, so
	// a skip that has rotted -- because a line was inserted above it -- exempts
	// nothing instead of silently exempting its new neighbour.
	Anchor map[Site]string
}

// Site identifies one line in one file, the unit -skip clears.
type Site struct {
	Path string
	Line int
}

var (
	identRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

	// Monetary amounts and explicit hourly rates. Deliberately narrow: words such
	// as "invoice" and "retainer" are legitimate domain vocabulary throughout the
	// design prose, and flagging them would bury the actual figures.
	rateRes = []*regexp.Regexp{
		regexp.MustCompile(`\$[0-9][0-9,]*(?:\.[0-9]{2})?k?\b`),
		regexp.MustCompile(`(?i)[0-9]+\s*(?:/|per\s+)(?:hr|hour)\b`),
	}

	// A secret-NAMED assignment that actually carries a value. The name on its own
	// is not a leak — CLAUDE.md narrates this incident by name, deliberately.
	secretRe = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|APIKEY|API_KEY|CREDENTIAL)[A-Z0-9_]*=\S{8,}`)

	// A signing certificate's common name identifies the developer even without
	// the team ID beside it, and in practice the two are separated by a line wrap,
	// so the label alone has to be the trigger.
	certRe = regexp.MustCompile(`Developer ID (?:Application|Installer):\s*(.+)`)

	// A home-directory path names an actual account, independent of any client
	// or contract figure -- this is the "developer identifier" AGENTS.md's
	// confidentiality rule already names alongside those. /Users/<segment> and
	// /home/<segment> are the two shapes this project's own docs, code and
	// fixtures use; the segment is captured so it can be checked against
	// homePlaceholders below.
	homePathRe = regexp.MustCompile(`/(?:Users|home)/([A-Za-z][A-Za-z0-9_.-]*)`)

	// Forms that documentation and scripts legitimately use as placeholders.
	placeholderRe = regexp.MustCompile(`(?i)\b(someone|teamid|team_id|your[_ ]?(?:name|team)|example|xxxx+)\b`)

	// A bare single digit is never a real dollar figure in this project's
	// contracts, which run at least fifty dollars an hour or a four-figure
	// lump sum -- and IS exactly the shape of a shell positional parameter,
	// $0/$1/$2, which appears throughout scripts/*.sh. Filtering it removes
	// that false-positive class and nothing else: every genuine rate fixture
	// below is two or more characters after the sign.
	bareDigitDollarRe = regexp.MustCompile(`^\$[0-9]$`)
)

// homePlaceholders are the home-path segments this project treats as already
// neutral, not a real account. "you" is the one docs/content/docs has settled
// on for every console sample (see the task that added this check); "shared"
// is a real macOS system directory (/Users/Shared), not a per-user home, and
// would otherwise be a permanent false positive. Matching is case-insensitive.
var homePlaceholders = map[string]bool{
	"you":    true,
	"shared": true,
}

// selfFixturePath and fixtureMarker exist so the gate can pass on its own
// repository. This tool's own test fixtures are deliberately rate-, secret-
// and identity-shaped -- that is the whole point of pinning the patterns the
// gate hunts for -- so a scan of the tracked tree would otherwise trip on its
// own tests forever, and a gate that can never exit 0 is not a gate.
//
// The obvious fix, excluding scripts/confidential/ (or every _test.go)
// wholesale, is exactly the shape of the hole this tool exists to close: a
// real leak pasted anywhere in that directory, or in any test file anywhere
// in the repo, would go unaudited from then on. Instead the exemption is
// narrowed on two independent axes: it names exactly one file, and even
// within that one file it silences only a line carrying an explicit,
// visible marker. An unmarked line in this same file -- a client name added
// to a new test case and forgotten -- is still audited exactly like the rest
// of the tree, and the marker is never recognised outside this one path, so
// it cannot function as a general-purpose bypass comment.
const (
	selfFixturePath = "scripts/confidential/main_test.go"
	fixtureMarker   = "gate:fixture"
)

// Finding is one place where sensitive material was seen. Text is a trimmed copy
// of the line for context, except for secrets, where it is redacted.
type Finding struct {
	Path  string
	Line  int
	Kind  string
	Match string
	Text  string
	// Anchor is a digest of the line this finding sits on. It identifies the
	// finding across a file's revisions without carrying its content, which is
	// what lets the staged scan tell a leak that MOVED from one that was ADDED,
	// and what a -skip site is anchored to. Never the text itself: a digest can
	// be printed and pasted, and the text of a finding is the leak.
	Anchor string
}

// splitIdent breaks an identifier into camelCase and underscore components, so
// that "realAcme" yields ["real","Acme"] and "ACMERepo" yields ["ACME","Repo"],
// while "catalog" stays whole and so never matches a client named "cat".
func splitIdent(s string) []string {
	var parts []string
	for chunk := range strings.SplitSeq(s, "_") {
		if chunk == "" {
			continue
		}
		r := []rune(chunk)
		start := 0
		for i := 1; i < len(r); i++ {
			prevLower := isLower(r[i-1]) || isDigit(r[i-1])
			// split at aB, and at the tail of a run such as ACMERepo -> ACME|Repo
			if isUpper(r[i]) && (prevLower || (i+1 < len(r) && isUpper(r[i-1]) && isLower(r[i+1]))) {
				parts = append(parts, string(r[start:i]))
				start = i
			}
		}
		parts = append(parts, string(r[start:]))
	}
	return parts
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// scanText reports every sensitive item in content, with 1-based line numbers.
func scanText(path, content string, sc Scanner) []Finding {
	var out []Finding
	for n, line := range strings.Split(content, "\n") {
		lineNo := n + 1
		anchor := lineAnchor(line)
		at := func(kind, match, text string) Finding {
			return Finding{Path: path, Line: lineNo, Kind: kind, Match: match, Text: text, Anchor: anchor}
		}
		// This tool's own test fixtures need to be rate-, secret- and
		// identity-shaped on purpose; see the doc comment on selfFixturePath.
		if path == selfFixturePath && strings.Contains(line, fixtureMarker) {
			continue
		}
		// A human has already reviewed and cleared this exact line, most often
		// the genuine-English-use case the doc comment describes: a machine
		// cannot tell it apart from a real client reference, so a person has
		// to. -skip records that decision explicitly at run time rather than
		// baking a silent exception into the tool.
		//
		// An ANCHORED site is cleared only while the line it was reviewed on is
		// still the line at that number. A path:line list rots the instant
		// something is inserted above it, and a rotted skip does not merely
		// stop working -- it silently exempts a DIFFERENT line, which is a hole
		// in the gate that reads exactly like a pass. So a mismatch clears
		// nothing and is itself reported, carrying the anchor that would clear
		// the site as it stands now.
		if sc.Skip[Site{path, lineNo}] {
			want, anchored := sc.Anchor[Site{path, lineNo}]
			if !anchored || want == anchor {
				continue
			}
			out = append(out, at(KindSkip,
				"stale -skip site; the reviewed line is gone, the anchor here is now "+anchor, "<redacted>"))
		}
		if len(sc.Names) > 0 || len(sc.Candidates) > 0 {
			// Deescape only for identifier extraction: a backslash-letter escape
			// such as \b is not an identifier character, so \bflonk\b must yield
			// the token "flonk", not "bflonk". Every other check below still runs
			// against the original line.
			for _, ident := range identRe.FindAllString(symbolscan.Deescape(line), -1) {
				if matchesName(ident, sc.Names) {
					out = append(out, at(KindClient, ident, strings.TrimSpace(line)))
					continue
				}
				// A registered name nobody has classified. Reported as an open
				// question rather than as a leak, and in its own kind, so the
				// two are never added together in a summary.
				if matchesName(ident, sc.Candidates) {
					out = append(out, at(KindCandidate, ident, strings.TrimSpace(line)))
				}
			}
		}
		// Known-sensitive literals. Reported by position only: neither the value
		// nor the surrounding line is echoed, or the report becomes the leak.
		for _, d := range sc.Deny {
			if d != "" && strings.Contains(line, d) {
				out = append(out, at(KindIdentity, "<denied literal>", "<redacted>"))
			}
		}
		if m := certRe.FindStringSubmatch(line); m != nil && !placeholderRe.MatchString(m[1]) {
			out = append(out, at(KindIdentity, "Developer ID common name", "<redacted>"))
		}
		for _, m := range homePathRe.FindAllStringSubmatch(line, -1) {
			if !homePlaceholders[strings.ToLower(m[1])] {
				out = append(out, at(KindIdentity, "home-directory path", "<redacted>"))
			}
		}
		for _, re := range rateRes {
			for _, m := range re.FindAllString(line, -1) {
				if bareDigitDollarRe.MatchString(m) {
					continue
				}
				out = append(out, at(KindRate, m, strings.TrimSpace(line)))
			}
		}
		for _, m := range secretRe.FindAllString(line, -1) {
			name := m
			if before, _, ok := strings.Cut(m, "="); ok {
				name = before
			}
			// never echo the value, not even into our own report
			out = append(out, at(KindSecret, name, "<redacted assignment>"))
		}
	}
	return out
}

// lineAnchor identifies a line by its content without carrying it: the first 16
// hex digits of the SHA-256 of the line with surrounding whitespace removed.
//
// Trimmed, so re-indenting a line neither rots a -skip anchor nor makes the
// staged scan call a moved line new. A digest rather than the text, because
// both of its users get printed: a -skip site is typed into commands and pasted
// into project/ documents, and a finding's own report must never echo what it
// found.
func lineAnchor(line string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(line)))
	return hex.EncodeToString(sum[:8])
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseSkipSites turns "path:line" and "path:line:anchor" entries into a Site
// set. A malformed entry -- no colon, or a non-numeric line -- is dropped
// rather than aborting the gate; mirrors scripts/pseudonymize's parseSkips.
func parseSkipSites(entries []string) map[Site]bool {
	out := map[Site]bool{}
	for _, e := range entries {
		if site, _, ok := parseSkipEntry(e); ok {
			out[site] = true
		}
	}
	return out
}

// parseSkipAnchors reads the third field of the entries that carry one: the
// digest of the line a human actually reviewed. Sites without one are absent
// from the map rather than present with an empty string, so "not anchored" and
// "anchored to nothing" cannot be confused; see unanchoredSkips.
func parseSkipAnchors(entries []string) map[Site]string {
	out := map[Site]string{}
	for _, e := range entries {
		if site, anchor, ok := parseSkipEntry(e); ok && anchor != "" {
			out[site] = anchor
		}
	}
	return out
}

func parseSkipEntry(e string) (Site, string, bool) {
	parts := strings.SplitN(e, ":", 3)
	if len(parts) < 2 {
		return Site{}, "", false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return Site{}, "", false
	}
	anchor := ""
	if len(parts) == 3 {
		anchor = strings.TrimSpace(parts[2])
	}
	return Site{parts[0], n}, anchor, true
}

// unanchoredSkips lists the sites cleared without saying WHICH line was
// reviewed. They still work for an exploratory run, where a human is reading
// every line of the output anyway. A -gate run refuses them: its whole output
// is an exit code, and an exemption that has silently slid onto a different
// line is invisible in one.
func unanchoredSkips(sites map[Site]bool, anchors map[Site]string) []Site {
	var out []Site
	for s := range sites {
		if _, ok := anchors[s]; !ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func matchesName(ident string, names []string) bool {
	for _, part := range splitIdent(ident) {
		for _, n := range names {
			if strings.EqualFold(part, n) {
				return true
			}
		}
	}
	return false
}

func looksBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// gitCommand builds an exec.Cmd for git args, with GIT_DIR and friends
// stripped from the inherited environment so -C is never silently
// overridden -- the rule internal/gitenv holds once for every git-shelling
// package in this repository, rather than a copy carried here. This project's
// own pre-commit hook is a real example of why it matters -- its "go test"
// step inherits GIT_DIR from the `git commit` that spawned it -- and a
// pre-publication gate is realistically run from inside exactly this kind of
// hook or CI step. Without stripping, the gate would silently audit the
// wrong tree and report a false all-clear: the same failure shape as Defect
// 1, with the whole repository as the blind spot instead of one line. Every
// git invocation in this file goes through here. See
// TestScanTreeDoesNotLeakGitDirIntoTheGitCommand.
func gitCommand(args ...string) *exec.Cmd {
	return gitenv.Command("git", args...)
}

// scanTree audits root, defaulting to exactly what git would publish: files
// already tracked, plus untracked files that are not gitignored (a brand new
// file nobody has `git add`ed yet is still about to be published, and must
// not be invisible to the gate just because staging hasn't happened). This is
// deliberately not "tracked files only" -- see TestScanTreeIncludesUntrackedFilesThatAreNotIgnored
// -- and it is deliberately not a raw filesystem walk, which is how the gate
// came to scan .claude/worktrees/: every agent worktree that has ever run on
// a machine, none of it tracked, none of it publishable, and enough of it to
// make -gate permanently unable to pass.
//
// If root is not itself a git worktree, scanTree falls back to a full walk.
// This is the shape the confidentiality scrub actually used: export
// `git ls-files` into a fresh directory and audit that directory raw. That
// directory is not a repository, so there is no git index to ask.
func scanTree(root string, sc Scanner) ([]Finding, int) {
	rels, err := gitPublishableFiles(root)
	if err != nil {
		return scanFiles(root, walkFiles(root), sc)
	}
	return scanFiles(root, rels, sc)
}

// gitPublishableFiles lists root's tracked files plus its untracked-but-not-
// ignored files, relative to root. It errors if root is not inside a git
// worktree, which callers use as the signal to fall back to a raw walk.
func gitPublishableFiles(root string) ([]string, error) {
	b, err := gitCommand("-C", root, "ls-files", "-z",
		"--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var out []string
	for p := range strings.SplitSeq(strings.TrimRight(string(b), "\x00"), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// walkFiles lists every file under root by a raw filesystem walk, skipping
// the directories a plain walk should never descend into regardless of git.
func walkFiles(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".jj":
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// scanFiles reads and scans each of rels (relative to root), skipping
// anything unreadable or binary.
func scanFiles(root string, rels []string, sc Scanner) ([]Finding, int) {
	var out []Finding
	files := 0
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || looksBinary(b) {
			continue
		}
		files++
		out = append(out, scanText(rel, string(b), sc)...)
	}
	return out, files
}

// gitObject is one line of `cat-file --batch-check`'s output: an object hash
// and its type.
type gitObject struct {
	Hash string
	Type string
}

// listGitObjects lists every object in root's store, reachable or not -- the
// only view that shows an amended-away commit still present in .git/objects.
func listGitObjects(root string) ([]gitObject, error) {
	b, err := gitCommand("-C", root, "cat-file", "--batch-all-objects",
		"--batch-check=%(objectname) %(objecttype)").Output()
	if err != nil {
		return nil, fmt.Errorf("listing objects: %w", err)
	}
	var objs []gitObject
	lines := bufio.NewScanner(bytes.NewReader(b))
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) != 2 {
			continue
		}
		objs = append(objs, gitObject{Hash: f[0], Type: f[1]})
	}
	return objs, nil
}

// fixtureBlobHashes returns the set of blob hashes that were ever recorded at
// selfFixturePath in the tree of some commit object in objs -- reachable or
// not, matching listGitObjects's own "every object in the store" contract. A
// blob is content-addressed, not path-addressed: the object itself carries no
// path, so this is the only way a scanned blob can be told "this IS (a
// historical version of) main_test.go" rather than merely looking like it.
// See the doc comment on selfFixturePath for why the exemption is scoped this
// narrowly in the first place.
func fixtureBlobHashes(root string, objs []gitObject) map[string]bool {
	hashes := map[string]bool{}
	for _, o := range objs {
		if o.Type != "commit" {
			continue
		}
		// ls-tree resolves a commit to its tree automatically, and the
		// pathspec after -- prunes the walk to the one path, so this costs
		// one git invocation per commit object in the store, not per file
		// inside it.
		out, err := gitCommand("-C", root, "ls-tree", "-r", o.Hash, "--", selfFixturePath).Output()
		if err != nil {
			// A malformed or partially-fetched commit object: skip it rather
			// than abort the whole gate over one bad object.
			continue
		}
		for ln := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
			fields := strings.Fields(ln)
			if len(fields) >= 3 && fields[1] == "blob" {
				hashes[fields[2]] = true
			}
		}
	}
	return hashes
}

// scanGitObjects reads every object in the store, reachable or not. This is the
// only view that shows an amended-away commit still present in .git/objects.
func scanGitObjects(root string, sc Scanner) ([]Finding, int, error) {
	objs, err := listGitObjects(root)
	if err != nil {
		return nil, 0, err
	}
	fixtureBlobs := fixtureBlobHashes(root, objs)

	var out []Finding
	n := 0
	for _, o := range objs {
		if o.Type != "blob" && o.Type != "commit" {
			continue
		}
		content, err := gitCommand("-C", root, "cat-file", o.Type, o.Hash).Output()
		if err != nil || looksBinary(content) {
			continue
		}
		n++
		label := o.Type + " " + o.Hash[:12]
		if o.Type == "blob" && fixtureBlobs[o.Hash] {
			// Scan under the real path so the exact same per-line
			// gate:fixture check scanText already applies to the working
			// tree also applies here -- then relabel the findings so a
			// report still names which historical object they came from.
			fs := scanText(selfFixturePath, string(content), sc)
			for i := range fs {
				fs[i].Path = label + " (" + selfFixturePath + ")"
			}
			out = append(out, fs...)
			continue
		}
		out = append(out, scanText(label, string(content), sc)...)
	}
	return out, n, nil
}

// Exit codes. They are three because there are three outcomes, and a gate that
// collapsed the last two would answer "is this publishable" with "I could not
// tell" while looking exactly like "yes".
const (
	exitClean       = 0
	exitFindings    = 1
	exitCannotJudge = 2
)

func main() {
	dir := flag.String("dir", ".", "repository root to audit")
	nameList := flag.String("names", os.Getenv("TIMETATTLE_CLIENT_NAMES"),
		"comma-separated client symbols to hunt, ADDED to the derived list (or set TIMETATTLE_CLIENT_NAMES)")
	denyList := flag.String("deny", os.Getenv("TIMETATTLE_DENY_LITERALS"),
		"comma-separated literal values to flag anywhere, added to the derived list (or set TIMETATTLE_DENY_LITERALS)")
	kind := flag.String("kind", "", "report only this kind: client, candidate, rate, secret, identity or skip")
	gitMode := flag.Bool("git", false, "also scan every git object, including unreachable ones")
	staged := flag.Bool("staged", false, "audit the content this commit would record, against the same files at HEAD")
	verbose := flag.Bool("v", false, "list every finding, not just the summary")
	gate := flag.Bool("gate", false, "exit non-zero if anything is found, and refuse to run at all if the list cannot be built")
	classify := flag.Bool("classify", false, "print a classification file naming every unclassified registered name now in the tree, and exit")
	deriveNames := flag.Bool("derive", true, "build the list from this machine's registry, config and keychain")
	configPath := flag.String("config", envOr("TIMETATTLE_GATE_CONFIG", defaultUserFile("config.json")),
		"the config to derive reference-project and engagement names from (or set TIMETATTLE_GATE_CONFIG)")
	registryPath := flag.String("registry", envOr("TIMETATTLE_GATE_REGISTRY", defaultUserFile("registered-directories.json")),
		"the registry index to derive registered names from (or set TIMETATTLE_GATE_REGISTRY)")
	gatePath := flag.String("classification", envOr("TIMETATTLE_GATE_CLASSIFICATION", defaultUserFile("publication-gate.json")),
		"the file recording which registered names a human has ruled publishable (or set TIMETATTLE_GATE_CLASSIFICATION)")
	skipList := flag.String("skip", "",
		"comma-separated path:line:anchor sites a human has reviewed and cleared; -gate requires the anchor")
	flag.Parse()

	sc := Scanner{
		Names:  splitList(*nameList),
		Deny:   nil,
		Skip:   parseSkipSites(splitList(*skipList)),
		Anchor: parseSkipAnchors(splitList(*skipList)),
	}
	// Hand-supplied literals are vetted exactly like derived ones. The false
	// finding this gate produced against scripts/sign.sh came from a
	// placeholder typed into this very flag.
	for _, lit := range splitList(*denyList) {
		accepted, err := acceptDenyLiteral(lit)
		if err != nil {
			fmt.Fprintln(os.Stderr, "note:", err)
			continue
		}
		sc.Deny = append(sc.Deny, accepted)
	}

	var d derived
	if *deriveNames {
		d = derive(derivePaths{Config: *configPath, Registry: *registryPath, Gate: *gatePath})
		sc.Names = append(sc.Names, d.Names...)
		sc.Candidates = d.Candidates
		sc.Deny = append(sc.Deny, d.Deny...)
	}
	listOut := io.Writer(os.Stdout)
	if *classify {
		listOut = os.Stderr
	}
	printList(listOut, d, sc, *deriveNames)

	// THE PRECONDITION, AND WHY IT COMES BEFORE THE SCAN. Three commits in this
	// repository have gone out on a skip that named a conclusion instead of a
	// precondition, and a gate is the worst possible place to repeat that: its
	// output is an exit code, and "I hunted an empty list" is indistinguishable
	// from "there is nothing here" once it has been reduced to one. So a -gate
	// run that cannot build its list does not scan, does not print a count of
	// findings, and does not exit 0.
	if *gate {
		if reasons := cannotJudge(d, sc, *deriveNames, *skipList); len(reasons) > 0 {
			fmt.Println("\nPRECONDITION NOT MET -- this run cannot judge whether the tree is publishable.")
			for _, r := range reasons {
				fmt.Println("  " + r)
			}
			fmt.Println("\nNothing was scanned and nothing is being reported clean.")
			os.Exit(exitCannotJudge)
		}
	}

	if *classify {
		printClassification(*dir, sc)
		return
	}

	var findings []Finding
	if *staged {
		res, err := scanStaged(*dir, sc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(exitCannotJudge)
		}
		fmt.Printf("\nstaged for this commit: %d text file(s) of %d path(s), %d finding(s) added, %d carried from HEAD\n",
			res.Files, len(res.Paths), len(res.Added), len(res.Carried))
		// Carried findings are printed, always. They are not this commit's
		// doing and they are not nothing: a run that mentioned only additions
		// would read as a clean tree.
		if len(res.Carried) > 0 {
			fmt.Println("  carried (already at HEAD, not introduced here):")
			report(res.Carried, *kind, *verbose)
		}
		if len(res.Added) > 0 {
			fmt.Println("  added by this commit:")
			report(res.Added, *kind, *verbose)
		}
		findings = res.Added
	} else {
		f, files := scanTree(*dir, sc)
		fmt.Printf("\nworking tree: %d text files scanned, %d findings\n", files, len(f))
		report(f, *kind, *verbose)
		findings = f
	}

	if *gitMode {
		gf, n, err := scanGitObjects(*dir, sc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(exitCannotJudge)
		}
		fmt.Printf("\ngit objects: %d text objects scanned, %d findings\n", n, len(gf))
		report(gf, *kind, *verbose)
		findings = append(findings, gf...)
	}

	if *gate && len(findings) > 0 {
		fmt.Printf("\nverdict: %d finding(s), against %s\n", len(findings), listSummary(sc))
		os.Exit(exitFindings)
	}
	fmt.Printf("\nverdict: nothing found, against %s\n", listSummary(sc))
}

// defaultUserFile is ~/.timetattle/<name>, following store.DefaultPath and
// registry.DefaultIndexPath. A home directory that cannot be resolved yields
// an empty path, which the derivation then reports as an unreadable source
// rather than as an empty list.
func defaultUserFile(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".timetattle", name)
}

// envOr lets each derivation source be redirected, which is how the gate itself
// is tested end to end: pointing it at a fixture registry is the only way to
// prove the hook fires on a planted name without committing a real one. It is
// not a security boundary and does not pretend to be — anyone who can set an
// environment variable can already pass --no-verify. It is an accident
// detector, and the accident it detects is a pasted capture.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// listSummary says what the verdict rests on. CLAUDE.md's rule for a tool that
// takes what to look for as an argument: derive the list from something that
// cannot go stale, or state in the output which list produced the verdict.
// This does both.
func listSummary(sc Scanner) string {
	return fmt.Sprintf("%d name(s), %d unclassified registered name(s) and %d denied literal(s)",
		len(sc.Names), len(sc.Candidates), len(sc.Deny))
}

// printList prints the provenance of every part of the list, before anything is
// scanned, whether or not the run is a gate.
//
// The writer is a parameter because -classify's stdout is a FILE: the hook's
// own refusal message says to redirect it straight into
// ~/.timetattle/publication-gate.json, so in that mode everything that is not
// the JSON must go to stderr or the saved file cannot be read back. gatecheck's
// fourth check is the round-trip that caught this.
func printList(w io.Writer, d derived, sc Scanner, didDerive bool) {
	fmt.Fprintf(w, "list: %s\n", listSummary(sc))
	if !didDerive {
		fmt.Fprintln(w, "  -derive=false: nothing was read from this machine; the list is exactly what was passed in")
		return
	}
	for _, s := range d.Sources {
		fmt.Fprintln(w, "  "+s.String())
	}
	for _, r := range d.Refused {
		fmt.Fprintln(w, "  "+r)
	}
}

// cannotJudge lists every reason this run has no standing to report on the
// tree. Each entry names what was OBSERVED to be missing, never what that
// implies.
func cannotJudge(d derived, sc Scanner, didDerive bool, skipList string) []string {
	var out []string
	for _, s := range d.Unavailable() {
		out = append(out, s.String())
	}
	if len(sc.Names) == 0 && len(sc.Candidates) == 0 {
		if didDerive {
			out = append(out, "the derived list is empty: no name to hunt was found anywhere on this machine")
		} else {
			out = append(out, "-derive=false and -names is empty, so there is no client symbol to hunt")
		}
	}
	if len(sc.Deny) == 0 {
		out = append(out, "no denied literal could be derived or was supplied, so a team ID or certificate name would not be reported")
	}
	for _, s := range unanchoredSkips(sc.Skip, sc.Anchor) {
		out = append(out, fmt.Sprintf(
			"-skip %s:%d carries no anchor: a bare path:line exemption slides onto a different line the moment one is inserted above it",
			s.Path, s.Line))
	}
	if entries := splitList(skipList); len(entries) > 0 && len(sc.Skip) != len(entries) {
		out = append(out, fmt.Sprintf("-skip was given %d entr(ies) and %d parsed; a mistyped exemption clears nothing and must not pass unnoticed",
			len(entries), len(sc.Skip)))
	}
	return out
}

// printClassification emits the classification file a human has to rule on:
// every registered name that is actually in the tree and has not been
// classified yet. It never decides anything -- a name that belongs to a client
// must be pseudonymised, not listed here -- which is why it prints to stdout
// for a person to read before saving.
func printClassification(dir string, sc Scanner) {
	findings, _ := scanTree(dir, sc)
	seen := map[string]bool{}
	var names []string
	for _, f := range findings {
		if f.Kind != KindCandidate {
			continue
		}
		for _, c := range sc.Candidates {
			if strings.EqualFold(c, f.Match) && !seen[strings.ToLower(c)] {
				seen[strings.ToLower(c)] = true
				names = append(names, c)
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })

	fmt.Fprintln(os.Stderr, "\nREAD THIS LIST BEFORE SAVING IT. Every name below is registered on this")
	fmt.Fprintln(os.Stderr, "machine AND present in the tracked tree. Listing one here says it is safe to")
	fmt.Fprintln(os.Stderr, "publish. A name that identifies a client is NOT safe to publish and does not")
	fmt.Fprintln(os.Stderr, "belong in this file: run scripts/pseudonymize over it instead.")

	out, err := json.MarshalIndent(gateFile{
		Version:          gateFileVersion,
		PublishableNames: names,
		DeniedLiterals:   []string{},
	}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitCannotJudge)
	}
	fmt.Println(string(out))
}

func report(fs []Finding, only string, verbose bool) {
	byKind := map[string]int{}
	byTerm := map[string]int{}
	for _, f := range fs {
		if only != "" && f.Kind != only {
			continue
		}
		byKind[f.Kind]++
		byTerm[f.Kind+" "+strings.ToLower(f.Match)]++
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-7s %d\n", k, byKind[k])
	}
	terms := make([]string, 0, len(byTerm))
	for t := range byTerm {
		terms = append(terms, t)
	}
	sort.Slice(terms, func(i, j int) bool { return byTerm[terms[i]] > byTerm[terms[j]] })
	for i, t := range terms {
		if i >= 12 {
			break
		}
		fmt.Printf("      %-44s %d\n", t, byTerm[t])
	}
	if verbose {
		for _, f := range fs {
			if only != "" && f.Kind != only {
				continue
			}
			fmt.Printf("  %s:%d [%s] %s\n", f.Path, f.Line, f.Kind, f.Match)
		}
	}
}
