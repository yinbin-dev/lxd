## Floating IP (Network Address Forwards)

LXD does not use the term "floating IP" but has an equivalent feature called **Network Address
Forwards** (`/1.0/networks/{networkName}/forwards`). A forward binds a stable external listen
address to a private instance IP, and that mapping can be changed at any time without touching
the instance.

### How it works

A `NetworkForward` has a `listen_address` (the "floating" external IP), an optional
`target_address` (the default instance IP for whole-IP forwarding), and an optional list of
port-level rules. When `target_address` is set without port rules, all traffic arriving at the
listen address is NATted to the target instance — a direct floating IP equivalent.

**OVN networks** (`driver_ovn.go:5050`): the forward is implemented as an OVN LoadBalancer VIP
in the Northbound DB (`client.LoadBalancerApply`), applied to both the logical router and the
internal switch. OVN's stateful connection tracking handles the NAT entirely in the kernel.
After creation, `forwardBGPSetupPrefixes` advertises the listen address as a BGP prefix to
upstream routers.

- Auto-allocation: passing `0.0.0.0` or `::` as the listen address causes LXD to allocate an
  unused IP from the uplink network's OVN IP range (`allocateUplinkAddress`), equivalent to
  "allocate me a floating IP."
- Forwards are cluster-wide on OVN (not per-member) — OVN handles routing automatically.

**Bridge networks** (`driver_bridge.go:3153`): the forward is implemented as DNAT firewall rules
(nftables/iptables) via the firewall driver. Forwards are per-member on bridge networks.
Auto-allocation of listen address is not supported for bridge networks.

### Reassignment

To move the floating IP to a different instance, issue a `PUT` or `PATCH` to
`/1.0/networks/{name}/forwards/{listen_address}` updating `config.target_address` (or the port
rules). LXD updates the OVN LoadBalancer VIP or firewall rules immediately. There is no
downtime on the listen address itself.

### Cross-host forwarding

OVN forwards traffic to any instance on any cluster member — not just instances on the same
host. The load balancer is programmed into the OVN Northbound DB and attached to both the
logical router (for external ingress) and the internal logical switch (for east-west traffic).
OVN's `ovn-controller` on every chassis compiles those entries into OpenFlow rules. When a
packet arrives at the listen address on any chassis, that chassis performs the DNAT and forwards
the inner packet to whichever physical host the target instance is bound to via the Southbound
DB port binding.

LXD passes `target_address` as a plain IP to the OVN VIP — there is no host co-location check.
If the target instance later migrates to a different cluster member, OVN follows automatically
via the port binding update; the forward does not need to be reconfigured.

Bridge network forwards are per-member firewall DNAT rules and do not have this property — they
only handle traffic arriving on that specific host.

### What is not supported

- **Automatic failover**: LXD has no health checking of target instances. If the target
  instance goes down, traffic is dropped until `target_address` is manually updated.
- **Per-instance association workflow**: forwards are configured on the network, not on the
  instance. There is no "attach floating IP to instance" operation analogous to OpenStack's
  `nova floating-ip-associate`.

### Summary

| Capability | Bridge network | OVN network |
|---|---|---|
| Whole-IP forwarding (floating IP) | Yes (DNAT firewall rules) | Yes (OVN load balancer VIP) |
| Port-level forwarding | Yes | Yes |
| Auto-allocate listen address | No | Yes (`0.0.0.0` / `::`) |
| BGP advertisement of listen address | No | Yes |
| Cluster-wide (no per-member config) | No (per-member) | Yes |
| Automatic failover on instance failure | No | No |

---

## Load Balancing

LXD has two distinct load balancing features that use the term differently:

### 1. Network load balancers (traffic distribution to instances)
LXD has a first-class NetworkLoadBalancer resource that distributes inbound connections across a pool of backend instances. It's available on OVN networks (full stateful LB) and bridge networks (stateless NAT).

How it works:

