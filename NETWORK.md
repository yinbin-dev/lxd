# LXD Project OVN Network Topology

## Single OVN Network — Logical Objects

Each OVN network created in a project instantiates exactly these OVN logical objects:

```
 Uplink (bridge or physical net, in "default" project)
 ┌──────────────────────────────────────────────────┐
 │  provider network  (e.g. "UPLINK" localnet port) │
 └──────────────────────┬───────────────────────────┘
                        │ localnet
          ┌─────────────▼──────────────────┐
          │  External Switch (ls-ext)       │
          │  <prefix>-ls-ext               │
          │  ┌─────────────────────────┐   │
          │  │ lsp-provider  (localnet)│   │
          │  │ lsp-router    (to LR)   │   │
          │  └─────────────────────────┘   │
          └─────────────┬──────────────────┘
                        │ lrp-ext (has uplink IP)
          ┌─────────────▼──────────────────┐
          │  Logical Router (lr)            │
          │  <prefix>-lr                   │
          │  - SNAT rule (int→ext)         │
          │  - default route → uplink GW   │
          │  - chassis group (HA)          │
          └─────────────┬──────────────────┘
                        │ lrp-int (= gateway IP: ipv4.address/ipv6.address)
          ┌─────────────▼──────────────────┐
          │  Internal Switch (ls-int)       │
          │  <prefix>-ls-int               │
          │  - DHCP options (v4/v6)        │
          │  - IP allocation pool          │
          │  - ACL port group              │
          │  ┌───────────────────────────┐ │
          │  │ lsp-router  (to LR)       │ │
          │  │ lsp-instance-<uuid>-nic0  │ │
          │  │ lsp-instance-<uuid>-nic1  │ │
          │  │ ...                       │ │
          │  └───────────────────────────┘ │
          └────────────────────────────────┘
                   instance NICs
               (one port per NIC per VM/container)
```

## Is it one subnet or multiple?

**Single flat subnet.** Each OVN network has exactly one internal switch (`ls-int`) with one
IPv4 prefix (`ipv4.address`) and/or one IPv6 prefix (`ipv6.address`). All instances connected
to that network share that single L2/L3 domain. The router's internal port is the default gateway.

There is no sub-segmentation inside the network — it is a flat broadcast domain on the internal
switch, with the logical router acting as the gateway to the uplink.

## Multiple Networks in a Project — Network Peering

A project can have multiple independent OVN networks, each with its own isolated subnet and its
own set of the objects above. They are isolated by default, but can be connected via **network
peering**, which creates a direct router-to-router link (no external switch involved):

```
  Project "myproject"

  OVN net-A (10.0.1.0/24)          OVN net-B (10.0.2.0/24)
  ┌──────────────────┐              ┌──────────────────┐
  │  ls-int-A        │              │  ls-int-B        │
  │  instances...    │              │  instances...    │
  └──────┬───────────┘              └──────┬───────────┘
         │ lrp-int                         │ lrp-int
  ┌──────▼───────────┐              ┌──────▼───────────┐
  │  lr-A            │              │  lr-B            │
  │  lrp-peer-netB ──┼──────────────┼── lrp-peer-netA │
  │  lrp-ext         │              │  lrp-ext         │
  └──────┬───────────┘              └──────┬───────────┘
         │                                 │
      ls-ext-A                          ls-ext-B
         │                                 │
      uplink                            uplink
```

The peer ports are direct router-to-router links (`lrp-peer-net<id>`); each router gets a static
route for the peer's subnet pointing to that peer port. Traffic between the two networks never
leaves the OVN southbound fabric.

## Summary

| Aspect | Detail |
|---|---|
| Subnets per OVN network | **One** (single `ls-int`, one IPv4 + optional IPv6 prefix) |
| External connectivity | Via `lr` → `ls-ext` → `localnet` → uplink bridge/physical |
| NAT | SNAT on `lr` when `ipv4.nat=true` (maps internal subnet → uplink IP) |
| Instance isolation from uplink | Yes — fully behind the logical router |
| Cross-network routing | Only via explicit **network peering** (router-to-router ports) |
| HA | Chassis group on `lrp-ext` picks the active gateway node |

## Key Source Files

- `lxd/network/driver_ovn.go` — `setup()` at line 2266 is the authoritative topology builder
- `lxd/network/openvswitch/ovn.go` — OVN northbound API wrappers
- `lxd/network/driver_ovn.go` — `PeerCreate()` / `peerSetup()` at line 5976 for peering logic
