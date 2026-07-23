# Filesystem and Image Extraction

This document describes the filesystem path implemented by containy: ingesting
a Docker-save archive, flattening its layers into a rootfs, preparing runtime
filesystems, and entering the result with `chroot`.

For the complete startup sequence, see [`overview.md`](overview.md).

## 1. Accepted inputs

`containy run` accepts:

- an existing rootfs directory;
- an uncompressed Docker-save tar containing `manifest.json`;
- a local Docker image reference, converted to a temporary archive with
  `docker save`.

It does not currently accept a raw flat-rootfs tar, gzip-compressed archive,
OCI image-layout directory, or registry reference that is not already present
in Docker.

## 2. Docker-save format

A Docker-save archive contains metadata and one tar per filesystem layer:

```text
image.tar
├── manifest.json
├── <config>.json
└── <layer paths>
    └── layer.tar
```

`manifest.json[0].Layers` provides the layer paths in base-to-top order.
Containy's extractor reads the archive, looks up those layers, and applies
them sequentially to one destination directory.

## 3. Extraction algorithm

```text
Extract(dockerSaveTar, destination):
    create destination and normalize its mode to 0755
    read outer tar
    parse manifest.json
    retain regular-file entries needed for layer lookup

    for layer in manifest[0].Layers:
        for entry in layer tar:
            .wh.<name>    -> remove the named lower entry
            .wh..wh..opq -> remove existing children of the directory
            directory    -> create and restore metadata
            regular file -> create content and restore metadata
            symlink      -> create link and restore link ownership
            hard link    -> create link and restore metadata

    return flattened destination
```

The extractor restores numeric UID/GID values from tar headers. Those IDs are
filesystem semantics: services such as APT may deliberately change to an image
user and depend on the archive's original ownership. File modes are applied
after ownership because `chown` can clear set-ID bits.

The destination root is normalized to mode `0755` because temporary
directories created by `os.MkdirTemp` begin as `0700`; leaving that mode in
place would prevent non-root image users from traversing `/`.

## 4. Trust and limitations

The extractor is suitable only for trusted archives. It currently:

- buffers outer regular-file entries, including layer tars, in memory;
- has no entry-count or extracted-byte limits;
- does not completely prevent `..` or symlink traversal from a malicious
  layer;
- accepts a narrower set of tar behavior than a production OCI unpacker;
- removes whiteout targets using best-effort deletion.

Because containy runs as root, processing an untrusted archive could modify
host paths. A hardened extractor would use descriptor-relative traversal
(`openat2` or carefully constrained `openat`), reject absolute and escaping
paths, bound resource use, and roll back partial extraction.

## 5. Host-side preparation

Before creating the namespace init, the supervisor:

1. normalizes rootfs `/etc/resolv.conf` to a regular file and overwrites it
   with the host resolver configuration, falling back to public resolvers;
2. applies requested user bind mounts beneath the rootfs;
3. later unmounts those binds after the namespace process exits.

For an existing rootfs directory, these operations can modify the supplied
tree. A rootfs extracted into a temporary directory is deleted after the run.

Bind-mount details are in [`bind-mount.md`](bind-mount.md).

## 6. Namespace runtime filesystems

After the mount namespace is created, `SetupRuntimeFilesystems` makes mount
propagation private and mounts `/proc`, `/dev`, `/dev/shm`, and `/run` beneath
the rootfs. It also prepares `/tmp` and `/var/run`.

All runtime directory components are checked with `Lstat` so an existing
rootfs symlink cannot redirect a privileged mount outside the tree.

See [`runtime-dir.md`](runtime-dir.md) for exact filesystems, flags, devices,
and modes.

## 7. Entering the rootfs

The private init process performs:

```text
chroot(rootfsDir)
chdir("/")
exec(workload)
```

Containy does not currently use `pivot_root`. Since it retains root
credentials and applies no capability restrictions, `chroot` is a filesystem
view change—not a complete security boundary.

The workload environment is:

```text
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
HOME=/root
USER=root
LOGNAME=root
TERM=<host TERM or xterm-256color>
```

Bare command names are resolved against that `PATH` after entering the
rootfs.

## 8. Relevant code and tests

```text
internal/rootfs/rootfs.go              Docker-save layer extraction
internal/rootfs/rootfs_test.go         extractor unit tests
internal/rootfs/rootfs_integration_test.go
internal/rootfs/runtimefs.go           private runtime mounts
internal/rootfs/runtimefs_test.go
internal/runtime/runtime.go            resolver setup, chroot, and exec
```

Run:

```bash
go test ./internal/rootfs ./internal/runtime
sudo go test -tags=integration ./internal/rootfs
```