Configured via POST /1.0/networks/{name}/load-balancers with a listen address, one or more backend pools (each with a list of instance IPs + ports), and health-check config.
OVN implementation (lxd/network/driver_ovn.go): creates an OVN LoadBalancer VIP object in the OVN Northbound DB. OVN's southbound dataplane handles connection tracking and distribution entirely in kernel (no userspace proxy). Supports projects, peering, and integrates with OVN ACL port groups.
Bridge implementation (lxd/network/driver_bridge.go): uses stateless NAT rules via the firewall driver (nftables or xtables DNAT), so it's simpler but has no connection tracking.
The firewall abstraction layer (lxd/firewall/) implements NetworkApplyForwards for bridge-based forwarding, while OVN handles its own via ovn-nbctl.
REST surface is in lxd/network_load_balancers.go; the interface methods LoadBalancerCreate/Update/Delete and LoadBalancerPoolCreate/Update/Delete are defined on the Network interface (lxd/network/network_interface.go).

### 2. BGP route advertisement (multi-host external load balancing)
For clusters exposed to an external BGP router, lxd/bgp/ (wrapping GoBGP) advertises OVN load-balancer VIPs and uplink network routes to external BGP speakers. Multiple cluster members can advertise the same VIP, letting the upstream router do ECMP-based load balancing across members — this is how LXD achieves multi-host load balancing/failover for externally reachable services.

### 3. Instance placement ("load balancing" at scheduling time)
When creating an instance in a cluster, GetNodeWithLeastInstances picks the member with the fewest instances. This is a one-shot placement decision — not ongoing rebalancing (as covered in the previous question).

In practice, the OVN network load balancer is the primary feature when people say "LXD load balancing": it gives you a stable VIP that fans out to a group of instance backends, with OVN handling the actual packet distribution in the kernel.

---

## NUMA-awareness

LXD supports NUMA-aware CPU and memory placement, but the depth of support differs between VMs and containers.

### VMs — full NUMA-aware co-location

For VMs, LXD achieves genuine CPU+memory NUMA alignment. When `limits.cpu.nodes` is set alongside CPU pinning (`limits.cpu` as a specific cpuset, or `limits.cpu.pin_strategy=auto`), `addCPUMemoryConfig` in `lxd/instance/drivers/driver_qemu.go` generates QEMU config that does three things together:

**1. Memory binding to the host NUMA node**

For each host NUMA node in use, a QEMU `memory-backend-memfd` object is created with `policy=bind` and `host-nodes.0=<N>` (`lxd/instance/drivers/driver_qemu_templates.go`). This instructs the kernel's memory allocator to allocate VM RAM from that specific host NUMA node — not just "prefer" it.

**2. Guest NUMA topology exposure**

A guest `[numa]` section is emitted for each host node, mapping guest vCPUs to guest NUMA nodes that mirror the host layout.

**3. vCPU thread pinning to the matching physical CPUs**

`cpuTopology` walks the host CPU topology, groups the pinned threads by their physical NUMA node, and maps each vCPU to its host socket/core/thread — ensuring the vCPU threads run on cores that are local to the memory they access.

The end result: guest memory, guest vCPUs, and the underlying host NUMA node are all co-located. Hugepages (`limits.memory.hugepages=true`) are handled the same way, using `memory-backend-file` with `prealloc=on` instead of `memory-backend-memfd`.

### Containers — CPU-only NUMA pinning

For containers, `limits.cpu.nodes` causes `getNumaCPUs()` to resolve the node to its physical CPU IDs, which are then written to the container's cgroup `cpuset.cpus`. However, `cpuset.mems` is never set — there is no memory binding to the corresponding NUMA node. Memory can still be allocated on any NUMA node by the kernel.

### Summary

| | CPU pinned to NUMA node | Memory bound to NUMA node |
|---|---|---|
| VM (`limits.cpu.nodes`) | Yes | Yes (`policy=bind` in QEMU) |
| Container (`limits.cpu.nodes`) | Yes (cgroup `cpuset.cpus`) | No |

---

## OVN SmartNIC / DPU Support

### Off-path SmartNIC DPU — not supported

LXD does not support the off-path DPU model, where `ovn-controller` and `ovs-vswitchd` run on the DPU's ARM cores rather than the host CPU. The OVN chassis role (`ClusterRoleOVNChassis` in `lxd/db/node.go`) is always assigned to regular LXD cluster members and managed via `networkUpdateOVNChassis` on heartbeats. There is no mechanism to delegate that role to an external DPU device.

