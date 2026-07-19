# Filesystem Design Spec — Containy Minimal Instance

This document describes the filesystem design for the minimal containy
instance: a foreground container runtime that imports Docker images, extracts
their layered archives into a flat rootfs, and chroots into it to run a
command.

## 1. Directory Layout

```
src/containy/
├── go.mod                          # module github.com/hxy9243/go-dojo/containy
├── go.sum
├── main.go                         # entry point
├── cmd/
│   └── cmd.go                      # CLI dispatch (run, import, help)
├── internal/
│   ├── docker/
│   │   └── docker.go               # docker CLI wrapper (save, inspect)
│   ├── rootfs/
│   │   ├── rootfs.go               # extract docker-save tar → flat rootfs
│   │   └── rootfs_test.go          # unit tests
│   └── runtime/
│       └── runtime.go              # chroot + exec workload
├── spec/
│   ├── overview.md                 # full MVP spec
│   └── filesystem.md              # this file
├── docs/
│   └── filesystem.md              # importing guide
└── ubuntu/                         # sample OCI image + rootfs.tar
```

## 2. Data Flow

```
docker image (e.g. ubuntu:latest)
  │
  │  docker save → tar archive (docker-save format)
  │  OR user provides a docker-save tar directly
  ▼
internal/docker.Import(imageRef) → dockerSaveTar (temp file)
  │
  │  internal/rootfs.Extract(dockerSaveTar, destDir)
  │  1. read manifest.json from the docker-save tar
  │  2. for each layer tar (in order):
  │     a. extract into destDir
  │     b. apply whiteouts (.wh.<name>, .wh..wh..opq)
  │  3. result: a flat rootfs directory
  ▼
internal/runtime.Run(rootfsDir, command, args...)
  │
  │  1. chroot(rootfsDir)
  │  2. chdir("/")
  │  3. exec(command, args...)
  ▼
workload runs inside chroot with inherited stdio
```

## 3. Docker Save Archive Format

A `docker save <image>` tar contains:

```
<image>.tar/
├── manifest.json          # array: [{ Config, RepoTags, Layers }]
├── <config-sha256>.json   # image config (history, rootfs diff_ids)
├── <repo>:<tag>/           # legacy tag directory
└── <layer-sha256>/         # one directory per layer
    └── layer.tar           # the actual layer tar
```

`manifest.json[0].Layers` is an ordered array of layer tar paths
(e.g. `["abc.../layer.tar", "def.../layer.tar"]`).

Each `layer.tar` is a standard tar with paths relative to `/` (e.g.
`bin/`, `etc/passwd`). Whiteout files are named `.wh.<name>` (delete
specific entry) or `.wh..wh..opq` (delete entire parent directory
contents).

## 4. Rootfs Extraction Algorithm

```
Extract(dockerSaveTar, destDir):
    open dockerSaveTar
    read manifest.json → layerPaths[]
    for each layerPath in layerPaths (base first):
        open layer.tar from dockerSaveTar
        for each entry in layer.tar:
            if entry.name matches .wh..wh..opq:
                remove all entries in entry.dir from destDir
                continue
            if entry.name matches .wh.<name>:
                remove destDir/entry.dir/<name>
                continue
            extract entry into destDir (preserving mode, symlinks)
    return destDir
```

Security: the minimal instance trusts the docker image (per spec §2, the
runtime is for trusted images only). Path traversal protection will be
added in the full MVP implementation (spec §6.2).

## 5. Chroot Execution

```
Run(rootfsDir, command, args):
    fork()
    in child:
        chroot(rootfsDir)
        chdir("/")
        exec(command, args...)  # inherits stdio
    in parent:
        wait(child)
        return exitCode
```

The minimal instance uses `chroot(2)` directly — no namespaces, no
pivot_root, no cgroups. This is the simplest isolation that lets us
verify the rootfs extraction and command execution pipeline. The full
MVP will replace this with `pivot_root` + namespaces (spec §6.3, §6.4).

## 6. CLI Interface

```
containy run <docker-image|docker-save-tar|rootfs-dir> <command> [args...]
containy import <docker-image> <output-tar>
containy help
```

### run

- If the first argument is a directory, use it directly as rootfs.
- If it's a tar file, extract it (docker-save format) to a temp dir.
- If it's an image name (no `/` and no `.tar`), call `docker save` first.
- Then chroot into the rootfs and exec the command.
- Runs in the foreground; stdio is inherited.
- Requires root (for chroot).

### import

- Calls `docker save <image> -o <output-tar>`.
- Wraps the docker CLI; requires docker daemon running.

## 7. Limitations of the Minimal Instance

- No PID/mount/UTS/IPC namespaces (uses chroot only).
- No cgroups.
- No signal forwarding.
- No cleanup stack (temp dirs are best-effort cleaned).
- No path traversal protection (trusted images only).
- No gzip support for docker-save tars (docker save produces uncompressed).
- Host network namespace is shared (same as full MVP).