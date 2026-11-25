package main

import (
	"bytes"
	"strings"
	"testing"
)

const header = "level,name,seconds,observed_seconds,allocated_seconds\n"

// TestComparePrintsTotalsAndNoName: the output is pasted into a public
// document, so a client or project name must never reach it.
func TestComparePrintsTotalsAndNoName(t *testing.T) {
	before := header +
		"presence,,36000,18000,\n" +
		"unattributed,unregistered,3600,,\n" +
		"client,SecretClient,7200,,7200\n" +
		"project,SecretKept,7200,,3600\n" +
		"project,SecretGone,3600,,3600\n"
	after := header +
		"presence,,32400,18000,\n" +
		"unattributed,unregistered,3600,,\n" +
		"client,SecretClient,3600,,3600\n" +
		"project,SecretKept,3600,,3600\n"

	var out bytes.Buffer
	if err := compare(&out, strings.NewReader(before), strings.NewReader(after)); err != nil {
		t.Fatalf("compare: %v", err)
	}
	got := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(got, "Secret") {
		t.Errorf("a name reached the output:\n%s", out.String())
	}
	for _, want := range []string{
		"presence 10.0 9.0 -1.0",
		"unattributed: unregistered 1.0 1.0 +0.0",
		"engaged, summed over projects 3.0 1.0 -2.0",
		"allocated, summed over projects 2.0 1.0 -1.0",
		"projects with a row: 2 before, 1 after",
		"changed: 2, of which 1 no longer appear",
		"largest fall in one project's engaged hours: 1.0",
		"largest rise in one project's engaged hours: 0.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
