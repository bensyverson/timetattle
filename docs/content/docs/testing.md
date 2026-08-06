---
title: "Running the tests"
weight: 95
description: "The two test runs — the machine-independent one and the deliberate corpus one — what each verifies, what the ordinary run does NOT verify, and the gate that keeps the two from merging back together."
---

# Running the tests

There are two runs.

```sh
go test ./...                            # machine-independent. Green means green.
go test -count=1 -tags realdata ./...    # the corpus checks, on a machine that has a corpus.
```

**The first run has no skips at all.** A skip appearing there is a regression:
either a machine-coupled test escaped the tag, or something portable stopped
being portable. Measured 2026-08-02 with `go test ./... -v`: **1264 PASS, 0
SKIP, 0 FAIL** counting top-level tests, 1336 counting subtests too — which is
the denominator `scripts/foreignsuite` prints.

**The first one does not run the corpus checks.** They are not skipped; they are
not compiled. If you have only run `go test ./...`, you have verified the code
against fixtures the repository carries, and you have verified nothing against
real data on a real disk. That is a deliberate trade and the rest of this page
is about why.

**The corpus run needs `-count=1`, and leaving it off produces a false green.**
Go's test cache keys on the stat of every file a test read — mtime and size.
Those are exactly the two detectors `internal/snapshot/biome.go` had already
rejected for Biome segments, and on 2026-08-03 the reason became visible: a
tombstone pass zeroed 37,331 payload bytes and flipped 946 index entries from
state 1 to state 3 **without changing either**. The file is still stamped
Jul 9 12:21 and still 524,288 bytes; only ctime moved. So the cache saw an
unchanged input and served three stale passes for tests that fail on a real
run:

```sh
go test -tags realdata ./internal/biome/ ./internal/manifest/ ./internal/store/
# ok ... (cached)   ok ... (cached)   ok ... (cached)
go test -count=1 -tags realdata ./internal/biome/ ./internal/manifest/ ./internal/store/
# 6 failures: internal/biome 2, internal/manifest 1, internal/store 3
```

This is the failure this page already exists to prevent, wearing a different
disguise: not a suite that skipped quietly, but one that did not run at all.
The full argument is in `project/private/2026-08-03-segb-tombstones.md`.

## What each run covers

| | `go test ./...` | `go test -tags realdata ./...` |
| --- | --- | --- |
| Runs on a fresh clone | yes | yes, but most cases skip |
| Reads `~/.timetattle`, `~/Library/Biome`, `~/.claude`, a Motion vault | never | yes |
| Reads other repositories beside the checkout | never | yes (`internal/jobs`) |
| Queries Spotlight | never | yes (`internal/manifest`) |
| Depends on the machine's timezone or home directory | no | yes |
| Suitable for CI, or for a pre-commit hook | yes | no |

The pre-commit hook (`scripts/git-hooks/pre-commit`) runs the first one. That is
the right choice — a hook that failed because a laptop had not been harvested
recently would be turned off within a week — but it means a commit that passes
every hook has not been checked against real data. Run the second one yourself
before merging anything that changes how evidence is read.

## Why a corpus check exists at all

`CLAUDE.md` states the rule this project learned the hard way: **constructed
cases must sit BESIDE real-data checks, and neither substitutes for the other.**

The canonical case is an over-general supersession rule that passed against the
entire live Biome stream, deleting nothing, purely because no interval in it
happened to share an endpoint with a bare stop. It would have begun destroying
records the first time a second source arrived. Real data validated a wrong
rule. A constructed case caught it.

The reverse is just as common. `internal/chains` refuses to split a filename at
its dashes; the constructed case for that rule produces zero wrong joins against
the whole real save-as corpus, because no `dark-*` sibling family happens to
exist there. Only a fixture puts one on a disk.

So the corpus checks are not legacy and they are not going away. What changed is
that they no longer masquerade as unit tests.

## The failure this replaced

A test that cannot find its corpus calls `t.Skip`, and `go test` prints `ok`
either way. Before the tag, a real-data test and a unit test were
indistinguishable from the outside, so the suite **failed open**: on any machine
but one it reported green while running almost none of them.

That is not a hypothetical. On 2026-08-02 it fired three times in a day.

1. A pseudonym rename pointed eight fixtures at a directory that does not exist.
   Eight tests began skipping. Nothing went red.
2. Migration `0011` landed while the live store still sat at `0010`. Ten more
   began skipping. Nothing went red.
