# Containy System Overview

Containy is a small, Linux-only container runtime written in Go. It is an
educational implementation of image extraction, namespaces, chroot, virtual
runtime filesystems, bind mounts, PTYs, and cgroups v2.

It is not a secure sandbox. Run it only with trusted images, rootfs
directories, mount sources, and commands.

## 1. Implemented system

Containy currently provides:

- foreground container execution as host root;
- Docker-save archive extraction into a flat rootfs;
- direct use of an existing rootfs directory;
- conversion of a local Docker image to a temporary Docker-save archive;
- mount, PID, UTS, and IPC namespaces;
- host networking, with no network namespace;
- cgroups v2 memory, CPU, and PID limits;
- private `/proc`, `/dev`, `/dev/shm`, and `/run` mounts;
- bind-mounted host files and directories;
- inherited stdio or an allocated PTY;
- supervisor signal forwarding to the workload process group;
- an anonymous JSON bootstrap passed to a re-executed private init process.

The implementation uses `chroot`, not `pivot_root`. The private init process
sets up the runtime filesystems and then replaces itself with the workload via
`exec`. The workload consequently becomes PID 1 in the new PID namespace.
Containy does not yet keep a separate PID 1 process to forward signals or reap
orphaned descendants.

## 2. CLI

```text
containy run [options] <image|docker-save.tar|rootfs-dir> <command> [args...]
containy import <docker-image> <output-tar>
containy help
```

`run` accepts:

| Option | Default | Meaning |
| --- | --- | --- |
| `-i`, `--interactive` | off | Copy host stdin to the workload. |
| `-t`, `--tty` | off | Allocate a PTY and forward window-size changes. |
| `-it`, `-ti` | off | Enable both interactive input and a PTY. |
| `-v`, `--volume HOST:DEST[:ro\|rw]` | — | Add a bind mount; repeatable. |
| `-m`, `--memory SIZE` | unlimited | Set `memory.max`; `k`, `m`, and `g` are powers of 1024. |
| `--cpu CORES` | unlimited | Set a CPU quota using a 100000 µs period. |
| `--pids COUNT` | `256` | Set `pids.max`; valid range is `1..4194304`. |

Input selection works as follows:

- an existing directory is used directly;
- an existing regular file is treated as an uncompressed Docker-save tar;
- a missing value that does not look like an archive path is treated as a
  Docker image reference and passed to `docker save`;
- `containy import` explicitly runs `docker save` without starting a
  container.

Docker must already know the referenced image; containy does not pull from a
registry. Gzip-compressed archives and raw flat-rootfs tars are not accepted by
the current extractor.

## 3. End-to-end startup

```text
CLI
 │
 ├─ parse flags and resource limits
 ├─ use rootfs directory or flatten Docker-save layers into a temp directory
 ├─ normalize rootfs /etc/resolv.conf
 ├─ apply requested bind mounts to paths beneath the rootfs
 ├─ create /sys/fs/cgroup/containy/<random-id>
 ├─ write memory.max, cpu.max, and pids.max
 ├─ encode {rootfs, command, args} as JSON in an anonymous memfd
 │
 └─ exec /proc/self/exe in NEWNS | NEWPID | NEWUTS | NEWIPC
      │  process is placed atomically in the cgroup with CLONE_INTO_CGROUP
      │
      └─ private init (PID 1)
           ├─ read JSON from inherited FD 3 and close it
           ├─ make mount propagation private
           ├─ mount /proc, /dev, /dev/shm, and /run beneath the rootfs
           ├─ create minimal device nodes and compatibility symlinks
           ├─ chroot into the rootfs and chdir("/")
           ├─ install a deterministic workload environment
           └─ exec the workload, replacing private init

supervisor waits
 ├─ report an observed cgroup OOM kill
 ├─ kill any processes left in the cgroup
 ├─ remove the cgroup leaf
 ├─ unmount host-side bind mounts
 └─ attempt to remove a temporary extracted rootfs, when one was created
```

The bootstrap is described in detail in
[`bootstrap.md`](bootstrap.md).

## 4. Process and namespace model

```text
Host PID namespace                       Container PID namespace
==================                       =======================

containy supervisor
  │
  └─ re-exec with namespace flags ─────► workload (PID 1)
       waits for exit                       │
                                           └─ workload descendants
```

The re-executed process briefly runs containy's private initialization code,
but `unix.Exec` replaces it with the requested command. This keeps the startup
path small, but has important consequences:

- the workload must tolerate running as PID 1;
- there is no independent init loop to reap adopted orphans;
- the supervisor forwards common signals to the workload process group and
  escalates termination signals to `SIGKILL` after two seconds;
- when PID 1 exits, the kernel tears down the PID namespace and remaining
  processes are removed during namespace/cgroup cleanup.

The runtime creates no user or network namespace. Container processes run with
host-root credentials and share host interfaces, routes, loopback, and port
space.

## 5. Filesystem model

### Image rootfs

