# LXD Project-Level Quota Management

Source: `lxd/project/limits/permissions.go`

Projects are the primary multi-tenancy boundary in LXD. Two orthogonal systems
control what a project can do: **limits** (hard numeric ceilings) and
**restrictions** (policy gates that block or narrow specific capabilities).

---

## Count Limits

These cap the number of objects in the project. Checked at creation time only.
Reducing a limit below the current count is rejected immediately.

| Config key | Value | What it caps |
|---|---|---|
| `limits.instances` | integer | Total instances (containers + VMs combined) |
| `limits.containers` | integer | Containers only |
| `limits.virtual-machines` | integer | VMs only |
| `limits.networks` | integer | Networks (only meaningful when `features.networks=true`) |

`limits.instances` and `limits.containers`/`limits.virtual-machines` are
independent; both are checked when creating an instance.

---

## Aggregate Resource Limits

These sum a resource dimension across **all instances and custom volumes in the
project** and compare the total against the configured ceiling. They are checked
at instance create, instance update, volume create, volume update, and profile
update — any mutation that could push the aggregate over the limit is rejected.

| Config key | Value | What is summed |
|---|---|---|
| `limits.cpu` | integer (vCPU count) | Sum of `limits.cpu` across all instances |
| `limits.memory` | byte size (e.g. `16GiB`) | Sum of `limits.memory` across all instances |
| `limits.processes` | integer | Sum of `limits.processes` across all instances (containers only) |
| `limits.disk` | byte size | Sum of root disk `size` + `size.state` across all instances, plus `size` of all custom volumes |
| `limits.disk.pool.<poolname>` | byte size | Same as `limits.disk` but scoped to one named storage pool |

**Constraints imposed by aggregate limits:**

- `limits.cpu`: if set on the project, instances cannot use CPU pinning syntax
  (`limits.cpu=0-3` or `0,2,4`). Only plain integer vCPU counts are allowed.
- `limits.memory`: percentage values (`50%`) are forbidden when a project
  aggregate memory limit is active.
- `limits.disk` for VMs: accounts for both the root disk `size` **and**
  `size.state` (the VM state snapshot volume). If `size.state` is not set
  explicitly, LXD uses the storage pool driver's default VM block filesystem
  size.
- Profile changes propagate: updating a profile that is used by instances in
  the project triggers the same aggregate check as if each affected instance
  were being updated.

---

## Network IP Limits

These cap how many uplink IP addresses the project may consume from a given
upstream network. Applied when the project has `features.networks=true`.

| Config key | Value | What it caps |
|---|---|---|
| `limits.networks.uplink_ips.ipv4.<uplink>` | integer | Max IPv4 addresses consumed from the named uplink |
| `limits.networks.uplink_ips.ipv6.<uplink>` | integer | Max IPv6 addresses consumed from the named uplink |

---

## Restriction System

Restrictions require the master switch `restricted=true` on the project. When
`restricted` is false (the default), all restriction keys below are ignored
regardless of their values. The defaults shown are what apply when
`restricted=true` but the specific key is not set.

### Master switch

| Config key | Values | Default (when restricted=true) |
|---|---|---|
| `restricted` | `true` / `false` | `false` |

### Cluster targeting

| Config key | Values | Default |
|---|---|---|
| `restricted.cluster.target` | `block` / `allow` | `block` |
| `restricted.cluster.groups` | comma-separated group names | `` (all groups allowed) |

`restricted.cluster.target=block` prevents users from specifying `?target=` to
pin a new instance to a particular cluster member. Server administrators can
override this. `restricted.cluster.groups` limits which cluster groups the
project may schedule onto.

### Container capabilities

| Config key | Values | Default |
|---|---|---|
| `restricted.containers.nesting` | `block` / `allow` | `block` |
| `restricted.containers.interception` | `block` / `allow` | `block` |
| `restricted.containers.lowlevel` | `block` / `allow` | `block` |
| `restricted.containers.privilege` | `unprivileged` / `allow` | `unprivileged` |

