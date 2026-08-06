#!/bin/bash
# One-command install for people who download timetattle rather than build it
# from source: fetch the latest notarized, stapled release dmg from GitHub
# Releases, mount it without involving Finder, verify the binary's own
# signature and notarization status (not just transport integrity), copy it
# onto PATH, and unmount.
#
#   curl -fsSL https://raw.githubusercontent.com/bensyverson/timetattle/main/scripts/install.sh | bash
#
# `| bash` and not `| sh`, deliberately. The shebang above is ignored when the
# script is piped to a shell, so it runs under whatever the pipe names -- and
# this script uses `set -o pipefail`, which /bin/sh does not portably support.
# On macOS /bin/sh happens to be bash in POSIX mode, where pipefail happens to
# work, which is luck rather than a guarantee. Naming `bash` explicitly makes
# the shebang and the invocation agree instead of relying on that.
#
# WHY A CURL SCRIPT AND NOT A HOMEBREW TAP, decided 2026-08-02 (see
# project/private/2026-08-02-notarization.md). A tap is the more idiomatic macOS
# install path in the abstract, and nothing here forecloses adding one later
# -- a Homebrew cask's `binary` artifact stanza can point at exactly the dmg
# this script downloads, unmodified. But a tap needs a second repository
# (homebrew-timetattle), its own versioning and `brew audit` conventions, and
# ongoing SHA-bump maintenance, none of which is justified before this project
# has more than one user outside Ben. This script needs none of that: it lives
# in the same repo as the release it installs and is updated by the same
# commit.
#
# WHY MOUNT RATHER THAN ASK THE USER TO DOUBLE-CLICK. A CLI tool's dmg is not
# meant to be dragged into /Applications; the payload is a single binary that
# belongs on PATH. `hdiutil attach -nobrowse -noautoopen` mounts without
# handing the volume to Finder, so this stays a single non-interactive step.
#
# WHY THIS SCRIPT VERIFIES THE SIGNATURE ITSELF, RATHER THAN LEANING ON
# GATEKEEPER. The task this script exists for is Gatekeeper acceptance, so
# skipping past codesign/notarization and only checking a same-release sha256
# would prove transport integrity and nothing about provenance -- an attacker
# who could replace the release asset could replace its checksum sibling too.
# It gets worse than that: this script mounts a .dmg and `cp`s a bare binary
# out of it, and THE BINARY ON PATH CAN NEVER CARRY A STAPLED TICKET. That was
# an open question on 2026-08-02; it was settled by measurement on 2026-08-03
# (project/private/2026-08-03-a-bare-mach-o-cannot-be-stapled.md): `xcrun stapler
# staple` against a bare Mach-O for which a REAL notarization ticket exists
# downloads that ticket and then fails with error 73, because it tries to
# write it to <path>/Contents/CodeResources -- a bundle-relative path that is
# ENOTDIR against a flat executable. There is no code path in `stapler` that
# embeds a ticket in a bare Mach-O.
#
# Worse still -- measured 2026-08-02, `cp` on macOS preserves extended
# attributes by default, so IF the downloaded .dmg ever carries
# com.apple.quarantine (it does not via `curl`, also measured, but that is a
# property of `curl`, not something this script enforces), a plain `cp` from
# the mounted volume carries that quarantine flag onto the copy on PATH. A
# quarantined, permanently-unstapleable binary is exactly the shape that fails
# Gatekeeper's automatic check the moment the machine it's running on is
# offline or behind a captive portal -- which could be the very next
# `timetattle` invocation, at a time this script has no way to predict.
#
# So this script does not defer to Gatekeeper at all. It runs the same checks
# Gatekeeper would (`codesign --verify --strict` for signature integrity,
# `spctl --assess --type install` for the notarization verdict -- NOT `--type
# execute`, which rejects every bare CLI executable on this machine
# unconditionally, including Apple's own /bin/ls AND a fully notarized
# third-party CLI binary that `--type install` accepts, because that context is
# for LaunchServices-openable items and a raw Mach-O "does not seem to be an
# app" to it regardless of signing status; verified 2026-08-02, and re-verified
# 2026-08-03 against a known-notarized binary, which is the case that actually
# proves it) itself, at install time, while connectivity is guaranteed -- this
# script just downloaded from the network it's about to be assessed over.
#
# That `--type install` check is not a formality: an UNSTAPLED but notarized
# bare Mach-O is accepted by it, via an online CloudKit ticket lookup
# (measured 2026-08-03 against a binary `stapler validate` reports has no
# ticket stapled). That online lookup is the only way this shape can ever be
# accepted, which is exactly why the check has to happen here, now, rather
# than being left to macOS at some later moment. Afterwards this script
# deliberately clears any quarantine flag the copy inherited, so macOS never
# attempts its own unreliable, possibly-offline re-check later. The
# verification happens once, under known-good conditions, rather than being
# left to chance at a moment this script cannot control.
set -euo pipefail

