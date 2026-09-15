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
