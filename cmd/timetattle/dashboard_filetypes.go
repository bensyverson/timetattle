package main

import (
	"context"
	"io"
)

// renderFiletypeBreakdown will draw a stacked bar of file activity by
// category over the period — docs versus tests versus code versus graphics —
// from the filedays data, with an explicit "other" bucket rather than silent
// omission — the filetype task (project/2026-08-07-cli-overhaul-plan.md).
// internal/filedays is the existing package this reads; metrics.Compute never
// touches file extensions at all, so this section reaches past data.Report
// into data.Store the same way source freshness does. Totals must reconcile
// with filedays counts for the same scope and period, per that task's own
// criteria.
//
// THIS FILE, AND dashboard_filetypes_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderFiletypeBreakdown(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
