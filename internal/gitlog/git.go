package gitlog

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/gitenv"
)

// withoutGitRepoEnv strips the variables git exports into every hook and
// subprocess it runs, naming the repository that invoked it — the same rule
// internal/gitenv holds once for every git-shelling package in this
// repository, because they silently override -C: a git command meant for
// the repo this source was constructed against would instead operate on
// whatever repository invoked the process that is running Timetattle (this
// project's own pre-commit hook is a real example — its "go test" step
// inherits GIT_DIR from the `git commit` that spawned it).
//
// This is a thin forwarding wrapper, not a copy of the rule: it exists so
// internal/gitlog/realdata_test.go's realRepo — deliberately left without
// its own dedicated regression test; see job task OWUF1 — can keep calling
// an unexported helper by that name without this package inventing a second
// implementation of the strip itself. runGit below does not use it; runGit
// goes straight through gitenv.CommandContext, which is what
// TestGitEnvVarsDoNotLeakIntoSubprocess actually proves by mutation.
func withoutGitRepoEnv(env []string) []string {
	return gitenv.Strip(env)
}

// runGit runs git -C repo <args...> and returns stdout. Every git invocation
// in this package goes through here, so gitenv's environment guard and the
// -C targeting are applied exactly once.
func runGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := gitenv.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// repoValid reports an error if repo is not a git repository at all — a
// registered path that is wrong, which this package treats as a
// configuration mistake worth surfacing rather than a silently empty
// collection. This mirrors internal/knowledgec's readable(): a path a
// caller chose, pointing nowhere useful, is an error, not "nothing to
// report".
func repoValid(ctx context.Context, repo string) error {
	if _, err := runGit(ctx, repo, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("%s is not a git repository: %w", repo, err)
	}
	return nil
}

