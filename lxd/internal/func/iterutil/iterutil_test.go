package iterutil

import (
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
