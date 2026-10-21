package instance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/osarch"
)

// fakeImageServer answers the image reads of ResolveRemoteImage from fixed data.
type fakeImageServer struct {
	lxd.ImageServer

	aliases map[string]map[string]*api.ImageAliasesEntry
	images  map[string]*api.Image
	private map[string]*api.Image
}

func (f *fakeImageServer) GetImageAliasArchitectures(imageType string, name string) (map[string]*api.ImageAliasesEntry, error) {
	entries, ok := f.aliases[name]
	if !ok {
		return nil, errors.New("Alias not found")
	}

	return entries, nil
}

func (f *fakeImageServer) GetImage(fingerprint string) (*api.Image, string, error) {
	img, ok := f.images[fingerprint]
	if !ok {
		return nil, "", errors.New("Image not found")
	}

	return img, "", nil
}

func (f *fakeImageServer) GetPrivateImage(fingerprint string, secret string) (*api.Image, string, error) {
	img, ok := f.private[fingerprint]
	if !ok || secret != "s3cret" {
		return nil, "", errors.New("Image not found")
	}

	return img, "", nil
}

func TestResolveRemoteImage(t *testing.T) {
	x86, err := osarch.ArchitectureId("x86_64")
	require.NoError(t, err)

	i686, err := osarch.ArchitectureId("i686")
	require.NoError(t, err)

	vmImage := &api.Image{Fingerprint: "vm1", Architecture: "x86_64", Type: "virtual-machine"}
	privateImage := &api.Image{Fingerprint: "priv1", Architecture: "i686", Type: "container"}
	server := &fakeImageServer{
		aliases: map[string]map[string]*api.ImageAliasesEntry{
			"ubuntu": {
				"x86_64": {Target: "fp-x86", Type: "container"},
				"i686":   {Target: "fp-i686", Type: "container"},
			},
		},
		images:  map[string]*api.Image{"vm1": vmImage},
		private: map[string]*api.Image{"priv1": privateImage},
	}

	request := func(imageType api.InstanceType, secret string) api.InstancesPost {
		return api.InstancesPost{
			Type: imageType,
			Source: api.InstanceSource{
				Type:          api.SourceTypeImage,
				ImageRegistry: "reg",
				Secret:        secret,
			},
		}
	}

	// An alias gives one fingerprint for each architecture.
	remote, err := ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, request("", ""), "ubuntu")
	require.NoError(t, err)
	require.Equal(t, []int{i686, x86}, remote.IDs)
	require.Equal(t, map[int]string{x86: "fp-x86", i686: "fp-i686"}, remote.Fingerprints)
	require.Equal(t, "container", remote.Type)
	require.Nil(t, remote.Images)

	// A fingerprint gives its own architecture, and its type for a LXD registry.
	remote, err = ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, request("", ""), "vm1")
	require.NoError(t, err)
	require.Equal(t, []int{x86}, remote.IDs)
	require.Equal(t, map[int]string{x86: "vm1"}, remote.Fingerprints)
	require.Equal(t, "virtual-machine", remote.Type)

	// A simplestreams registry, or a request with a type, does not set the type.
	remote, err = ResolveRemoteImage(server, api.ImageRegistryProtocolSimpleStreams, request("", ""), "vm1")
	require.NoError(t, err)
	require.Empty(t, remote.Type)

	remote, err = ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, request(api.InstanceTypeVM, ""), "vm1")
	require.NoError(t, err)
	require.Empty(t, remote.Type)

	// A private image gives its metadata.
	remote, err = ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, request("", "s3cret"), "priv1")
	require.NoError(t, err)
	require.Equal(t, []int{i686}, remote.IDs)
	require.Equal(t, map[int]*api.Image{i686: privateImage}, remote.Images)

	// An unknown reference fails.
	_, err = ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, request("", ""), "missing")
	require.Error(t, err)

	// A request without an image registry reads nothing.
	req := request("", "")
	req.Source.ImageRegistry = ""
	remote, err = ResolveRemoteImage(server, api.ImageRegistryProtocolLXD, req, "ubuntu")
	require.NoError(t, err)
	require.Empty(t, remote.IDs)
}
