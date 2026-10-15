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
