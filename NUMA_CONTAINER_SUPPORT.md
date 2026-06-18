# NUMA CPU+Memory Alignment for Containers — Implementation Plan

## Current state

LXD containers support `limits.cpu.nodes`, which resolves to a list of physical CPU IDs and
writes them to the container's cgroup `cpuset.cpus`. Memory binding is never set. The kernel's
allocator is free to satisfy memory faults on any NUMA node, regardless of where the container's
threads run.

The gap is entirely at the software level. The kernel cgroup interface (`cpuset.mems`) for
binding memory to NUMA nodes is identical between cgroup v1 and v2, and it is present on every
host that has `cpuset.cpus` support. No new kernel features are needed.

---

## Why VMs already work

For VMs, `addCPUMemoryConfig` (`lxd/instance/drivers/driver_qemu.go:3977`) generates a QEMU
`memory-backend-memfd` object per host NUMA node with `policy=bind, host-nodes.0=<N>`
(`lxd/instance/drivers/driver_qemu_templates.go:501`). This is set at boot time and is
sufficient — QEMU's `SetAffinity` (`driver_qemu.go:7930`) only needs to pin vCPU threads;
memory is already bound.

Containers have no equivalent boot-time mechanism, so memory binding must be done at runtime
via the cgroup `cpuset.mems` file.

---

## Code changes required

### Change 1 — `lxd/cgroup/abstraction.go`

Add `SetCpusetMems` immediately after `SetCpuset` (line 800), following the same pattern:

```go
// SetCpusetMems sets the allowed NUMA memory nodes for the cgroup.
func (cg *CGroup) SetCpusetMems(limit string) error {
    version := cgControllers["cpuset"]
    switch version {
    case Unavailable:
        return ErrControllerMissing
    case V1, V2:
        return cg.rw.Set(version, "cpuset", "cpuset.mems", limit)
    }

    return ErrUnknownVersion
}
```

The `cpuset.mems` file exists for both cgroup v1 and v2 under the same controller and the same
file name. The `rw.Set` call path (`lxcCgroupReadWriter`, `directCgroupReadWriter`) already
handles writing arbitrary cpuset files, so no changes are needed below this layer.

---

### Change 2 — `lxd/instance/instance_interface.go`

Add a new method to the `Container` interface (alongside `SetAffinity` at line 125):

```go
SetMemoryNodes(nodes string) error
```

`nodes` is a comma-separated NUMA node list in the same format as `cpuset.mems` (e.g., `"0"`,
`"0,1"`, `"0-3"`).

This is a separate method rather than changing `SetAffinity`'s signature because QEMU's
`SetAffinity` has a structurally different role (it pins individual vCPU thread PIDs via
`unix.SchedSetaffinity`) and already handles memory binding at VM start time — threading a
memory node parameter into it would be a no-op and misleading.

---

### Change 3 — `lxd/instance/drivers/driver_lxc.go`

Implement `SetMemoryNodes` on the `lxc` driver, parallel to `SetAffinity` (line 7127):

```go
func (d *lxc) SetMemoryNodes(nodes string) error {
    if d.InitPID() <= 0 {
        return nil
    }

    cg, err := d.CGroup()
    if err != nil {
        return fmt.Errorf("Cannot get cgroup struct: %w", err)
    }

    err = cg.SetCpusetMems(nodes)
    if err != nil {
        return fmt.Errorf("Cannot set cgroup cpuset.mems to %q: %w", nodes, err)
    }

    return nil
}
```

---

### Change 4 — `lxd/instance/drivers/driver_qemu.go`

Add a no-op `SetMemoryNodes` to satisfy the interface. QEMU binds memory at boot time and has
no runtime cgroup-based mechanism for memory placement:

```go
func (d *qemu) SetMemoryNodes(_ string) error {
    return nil
}
```

---

### Change 5 — `lxd/devices.go` (`deviceTaskBalance`)

This is where the two operations must be wired together. The function already builds
`numaNodeToCPU` at line 540. The "Set the new pinning" loop (lines 658–663) calls
`inst.SetAffinity(set)` for each instance. After that call, derive the NUMA memory nodes from
the assigned CPU set and call `SetMemoryNodes`.

**5a. Build a reverse map from CPU ID to NUMA node** (add after line 543):

```go
cpuToNUMANode := make(map[int64]int64, len(numaNodeToCPU)*8)
for node, cpus := range numaNodeToCPU {
    for _, cpu := range cpus {
        cpuToNUMANode[cpu] = node
    }
}
```

**5b. Replace the "Set the new pinning" loop** (lines 657–663):

