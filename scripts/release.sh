#!/bin/bash
# Build, sign, notarize and staple a distributable macOS release of
# timetattle: the path for someone who downloads a prebuilt binary and has
# never seen this code, as opposed to a developer running `go install`
# locally, who is already served by scripts/sign.sh's self-signed fallback.
#
# WHY A .DMG AND NOT A BARE BINARY OR A .PKG, settled by testing on
# 2026-08-02 (see project/private/2026-08-02-notarization.md for the full trace):
#
#   - A BARE MACH-O CANNOT BE STAPLED. Settled by measurement on 2026-08-03
#     (project/private/2026-08-03-a-bare-mach-o-cannot-be-stapled.md). On 2026-08-02
#     this was only half-answered: `stapler staple` on a signed bare Mach-O
#     proceeds through the full CloudKit lookup rather than rejecting the
#     format, and fails with error 65 "Record not found" simply because an
#     unnotarized binary has no ticket -- easy to misread as "it works".
#     Running the same command against a bare Mach-O for which a REAL ticket
#     EXISTS reaches the next step and fails with error 73: stapler downloads
#     the ticket, then tries to write it to <path>/Contents/CodeResources,
#     a bundle-relative path that is ENOTDIR against a flat executable. The
#     file is left byte-identical. So the container is not a preference here,
#     it is the only shape that can carry a ticket at all.
#   - A .pkg needs a "Developer ID Installer" certificate, which is DIFFERENT
#     from the "Developer ID Application" certificate this repo already has
#     (`security find-identity -v -p codesigning` on 2026-08-02 showed
#     exactly one Developer ID identity, and it is the Application kind).
#     Building a .pkg release today would introduce a second unmet
#     credential prerequisite on top of the notarization one this task is
#     already stopped on.
#   - A .dmg signs with the SAME "Developer ID Application" identity already
#     in the keychain (`codesign --sign "<identity>" some.dmg` was verified
#     to work, unmodified, on 2026-08-02) and is one of stapler's three
#     documented supported formats (UDIF disk images, code-signed bundles,
#     signed flat installer packages -- from `stapler staple`'s own usage
#     text). It is the only shape that adds no new prerequisite.
#
# UNIVERSAL, NOT ARM64-ONLY. modernc.org/sqlite is pure Go -- no cgo, so no
# cross-compiler toolchain is needed -- and `GOOS=darwin GOARCH=amd64
# CGO_ENABLED=0 go build` was verified to produce a working Mach-O on this
# arm64 machine on 2026-08-02. `lipo -create` combines the two slices and
# `codesign` signs the fat binary in one pass, verified the same day. There is
# no reason to ship arm64-only when the alternative costs nothing.
#
# REPRODUCIBILITY. Go embeds a VCS stamp (commit, build time, dirty flag)
# automatically when building from a git checkout (since Go 1.18) -- verified
# present via `go version -m` on 2026-08-02 with no ldflags or other help from
# this script. `timetattle version` surfaces it. This script refuses to
# release from a dirty working tree, because a binary a dirty build produced
# is not traceable to a commit no matter what the report says.
#
# THE HARD WALL. No notary credentials are stored on this machine
# (`xcrun notarytool history --keychain-profile notarytool` returns "No
# Keychain password item found"). This script attempts the submit step
# anyway -- the attempt IS the artifact of having reached the wall -- and
# stops with instructions rather than guessing at credentials. Storing them
# needs either an app-specific password or an App Store Connect API key,
# and that is not something this script can do for you.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IDENTIFIER="${TIMETATTLE_BUNDLE_ID:-com.bensyverson.timetattle}"
NOTARY_PROFILE="${TIMETATTLE_NOTARY_PROFILE:-notarytool}"
OUT_DIR="${TIMETATTLE_RELEASE_DIR:-$REPO_ROOT/dist}"

# Stop after packaging and checksumming, without submitting for notarization.
# This exists so scripts/releasecheck can exercise the REAL producing side of
# the release/install contract on a machine with no notary credentials --
# because the alternative, a second copy of the packaging steps living in the
# test, is precisely how the two sides drifted apart and shipped an installer
# that failed its own checksum on every release. The output is NOT
# distributable and this script says so, loudly, on the way out.
PACKAGE_ONLY="${TIMETATTLE_UNNOTARIZED_PACKAGE:-}"

cd "$REPO_ROOT"

# --- 1. refuse a release built from anything but a clean, known commit ----
if [ -n "$(git status --porcelain)" ]; then
	echo "error: working tree is not clean." >&2
	echo "A release binary that isn't traceable to a commit is the failure" >&2
	echo "'timetattle version' exists to catch. Commit or stash first." >&2
	exit 1
fi
COMMIT="$(git rev-parse HEAD)"
echo "==> releasing $COMMIT"

# --- 2. build a universal binary, no cgo needed ----------------------------
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR/root"

echo "==> building arm64"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o "$OUT_DIR/timetattle-arm64" ./cmd/timetattle
echo "==> building amd64"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o "$OUT_DIR/timetattle-amd64" ./cmd/timetattle
echo "==> combining into a universal binary"
lipo -create "$OUT_DIR/timetattle-arm64" "$OUT_DIR/timetattle-amd64" -output "$OUT_DIR/root/timetattle"
rm -f "$OUT_DIR/timetattle-arm64" "$OUT_DIR/timetattle-amd64"
lipo -info "$OUT_DIR/root/timetattle"

# --- 3. sign with the stable identity, same script as the FDA path --------
TIMETATTLE_BIN="$OUT_DIR/root/timetattle" TIMETATTLE_BUNDLE_ID="$IDENTIFIER" "$REPO_ROOT/scripts/sign.sh"