### SR-IOV and VDPA hardware acceleration — supported

LXD does support inline/on-path SmartNIC offload via SR-IOV (`switchdev` mode) and VDPA, configured per OVN NIC device (`lxd/device/nic_ovn.go`). In this model, `ovn-controller` still runs on the host CPU, but the NIC hardware handles dataplane packet forwarding.

**SR-IOV acceleration** (`acceleration=sriov`):
- Requires a NIC supporting the `switchdev` kernel driver model with `hw-tc-offload` enabled, and OVS configured with `hw-offload=true`.
- LXD allocates a VF from the PF (specified via `acceleration.parent` on the OVN network, or auto-detected from the OVS integration bridge) and passes it directly to the instance.
- OVS/OVN flow rules are offloaded to the NIC hardware via TC flower, bypassing the host kernel datapath for forwarded traffic.
- Supported for both containers and VMs.

**VDPA acceleration** (`acceleration=vdpa`):
- Same NIC setup as SR-IOV, but uses the kernel vDPA subsystem (`vdpa`, `vhost_vdpa` modules) to expose a virtio-compatible interface to the guest.
- Supported for VMs only (not containers).

### Summary

| Feature | Supported |
|---|---|
| Off-path DPU (OVN chassis on DPU ARM cores) | No |
| SR-IOV inline hardware acceleration (`switchdev`) | Yes (containers + VMs) |
| VDPA inline hardware acceleration | Yes (VMs only) |

---

## MIG and vGPU

LXD has four GPU device types (`gputype`), and MIG/vGPU support splits across two of them.

### `gpu.type=mig` — bare MIG passthrough, containers only

`lxd/device/gpu_mig.go` implements this. The container specifies a MIG GPU Instance + Compute
Instance via `mig.gi`/`mig.ci` pair or `mig.uuid`. At startup LXD verifies the GI/CI exists
under `/proc/driver/nvidia/capabilities/gpu<N>/mig/gi<GI>/ci<CI>/access`, then passes the MIG
device name (`MIG-<gpu-uuid>/<gi>/<ci>`) to the container via the NVIDIA container runtime
(`nvidia.runtime=true` required, uses `libnvidia-container`). The container sees only that MIG
slice.

This is not vGPU — it is raw MIG passthrough with NVIDIA's container runtime enforcing isolation.

### `gpu.type=mdev` — vGPU via the kernel mdev subsystem, VMs only

`lxd/device/gpu_mdev.go` implements this. It:
1. Looks for the requested mdev profile directly on the physical GPU (`gpu.Mdev`).
2. If not found there, scans SR-IOV VFs of that GPU (`gpu.SRIOV.VFs[i].Mdev`) — this is the
   MIG-backed vGPU path.

On NVIDIA A100/H100 with both MIG and vGPU enabled, each MIG GPU Instance is exposed to the
host as an SR-IOV VF with its own `mdev_supported_types/` directory. LXD's resource enumeration
(`lxd/resources/gpu.go`) runs `gpuAddDeviceInfo` on every PCI device including VFs, so it
discovers those mdev profiles. `gpu_mdev.go` then creates a vGPU UUID under the VF's mdev path
and passes it to QEMU via the standard vGPU QEMU device.

MIG-backed vGPU for VMs is therefore supported via `gpu.type=mdev` — no special configuration
beyond selecting the mdev profile is required.

### Summary

| Use case | Supported | Device type | Target |
|---|---|---|---|
| Raw MIG GI passthrough | Yes | `gpu.type=mig` | Containers only |
| Standard vGPU (whole GPU) | Yes | `gpu.type=mdev` | VMs only |
| MIG-backed vGPU (MIG GI → SR-IOV VF → mdev) | Yes | `gpu.type=mdev` | VMs only |
| MIG-backed vGPU for containers | No | — | Not supported |
| SR-IOV VF passthrough (raw, no vGPU features) | Yes | `gpu.type=sriov` | Containers + VMs |
| Whole GPU passthrough | Yes | `gpu.type=physical` | Containers + VMs |

