package main

import (
	"context"

	lxd "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/lxd/db"
	dbCluster "github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/placement"
	"github.com/canonical/lxd/lxd/project/limits"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/osarch"
)

// instancesPostPlace selects the cluster member for a new instance. It filters the members on the architectures of
// the source, the cluster group of the target, and the cluster groups that the project permits, and then applies the
// placement group of the instance. It also returns the name of the placement group.
func instancesPostPlace(ctx context.Context, s *state.State, tx *db.ClusterTx, targetProject *api.Project, req api.InstancesPost, profiles []api.Profile, sourceInst *dbCluster.Instance, sourceImageRef string, remoteServer lxd.ImageServer, allMembers []db.NodeInfo, targetGroupName string) (*db.NodeInfo, string, error) {
	// Determine which cluster members are suitable for this instance based on its image architecture.
	// We pass the pre-connected remoteServer (if any) to resolve architectures for registry-based images.
	architectures, err := instance.SuitableArchitectures(ctx, s, tx, targetProject.Name, sourceInst, sourceImageRef, req, remoteServer)
	if err != nil {
		return nil, "", err
	}

	// If no architectures have been ascertained from the source then use the default
	// architecture from project or global config if available.
	if len(architectures) < 1 {
		defaultArch := targetProject.Config["images.default_architecture"]
		if defaultArch == "" {
			defaultArch = s.GlobalConfig.ImagesDefaultArchitecture()
		}

		if defaultArch != "" {
			defaultArchID, err := osarch.ArchitectureId(defaultArch)
			if err != nil {
				return nil, "", err
			}

			architectures = append(architectures, defaultArchID)
		} else {
			architectures = nil // Don't exclude candidate members based on architecture.
		}
	}

	clusterGroupsAllowed := limits.GetRestrictedClusterGroups(targetProject)

	candidateMembers, err := tx.GetCandidateMembers(ctx, allMembers, architectures, targetGroupName, clusterGroupsAllowed, s.GlobalConfig.OfflineThreshold())
	if err != nil {
		return nil, "", err
	}

	expandedConfig := instancetype.ExpandInstanceConfig(s.GlobalConfig.Dump(), req.Config, profiles)
	placementGroupName := expandedConfig["placement.group"]
	targetMemberInfo, err := instancesPostSelectClusterMember(ctx, tx, placementGroupName, candidateMembers, targetProject.Name)
	if err != nil {
		return nil, "", err
	}

	return targetMemberInfo, placementGroupName, nil
}

// instancesPostSelectClusterMember determines which cluster member to use for placing an instance during creation or migration.
// It first checks whether the instance belongs to a placement group and, if so, applies the placement group’s policy and rigor to filter the available members.
// Among the remaining candidates, the member with the fewest existing instances is selected.
// If the instance does not belong to a placement group, the member with the fewest instances is chosen from all candidates.
func instancesPostSelectClusterMember(ctx context.Context, tx *db.ClusterTx, placementGroupName string, candidateMembers []db.NodeInfo, projectName string) (*db.NodeInfo, error) {
	// Check if instance is using a placement group.
	if placementGroupName == "" {
		return tx.GetNodeWithLeastInstances(ctx, candidateMembers)
	}

	placementGroup, err := dbCluster.GetPlacementGroup(ctx, tx.Tx(), placementGroupName, projectName)
	if err != nil {
		return nil, err
	}

	configs, err := dbCluster.PlacementGroupsConfigStore().GetByEntityIDs(ctx, tx.Tx(), placementGroup.Row.ID)
	if err != nil {
		return nil, err
	}

	apiPlacementGroup := placementGroup.ToAPI(configs)

	filteredCandidates, err := placement.Filter(ctx, tx, candidateMembers, *apiPlacementGroup, false)
	if err != nil {
		return nil, err
	}

	// Early return if only a single candidate.
	if len(filteredCandidates) == 1 {
		return &filteredCandidates[0], nil
	}

	// Use filtered candidates to pick the node with least instances.
	return tx.GetNodeWithLeastInstances(ctx, filteredCandidates)
}