The extractor reads `manifest.json` from a Docker-save archive, loads the
referenced layer tars, and applies them in order. It handles Docker/OCI
whiteouts and restores file modes plus numeric UID/GID.

The extractor is intentionally limited and currently trusts the archive. It
does not provide complete protection against archive path or symlink
traversal, does not bound archive size, and buffers layer contents in memory.
This is one reason containy must not process untrusted images.

### Runtime filesystems

The namespace init creates:

```text
/
├── proc/                 procfs, nosuid/nodev/noexec
├── dev/                  bounded tmpfs
│   ├── null
│   ├── zero
│   ├── random
│   ├── urandom
│   ├── fd -> /proc/self/fd
│   └── shm/              bounded tmpfs, mode 1777
├── run/                  bounded tmpfs, nosuid/nodev/noexec
│   └── lock/             mode 1777
├── var/run -> ../run     created only when absent
└── tmp/                  directory, mode 1777
```

`/sys`, `/sys/fs/cgroup`, `devpts`, and host device trees are not mounted into
the container.

Runtime filesystem target creation rejects symlink traversal. Bind mount
targets also reject existing symlinks. The runtime reserves `/`, `/proc`,
`/dev`, `/run`, `/tmp`, and `/var` where a user bind would replace or be
modified by runtime setup. These checks reduce accidental host-path traversal,
but they are not a substitute for a production-grade `openat2`-based path
resolver.

## 6. Resource limits

The supervisor requires a unified cgroups v2 hierarchy at `/sys/fs/cgroup`
with the `cpu`, `memory`, and `pids` controllers available to:

```text
/sys/fs/cgroup/containy/
└── <24-hex-character-id>/
```

Before starting the namespace init, it writes:

| CLI input | cgroup file | Written value |
| --- | --- | --- |
| no memory limit | `memory.max` | `max` |
| `--memory 128m` | `memory.max` | `134217728` |
| memory limit set | `memory.oom.group` | `1`, when supported |
| no CPU limit | `cpu.max` | `max 100000` |
| `--cpu 0.5` | `cpu.max` | `50000 100000` |
| `--pids 64` | `pids.max` | `64` |

The open cgroup directory FD is supplied through
`SysProcAttr.UseCgroupFD`/`CgroupFD`, placing the child in the leaf during
`clone3` rather than attaching it after startup.

## 7. Package boundaries

```text
main.go
└── cmd/
    ├── CLI parsing
    ├── image/rootfs preparation
    └── resource-limit parsing

internal/
├── docker/     docker save wrapper
├── rootfs/     Docker-save extraction and runtime filesystem setup
├── runtime/    supervisor, namespace bootstrap, chroot, PTY, and bind mounts
└── cgroup/     cgroups v2 creation, limits, OOM observation, and cleanup
```

There is no daemon, persisted container state, detached mode, or OCI runtime
API. Each invocation owns one foreground container lifecycle.

## 8. Failure and cleanup behavior

- Setup errors are wrapped with the operation that failed.
- Partially applied bind mounts and runtime mounts are unmounted in reverse
  order.
- Cgroup cleanup uses `cgroup.kill` when processes remain, waits up to two
  seconds for the leaf to empty, and removes the leaf.
- Temporary image archives are removed with errors reported; extracted rootfs
  cleanup is best-effort.
- OOM detection produces a warning after the workload exits.
- The CLI currently maps all returned errors to process exit code `1`; exact
  workload exit-code propagation is not implemented.

`SIGKILL`, power loss, or a crash can leave host-side state behind. There is no
startup reconciliation for stale mounts or cgroups.

## 9. Verification

Normal development checks:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Mount, namespace, cgroup, and real-image behavior require root privileges and
an appropriate Linux host. The integration-tagged extraction test uses the
large Ubuntu fixture when present:

```bash
sudo go test -tags=integration ./internal/rootfs
```

## 10. Documentation map

- [`bootstrap.md`](bootstrap.md): supervisor-to-init JSON/memfd protocol and
  Docker/runc comparison.
- [`filesystem.md`](filesystem.md): Docker-save extraction and chroot details.
- [`runtime-dir.md`](runtime-dir.md): `/proc`, `/dev`, `/run`, and `/tmp`.
- [`bind-mount.md`](bind-mount.md): host bind-mount behavior and constraints.
- [`../docs/filesystem.md`](../docs/filesystem.md): user-facing image/rootfs
  preparation guide.
- [`../notes.md`](../notes.md): implementation learnings and background notes.

## 11. Explicit non-goals

The current system does not provide:

- a production security boundary;
- rootless or non-root workload credentials;
- network isolation or port publishing;
- a persistent PID 1 reaper and signal-forwarding loop;
- `pivot_root`;
- seccomp, LSM policy, capability filtering, or `no_new_privs`;
- detached containers, restart policy, logs, `exec`, or state recovery;
- registry pulls, OCI image-layout ingestion, or overlayfs snapshots;
- exact Docker or OCI CLI/runtime compatibility.
