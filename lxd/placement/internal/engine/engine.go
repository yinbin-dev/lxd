package engine

import (
	"context"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/placement/internal/models"
)

// FilterStage narrows a candidate cluster member list using whatever models.PlacementContext
// fields its own algorithm needs, or returns an error explaining why no candidates remain.
type FilterStage func(ctx context.Context, tx *db.ClusterTx, pctx *models.PlacementContext, candidates []db.NodeInfo) ([]db.NodeInfo, error)

// PlacementEngine narrows a candidate cluster member list by applying a sequence of FilterStages
// using ctx/tx/models.PlacementContext.
type PlacementEngine struct {
	ctx        context.Context
	tx         *db.ClusterTx
	pctx       *models.PlacementContext
	candidates []db.NodeInfo
	err        error
}

// New starts a placement decision over the given candidates.
func New(ctx context.Context, tx *db.ClusterTx, pctx *models.PlacementContext, candidates []db.NodeInfo) *PlacementEngine {
	return &PlacementEngine{ctx: ctx, tx: tx, pctx: pctx, candidates: candidates}
}

// Apply runs f against the engine's current candidates.
func (e *PlacementEngine) Apply(f FilterStage) *PlacementEngine {
	if e.err != nil {
		// Skip f if an earlier Apply call already failed.
		return e
	}

	e.candidates, e.err = f(e.ctx, e.tx, e.pctx, e.candidates)
	return e
}

// Result returns the fully-narrowed candidates, or the first error returned by any applied
// FilterStage.
func (e *PlacementEngine) Result() ([]db.NodeInfo, error) {
	return e.candidates, e.err
}