The one gap: there is no way to give a container a vGPU backed by a MIG slice. Containers can
get raw MIG slices (`gpu.type=mig`) or raw SR-IOV VFs (`gpu.type=sriov`), but not mdev-based
vGPU — the mdev device type is gated to VMs at validation time (`gpu_mdev.go:210`).

### Can bare MIG passthrough and MIG-backed vGPU coexist on the same host?

**On the same physical GPU: No.** The two features require mutually exclusive NVIDIA driver
modes:

- `gpu.type=mig` requires the standard NVIDIA datacenter driver with MIG enabled. MIG GIs are
  exposed through `/proc/driver/nvidia/capabilities/gpu<N>/mig/gi<GI>/ci<CI>/access`, which LXD
  checks at startup (`gpu_mig.go:149`). The GPU does not expose SR-IOV VFs in this mode.

- `gpu.type=mdev` (MIG-backed vGPU) requires the NVIDIA vGPU software driver (enterprise
  license, separate package). In this mode, MIG GIs are exposed as SR-IOV VFs with
  `mdev_supported_types/` directories, which LXD writes to at startup (`gpu_mdev.go:130`). The
  character-device-based MIG capability path used by bare passthrough may not exist or be usable
  in this mode.

These driver modes are mutually exclusive per GPU. LXD has no code to detect or prevent the
conflict — if both are configured on the same physical GPU, the failure comes from the NVIDIA
driver at instance start time.

**On different GPUs on the same host: Yes.** LXD identifies GPUs by PCI address and has no
cross-GPU constraints. A host with two A100s could run one GPU in standard MIG mode (containers
via `gpu.type=mig`) and the other in vGPU software mode (VMs via `gpu.type=mdev`) without any
issues.

---

## Jumbo Frames

LXD accepts MTU values from **68 to 16384** (`IsNetworkMTU` in `shared/validate/validate.go`).
Whether jumbo frames work end-to-end — and whether LXD picks them up automatically — depends on
the network type.

### Instance-to-instance (east-west)

**Bridge network** (`network.type=bridge`): set `bridge.mtu` on the network. LXD sets that MTU
on the bridge interface and attaches a dummy `-mtu` interface to force the bridge to honour it
(`driver_bridge.go`). Every veth connecting an instance to the bridge inherits the MTU
automatically — no per-NIC configuration is needed. Two hard ceilings apply regardless:
- FAN bridges: maximum 1450
- Bridges with GRE/VXLAN tunnels: maximum 1400
- Standard bridges: up to 16384

**OVN network** (`network.type=ovn`): set `bridge.mtu` on the OVN network. LXD propagates it
to all instance veth/tap pairs and advertises it via DHCP option 26 and IPv6 RA. The OVN Geneve
tunnel eats overhead from the underlay — if the physical underlay supports jumbo frames (e.g.,
9000 MTU), setting `bridge.mtu=8942` (IPv4) or `bridge.mtu=8922` (IPv6) enables jumbo frames
between all instances on that network across any cluster member.

### Instance-to-host

Handled automatically by `networkCalculatePairMTU` (`device_utils_network.go`). The host-side
veth is set to `max(bridge MTU, instance MTU)`, and DHCP option 26 is advertised so the
instance's DHCP client sets the correct MTU without manual configuration.

For passthrough NIC types (`nic.physical`, `nic.macvlan`, `nic.sriov`, `nic.ipvlan`), the
`mtu` config key is applied directly to the host-side or VF interface.

### Egress from the host

For bridge networks, the bridge MTU must not exceed the physical interface's MTU. LXD does not
configure the physical NIC MTU — that is host OS configuration. Once set at the OS level,
`bridge.mtu` must be set to match.

For OVN networks, the uplink network's `bridge.mtu` or `mtu` config propagates to the egress
veth pair (`driver_ovn.go`). The physical NIC must be configured at the OS level for the desired
MTU.

### Auto-detection vs. explicit configuration

Behaviour differs by network type when `bridge.mtu` is not set:

**Bridge network — explicit required.** `driver_bridge.go` hardcodes 1500 as the default with
no code to read the host NIC's MTU. If `bridge.mtu` is not set, the bridge comes up at 1500
regardless of the host configuration.

