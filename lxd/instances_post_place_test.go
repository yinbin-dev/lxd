package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/db"
	dbCluster "github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/osarch"
)

func TestSelectImageFingerprint(t *testing.T) {
	archID := func(name string) int {
		id, err := osarch.ArchitectureId(name)
		require.NoError(t, err)
		return id
	}

	x86 := archID("x86_64")
	i686 := archID("i686")
	aarch64 := archID("aarch64")
	armv7l := archID("armv7l")
	armv8l := archID("armv8l")

	// The native architecture of the member comes first.
	remote := instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86", i686: "fp-i686"}}
	fingerprint, arch, err := selectImageFingerprint(remote, x86)
	require.NoError(t, err)
	require.Equal(t, "fp-x86", fingerprint)
	require.Equal(t, x86, arch)

	// Without the native architecture, the newest personality comes first.
	remote = instance.RemoteImage{Fingerprints: map[int]string{armv7l: "fp-armv7l", armv8l: "fp-armv8l"}}
	fingerprint, arch, err = selectImageFingerprint(remote, aarch64)
	require.NoError(t, err)
	require.Equal(t, "fp-armv8l", fingerprint)
	require.Equal(t, armv8l, arch)

	// A source with no registry image selects nothing.
	fingerprint, _, err = selectImageFingerprint(instance.RemoteImage{}, x86)
	require.NoError(t, err)
	require.Empty(t, fingerprint)

	// A member that runs none of the images fails.
	remote = instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86"}}
	_, _, err = selectImageFingerprint(remote, aarch64)
	require.True(t, api.StatusErrorCheck(err, http.StatusBadRequest))
}

func TestSelectRegistryImage(t *testing.T) {
	archID := func(name string) int {
		id, err := osarch.ArchitectureId(name)
		require.NoError(t, err)
		return id
	}

	x86 := archID("x86_64")
	i686 := archID("i686")
	aarch64 := archID("aarch64")

	clusterDB, cleanup := db.NewTestCluster(t)
	defer cleanup()

	// cacheImage writes a cached container image of registry "reg" for alias.
	cacheImage := func(ctx context.Context, tx *db.ClusterTx, fingerprint string, architecture string, alias string) {
		err := tx.CreateImage(ctx, api.ProjectDefaultName, fingerprint, fingerprint+".gz", 16, false, true, architecture, time.Now(), time.Now(), map[string]string{}, "container", nil)
		require.NoError(t, err)

		project := api.ProjectDefaultName
		id, _, err := tx.GetImage(ctx, fingerprint, dbCluster.ImageFilter{Project: &project})
		require.NoError(t, err)

		err = tx.CreateImageSource(ctx, id, "reg", alias)
		require.NoError(t, err)
	}

	tests := []struct {
		name            string
		source          api.InstanceSource
		imageRef        string
		imageType       instancetype.Type
		remote          instance.RemoteImage
		memberArch      int
		autoUpdate      bool
		wantFingerprint string
		wantArch        int
		wantErr         bool
		wantStatus      int
	}{
		{
			name:       "A cached copy of the alias has precedence over a registry image of a different architecture",
			imageRef:   "ubuntu",
			imageType:  instancetype.Container,
			remote:     instance.RemoteImage{Fingerprints: map[int]string{aarch64: "fp-aarch64"}},
			memberArch: x86,
			autoUpdate: true,
			wantArch:   x86,
		},
		{
			name:       "A cached copy for a personality of the member has precedence",
			imageRef:   "legacy",
			imageType:  instancetype.Container,
			remote:     instance.RemoteImage{Fingerprints: map[int]string{aarch64: "fp-aarch64"}},
			memberArch: x86,
			autoUpdate: true,
			wantArch:   i686,
		},
		{
			name:            "Without auto-update, the registry image is pinned",
			imageRef:        "ubuntu",
			imageType:       instancetype.Container,
			remote:          instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86"}},
			memberArch:      x86,
			wantFingerprint: "fp-x86",
			wantArch:        x86,
		},
		{
			name:       "Without auto-update, a member that runs no registry image fails",
			imageRef:   "ubuntu",
			imageType:  instancetype.Container,
			remote:     instance.RemoteImage{Fingerprints: map[int]string{aarch64: "fp-aarch64"}},
			memberArch: x86,
			wantErr:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:            "A private image ignores the cached copies",
			source:          api.InstanceSource{Secret: "secret"},
			imageRef:        "ubuntu",
			imageType:       instancetype.Container,
			remote:          instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86"}},
			memberArch:      x86,
			autoUpdate:      true,
			wantFingerprint: "fp-x86",
			wantArch:        x86,
		},
		{
			name:            "A full fingerprint ignores the cached copies",
			imageRef:        "fp-x86",
			imageType:       instancetype.Container,
			remote:          instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86"}},
			memberArch:      x86,
			autoUpdate:      true,
			wantFingerprint: "fp-x86",
			wantArch:        x86,
		},
		{
			name:            "A cached copy of a different type is ignored",
			imageRef:        "ubuntu",
			imageType:       instancetype.VM,
			remote:          instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-vm"}},
			memberArch:      x86,
			autoUpdate:      true,
			wantFingerprint: "fp-vm",
			wantArch:        x86,
		},
		{
			name:            "Without a cached copy, the registry image is pinned",
			imageRef:        "debian",
			imageType:       instancetype.Container,
			remote:          instance.RemoteImage{Fingerprints: map[int]string{x86: "fp-x86"}},
			memberArch:      x86,
			autoUpdate:      true,
			wantFingerprint: "fp-x86",
			wantArch:        x86,
		},
		{
			name:       "Without a cached copy or a registry image, the selection fails",
			imageRef:   "debian",
			imageType:  instancetype.Container,
			remote:     instance.RemoteImage{},
			memberArch: x86,
			autoUpdate: true,
			wantErr:    true,
		},
	}

	err := clusterDB.Transaction(context.Background(), func(ctx context.Context, tx *db.ClusterTx) error {
		_, err := dbCluster.CreateImageRegistry(ctx, tx.Tx(), dbCluster.ImageRegistriesRow{
			Name:     "reg",
			Protocol: dbCluster.ImageRegistryProtocol(api.ImageRegistryProtocolSimpleStreams),
		})
		require.NoError(t, err)

		cacheImage(ctx, tx, "aaa", "x86_64", "ubuntu")
		cacheImage(ctx, tx, "bbb", "i686", "legacy")
		cacheImage(ctx, tx, "ccc", "x86_64", "fp-x86")

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				tt.source.ImageRegistry = "reg"
				fingerprint, arch, err := selectRegistryImage(ctx, tx, tt.source, tt.imageRef, tt.imageType, tt.remote, tt.memberArch, tt.autoUpdate)
				if tt.wantErr {
					require.Error(t, err)
					if tt.wantStatus != 0 {
						require.True(t, api.StatusErrorCheck(err, tt.wantStatus))
					}

					return
				}

				require.NoError(t, err)
				require.Equal(t, tt.wantFingerprint, fingerprint)
				require.Equal(t, tt.wantArch, arch)
			})
		}

		return nil
	})
	require.NoError(t, err)
}
