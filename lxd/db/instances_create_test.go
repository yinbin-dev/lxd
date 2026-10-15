//go:build linux && cgo && !agent

package db_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
)

// holdInstance registers an operation that holds the instance with the given ID as an operation resource.
func holdInstance(t *testing.T, tx *db.ClusterTx, instanceID int) {
	t.Helper()

	res, err := tx.Tx().Exec(`INSERT INTO operations (uuid, node_id, type, metadata, inputs, error, conflict_reference) VALUES ('op-1', 1, 1, '{}', '{}', '', '')`)
	require.NoError(t, err)

	opID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = tx.Tx().Exec(`INSERT INTO operations_resources (operation_id, entity_id, entity_type) VALUES (?, ?, ?)`, opID, instanceID, cluster.EntityType(entity.TypeInstance))
	require.NoError(t, err)
}

func TestCheckInstanceHandoff(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	ctx := context.Background()

	nodeID2, err := tx.CreateNode("node2", "1.2.3.4:666")
	require.NoError(t, err)

	addContainer(t, tx, 1, "local")
	addContainer(t, tx, nodeID2, "remote")

	localID, err := tx.GetInstanceID(ctx, "default", "local")
	require.NoError(t, err)

	remoteID, err := tx.GetInstanceID(ctx, "default", "remote")
	require.NoError(t, err)

	// A row of this member that no operation holds.
	require.NoError(t, tx.CheckInstanceHandoff(ctx, localID))

	// A row of a different member.
	err = tx.CheckInstanceHandoff(ctx, remoteID)
	require.True(t, api.StatusErrorCheck(err, http.StatusNotFound))

	// A missing row.
	err = tx.CheckInstanceHandoff(ctx, remoteID+100)
	require.True(t, api.StatusErrorCheck(err, http.StatusNotFound))

	// A row that an operation holds.
	holdInstance(t, tx, localID)
	err = tx.CheckInstanceHandoff(ctx, localID)
	require.True(t, api.StatusErrorCheck(err, http.StatusNotFound))
}

func TestDeleteInstanceIfNotHeld(t *testing.T) {
	tx, cleanup := db.NewTestClusterTx(t)
	defer cleanup()

	ctx := context.Background()

	addContainer(t, tx, 1, "free")
	addContainer(t, tx, 1, "held")

	freeID, err := tx.GetInstanceID(ctx, "default", "free")
	require.NoError(t, err)

	heldID, err := tx.GetInstanceID(ctx, "default", "held")
	require.NoError(t, err)

	holdInstance(t, tx, heldID)

	held, err := tx.IsInstanceHeld(ctx, heldID)
	require.NoError(t, err)
	require.True(t, held)

	held, err = tx.IsInstanceHeld(ctx, freeID)
	require.NoError(t, err)
	require.False(t, held)

	// A row that an operation holds stays.
	deleted, err := tx.DeleteInstanceIfNotHeld(ctx, heldID)
	require.NoError(t, err)
	require.False(t, deleted)

	_, err = tx.GetInstanceID(ctx, "default", "held")
	require.NoError(t, err)

	// A row that no operation holds goes.
	deleted, err = tx.DeleteInstanceIfNotHeld(ctx, freeID)
	require.NoError(t, err)
	require.True(t, deleted)

	_, err = tx.GetInstanceID(ctx, "default", "free")
	require.True(t, api.StatusErrorCheck(err, http.StatusNotFound))

	// A missing row deletes nothing.
	deleted, err = tx.DeleteInstanceIfNotHeld(ctx, freeID)
	require.NoError(t, err)
	require.False(t, deleted)
}