**OVN network — fully automatic.** At network start, if `bridge.mtu` is not set, LXD calls
`getOptimalBridgeMTU()` (`driver_ovn.go`), which reads the actual MTU of the host's underlay
interface (whichever interface holds OVN's encap IP) and subtracts Geneve tunnel overhead:
- IPv4 encap: underlay MTU − 58 bytes
- IPv6 encap: underlay MTU − 78 bytes

The computed value is saved back to `bridge.mtu` in the DB so instances see it via DHCP. If the
host NIC is already at 9000, LXD automatically uses 8942 (IPv4) or 8922 (IPv6) with no
explicit configuration.

**Passthrough NIC types (macvlan, physical, SR-IOV) — transparent.** If `mtu` is not set on
the device, the Linux kernel inherits the parent interface's MTU at creation time. Jumbo frames
are transparent as long as the host is configured.

### Summary

| Network / NIC type | Auto-detects host jumbo MTU? | Action required |
|---|---|---|
| Bridge (`network.type=bridge`) | No — hardcoded 1500 default | Set `bridge.mtu` explicitly |
| OVN (`network.type=ovn`) | Yes — reads underlay MTU, subtracts Geneve overhead | Nothing; computed and saved automatically |
| OVN egress veth (uplink) | Only if uplink network has `bridge.mtu`/`mtu` set | Set MTU on the uplink network |
| `nic.macvlan` | Yes — kernel inherits parent MTU | Nothing |
| `nic.physical` / `nic.sriov` | Yes — host interface MTU used as-is | Nothing |

---

## DNS and NTP Configuration

### DNS server configuration

**Bridge networks**

dnsmasq runs bound to the bridge IP and serves as both DHCP server and DNS resolver for
instances. The bridge IP is automatically advertised as the DNS server — there is no config key
to push a different DNS server IP to instances. What can be configured:

- `dns.domain` — domain dnsmasq resolves for instance hostnames (e.g. `lxd`)
- `dns.mode` — `dynamic` (auto-register hostnames), `managed` (manual), `none`
- `dns.search` — pushed as DHCP option 119 (search domain list)
- `dns.zone.forward` / `dns.zone.reverse.ipv4` / `dns.zone.reverse.ipv6` — integrate with
  LXD's built-in DNS zone feature

dnsmasq itself forwards unresolved queries to the host's `/etc/resolv.conf`. To push a specific
upstream DNS server to instances (as DHCP option 6), the only path is `raw.dnsmasq` — a raw
config escape hatch that appends arbitrary dnsmasq directives (`driver_bridge.go:1189`).

**OVN networks**

DNS servers for instances come from the **uplink (physical) network** config key
`dns.nameservers` — a comma-separated list of DNS server IPs (`driver_ovn.go:1359`). If
`dns.nameservers` is not set, the uplink gateway IP is used by default. LXD programs these into
OVN DHCP options and IPv6 RA `RecursiveDNSServer` fields.

The OVN network itself supports:
- `dns.domain` / `dns.search` — domain and search list pushed via DHCP/RA
- `dns.zone.forward` / `dns.zone.reverse.*` — LXD DNS zone integration

**Per-instance DNS override**

`cloud-init.network-config` (instance config key) lets you inject a full cloud-init network
config YAML including custom DNS servers per NIC, served via the `devlxd` metadata API and
applied by cloud-init inside the instance.

### NTP server configuration

Not supported. There is no NTP-related configuration anywhere in LXD:
- dnsmasq never pushes DHCP option 42 (NTP server)
- OVN DHCP/RA options have no NTP field
- No `ntp.*` config keys exist at any level

The only path is via **cloud-init** using the `cloud-init.vendor-data` instance config key:

```yaml
#cloud-config
ntp:
  enabled: true
  servers:
    - 192.0.2.1
    - pool.ntp.org
```

This is served through the `devlxd` metadata endpoint and applied by cloud-init at boot. Works
for both VMs and containers that have cloud-init installed.

### Summary

| Feature | Bridge | OVN | Per-instance |
|---|---|---|---|
| DNS server for instances | Bridge IP (dnsmasq), not configurable directly | From uplink `dns.nameservers` | `cloud-init.network-config` |
| DNS search domain | `dns.search` → DHCP option 119 | `dns.search` → DHCP/RA | `cloud-init.network-config` |
| DNS domain (hostname resolution) | `dns.domain` | `dns.domain` | — |
| Custom upstream DNS forwarder | `raw.dnsmasq` escape hatch only | Set on uplink network | `cloud-init.network-config` |
| NTP server | Not supported | Not supported | `cloud-init.vendor-data` only |

---

## Secret Management

LXD has no dedicated secret store or key management API. What it offers falls into four
categories.

### vTPM (Virtual Trusted Platform Module) — the primary mechanism

Both containers and VMs support a `tpm` device type backed by `swtpm` (software TPM 2.0
emulator).

**Containers** (`device/tpm.go`): LXD loads the `tpm_vtpm_proxy` kernel module and runs
`swtpm` in `chardev` mode. The emulator creates a new `/dev/tpm0` + `/dev/tpmrm0` pair and
exposes them inside the container. TPM state is persisted in `{instance_path}/tpm.{device}/`
on the host across restarts.

**VMs** (`device/tpm.go`): `swtpm` runs in `socket` mode connected to QEMU via a Unix socket,
which QEMU exposes as a standard TPM 2.0 device to the guest firmware and OS.

This is LXD's mechanism for TPM-based workloads: disk encryption key sealing (e.g., LUKS with
TPM unlock), measured boot via PCR attestation, and certificate/key storage inside the
instance. TPM state is local to each cluster member — the device cannot be migrated when
`migration.stateful` is enabled.

