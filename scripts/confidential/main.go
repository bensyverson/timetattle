// Command confidential audits a repository for material that must not reach a
// public mirror: client-identifying symbols, contract figures, and secret-shaped
// assignments. It is a pre-publication gate, not a rewriter — it never edits
// anything, it only reports, and with -gate it fails.
//
// # The names live outside the repository
//
// The symbols to hunt are supplied at run time, via -names or the
// TIMETATTLE_CLIENT_NAMES environment variable. They are deliberately NOT
// compiled in. A gate that hard-coded the real client names could never report
// itself clean, and publishing the auditor would publish the very thing it
// audits. This mirrors the rule internal/registry already argues for contract
// values: identity may live in the repo, the sensitive particulars may not.
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
// adjudicated, -skip path:line clears that one site explicitly at run time —
// the same shape scripts/pseudonymize already uses for the identical problem
// — rather than the tool silently guessing or a real leak on a neighbouring
// line going unreported because the whole file got waved through.
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
//	go run ./scripts/confidential -dir . -names a,b,c
//	go run ./scripts/confidential -dir . -names a,b,c -git       # every object, incl. unreachable
//	go run ./scripts/confidential -dir . -names a,b,c -git -gate # non-zero exit if anything is found
//
// The -git mode reads every object the store holds, reachable or not, which is
// the only way to see an amended-away commit that is still sitting in
// .git/objects. It is the mode that matters before publication: the working tree
// will look clean long before the repository is.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
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
	KindClient   = "client"
	KindRate     = "rate"
	KindSecret   = "secret"
	KindIdentity = "identity"
)

// Scanner carries the sensitive particulars, which are supplied at run time and
// never compiled in. Names are client symbols; Deny is a list of literal values
// — a developer team ID, an account name — that must be reported wherever they
// appear. Skip is the set of path:line sites a human has already reviewed and
// cleared, such as a genuine English use of a short client name; see -skip.
type Scanner struct {
	Names []string
	Deny  []string
	Skip  map[Site]bool
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
		if sc.Skip[Site{path, lineNo}] {
			continue
		}
		if len(sc.Names) > 0 {
			// Deescape only for identifier extraction: a backslash-letter escape
			// such as \b is not an identifier character, so \bflonk\b must yield
			// the token "flonk", not "bflonk". Every other check below still runs
			// against the original line.
			for _, ident := range identRe.FindAllString(symbolscan.Deescape(line), -1) {
				if matchesName(ident, sc.Names) {
					out = append(out, Finding{path, lineNo, KindClient, ident, strings.TrimSpace(line)})
				}
			}
		}
		// Known-sensitive literals. Reported by position only: neither the value
		// nor the surrounding line is echoed, or the report becomes the leak.
		for _, d := range sc.Deny {
			if d != "" && strings.Contains(line, d) {
				out = append(out, Finding{path, lineNo, KindIdentity, "<denied literal>", "<redacted>"})
			}
		}
		if m := certRe.FindStringSubmatch(line); m != nil && !placeholderRe.MatchString(m[1]) {
			out = append(out, Finding{path, lineNo, KindIdentity, "Developer ID common name", "<redacted>"})
		}
		for _, m := range homePathRe.FindAllStringSubmatch(line, -1) {
			if !homePlaceholders[strings.ToLower(m[1])] {
				out = append(out, Finding{path, lineNo, KindIdentity, "home-directory path", "<redacted>"})
			}
		}
		for _, re := range rateRes {
			for _, m := range re.FindAllString(line, -1) {
				if bareDigitDollarRe.MatchString(m) {
					continue
				}
				out = append(out, Finding{path, lineNo, KindRate, m, strings.TrimSpace(line)})
			}
		}
		for _, m := range secretRe.FindAllString(line, -1) {
			name := m
			if before, _, ok := strings.Cut(m, "="); ok {
				name = before
			}
			// never echo the value, not even into our own report
			out = append(out, Finding{path, lineNo, KindSecret, name, "<redacted assignment>"})
		}
	}
	return out
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

// parseSkipSites turns "path:line" entries into a Site set. A malformed entry
// -- no colon, or a non-numeric line -- is dropped rather than aborting the
// gate; mirrors scripts/pseudonymize's parseSkips.
func parseSkipSites(entries []string) map[Site]bool {
	out := map[Site]bool{}
	for _, e := range entries {
		p, l, ok := strings.Cut(e, ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(l)
		if err != nil {
			continue
		}
		out[Site{p, n}] = true
	}
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

func main() {
	dir := flag.String("dir", ".", "repository root to audit")
	nameList := flag.String("names", os.Getenv("TIMETATTLE_CLIENT_NAMES"),
		"comma-separated client symbols to hunt (or set TIMETATTLE_CLIENT_NAMES)")
	denyList := flag.String("deny", os.Getenv("TIMETATTLE_DENY_LITERALS"),
		"comma-separated literal values to flag anywhere, such as a developer team ID (or set TIMETATTLE_DENY_LITERALS)")
	kind := flag.String("kind", "", "report only this kind: client, rate, secret or identity")
	gitMode := flag.Bool("git", false, "also scan every git object, including unreachable ones")
	verbose := flag.Bool("v", false, "list every finding, not just the summary")
	gate := flag.Bool("gate", false, "exit non-zero if anything is found")
	skipList := flag.String("skip", "",
		"comma-separated path:line sites a human has reviewed and cleared, such as a genuine English use of a short client name")
	flag.Parse()

	sc := Scanner{Names: splitList(*nameList), Deny: splitList(*denyList), Skip: parseSkipSites(splitList(*skipList))}
	if len(sc.Names) == 0 {
		fmt.Fprintln(os.Stderr, "note: no -names given, so no client symbols are reported")
	}
	if len(sc.Deny) == 0 {
		fmt.Fprintln(os.Stderr, "note: no -deny given, so known literals such as a team ID are not reported")
	}

	findings, files := scanTree(*dir, sc)
	fmt.Printf("working tree: %d text files scanned, %d findings\n", files, len(findings))
	report(findings, *kind, *verbose)

	if *gitMode {
		gf, n, err := scanGitObjects(*dir, sc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		fmt.Printf("\ngit objects: %d text objects scanned, %d findings\n", n, len(gf))
		report(gf, *kind, *verbose)
		findings = append(findings, gf...)
	}

	if *gate && len(findings) > 0 {
		os.Exit(1)
	}
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
