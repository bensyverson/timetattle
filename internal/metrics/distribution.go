package metrics

import (
	"slices"
	"time"
)

// Distribution is the shape of a set of spans, not just their sum.
//
// It exists because ground-truth.md finding 1 is a finding and not a nicety:
// Saturday's presence arrived in blocks of 265 and 243 minutes and Sunday's
// averaged twenty, and a duration model that reports only totals treats them
// identically. Sixteen twenty-minute blocks and two four-hour blocks are both
// "presence hours" and are obviously different kinds of time, and the second is
// sustained work while the first is a phone being checked around a family.
type Distribution struct {
	// Count is how many spans, and Total how much clock they cover.
	Count int
	Total time.Duration
	// Shortest, Median, P95 and Longest describe the spread. The quantiles are
	// NEAREST-RANK, so each is the length of a span that exists rather than an
	// interpolation between two that do — and so that a figure here is the
	// same figure `timetattle sessionize` prints, which uses the same rule.
	Shortest, Median, P95, Longest time.Duration
	// Buckets is the histogram, in ascending order and covering every length,
	// so the shape is legible without reading every span.
	Buckets []Bucket

	// lengths is the sorted population, kept so a caller can re-derive any
	// quantile this struct did not think to offer.
	lengths []time.Duration
}

// Lengths is the sorted span lengths the distribution describes.
func (d Distribution) Lengths() []time.Duration {
	return slices.Clone(d.lengths)
}

// Bucket is one band of the histogram. AtLeast is inclusive and Below
// exclusive; Below is zero on the last bucket, which is unbounded above.
type Bucket struct {
	AtLeast, Below time.Duration
	Count          int
	Total          time.Duration
}

// bucketBounds are the bands sittings are counted into.
//
// They are chosen to make the distinction ground truth actually turns on
// visible at a glance — a fragmented weekend against a sustained working day —
// and they are otherwise arbitrary round numbers. Nothing downstream reads
// them; they are for a person.
var bucketBounds = []time.Duration{
	0,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
	2 * time.Hour,
	4 * time.Hour,
}

// distribute describes a set of spans.
//
// An empty set produces an empty Distribution, with no buckets at all. A
// histogram of zeroes over a population nobody measured reads exactly like a
// histogram over a population that was measured and found empty, and those are
// two different answers.
func distribute(spans []span) Distribution {
	if len(spans) == 0 {
		return Distribution{}
	}
	lengths := make([]time.Duration, 0, len(spans))
	for _, s := range spans {
		lengths = append(lengths, s.length())
	}
	slices.Sort(lengths)

	d := Distribution{
		Count:    len(lengths),
		Shortest: lengths[0],
		Longest:  lengths[len(lengths)-1],
		Median:   nearestRank(lengths, 0.5),
		P95:      nearestRank(lengths, 0.95),
		lengths:  lengths,
	}
	for _, l := range lengths {
		d.Total += l
	}

	d.Buckets = make([]Bucket, len(bucketBounds))
	for i, low := range bucketBounds {
		d.Buckets[i].AtLeast = low
		if i+1 < len(bucketBounds) {
			d.Buckets[i].Below = bucketBounds[i+1]
		}
	}
	for _, l := range lengths {
		i := len(bucketBounds) - 1
		for j, low := range bucketBounds {
			if l < low {
				i = j - 1
				break
			}
		}
		d.Buckets[i].Count++
		d.Buckets[i].Total += l
	}
	return d
}

// nearestRank is the qth value of a sorted population, by the same arithmetic
// `timetattle sessionize` uses, so that the two agree to the microsecond rather
// than nearly.
func nearestRank(sorted []time.Duration, q float64) time.Duration {
	rank := int(q * float64(len(sorted)))
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
