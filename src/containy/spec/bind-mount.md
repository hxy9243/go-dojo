# Bind Mounts

Containy can expose a host directory or file at a path inside the container:

```bash
sudo containy run \
  -v /host/data:/data:ro \
  ./rootfs \
  /bin/sh -c 'ls /data'
```

## CLI contract

```text
-v HOST:DEST[:ro|rw]
--volume HOST:DEST[:ro|rw]
```

- The flag is repeatable.
- `HOST` is resolved to an absolute host path and must exist.
- `DEST` must be an absolute container path.
- `ro` requests a read-only bind remount.
- `rw` and an omitted mode are read-write.

The current colon-separated syntax does not support a host filename containing
a colon.

## Lifecycle

Bind mounts are applied by the host supervisor before the child mount
namespace is cloned:

```text
parse -v
  │
  └─ runtime.Mount{Source, Destination, ReadOnly}
       │
       ├─ validate source and destination
       ├─ reject runtime-owned destinations
       ├─ reject existing symlinks in the destination path
       ├─ create a directory or file mountpoint beneath rootfs
       ├─ mount(source, target, MS_BIND)
       └─ optionally remount MS_BIND | MS_REMOUNT | MS_RDONLY

clone child mount namespace
  │
  └─ child inherits the bind mount

container exits
  │
  └─ supervisor unmounts bind targets in reverse order
```

If setup fails after one or more mounts were applied, containy unmounts the
partial set before returning the error.

## Reserved destinations

The runtime owns these trees:

```text
/
/proc
/dev
/run
/tmp
/var
```

A bind destination equal to or below `/proc`, `/dev`, or `/run` is rejected
because the namespace init later replaces those trees with container-private
filesystems. The exact destinations `/`, `/tmp`, and `/var` are also rejected
because runtime setup would chmod or populate the mounted host directory.
Nested destinations such as `/tmp/cache` and `/var/lib/data` remain valid.
`/sys` is not mounted by the runtime and is not currently reserved.

## Target preparation

Directory sources require a directory destination. Missing directory targets
are created with parent directories. File sources require a file destination;
a missing placeholder file is created with mode `0644`.

Existing symlinks in any destination component are rejected before mounting.
This avoids obvious redirection outside the rootfs, but the check and mount are
not atomic. A production-grade implementation would resolve and mount through
stable directory descriptors, preferably using `openat2` constraints.

Created mountpoint directories and placeholder files remain in a caller-owned
rootfs directory after unmount. They disappear automatically when containy
uses a temporary extracted rootfs.

## API

```go
type Mount struct {
    Source      string
    Destination string
    ReadOnly    bool
}
```

Implementation:

| Function | Responsibility |
| --- | --- |
| `applyMounts` | Validate, prepare, bind, and roll back on failure. |
| `resolveMountTarget` | Reject existing symlink traversal. |
| `prepareMountTarget` | Enforce file/directory compatibility and create targets. |
| `reservedRuntimeDestination` | Protect `/proc`, `/dev`, and `/run`. |
| `cleanupMounts` | Detach mounts in reverse order. |

## Trust boundary

Containy itself runs as root and does not claim to sandbox hostile input. Bind
source content remains host content, and a read-write mount allows the
container workload to modify it. Use `:ro` where mutation is not intended and
pass only paths controlled by the operator.

Tests cover CLI parsing, reserved destinations, symlink rejection, directory
and file mounts, and read-only behavior. Actual mount tests require root and a
Linux host with namespace support.
