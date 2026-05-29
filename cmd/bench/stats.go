package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// summary holds the descriptive statistics for a set of samples.
type summary struct {
	N    int
	Min  float64
	Max  float64
	Mean float64
	P50  float64
	P99  float64
}

// percentile returns the p-th percentile (0..100) of samples using linear
// interpolation between the two nearest ranks. It sorts a copy, so the caller's
// slice is left untouched. Returns 0 for an empty slice.
func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	s := make([]float64, len(samples))
	copy(s, samples)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	if p <= 0 {
		return s[0]
	}
	if p >= 100 {
		return s[len(s)-1]
	}
	// rank in [0, n-1]
	rank := (p / 100) * float64(len(s)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return s[lo]
	}
	frac := rank - float64(lo)
	return s[lo] + frac*(s[hi]-s[lo])
}

// summarize computes min/max/mean/p50/p99 over samples. An empty slice yields a
// zero-valued summary.
func summarize(samples []float64) summary {
	if len(samples) == 0 {
		return summary{}
	}
	min, max, sum := samples[0], samples[0], 0.0
	for _, v := range samples {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	return summary{
		N:    len(samples),
		Min:  min,
		Max:  max,
		Mean: sum / float64(len(samples)),
		P50:  percentile(samples, 50),
		P99:  percentile(samples, 99),
	}
}

// histogram renders samples as a fixed-width ASCII bar chart of `buckets` equal
// linear bins between the min and max sample. unit labels the value axis (e.g.
// "ms"). Returns "(no samples)" when empty.
func histogram(samples []float64, buckets int, unit string) string {
	if len(samples) == 0 {
		return "(no samples)"
	}
	if buckets < 1 {
		buckets = 1
	}
	min, max := samples[0], samples[0]
	for _, v := range samples {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	width := max - min
	counts := make([]int, buckets)
	for _, v := range samples {
		b := 0
		if width > 0 {
			b = int((v - min) / width * float64(buckets))
			if b >= buckets {
				b = buckets - 1
			}
		}
		counts[b]++
	}
	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}

	const barWidth = 40
	var b strings.Builder
	for i, c := range counts {
		lo := min + float64(i)*width/float64(buckets)
		hi := min + float64(i+1)*width/float64(buckets)
		bars := 0
		if maxCount > 0 {
			bars = c * barWidth / maxCount
		}
		fmt.Fprintf(&b, "%8.1f-%-8.1f %s | %s %d\n",
			lo, hi, unit, strings.Repeat("█", bars), c)
	}
	return b.String()
}
