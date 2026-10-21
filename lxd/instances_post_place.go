package main

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/canonical/lxd/lxd/db"
	dbCluster "github.com/canonical/lxd/lxd/db/cluster"
	deviceConfig "github.com/canonical/lxd/lxd/device/config"
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
func instancesPostPlace(ctx context.Context, s *state.State, tx *db.ClusterTx, targetProject *api.Project, req api.InstancesPost, profiles []api.Profile, sourceInst *dbCluster.Instance, sourceImageRef string, remoteImage instance.RemoteImage, allMembers []db.NodeInfo, targetGroupName string) (*db.NodeInfo, string, error) {
	// Determine which cluster members are suitable for this instance based on its image architecture.
	architectures, err := instance.SuitableArchitectures(ctx, tx, targetProject.Name, sourceInst, sourceImageRef, req, remoteImage)
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

// selectImageFingerprint returns the fingerprint and the architecture of the registry image in remote that a member
// with memberArch runs. It prefers the native architecture of the member, then its personalities from the newest
// to the oldest. It returns an empty fingerprint when remote has no image.
func selectImageFingerprint(remote instance.RemoteImage, memberArch int) (string, int, error) {
	if len(remote.Fingerprints) == 0 {
		return "", 0, nil
	}

	archs := []int{memberArch}
	personalities, err := osarch.ArchitecturePersonalities(memberArch)
	if err == nil {
		for i := len(personalities) - 1; i >= 0; i-- {
			archs = append(archs, personalities[i])
		}
	}

	for _, arch := range archs {
		fingerprint, ok := remote.Fingerprints[arch]
		if ok {
			return fingerprint, arch, nil
		}
	}

	archName, _ := osarch.ArchitectureName(memberArch)
	return "", 0, api.StatusErrorf(http.StatusBadRequest, "No image for the architecture %q of the selected cluster member", archName)
}

// selectRegistryImage returns the fingerprint to pin and the architecture of the registry image that a member with
// memberArch runs. With autoUpdate, a cached copy of imageRef for the member has precedence, as in ImageDownload. The
// fingerprint is then empty, so that the target resolves imageRef to the cached copy.
func selectRegistryImage(ctx context.Context, tx *db.ClusterTx, source api.InstanceSource, imageRef string, imageType instancetype.Type, remote instance.RemoteImage, memberArch int, autoUpdate bool) (string, int, error) {
	// A private image and a full fingerprint have no cached copy to prefer.
	if autoUpdate && source.Secret == "" && !slices.Contains(slices.Collect(maps.Values(remote.Fingerprints)), imageRef) {
		archs := []int{memberArch}
		personalities, err := osarch.ArchitecturePersonalities(memberArch)
		if err == nil {
			archs = append(archs, personalities...)
		}

		cachedFingerprint, arch, err := tx.FindCachedImageSourceFingerprint(ctx, source.ImageRegistry, imageRef, imageType.String(), archs)
		if err != nil {
			return "", 0, err
		}

		if cachedFingerprint != "" {
			return "", arch, nil
		}
	}

	fingerprint, arch, err := selectImageFingerprint(remote, memberArch)
	if err != nil {
		return "", 0, err
	}

	if fingerprint == "" {
		return "", 0, errors.New("Failed resolving the image from its image registry")
	}

	return fingerprint, arch, nil
}

// instancesPostCreateRecord writes the instance row of an image or a none source for member target in the caller's
// transaction, and returns the internal create request that completes the row on that member. A nil target is this
// member. With imageUnauthorized, the row gets no metadata of the source image, because the caller authorizes the
// image after the transaction.
func instancesPostCreateRecord(ctx context.Context, s *state.State, tx *db.ClusterTx, targetProject *api.Project, req *api.InstancesPost, profiles []api.Profile, sourceImage *api.Image, sourceImageRef string, imageUnauthorized bool, remoteImage instance.RemoteImage, target *db.NodeInfo, clusterGroup string) (internalInstanceCreatePost, error) {
	dbType, err := instancetype.New(string(req.Type))
	if err != nil {
		return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusBadRequest, "%w", err)
	}

	node := s.ServerName
	memberArch := s.OS.Architectures[0]
	if target != nil && target.Name != "" && target.Name != s.ServerName {
		// Placement never selects an evacuated member, but the user can target one.
		if target.State == db.ClusterMemberStateEvacuated {
			return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusForbidden, "Cluster member is evacuated")
		}

		node = target.Name
		memberArch = target.Architecture
	}

	args := db.InstanceArgs{
		Project:     targetProject.Name,
		Config:      maps.Clone(req.Config),
		Type:        dbType,
		Description: req.Description,
		Devices:     deviceConfig.ApplyDeviceInitialValues(deviceConfig.NewDevices(req.Devices), profiles),
		Ephemeral:   req.Ephemeral,
		Name:        req.Name,
		Profiles:    profiles,
	}

	if args.Config == nil {
		args.Config = map[string]string{}
	}

	if clusterGroup != "" {
		args.Config["volatile.cluster.group"] = clusterGroup
	}

	createReq := internalInstanceCreatePost{
		Project:  targetProject.Name,
		Name:     req.Name,
		Source:   req.Source,
		ImageRef: sourceImageRef,
		Start:    req.Start,
	}

	// The architecture of the instance: the image that the member runs, or the member itself.
	args.Architecture = memberArch
	if req.Source.Type == api.SourceTypeNone && req.Architecture != "" {
		args.Architecture, err = osarch.ArchitectureId(req.Architecture)
		if err != nil {
			return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusBadRequest, "%w", err)
		}
	}

	if req.Source.Type == api.SourceTypeImage {
		var img *api.Image
		if req.Source.ImageRegistry != "" {
			// Pin the registry image that placement selected the member for, unless the member uses a cached copy.
			autoUpdate := s.GlobalConfig.ImagesAutoUpdateIntervalHours() > 0
			fingerprint, arch, err := selectRegistryImage(ctx, tx, req.Source, sourceImageRef, dbType, remoteImage, memberArch, autoUpdate)
			if err != nil {
				return internalInstanceCreatePost{}, err
			}

			args.Architecture = arch
			createReq.Fingerprint = fingerprint
			img = remoteImage.Images[arch]
		} else {
			if sourceImage == nil {
				return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusBadRequest, "Image not provided for instance creation")
			}

			img = sourceImage
			createReq.Fingerprint = sourceImage.Fingerprint
		}

		// Write the image.* keys and the base image when the metadata of the image is known here and the caller
		// can see it. Otherwise, the target writes them after the download or the authorization.
		createReq.Image = img
		if img != nil && !imageUnauthorized {
			args.Architecture, err = osarch.ArchitectureId(img.Architecture)
			if err != nil {
				return internalInstanceCreatePost{}, err
			}

			imgType, err := instancetype.New(img.Type)
			if err != nil {
				return internalInstanceCreatePost{}, err
			}

			if imgType != dbType {
				return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusBadRequest, "Requested image's type %q does not match instance type %q", imgType, dbType)
			}

			for k, v := range img.Properties {
				args.Config["image."+k] = v
			}

			args.BaseImage = img.Fingerprint
		}
	}

	args, err = instance.PrepareCreate(s, args)
	if err != nil {
		return internalInstanceCreatePost{}, api.StatusErrorf(http.StatusBadRequest, "%w", err)
	}

	// The instance row does not store the initial.* device keys, so the target gets them from the request.
	createReq.InitialDevices = args.Devices.Clone().CutInitialConfig()

	stored, err := instance.CreateRecord(ctx, tx, args, node)
	if err != nil {
		return internalInstanceCreatePost{}, err
	}

	createReq.ID = stored.ID

	return createReq, nil
}
