package cluster

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/features"
)

// TestMain turns on the failure-domain-aware placement feature preview for tests in this package.
func TestMain(m *testing.M) {
	err := os.Setenv(features.EnvVar, string(features.FailureDomainPlacement))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = features.LoadFromEnv(features.EnvVar)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestPlacementGroupToAPIScopeBackwardCompatibility asserts a missing scope key normalizes to host.
func TestPlacementGroupToAPIScopeBackwardCompatibility(t *testing.T) {
	group := PlacementGroup{
		Row:         PlacementGroupsRow{ID: 1, Name: "pg1"},
		ProjectName: "default",
	}

	cases := []struct {
		name      string
		configs   map[int64]map[string]string
		wantScope string
	}{
		{
			name:      "no config entry at all for this group",
			configs:   map[int64]map[string]string{},
			wantScope: api.PlacementScopeHost,
		},
		{
			name: "config present but scope key absent (pre-scope group)",
			configs: map[int64]map[string]string{
				1: {"policy": api.PlacementPolicySpread, "rigor": api.PlacementRigorStrict},
			},
			wantScope: api.PlacementScopeHost,
		},
		{
			name: "scope explicitly set to host",
			configs: map[int64]map[string]string{
				1: {"policy": api.PlacementPolicySpread, "rigor": api.PlacementRigorStrict, "scope": api.PlacementScopeHost},
			},
			wantScope: api.PlacementScopeHost,
		},
		{
			name: "scope explicitly set to failure-domain is preserved unchanged",
			configs: map[int64]map[string]string{
				1: {"policy": api.PlacementPolicySpread, "rigor": api.PlacementRigorStrict, "scope": api.PlacementScopeFailureDomain},
			},
			wantScope: api.PlacementScopeFailureDomain,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := group.ToAPI(tt.configs)
			assert.Equal(t, tt.wantScope, got.Config["scope"])
		})
	}
}

// TestPlacementGroupToAPIDoesNotMutateInputConfigs asserts ToAPI never writes scope back into
// its input configs map.
func TestPlacementGroupToAPIDoesNotMutateInputConfigs(t *testing.T) {
	group := PlacementGroup{
		Row:         PlacementGroupsRow{ID: 1, Name: "pg1"},
		ProjectName: "default",
	}

	configs := map[int64]map[string]string{
		1: {"policy": api.PlacementPolicySpread, "rigor": api.PlacementRigorStrict},
	}

	got := group.ToAPI(configs)

	assert.Equal(t, api.PlacementScopeHost, got.Config["scope"])
	_, ok := configs[1]["scope"]
	assert.False(t, ok, "ToAPI must not write scope back into the caller's configs map")
}

// TestPlacementGroupToAPIFeatureGate asserts scope is absent from ToAPI's output while the
// feature preview is disabled.
func TestPlacementGroupToAPIFeatureGate(t *testing.T) {
	// Re-syncs the cached feature snapshot after t.Setenv restores the environment.
	t.Cleanup(func() {
		require.NoError(t, features.LoadFromEnv(features.EnvVar))
	})

	t.Setenv(features.EnvVar, "")
	require.NoError(t, features.LoadFromEnv(features.EnvVar))

	group := PlacementGroup{
		Row:         PlacementGroupsRow{ID: 1, Name: "pg1"},
		ProjectName: "default",
	}

	got := group.ToAPI(map[int64]map[string]string{
		1: {"policy": api.PlacementPolicySpread, "rigor": api.PlacementRigorStrict},
	})

	_, ok := got.Config["scope"]
	assert.False(t, ok, "scope must not appear at all while the feature preview is off")
}
