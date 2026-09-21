// Package iterutil provides small generic helpers for working with iter.Seq sequences.
package iterutil

import (
	"iter"
)

// Filter returns a sequence containing only the elements of seq for which keep returns true. A
// nil seq yields nothing; a nil keep returns seq unchanged.
func Filter[T any](seq iter.Seq[T], keep func(T) bool) iter.Seq[T] {
	if keep == nil {
		if seq == nil {
			return func(func(T) bool) {}
		}

		return seq
	}

	return func(yield func(T) bool) {
		if seq == nil {
			return
		}

		for v := range seq {
			if keep(v) && !yield(v) {
				return
			}
		}
	}
}

// Filter2 returns a sequence containing only the (key, value) pairs of seq for which keep returns
// true. It's the two-argument counterpart to Filter, for filtering a map via maps.All before
// rebuilding it with maps.Collect.
func Filter2[K, V any](seq iter.Seq2[K, V], keep func(K, V) bool) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for k, v := range seq {
			if keep(k, v) && !yield(k, v) {
				return
			}
		}
	}
}
