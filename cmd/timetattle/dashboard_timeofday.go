package main

import (
	"context"
	"io"
)

// renderTimeOfDay will draw a horizontal 24-hour strip of when work typically
// happened across the period, from sitting spans, honoring the
// no-data-versus-zero rule for hours outside any observation window — the
// time-of-day task (project/2026-08-07-cli-overhaul-plan.md).
// data.Report.Sessions carries every sitting's Start/End wall-clock; data.Log
// is how an hour outside any run's coverage is told from one observed idle.
//
// THIS FILE, AND dashboard_timeofday_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderTimeOfDay(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
