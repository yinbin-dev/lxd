package config_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clusterConfig "github.com/canonical/lxd/lxd/cluster/config"
	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/shared/features"
)

// TestMain turns the failure-domain-aware placement feature preview on for tests.
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

// The server configuration is initially empty.
func TestConfigLoad_Initial(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	clusterUUID := config.ClusterUUID()
	uuidv7, err := uuid.Parse(clusterUUID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), uuidv7.Version())
	assert.Equal(t, map[string]string{
		"volatile.uuid": clusterUUID,
	}, config.Dump())

	assert.Equal(t, float64(20), config.OfflineThreshold().Seconds())
}

// If the database contains invalid keys, they are ignored.
func TestConfigLoad_IgnoreInvalidKeys(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	err := tx.UpdateClusterConfig(map[string]string{
		"foo":             "garbage",
		"core.proxy_http": "foo.bar",
	})
	require.NoError(t, err)

	config, err := clusterConfig.Load(context.Background(), tx)

	require.NoError(t, err)
	values := map[string]string{"core.proxy_http": "foo.bar", "volatile.uuid": config.ClusterUUID()}
	assert.Equal(t, values, config.Dump())
}

// Triggers can be specified to execute custom code on config key changes.
func TestConfigLoad_Triggers(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"volatile.uuid": config.ClusterUUID(),
	}, config.Dump())
}

func TestConfig_DumpPublicUnauthenticated(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	publicConfig := config.DumpPublic(false)
	assert.NotContains(t, publicConfig, "volatile.uuid")
}

func TestConfig_DumpPublicAuthenticated(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	publicConfig := config.DumpPublic(true)
	assert.Equal(t, config.ClusterUUID(), publicConfig["volatile.uuid"])
}

// Offline threshold must be greater than the heartbeat interval.
func TestConfigLoad_OfflineThresholdValidator(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	_, err = config.Patch(context.Background(), tx, map[string]string{"cluster.offline_threshold": "2"})
	require.EqualError(t, err, `Cannot set "cluster.offline_threshold" to "2": Value must be greater than 10`)
}

// Max number of voters must be odd.
func TestConfigLoad_MaxVotersValidator(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	_, err = config.Patch(context.Background(), tx, map[string]string{"cluster.max_voters": "4"})
	require.EqualError(t, err, `Cannot set "cluster.max_voters" to "4": Value must be an odd number equal to or higher than 3`)
}

// TestConfigLoad_FailureDomainsValidator asserts unknown names are rejected and known names are
// accepted.
func TestConfigLoad_FailureDomainsValidator(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	_, err = config.Patch(context.Background(), tx, map[string]string{"instances.placement.failure_domain.exclude": "az1"})
	require.EqualError(t, err, `Invalid value for "instances.placement.failure_domain.exclude": ["az1"] are not known failure domains`)

	id, err := tx.CreateNode("buzz", "1.2.3.4:666")
	require.NoError(t, err)
	require.NoError(t, tx.UpdateNodeFailureDomain(context.Background(), id, "az1"))

	changed, err := config.Patch(context.Background(), tx, map[string]string{"instances.placement.failure_domain.exclude": "az1"})
	require.NoError(t, err)
	assert.Equal(t, "az1", changed["instances.placement.failure_domain.exclude"])
	assert.Equal(t, []string{"az1"}, config.ExcludedFailureDomains())
}

// TestConfigLoad_FailureDomainsFeatureGate confirms instances.placement.failure_domain.exclude is
// rejected outright while FailureDomainPlacement is disabled.
func TestConfigLoad_FailureDomainsFeatureGate(t *testing.T) {
	// Re-syncs the cached feature snapshot after t.Setenv restores the environment.
	t.Cleanup(func() {
		require.NoError(t, features.LoadFromEnv(features.EnvVar))
	})

	t.Setenv(features.EnvVar, "")
	require.NoError(t, features.LoadFromEnv(features.EnvVar))

	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	_, err = config.Patch(context.Background(), tx, map[string]string{"instances.placement.failure_domain.exclude": "az1"})
	require.EqualError(t, err, "Unknown key")
}

// If some previously set values are missing from the ones passed to Replace(),
// they are deleted from the configuration.
func TestConfig_ReplaceDeleteValues(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	changed, err := config.Replace(context.Background(), tx, map[string]string{"core.proxy_http": "foo.bar"})
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{
		"core.proxy_http": "foo.bar",
		// Validation that the volatile.uuid value cannot change happens in the PUT/PATCH /1.0 API handlers.
		"volatile.uuid": "",
	}, changed)

	_, err = config.Replace(context.Background(), tx, map[string]string{})
	assert.NoError(t, err)

	assert.Empty(t, config.ProxyHTTP())

	values, err := tx.Config(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{}, values)
}

// If some previously set values are missing from the ones passed to Patch(),
// they are kept as they are.
func TestConfig_PatchKeepsValues(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	config, err := clusterConfig.Load(context.Background(), tx)
	require.NoError(t, err)

	_, err = config.Replace(context.Background(), tx, map[string]string{"core.proxy_http": "foo.bar"})
	assert.NoError(t, err)

	_, err = config.Patch(context.Background(), tx, map[string]string{})
	assert.NoError(t, err)

	assert.Equal(t, "foo.bar", config.ProxyHTTP())

	values, err := tx.Config(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"core.proxy_http": "foo.bar"}, values)
}