REPO="bensyverson/timetattle"
DEST_DIR="${TIMETATTLE_INSTALL_DIR:-/usr/local/bin}"
TAG="${TIMETATTLE_VERSION:-latest}"

# Install a specific .dmg instead of resolving one from GitHub Releases. Any
# URL curl understands, including file:// -- which is how scripts/releasecheck
# exercises this script end to end without a published release. A sibling
# "<url>.sha256" must exist either way; the verification below is identical.
DMG_URL_OVERRIDE="${TIMETATTLE_DMG_URL:-}"

if [ "$(uname -s)" != "Darwin" ]; then
	echo "error: this release is macOS-only (signed and notarized for Gatekeeper)." >&2
	echo "On another platform, build from source: go install ./cmd/timetattle" >&2
	exit 1
fi

if [ -n "$DMG_URL_OVERRIDE" ]; then
	echo "==> using TIMETATTLE_DMG_URL"
	dmg_url="$DMG_URL_OVERRIDE"
	sha_url="$DMG_URL_OVERRIDE.sha256"
else
	api_url="https://api.github.com/repos/$REPO/releases/latest"
	if [ "$TAG" != "latest" ]; then
		api_url="https://api.github.com/repos/$REPO/releases/tags/$TAG"
	fi

	echo "==> looking up release ($TAG)"
	release_json="$(curl -fsSL "$api_url")"

	# Pull out the .dmg asset URL and its sha256 sibling with a small,
	# dependency-free JSON scrape rather than requiring jq, since this script is
	# meant to run on a machine that has nothing installed yet.
	dmg_url="$(printf '%s' "$release_json" |
		grep -o '"browser_download_url": *"[^"]*\.dmg"' | head -1 | sed -E 's/.*"(https:[^"]+)"/\1/')"
	sha_url="$(printf '%s' "$release_json" |
		grep -o '"browser_download_url": *"[^"]*\.dmg\.sha256"' | head -1 | sed -E 's/.*"(https:[^"]+)"/\1/')"

	if [ -z "$dmg_url" ]; then
		echo "error: no .dmg asset found on release '$TAG' of $REPO." >&2
		exit 1
	fi
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> downloading $(basename "$dmg_url")"
curl -fsSL -o "$WORK/timetattle.dmg" "$dmg_url"

if [ -z "$sha_url" ]; then
	# A missing checksum is a hard failure, not a warning: whoever can publish
	# (or replace) the .dmg release asset can just as easily omit its .sha256
	# sibling, so treating the absence as "skip the check" makes the check
	# bypassable by anyone able to tamper with the release in the first place.
	# This only proves transport integrity in any case -- see the signature
	# and notarization checks below for the check that actually matters.
	echo "error: release '$TAG' has no .dmg.sha256 sibling asset; refusing to install unverified." >&2
	exit 1
fi
echo "==> verifying checksum"
curl -fsSL -o "$WORK/timetattle.dmg.sha256" "$sha_url"

