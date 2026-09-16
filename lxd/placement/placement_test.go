package placement

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/db"
)

// TestPlaceInstanceSkipsStagesWithNoInputs confirms a single live candidate is picked unchanged
// when placementGroup is nil.
func TestPlaceInstanceSkipsStagesWithNoInputs(t *testing.T) {
	testCluster, cleanup := db.NewTestCluster(t)
	defer cleanup()

	var candidate db.NodeInfo
	err := testCluster.Transaction(context.Background(), func(ctx context.Context, tx *db.ClusterTx) error {
		id, err := tx.CreateNode("member01", "192.0.2.1")
		require.NoError(t, err)
		candidate = db.NodeInfo{ID: id, Name: "member01"}
		return nil
	})
	require.NoError(t, err)

	err = testCluster.Transaction(context.Background(), func(ctx context.Context, tx *db.ClusterTx) error {
		selected, err := PlaceInstance(ctx, tx, []db.NodeInfo{candidate}, nil, "", false)
		require.NoError(t, err)
		require.Equal(t, candidate, *selected)
		return nil
	})
	require.NoError(t, err)
}

// TestPlaceInstanceWrapsFilterStageFailure confirms every failure surfaces as ErrNoEligibleCandidate.
func TestPlaceInstanceWrapsFilterStageFailure(t *testing.T) {
	testCluster, cleanup := db.NewTestCluster(t)
	defer cleanup()

	err := testCluster.Transaction(context.Background(), func(ctx context.Context, tx *db.ClusterTx) error {
		_, err := PlaceInstance(ctx, tx, nil, nil, "", false)
		require.ErrorIs(t, err, ErrNoEligibleCandidate)
		return nil
	})
	require.NoError(t, err)
}
