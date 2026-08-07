package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for allocation progress and effective rate. The sibling
// task that fills renderAllocationRate in owns this file and
// dashboard_allocation.go only.
//
// THIS SECTION PRINTS MONEY once implemented — see design principle 4 in
// project/2026-08-07-cli-overhaul-plan.md — so its real test file, when it
// replaces this one, must carry no real client name or rate; the confidential
// gate (go run ./scripts/confidential -names <names> -gate .) is the check.
// This placeholder test carries neither.
func TestRenderAllocationRateWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderAllocationRate(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderAllocationRate: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderAllocationRate wrote nothing")
	}
}
