//go:build linux && cgo && !agent

package db_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/osarch"
)

func TestLocateImage(t *testing.T) {
	cluster, cleanup := db.NewTestCluster(t)
	defer cleanup()

	_ = cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		err := tx.CreateImage(ctx,
			"default", "abc", "x.gz", 16, false, false, "amd64", time.Now(), time.Now(), map[string]string{}, "container", nil)
		require.NoError(t, err)

		address, err := tx.LocateImage(ctx, "abc")
		require.NoError(t, err)
		assert.Empty(t, address)

		// Pretend that the function is being run on another node.
		tx.NodeID(2)

		address, err = tx.LocateImage(ctx, "abc")
		require.NoError(t, err)
		assert.Equal(t, "0.0.0.0", address)

		// Pretend that the target node is down
		err = tx.SetNodeHeartbeat("0.0.0.0", time.Now().Add(-time.Minute))
		require.NoError(t, err)

		address, err = tx.LocateImage(ctx, "abc")
		require.Empty(t, address)
		require.EqualError(t, err, "Image not available on any online member")

		return nil
	})
}

func TestImageExists(t *testing.T) {
	cluster, cleanup := db.NewTestCluster(t)
	defer cleanup()

	_ = cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		exists, err := tx.ImageExists(ctx, "default", "abc")
		require.NoError(t, err)

		assert.False(t, exists)

		err = tx.CreateImage(ctx,
			"default", "abc", "x.gz", 16, false, false, "amd64", time.Now(), time.Now(), map[string]string{}, "container", nil)
		require.NoError(t, err)

		exists, err = tx.ImageExists(ctx, "default", "abc")
		require.NoError(t, err)

		assert.True(t, exists)

		return nil
	})
}

func TestGetImage(t *testing.T) {
	dbCluster, cleanup := db.NewTestCluster(t)
	defer cleanup()
	project := "default"

	_ = dbCluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		// public image with 'default' project
		err := tx.CreateImage(ctx, project, "abcd1", "x.gz", 16, true, false, "amd64", time.Now(), time.Now(), map[string]string{}, "container", nil)
		require.NoError(t, err)

		// 'public' is ignored if 'false'
		id, img, err := tx.GetImage(ctx, "a", cluster.ImageFilter{Project: &project})
		require.NoError(t, err)
		assert.True(t, img.Public)
		assert.NotEqual(t, -1, id)

		// non-public image with 'default' project
		err = tx.CreateImage(ctx, project, "abcd2", "x.gz", 16, false, false, "amd64", time.Now(), time.Now(), map[string]string{}, "container", nil)
		require.NoError(t, err)

		// empty project fails
		_, _, err = tx.GetImage(ctx, "a", cluster.ImageFilter{})
		require.Error(t, err)

		// 'public' is ignored if 'false', returning both entries
		_, _, err = tx.GetImage(ctx, "a", cluster.ImageFilter{Project: &project})
		require.Error(t, err)
		assert.True(t, api.StatusErrorCheck(err, http.StatusBadRequest))

		public := true
		id, img, err = tx.GetImage(ctx, "a", cluster.ImageFilter{Project: &project, Public: &public})
		require.NoError(t, err)
		assert.True(t, img.Public)
		assert.NotEqual(t, -1, id)

		return nil
	})
}

// withImageRegistry runs f in a transaction of a new test cluster that has the image registry "reg".
func withImageRegistry(t *testing.T, f func(ctx context.Context, tx *db.ClusterTx)) {
	dbCluster, cleanup := db.NewTestCluster(t)
	defer cleanup()

	err := dbCluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		_, err := cluster.CreateImageRegistry(ctx, tx.Tx(), cluster.ImageRegistriesRow{
			Name:     "reg",
			Protocol: cluster.ImageRegistryProtocol(api.ImageRegistryProtocolSimpleStreams),
		})
		require.NoError(t, err)

		f(ctx, tx)
		return nil
	})
	require.NoError(t, err)
}

// cacheImage creates a cached container image with the given fingerprint and architecture from alias "ubuntu" of "reg".
func cacheImage(ctx context.Context, t *testing.T, tx *db.ClusterTx, fingerprint string, architecture string) {
	err := tx.CreateImage(ctx, "default", fingerprint, fingerprint+".gz", 16, false, true, architecture, time.Now(), time.Now(), map[string]string{}, "container", nil)
	require.NoError(t, err)

	project := "default"
	id, _, err := tx.GetImage(ctx, fingerprint, cluster.ImageFilter{Project: &project})
	require.NoError(t, err)

	err = tx.CreateImageSource(ctx, id, "reg", "ubuntu")
	require.NoError(t, err)
}

// architectureID returns the ID of the architecture with the given name.
func architectureID(t *testing.T, name string) int {
	id, err := osarch.ArchitectureId(name)
	require.NoError(t, err)

	return id
}

// TestFindCachedImageSourceFingerprint_NoCachedImage checks that no fingerprint is found when no image is cached.
func TestFindCachedImageSourceFingerprint_NoCachedImage(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		architectures := []int{architectureID(t, "x86_64"), architectureID(t, "i686")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "container", architectures)
		require.NoError(t, err)
		assert.Empty(t, fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_LaterArchitecture checks that a later architecture is found when the first one has no cached image.
func TestFindCachedImageSourceFingerprint_LaterArchitecture(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "x86_64"), architectureID(t, "i686")}

		fingerprint, architecture, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "container", architectures)
		require.NoError(t, err)
		assert.Equal(t, "bbb", fingerprint)
		assert.Equal(t, architectureID(t, "i686"), architecture)
	})
}

// TestFindCachedImageSourceFingerprint_FirstArchitectureWins checks that the first architecture wins when two architectures have a cached image.
func TestFindCachedImageSourceFingerprint_FirstArchitectureWins(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "aaa", "x86_64")
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "x86_64"), architectureID(t, "i686")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "container", architectures)
		require.NoError(t, err)
		assert.Equal(t, "aaa", fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_ReversedArchitectureOrder checks that the order of the architectures, not their IDs, selects the cached image.
func TestFindCachedImageSourceFingerprint_ReversedArchitectureOrder(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "aaa", "x86_64")
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "i686"), architectureID(t, "x86_64")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "container", architectures)
		require.NoError(t, err)
		assert.Equal(t, "bbb", fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_OtherArchitecture checks that a cached image of a different architecture is not found.
func TestFindCachedImageSourceFingerprint_OtherArchitecture(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "aarch64")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "container", architectures)
		require.NoError(t, err)
		assert.Empty(t, fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_OtherAlias checks that a cached image of a different alias is not found.
func TestFindCachedImageSourceFingerprint_OtherAlias(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "i686")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "debian", "container", architectures)
		require.NoError(t, err)
		assert.Empty(t, fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_OtherType checks that a cached image of a different instance type is not found.
func TestFindCachedImageSourceFingerprint_OtherType(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		cacheImage(ctx, t, tx, "bbb", "i686")
		architectures := []int{architectureID(t, "i686")}

		fingerprint, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "virtual-machine", architectures)
		require.NoError(t, err)
		assert.Empty(t, fingerprint)
	})
}

// TestFindCachedImageSourceFingerprint_InvalidType checks that an error of the lookup is returned.
func TestFindCachedImageSourceFingerprint_InvalidType(t *testing.T) {
	withImageRegistry(t, func(ctx context.Context, tx *db.ClusterTx) {
		architectures := []int{architectureID(t, "i686")}

		_, _, err := tx.FindCachedImageSourceFingerprint(ctx, "reg", "ubuntu", "invalid", architectures)
		require.Error(t, err)
	})
}
