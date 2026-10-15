//go:build linux && cgo && !agent

package db

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
)

// instanceNotHeld is the SQL condition that no operation holds the instance row as an operation resource.
const instanceNotHeld = `NOT EXISTS (SELECT 1 FROM operations_resources WHERE operations_resources.entity_type = ? AND operations_resources.entity_id = instances.id)`

// CheckInstanceHandoff returns a 404 error unless the instance row with the given ID is on this member and no
// operation holds it. The target runs it in the transaction that registers its create operation.
func (c *ClusterTx) CheckInstanceHandoff(ctx context.Context, id int) error {
	q := `SELECT 1 FROM instances WHERE instances.id = ? AND instances.node_id = ? AND ` + instanceNotHeld

	var found int
	err := c.tx.QueryRowContext(ctx, q, id, c.nodeID, cluster.EntityType(entity.TypeInstance)).Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return api.StatusErrorf(http.StatusNotFound, "Instance creation was cancelled")
		}

		return err
	}

	return nil
}

// DeleteInstanceIfNotHeld deletes the instance row with the given ID when no operation holds it, and reports
// whether it deleted the row. The receiver of a create request uses it when it knows that the handoff did not occur.
func (c *ClusterTx) DeleteInstanceIfNotHeld(ctx context.Context, id int) (bool, error) {
	q := `DELETE FROM instances WHERE instances.id = ? AND ` + instanceNotHeld

	res, err := c.tx.ExecContext(ctx, q, id, cluster.EntityType(entity.TypeInstance))
	if err != nil {
		return false, err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return n > 0, nil
}

// IsInstanceHeld reports whether an operation holds the instance row with the given ID as an operation resource.
func (c *ClusterTx) IsInstanceHeld(ctx context.Context, id int) (bool, error) {
	q := `SELECT COUNT(*) FROM operations_resources WHERE entity_type = ? AND entity_id = ?`

	var count int
	err := c.tx.QueryRowContext(ctx, q, cluster.EntityType(entity.TypeInstance), id).Scan(&count)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}
