package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// This file is the load-bearing example every sibling stub test copies: it
// asserts only that the section renders SOMETHING under its own render
// function, called directly rather than through the whole dashboardCmd
// pipeline. The task that fills in renderActivityHeatmap for real (see
// project/2026-08-07-cli-overhaul-plan.md) replaces this file's body along
// with dashboard_heatmap.go's; dashboard.go and every other section's file
// are untouched by that work.
func TestRenderActivityHeatmapWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderActivityHeatmap(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderActivityHeatmap: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderActivityHeatmap wrote nothing")
	}
}
