package main

import (
	"context"
	"io"
)

// renderComponentStats will show per-source counts and per-day averages for
// the selected period — screen-on hours, commits, LOC inserted/removed,
// files created or updated, Jobs tasks closed, Jobs agents active, Claude
// sessions and tokens or cost where present — the component stats task
// (project/2026-08-07-cli-overhaul-plan.md). data.Events already carries
// every retained, client-stamped observation the counts are drawn from; a
// stat whose source has none for the period must say so rather than print
// zero, per design principle 3.
//
// THIS FILE, AND dashboard_stats_test.go, ARE THE ONLY FILES THAT TASK EDITS.
// See the section list in dashboard.go for the contract every render
// function shares.
func renderComponentStats(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
