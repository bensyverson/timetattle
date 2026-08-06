#!/bin/bash
# Install the launchd agent that runs `timetattle harvest` on a schedule.
#
# This is the piece that makes the harvester worth anything. knowledgeC
# prunes itself — /app/usage holds under 10 days, the other streams roughly
# 28 — so presence data not captured before it is pruned is gone permanently.
#
# Re-runnable: it replaces any existing agent and reloads it.
set -euo pipefail

LABEL="com.bensyverson.timetattle.harvest"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
BIN="${TIMETATTLE_BIN:-$HOME/go/bin/timetattle}"
DATA="$HOME/.timetattle"

# Twice a day. Frequency is not what protects us — an 8.9-day retention floor
# on /app/usage means even weekly capture would rarely lose anything — but a
# snapshot is only ~1.6 MB, and a wider margin covers the laptop being away
# for a stretch. launchd runs a missed interval on the next wake.
INTERVAL="${TIMETATTLE_INTERVAL:-43200}"

if [ ! -x "$BIN" ]; then
	echo "error: $BIN is not executable." >&2
	echo "Build it first:  go install ./cmd/timetattle" >&2
	exit 1
fi

# Sign before installing, so launchd never runs an ad-hoc binary whose Full
# Disk Access grant is keyed to a hash that the next build will invalidate.
# scripts/sign.sh explains the mechanism and prints the designated requirement
# it produced. Set TIMETATTLE_SKIP_SIGN=1 to install without signing, which is
# the old behaviour and reopens the revoke-on-every-install loop.
if [ "${TIMETATTLE_SKIP_SIGN:-0}" = "1" ]; then
	echo "==> skipping signing (TIMETATTLE_SKIP_SIGN=1)"
	echo "    Full Disk Access will be keyed to this binary's hash and revoked"
	echo "    by the next 'go install'."
else
	TIMETATTLE_BIN="$BIN" "$(dirname "$0")/sign.sh"
fi

mkdir -p "$HOME/Library/LaunchAgents" "$DATA"

cat >"$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN</string>
		<string>harvest</string>
	</array>
	<key>StartInterval</key>
	<integer>$INTERVAL</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>$DATA/harvest.log</string>
	<key>StandardErrorPath</key>
	<string>$DATA/harvest.err</string>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
</dict>
</plist>
PLIST_EOF

# bootout is expected to fail when nothing is loaded yet.
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$PLIST"
launchctl kickstart -k "gui/$UID/$LABEL"

echo "installed $LABEL"
echo "  binary:    $BIN"
echo "  interval:  $((INTERVAL / 3600))h"
echo "  snapshots: $DATA/snapshots"
echo "  logs:      $DATA/harvest.log, $DATA/harvest.err"
echo
echo "IMPORTANT: macOS grants Full Disk Access per binary, keyed to the binary's"
echo "code signature. This script signed it above, so the grant is now stored"
echo "against a designated requirement naming the signing identity rather than"
echo "against a hash of the code — which means REBUILDS KEEP THE GRANT."
echo
echo "Unsigned, they do not. Go links ad-hoc, with no designated requirement to"
echo "match, so macOS falls back to the cdhash and every 'go install' revokes"
echo "the grant while the entry still looks enabled in System Settings. A"
echo "background agent gets no prompt; it just fails. That cost this project 44"
echo "hours of dead capture on 2026-07-30 and 33 hours three days earlier."
echo
echo "GRANT IT ONCE, NOW, and only again if the signing identity changes:"
echo "  1. System Settings > Privacy & Security > Full Disk Access"
echo "  2. REMOVE any old $BIN entry with '-', then add it again. Toggling is"
echo "     not enough: the entry itself holds the previous signature."
echo "  3. Run 'timetattle status' to confirm the sources are advancing."
echo
echo "See docs/content/docs/harvest.md for how to recognise a revoked grant,"
echo "and scripts/sign.sh for what the signature actually has to satisfy."
