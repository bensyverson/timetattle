#!/bin/bash
# Install the launchd agent that runs `timetattle harvest` on a schedule.
#
# This is the piece that makes the harvester worth anything, and there are two
# different kinds of loss waiting behind a missed run. They are not the same
# kind and the difference decides the schedule.
#
#   knowledgeC and Biome PRUNE. /app/usage holds under 10 days, the other
#   knowledgeC streams roughly 28, and Biome fills fixed 512 KB segments and
#   eventually discards the oldest. Anything still inside those windows comes
#   back in full whenever the next harvest runs. On 2026-08-12 the store held
#   nothing at all for that day; a harvest run by hand recovered it as 8,280
#   events. Late, but whole.
#
#   The mtime sources DECAY, and no later harvest ever recovers them. A file
#   records only its LAST modification, so work done today on a file last
#   touched on 11 July does not add evidence for 11 July — it MOVES it,
#   deleting that day and crediting today. Measured: one repository scan found
#   1,773 files bearing 2026-07-11, and the same scan hours later found 513.
#   Both numbers were true when taken. The 1,260 that moved cannot be got back
#   by harvesting harder afterwards, only by having harvested sooner. Worse,
#   the loss leaves no edge to notice: the count just gets smaller and stays
#   plausible. internal/filedays/filedays.go carries the full argument.
#
# So frequency IS what protects us, for one of the two. Until 2026-08-12 this
# script asserted the opposite — "Frequency is not what protects us — an
# 8.9-day retention floor on /app/usage means even weekly capture would rarely
# lose anything" — which weighed only the prunable sources and left the
# decaying ones out of the sentence entirely.
#
# Re-runnable: it replaces any existing agent and reloads it.
set -euo pipefail

LABEL="com.bensyverson.timetattle.harvest"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
BIN="${TIMETATTLE_BIN:-$HOME/go/bin/timetattle}"
DATA="$HOME/.timetattle"

# WHY NOT StartInterval, WHICH THIS USED TO BE.
#
# It was StartInterval=43200, under a comment claiming "launchd runs a missed
# interval on the next wake." It does not, and launchd.plist(5) says so under
# StartInterval itself: "If the system is asleep during the time of the next
# scheduled interval firing, that interval will be missed due to shortcomings
# in kqueue(3)." The sentence about running on the next wake sits three
# paragraphs lower, under StartCalendarInterval, and had been read onto the
# wrong key. On a laptop that sleeps, a 12h StartInterval therefore counts
# something closer to awake time and has no wall-clock bound at all.
#
# Measured over all 35 inter-harvest gaps since 2026-07-30: mean 8.49h, max
# 54.64h (2026-08-07T19:10:22Z to 2026-08-10T01:49:00Z), 13 gaps longer than
# the nominal 12h, 2 longer than 24h. A 12h timer that produced a 54.64h gap
# is not jittering; it is missing fires. Note also that the mean came in BELOW
# the nominal interval, which a 12h timer cannot do on its own: the short gaps
# are harvests run by hand and reinstalls of this script. The unattended floor
# was worse than 8.49h suggests.
#
# WHAT StartCalendarInterval BUYS, EXACTLY. launchd.plist(5): "Unlike cron
# which skips job invocations when the computer is asleep, launchd will start
# the job the next time the computer wakes up. If multiple intervals transpire
# before the computer is woken, those events will be coalesced into one event
# upon wake from sleep."
#
# THAT WAS VERIFIED ON THIS MACHINE RATHER THAN TAKEN OFF THE PAGE, because a
# misread of this very man page is what put the wrong key here in the first
# place. macOS ships com.apple.newsyslog on StartCalendarInterval at Minute 30
# of every hour, and on 2026-08-12 the machine was asleep across one of its
# slots. From pmset -g log: Sleep at 13:26:56, DarkWake at 13:34:57, so 13:30
# passed with the machine down. From the unified log, newsyslog's previous run
# at 12:30:04 had rescheduled itself for "13:30:00 Central Daylight Time", and
# the run that followed is stamped 13:34:57.652 — 0.65 seconds after the wake,
# not at 13:30 and not at 14:30 — after which it rescheduled for 14:30. Both
# halves are reproducible:
#
#   pmset -g log | grep -E 'Sleep|DarkWake'
#   log show --last 8h --predicate 'eventMessage CONTAINS "newsyslog"' --style compact
#
# Note that a DarkWake was enough. The job did not wait for a user to open the
# lid.
#
# Read the guarantee for precisely what it says. It bounds the delay after
# WAKE. It does NOT bound the gap in wall-clock time, and nothing downstream
# should be written as though it does. A laptop closed on Friday and opened on
# Monday still has a three-day hole in the record either way. What changes is
# that it harvests within seconds of the lid opening rather than somewhere in
# the next 4 hours, and that the coalescing means it harvests once on waking
# rather than 18 times in a row.
#
# Boot, logout and reinstall are a separate case this key does not cover: a
# job that is not loaded cannot have missed an interval, so nothing is owed on
# load. RunAtLoad below is what covers that, and it is why it stays.

