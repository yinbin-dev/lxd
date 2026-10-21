package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/instance"
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
