package failuredomain

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"
)

// stringSet builds a map[string]struct{} from items.
func stringSet(items ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, item := range items {
		s[item] = struct{}{}
	}

	return s
}

var domainPool = []string{"fd1", "fd2", "fd3", "fd4", "fd5"}

// genDomainSubset draws a random subset of pool, preserving pool's order (and therefore
// distinctness), by independently flipping a coin for each element.
func genDomainSubset(t *rapid.T, pool []string, label string) []string {
	var subset []string
	for _, name := range pool {
		if rapid.Bool().Draw(t, label+"-"+name) {
			subset = append(subset, name)
		}
	}

	return subset
}

// TestResolveCalculatedFailureDomainsNarrowingProperty asserts the result is always a subset of
// known and never contains an excluded name.
func TestResolveCalculatedFailureDomainsNarrowingProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		known := genDomainSubset(t, domainPool, "known")
		exclude := genDomainSubset(t, domainPool, "exclude")

		calculated := ResolveCalculatedFailureDomains(stringSet(known...), exclude)

		for name := range calculated {
			if !slices.Contains(known, name) {
				t.Fatalf("calculated domain %q is not in known %v", name, known)
			}

			if slices.Contains(exclude, name) {
				t.Fatalf("calculated domain %q is in the exclude list %v", name, exclude)
			}
		}
	})
}

// TestResolveCalculatedFailureDomainsIdentityProperty asserts an empty exclude list leaves known
// unchanged.
func TestResolveCalculatedFailureDomainsIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		known := genDomainSubset(t, domainPool, "known")

		calculated := ResolveCalculatedFailureDomains(stringSet(known...), nil)

		assert.ElementsMatch(t, known, slices.Collect(maps.Keys(calculated)))
	})
}

// TestValidateFailureDomainsProperty asserts a name is accepted only if it's present in known.
func TestValidateFailureDomainsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		known := genDomainSubset(t, domainPool, "known")
		names := genDomainSubset(t, domainPool, "names")

		err := ValidateFailureDomains(stringSet(known...), names)

		allKnown := true
		for _, name := range names {
			if !slices.Contains(known, name) {
				allKnown = false
				break
			}
		}

		if allKnown {
			assert.NoError(t, err)
		} else {
			assert.Error(t, err)
		}
	})
}

func TestValidateFailureDomainsEmptyNamesAlwaysValid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		known := genDomainSubset(t, domainPool, "known")

		assert.NoError(t, ValidateFailureDomains(stringSet(known...), nil))
	})
}

// TestValidateFailureDomainsReportsAllInvalidNames asserts every unknown name is identified in
// the returned error, not just the first one.
func TestValidateFailureDomainsReportsAllInvalidNames(t *testing.T) {
	err := ValidateFailureDomains(stringSet("fd1", "fd2"), []string{"fd1", "bogus1", "fd2", "bogus2"})
	if !assert.Error(t, err) {
		return
	}

	assert.Contains(t, err.Error(), "bogus1")
	assert.Contains(t, err.Error(), "bogus2")
	assert.NotContains(t, err.Error(), "fd1", "a known name should never be reported as invalid")
	assert.NotContains(t, err.Error(), "fd2", "a known name should never be reported as invalid")
}

// TestResolveCalculatedFailureDomainsNilAndEmptyInputs covers nil vs. empty exclude and known
// inputs.
func TestResolveCalculatedFailureDomainsNilAndEmptyInputs(t *testing.T) {
	for _, known := range []map[string]struct{}{nil, {}} {
		for _, exclude := range [][]string{nil, {}} {
			assert.Empty(t, ResolveCalculatedFailureDomains(known, exclude))
		}

		// A non-empty exclude list against a nil/empty known has nothing to remove from.
		assert.Empty(t, ResolveCalculatedFailureDomains(known, []string{"fd1"}))
	}

	// A nil/empty exclude list against a non-empty known leaves known untouched.
	known := stringSet("fd1", "fd2")
	for _, exclude := range [][]string{nil, {}} {
		assert.ElementsMatch(t, []string{"fd1", "fd2"}, slices.Collect(maps.Keys(ResolveCalculatedFailureDomains(known, exclude))))
	}

	// A non-empty exclude list removes exactly the named domains from known, nothing else.
	assert.ElementsMatch(t, []string{"fd2"}, slices.Collect(maps.Keys(ResolveCalculatedFailureDomains(known, []string{"fd1"}))))
}

// TestValidateFailureDomainsNilAndEmptyInputs covers nil vs. empty names and known inputs.
func TestValidateFailureDomainsNilAndEmptyInputs(t *testing.T) {
	for _, names := range [][]string{nil, {}} {
		for _, known := range []map[string]struct{}{nil, {}} {
			assert.NoError(t, ValidateFailureDomains(known, names), "empty names is always valid, even against nil/empty known")
		}
	}

	// A non-empty names against a nil/empty known is always rejected.
	for _, known := range []map[string]struct{}{nil, {}} {
		assert.Error(t, ValidateFailureDomains(known, []string{"fd1"}))
	}
}
