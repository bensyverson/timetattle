package main

import (
	"context"
	"io"
)

// renderAllocationRate will show, where the scoped project maps to a client
// allocation, hours against allocation and the effective hourly rate — the
// allocation task (project/2026-08-07-cli-overhaul-plan.md). No allocation or
// rate figure exists anywhere in this codebase's config or store today (see
// this task's own job note on fW4vV for what was checked and where such a
// figure would plausibly need to be added); metrics.Client.Allocated is a
// different thing entirely — an even division of concurrent presence across
// active clients, not a contracted hour budget — so this section cannot be
// filled in from data.Report alone and will need real design work, not just
// a render pass.
//
// THIS SECTION PRINTS MONEY once implemented — design principle 4 — so its
// real render function's test, when it replaces dashboard_allocation_test.go,
// must carry no real client name, rate or contract figure; the confidential
// gate (go run ./scripts/confidential -names <names> -gate .) is the check,
// same as review and orient.
//
// THIS FILE, AND dashboard_allocation_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
func renderAllocationRate(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	_ colorCapability, _ *dashboardData) error {
	return dashboardPlaceholder(out)
}
