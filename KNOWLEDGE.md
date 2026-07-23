# LXD Architecture Support

## Host architectures LXD can run on

| Architecture | Canonical name |
|---|---|
| x86 32-bit | `i686` |
| x86 64-bit | `x86_64` |
| ARMv6 32-bit LE | `armv6l` |
| ARMv7 32-bit LE | `armv7l` |
| ARMv8 32-bit LE | `armv8l` |
| ARMv8 64-bit LE | `aarch64` |
| PowerPC 32-bit BE | `ppc` |
| PowerPC 64-bit BE | `ppc64` |
| PowerPC 64-bit LE | `ppc64le` |
| s390x 64-bit BE | `s390x` |
| MIPS 32-bit | `mips` |
| MIPS 64-bit | `mips64` |
| RISC-V 32-bit LE | `riscv32` |
| RISC-V 64-bit LE | `riscv64` |
| LoongArch 64-bit | `loongarch64` |

## VM guest architectures

| Guest arch | QEMU binary | PCIe bus type |
|---|---|---|
| `x86_64` | `qemu-system-x86_64` | `pcie` |
| `aarch64` / ARMv7 / ARMv8 32-bit | `qemu-system-aarch64` | `pcie` |
| `ppc64le` | `qemu-system-ppc64` | `pci` |
| `riscv64` | `qemu-system-riscv64` | `pcie` |
| `s390x` | `qemu-system-s390x` | `ccw` |

## Valid host/guest VM architecture combinations

A host can run a VM guest if the guest arch equals the host's native arch or falls within the host's supported personalities (32-bit compat ABIs). Guest arch must also be supported by `qemuArchConfig`.

| Host | Valid VM guests |
|---|---|
| `x86_64` | `x86_64` |
| `aarch64` | `aarch64`, `armv7l`, `armv8l` |
| `armv7l` | `armv7l` |
| `armv8l` | `armv8l`, `armv7l` |
| `ppc64le` | `ppc64le` |
| `riscv64` | `riscv64` |
| `s390x` | `s390x` |

Notes:
- `i686` is in `x86_64`'s personalities but is not a supported VM guest arch in `qemuArchConfig`.
- `armv6l` is in `aarch64`/`armv7l`/`armv8l` personalities but is not a supported VM guest arch.
- `ppc`, `ppc64`, `mips`, `mips64`, `riscv32`, `loongarch64` hosts have no supported VM guest archs.
- All ARM guests (armv7l, armv8l, aarch64) use `qemu-system-aarch64`.
