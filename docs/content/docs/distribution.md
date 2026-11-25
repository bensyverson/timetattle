---
title: "Releasing"
weight: 60
description: "How a version of timetattle reaches Homebrew, how its binary is signed and notarized, and what that means for Full Disk Access."
---

# Releasing

timetattle is distributed through the `bensyverson/tap` Homebrew tap. With the
tap checked out beside this repository:

```sh
scripts/release 0.2.0      # bump, test, tag, push, update the formula, bottle, check
scripts/release            # publish the version the source already states
scripts/release --dry-run  # print the plan, change nothing
```

`scripts/release` runs the tap's shared release script, whose `--help` describes
each step. A step already done is skipped, so a release that stopped halfway
resumes when run again. What is particular to this project is in
`scripts/release.conf`.

## The version

`cmd/timetattle/version.go` states the release number, and the script rewrites
that line. Homebrew builds from a tarball with no VCS stamp, so `timetattle
version` on an installed copy reports the commit as unknown.

## Signing

The bottle is signed with a Developer ID and notarized. `sign=developer-id`
takes the identity from the keychain, so the certificate's name never appears in
this repository (see [the publication gate](confidentiality.md)). Notarizing
uses a `notarytool` keychain profile stored once on the releasing machine.

The binary is a bare Mach-O, which cannot carry a stapled ticket. Homebrew does
not quarantine what a formula downloads, so Gatekeeper does not assess it.

A Mac without a matching bottle builds from source. That binary is ad-hoc
signed, so its Full Disk Access grant does not survive a rebuild.

## Full Disk Access and the schedule

macOS grants Full Disk Access per binary. Homebrew installs each version at a
new path, so check the grant after an upgrade with `timetattle status`. Whether
a signed upgrade keeps it has not been tested.

`brew services start timetattle` runs `timetattle harvest` hourly and at load,
under the same launchd label as `make agent`, which is the label `timetattle
status` reads the schedule from. The two replace each other. Use one.

The two builds are also signed under different identifiers: `scripts/sign.sh`
sets one explicitly and the release does not. Moving a machine between them
means granting access again.
