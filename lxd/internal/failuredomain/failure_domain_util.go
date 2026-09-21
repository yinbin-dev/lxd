package failuredomain

import (
	"fmt"
	"maps"
	"slices"

	"github.com/canonical/lxd/lxd/internal/func/iterutil"
	"github.com/canonical/lxd/shared"
)

// ParseFailureDomains splits an instances.placement.failure_domain.exclude config value into its
// individual domain names, trimming whitespace around each and dropping empty entries.
func ParseFailureDomains(raw string) []string {
	return shared.SplitNTrimSpace(raw, ",", -1, true)
}

// ResolveCalculatedFailureDomains removes the excluded domains (if any) from the known set and returns the result.
func ResolveCalculatedFailureDomains(known map[string]struct{}, exclude []string) map[string]struct{} {
	if len(exclude) == 0 {
		return maps.Clone(known)
	}

	excluded := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		excluded[name] = struct{}{}
	}

	result := make(map[string]struct{}, len(known))
	for name := range known {
		_, isExcluded := excluded[name]
		if !isExcluded {
			result[name] = struct{}{}
		}
	}

	return result
}

// ValidateFailureDomains returns an error identifying every entry in names not present in known.
// An empty names is always valid.
func ValidateFailureDomains(known map[string]struct{}, names []string) error {
	isUnknown := func(name string) bool {
		_, ok := known[name]
		return !ok
	}

	unknown := slices.Collect(iterutil.Filter(slices.Values(names), isUnknown))
	if len(unknown) > 0 {
		return fmt.Errorf("%q are not known failure domains", unknown)
	}

	return nil
}
