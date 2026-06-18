# Off-path BlueField-3 DPU Support in LXD — Feasibility Assessment

## What "off-path BlueField-3" requires from LXD

In the off-path model, the DPU's ARM cores run `ovs-vswitchd` + `ovn-controller` as the OVN
chassis. The host has no local OVS dataplane — it just hands VFs to instances and the DPU's
OVS picks up the representors. LXD currently assumes OVS and `ovn-controller` are co-located
with the daemon on every cluster member. Four independent areas need surgery:

---

## 1. Chassis management — moderate

`networkUpdateOVNChassis` (`lxd/networks_utils.go`) checks `ClusterRoleOVNChassis` on
heartbeat and calls `networkRestartOVN` if the local chassis state is wrong. In off-path mode,
the host must never start `ovn-controller` locally — the DPU is the chassis.

Needed:
- New cluster config keys (`network.ovn.dpu.enabled`, `network.ovn.dpu.chassis_id`,
  `network.ovn.dpu.ovsdb_connection`)
- A conditional in `networkUpdateOVNChassis` to skip local chassis startup entirely when DPU
  mode is active
- HA chassis group wiring (`lxd/network/driver_ovn.go`) reads `ovs.ChassisID()` from the
  **host's** local OVS `external_ids:system-id`, but must use the **DPU's** chassis ID
  instead — needs a new lookup path

This part is well-contained and not too large.

---

## 2. OVS wrapper — the hardest part

Every OVS operation in LXD (`lxd/network/openvswitch/ovs.go`) shells out to local
`ovs-vsctl`. In off-path mode, the relevant OVS instance is on the DPU, not the host:

```go
// Every function looks like this — all local:
shared.RunCommand(context.TODO(), "ovs-vsctl", ...)
```

To talk to the DPU's OVS you need remote OVSDB (TCP/SSL to the DPU, e.g.
`tcp:192.168.100.2:6640`). LXD has no OVSDB client library — it only has shell wrappers. The
options are:

- **Option A**: Add a Go OVSDB client (e.g. `ovn-org/libovsdb`) and rewrite `ovs.go` to use
  it, parameterised by connection endpoint. Significant refactor.
- **Option B**: Shell out to `ovs-vsctl --db=tcp:DPU_IP:6640 ...` instead of local. Much
  smaller change — just thread a connection string through the `OVS` struct — but fragile and
  non-idiomatic.
- **Option C**: Rely on BlueField-3's DOCA/DOCA-OVS auto-discovery. BF3 in embedded mode
  (host management mode) can auto-discover VF representors and wire them to its local `br-int`
  without the host telling it anything. If LXD can depend on DOCA doing this automatically,
  the problem simplifies dramatically — LXD just allocates the VF, passes it to the VM, and
  the DPU handles OVS wiring. This is the most realistic short-path.

---

## 3. Representor wiring in `nic_ovn.go` — moderate, depends on Option C above

`setupAcceleration` (`lxd/device/nic_ovn.go`) currently:
1. Finds a free VF on the host PF
2. Adds the VF's representor to the **host's** OVS `br-int`
3. Sets `external_ids:iface-id=<ovn-port-name>` on that representor so `ovn-controller` picks
   it up

In off-path mode, steps 2–3 must happen on the DPU's OVS, not the host's. With Option C
(DOCA auto-discovery), the DPU auto-adds the representor — but LXD still needs to set
`iface-id` on it so OVN binds the logical port. That requires either remote OVSDB or a DOCA
API call.

Without DOCA auto-discovery, LXD needs to know the representor port name as seen by the DPU's
OVS (which may differ from the host's view), and make a remote OVSDB call to set `iface-id`.
This is non-trivial because the representor netdev name on the DPU side and the host side can
differ.

---

## 4. Cluster config and API surface — small

New `cluster.Config` keys in `lxd/cluster/config/config.go`, new validation, possibly a new
cluster role or a per-member config entry identifying which DPU serves which host member.
Straightforward boilerplate.

---

## Overall difficulty: moderate-to-hard

| Area | Effort | Risk |
|---|---|---|
| Chassis mgmt / `networkUpdateOVNChassis` | Small (~100–200 LOC) | Low |
| Cluster config keys + API | Small | Low |
| HA chassis group using DPU chassis ID | Small | Low |
| OVS wrapper — remote OVSDB (full) | Large (400–600+ LOC, new dep) | High |
| OVS wrapper — `--db=` flag shortcut | Small (~30 LOC) | Medium (fragile) |
| Representor wiring with DOCA auto-discovery | Small–Medium | Medium |
| Representor wiring without DOCA (remote OVSDB) | Tied to OVS wrapper above | High |

The critical path is the OVS interaction model. If DOCA's embedded-mode auto-discovery can be
depended on (i.e. the DPU auto-wires representors into its `br-int` and LXD only needs to set
`iface-id` via remote OVSDB), then the total work is roughly 1–2 engineer-weeks of focused
effort. If full remote OVSDB plumbing is required without DOCA, it is closer to 3–5 weeks
including testing, because `ovs.go` is used pervasively and the `OVS` struct would need to
become connection-aware throughout.
