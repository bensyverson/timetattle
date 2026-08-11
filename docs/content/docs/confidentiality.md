---
title: "The publication gate"
weight: 96
description: "What must never reach a public mirror, the check that hunts it, where its list of names comes from now that it derives one instead of being handed one, and why it refuses to answer rather than answering wrongly."
---

# The publication gate

This repository is published. The machine it was built on is not: it holds a
store of real working days against real clients, a registry of every directory
ever blessed, a config carrying rates and contract totals, and a task database
where twenty of fifty-three tasks name a client and four carry money.

Nothing that ships may carry a real client name, a rate, a contract figure or a
developer identifier — **and that includes code comments, test names and
fixtures**, not just prose. `scripts/confidential` is the check. It never edits
anything; `scripts/pseudonymize` is the rewriting half.

```sh
make gate       # is this repository publishable as it stands?
make classify   # print the classification file the gate reads
scripts/gatecheck   # is the gate actually wired, and does it fire?
```

## The mechanism this exists to stop

The repository was scrubbed once. The machine was not, and cannot be: the live
store and the on-disk registry markers still hold the real names, because that
is what they are *for*. So every pasted `metrics`, `review`, `orient` or
`dashboard` capture re-imports exactly what the scrub removed — and this
project's house style asks for pasted captures, in `project/` documents, with a
tool and flags beside every figure.

`dashboard` is the worst of them: its allocation section reads the config's
`engagements`, so a pasted capture carries a rate or a contract total as well as
a name.

Three leaks arrived this way in three days and were found four days late.

## Two triggers, asking two different questions

| | pre-commit hook | `make gate` |
| --- | --- | --- |
| Scope | the content this commit records | the whole tree **and** every object in `.git` |
| Baseline | the same files at `HEAD` | none — everything counts |
| Fails on | what this commit **adds** | anything at all |
| When | automatically, on every commit | before publishing |

The hook judges the **index**, not the working tree: half a file can be staged,
so the copy on disk can be clean while the bytes about to be committed are not.

It fails only on additions, and that is a deliberate concession rather than
laxity. The tracked tree today holds hundreds of findings — most of them a
developer home path in test fixtures, a real class the gate only learned to
catch on 2026-08-07. A hook that failed on all of them would block every
unrelated commit until somebody had time to clear the backlog, and a hook that
blocks unrelated work is bypassed with `--no-verify` and then deleted. So the
hook is adoptable and `make gate` is honest, and the two are not the same
question. Findings the commit merely carries are **printed anyway**: a run that
mentioned only additions would read like a clean tree.

The baseline is derived — the file's own content at `HEAD` — rather than a
checked-in list of exemptions. There is nothing to keep up to date and no way
for it to go stale. A finding is matched to the baseline by what was found and a
**digest of the line it sits on**, never by line number, so inserting a line
above a leak does not present everything below it as new.

## Why a hook and not CI

The usual trade-off says a hook is weak because `--no-verify` bypasses it and a
fresh clone does not have it. Both are true. Neither is the reason CI is not the
answer here.

**The names deliberately do not live in the repository**, so the only machine
that can run this check is the one holding `~/.timetattle`. A CI runner would
necessarily run it with no name source — which now exits 2 saying so, rather
than reporting clean, but a step that can only ever be red or silent is not a
gate. Putting the real client names into a CI secret would publish the list into
a third party in order to keep it from being published, and on a public
repository a fork's pull request does not receive secrets, so the check would go
nameless exactly on untrusted input.

If CI is ever added, the gate belongs in it only in the mode it already has:
one that fails when it cannot find its names.

The hook is installed once per clone — this repository sets `core.hooksPath`, so
check with `scripts/gatecheck` rather than assuming. **Note for worktrees:**
`core.hooksPath` here is an absolute path into the main checkout, so a commit
made from an agent worktree runs the *main* checkout's copy of the hook, not the
worktree's.

## Where the list of names comes from

A tool that takes what to look for as an argument is only as good as the
argument, and a clean report from a short list is indistinguishable from a
correct one. This gate reported clean for five days while fourteen registered
directory names sat in the shipped tree, because the list it was handed was the
four names somebody thought of on one afternoon and nothing connected that list
to the machine. The same defect then appeared in the other input: a deny list
handed a placeholder copied out of an example command produced a finding against
the very redaction that placeholder documents.

So the list is derived, from three places that cannot go stale:

