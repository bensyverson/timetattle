package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the time-of-day heatmap. The sibling task that fills
// renderTimeOfDay in owns this file and dashboard_timeofday.go only.
func TestRenderTimeOfDayWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderTimeOfDay(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderTimeOfDay wrote nothing")
	}
}
