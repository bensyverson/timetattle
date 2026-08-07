package main

import (
	"context"
	"io"
)

// renderSourceAttribution will draw a horizontal stacked bar of estimated
// hours by evidencing source combination, with explicit segments for
// multi-source corroboration — the source attribution task
// (project/2026-08-07-cli-overhaul-plan.md). data.Report.Sessions' own
// Sources field already carries the per-sitting source list this is built
// from; segment totals must reconcile numerically with the metrics figures
// for the same scope and period, per that task's own criteria.
//
// THIS FILE, AND dashboard_sources_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderSourceAttribution(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