# Compare the digests directly rather than running `shasum -c` on the published
# file. `shasum -c` re-opens whatever FILENAME the checksum file names, and
# scripts/release.sh names the dmg after the commit (timetattle-<sha>.dmg)
# while this script downloads to a fixed local name -- so `shasum -c` looked
# for a file that was never going to be there and failed EVERY release with
# "FAILED open or read". That was measured on 2026-08-03, not reasoned about;
# see project/private/2026-08-03-a-bare-mach-o-cannot-be-stapled.md. Comparing digests
# removes the filename coupling entirely, so the two scripts can never drift on
# a naming convention again.
expected_sha="$(awk 'NF {print $1; exit}' "$WORK/timetattle.dmg.sha256")"
# A checksum file that is empty, truncated or not a checksum at all must be a
# hard failure and not a vacuous comparison against an empty string: absence of
# a digest is not a matching digest.
if ! printf '%s' "$expected_sha" | grep -qE '^[0-9a-fA-F]{64}$'; then
	echo "error: $sha_url did not contain a sha256 digest; refusing to install." >&2
	exit 1
fi
actual_sha="$(shasum -a 256 "$WORK/timetattle.dmg" | awk '{print $1}')"
if [ "$(printf '%s' "$expected_sha" | tr 'A-F' 'a-f')" != "$actual_sha" ]; then
	echo "error: checksum mismatch for $(basename "$dmg_url")." >&2
	echo "  published: $expected_sha" >&2
	echo "  downloaded: $actual_sha" >&2
	exit 1
fi
echo "    sha256 ok  $actual_sha"

echo "==> mounting"
# -mountpoint, rather than parsing hdiutil's -plist output for a mount-point
# key: a UDZO image carries several system-entities (whole-disk, partition map,
# the mountable volume) and only some of them have a mount point, so scraping
# the plist meant guessing which. Naming the mount point makes it known before
# the attach happens, which is also what lets the detach trap be armed FIRST --
# previously, a failure to work out where the image had landed exited while the
# volume was still attached, leaking it.
MOUNT_POINT="$WORK/mnt"
mkdir -p "$MOUNT_POINT"
trap 'hdiutil detach "$MOUNT_POINT" -quiet >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT
hdiutil attach "$WORK/timetattle.dmg" -nobrowse -noautoopen -readonly -mountpoint "$MOUNT_POINT" >/dev/null

if [ ! -x "$MOUNT_POINT/timetattle" ]; then
	echo "error: the disk image does not contain an executable named 'timetattle'." >&2
	exit 1
fi

# The checksum above only proves the bytes match what the release published --
# it says nothing about who signed them. This is the check that actually
# answers the question this whole task exists for: is this a genuine,
# notarized Developer ID build. Both must pass or nothing gets copied to PATH.
echo "==> verifying signature"
codesign --verify --strict --verbose=2 "$MOUNT_POINT/timetattle"
echo "==> verifying notarization"
spctl --assess -vvv --type install "$MOUNT_POINT/timetattle"

# The privilege decision is made ONCE, as as_root(), rather than branched on
# twice, because the quarantine strip below has to run as the same user that
# made the copy. When the copy is made with sudo the result is root-owned, and
# an unprivileged `xattr -d` on it fails with EPERM -- which the `|| true`
# there would have swallowed silently, leaving the quarantine flag in place on
# the default /usr/local/bin path, which is exactly the path that needs sudo.
if [ ! -w "$DEST_DIR" ]; then
	echo "==> $DEST_DIR is not writable; sudo will prompt for a password"
	as_root() { sudo "$@"; }
else
	as_root() { "$@"; }
fi
as_root mkdir -p "$DEST_DIR"
as_root cp "$MOUNT_POINT/timetattle" "$DEST_DIR/timetattle"
as_root chmod 755 "$DEST_DIR/timetattle"

# `cp` on macOS preserves extended attributes by default, so if the .dmg this
# script downloaded ever carries com.apple.quarantine, the copy just made
# inherits it -- verified 2026-08-02. This binary was already verified above,
# under network connectivity this script itself required to get here. Clear
# any inherited quarantine deliberately, so a later, unrelated launch of
# `timetattle` never depends on macOS repeating that check on its own under
# conditions (offline, a captive portal) this script cannot vouch for -- and
# it never could pass such a check on its own, because a bare Mach-O cannot
# hold a stapled ticket at all (see the header).
as_root xattr -d com.apple.quarantine "$DEST_DIR/timetattle" 2>/dev/null || true

echo "==> installed $DEST_DIR/timetattle"
echo "    $DEST_DIR/timetattle version"
echo "    $DEST_DIR/timetattle orient"
