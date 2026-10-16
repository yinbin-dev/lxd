package instance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/db/cluster"
	deviceConfig "github.com/canonical/lxd/lxd/device/config"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/osarch"
	"github.com/canonical/lxd/shared/revert"
)

// PrepareCreate returns the args of a new instance or snapshot with their defaults set, and validates them without I/O.
func PrepareCreate(s *state.State, args db.InstanceArgs) (db.InstanceArgs, error) {
	if args.Project == "" {
		args.Project = api.ProjectDefaultName
	}

	if args.Config == nil {
		args.Config = map[string]string{}
	}

	if args.BaseImage != "" {
		args.Config["volatile.base_image"] = args.BaseImage
	}

	if args.Config["volatile.uuid"] == "" {
		args.Config["volatile.uuid"] = uuid.New().String()
	}

	args.Config["volatile.uuid.generation"] = args.Config["volatile.uuid"]

	if args.Devices == nil {
		args.Devices = deviceConfig.Devices{}
	}

	err := instancetype.ValidName(args.Name, args.Snapshot)
	if err != nil {
		return db.InstanceArgs{}, err
	}

	if !args.Snapshot {
		// Unset expiry date since instances don't expire.
		args.ExpiryDate = time.Time{}

		// Generate a cloud-init instance-id if not provided.
		//
		// This is generated here rather than in startCommon as only new
		// instances or those which get modified should receive an instance-id.
		// Existing instances will keep using their instance name as instance-id to
		// avoid triggering cloud-init on upgrade.
		if args.Config["volatile.cloud-init.instance-id"] == "" {
			args.Config["volatile.cloud-init.instance-id"] = uuid.New().String()
		}
	}

	// Validate instance config.
	err = ValidConfig(s.OS, args.Config, false, args.Type)
	if err != nil {
		return db.InstanceArgs{}, err
	}

	// Leave validating devices to Create function call below.

	// Validate architecture.
	_, err = osarch.ArchitectureName(args.Architecture)
	if err != nil {
		return db.InstanceArgs{}, err
	}

	if args.CreationDate.IsZero() {
		args.CreationDate = time.Now().UTC()
	}

	if args.LastUsedDate.IsZero() {
		args.LastUsedDate = time.Unix(0, 0).UTC()
	}

	return args, nil
}

// CreateRecord writes the instance or snapshot row of the prepared args for member node in the caller's transaction,
// and returns the stored args. A name conflict returns a 409 error.
func CreateRecord(ctx context.Context, tx *db.ClusterTx, args db.InstanceArgs, node string) (db.InstanceArgs, error) {
	stored, err := createRecord(ctx, tx, args, node)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusConflict) {
			thing := "Instance"
			if shared.IsSnapshot(args.Name) {
				thing = "Snapshot"
			}

			return db.InstanceArgs{}, api.StatusErrorf(http.StatusConflict, "%s %q already exists", thing, args.Name)
		}

		return db.InstanceArgs{}, err
	}

	return stored, nil
}

