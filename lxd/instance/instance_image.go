package instance

import (
	"slices"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/osarch"
)

// RemoteImage holds what an image registry tells about the image of a request: the type, the architectures, and
// the fingerprint for each architecture.
type RemoteImage struct {
	// Type is the type of the image. It is set only when the request has no type and the registry uses the
	// LXD protocol.
	Type string

	// IDs are the architectures of the image.
	IDs []int

	// Fingerprints maps each architecture to the fingerprint of its image.
	Fingerprints map[int]string

	// Images maps each architecture to the metadata of its image. It is set only for a private image.
	Images map[int]*api.Image
}

// ResolveRemoteImage reads the image of the request from its image registry. It returns an empty value when the
// request has no image registry. It runs before the placement transaction, because it does network I/O.
func ResolveRemoteImage(remote lxd.ImageServer, protocol string, req api.InstancesPost, imageRef string) (RemoteImage, error) {
	if req.Source.Type != api.SourceTypeImage || req.Source.ImageRegistry == "" || remote == nil || imageRef == "" {
		return RemoteImage{}, nil
	}

	// Only images from a LXD protocol registry set the type of the instance. This keeps the default
	// type of a simplestreams image a container.
	inferType := req.Type == "" && protocol == api.ImageRegistryProtocolLXD

	result := RemoteImage{
		Fingerprints: map[int]string{},
	}

	addImage := func(img *api.Image, private bool) error {
		id, err := osarch.ArchitectureId(img.Architecture)
		if err != nil {
			return err
		}

		result.IDs = append(result.IDs, id)
		result.Fingerprints[id] = img.Fingerprint
		if private {
			result.Images = map[int]*api.Image{id: img}
		}

		if inferType {
			result.Type = img.Type
		}

		return nil
	}

	// A private image needs the secret to read its metadata.
	if req.Source.Secret != "" {
		img, _, err := remote.GetPrivateImage(imageRef, req.Source.Secret)
		if err != nil {
			return RemoteImage{}, err
		}

		err = addImage(img, true)
		if err != nil {
			return RemoteImage{}, err
		}

		return result, nil
	}

	// An alias can have an image for each architecture.
	entries, err := remote.GetImageAliasArchitectures(string(req.Type), imageRef)
	if err == nil {
		for arch, entry := range entries {
			id, err := osarch.ArchitectureId(arch)
			if err != nil {
				return RemoteImage{}, err
			}

			result.IDs = append(result.IDs, id)
			result.Fingerprints[id] = entry.Target
			if inferType {
				result.Type = entry.Type
			}
		}

		slices.Sort(result.IDs)

		return result, nil
	}

	// The reference is not an alias. Read the image by its fingerprint.
	img, _, err := remote.GetImage(imageRef)
	if err != nil {
		return RemoteImage{}, err
	}

	err = addImage(img, false)
	if err != nil {
		return RemoteImage{}, err
	}

	return result, nil
}