3. `cmd/timetattle`'s `TestMain` points `HOME` at a temp directory, so a test
   reading "the newest real snapshot" resolved a snapshot directory inside that
   temp home and skipped on **every** run, on the machine holding the snapshots
   included, from the day `TestMain` landed. Nothing ever said so.

Each time, coverage vanished and the suite stayed green. The tag does not stop a
corpus check from skipping — a machine without a Motion vault genuinely cannot
run the Motion tests — but it moves the skip out of the run you use to decide
whether the code works, and into the run whose whole purpose is real data.

## The three instruments

**1. The build tag.** A test goes behind `//go:build realdata` when its
assertion is a claim about the data on one particular machine that no fixture
could carry. Not because it currently skips, and not because it happens to read
the machine today — if a fixture could carry it, it gets a fixture instead. See
`internal/storefixture` and `internal/fsfixture` for the two that were built
rather than tagged.

**2. `scripts/foreignsuite`.** Runs the suite with `HOME` pointed at an empty
directory and prints the skip census:

```console
$ scripts/foreignsuite ./...
foreignsuite: HOME=/var/folders/.../tmp.djFj6hpV1D tags=none
1336 PASS  0 SKIP  0 FAIL
```

An empty home rather than no home at all: `os.UserHomeDir` fails outright when
`HOME` is unset, which turns thirty-odd portable tests red for a reason that has
nothing to do with the corpus. It also takes `-tags`, so
`scripts/foreignsuite -tags realdata ./...` shows what the corpus run would do
on a laptop that has never been harvested.

**3. `internal/corpusgate`.** A test in the ordinary suite that fails when an
untagged test file *calls* one of the machine resolvers — `store.DefaultPath`,
`snapshot.DefaultDir`, `os.UserHomeDir`, `os.Getwd` and the rest. It is the only
one of the three that fires *before* the coverage is lost, rather than after
somebody thinks to go looking.

It matches syntax rather than text, so naming a resolver in a comment is fine.
Legitimate untagged uses are listed in `internal/corpusgate/suite_test.go` with
a written reason each, and a second test deletes an exemption that has stopped
being true.

## Where the boundary falls

A test belongs behind the tag when its assertion is about **one machine's data**.
It does not belong there merely because it reads something real:

- `internal/gitlog/realdata_test.go` reads this repository's own git history.
  That travels with the checkout, so it is not the machine, and it stays in the
  ordinary run.
- `internal/sessions/calendar_realdata_test.go` and
  `internal/adjustments/realdata_test.go` read anonymised CSV corpora that are
  committed. Real data, no machine.
- The `-store` guards in `cmd/timetattle` resolve `~/.timetattle/timetattle.db`
  in order to prove that nothing was written to it. Tagging a guard deletes the
  protection it exists to give.

And it does not belong there just because it skips. `internal/filedays`' socket
test skipped on macOS because a `t.TempDir()` path exceeds the 104-byte
`sun_path` limit — an assertion about `Scan`, blocked by a platform limit, with
no corpus involved anywhere. It was fixed rather than tagged (it binds under a
short `os.MkdirTemp` name now), which is what "not a corpus test" is supposed to
mean.

## What a skip message may say

**A skip message names the missing precondition and nothing else.** It states
what the test OBSERVED to be missing, in the words of the observation it
actually made.

`go test ./...` has zero skips, so every skip you will ever read comes from the
corpus run, and it is the only thing telling you why a check did not happen.
A wrong one sends the next reader after a bug that is not there.

| | |
| --- | --- |
| ✅ | `/Users/ben/.timetattle/config.json maps "acorn" to /Users/ben/git/<real>, which is not present on this machine: stat …: no such file or directory` |
| ✅ | `/Users/ben/.timetattle/timetattle.db is at migration 10 and this binary carries 0011_review_shown.sql` |
| ✅ | `the 2026-07-28 report names 4 projects, none of them "birch"` |
| ❌ | `birch is not attributable on 2026-07-28; the registry has not resolved it` |
| ❌ | `the autosave vault is empty; Motion prunes it` |
| ❌ | `segment 12ab…segb has been pruned` |

The third row is what the fourth used to say, and the fourth was **false**. The
registry had resolved that day perfectly well — `TestTheConcurrentDayStillCarriesAttribution`,
built on the very same `metrics.Report`, passes only when it has. What was
missing was the *name*: the live store holds the real client name and the test,
after the pseudonym scrub, asks for the pseudonym. The test observed "this
report does not contain a project called birch" and reported "attribution is
broken".