# sign.sh accepts a self-signed fallback identity, because that is enough for
# Full Disk Access on this machine. A release is a different audience: it
# must be a Developer ID identity or notarization will reject it outright.
# Read back which identity actually got used, rather than re-implementing
# sign.sh's own choose_identity() a second time here.
AUTHORITY="$(codesign -dvvv "$OUT_DIR/root/timetattle" 2>&1 | sed -n 's/^Authority=//p' | head -1)"
if ! printf '%s' "$AUTHORITY" | grep -qF 'Developer ID Application'; then
	echo "error: signed with '$AUTHORITY', not a Developer ID Application identity." >&2
	echo "A release cannot ship self-signed -- see scripts/sign.sh's own message" >&2
	echo "above for how to obtain a Developer ID Application certificate." >&2
	exit 1
fi
TEAM_ID="$(codesign -dvvv "$OUT_DIR/root/timetattle" 2>&1 | sed -n 's/^TeamIdentifier=//p' | head -1)"

# --- 4. package as a .dmg, the only shape that can carry a ticket ---------
VOLNAME="Timetattle"
DMG="$OUT_DIR/timetattle-$COMMIT.dmg"
echo "==> building $DMG"
hdiutil create -volname "$VOLNAME" -srcfolder "$OUT_DIR/root" -ov -format UDZO "$DMG" >/dev/null
codesign --sign "$AUTHORITY" --timestamp "$DMG"
codesign --verify --strict "$DMG"

# The checksum is written by one function, called from exactly one place per
# path, so that the published digest is always taken from the FINAL bytes of
# the dmg. That ordering matters: stapling rewrites the image, so a checksum
# computed before step 6 would be a checksum of something nobody downloads.
write_checksum() {
	local sha
	sha="$(shasum -a 256 "$DMG" | awk '{print $1}')"
	echo "==> sha256  $sha"
	printf '%s  %s\n' "$sha" "$(basename "$DMG")" >"$DMG.sha256"
}

if [ -n "$PACKAGE_ONLY" ]; then
	write_checksum
	cat <<PACKAGED

==> TIMETATTLE_UNNOTARIZED_PACKAGE is set: stopping before notarization.

  $DMG

THIS ARTIFACT IS NOT DISTRIBUTABLE. It is signed but not notarized and not
stapled, so Gatekeeper on any machine that has never seen this code will
reject it. It exists so scripts/releasecheck can exercise the download,
checksum, mount and signature-verification path of scripts/install.sh against
a real artifact produced by this script, rather than by a second copy of these
steps living in a test. Unset TIMETATTLE_UNNOTARIZED_PACKAGE to cut a real
release.
PACKAGED
	exit 0
fi

# --- 5. notarize -----------------------------------------------------------
echo "==> submitting for notarization (profile: $NOTARY_PROFILE)"
SUBMIT_LOG="$OUT_DIR/notarytool-submit.log"
if ! xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait 2>&1 | tee "$SUBMIT_LOG"; then
	if grep -qF 'No Keychain password item found' "$SUBMIT_LOG"; then
		cat >&2 <<WALL

==> stopped: no notary credentials are stored for profile '$NOTARY_PROFILE'.

This is Ben's step, not something this script can do for him. Storing
credentials needs EITHER an app-specific password for the Apple ID enrolled
in the Developer Program, OR an App Store Connect API key. The team ID this
release was signed with is $TEAM_ID (read back from the signature just now,
not hardcoded here).

To store an app-specific password (generate one at appleid.apple.com under
Sign-In and Security -> App-Specific Passwords):

  xcrun notarytool store-credentials $NOTARY_PROFILE \\
      --apple-id <your-apple-id-email> \\
      --team-id $TEAM_ID \\
      --password <the-app-specific-password>

Or with an API key instead (developer.apple.com -> Users and Access -> Keys):

  xcrun notarytool store-credentials $NOTARY_PROFILE \\
      --key <path-to-.p8-key> \\
      --key-id <key-id> \\
      --issuer <issuer-id>

Then re-run this script. Everything up to this point ($DMG, signed and
built from $COMMIT) does not need to be redone -- re-running will rebuild it,
which is cheap, but the artifact this run produced is already sitting at:

  $DMG
WALL
		exit 2
	fi
	echo "error: notarization failed for a reason other than missing credentials." >&2
	echo "See $SUBMIT_LOG." >&2
	exit 1
fi

# --- 6. staple and verify ---------------------------------------------------
echo "==> stapling"
xcrun stapler staple -v "$DMG"
echo "==> stapler validate"
xcrun stapler validate -v "$DMG"

# --type install, not --type open: tested 2026-08-02 against an unnotarized
# signed .dmg -- --type open returned the uninformative "Insufficient
# Context", while --type install gave the correct, discriminating verdict
# (rejected / source=Unnotarized Developer ID). Same reasoning as
# scripts/install.sh's choice of --type install over --type execute for the
# bare binary: pick the context that actually evaluates notarization instead
# of one that produces a generic non-answer for this artifact shape.
echo "==> spctl assessment (local approximation of a clean-machine Gatekeeper check)"
spctl --assess -vvv --type install "$DMG" || true

# After stapling, not before: stapling rewrites the image, so this has to be
# the digest of the bytes that actually get uploaded.
write_checksum

echo
echo "==> done: $DMG"
echo "    Upload it to a GitHub release, e.g.:"
echo "      gh release create v<version> '$DMG' '$DMG.sha256' --title v<version> --generate-notes"
echo
echo "    The .dmg.sha256 sibling is NOT optional -- scripts/install.sh refuses"
echo "    to install a release that does not publish one."
