# Specification: Containy MVP

`containy` is a small, Linux-only container runtime written in Go. The MVP is a
learning project for four container primitives:

1. PID, mount, UTS, and IPC namespaces
2. an isolated root filesystem using `pivot_root`
3. a PID 1 init process that forwards signals and reaps children
4. cgroups v2 CPU, memory, and process limits

The MVP deliberately uses the host network namespace. It is not a security
boundary and must not run untrusted images or commands.

## 1. Goals and acceptance criteria

The MVP is complete when a user can run:

```bash
sudo containy run \
  --memory 128m \
  --cpu 0.5 \
  --pids 64 \
  --user 1000 \
  ./rootfs.tar \
  /bin/sh -c 'echo "pid=$$ host=$(hostname)"'
```

and observe all of the following:

- the command sees itself beneath a namespace-local PID 1;
- `/` comes from the supplied rootfs and the old host root is unreachable;
- `/proc` describes the container PID namespace;
- the configured cgroup limits apply before the workload starts;
- the command runs as the requested UID/GID;
- stdin, stdout, stderr, signals, and exit status propagate correctly;
- orphaned descendants are reaped by the container init process;
- all temporary files and cgroup state are removed after success or failure;
- networking behaves exactly like an ordinary host process because no network
  namespace is created.

## 2. Non-goals and security statement

### NOT in scope for the MVP

- **Network isolation:** no `CLONE_NEWNET`, bridge, veth, IPAM, DNS rewriting,
  firewall rules, or port publishing. These are Milestone 2.
- **PTY/TTY support:** no `-t`; terminal raw mode and window resizing are
  deferred. The MVP supports direct stdio for non-interactive commands.
- **Bind volumes:** no `-v`; this avoids host-path and symlink-escape policy in
  the first milestone.
- **OCI/Docker images:** input is one unpacked-rootfs tar archive, not an OCI
  image layout, registry reference, or `docker save` archive.
- **Rootless execution:** no user namespace or subordinate UID/GID mapping.
- **Production sandboxing:** no seccomp profile, LSM profile, user namespace,
  masked paths, or production-grade capability policy.
- **Long-running container management:** no detached mode, persisted metadata,
  `list`, `stop`, `exec`, logs, restart policy, or daemon.
- **Distribution:** GitHub Releases and package-manager installation are
  deferred until the runtime passes its privileged integration suite.

### Security warning

The runtime must print this warning when invoked unless a future `--quiet`
policy explicitly changes it:

> containy is an educational runtime, not a secure sandbox. Run only trusted
> rootfs archives and trusted commands.

The parent runtime runs as root. Namespace and cgroup isolation alone do not
make hostile container code safe. In particular, the MVP intentionally does
not claim protection against kernel exploits or all privilege-escalation
paths.

## 3. Platform requirements

The MVP supports:

- Linux on `amd64` or `arm64`;
- kernel 5.7 or newer, for `clone3(CLONE_INTO_CGROUP)` placement;
- a unified cgroups v2 mount at `/sys/fs/cgroup`;
- `cpu`, `memory`, and `pids` controllers available to a root-owned
  `/sys/fs/cgroup/containy` subtree;
- root privileges, including the capabilities needed for namespaces, mounts,
  `pivot_root`, device creation, hostname changes, and cgroup administration;
- Go 1.25 or the repository's eventual pinned newer version.

Startup performs a read-only preflight before extracting an image. Unsupported
OS, insufficient privileges, missing cgroup v2, unavailable controllers, or an
unsupported kernel must fail with an actionable error and no created state.

## 4. CLI contract

```text
containy run [options] <rootfs.tar|rootfs.tar.gz> <command> [args...]
```

The public MVP contains only `run`. `init` and `exec-child`, if used for
re-execution, are hidden internal commands and must reject direct invocation
unless a private inherited file descriptor and versioned bootstrap payload are
present.

### 4.1 Supported options