This is `CLAUDE.md`'s cardinal rule in a new place: **absence of a row is not an
observation.** Distinguish "observed X", "observed not-X", and "nothing was
recorded" every time.

**The first row shipped false too, in a different shape, and task paDFg is why
it no longer reads the way it used to.** Before that task, `realProject(t)` in
`internal/chains/realdata_test.go` and `internal/fsobserve/realdata_test.go`
resolved the pseudonym literally: `filepath.Join(home, "git", "acorn")`. The
pseudonym scrub renamed the fixtures but nobody's disk changed name to match,
so that path never existed and six published-figure tests in `internal/chains`
plus one in `internal/fsobserve` skipped with "`/Users/ben/git/acorn` is not
present on this machine" — which reads exactly like the second (good) row
above, a directory genuinely absent. It was not. The directory was there,
under its real name; what was missing was a NAME MAPPING, and that is a third
state the old message could not express at all.

`config.ResolveReferenceProject` (`internal/config/reference.go`) now
distinguishes all three ways this can fail, because they are three different
missing preconditions:

1. **No config file at all** — nobody has written `~/.timetattle/config.json`
   on this machine.
2. **A config file with no mapping for this pseudonym** — the file exists and
   parses, but its `reference_projects` section does not name `"acorn"` (it
   may name others, or none; the message counts them, the same shape as the
   `birch` row above).
3. **A mapped path that is not on disk** — the config names a real path for
   the pseudonym, and that path is the one genuinely missing.

Only the third state may read like "this is not present on this machine";
collapsing the second into the third is precisely the defect this section
exists to name. The mapping itself lives outside the repository entirely, in
`~/.timetattle/config.json`'s `reference_projects` — never in a fixture, never
in a comment, never in this repository's history — because the pseudonym
exists so the real name never has to be. See
`project/private/2026-08-03-reference-project-resolution.md`.

Three shapes to watch for, all of which shipped here:

1. **A cause the test did not check.** `segment X has been pruned` was written
   in an `if err != nil` branch that then discarded `err`. Pruning is the usual
   cause; a revoked Full Disk Access is another, and only one of them is worth
   getting out of bed for. Carry the error.
2. **A conclusion drawn wider than the window.** `nothing has been sessionized
   in the live store` came from `st.Sittings(ctx, start, end)` for a single day.
   It now reads `the live store holds no derivation covering 2026-07-28`.
3. **Two conditions with one message.** `if err != nil || !info.IsDir()` made an
   unreadable Motion vault indistinguishable from a path that is not a
   directory. Two conditions, two messages.

A skip that reports a *rule* rather than a machine fact is fine, because the
rule can be checked by reading the code: "the source registry holds no `*Reader`;
init registers one only when `DefaultRoot()` resolves at process start" says
where to look without asserting what happened.

**This is enforced by review, not by a checker, and that is deliberate.** The
discriminator between a good message and a bad one is not in the sentence —
`the live store holds no backlight readings for 2026-07-28` (good) and `the
autosave vault is empty` (bad) are the same English shape, and differ only in
whether the statement above the call looked at the thing being described. A
word denylist would be a list of the sentences somebody already wrote, which
is a regression test for the past rather than a rule for the future.

A narrower mechanical rule *was* prototyped — "a skip inside an `if err != nil`
branch must interpolate the error" — and measured against the suite: 153 call
sites, 13 flagged, of which 6 were `err == nil` branches with no error to carry
and 4 were `exec.LookPath("git")` where `git not found on PATH` is the whole
content of the error. Three were real, and are fixed. A gate that needs an
exemption for more than half its own findings on its first run gets switched off
within a week — the same reasoning `internal/corpusgate` gives for matching
syntax instead of text. `internal/corpusgate` covers the axis that *is*
mechanical: which function a test calls.

## Regenerating a fixture

One corpus check is a writer rather than a reader. It rebuilds
`internal/biome/testdata/realfocus.segb` from live donor records, and needs both
gates:

```sh
REGENERATE_BIOME_FIXTURE=1 go test -tags realdata \
    -run TestGenerateRealFocusFixture ./internal/biome/
```

The tag keeps the live stream out of the ordinary suite; the environment
variable keeps a checked-in file from being rewritten by anyone who merely runs
the corpus suite.
