package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the component stats panel. The sibling task that
// fills renderComponentStats in owns this file and dashboard_stats.go only.
func TestRenderComponentStatsWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderComponentStats(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderComponentStats: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderComponentStats wrote nothing")
	}
}
