package discern

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/ident"
)

func TestIDSetScenarios(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("option-%03d", i)
		}
		return out
	}
	tests := []struct {
		name  string
		reset int // -1 uses the zero value without reset
		ids   []string
		dup   int // index of the first repeated ID, or -1
	}{
		{"zero value, small, distinct", -1, ids(smallIDSet), -1},
		{"zero value, small, repeated", -1, append(ids(3), "option-001"), 3},
		{"zero value grows past the array", -1, append(ids(smallIDSet+4), "option-002"), smallIDSet + 4},
		{"zero value repeat right after growing", -1, append(ids(smallIDSet+1), "option-016"), smallIDSet + 1},
		{"reset for a long list starts as a map", 255, append(ids(254), "option-200"), 254},
		{"reset for a long list, distinct", 255, ids(255), -1},
		{"reset for a short list", 4, []string{"a", "b", "a"}, 2},
		{"empty IDs are IDs", -1, []string{"", "x", ""}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s idSet
			if tt.reset >= 0 {
				s.reset(tt.reset)
			}
			got := -1
			for i, id := range tt.ids {
				if s.add(id) {
					got = i
					break
				}
			}
			assert.Equal(t, tt.dup, got)
		})
	}
}

func TestIDSetResetReusesItsMap(t *testing.T) {
	var s idSet
	s.reset(40)
	for i := range 40 {
		require.False(t, s.add(fmt.Sprint(i)))
	}
	m := s.large
	s.reset(3)
	assert.Equal(t, fmt.Sprintf("%p", m), fmt.Sprintf("%p", s.large), "the map is kept for the next list")
	assert.Empty(t, s.large)
	assert.False(t, s.add("7"), "IDs of an earlier list are forgotten")
	assert.True(t, s.add("7"))

	var small idSet
	small.reset(2)
	assert.Nil(t, small.large, "short lists never build a map")
	assert.False(t, small.add("a"))
	small.reset(2)
	assert.False(t, small.add("a"))
}

func TestCheckObservationRejectsMissingObservation(t *testing.T) {
	qs := []QuestionInfo{{ID: "outage", Kind: KindNoul}}
	require.ErrorIs(t, checkObservation(qs, nil), errAnswers)
	_, err := normalizeObservation(qs, nil)
	require.ErrorIs(t, err, errAnswers)
}

func TestCheckObservationRejectsInvalidProviderIDs(t *testing.T) {
	qs := []QuestionInfo{{ID: "outage", Kind: KindNoul}}
	answers := []Answer{{QuestionID: "outage", Kind: KindNoul, Noul: &NoulAnswer{Value: 0.07}}}
	tests := []struct {
		name  string
		obs   Observation
		field string
	}{
		{"model", Observation{Model: "jev 1", Answers: answers}, "model"},
		{"request ID", Observation{RequestID: "req\n1", Answers: answers}, "request_id"},
		{"ray ID", Observation{RayID: "\u00e9", Answers: answers}, "ray_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, err := range []error{checkObservation(qs, &tt.obs), func() error {
				_, err := normalizeObservation(qs, &tt.obs)
				return err
			}()} {
				require.ErrorIs(t, err, errProviderID)
				assert.Contains(t, err.Error(), tt.field)
			}
		})
	}
	require.NoError(t, checkObservation(qs, &Observation{Model: "jev-1.13.0", RequestID: "req-1", RayID: "8f00-SJC", Answers: answers}))
}

func TestValidProviderIDScenarios(t *testing.T) {
	tests := []struct {
		id string
		ok bool
	}{
		{"", true},
		{"!", true},
		{"~", true},
		{"jev-1.13.0", true},
		{"@cf/cloudflare/clef", true},
		{"a b", false},
		{"a\x7f", false},
		{"a\x00", false},
		{"caf\u00e9", false},
		{"a\u200b", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.ok, validProviderID(tt.id), "%q", tt.id)
	}
	long := make([]byte, ident.MaxProviderID+1)
	for i := range long {
		long[i] = 'r'
	}
	assert.True(t, validProviderID(string(long[:ident.MaxProviderID])))
	assert.False(t, validProviderID(string(long)))
	assert.Empty(t, providerID(string(long)))
	assert.Equal(t, "req-1", providerID("req-1"))
}

func TestFractionDigitsScenarios(t *testing.T) {
	tests := []struct {
		v    float64
		want int
	}{
		{0, 0},
		{1, 0},
		{0.5, 1},
		{0.91, 2},
		{0.1234, 4},
		{1e-7, 7},
		{12.5, 1},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
		{float64(float32(0.33)), 2}, // 0.33000001311302185 as a float64
		{float64(float32(0.1234)), 4},
		{float64(float32(1e-7)), 7},
		{0.375, 3}, // exact in both widths
		{0.33000001311302185, 2},
		{0.3300000131130219, 16}, // not a float32, so its own digits count
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, fractionDigits(tt.v), "%v", tt.v)
	}
	widened := float64(float32(0.33))
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = fractionDigits(widened) }), "fractionDigits never allocates")
	assert.InDelta(t, 0.01, gridUnit(0), 0, "at least two digits")
	assert.InDelta(t, 1e-6, gridUnit(9), 0, "at most six digits")
}

func TestAppendCappedScenarios(t *testing.T) {
	pool := make([]string, 0, 4)
	a := appendCapped(&pool, []string{"a", "b"})
	b := appendCapped(&pool, []string{"c"})
	assert.Equal(t, []string{"a", "b"}, a)
	assert.Equal(t, []string{"c"}, b)
	assert.Equal(t, 2, cap(a), "capped to its own length")
	a = append(a, "x")
	assert.Equal(t, []string{"c"}, b, "appending to one never overwrites the next")
	assert.Equal(t, []string{"a", "b", "x"}, a)

	assert.Nil(t, appendCapped(&pool, nil))
	empty := appendCapped(&pool, []string{})
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	// Past the pool's capacity, earlier slices keep their elements.
	c := appendCapped(&pool, []string{"d", "e", "f"})
	assert.Equal(t, []string{"d", "e", "f"}, c)
	assert.Equal(t, []string{"c"}, b)

	floats := make([]float64, 0, 1)
	half, quarter := 0.5, 0.25
	p := appendPtr(&floats, &half)
	q := appendPtr(&floats, &quarter)
	assert.InDelta(t, 0.5, *p, 0)
	assert.InDelta(t, 0.25, *q, 0)
	assert.Nil(t, appendPtr(&floats, (*float64)(nil)))
}
