package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the source attribution stacked bar. The sibling task
// that fills renderSourceAttribution in owns this file and
// dashboard_sources.go only.
func TestRenderSourceAttributionWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderSourceAttribution wrote nothing")
	}
}
