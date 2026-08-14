package sessions_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
)

// The recorded policy and the implemented one are coupled by nothing but a
// human remembering, and on 2026-08-14 the human did not.
//
// sittings.CurrentDarknessPolicy is stamped onto every row a derivation writes,
// so a stale value does not fail loudly — it makes the stored series look
// CONTINUOUS across the one change it exists to make visible, which is the
// worst available outcome and indistinguishable from correctness at read time.
// It went stale within hours of being written: the constant was authored beside
// the run table while another worker narrowed what darkness may do, and the two
// reached integration still disagreeing. Its own comment warned that this would
// happen and that nothing could notice. Both halves were right.
//
// So this test is the noticing. It does not compare the constant to a literal —
// that would only assert the constant equals itself. It runs Cluster over the
// two observations the policies disagree about and asserts the OBSERVED
// behaviour is the behaviour the constant claims. Change Cluster without
// changing the constant, or the reverse, and the arm below stops matching.
func TestTheRecordedPolicyMatchesWhatClusterDoes(t *testing.T) {
	// One observation of each kind, both anchored inside the same dark span.
	// The policies differ on the first and agree on the second, so a single
	// corpus separates them.
	darkness := backlit(0, 30*time.Minute, "off")

	surviving := cluster(t, []source.Event{point(10 * time.Minute), darkness}, params())
	timerOnly := cluster(t, []source.Event{autosave(10 * time.Minute), darkness}, params())

	switch sittings.CurrentDarknessPolicy {
	case sittings.SuppressTimerOnly:
		if len(surviving.Sessions) != 1 {
			t.Errorf("the recorded policy is %q, under which an observation carrying the empty cause survives darkness, "+
				"but clustering one produced %d sittings, want 1. Either Cluster changed and the constant did not, "+
				"or the constant changed and Cluster did not; every derivation this binary writes is mislabelled either way.",
				sittings.CurrentDarknessPolicy, len(surviving.Sessions))
		}
		if surviving.Stats.SuppressedByAbsence != 0 {
			t.Errorf("SuppressedByAbsence = %d, want 0 under %q: only a wall-clock timer is suppressed",
				surviving.Stats.SuppressedByAbsence, sittings.CurrentDarknessPolicy)
		}
		if surviving.Stats.TrimmedByAbsence != 0 || surviving.Stats.SplitByAbsence != 0 {
			t.Errorf("trimmed %v across %d splits, want 0 and 0 under %q: darkness subtracts nothing from a sitting",
				surviving.Stats.TrimmedByAbsence, surviving.Stats.SplitByAbsence, sittings.CurrentDarknessPolicy)
		}
		if len(timerOnly.Sessions) != 0 || timerOnly.Stats.SuppressedByAbsence != 1 {
			t.Errorf("a timer artifact in darkness produced %d sittings and %d suppressed, want 0 and 1 under %q",
				len(timerOnly.Sessions), timerOnly.Stats.SuppressedByAbsence, sittings.CurrentDarknessPolicy)
		}

	case sittings.SuppressAllAndTrim:
		if len(surviving.Sessions) != 0 || surviving.Stats.SuppressedByAbsence != 1 {
			t.Errorf("the recorded policy is %q, under which EVERY observation in darkness is dropped, but one carrying "+
				"the empty cause produced %d sittings and %d suppressed, want 0 and 1. The constant is stale.",
				sittings.CurrentDarknessPolicy, len(surviving.Sessions), surviving.Stats.SuppressedByAbsence)
		}

	default:
		t.Fatalf("sittings.CurrentDarknessPolicy is %q, which this test does not know how to check. A new policy needs "+
			"an arm here, or it ships stamped onto every derivation with nothing verifying it means what it says.",
			sittings.CurrentDarknessPolicy)
	}
}