| Option | Required | Default | Contract |
| --- | --- | --- | --- |
| `-u`, `--user UID|USER` | no | `0` | Resolve a numeric UID or username from the rootfs. |
| `-m`, `--memory SIZE` | no | `max` | Set `memory.max`; suffixes `k`, `m`, and `g` use powers of 1024. |
| `--cpu CORES` | no | `max` | Positive decimal CPU allowance; write quota with a 100000 µs period. |
| `--pids COUNT` | no | `256` | Positive integer written to `pids.max`. |
| `--help` | no | — | Print usage without requiring root. |

Removed from the MVP: `--net`, `--publish`, `--volume`, `--interactive`, and
`--tty`. Unknown flags are errors. Flag parsing stops at `<rootfs>` so command
flags are passed through unchanged. A literal `--` may be used before the
rootfs for clarity.

### 4.2 Input validation

- Exactly one rootfs and at least one command argument are required.
- The archive must be a regular file and must not be a symlink.
- Memory values must be greater than zero and must not overflow `uint64`.
- CPU values must be finite and greater than zero; quota is rounded up to the
  nearest microsecond and must be at least 1000 µs.
- PID limits must be in the range `1..4194304`.
- Usernames must be non-empty and contain no colon or newline.
- Usage errors exit `2`; environment/setup errors exit `125`; command-not-found
  exits `127`; command-not-executable exits `126`.

### 4.3 Workload defaults

- Working directory: `/`
- Umask: `0022`
- Environment: a deterministic `PATH`, plus `HOME`, `USER`, and `LOGNAME` from
  the selected passwd entry when available; the MVP does not copy arbitrary
  host environment variables.
- Network: host network namespace, with the host's interfaces, routes, DNS,
  loopback, and port space visible unchanged.
- Stdio: file descriptors 0, 1, and 2 are inherited directly. Interactive TTY
  behavior is not guaranteed until PTY support is implemented.

## 5. Runtime architecture

The runtime has two long-lived roles: the host supervisor and the container
init. The workload is a child of init rather than replacing init.

```text
Host PID namespace                         Container PID namespace
==================                         =======================

containy supervisor
  │
  ├─ validate + extract rootfs
  ├─ create/configure cgroup
  │
  └─ clone init into cgroup ─────────────► containy init (PID 1)
       NEWPID | NEWNS | NEWUTS | NEWIPC      │
                                              ├─ private mounts
                                              ├─ pivot_root + /proc + /dev
                                              ├─ resolve/drop credentials
                                              └─ workload (PID 2+)
                                                   └─ descendants

supervisor ◄──── status / exit code ─────── init reaps all children
```

### 5.1 Component boundaries

The implementation should remain small and explicit:

- `cmd/containy`: CLI dispatch and exit-code mapping
- `internal/runtime`: host supervisor, bootstrap payload, and cleanup stack
- `internal/init`: namespace initialization, PID 1 loop, and signal forwarding
- `internal/rootfs`: secure archive extraction and pivot-root mounts
- `internal/cgroup`: v2 preflight, limit parsing, creation, and cleanup
- `internal/user`: passwd/group parsing and credential selection

Avoid a plugin system, generic resource interfaces, or daemon abstractions in
the MVP. Each package owns one Linux lifecycle boundary and returns wrapped,
operation-specific errors.

### 5.2 Bootstrap protocol

The supervisor re-executes `/proc/self/exe` as the hidden `init` command using
`os/exec`. Configuration is encoded as a versioned JSON bootstrap document and
passed through an inherited, read-only pipe rather than environment variables
or public command-line flags.

The payload contains only:

- schema version and container ID;
- absolute rootfs path;
- command and arguments;
- selected user string;
- deterministic workload environment.

The init command validates the schema, verifies that the bootstrap FD exists,
closes all inherited non-stdio/bootstrap descriptors, and rejects malformed or
oversized payloads. The maximum bootstrap size is 64 KiB.

## 6. Feature system specifications

### 6.1 Container identity and state

The supervisor generates a cryptographically random 12-byte hexadecimal ID.
State lives at:

```text
/var/lib/containy/containers/<id>/
└── rootfs/
```

