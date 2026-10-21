package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/canonical/lxd/lxd/db"
	dbCluster "github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/db/operationtype"
	deviceConfig "github.com/canonical/lxd/lxd/device/config"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/instance/operationlock"
	"github.com/canonical/lxd/lxd/metrics"
	"github.com/canonical/lxd/lxd/operations"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/lxd/state"
	storagePools "github.com/canonical/lxd/lxd/storage"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
	"github.com/canonical/lxd/shared/osarch"
	"github.com/canonical/lxd/shared/revert"
	"github.com/canonical/lxd/shared/version"
)

// instanceCreateRequestTimeout is the deadline of the internal create request.
const instanceCreateRequestTimeout = 30 * time.Second

// internalInstanceCreatePost is the body of the internal create request that the receiver sends to the target to
// complete the creation of an instance row that the receiver wrote.
type internalInstanceCreatePost struct {
	// ID is the ID of the instance row.
	ID int `json:"id"`

	// Project and Name identify the instance row, so that the target can reject a row that a different
	// request now uses.
	Project string `json:"project"`
	Name    string `json:"name"`

	// Source is the instance source of the client request.
	Source api.InstanceSource `json:"source"`

	// ImageRef is the alias or the fingerprint of the image, as the receiver resolved it from Source.
	ImageRef string `json:"image_ref"`

	// Fingerprint is the full fingerprint of the image that the receiver placed for.
	Fingerprint string `json:"fingerprint"`

	// Image is the metadata of a local image or of a private registry image, when the receiver has it.
	Image *api.Image `json:"image"`

	// InitialDevices holds the initial.* device keys of the request, which the instance row does not store.
	InitialDevices deviceConfig.Devices `json:"initial_devices"`

	// Start tells the target to start the instance after the creation.
	Start bool `json:"start"`
}

var internalInstanceCreateCmd = APIEndpoint{
	Path: "instances/create",

	// The request carries the identity of the client, so the handler checks that a cluster member sent it.
	Post: APIEndpointAction{Handler: internalInstanceCreate, AccessHandler: allowAuthenticated},
}

// internalInstanceCreate completes the creation of an instance row that a receiver wrote for this member. It
// accepts only a cluster notification.
func internalInstanceCreate(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	requestor, err := request.GetRequestor(r.Context())
	if err != nil {
		return response.SmartError(err)
	}

	if !requestor.IsClusterNotification() {
		return response.Forbidden(errors.New("The internal create request accepts only a cluster notification"))
	}

	// Count the request as an instance create of this member, so that the API metrics show the final result of
	// the operation.
	metrics.TrackStartedRequest(r, entity.TypeInstance)

	req := internalInstanceCreatePost{}
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		return response.BadRequest(err)
	}

	op, err := instanceCreateOnTarget(s, r, req)
	if err != nil {
		return response.SmartError(err)
	}

	return response.OperationResponse(op)
}

