package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// WHAT A PROJECT'S HOURS REST ON.
//
// Task KrfMh. `Easley` read 135.2h against 0.0h corroborated and nothing acted
// on it. Corroborated answers "was the machine observed in use", which is a
// question about Biome and knowledgeC being alive at the time; it does not
// answer "does anything but a file's modification time say this project was
// worked on at all", and the two are not the same measurement.

// mtime is a walked file modification time attributed to a project: the
// observation that read as 126.2h of presence before 4LQHd excluded the cron
// that wrote it.
func mtime(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "fs-observations")
	e.Locator = "/Users/you/git/" + project
	return e
}

// A project whose whole trace is file modification times: every hour of it is
// weak, none of it is attested, and the total does not move.
func TestEngagedTimeRestingOnlyOnFileTimestampsIsWeak(t *testing.T) {
	r := compute(t, []source.Event{mtime(at(20, 0), "Easley"), mtime(at(20, 10), "Easley")})
	p := projectOf(t, r, "Easley")
	if p.Weak != p.Engaged.Total {
		t.Errorf("Easley is weak for %v of %v engaged; every observation behind it is an mtime",
			p.Weak, p.Engaged.Total)
	}
	if p.Attested != 0 {
		t.Errorf("Easley reads %v attested, want none", p.Attested)
	}
}

func TestEngagedTimeRestingOnRecordedActsIsAttestedRatherThanWeak(t *testing.T) {
	r := compute(t, []source.Event{point(at(10, 0), "acorn"), point(at(10, 10), "acorn")})
	p := projectOf(t, r, "acorn")
	if p.Attested != p.Engaged.Total {
		t.Errorf("acorn is attested for %v of %v engaged", p.Attested, p.Engaged.Total)
	}
	if p.Weak != 0 {
		t.Errorf("acorn reads %v weak, want none", p.Weak)
	}
}

// THREE ANSWERS, NOT TWO. Attested, weak, and accounted for by NOTHING — clock
// the gap threshold bridged inside a project's own cluster, which no
// observation of that project is near enough to be responsible for. Reading the
// third as weak overstates the weakness; reading it as attested invents
// evidence. The three must partition the engaged total exactly.
func TestAttestedWeakAndUnaccountedPartitionEveryProjectsEngagedTime(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		mtime(at(10, 30), "acorn"), // 20m later: one sitting, 15m gap threshold
		point(at(14, 0), "birch"),
		mtime(at(20, 0), "Easley"),
	})
	for _, p := range r.Projects {
		if got := p.Attested + p.Weak + p.Unaccounted; got != p.Engaged.Total {
			t.Errorf("%s: attested %v + weak %v + unaccounted %v = %v, want the engaged %v",
				p.Name, p.Attested, p.Weak, p.Unaccounted, got, p.Engaged.Total)
		}
	}
}

// The third state, made to happen, and the window it happens in is narrow. The
// two observations are TWELVE minutes apart: closer than the 15m gap threshold,
// so the clustering bridges them into one sitting and bills acorn for the whole
// of it, and further apart than the lead-in plus the tail-out, so nothing is
// responsible for the two minutes in the middle. Anything past 15m would split
// into two sittings and there would be no bridged silence to find.
func TestSilenceTheGapThresholdBridgedIsUnaccountedRatherThanWeak(t *testing.T) {
	r := compute(t, []source.Event{point(at(10, 0), "acorn"), mtime(at(10, 12), "acorn")})
	p := projectOf(t, r, "acorn")
	if want := 22 * time.Minute; p.Engaged.Total != want {
		t.Fatalf("acorn engaged %v, want the one bridged sitting 09:55–10:17 of %v", p.Engaged.Total, want)
	}
	if p.Unaccounted != 2*time.Minute {
		t.Errorf("acorn reads %v unaccounted, want the 2m between 10:05 and 10:07 that no "+
			"observation of acorn is responsible for", p.Unaccounted)
	}
	if p.Attested != 10*time.Minute {
		t.Errorf("acorn reads %v attested, want the 10m around its transcript instant", p.Attested)
	}
	if p.Weak != 10*time.Minute {
		t.Errorf("acorn reads %v weak, want the 10m around its file save", p.Weak)
	}
}

