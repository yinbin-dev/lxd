package main

import (
	"slices"

	"github.com/canonical/lxd/lxd/internal/func/iterutil"
	"github.com/canonical/lxd/shared/features"
	"github.com/canonical/lxd/shared/version"
)

// Hides API extensions that are gated behind feature previews.
var gatedAPIExtensions = map[string]features.Feature{}

// visibleAPIExtensions returns version.APIExtensions minus whichever gatedAPIExtensions entries
// have their own gating feature currently disabled.
func visibleAPIExtensions() []string {
	return filterAPIExtensions(version.APIExtensions, gatedAPIExtensions)
}

// filterAPIExtensions returns extensions minus whichever gated entries have their own gating
// feature currently disabled.
func filterAPIExtensions(extensions []string, gated map[string]features.Feature) []string {
	if len(gated) == 0 {
		return extensions
	}

	isVisible := func(ext string) bool {
		feature, ok := gated[ext]
		return !ok || features.IsEnabled(feature)
	}

	visible := make([]string, 0, len(extensions))
	return slices.AppendSeq(visible, iterutil.Filter(slices.Values(extensions), isVisible))
}
