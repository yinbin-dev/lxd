package placement

import (
	"context"
	"errors"
	"fmt"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/placement/internal/engine"
	"github.com/canonical/lxd/lxd/placement/internal/filters"
	"github.com/canonical/lxd/lxd/placement/internal/models"
	"github.com/canonical/lxd/shared/api"
)

// ErrNoEligibleCandidate is returned (wrapped) by PlaceInstance whenever any FilterStage in its
// chain fails to find a candidate. Check with errors.Is.
var ErrNoEligibleCandidate = errors.New("no eligible candidate cluster member")

// PlaceInstance is the placement package's sole entry point: it narrows candidates by cluster
// group or placement group, then picks the single least-loaded cluster member among whatever
// remains. It never returns an API error — on failure it returns ErrNoEligibleCandidate (check
// with errors.Is), wrapping the specific underlying reason.
func PlaceInstance(ctx context.Context, tx *db.ClusterTx, candidates []db.NodeInfo, placementGroup *api.PlacementGroup, clusterGroupName string, evacuation bool) (*db.NodeInfo, error) {
	pctx := &models.PlacementContext{ClusterGroupName: clusterGroupName, Evacuation: evacuation}

	if placementGroup != nil {
		pctx.PlacementGroup = *placementGroup
	}

	selected, err := engine.New(ctx, tx, pctx, candidates).
		Apply(filters.FilterByClusterGroup).
		Apply(filters.FilterByPlacementGroup).
		Apply(filters.SelectLeastLoaded).
		Result()
	if err != nil {
		return nil, fmt.Errorf("%w. (reason: %w)", ErrNoEligibleCandidate, err)
	}

	return &selected[0], nil
}
