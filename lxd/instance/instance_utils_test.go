package instance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/canonical/lxd/lxd/idmap"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/sys"
	"github.com/canonical/lxd/shared/osarch"
)

func Test_lxcParseRawLXC(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		key       string
		val       string
		expectErr bool
	}{
		{
			name:      "ValidConfig",
			line:      `lxc.mount.entry="/dev/cgroup cgroup /sys/fs/cgroup cgroup rw,relatime,mode=755 0 0"`,
			key:       "lxc.mount.entry",
			val:       `"/dev/cgroup cgroup /sys/fs/cgroup cgroup rw,relatime,mode=755 0 0"`,
			expectErr: false,
		},
		{
			name:      "Invalid separator",
			line:      `lxc.mount.entry: "/dev/cgroup"`,
			key:       "",
			val:       "",
			expectErr: true,
		},
		{
			name:      "EmptyConfig",
			line:      "",
			key:       "",
			val:       "",
			expectErr: false,
		},
		{
			name:      "WhitespaceOnlyConfig",
			line:      "    ",
			key:       "",
			val:       "",
			expectErr: true,
		},
		{
			name:      "CommentOnlyConfig",
			line:      "# This is a comment",
			key:       "",
			val:       "",
			expectErr: false,
		},
	}

	for _, test := range tests {
		t.Log("Running test case:", test.name)
		key, val, err := lxcParseRawLXC(test.line)
		if test.expectErr {
			assert.Error(t, err, "Expected error for test case: %s", test.name)
		} else {
			assert.NoError(t, err, "Did not expect error for test case: %s", test.name)
		}

		assert.Equal(t, test.key, key, "Unexpected key for test case: %s", test.name)
		assert.Equal(t, test.val, val, "Unexpected value for test case: %s", test.name)
	}
}

func Test_lxcValidConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		expectErr bool
	}{
		{
			name:      "ValidConfig",
			config:    `lxc.mount.entry="/dev/cgroup cgroup /sys/fs/cgroup cgroup rw,relatime,mode=755 0 0"`,
			expectErr: false,
		},
		{
			name:      "Illegal log file",
			config:    `lxc.log.file = "/dev/null"`,
			expectErr: true,
		},
		{
			name:      "Illegal syslog",
			config:    `lxc.log.syslog = "true"`,
			expectErr: true,
		},
		{
			name:      "Illegal ephemeral",
			config:    `lxc.ephemeral = "true"`,
			expectErr: true,
		},
		{
			name:      "Illegal prlimit",
			config:    `lxc.prlimit.nice = "10"`,
			expectErr: true,
		},
		{
			name:      "Allowed nice limit",
			config:    `lxc.kernel.nice = "10"`,
			expectErr: false,
		},
		{
			name:      "Global network config",
			config:    `lxc.net.ipv4.address=192.0.2.2`,
			expectErr: false,
		},
		{
			name:      "Interface specific network config",
			config:    `lxc.net.0.ipv4.address=192.0.2.2`,
			expectErr: false,
		},
		{
			name: "InvalidConfig",
			config: `
# invalid config
lxc.log.file = "/dev/null"`,
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Log("Running test case:", test.name)
		err := lxcValidConfig(test.config)
		if test.expectErr {
			assert.Error(t, err, "Expected error for test case: %s", test.name)
		} else {
			assert.NoError(t, err, "Did not expect error for test case: %s", test.name)
		}
	}

	err := lxcValidConfig(`lxc.idmap="u 0 0 1000"`)
	assert.NoError(t, err, "Expected no error for valid unprivileged mapping")

	t.Setenv("LXD_UNPRIVILEGED_ONLY", "1")
	err = lxcValidConfig(`lxc.idmap="u 0 0 1000"`)
	assert.Error(t, err, "Expected error for unprivileged mapping with LXD_UNPRIVILEGED_ONLY set")
}

func Test_DeviceNextInterfaceHWAddr(t *testing.T) {
	mac1, err := DeviceNextInterfaceHWAddr()
	if err != nil {
		t.Fatalf("Failed getting next interface HW address: %v", err)
	}

	assert.Len(t, mac1, 17, "Expected MAC address to be 17 characters long")
	if !strings.HasPrefix(mac1, "00:16:3e:") {
		t.Fatalf("Expected MAC address to start with '00:16:3e:', got %s", mac1)
	}

	mac2, err := DeviceNextInterfaceHWAddr()
	if err != nil {
		t.Fatalf("Failed getting next interface HW address: %v", err)
	}

	assert.Len(t, mac2, 17, "Expected MAC address to be 17 characters long")
	if !strings.HasPrefix(mac2, "00:16:3e:") {
		t.Fatalf("Expected MAC address to start with '00:16:3e:', got %s", mac2)
	}

	assert.NotEqual(t, mac1, mac2, "Expected different MAC addresses for subsequent calls")
}

func Benchmark_DeviceNextInterfaceHWAddr(b *testing.B) {
	for b.Loop() {
		_, err := DeviceNextInterfaceHWAddr()
		if err != nil {
			b.Fatalf("Failed getting next interface HW address: %v", err)
		}
	}
}

func TestValidConfigHostChecks(t *testing.T) {
	s390x := &sys.OS{Architectures: []int{osarch.ARCH_64BIT_S390_BIG_ENDIAN}, IdmapSet: &idmap.IdmapSet{}}
	x86 := &sys.OS{Architectures: []int{osarch.ARCH_64BIT_INTEL_X86}, IdmapSet: &idmap.IdmapSet{}}
	noIdmap := &sys.OS{Architectures: []int{osarch.ARCH_64BIT_INTEL_X86}}

	tests := []struct {
		name       string
		sysOS      *sys.OS
		config     map[string]string
		expanded   bool
		unprivOnly bool
		expectErr  bool
	}{
		{
			name:      "deny_compat on a host without a compat architecture",
			sysOS:     s390x,
			config:    map[string]string{"security.syscalls.deny_compat": "true"},
			expectErr: true,
		},
		{
			name:   "deny_compat on a host with a compat architecture",
			sysOS:  x86,
			config: map[string]string{"security.syscalls.deny_compat": "true"},
		},
		{
			name:   "deny_compat without a host",
			config: map[string]string{"security.syscalls.deny_compat": "true"},
		},
		{
			name:       "Privileged instance on a host that permits only unprivileged instances",
			sysOS:      x86,
			config:     map[string]string{"security.privileged": "true"},
			unprivOnly: true,
			expectErr:  true,
		},
		{
			name:       "Privileged instance without a host",
			config:     map[string]string{"security.privileged": "true"},
			unprivOnly: true,
		},
		{
			name:      "Unprivileged expanded config on a host without an idmap",
			sysOS:     noIdmap,
			config:    map[string]string{},
			expanded:  true,
			expectErr: true,
		},
		{
			name:     "Unprivileged expanded config without a host",
			config:   map[string]string{},
			expanded: true,
		},
		{
			name:      "Host-independent checks run without a host",
			config:    map[string]string{"raw.seccomp": "x", "security.syscalls.deny_compat": "true"},
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.unprivOnly {
				t.Setenv("LXD_UNPRIVILEGED_ONLY", "true")
			}

			err := ValidConfig(test.sysOS, test.config, test.expanded, instancetype.Container)
			if test.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
