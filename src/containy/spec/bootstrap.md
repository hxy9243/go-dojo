# Private Init Bootstrap

Containy starts a container by re-executing its own binary as a private process
inside new Linux namespaces. The host supervisor must give that process the
rootfs path and workload command before it can `chroot` and call `exec`.

## Current protocol

The supervisor builds this private structure:

```go
type initConfig struct {
    RootfsDir string   `json:"rootfs"`
    Command   string   `json:"command"`
    Args      []string `json:"args"`
}
```

It serializes the structure as JSON into a Linux `memfd`: an anonymous,
memory-backed file with no persistent pathname. `createInitCommand` passes that
file as `exec.Cmd.ExtraFiles[0]` while starting `/proc/self/exe`.

```text
host supervisor                         namespace init
---------------                         --------------
initConfig
    │
    ├─ JSON → anonymous memfd
    │
    └─ ExtraFiles[0] ───── exec ───────► file descriptor 3
                                          │
                                          └─ readInitConfig(3)
                                             validate and close FD
```

Unix reserves descriptors `0`, `1`, and `2` for stdin, stdout, and stderr.
Go therefore installs `ExtraFiles[0]` as descriptor `3` in the child. The
memfd may have a different descriptor number in the supervisor; descriptor
numbers are process-local.

The `_CONTAINY_INTERNAL_INIT=1` marker tells the re-executed binary to enter
the private init path. The marker selects the mode; the structured
configuration itself travels through FD 3. `readInitConfig` reads at most
64 KiB plus one byte, rejects oversized or malformed JSON, validates the
absolute rootfs and non-empty command, and closes the descriptor.

## Why this design

- It carries structured arguments across an `exec` boundary without custom
  shell escaping.
- It keeps internal rootfs data out of public command-line arguments.
- It does not consume workload stdin.
- It needs no temporary pathname, permissions, or cleanup.
- It is simpler than a bidirectional socket because containy sends only one
  startup message.

The current memfd is anonymous but not sealed. This is sufficient for the
present trust model because only the trusted re-executed containy binary can
access it, and it closes the descriptor before starting the workload. Memfd
seals would make immutability explicit if this becomes a security boundary.

## Alternatives

| Transport | Trade-off |
| --- | --- |
| Command-line arguments | Simple, but visible in process inspection and awkward for structured data. |
| Environment variable | Simple, but size-limited and easy to leak into inherited process state. |
| Temporary file | Easy to inspect, but needs a pathname, safe permissions, and cleanup. |
| Pipe | Gives the child a read-only stream, but requires coordinated writing and startup. |
| Unix socket pair | Supports synchronization and FD passing, but adds complexity unnecessary for one message. |

## Docker and runc comparison

Docker uses several boundaries rather than one protocol:

1. The Docker CLI sends JSON over the Engine API.
2. Docker Engine and containerd communicate using APIs; containerd prepares an
   OCI bundle containing `config.json` and a rootfs.
3. The containerd shim invokes `runc`.
4. Inside `runc`, the parent passes an internal JSON `initConfig` to `runc
   init` through an inherited Unix socket and uses additional sockets for
   readiness, errors, hooks, and descriptor transfer.

Containy's bootstrap is therefore closest to the final `runc` parent-to-init
handoff, but it is deliberately one-way and smaller.

References:

- [OCI runtime bundle](https://github.com/opencontainers/runtime-spec/blob/main/bundle.md)
- [containerd runtime-v2 architecture](https://github.com/containerd/containerd/blob/main/docs/runtime-v2.md)
- [runc parent/init communication](https://github.com/opencontainers/runc/blob/main/libcontainer/process_linux.go)
- [runc init-side decoder](https://github.com/opencontainers/runc/blob/main/libcontainer/init_linux.go)
