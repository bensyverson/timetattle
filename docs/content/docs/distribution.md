---
title: "Distributing a signed release"
weight: 60
description: "The path from a commit to a notarized, stapled .dmg someone else's Mac will open without a Gatekeeper warning, and what a one-command install looks like."
---

# Distributing a signed release

This is for the audience `go install` does not serve: someone who downloads a
prebuilt `timetattle` binary on a Mac that has never seen this repository. A
developer who clones the repo and runs `go install ./cmd/timetattle` gets an
ad-hoc-signed binary that `scripts/sign.sh` can stabilize for Full Disk
Access on their own machine with a self-signed certificate — that path is
already covered and needs nothing here. Gatekeeper on a *different* machine
will not accept a self-signed certificate no matter what; it needs a real
Apple Developer ID identity, notarization, and (for the reasons below) a
disk-image container.

## Why a `.dmg`, not a bare binary or a `.pkg`

A CLI tool is not an app bundle, so submitting it for notarization means
zipping it first. The question this section actually answers is what shape
the *distributed* artifact should be, and specifically whether a notarization
ticket can be stapled directly to a bare Mach-O executable — because if it
can, the zip used for submission does not need to be the thing people
download.

**It cannot.** This was settled by measurement on 2026-08-03, and the answer
decides the artifact shape rather than merely permitting it.

The test that settles it needs a bare Mach-O for which a *real* notarization
ticket already exists — otherwise `stapler` fails at the lookup step for the
boring reason that there is nothing to look up, which is what happened on
2026-08-02 and left the question open. Any notarized third-party CLI binary
serves. Against one, `xcrun stapler staple` finds the ticket, downloads it
successfully, and then fails with **error 73** while trying to write it to
`<path>/Contents/CodeResources` — a *bundle-relative* path, which is
"Not a directory" against a flat executable. The file is left byte-identical.
`stapler` has no code path that embeds a ticket in a bare Mach-O; its own usage
text lists the only three shapes it can write into: "UDIF disk images,
code-signed executable bundles, and signed 'flat' installer packages."

So a container is not a convenience here. It is the only thing that can carry a
ticket. Of the three, a `.dmg` is the one this project can produce today: it
signs with the same **Developer ID *Application*** identity already in the
keychain, whereas a `.pkg` — also considered — needs a **Developer ID
*Installer*** certificate, a different kind that does not exist here
(confirmed via `security find-identity -v -p codesigning`: exactly one
Developer ID identity, and it is the Application kind). Shipping a `.pkg`
today would trade one unmet credential prerequisite for another, for no benefit
a `.dmg` doesn't already give a single CLI binary.

The full trace, including the exact commands and the `ENOTDIR` underneath error
73, is in `project/private/2026-08-03-a-bare-mach-o-cannot-be-stapled.md`.

## Universal, not arm64-only

`modernc.org/sqlite`, this project's SQLite driver, is pure Go — no cgo, so
no cross-compiler toolchain is needed to target Intel from an Apple Silicon
machine. `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build` was verified to
produce a working Mach-O on an arm64 machine, and `lipo -create` combines the
two architecture slices into one binary that `codesign` signs in a single
pass. Since a universal binary costs nothing extra to build, there is no
reason to make Intel users a second-class case.

## Reproducibility

Go embeds a VCS stamp — commit, build time, and whether the tree was dirty —
into every binary built from within a git checkout, automatically, since Go
1.18. `go version -m <binary>` shows it from outside the process; `timetattle
version` surfaces the same thing from inside it, for someone who does not
have the Go toolchain installed. Nothing about this requires the release
script to compute or hardcode a version string. `scripts/release.sh` refuses
to build from a dirty working tree, because a binary a dirty build produced
is not what `timetattle version` says it is.

## The release script

```sh
scripts/release.sh
```

