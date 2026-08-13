package metrics_test

import (
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// A CONTRADICTION INSIDE THE ROLL-UP, FOUND WHILE MEASURING ITS COST, AND
// DELIBERATELY NOT FIXED HERE. It is an architectural question with two
// defensible answers, and picking one is not a measuring agent's decision.
//
// This package says two different things about what a client's hours ARE:
//
//	the implementation   re-clusters the CLIENT'S OWN evidence under the same
//	                     parameters, which is "model B" applied one level up
//	                     and is what reduce actually does.
//	Client's doc and the
//	closing guard        say the figure is the UNION of the client's projects'
//	                     spans, and the guard REFUSES any report where the
//	                     client exceeds its projects' sum.
//
// They agree everywhere except where a client's projects INTERLEAVE, and there
// they cannot both be right, because clustering does not distribute over a
// union. Gaps too wide to bridge inside any one project are narrow enough to
// bridge once that client's projects are taken together.
//
// The window is ordinary, not exotic. At the shipped defaults — gap 15m,
// lead-in and tail-out 5m — two of one client's projects touched ALTERNATELY
// every 8 to 15 minutes put each project's own observations more than a gap
// apart while the client's stay inside one. Measured by walking the spacing:
// 10-minute alternation gives 40m at both levels and passes; 12-minute
// alternation gives the client 46m against 40m summed and is REFUSED; 14-minute
// gives 52m against 40m.
//
// A refusal is a whole failed command, not a wrong row: Compute returns an
// error and there is no report at all. It has never fired on the live store,
// and the live registry holds one client spanning 33 projects, which is the
// shape most able to produce it.
//
// EITHER ANSWER RESOLVES IT, AND THEY POINT OPPOSITE WAYS:
//
//   - If a client's hours are its own evidence re-clustered, the guard and
//     Client's doc are wrong and the roll-up must keep re-clustering.
//   - If they are the union of its projects' spans, the guard is right and the
//     roll-up should MERGE the spans already computed instead of clustering a
//     second time — which is also the cheaper of the two, and is the open
//     question the roll-up's cost was measured to answer.
//
// This test pins the reproduction so the contradiction cannot be lost. It
// asserts the CURRENT refusal, which is a defect and not a feature; whoever
// settles the question above will see this fail, and should delete it.
func TestTheRollUpAndItsOwnGuardContradictWhenAClientsProjectsInterleave(t *testing.T) {
	// Alternating every 12 minutes. Each project's own observations are then 24
	// minutes apart — more than the 15-minute gap, so neither project bridges
	// its own silence — while the client's are 12 apart throughout and bridge
	// all of it into one sitting.
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
	}

	_, err := metrics.Compute(events, testParams())
	if err == nil {
		t.Fatal("the interleaving corpus now computes; the contradiction this pins has been " +
			"settled one way or the other, so delete this test and say which answer was taken")
	}
	if !strings.Contains(err.Error(), "can never exceed their sum") {
		t.Fatalf("the corpus was refused for some other reason than the roll-up guard, so this "+
			"test is no longer pinning what it says it is: %v", err)
	}
	t.Logf("pinned, and it is a defect rather than an intended refusal: %v", err)
}
