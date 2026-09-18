package engine

import (
	"context"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/placement/internal/models"
)

// FilterStage narrows a candidate cluster member list using whatever models.PlacementContext
// fields its own algorithm needs, or returns an error explaining why no candidates remain.
// A FilterStage can also be used to modify the PlacementContext or perform side effects,
// as long as it adheres to the contract of returning the unmodified candidate list and an error if applicable.
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
		// If an earlier Apply call already failed, f is skipped entirely and the earlier error carries forward unchanged.
		// This is the only place the chain's short-circuit-on-error check exists: individual FilterStage
		// values never perform it themselves, so a new FilterStage can never accidentally skip it.
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