| Source | What it contributes |
| --- | --- |
| `~/.timetattle/registered-directories.json` | every registered directory's name, and through its marker the client and project it declares — exactly what a pasted `metrics` capture prints |
| `~/.timetattle/config.json` | the real path behind each published pseudonym (`reference_projects`), and every name being billed (`engagements`) |
| `security find-identity` | the signing certificate's common name and team ID |

Registering a new client directory therefore widens the gate's list on the next
run, instead of silently widening the surface it cannot see.

### Two tiers, because a registered name is not automatically a client name

This repository's own directory is registered and its name is in three hundred
files. A gate that failed on every registered name would be switched off within
a week.

- **Names** — sensitive by construction: a path that already has a pseudonym, a
  name that is being billed. Finding one is a leak.
- **Candidates** — registered names nobody has ruled on. Finding one is not a
  leak; it is an unanswered question, and the gate stays red until it is
  answered.

The answer is recorded in `~/.timetattle/publication-gate.json`, outside the
repository for the same reason every other sensitive particular is: writing "so
and so is publishable" into a tracked file publishes the name the ruling is
about. It is a separate file from `config.json` rather than a new key in it,
because `config.Load` refuses unknown keys outright — a gate key added there
would fail every `timetattle` command on a machine running an older build,
including the scheduled harvest.

```sh
make classify > ~/.timetattle/publication-gate.json
```

**Read it before saving it.** Every name in it is one this machine knows about
and the tree contains; listing it says it is safe to publish. A name that
identifies a client is not safe to publish and does not belong in that file —
run `scripts/pseudonymize` over it instead.

### Denied literals are vetted, not trusted

A denied literal is matched as a bare substring anywhere in a line, so a bad one
is not a check but a noise generator. Anything placeholder-shaped
(`<team id>`, `TEAMID`, `Someone`, `example`) or shorter than six characters is
**refused, with the reason printed**, rather than hunted — including literals
passed by hand to `-deny`. That is the direct fix for a gate that once reported
a finding against `scripts/sign.sh`'s own redaction.

## Three exits, because there are three outcomes

| Exit | Meaning |
| --- | --- |
| 0 | it ran and found nothing |
| 1 | `-gate`, and it found something |
| 2 | it could not build its list, so it has judged **nothing** |

The third is the one that matters. A gate that silently passes when it cannot
find its names is worse than no gate, because it reports clean — and this
project has shipped three commits on a skip that named a conclusion instead of a
precondition. So a `-gate` run that cannot build its list does not scan, prints
no count of findings, and exits 2:

```console
$ scripts/confidential -dir . -gate      # on a machine with no ~/.timetattle
list: 0 name(s), 0 unclassified registered name(s) and 0 denied literal(s)
  config (absent): …/config.json does not exist, so no reference-project or engagement name is in this list
  registry index (absent): …/registered-directories.json does not exist, so no registered directory, client or project name is in this list

PRECONDITION NOT MET -- this run cannot judge whether the tree is publishable.
  the derived list is empty: no name to hunt was found anywhere on this machine

Nothing was scanned and nothing is being reported clean.
```

Before this existed, the identical run printed `working tree: 1 text files
scanned, 0 findings` and exited 0, over a file containing a client name.

Every run — gate or not — prints the provenance of its list first, and ends with
a verdict naming what that verdict rests on. `CLAUDE.md`'s rule for a tool that
takes what to look for as an argument is to derive the list from something that
cannot go stale, **or** state in the output which list produced the verdict.
This does both.

## Clearing a line a human has reviewed

A word-boundary scan cannot tell a genuine English use of a short client name
from an actual reference. A person has to, and `-skip` records that decision at
run time rather than baking a silent exception into the tool:

```sh
scripts/confidential -dir . -gate -skip project/2026-07-29-a-document.md:142:9f3a1c07b2d45e68
```

The third field is an **anchor**: a digest of the line that was reviewed. A bare
`path:line` exemption slides onto a different line the instant something is
inserted above it, and a slid exemption does not stop working — it silently
clears a line nobody reviewed. So a mismatched anchor clears nothing, reports
itself, and carries the digest that would clear the site as it stands now.
`-gate` refuses an unanchored skip outright.

A digest rather than the line's text, because `-skip` strings get typed into
commands and pasted into `project/` documents, and the text of a reviewed line
is usually the sensitive thing itself.

## What the tool is not

It is not a security boundary against its own operator. `--no-verify` exists,
and so does `TIMETATTLE_GATE_REGISTRY`, which is how `scripts/gatecheck` proves
the gate fires without committing a real name. It is an **accident detector**,
and the accident it detects is a pasted capture.

It also cannot tell you that a name is safe. It can only tell you that a name is
one somebody has already ruled on, or one nobody has.