# CADENCE: every 4 hours, six slots a day.
#
# The argument is sampling, and it follows from the mtime decay above. Only a
# scan taken while a file still carries a given day's mtime can ever record
# that day, so what matters is how many harvests land inside a working
# session. Twice a day at 00:00 and 12:00 puts one inside an ordinary day and
# can put none inside a short one. Six puts three or four inside any session
# long enough to be worth measuring.
#
# The ceiling is storage, and it is not a scheduling problem. A knowledgeC
# snapshot is ~1.54 MB (measured: 57 MB across 37 snapshots) and a fresh one
# is written under a new timestamped name on EVERY run — unlike Biome
# segments, which are skipped when their digest is already on disk.
# Undeduplicated, and for now unpruned because a retention policy is
# deliberately deferred until ingestion is trusted, that is roughly 1.1 GB a
# year at twice a day, 3.3 GB at every 4h, and 13 GB at hourly.
#
# Hourly is otherwise the better schedule and, contrary to the obvious guess,
# the SIMPLER plist: omitted keys are wildcards, so hourly is a single dict
# with Minute 0 and no Hour at all, where 4-hourly needs six dicts. macOS
# ships com.apple.newsyslog written exactly that way. Hourly is rejected on
# the 13 GB alone. If snapshot deduplication or a retention policy lands,
# revisit this line and go hourly; nothing else about the schedule argues
# against it.
#
# What no cadence buys is safety against the perishable floors, because those
# are VOLUME-based rather than time-based: /app/usage is capped near 10,000
# rows and Biome fills fixed 512 KB segments. "Under 10 days" describes
# typical activity, and a heavy stretch shortens the real deadline by an
# amount nobody has measured — segment roll rate against activity has never
# been put on a graph. Every 4h is a cost judgement made under that
# uncertainty. It is not a proven margin, and "no loss yet" is an observation
# rather than a guarantee.
HOURS="${TIMETATTLE_HOURS:-0,4,8,12,16,20}"

if [ ! -x "$BIN" ]; then
	echo "error: $BIN is not executable." >&2
	echo "Build it first:  go install ./cmd/timetattle" >&2
	exit 1
fi

# Build one StartCalendarInterval dict per hour. A bad hour here produces a
# plist that launchd rejects with a bare I/O error, so it is caught up front.
CALENDAR=""
IFS=',' read -r -a HOUR_LIST <<<"$HOURS"
for h in "${HOUR_LIST[@]}"; do
	case "$h" in
	'' | *[!0-9]*)
		echo "error: TIMETATTLE_HOURS entry '$h' is not a number." >&2
		exit 1
		;;
	esac
	if [ "$h" -gt 23 ]; then
		echo "error: TIMETATTLE_HOURS entry '$h' is not an hour (0-23)." >&2
		exit 1
	fi
	CALENDAR="$CALENDAR		<dict><key>Hour</key><integer>$h</integer><key>Minute</key><integer>0</integer></dict>
"
done

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
	<key>StartCalendarInterval</key>
	<array>
$CALENDAR	</array>
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

plutil -lint "$PLIST" >/dev/null

# bootout is expected to fail when nothing is loaded yet. It is also what makes
# this re-runnable: bootstrap then reads the file afresh, so a previous
# StartInterval registration is replaced rather than merged with the new
# calendar entries. launchd.plist(5) warns that the two keys "are not aware of
# each other" and are evaluated independently, so a half-replaced job would
# quietly run on both schedules.
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$PLIST"

# No kickstart here, deliberately. RunAtLoad already started the harvest during
# the bootstrap above — measured on 2026-08-12 with a throwaway agent carrying
# RunAtLoad and a calendar entry, which logged its run in the same second as
# the bootstrap call. The line that used to sit here was `launchctl kickstart
# -k`, and -k means "kill the job if it is running, then start it": every
# install has been aiming a kill at a harvest that began a fraction of a second
# earlier and may have been mid-write.

echo "installed $LABEL"
echo "  binary:    $BIN"
echo "  schedule:  ${HOURS//,/:00, }:00 local, plus once at load"
echo "  snapshots: $DATA/snapshots"
echo "  logs:      $DATA/harvest.log, $DATA/harvest.err"
echo
echo "To confirm what launchd actually loaded, rather than what this script"
echo "wrote, note that a calendar schedule does NOT appear as the 'run interval'"
echo "line StartInterval used to produce. It is one entry per hour under event"
echo "triggers, and its absence there means the schedule did not load:"
echo "  launchctl print gui/\$UID/$LABEL | grep -A 8 calendarinterval"
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
