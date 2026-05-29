package main

import (
	"math"
	"testing"
)

func TestPercentile_KnownSet(t *testing.T) {
	// 1..100: p50 ~ 50.5, p99 ~ 99.01 with linear interpolation over ranks 0..99.
	var s []float64
	for i := 1; i <= 100; i++ {
		s = append(s, float64(i))
	}
	if got := percentile(s, 50); math.Abs(got-50.5) > 0.5 {
		t.Errorf("p50 = %v, want ~50.5", got)
	}
	if got := percentile(s, 99); got < 99 || got > 100 {
		t.Errorf("p99 = %v, want in [99,100]", got)
	}
	if got := percentile(s, 0); got != 1 {
		t.Errorf("p0 = %v, want 1 (min)", got)
	}
	if got := percentile(s, 100); got != 100 {
		t.Errorf("p100 = %v, want 100 (max)", got)
	}
}

func TestPercentile_Unsorted(t *testing.T) {
	// Order must not matter, and the input slice must not be mutated.
	s := []float64{9, 1, 5, 3, 7}
	orig := append([]float64(nil), s...)
	if got := percentile(s, 50); got != 5 {
		t.Errorf("p50 = %v, want 5", got)
	}
	for i := range s {
		if s[i] != orig[i] {
			t.Fatalf("percentile mutated input: %v != %v", s, orig)
		}
	}
}

func TestPercentile_Edges(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty p50 = %v, want 0", got)
	}
	if got := percentile([]float64{42}, 99); got != 42 {
		t.Errorf("single-element p99 = %v, want 42", got)
	}
}

func TestSummarize_BasicStats(t *testing.T) {
	s := summarize([]float64{2, 4, 6, 8})
	if s.N != 4 {
		t.Errorf("N = %d, want 4", s.N)
	}
	if s.Min != 2 || s.Max != 8 {
		t.Errorf("min/max = %v/%v, want 2/8", s.Min, s.Max)
	}
	if s.Mean != 5 {
		t.Errorf("mean = %v, want 5", s.Mean)
	}
}