// createRecord validates the profiles of args, writes the instance or snapshot rows, and reads them back.
func createRecord(ctx context.Context, tx *db.ClusterTx, args db.InstanceArgs, node string) (db.InstanceArgs, error) {
	var err error
	if args.Profiles == nil {
		args.Profiles, err = tx.GetProfiles(ctx, args.Project, []string{"default"})
		if err != nil {
			return db.InstanceArgs{}, errors.New("Failed getting default profile for new instance")
		}
	}

	// Validate profiles.
	profiles, err := tx.GetProfileNames(ctx, args.Project)
	if err != nil {
		return db.InstanceArgs{}, err
	}

	checkedProfiles := map[string]bool{}
	for _, profile := range args.Profiles {
		if !slices.Contains(profiles, profile.Name) {
			return db.InstanceArgs{}, fmt.Errorf("Requested profile %q does not exist", profile.Name)
		}

		if checkedProfiles[profile.Name] {
			return db.InstanceArgs{}, errors.New("Duplicate profile found in request")
		}

		checkedProfiles[profile.Name] = true
	}

	var dbInst cluster.Instance

	// Do not store initial.* device config keys in database.
	initialDevicesConfig := args.Devices.CutInitialConfig()
	defer func() { initialDevicesConfig.Copy(args.Devices) }() // Restore after DB transaction.

	devices, err := cluster.APIToDevices(args.Devices.CloneNative())
	if err != nil {
		return db.InstanceArgs{}, err
	}

	if args.Snapshot {
		parts := strings.SplitN(args.Name, shared.SnapshotDelimiter, 2)
		instanceName := parts[0]
		snapshotName := parts[1]
		instance, err := cluster.GetInstance(ctx, tx.Tx(), args.Project, instanceName)
		if err != nil {
			return db.InstanceArgs{}, fmt.Errorf("Get instance %q in project %q", instanceName, args.Project)
		}

		snapshot := cluster.InstanceSnapshot{
			Project:      args.Project,
			Instance:     instanceName,
			Name:         snapshotName,
			CreationDate: args.CreationDate,
			Stateful:     args.Stateful,
			Description:  args.Description,
			ExpiryDate:   sql.NullTime{Time: args.ExpiryDate, Valid: true},
		}

		id, err := cluster.CreateInstanceSnapshot(ctx, tx.Tx(), snapshot)
		if err != nil {
			return db.InstanceArgs{}, fmt.Errorf("Add snapshot info to the database: %w", err)
		}

		err = cluster.CreateInstanceSnapshotConfig(ctx, tx.Tx(), id, args.Config)
		if err != nil {
			return db.InstanceArgs{}, err
		}

		err = cluster.CreateInstanceSnapshotDevices(ctx, tx.Tx(), id, devices)
		if err != nil {
			return db.InstanceArgs{}, err
		}

		// Read back the snapshot, to get ID and creation time.
		s, err := cluster.GetInstanceSnapshot(ctx, tx.Tx(), args.Project, instanceName, snapshotName)
		if err != nil {
			return db.InstanceArgs{}, fmt.Errorf("Fetch created snapshot from the database: %w", err)
		}

		dbInst = s.ToInstance(instance.Name, instance.Node, instance.Type, instance.Architecture)
	} else {
		// Create the instance entry.
		dbInst = cluster.Instance{
			Project:      args.Project,
			Name:         args.Name,
			Node:         node,
			Type:         args.Type,
			Snapshot:     args.Snapshot,
			Architecture: args.Architecture,
			Ephemeral:    args.Ephemeral,
			CreationDate: args.CreationDate,
			Stateful:     args.Stateful,
			LastUseDate:  sql.NullTime{Time: args.LastUsedDate, Valid: true},
			Description:  args.Description,
			ExpiryDate:   sql.NullTime{Time: args.ExpiryDate, Valid: true},
		}

		instanceID, err := cluster.CreateInstance(ctx, tx.Tx(), dbInst)
		if err != nil {
			return db.InstanceArgs{}, fmt.Errorf("Add instance info to the database: %w", err)
		}

		err = cluster.CreateInstanceDevices(ctx, tx.Tx(), instanceID, devices)
		if err != nil {
			return db.InstanceArgs{}, err
		}

		err = cluster.CreateInstanceConfig(ctx, tx.Tx(), instanceID, args.Config)
		if err != nil {
			return db.InstanceArgs{}, err
		}

		profileNames := make([]string, 0, len(args.Profiles))
		for _, profile := range args.Profiles {
			profileNames = append(profileNames, profile.Name)
		}

		err = cluster.UpdateInstanceProfiles(ctx, tx.Tx(), int(instanceID), dbInst.Project, profileNames)
		if err != nil {
			return db.InstanceArgs{}, err
		}

		// Read back the instance, to get ID and creation time.
		dbRow, err := cluster.GetInstance(ctx, tx.Tx(), args.Project, args.Name)
		if err != nil {
			return db.InstanceArgs{}, fmt.Errorf("Fetch created instance from the database: %w", err)
		}

		dbInst = *dbRow

		if dbInst.ID < 1 {
			return db.InstanceArgs{}, fmt.Errorf("Unexpected instance database ID %d: %w", dbInst.ID, err)
		}
	}

	newArgs, err := tx.InstancesToInstanceArgs(ctx, false, dbInst)
	if err != nil {
		return db.InstanceArgs{}, err
	}

	// Populate profile info that was already loaded.
	newInstArgs := newArgs[dbInst.ID]
	newInstArgs.Profiles = args.Profiles
	args = newInstArgs

	return args, nil
}

// CheckLocalSupport returns an error if this member cannot run an instance with the type and architecture of args.
func CheckLocalSupport(s *state.State, args db.InstanceArgs) error {
	err := s.InstanceTypes[args.Type]
	if err != nil {
		return fmt.Errorf("Instance type %q is not supported on this server: %w", args.Type, err)
	}

	if !slices.Contains(s.OS.Architectures, args.Architecture) {
		return errors.New("Requested architecture is not supported by this host")
	}

	return nil
}

// CreateLocal runs the driver Create for the stored args on this member, which validates the devices against the
// local host. The caller holds the operation lock of the instance.
func CreateLocal(ctx context.Context, s *state.State, args db.InstanceArgs, p api.Project, clearLogDir bool) (Instance, revert.Hook, error) {
	err := CheckLocalSupport(s, args)
	if err != nil {
		return nil, nil, err
	}

	inst, cleanup, err := Create(ctx, s, args, p)
	if err != nil {
		logger.Error("Failed initialising instance", logger.Ctx{"project": args.Project, "instance": args.Name, "type": args.Type, "err": err})
		return nil, nil, fmt.Errorf("Failed initialising instance: %w", err)
	}

	// Wipe any existing log for this instance name.
	if clearLogDir {
		_ = os.RemoveAll(inst.LogPath())
	}

	return inst, cleanup, nil
}