`restricted.containers.lowlevel=block` forbids a defined set of raw/low-level
config keys: `raw.lxc`, `raw.seccomp`, `raw.apparmor`, `raw.idmap`,
`linux.kernel_modules`, `security.idmap.*`, `boot.host_shutdown_timeout`, and
all `security.syscalls.intercept.*` keys not in the allowlist.
`restricted.containers.interception` separately gates the allowlisted syscall
interception keys (`bpf`, `mknod`, `mount`, `setxattr`, `sysinfo`).

### VM capabilities

| Config key | Values | Default |
|---|---|---|
| `restricted.virtual-machines.lowlevel` | `block` / `allow` | `block` |

Blocks: `raw.qemu`, `raw.qemu.conf`, `raw.apparmor`, `raw.idmap`,
`limits.memory.hugepages`, `boot.host_shutdown_timeout`.

### Device access

| Config key | Values | Default |
|---|---|---|
| `restricted.devices.unix-char` | `block` / `allow` | `block` |
| `restricted.devices.unix-block` | `block` / `allow` | `block` |
| `restricted.devices.unix-hotplug` | `block` / `allow` | `block` |
| `restricted.devices.infiniband` | `block` / `allow` | `block` |
| `restricted.devices.gpu` | `block` / `allow` | `block` |
| `restricted.devices.usb` | `block` / `allow` | `block` |
| `restricted.devices.pci` | `block` / `allow` | `block` |
| `restricted.devices.proxy` | `block` / `allow` | `block` |
| `restricted.devices.nic` | `managed` / `allow` / `block` | `managed` |
| `restricted.devices.disk` | `managed` / `allow` / `block` | `managed` |
| `restricted.devices.disk.paths` | comma-separated path prefixes | `` (no paths allowed) |

`managed` for NIC and disk means only LXD-managed networks/pools are allowed
(no arbitrary host paths or unmanaged networks). `restricted.devices.disk.paths`
only takes effect when `restricted.devices.disk=allow`; it lists host path
prefixes that instances may bind-mount.

### ID mapping

| Config key | Value | Default |
|---|---|---|
| `restricted.idmap.uid` | comma-separated `start-size` ranges | `` (no custom mapping) |
| `restricted.idmap.gid` | comma-separated `start-size` ranges | `` (no custom mapping) |

Restrict which host UID/GID ranges may appear in `raw.idmap`. Only relevant
when `restricted.containers.lowlevel=allow` (otherwise `raw.idmap` is blocked
entirely).

### Network access

| Config key | Value | Default |
|---|---|---|
| `restricted.networks.access` | comma-separated network names | `` (all networks) |
| `restricted.networks.uplinks` | comma-separated uplink names | `` (all uplinks) |

`restricted.networks.access` limits which existing networks instances may
attach to. `restricted.networks.uplinks` limits which upstream networks the
project's own networks (when `features.networks=true`) may use as an uplink.

### Backup and snapshot gates

| Config key | Values | Default |
|---|---|---|
| `restricted.backups` | `block` / `allow` | `block` |
| `restricted.snapshots` | `block` / `allow` | `block` |

---

## When Limits Are Enforced

| Event | Count limits | Aggregate limits | Restrictions |
|---|---|---|---|
| Instance create | Yes | Yes | Yes |
| Instance update (config/devices) | No | Yes | Yes |
| Volume create | No | Yes (`limits.disk`) | No |
| Volume update | No | Yes (`limits.disk`) | No |
| Profile update | No | Yes | Yes |
| Project config update | Yes (validates against current count) | Yes | Yes |

Limits are enforced in a database transaction before the operation proceeds.
There is no post-hoc enforcement; overcommit cannot happen through the limits
system itself (though the scheduler is blind to actual host resource pressure —
see placement docs).