// Where an act and an mtime account for the same instant, the act wins. A
// project is not made weaker by ALSO having saved a file.
func TestAnActAndAnMtimeCoveringTheSameInstantReadAsAttested(t *testing.T) {
	r := compute(t, []source.Event{point(at(10, 0), "acorn"), mtime(at(10, 0), "acorn")})
	p := projectOf(t, r, "acorn")
	if p.Weak != 0 {
		t.Errorf("acorn reads %v weak while a transcript covers the same instant", p.Weak)
	}
	if p.Attested != p.Engaged.Total {
		t.Errorf("acorn is attested for %v of %v", p.Attested, p.Engaged.Total)
	}
}

// WEAK AND CORROBORATED ARE DIFFERENT AXES AND BOTH CAN BE TRUE. A day of file
// saves that happens to fall inside a Biome focus interval is corroborated —
// somebody was at the machine — and still weak, because nothing said the
// machine was being used on THIS project. Collapsing them would let a running
// focus stream launder an mtime into evidence about a client.
func TestWeakTimeCanAlsoBeCorroboratedWithoutTheTwoCancellingOut(t *testing.T) {
	r := compute(t, []source.Event{
		mtime(at(10, 0), "Easley"),
		unlocatable(at(9, 50), at(10, 20)), // a focus interval belonging to nobody
	})
	p := projectOf(t, r, "Easley")
	if p.Corroborated <= 0 {
		t.Fatalf("Easley reads %v corroborated; the focus interval covers its whole sitting",
			p.Corroborated)
	}
	if p.Weak != p.Engaged.Total {
		t.Errorf("Easley reads %v weak of %v engaged; a focus interval says the machine was in "+
			"use and nothing whatever about which project", p.Weak, p.Engaged.Total)
	}
}

// A weak project keeps every figure it had. This is the mark-not-a-filter
// property at the level of the report: nothing is removed, nothing is reduced,
// and the columns that were there before read exactly as before.
func TestMarkingTimeWeakChangesNoOtherFigure(t *testing.T) {
	weak := compute(t, []source.Event{mtime(at(10, 0), "x"), mtime(at(10, 10), "x")})
	strong := compute(t, []source.Event{point(at(10, 0), "x"), point(at(10, 10), "x")})
	a, b := projectOf(t, weak, "x"), projectOf(t, strong, "x")
	if a.Engaged.Total != b.Engaged.Total || a.Allocated != b.Allocated {
		t.Errorf("the weak project reads %v engaged and %v allocated against %v and %v",
			a.Engaged.Total, a.Allocated, b.Engaged.Total, b.Allocated)
	}
	if weak.Presence.Total != strong.Presence.Total {
		t.Errorf("presence %v against %v", weak.Presence.Total, strong.Presence.Total)
	}
	if a.Weak == b.Weak {
		t.Error("the two read the same, which is the bug this task exists to fix")
	}
}

// The segments are what every total is summed from, and a caller must be able
// to re-derive the weak hours from them rather than taking the total on trust.
func TestTheSegmentsReDeriveEveryProjectsWeakHours(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		mtime(at(10, 30), "acorn"),
		mtime(at(20, 0), "Easley"),
	})
	summed := map[string]time.Duration{}
	for _, seg := range r.Segments {
		for _, name := range seg.Weak {
			summed[name] += seg.Duration()
		}
	}
	for _, p := range r.Projects {
		if summed[p.Name] != p.Weak {
			t.Errorf("%s: the segments carry %v weak against the total's %v",
				p.Name, summed[p.Name], p.Weak)
		}
	}
}

// A project active in a segment is either attested there or weak there or
// neither, and never both. Otherwise the partition above would hold by
// accident of arithmetic rather than by construction.
func TestNoSegmentCallsAProjectAttestedAndWeakAtOnce(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"), mtime(at(10, 0), "acorn"), mtime(at(10, 30), "acorn"),
	})
	for _, seg := range r.Segments {
		for _, name := range seg.Attested {
			for _, other := range seg.Weak {
				if name == other {
					t.Errorf("%s is both attested and weak across %v..%v",
						name, seg.Start, seg.End)
				}
			}
		}
	}
}