Run from a clean working tree, this builds a universal binary, signs it with
whatever identity `scripts/sign.sh` finds (and refuses to continue if that
identity is not a Developer ID Application certificate — a release cannot
ship self-signed), packages it as a `.dmg`, signs the `.dmg` with the same
identity, and submits it to Apple's notary service with `xcrun notarytool
submit --wait`. If that succeeds it staples the ticket and runs
`stapler validate` and `spctl --assess` as local checks.

**It stops at the submit step if no notary credentials are stored**, which is
the case on every machine today. `xcrun notarytool history --keychain-profile
notarytool` returns "No Keychain password item found" — there is no
certificate this project can generate that solves this; it needs either an
app-specific password for the enrolled Apple ID or an App Store Connect API
key, both of which only Ben can create. `scripts/release.sh` prints the exact
`xcrun notarytool store-credentials` command to run, including the team ID
(read back from the signature it just made, not hardcoded anywhere) — this is
a one-time setup step. After it is done once, `scripts/release.sh` needs
nothing further to notarize and staple.

Nothing in `scripts/release.sh` or `scripts/sign.sh` hardcodes a signing
identity, team identifier, or Apple account. Both auto-detect the identity
present in the local keychain and accept `TIMETATTLE_SIGN_IDENTITY` as an
override.

## The acceptance test, and what actually demonstrates it

The real bar is: a binary downloaded on a machine that has never run this
project opens without a Gatekeeper prompt. Two commands approximate that
locally without a second machine:

```sh
spctl --assess -vvv --type install timetattle-<commit>.dmg
xcrun stapler validate -v timetattle-<commit>.dmg
```

(`--type install`, not `--type open` — `--type open` gave the uninformative
"Insufficient Context" against a test `.dmg`; `--type install` gives the
correct, discriminating verdict.)

Both are approximations, not the test itself — `spctl` on the *signing*
machine can behave differently from a machine that has never seen the binary,
and a stapled ticket only proves the ticket is attached, not that Gatekeeper
will accept it. What was demonstrated directly and locally (2026-08-02, no
notarization involved — Ben has no notary credentials stored yet):

- A Developer ID-signed but *unnotarized* binary is correctly **rejected** by
  `spctl -a -vvv -t install` with `source=Unnotarized Developer ID` — the
  assessment machinery discriminates the state notarization is meant to fix.
- The same binary, quarantined (as a downloaded file would be) and executed
  directly, was killed outright rather than launching — the CLI equivalent of
  the GUI "cannot verify the developer" dialog.
- `codesign`, `hdiutil`, and `lipo` all behave as `scripts/release.sh`
  assumes: a universal binary signs in one pass, a `.dmg` signs with the
  existing Developer ID Application identity, and `stapler` treats a `.dmg`
  as a first-class stapling target.

Added 2026-08-03, and it goes further than any of the above:

- The **whole pipeline runs end to end to the credential wall**, and is checked
  by `scripts/releasecheck` (below) rather than asserted. A universal binary is
  built, signed, packaged, checksummed, downloaded, checksum-verified, mounted
  and signature-verified — stopping only at the notarization verdict, which
  correctly reports `source=Unnotarized Developer ID` for an artifact nobody
  notarized.
- `spctl --assess --type install` **accepts** a notarized bare Mach-O that has
  no stapled ticket, via an online lookup. Previously only the rejected case
  had ever been observed, which left open whether the check could ever say yes
  to the shape this project installs. It can.

What remains **undemonstrated**, and cannot be until Ben stores notary
credentials: an actual successful `notarytool submit`, a real stapled ticket,
and — the acceptance test itself — a clean-machine, no-prior-context install
that opens without a prompt. See
[What Ben still has to do](#what-ben-still-has-to-do).

## Installing (once a release is notarized)

```sh
curl -fsSL https://raw.githubusercontent.com/bensyverson/timetattle/main/scripts/install.sh | bash
```

`| bash`, not `| sh` — the script's shebang is ignored when it's piped in,
and it uses `set -o pipefail`, which `/bin/sh` does not portably guarantee.

`scripts/install.sh` resolves the latest (or a specific, via
`TIMETATTLE_VERSION`) GitHub release, downloads its `.dmg`, and requires a
matching `.dmg.sha256` sibling asset — a release missing one fails closed
rather than skipping the check, since anyone who could tamper with the `.dmg`
could omit the checksum too. It compares the published digest against the
downloaded bytes directly, and requires that digest to be 64 hex characters, so
an empty or truncated checksum asset cannot pass by comparing equal to nothing.
It mounts the `.dmg` with
`hdiutil attach -nobrowse -noautoopen -readonly -mountpoint` (so it never hands
the volume to Finder, and never has to guess where the image landed) and,
critically, does not stop at the checksum: a same-release sha256
proves transport integrity, not provenance. Before copying anything to `PATH`
it runs `codesign --verify --strict` and `spctl --assess --type install` on
the binary itself and fails closed on either. This matters because the
container's stapled ticket does not automatically cover the bare binary
extracted from it — see
[Does the ticket cover the copy on PATH?](#does-the-ticket-cover-the-copy-on-path)
below. After copying to `TIMETATTLE_INSTALL_DIR` (default `/usr/local/bin`),
it clears any `com.apple.quarantine` attribute the copy inherited and
unmounts.

### Does the ticket cover the copy on PATH?

No, and it never can. `scripts/install.sh` mounts a `.dmg` and `cp`s a bare
Mach-O binary out of it, and — per the section above — no bare Mach-O can hold
a stapled ticket at all. That is not a packaging choice with a workaround; it
is the shape of the thing.

What makes this workable is the other half of the same measurement: an
**unstapled but notarized** bare Mach-O *is* accepted by
`spctl --assess --type install`, through an online ticket lookup against
Apple's servers. Verified 2026-08-03 against a notarized third-party CLI binary
that `xcrun stapler validate` confirms has no ticket stapled to it — `spctl`
accepts it anyway, reporting `source=Notarized Developer ID`. An online lookup
is therefore the only mechanism by which the installed binary can ever be
accepted, which is precisely why `install.sh` performs that lookup itself,
once, while it still has the connectivity it just used to download the release,
rather than leaving it to macOS at an unpredictable later moment.

Two further measurements settle what quarantine means in practice (full trace
in `project/private/2026-08-02-notarization.md`):

- `curl` does not set `com.apple.quarantine` on what it downloads, so as this
  script is written, the copy it produces is never quarantined and Gatekeeper
  never assesses it automatically at all.
- That is a property of `curl`, not a guarantee this script makes on purpose —
  macOS's `cp` **does** preserve extended attributes, including quarantine,
  by default. If the `.dmg` ever did carry quarantine (a browser download,
  for instance), a plain `cp` from the mounted volume would carry it onto the
  copy, and an unstapled, quarantined binary is exactly the shape that fails
  Gatekeeper's online check when the machine is offline at the moment someone
  finally runs `timetattle`.

Rather than depend on `curl`'s behaviour holding forever, `install.sh` does
not defer to Gatekeeper for this artifact at all: it runs the equivalent
checks itself, explicitly, while it still has the network connectivity it
just used to download the release — a stronger guarantee than an
opportunistic OS recheck at some later, unknown moment — and then clears
quarantine so macOS never attempts that recheck. `--type install`, not
`--type execute`: the latter rejects every bare CLI executable unconditionally
("does not seem to be an app") — including, verified 2026-08-03, a binary that
`--type install` **accepts as notarized** in the same breath. It is a
LaunchServices/bundle context, not a notarization check, and using it here
would reject even a fully valid release.

## Testing the release and install path without credentials

```sh
scripts/releasecheck
```

This runs the real `scripts/release.sh` against the real `scripts/install.sh`:
it packages an unnotarized `.dmg` (via `TIMETATTLE_UNNOTARIZED_PACKAGE=1`),
then points `install.sh` at it with a `file://` URL and a scratch install
directory, and checks how far the install gets. It never executes the binary it
installs, and never touches `PATH`.

