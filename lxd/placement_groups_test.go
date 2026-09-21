package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestPlacementGroupDefaultConfig(t *testing.T) {
	// Nil config (e.g. a create request that omits "config" entirely): must not panic, and scope
	// still ends up defaulted.
	got := placementGroupDefaultConfig(nil)
	assert.Equal(t, "host", got["scope"])

	// scope missing alongside other keys: defaulted, other keys untouched.
	got = placementGroupDefaultConfig(map[string]string{"policy": "spread", "rigor": "strict"})
	assert.Equal(t, map[string]string{"policy": "spread", "rigor": "strict", "scope": "host"}, got)

	// scope already set to a non-empty value: left unchanged.
	got = placementGroupDefaultConfig(map[string]string{"scope": "failure-domain"})
	assert.Equal(t, "failure-domain", got["scope"])

	got = placementGroupDefaultConfig(map[string]string{"scope": "bogus"})
	assert.Equal(t, "bogus", got["scope"])
}

func TestPlacementGroupFeatureGate(t *testing.T) {
	// Re-syncs the cached feature snapshot after t.Setenv restores the environment.
	t.Cleanup(func() {
		require.NoError(t, features.LoadFromEnv(features.EnvVar))
	})

	t.Setenv(features.EnvVar, "")
	require.NoError(t, features.LoadFromEnv(features.EnvVar))

	got := placementGroupDefaultConfig(nil)
	assert.NotContains(t, got, "scope")

	err := placementGroupValidateConfig(map[string]string{"policy": "spread", "rigor": "strict", "scope": "host"})
	assert.EqualError(t, err, `Invalid placement group key "scope"`)

	// policy/rigor alone, with no scope at all, are still accepted.
	err = placementGroupValidateConfig(map[string]string{"policy": "spread", "rigor": "strict"})
	assert.NoError(t, err)
}