Requirements:

- `/var/lib/containy` and container directories use mode `0700` and root
  ownership;
- creation uses `mkdir`, never a predictable temporary-name race;
- an ID collision retries with a new ID;
- the supervisor records completed cleanup actions in memory, not a persistent
  database;
- every acquired resource immediately registers an idempotent reverse-order
  cleanup action.

### 6.2 Rootfs archive extraction

The accepted image format is a tar or gzip-compressed tar containing a single
Linux root filesystem at archive root. It must contain the requested command
and all dynamic libraries that command requires.

The extractor supports directories, regular files, symlinks, and hard links.
It preserves permission bits and numeric UID/GID. It ignores timestamps beyond
what is needed for ordinary file behavior and rejects sockets, FIFOs, device
nodes, sparse files, unknown entry types, and OCI whiteouts.

Security and correctness requirements:

- reject absolute entry names, empty names, NUL bytes, and any cleaned path
  that escapes with `..`;
- never follow an archive-created symlink while creating a later entry;
- validate symlink and hard-link targets as remaining inside the future
  container root;
- create regular files with `O_NOFOLLOW|O_CREAT|O_EXCL` semantics;
- apply ownership to symlinks without following them;
- cap entries at 100,000 and total extracted regular-file bytes at 4 GiB;
- remove the entire container directory after any extraction failure;
- call `fsync` only where needed for correctness; the MVP does not promise
  crash-persistent extracted images.

This protection is mandatory even though images are documented as trusted:
archive path traversal could otherwise overwrite arbitrary host files while
the runtime is root.

### 6.3 Namespace creation and cgroup placement

The supervisor launches init with:

```text
CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWUTS | CLONE_NEWIPC
```

It must not set `CLONE_NEWNET` or `CLONE_NEWUSER` in the MVP.

The cgroup directory is opened as an FD and supplied through Go's
`SysProcAttr.UseCgroupFD` and `CgroupFD`. This requests
`clone3(CLONE_INTO_CGROUP)`, ensuring init and every descendant are constrained
before user workload code can execute. Failure to use atomic placement is a
hard startup error; the MVP has no racy attach-after-start fallback.

Init sets its hostname to `containy-<first-12-id-chars>`. The hostname is not a
public option in the MVP.

### 6.4 Mount namespace and `pivot_root`

Init performs mount setup in this order:

1. Make `/` recursively private with `MS_REC|MS_PRIVATE`.
2. Bind-mount the prepared rootfs onto itself so it is a mount point.
3. Create `<rootfs>/.containy-old-root` with mode `0700`.
4. Call `pivot_root(rootfs, rootfs/.containy-old-root)`.
5. Change directory to `/`.
6. Detach `/.containy-old-root` with `MNT_DETACH` and remove it.
7. Mount a new `proc` at `/proc` with `nosuid,nodev,noexec`.
8. Mount `tmpfs` at `/dev` with `nosuid`, mode `0755`, and a bounded size.
9. Create `/dev/null`, `/dev/zero`, `/dev/random`, and `/dev/urandom` with the
   standard character-device major/minor numbers and permissions.
10. Create `/dev/fd`, `/dev/stdin`, `/dev/stdout`, and `/dev/stderr` symlinks
    into `/proc/self/fd`.
11. Mount `tmpfs` at `/dev/shm` with `nosuid,nodev`, mode `1777`, and a bounded
    size.

The MVP does not mount `/sys`, the host cgroup tree, `devpts`, or host devices.
All mount targets must be created without traversing symlinks. Any failure
aborts before the workload starts; namespace destruction releases mounts, and
the supervisor then deletes the extracted rootfs.

### 6.5 User and group selection

User resolution reads `/etc/passwd` and `/etc/group` from the extracted rootfs
before `pivot_root`, using parsers that reject malformed numeric fields and
overlong lines.

Rules:

- a username must exist in `/etc/passwd`;
- a numeric UID uses the matching passwd entry's primary GID when present;
- a numeric UID with no passwd entry uses the same numeric value as its GID;
- supplementary groups are all valid `/etc/group` entries containing the
  selected username;