With no notary credentials the expected, passing result is that `install.sh`
resolves, downloads, checksum-verifies, mounts and signature-verifies the
artifact and is then **correctly rejected** at the notarization check with
`source=Unnotarized Developer ID`. Anything that stops earlier is a real
failure and is reported as one. It exits 0 for a holding contract, 1 for a
broken one, and 2 when a precondition is missing (no Developer ID identity, a
dirty working tree), naming what was missing.

It exists because both scripts were individually correct on 2026-08-02 and the
pair was still broken: `install.sh` verified checksums with `shasum -c`, which
re-opens the *filename* recorded in the checksum file, and `release.sh` names
its image after the commit while `install.sh` downloaded to a fixed name. Every
release would have failed at that step before mounting anything. Pass a `.dmg`
as an argument (`scripts/releasecheck path/to/x.dmg`) to test an existing
image, which is how to check a genuinely notarized one once credentials exist.

## What Ben still has to do

Everything below needs either a credential or a second machine. Nothing in the
repository can substitute for any of it.

1. **Store notary credentials, once.** Run `scripts/release.sh`; it stops at
   the submit step and prints the exact `xcrun notarytool store-credentials`
   command with the team ID filled in — read back from the signature it just
   made, never hardcoded. It needs either an app-specific password (generated
   at appleid.apple.com, under Sign-In and Security) or an App Store Connect
   API key (developer.apple.com, under Users and Access → Keys).
2. **Re-run `scripts/release.sh`.** With credentials stored it should submit,
   wait, staple the `.dmg`, run `stapler validate`, and write the
   `.dmg.sha256`. Confirm `spctl --assess -vvv --type install` on the `.dmg`
   reports `accepted` rather than `source=Unnotarized Developer ID`.
3. **Run `scripts/releasecheck <that dmg>`.** It should now report the full
   `PASS`, not `PASS (to the expected wall)` — meaning `install.sh` got past
   the notarization check on a genuinely notarized artifact.
4. **Publish it.** `gh release create v<version> <dmg> <dmg>.sha256`. The
   `.sha256` sibling is not optional; `install.sh` refuses to install a release
   that does not publish one.
5. **The acceptance test.** Move to a Mac that has never seen this code (or a
   fresh VM), run the `curl … | bash` one-liner, and confirm the installed
   binary runs with no Gatekeeper prompt. `spctl` and `stapler validate` on the
   signing machine are approximations of this, not the thing itself.

Step 5 is the only one that closes the question this whole document exists for.

### Why a curl script instead of a Homebrew tap

A tap is the more idiomatic macOS install path in the abstract, and this
decision does not foreclose adding one later — a Homebrew cask's `binary`
artifact stanza can point at exactly the `.dmg` `scripts/release.sh`
produces, unmodified. But a tap needs a second repository
(`homebrew-timetattle`), its own release-tag conventions, `brew audit`
compliance, and a SHA-bump on every release. None of that is justified before
this project has users outside Ben. The curl script lives in this repo,
updates with the same commit as everything else, and needs no additional
infrastructure to exist.

## What is not done here

- **No GitHub Actions release workflow.** Signing needs Ben's private key and
  notary credentials in the environment that runs it. Doing that in CI means
  exporting the Developer ID certificate as a secret — a real security
  decision with its own tradeoffs, not an extension of "store credentials
  once locally." That choice is left to Ben rather than made here.
- **No `gh release create` automation.** `scripts/release.sh` prints the
  command to run; it does not run it, so tagging and release notes stay a
  deliberate step.