// instanceCreateOnTarget takes the ownership of the instance row of req with the handoff, and returns the operation
// that completes the creation on this member. When it returns an error, the handoff did not occur.
func instanceCreateOnTarget(s *state.State, r *http.Request, req internalInstanceCreatePost) (*operations.Operation, error) {
	if s.DB.Cluster.LocalNodeIsEvacuated() {
		return nil, api.StatusErrorf(http.StatusForbidden, "Cluster member is evacuated")
	}

	var args db.InstanceArgs
	var p api.Project
	err := s.DB.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
		var err error
		args, p, err = loadInstanceArgsByID(ctx, tx, req.ID)
		return err
	})
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return nil, api.StatusErrorf(http.StatusNotFound, "Instance creation was cancelled")
		}

		return nil, err
	}

	if args.Project != req.Project || args.Name != req.Name {
		return nil, api.StatusErrorf(http.StatusNotFound, "Instance creation was cancelled")
	}

	err = instance.CheckLocalSupport(s, args)
	if err != nil {
		return nil, api.StatusErrorf(http.StatusBadRequest, "%w", err)
	}

	// Prevent concurrent operations on the instance until the creation completes.
	lock, err := operationlock.Create(args.Project, args.Name, operationlock.ActionCreate, false, false)
	if err != nil {
		return nil, api.StatusErrorf(http.StatusConflict, "%w", err)
	}

	run := func(ctx context.Context, op *operations.Operation) error {
		err := instanceCreateTargetSteps(ctx, s, op, req, args, p, lock)
		if err != nil {
			return err
		}

		return instanceCreateFinish(ctx, s, &api.InstancesPost{Name: args.Name, Start: req.Start}, args, nil, op)
	}

	instanceURL := entity.InstanceURL(args.Project, args.Name)
	opArgs := operations.OperationArgs{
		ProjectName: args.Project,
		EntityURL:   api.NewURL().Path(version.APIVersion, "projects", args.Project),
		Type:        operationtype.InstanceCreate,
		Class:       operationtype.OperationClassTask,
		RunHook:     run,
		Metadata: map[string]any{
			api.MetadataEntityURL: instanceURL.String(),
		},
		// The handoff: the operation holds the instance row from the transaction that registers it.
		Resources: map[entity.Type][]api.URL{entity.TypeInstance: {*instanceURL}},
		Precondition: func(ctx context.Context, tx *db.ClusterTx) error {
			return tx.CheckInstanceHandoff(ctx, req.ID)
		},
	}

	op, err := operations.ScheduleUserOperationFromRequest(s, r, opArgs)
	if err != nil {
		lock.Done(err)
		return nil, err
	}

	return op, nil
}

// instanceCreateTargetSteps makes the image available, runs the driver checks, and creates the storage of the
// instance row of req, whose stored args and project the handler loaded. When a step fails, it removes the storage
// and the row. It releases lock when it returns.
func instanceCreateTargetSteps(ctx context.Context, s *state.State, op *operations.Operation, req internalInstanceCreatePost, args db.InstanceArgs, p api.Project, lock *operationlock.InstanceOperation) (err error) {
	reverter := revert.New()
	defer reverter.Fail()

	// Deferred calls run in reverse order, so the lock is released before the revert runs. The revert
	// deletes the instance, and a delete waits for the lock.
	defer func() { lock.Done(err) }()

	// The handler checked that the request identity matches the row.
	reverter.Add(func() {
		_ = s.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
			return tx.DeleteInstance(ctx, req.Project, req.Name)
		})
	})

	img, err := instanceCreateTargetImage(ctx, s, op, req, &args, p)
	if err != nil {
		return err
	}

	// Restore the initial.* device keys for the driver checks and the storage creation, without storing them.
	if len(req.InitialDevices) > 0 {
		if args.Devices == nil {
			args.Devices = deviceConfig.Devices{}
		}

		req.InitialDevices.Copy(args.Devices)
	}

	inst, cleanup, err := instance.CreateLocal(ctx, s, args, p, true)
	if err != nil {
		return fmt.Errorf("Failed creating instance record: %w", err)
	}

	reverter.Add(cleanup)

	pool, err := storagePools.LoadByInstance(s, inst)
	if err != nil {
		return fmt.Errorf("Failed loading instance storage pool: %w", err)
	}

	if img != nil {
		// Lock this operation to ensure that concurrent image operations don't conflict.
		unlockImage, lockErr := imageOperationLock(img.Fingerprint)
		if lockErr != nil {
			return lockErr
		}

		err = pool.CreateInstanceFromImage(ctx, inst, img.Fingerprint, op)
		unlockImage()
		if err != nil {
			return fmt.Errorf("Failed creating instance from image: %w", err)
		}
	} else {
		err = pool.CreateInstance(inst, nil)
		if err != nil {
			return fmt.Errorf("Failed creating instance: %w", err)
		}
	}

	reverter.Add(func() { _ = inst.Delete(context.Background(), true, "", op) })

	err = inst.UpdateBackupFile()
	if err != nil {
		return err
	}

	reverter.Success()
	return nil
}

