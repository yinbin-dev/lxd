package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/shared/features"
)

func TestFilterAPIExtensions(t *testing.T) {
	t.Cleanup(func() {
		require.NoError(t, features.LoadFromEnv(features.EnvVar))
	})

	t.Setenv(features.EnvVar, "")
	require.NoError(t, features.LoadFromEnv(features.EnvVar))

	// No gated extensions at all: the fast path returns extensions unchanged.
	extensionsOnly := []string{"foo", "bar"}
	assert.Equal(t, extensionsOnly, filterAPIExtensions(extensionsOnly, nil))

	const testExtension = "test_gated_extension"
	extensions := []string{"foo", "bar", testExtension}
	gated := map[string]features.Feature{testExtension: features.FailureDomainPlacement}

	assert.Equal(t, []string{"foo", "bar"}, filterAPIExtensions(extensions, gated))

	t.Setenv(features.EnvVar, string(features.FailureDomainPlacement))
	require.NoError(t, features.LoadFromEnv(features.EnvVar))
	assert.Equal(t, extensions, filterAPIExtensions(extensions, gated))
}
