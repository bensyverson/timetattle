#!/bin/bash
# Sign the timetattle binary with a stable identity, so Full Disk Access
# survives a reinstall.
#
# THE PROBLEM THIS SOLVES. Go links binaries with an ad-hoc signature:
# Identifier=a.out, Signature=adhoc, TeamIdentifier=not set, and no internal
# requirements. With no designated requirement to match against, TCC pins a
# Full Disk Access grant to the binary's CDHASH. Every `go install` produces a
# new cdhash, so the stored grant stops matching and the scheduled harvest
# starts failing with "operation not permitted" — while the entry still looks
# enabled in System Settings, because it is. Toggling it off and on re-enables
# the same entry against the same stale hash and changes nothing; the entry has
# to be removed and re-added.
#
# That loop cost this project 44 hours of dead capture on 2026-07-30 and 33
# hours three days before. knowledgeC deletes itself at a day per day and
# /app/usage retains under ten, so the window is not merely inconvenient.
#
# WHY SIGNING FIXES IT, measured rather than assumed. Signing with a real
# identity gives the binary a designated requirement that names the identity
# instead of the hash. Two builds of different content were signed with the
# same certificate and produced different cdhashes (232b82ce... and
# edb67577...) and the IDENTICAL designated requirement. That is the whole
# property: TCC matches the requirement, so a rebuild keeps the grant.
#
# THE TWO IDENTITY KINDS DO NOT PRODUCE THE SAME REQUIREMENT, and the
# difference decides whether the grant survives a CERTIFICATE renewal as well
# as a rebuild. Print it for whatever is actually installed with:
#
#   codesign -d -r- <binary>
#
# A self-signed certificate pins the requirement to that one certificate
# (`certificate leaf = H"<hash>"`), so renewing or replacing it breaks the
# grant and the System Settings dance returns. A Developer ID Application
# certificate produces a requirement anchored to `anchor apple generic` and
# `certificate leaf[subject.OU] = <team id>` -- the TEAM, not the certificate
# -- which is why renewal does not break it. Verified 2026-08-03 by reading
# the requirement back off the installed binary. An earlier version of this
# comment quoted the self-signed shape as if it were what this script
# produces, which understated the Developer ID path's main advantage.
#
# A SELF-SIGNED CERTIFICATE IS ENOUGH FOR THIS. It is not enough to distribute
# the binary to anyone else — Gatekeeper on another machine requires a Developer
# ID identity and notarization — but the Full Disk Access problem is solved by
# either. This script prefers a Developer ID when one exists and falls back to
# whatever valid identity it finds, so the same command serves both.
#
# --options runtime and --timestamp are applied ALWAYS, not only for Developer
# ID. Both are prerequisites for notarization, both work with a self-signed
# certificate (verified: Apple's timestamp server accepts one), and applying
# them now means the notarized path is a change of identity and nothing else.
set -euo pipefail

BIN="${TIMETATTLE_BIN:-$HOME/go/bin/timetattle}"

# The identifier TCC records as part of the designated requirement. It must stay
# stable across rebuilds or the grant breaks, which is the whole point, so it is
# set explicitly rather than left to Go — every ad-hoc Go binary on the machine
# otherwise presents the same Identifier=a.out, which is its own hazard.
IDENTIFIER="${TIMETATTLE_BUNDLE_ID:-com.bensyverson.timetattle}"

if [ ! -x "$BIN" ]; then
	echo "error: $BIN is not executable." >&2
	echo "Build it first:  go install ./cmd/timetattle" >&2
	exit 1
fi

# Pick an identity. An explicit TIMETATTLE_SIGN_IDENTITY always wins; otherwise
# prefer a Developer ID Application certificate, because that is the one that
# also satisfies Gatekeeper, and fall back to any valid code-signing identity.
choose_identity() {
	if [ -n "${TIMETATTLE_SIGN_IDENTITY:-}" ]; then
		printf '%s' "$TIMETATTLE_SIGN_IDENTITY"
		return
	fi
	local identities
	identities="$(security find-identity -v -p codesigning 2>/dev/null || true)"
	# sed extracts the quoted common name from a line like:
	#   1) ABC123... "Developer ID Application: Someone (TEAMID)"
	local devid
	devid="$(printf '%s\n' "$identities" | grep -F 'Developer ID Application' | head -1 |
		sed -n 's/.*"\(.*\)".*/\1/p')"
	if [ -n "$devid" ]; then
		printf '%s' "$devid"
		return
	fi
	printf '%s\n' "$identities" | grep -E '^\s+[0-9]+\)' | head -1 |
		sed -n 's/.*"\(.*\)".*/\1/p'
}

IDENTITY="$(choose_identity)"

if [ -z "$IDENTITY" ]; then
	cat >&2 <<'NO_IDENTITY'
error: no code-signing identity found.

Without one, Go's ad-hoc signature pins Full Disk Access to the binary's hash
and every reinstall silently revokes it.

Create a self-signed one, which is sufficient for Full Disk Access (it is NOT
sufficient to distribute the binary to other machines):

  Keychain Access -> Certificate Assistant -> Create a Certificate...
    Name:             Timetattle Signing
    Identity Type:    Self Signed Root
    Certificate Type: Code Signing
    Check "Let me override defaults" and raise the validity from the 365-day
    default, or the signature expires in a year and the loop returns.

Then trust it for code signing, or it will not be listed as a valid identity:

  Keychain Access -> double-click the certificate -> Trust ->
    Code Signing: Always Trust

Set only the Code Signing row rather than the blanket "Always Trust" above it.
Confirm with:

  security find-identity -v -p codesigning
NO_IDENTITY
	exit 1
fi

echo "==> signing $BIN"
echo "    identity:   $IDENTITY"
echo "    identifier: $IDENTIFIER"

# --force replaces the ad-hoc signature Go left. --options runtime and
# --timestamp are notarization prerequisites; see the header.
codesign --force \
	--sign "$IDENTITY" \
	--identifier "$IDENTIFIER" \
	--options runtime \
	--timestamp \
	"$BIN"

codesign --verify --strict "$BIN"

# Print the designated requirement, because it is the thing that has to stay
# constant across rebuilds and it is the only way to see that it did.
echo "==> designated requirement"
codesign -d -r- "$BIN" 2>&1 | sed -n 's/^designated => /    /p'

if printf '%s' "$IDENTITY" | grep -qF 'Developer ID Application'; then
	echo "==> Developer ID: this binary can be notarized for distribution."
else
	cat <<'SELF_SIGNED'
==> self-signed: enough for Full Disk Access on THIS machine.
    Gatekeeper on any other machine will reject it. Distributing a prebuilt
    binary needs a Developer ID Application certificate and notarization.
SELF_SIGNED
fi

# Unquoted heredoc, deliberately: the launchctl lines below are meant to be
# copied and run, so the uid has to be substituted rather than printed as
# literal $(id -u). Nothing else in this block expands.
cat <<GRANT

If the signing identity changed, Full Disk Access must be granted ONCE more:

  System Settings -> Privacy & Security -> Full Disk Access

REMOVE any existing timetattle entry with the "-" button and add the binary
again. Toggling the existing entry re-enables it against the previous
signature and does nothing. After this, rebuilds keep the grant.

Confirm the scheduled job can read what it needs, without touching System
Settings again:

  launchctl kickstart -k gui/$(id -u)/com.bensyverson.timetattle.harvest
  launchctl print gui/$(id -u)/com.bensyverson.timetattle.harvest | grep "last exit code"
GRANT
