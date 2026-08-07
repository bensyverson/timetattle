package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the filetype breakdown. The sibling task that fills
// renderFiletypeBreakdown in owns this file and dashboard_filetypes.go only.
func TestRenderFiletypeBreakdownWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderFiletypeBreakdown(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderFiletypeBreakdown wrote nothing")
	}
}