- default credentials are UID 0, GID 0, with no supplementary groups;
- init completes all privileged mount operations, then applies groups, GID,
  and UID before it starts the workload;
- failure to set any credential is fatal.

Because init drops to the workload credentials before forking it, PID 1 and the
workload share credentials. No privileged helper remains inside the container
namespace.

### 6.6 PID 1, process reaping, and signals

Init must remain PID 1 for the lifetime of the container. It starts the
workload as a child and runs a wait loop until all descendants have exited.

Requirements:

- resolve command paths against the container's deterministic `PATH`, never
  the host's `PATH`;
- start the workload in its own process group;
- forward `SIGINT`, `SIGTERM`, `SIGHUP`, `SIGQUIT`, `SIGUSR1`, `SIGUSR2`, and
  `SIGWINCH` to the workload process group;
- reap any child returned by `wait4`, including adopted orphans;
- after the main workload exits, send `SIGTERM` to its remaining process group,
  wait up to two seconds, then send `SIGKILL` and finish reaping;
- report the main workload's exit status to the supervisor only after
  descendants have been handled;
- if init receives a second termination signal during shutdown, skip directly
  to `SIGKILL`;
- if init fails before starting the workload, emit a stage-qualified message
  and exit `125`.

The supervisor forwards non-terminal signals addressed specifically to it to
init. Signal registration is installed before init is started. The supervisor
returns the workload's exit code, or `128 + signal` when it was terminated by
a signal.

### 6.7 cgroups v2

The host-side cgroup layout is:

```text
/sys/fs/cgroup/containy/                  # controller-owning internal node
└── <container-id>/                       # leaf containing init + workload
```

Preflight and creation requirements:

- verify `/sys/fs/cgroup` is `cgroup2fs`;
- create the `containy` parent with no resident processes;
- verify `cpu`, `memory`, and `pids` appear in the parent's available
  controllers;
- idempotently enable `+cpu +memory +pids` in the parent's
  `cgroup.subtree_control`;
- never modify the root cgroup's controller configuration;
- reject an existing container leaf rather than reusing it.

Limit mapping:

| CLI input | cgroup file | Value |
| --- | --- | --- |
| no `--memory` | `memory.max` | `max` |
| `--memory 128m` | `memory.max` | `134217728` |
| any memory limit | `memory.oom.group` | `1`, when supported |
| no `--cpu` | `cpu.max` | `max 100000` |
| `--cpu 0.5` | `cpu.max` | `50000 100000` |
| `--pids 64` | `pids.max` | `64` |

Write all limits before cloning init. If any write fails, remove the empty leaf
and abort. On exit, read `memory.events`; if `oom_kill` increased, print a clear
diagnostic without changing the kernel-derived workload exit code.

Cleanup first relies on PID namespace shutdown to terminate descendants. If
the cgroup remains populated, write `1` to `cgroup.kill` when available, wait
briefly for `cgroup.events` to report `populated 0`, and remove the leaf. Never
remove the shared `containy` parent during ordinary container cleanup.

### 6.8 Host networking

The MVP creates no network namespace and mutates no network state.

Consequences are part of the public contract:

- container processes see host interfaces and routes;
- `127.0.0.1` refers to the host loopback namespace;
- services bind directly to host addresses and ports;
- port collisions behave like ordinary host-process collisions;
- `/etc/resolv.conf` comes from the rootfs archive; containy does not rewrite
  DNS configuration;
- `--publish` is unnecessary and rejected as an unknown option.

This mode is easy to demonstrate but provides zero network isolation.

### 6.9 Lifecycle and cleanup

The supervisor owns a reverse-order cleanup stack:

```text
validate
  -> create state directory
  -> extract rootfs
  -> create cgroup leaf
  -> start init
  -> wait
  -> remove cgroup leaf
  -> remove state directory
```

Each cleanup operation must be safe to call more than once. Cleanup runs on
normal exit, setup failure, workload failure, and handled termination signals.
Errors are aggregated and printed after all cleanup actions have been attempted;
cleanup errors do not replace a nonzero workload status.