// hasHEAD reports whether repo has at least one commit. A freshly
// `git init`-ed repository has an unborn HEAD, which is a legitimate "no
// history yet" rather than a wrong path — the distinction repoValid above
// exists to preserve.
func hasHEAD(ctx context.Context, repo string) bool {
	_, err := runGit(ctx, repo, "rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// fieldSep and the COMMIT/REFLOG markers below use \x1f (ASCII unit
// separator) and a leading word to delimit git's --pretty=format output.
// \x1f essentially never appears in a commit subject or a reflog message —
// it is a non-printable control character with no keyboard binding — so
// splitting on it is safe in practice, the same trick internal/registry and
// the dayview spike do not need but knowledgeC-style row parsing elsewhere
// in this project relies on for the same reason.
const fieldSep = "\x1f"

// commitRecord is one --pretty=format-parsed line from `git log`, plus the
// --shortstat line that may follow it.
type commitRecord struct {
	hash          string
	isMerge       bool
	authorDate    time.Time
	committerDate time.Time
	stat          Stat
}

const commitLogFormat = "COMMIT" + fieldSep + "%H" + fieldSep + "%P" + fieldSep + "%aI" + fieldSep + "%cI"

var statLineRe = regexp.MustCompile(
	`^\s*(\d+)\s+files?\s+changed(?:,\s*(\d+)\s+insertions?\(\+\))?(?:,\s*(\d+)\s+deletions?\(-\))?\s*$`)

// parseCommitLog parses the output of
// `git log --all --shortstat --pretty=format:<commitLogFormat>`.
//
// git prints an optional shortstat line after a commit's header only when
// that commit has a computable diff: never for a merge (without -m, which
// this package does not pass — see the package doc for why) and never for a
// commit whose tree is byte-identical to its single parent's, which is an
// observed zero rather than a merge's not-applicable. Distinguishing those
// two absences is why isMerge is read from parent count (%P) instead of
// inferred from whether a stat line showed up.
func parseCommitLog(raw []byte) ([]commitRecord, error) {
	var records []commitRecord
	var cur *commitRecord

	for _, line := range splitNonEmptyLines(string(raw)) {
		if rest, ok := strings.CutPrefix(line, "COMMIT"+fieldSep); ok {
			if cur != nil {
				records = append(records, *cur)
			}
			fields := strings.Split(rest, fieldSep)
			if len(fields) != 4 {
				return nil, fmt.Errorf("malformed commit header %q", line)
			}
			hash, parents, authorDate, committerDate := fields[0], fields[1], fields[2], fields[3]
			aI, err := time.Parse(time.RFC3339, authorDate)
			if err != nil {
				return nil, fmt.Errorf("commit %s: parse author date %q: %w", hash, authorDate, err)
			}
			cI, err := time.Parse(time.RFC3339, committerDate)
			if err != nil {
				return nil, fmt.Errorf("commit %s: parse committer date %q: %w", hash, committerDate, err)
			}
			cur = &commitRecord{
				hash:          hash,
				isMerge:       len(strings.Fields(parents)) > 1,
				authorDate:    aI,
				committerDate: cI,
			}
			continue
		}

		if cur == nil {
			return nil, fmt.Errorf("stat line %q appeared before any commit header", line)
		}
		stat, err := parseStatLine(line)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", cur.hash, err)
		}
		cur.stat = stat
	}
	if cur != nil {
		records = append(records, *cur)
	}
	return records, nil
}

func parseStatLine(line string) (Stat, error) {
	m := statLineRe.FindStringSubmatch(line)
	if m == nil {
		return Stat{}, fmt.Errorf("unparseable shortstat line %q", line)
	}
	files, _ := strconv.Atoi(m[1])
	ins, del := 0, 0
	if m[2] != "" {
		ins, _ = strconv.Atoi(m[2])
	}
	if m[3] != "" {
		del, _ = strconv.Atoi(m[3])
	}
	return Stat{FilesChanged: files, Insertions: ins, Deletions: del}, nil
}

// reflogRecord is one --pretty=format-parsed line from `git log -g`.
type reflogRecord struct {
	hash   string
	at     time.Time
	action string
}

const reflogFormat = "REFLOG" + fieldSep + "%H" + fieldSep + "%gd" + fieldSep + "%gs"

// parseReflog parses the output of
// `git log -g --date=iso-strict --pretty=format:<reflogFormat> HEAD`.
//
// %gd expands to "HEAD@{<date>}" when --date is set; the date embedded there
// — not any commit's own author or committer date — is the true local
// wall-clock instant git recorded for that specific reflog entry, which is
// the whole reason this package reads the reflog at all. See the package
// doc for why this differs from a commit's own timestamps.
func parseReflog(raw []byte) ([]reflogRecord, error) {
	var records []reflogRecord
	for _, line := range splitNonEmptyLines(string(raw)) {
		rest, ok := strings.CutPrefix(line, "REFLOG"+fieldSep)
		if !ok {
			return nil, fmt.Errorf("malformed reflog line %q", line)
		}
		fields := strings.SplitN(rest, fieldSep, 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed reflog line %q", line)
		}
		hash, gd, gs := fields[0], fields[1], fields[2]
		at, err := parseReflogSelectorDate(gd)
		if err != nil {
			return nil, fmt.Errorf("reflog entry %s: %w", hash, err)
		}
		action := gs
		if before, _, ok0 := strings.Cut(gs, ": "); ok0 {
			action = before
		}
		records = append(records, reflogRecord{hash: hash, at: at, action: action})
	}
	return records, nil
}

// parseReflogSelectorDate extracts the date git embedded inside a
// "<ref>@{<date>}" selector.
func parseReflogSelectorDate(gd string) (time.Time, error) {
	open := strings.Index(gd, "@{")
	closeIdx := strings.LastIndex(gd, "}")
	if open < 0 || closeIdx < open {
		return time.Time{}, fmt.Errorf("reflog selector %q has no embedded date; was --date=iso-strict passed?", gd)
	}
	return time.Parse(time.RFC3339, gd[open+2:closeIdx])
}

func splitNonEmptyLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func splitFields(line string) []string {
	return strings.Split(line, fieldSep)
}
