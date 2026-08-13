# Backlog

Work that is deliberately deferred rather than planned. Nothing here is
scheduled, and nothing here blocks anything. Active work lives in the `job`
task tree — `job orient` is the entry point — and an item only lands here once
a decision has been taken NOT to do it for now.

Each entry says what it is, why it is parked, and what would un-park it. That
last part matters: a backlog without trigger conditions becomes a list nobody
can act on.

---

## Notarize and ship a one-command install

**Parked 2026-08-13.** Was task `11hMe`.

Gatekeeper will not accept a prebuilt binary on a machine that has never seen
this code without Developer ID signing, notarization and stapling. That is the
only thing standing between a prebuilt download and a user who does not build
from source.

**Why parked:** the tool is pre-launch with zero users. Nobody is currently
blocked by the absence of an installer, and the audience that does exist —
developers who clone the repo and run `go install` — is already served by the
self-signed path in `scripts/sign.sh`.

**What already exists, so nobody rebuilds it.** Done under `MH6HS` on
2026-08-01: `scripts/sign.sh` signs with a stable identity, preferring a
Developer ID Application identity when one is present; `--options runtime` and
`--timestamp` are already applied to every signature, both being notarization
prerequisites, both verified working; a Developer ID Application identity exists
in the login keychain with a designated requirement anchored to the team OU
rather than to a single certificate, so renewing the certificate will not break
it; and `xcrun notarytool` is present.

**What is missing:** notary credentials are not stored in the keychain, and the
final acceptance needs a test on a clean machine that has never seen this code.
Both need Ben rather than an agent.

**What would un-park it:** one person who wants to run timetattle and will not
build it from source.

---

## Carry the reasoning out of `project/private/` and into what ships

**Parked 2026-08-13.** Never filed as a task.

At least three shipped files cite a document under `project/private/`, which is
gitignored and does not ship — so a public reader hits a citation they cannot
follow. Found so far: `internal/sessions/sessions.go`,
`docs/content/docs/app-focus.md:69` and `docs/content/docs/testing.md:47`. The
house rule in `CLAUDE.md` is explicit that what ships must stand on its own and
that a comment saying "see the memo" has said nothing.

**Why parked:** all three are pre-existing, none is wrong, and each is a small
piece of judgement about how much of a private measurement to restate in public
rather than a mechanical fix.

**What would un-park it:** making the repository public, which turns three
dangling citations into three visible ones.