### AMD SEV (Secure Encrypted Virtualization) — VMs only

Four instance config keys enable AMD SEV memory encryption:

| Key | Purpose |
|---|---|
| `security.sev` | Enable SEV — VM RAM encrypted by AMD hardware; hypervisor cannot read it |
| `security.sev.policy.es` | Enable SEV-ES — also encrypts CPU register state |
| `security.sev.session.dh` | DH public key for remote attestation session |
| `security.sev.session.data` | Session data blob for remote attestation |

LXD reads SEV capabilities via QMP (`driver_qemu.go:1901`) and passes the session DH key and
data to QEMU for remote attestation workflows. SEV is incompatible with some disk device
configurations (checked at start time in `device_utils_disk.go:287`). Requires AMD EPYC with
SEV support.

### Plaintext secret injection (no protection at rest)

LXD provides three mechanisms to push values into instances, all stored **in plaintext in the
SQLite database**:

- **`environment.<VAR>=<value>`**: sets environment variables injected into the container's
  init process via `lxc.environment` (`driver_lxc.go:990`). Also supported for VMs.
- **`user.*`** keys: arbitrary metadata readable from inside the instance via the `devlxd`
  metadata endpoint at `GET /1.0/config`.
- **`cloud-init.vendor-data` / `cloud-init.user-data`**: cloud-init config served via
  `devlxd`, can include secrets (tokens, SSH keys, passwords). `cloud-init.ssh-keys.*` is a
  dedicated sub-namespace that LXD merges into cloud-init config automatically.

None of these are encrypted at rest — they live in `lxd.db` in plaintext.

### TLS certificate management (for LXD itself, not instances)

LXD supports ACME/Let's Encrypt to automatically provision and renew its own API TLS
certificate (`core.acme_domain`, `core.acme_ca_url`). This manages the LXD daemon's server
certificate, not secrets for instances.

### What LXD does not provide

- No secret store API (no equivalent of Kubernetes Secrets or HashiCorp Vault)
- No Vault / KMS / HSM integration. The only HashiCorp packages in the codebase are
  `go-envparse`, `errwrap`, and `go-multierror` — utility libraries unrelated to Vault. There
  are no Vault API calls, no `vault.*` config keys, and no Vault agent integration anywhere.
- No LUKS/dm-crypt key management at the LXD layer (ZFS native encryption is possible but
  managed entirely outside LXD)
- No key rotation or derivation
- No encryption of instance config values at rest in the database

### Summary

