package main

import (
	"context"
	"io"
)

// renderActivityHeatmap will draw the GitHub-style year grid task dash-heatmap
// (project/2026-08-07-cli-overhaul-plan.md) describes: 7 day-of-week rows,
// half-block rendering, an allocation-aware ramp where an allocation is
// known, and a day with no observations rendered distinctly from a day
// observed idle — data.Log.during(dayStart, dayEnd) is how, per
// dashboardPeriod's own doc comment in dashboard.go.
//
// THIS FILE, AND dashboard_heatmap_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares and why filling this in never touches that file.
func renderActivityHeatmap(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
