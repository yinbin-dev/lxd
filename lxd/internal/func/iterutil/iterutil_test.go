package iterutil

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilter(t *testing.T) {
	even := func(n int) bool { return n%2 == 0 }

	got := slices.Collect(Filter(slices.Values([]int{1, 2, 3, 4, 5, 6}), even))
	assert.Equal(t, []int{2, 4, 6}, got)

	// Both nil and empty input produce an empty result.
	assert.Empty(t, slices.Collect(Filter(slices.Values[[]int](nil), even)))
	assert.Empty(t, slices.Collect(Filter(slices.Values([]int{}), even)))

	// A nil seq itself (not just a seq over a nil slice) yields nothing rather than panicking.
	assert.Empty(t, slices.Collect(Filter(nil, even)))

	// A nil keep keeps every element rather than panicking.
	assert.Equal(t, []int{1, 2, 3}, slices.Collect(Filter(slices.Values([]int{1, 2, 3}), nil)))

	// A nil seq and a nil keep together still yield nothing rather than panicking.
	assert.Empty(t, slices.Collect(Filter[int](nil, nil)))

	// Stops pulling from seq once yield returns false.
	var visited []int
	for v := range Filter(slices.Values([]int{1, 2, 3, 4, 5}), even) {
		visited = append(visited, v)
		break
	}

	assert.Equal(t, []int{2}, visited)
}

func TestFilter2(t *testing.T) {
	nonZeroKey := func(k int, _ string) bool { return k != 0 }

	m := map[int]string{0: "default", 1: "one", 2: "two"}
	got := maps.Collect(Filter2(maps.All(m), nonZeroKey))
	assert.Equal(t, map[int]string{1: "one", 2: "two"}, got)

	// Both nil and empty input maps produce an empty (non-nil) result.
	assert.Equal(t, map[int]string{}, maps.Collect(Filter2(maps.All[map[int]string](nil), nonZeroKey)))
	assert.Equal(t, map[int]string{}, maps.Collect(Filter2(maps.All(map[int]string{}), nonZeroKey)))

	// Stops pulling from seq once yield returns false.
	visited := 0
	for range Filter2(maps.All(m), nonZeroKey) {
		visited++
		break
	}

	assert.Equal(t, 1, visited)
}