| Feature | Containers | VMs | Notes |
|---|---|---|---|
| vTPM 2.0 (key sealing, measured boot) | Yes (`swtpm` via vtpm-proxy) | Yes (`swtpm` socket to QEMU) | State persisted per host; not live-migratable |
| AMD SEV memory encryption | No | Yes | Requires AMD EPYC with SEV; SEV-ES available |
| Environment variable injection | Yes | Yes | Plaintext in DB |
| `user.*` / `cloud-init.*` metadata | Yes | Yes | Plaintext in DB; readable via devlxd |
| Vault / KMS integration | No | No | — |
| Storage encryption key management | No | No | ZFS native encryption managed outside LXD |

---

## Resource Overcommit

LXD supports overcommit across all three resource dimensions (CPU, memory, disk), but provides
no admission control — it never validates that aggregate limits across running instances stay
within host capacity.

### CPU Overcommit (Containers Only)

**Soft (share-based):** `limits.cpu.allowance=<N>%` maps to cgroup CPU shares:

- cgroup v1: `cpu.shares` (scale 0–1024)
- cgroup v2: `cpu.weight` (scale 0–100)

Computed as `(maxShares / 100) * percent ± priority_adjustment`. This is true soft overcommit —
unused CPU cycles spill to any container that wants them; the allowance only enforces relative
priority under contention.

**Hard (quota-based):** `limits.cpu.allowance=<quota>ms/<period>ms` sets CFS hard limits
(`cpu.cfs_quota_us` / `cpu.cfs_period_us` on v1; `cpu.max` on v2). The container is throttled
even when CPUs are idle.

**Priority:** `limits.cpu.priority=<0–10>` (container only) adjusts the share calculation by
`±(10 - priority)` shares. Default 10 (maximum). Described in the codebase as used "when
overcommitting resources". Lower values cause earlier yield under contention.

**VMs:** No CPU overcommit. `limits.cpu` sets the number of QEMU vCPU threads with no share
management from LXD.

### Memory Overcommit (Containers Only)

`limits.memory.enforce` controls hard vs. soft behavior:

| Value | cgroup knob | Behavior |
|---|---|---|
| `hard` (default) | `memory.limit_in_bytes` (v1), `memory.max` (v2) | OOM-kill if exceeded. LXD also sets soft limit at 90% to trigger early reclaim. |
| `soft` | `memory.soft_limit_in_bytes` (v1) | Container can exceed limit when host has free memory; reclaimed first under pressure. |

**Swap control:**
- `limits.memory.swap=false`: disables swap (`memsw.limit_in_bytes = memory.limit_in_bytes` on
  v1; `memory.swap.max=0` on v2)
- `limits.memory.swap.priority=<0–10>`: swappiness adjustment (v1 only, via `memory.swappiness`)

**VMs:** No soft memory limit. Live resize is done via a **virtio-balloon device** —
LXD inflates/deflates the balloon to reclaim or return memory from the VM. This adjusts what
the VM sees but does not implement traditional memory overcommit.

### Disk Overcommit

**LVM thin provisioning (default):** `lvm.use_thinpool=true` is the default. All volumes are
thin-provisioned LVs inside an LVM thin pool. Total logical sizes of all volumes can exceed the
pool's physical size — classic disk overcommit. `size` is a thin quota, not a pre-allocated
reservation.

**ZFS / Btrfs:** Inherently copy-on-write with no pre-allocation. `size` sets a dataset quota;
actual space is shared across the pool. Thin provisioning is implicit.

**Dir backend:** Uses Linux project quotas — space is enforced per-volume and is not
overcommitted.

### Summary

| Resource | Soft overcommit | Hard cap | Mechanism |
|---|---|---|---|
| CPU (containers) | `limits.cpu.allowance=<N>%` + `limits.cpu.priority` | `limits.cpu.allowance=<ms>/<ms>` | cgroup shares / CFS quota |
| CPU (VMs) | No | `limits.cpu` (count) | QEMU vCPU threads |
| Memory (containers) | `limits.memory.enforce=soft` | `limits.memory.enforce=hard` | cgroup soft/hard limit |
| Memory (VMs) | No | `limits.memory` + balloon | virtio-balloon live resize |
| Disk (LVM) | Yes, by default | `size` is thin quota | LVM thin pool |
| Disk (ZFS/Btrfs) | Yes, implicitly | `size` is dataset quota | CoW pool sharing |
| Disk (Dir) | No | `size` is fs quota | Project quota |

