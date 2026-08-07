package main

import (
	"context"
	"io"
)

// renderSourceFreshness will show every registered source's last harvest
// time, flagging one with no harvest in the selected period, so a stale
// source cannot masquerade as a quiet week — the freshness task
// (project/2026-08-07-cli-overhaul-plan.md). data.Store.Freshness(ctx) is the
// existing store query this reads; metrics.Compute never runs it, so this
// section reaches past data.Report the same way metrics.go itself reaches
// past Events() for st.ClientMap and st.Captures.
//
// THIS FILE, AND dashboard_freshness_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderSourceFreshness(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
