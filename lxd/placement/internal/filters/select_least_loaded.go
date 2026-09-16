package filters

import (
	"context"
	"errors"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/placement/internal/models"
)

var errNoCandidatesToSelectFrom = errors.New("No cluster members remain to select the least-loaded one from")

// SelectLeastLoaded narrows whatever candidates remain to the single least-loaded cluster member.
func SelectLeastLoaded(ctx context.Context, tx *db.ClusterTx, pctx *models.PlacementContext, candidates []db.NodeInfo) ([]db.NodeInfo, error) {
	if len(candidates) == 0 {
		return nil, errNoCandidatesToSelectFrom
	}

	selected, err := tx.GetNodeWithLeastInstances(ctx, candidates)
	if err != nil {
		return nil, err
	}

	return []db.NodeInfo{*selected}, nil
}