The MVP does not promise automatic recovery after `SIGKILL`, power loss, or a
kernel panic. On every later invocation, startup scans container directories
and cgroup leaves older than 24 hours and reports them as stale; automatic
deletion is deferred until persistent state can distinguish abandoned and
active containers safely.

## 7. Error model and observability

All errors use this shape on stderr:

```text
containy: <stage>: <operation>: <cause>
```

Examples:

```text
containy: preflight: cgroup controller "memory" is unavailable
containy: extract: entry "../../etc/shadow": path escapes rootfs
containy: init: mount /proc: operation not permitted
containy: exec: /bin/app: no such file or directory
```

No debug logger is required for the MVP. Errors must include the container ID
after it has been allocated. Secrets and the full inherited environment must
never be logged.

## 8. Implementation milestones

Each milestone ends in a runnable vertical slice and its own tests.

### Milestone 0: scaffolding and pure validation

- Initialize the Go module and Linux build constraints.
- Implement CLI parsing, limit parsing, exit-code constants, bootstrap schema,
  and platform preflight.
- Implement the secure rootfs extractor and passwd/group parsers.
- Add unit and archive-adversarial tests.

### Milestone 1: filesystem container

- Implement re-exec into mount, UTS, and IPC namespaces.
- Make mounts private, perform `pivot_root`, and mount `/proc`, `/dev`, and
  `/dev/shm`.
- Run one foreground workload with direct stdio.
- Verify the host root cannot be reached and all mounts disappear on exit.

### Milestone 2: PID 1 lifecycle

- Add the PID namespace and persistent init shim.
- Add workload process groups, signal forwarding, orphan reaping, shutdown
  escalation, and exact exit-status propagation.
- Verify zombie-free operation and termination behavior.

### Milestone 3: cgroups and credentials

- Add cgroup v2 preflight and atomic clone-into-cgroup placement.
- Add CPU, memory, OOM-group, and PID limits.
- Add rootfs user/group resolution and credential dropping.
- Verify limits are active before workload code executes.

### Milestone 4: hardening and release gate

- Make every failure path cleanup-safe and idempotent.
- Add concurrent-run and stale-state reporting tests.
- Run the complete privileged integration suite on `amd64` and `arm64` Linux.
- Document build, local installation, host impact, and security limitations.

## 9. Test specification

Go's standard `testing` package is the test framework. Unit tests run as an
ordinary user; privileged integration tests use a separate build tag and must
run in a disposable Linux VM or dedicated CI runner.

```text
TEST COVERAGE MAP
=================

CLI and parsers
  ├─ valid/invalid memory, CPU, PID, user, and argument boundaries
  ├─ overflow, NaN/Inf, zero, negative, missing command
  └─ exact usage and exit codes

Rootfs extraction
  ├─ regular files, directories, symlinks, hard links, ownership, modes
  ├─ absolute paths, ../ traversal, symlink-parent escape, hard-link escape
  ├─ duplicate entries, unsupported types, truncated gzip/tar
  └─ entry/byte limits and cleanup after partial extraction

Namespace + filesystem integration [root]
  ├─ PID, mount, UTS, IPC isolated; network namespace unchanged
  ├─ /proc shows namespace PIDs; old root inaccessible
  ├─ /dev nodes and /dev/shm usable
  └─ mount failure leaves no state

Init lifecycle integration [root]
  ├─ exit 0/nonzero/not-found/not-executable/signaled
  ├─ SIGINT/SIGTERM forwarding to workload process group
  ├─ orphan adoption and zombie reaping
  └─ TERM timeout followed by KILL

Cgroup integration [root]
  ├─ init starts in expected leaf
  ├─ memory.max, cpu.max, pids.max active before workload
  ├─ OOM and fork-limit behavior diagnosed
  └─ leaf removed after success, failure, and signal

Credential integration [root]
  ├─ username, known numeric UID, unknown numeric UID
  ├─ primary and supplementary groups
  └─ malformed passwd/group and failed credential change

Lifecycle integration [root]
  ├─ concurrent containers receive unique state and cgroups
  ├─ every injected setup failure runs complete reverse cleanup
  └─ stale state is reported but active state is untouched
```

