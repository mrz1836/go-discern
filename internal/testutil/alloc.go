package testutil

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SkipUnderRace skips the test when the race detector is on, because it changes
// allocation counts and sizes.
func SkipUnderRace(tb testing.TB) {
	tb.Helper()
	if RaceEnabled {
		tb.Skip("allocation counts and sizes differ under the race detector")
	}
}

// BytesPerOp measures the bytes one run of f allocates, as a benchmark reports
// them.
func BytesPerOp(f func()) int64 {
	return testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			f()
		}
	}).AllocedBytesPerOp()
}

// AllocStats reports the average number of allocations and of bytes allocated
// by one run of f, measured like testing.AllocsPerRun: one warm-up run, then
// runs runs with GOMAXPROCS set to 1. Unlike AllocsPerRun, it keeps fractions
// and also reports bytes. TotalAlloc only grows, so the bytes also bound the
// heap growth of a run.
func AllocStats(runs int, f func()) (allocs, allocated float64) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return float64(after.Mallocs-before.Mallocs) / float64(runs), float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

// Ceiling is one allocation guard: a run of Run may allocate at most Max
// times.
type Ceiling struct {
	Name string
	Max  float64
	Run  func()
}

// CheckCeilings runs each guard as a subtest named after it. It measures Run
// with testing.AllocsPerRun, logs the count and fails when the count is above
// Max.
func CheckCeilings(t *testing.T, ceilings []Ceiling) {
	t.Helper()
	for _, c := range ceilings {
		t.Run(c.Name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, c.Run)
			t.Logf("%v allocations per run (ceiling %v)", allocs, c.Max)
			assert.LessOrEqual(t, allocs, c.Max)
		})
	}
}