---

## Network Filesystem Support

### NFS / CIFS / SMB / GlusterFS — No Storage Driver

LXD has no storage pool type for any traditional network filesystem. There is no NFS, CIFS,
SMB, or GlusterFS driver. The complete list of storage pool backends is: `dir`, `lvm`, `btrfs`,
`zfs`, `ceph` (RBD), `cephfs`, `cephobject`, `powerflex`, `powerstore`, `pure`, `alletra`.

The only network-backed storage pools are the Ceph family. CephFS is a distributed POSIX
filesystem over a network, accessed via the kernel CephFS client, not via NFS/SMB.

### Host-Mounted NFS/CIFS as Container Bind-Mount

For containers, the `disk` device `source` accepts any host path. Since `sourceIsLocalPath()`
treats any non-Ceph path as a local path, an NFS or CIFS share that the host has already
mounted can be bind-mounted into a container:

```
lxc config device add mycontainer shareddata disk source=/mnt/nfs_share path=/data
```

LXD treats the path as opaque and bind-mounts whatever is at that host path. It does not set
up, configure, or tear down the underlying NFS mount. The host admin is responsible for
mounting the NFS share before the container starts. `restricted.devices.disk.paths` project
restrictions apply to NFS-backed paths the same as local paths.

### VirtIO-FS for Host-to-VM Directory Sharing

For VMs, host directory sharing is done via `virtiofsd` — a FUSE daemon running on the host
that presents host directory content to the VM over a VirtIO socket:

1. LXD starts a `virtiofsd` process per disk device with a Unix domain socket
2. QEMU connects to the socket via a `vhost-user-fs` device
3. The VM guest mounts the filesystem with `mount -t virtiofs`

VirtIO-FS has better performance than NFS for host-to-guest sharing and supports full POSIX
semantics. The `io.threads` config key controls the virtiofsd thread pool size.

**9p fallback:** When `virtiofsd` is not available, LXD falls back to VirtIO 9P (Plan 9
filesystem protocol) for the **config drive only**. User-added disk devices require virtiofsd
and do not fall back to 9p. A warning is issued when this fallback occurs.

### Summary

| Scenario | Supported | Mechanism |
|---|---|---|
| NFS / CIFS / SMB storage pool backend | No | — |
| GlusterFS storage pool backend | No | — |
| NFS mount on host → bind-mounted into container | Yes | Host bind-mount passthrough |
| NFS mount on host → shared to VM | Indirectly | Re-expose via virtiofsd |
| CephFS (distributed POSIX network FS) | Yes | Native `cephfs` storage driver |
| Host directory → VM directory (virtiofs) | Yes | `virtiofsd` + virtio socket |
| Host directory → VM directory (9p) | Config drive only | Fallback when virtiofsd is missing |

### Hot-Plugging Volumes to Running Instances

No restart is required to attach or detach additional disk devices. `disk.CanHotPlug()`
returns `true` unconditionally for every disk device type, and both instance drivers live-attach
without stopping the instance.

**Containers:** `deviceHandleMounts()` bind-mounts the new device directly into the running
container's mount namespace. UID/GID shifting for unprivileged containers is applied first.

**VMs:** Two paths depending on source type:

- **Directory / host path** (`virtiofs`): LXD starts a new `virtiofsd` process and sends QMP
  `AddCharDevice` + `AddDevice` to QEMU. The VM guest sees a new virtiofs mount tag.
- **Block device** (storage pool volume, RBD, etc.): `addDriveConfig` is called with the PCIe
  hot-plug bus allocator and the resulting monitor hook is fired via QMP. The block device
  appears as a new PCIe device in the VM.

Hot-detach is equally live: virtiofs devices use QMP `RemoveDevice` + `RemoveCharDevice`;
block devices use QMP `eject` + `device_del`.

**Caveats:**
- The root disk (`path=/`) is always present since instance creation and cannot be added or
  removed while running. Its storage pool can only be changed via `lxc move`.
- After hot-plug, the guest OS still needs to detect the new device (udev / rescan); LXD's
  side of the attachment is fully live.
- Host-path disk devices (`pool` unset) are flagged non-migratable but this is orthogonal to
  hot-plug capability.