### 9.1 Test fixtures

Integration tests build a tiny static Go helper and construct rootfs archives
programmatically. The suite must not depend on a host BusyBox installation,
Docker daemon, network access, or mutable external image.

### 9.2 Required commands

```bash
go test ./...
go test -race ./...
sudo go test -tags=integration ./...
go vet ./...
```

Privileged tests must fail with a clear prerequisite message when explicitly
requested on an unsupported host; they must not silently pass by skipping the
entire integration suite.

## 10. Failure-mode checklist

| Path | Realistic failure | Required handling | Required test |
| --- | --- | --- | --- |
| Preflight | cgroup v1 or missing controller | Fail before state creation | Yes |
| Extraction | tar path/symlink escape | Reject entry and delete partial rootfs | Yes |
| Namespace clone | clone3 blocked/unsupported | Actionable exit 125; no fallback race | Yes |
| Pivot root | shared mount propagation | Private propagation first; report stage | Yes |
| Device setup | missing `CAP_MKNOD` | Abort before workload; cleanup | Yes |
| User lookup | username absent | Clear error; do not fall back to root | Yes |
| Workload start | missing dynamic linker | Exit 127/126 with container path | Yes |
| Signal forwarding | child creates descendants | Signal process group; reap all | Yes |
| Memory limit | cgroup OOM kill | Preserve status and print OOM diagnostic | Yes |
| PID limit | fork returns `EAGAIN` | Kernel enforcement; cleanup normally | Yes |
| Cleanup | lingering process in cgroup | Namespace shutdown, `cgroup.kill`, bounded wait | Yes |
| Concurrency | duplicate ID/state | Cryptographic ID and collision retry | Yes |

No listed failure may be silent.

## 11. Future features, in recommended order

### Next: isolated bridge networking

Add `--net=bridge|host|none`, bridge creation, veth movement, locked IPAM state,
DNS configuration, forwarding policy, and transactional nftables-owned rules.
Port publishing belongs in the same milestone because it depends on the bridge
data plane. Do not add an automatic user-space proxy fallback; one explicit
network backend is easier to reason about and clean up.

### Then: practical container ergonomics

- `-t`/`-i` PTY support, controlling terminal, raw mode, and `SIGWINCH`
- bind volumes with `ro|rw`, file-vs-directory validation, and symlink-safe
  destination resolution
- `--env`, `--workdir`, `--hostname`, read-only rootfs, and tmpfs mounts
- cached extracted rootfs layers to avoid repeated archive expansion

### Later: stronger isolation

- user namespaces and rootless execution
- explicit capability allowlist and bounding-set removal
- `no_new_privs`, seccomp, masked paths, and read-only `/sys`
- cgroup namespace and optional time namespace
- resource additions such as `memory.swap.max`, I/O limits, and CPU weights

### Later: image and lifecycle compatibility

- OCI image layout and registry pulls with digest verification
- overlayfs snapshots and layer sharing
- detached containers, persisted state, `list`, `stop`, `exec`, `rm`, and logs
- metrics/stats and structured events
- reproducible release binaries and signed checksums

## 12. What already exists

There is currently no containy implementation to reuse. The repository contains
unrelated Go exercises, but no namespace, cgroup, archive, init, or container
lifecycle package. The implementation should use the Go standard library where
possible and `golang.org/x/sys/unix` for Linux interfaces not suitably exposed
by portable packages. PTY and netlink dependencies are unnecessary in the MVP.

## 13. Reference behavior

The implementation should be checked against the Linux kernel's cgroups v2
documentation, the Linux `pivot_root(2)`, namespaces, PID namespaces,
capabilities documentation, and Go's Linux `SysProcAttr` behavior. These are
behavioral references, not a commitment to Docker or OCI CLI compatibility.
