package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the source freshness section. The sibling task that
// fills renderSourceFreshness in owns this file and dashboard_freshness.go
// only.
func TestRenderSourceFreshnessWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderSourceFreshness(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderSourceFreshness: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderSourceFreshness wrote nothing")
	}
}