// instanceCreateTargetImage makes the image of req available on this member, writes the image.* keys and
// volatile.base_image of that image over the values of the instance row and of args, sets the architecture of the
// image on the instance row and on args, and records the use of the image. It returns nil for a source that is not
// an image.
func instanceCreateTargetImage(ctx context.Context, s *state.State, op *operations.Operation, req internalInstanceCreatePost, args *db.InstanceArgs, p api.Project) (*api.Image, error) {
	if req.Source.Type != api.SourceTypeImage {
		return nil, nil
	}

	var img *api.Image
	var err error
	if req.Source.ImageRegistry != "" {
		img, err = ensureDownloadedImageFitWithinBudget(ctx, s, op, p, req.ImageRef, req.Source, args.Type.String(), req.Fingerprint, req.Image)
		if err != nil {
			return nil, err
		}
	} else {
		if req.Image == nil {
			return nil, errors.New("Image not provided for instance creation")
		}

		img = req.Image
		err = ensureImageIsLocallyAvailable(ctx, s, img, args.Project)
		if err != nil {
			return nil, err
		}
	}

	// Validate the type of the image matches the type of the instance.
	imgType, err := instancetype.New(img.Type)
	if err != nil {
		return nil, err
	}

	if imgType != args.Type {
		return nil, fmt.Errorf("Requested image's type %q does not match instance type %q", imgType, args.Type)
	}

	imgArch, err := osarch.ArchitectureId(img.Architecture)
	if err != nil {
		return nil, err
	}

	// The image that this member uses replaces the image metadata of the request.
	values := map[string]string{"volatile.base_image": img.Fingerprint}
	for k, v := range img.Properties {
		values["image."+k] = v
	}

	err = s.DB.Cluster.Transaction(ctx, func(ctx context.Context, tx *db.ClusterTx) error {
		// The row can have a different architecture from the image, for example when the receiver did not write
		// the metadata of the image.
		if imgArch != args.Architecture {
			_, err := tx.Tx().ExecContext(ctx, "UPDATE instances SET architecture = ? WHERE id = ?", imgArch, args.ID)
			if err != nil {
				return fmt.Errorf("Failed updating the architecture of the instance: %w", err)
			}
		}

		err = tx.UpdateInstanceConfig(args.ID, values)
		if err != nil {
			return fmt.Errorf("Failed writing the image config of the instance: %w", err)
		}

		err = tx.UpdateImageLastUseDate(ctx, args.Project, img.Fingerprint, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("Error updating image last use date: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Apply the same changes to args, so that args stay equal to the instance row.
	args.Architecture = imgArch
	if args.Config == nil {
		args.Config = map[string]string{}
	}

	for k, v := range values {
		if v == "" {
			delete(args.Config, k)
			continue
		}

		args.Config[k] = v
	}

	return img, nil
}

// loadInstanceArgsByID returns the stored args, with the profiles, and the project of the instance row with the
// given ID.
func loadInstanceArgsByID(ctx context.Context, tx *db.ClusterTx, id int) (db.InstanceArgs, api.Project, error) {
	projectName, name, err := tx.GetInstanceProjectAndName(ctx, id)
	if err != nil {
		return db.InstanceArgs{}, api.Project{}, err
	}

	dbInst, err := dbCluster.GetInstance(ctx, tx.Tx(), projectName, name)
	if err != nil {
		return db.InstanceArgs{}, api.Project{}, err
	}

	instArgs, err := tx.InstancesToInstanceArgs(ctx, true, *dbInst)
	if err != nil {
		return db.InstanceArgs{}, api.Project{}, err
	}

	dbProject, err := dbCluster.GetProject(ctx, tx.Tx(), projectName)
	if err != nil {
		return db.InstanceArgs{}, api.Project{}, err
	}

	p, err := dbProject.ToAPI(ctx, tx.Tx())
	if err != nil {
		return db.InstanceArgs{}, api.Project{}, err
	}

	return instArgs[dbInst.ID], *p, nil
}