```go
for inst, set := range pinning {
    err = inst.SetAffinity(set)
    if err != nil {
        logger.Error("Error setting CPU affinity for the instance",
            logger.Ctx{"project": inst.Project().Name, "instance": inst.Name(), "err": err})
    }

    // For containers, also bind memory to the same NUMA node(s) as the pinned CPUs.
    if inst.Type() == instancetype.Container {
        numaNodes := make(map[int64]struct{})
        for _, cpuStr := range set {
            cpuID, _ := strconv.ParseInt(cpuStr, 10, 64)
            if node, ok := cpuToNUMANode[cpuID]; ok {
                numaNodes[node] = struct{}{}
            }
        }

        if len(numaNodes) > 0 {
            nodeStrs := make([]string, 0, len(numaNodes))
            for node := range numaNodes {
                nodeStrs = append(nodeStrs, strconv.FormatInt(node, 10))
            }

            sort.Strings(nodeStrs)
            err = inst.SetMemoryNodes(strings.Join(nodeStrs, ","))
            if err != nil {
                logger.Error("Error setting memory node affinity for the instance",
                    logger.Ctx{"project": inst.Project().Name, "instance": inst.Name(), "err": err})
            }
        }
    }
}
```

The `instancetype.Container` guard is important: QEMU instances are handled at boot time, and
calling `SetMemoryNodes` on a VM is a no-op by design, but skipping the reverse-map computation
for VMs avoids unnecessary work.

---

## Where `limits.cpu.nodes` is set but no explicit CPU pinning exists

When `limits.cpu.nodes` is set but `limits.cpu` is a count (not a pinset), `deviceTaskBalance`
resolves the NUMA CPUs via `getNumaCPUs` and feeds them into `fillFixedInstances` (line 589).
These instances end up in `fixedInstances` and eventually in `pinning`, so Change 5 above
covers them automatically — the reverse-map lookup works regardless of whether the CPU set came
from explicit pinning or NUMA-based load balancing.

---

## Caveats and edge cases

**cgroup v2 hierarchy inheritance**

In cgroup v2, a cgroup's effective `cpuset.mems` is the intersection of its own `cpuset.mems`
and its parent's `cpuset.mems.effective`. If the parent cgroup was created without any
`cpuset.mems` constraint (i.e., `cpuset.mems = ""` meaning "all nodes"), writing a node subset
on the child cgroup works correctly — the child simply restricts its own view. No parent cgroup
changes are needed.

**LXC library cgroup writer (`lxcCgroupReadWriter`)**

This writer uses the liblxc C API to read/write cgroup values. It already supports arbitrary
cgroup file names within a controller, so `cpuset.mems` will work without any changes to the
writer layer. Verify this assumption by confirming that `lxcCgroupReadWriter.Set` does not
hard-code a file allowlist.

**Containers started before this change**

Running containers that already have `limits.cpu.nodes` set will receive `cpuset.mems` the next
time `deviceTaskBalance` fires (triggered by CPU topology events or instance start/stop via
`cgroup.TaskSchedulerTrigger` in `onStart` at `driver_lxc.go:2422`). No migration or restart
is required.

**Unprivileged containers**

`cpuset.mems` is a pure cgroup write from the host side — it does not interact with the
container's user namespace or idmapped mounts. Unprivileged containers are unaffected by the
namespace change.

**Containers with no NUMA CPU pinning**

When a container has no `limits.cpu.nodes` and no explicit CPU pinning, `deviceTaskBalance`
places it in `balancedInstances` (line 591), which is balanced across all available CPUs. These
instances end up in `pinning` with CPUs spread across NUMA nodes, making any `cpuset.mems`
binding meaningless or harmful. The `numaNodes` set will contain multiple nodes in this case.
The safest behavior is to skip `SetMemoryNodes` when the resolved set spans all available NUMA
nodes (i.e., no constraint is tighter than the default). An alternative is to always set
`cpuset.mems` to the exact nodes corresponding to the pinned CPUs — this is correct and
conservative, and is what the implementation above does.

---

## Effort estimate

| Change | Scope | Effort |
|---|---|---|
| `SetCpusetMems` in `cgroup/abstraction.go` | ~10 LOC | Trivial |
| `SetMemoryNodes` on Container interface | 1 line | Trivial |
| `lxc` driver implementation | ~15 LOC | Trivial |
| `qemu` driver no-op | ~5 LOC | Trivial |
| `deviceTaskBalance` reverse-map + call | ~25 LOC | Small |
| Testing on a 2-NUMA-node host | — | ~1 day |

Total implementation: **half a day**. The dominant cost is integration testing — verifying
`/sys/fs/cgroup/<container-slice>/cpuset.mems` reflects the correct node after `limits.cpu.nodes`
is set and the instance is running.
