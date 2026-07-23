# Containy Runtime Learnings & Technical Notes

---

### 1. Architectural Scope: Root-Only Execution
- **Root-Only Execution Model**: To simplify the container runtime architecture for educational/demo purposes, `containy` operates as a root-only container runtime (requiring `sudo`).
- **Standard Linux Namespaces**: Uses `CLONE_NEWNS | CLONE_NEWPID | CLONE_NEWUTS | CLONE_NEWIPC` directly.
- **Simplification**: Eliminates user namespace complexity (`CLONE_NEWUSER`), subordinate UID/GID mappings (`/etc/subuid`), and setuid helper dependencies (`newuidmap`/`newgidmap`). When running as root, standard `chroot`, `setgroups`, `seteuid`, `setegid`, and PTY operations work natively without kernel user-namespace restrictions.

---

### 2. OCI Image Extraction & Flat Rootfs
- **OCI Layering**: OCI/Docker images consist of ordered, layered tar archives. To construct a usable rootfs, layers must be extracted sequentially into a staging directory while handling whiteout files (`.wh.*`) to apply deletions correctly.

---

### 3. Interactive Shell & PTY Allocation (`-i`, `-t`, `-it`)
- **Interactive Mode (`-i`)**: Keeps standard input open so the container process receives input streams.
- **Pseudo-Terminal (`-t`)**:
  - Opened master/slave PTY using `/dev/ptmx` and Linux `ioctls` (`TIOCSPTLCK` to unlock slave, `TIOCGPTN` to query slave index `/dev/pts/<N>`).
  - Applied **Raw Mode** to host stdin via `golang.org/x/term` (`term.MakeRaw`) so special characters (`Tab`, `Ctrl+C`, arrow keys) pass directly to the container process.
  - Forwarded window resize events (`syscall.SIGWINCH`) to update PTY dimensions (`unix.IoctlSetWinsize`).

---

### 4. Child Process Controlling Terminal (`SysProcAttr.Ctty`)
- **Child FD Indexing**: In Go's `os/exec.Cmd`, `SysProcAttr.Ctty` expects the **0-indexed file descriptor number inside the child process**, NOT the host process file descriptor integer.
- **FD Mapping**: When `cmd.Stdin` is assigned to `slaveFile`, the PTY slave becomes FD `0` in the child. Setting `Setsid = true`, `Setctty = true`, and `Ctty = 0` establishes controlling terminal leadership correctly and avoids `Setctty set but Ctty not valid in child` errors.

---

### 5. Privilege Dropping & User Namespaces (Background Note)
- **Rootless User Namespace Rationale**: Unprivileged rootless user namespaces (`CLONE_NEWUSER`) map only UID/GID `0` by default (`Size: 1`), causing privilege-dropping commands (like `apt update` switching to `_apt` UID 42 or `nogroup` GID 65534) to fail with `EINVAL` (`Invalid argument`) and `setgroups` to fail with `EPERM`.
- **Root Mode Advantage**: By requiring host root privileges (`sudo containy run ...`), `containy` bypasses user namespace restrictions entirely, allowing commands like `apt update` to function without special flags.

---

### 6. Container DNS Resolution & `/etc/resolv.conf`
- **DNS Failure Cause**: OCI base images often contain an empty `/etc/resolv.conf` or a broken symlink to `/run/systemd/resolve/stub-resolv.conf`, causing `getaddrinfo` inside `chroot` to fail with `Temporary failure resolving...`.
- **Resolution Strategy**: Normalize `<rootfs>/etc/resolv.conf` to a regular file on container startup and populate it with host `/etc/resolv.conf` content or fallback public nameservers (`8.8.8.8` / `1.1.1.1`).
---

### 7. OCI Layer Ownership Metadata & Privilege-Dropping Services
- **Numeric IDs Are Filesystem Semantics**: OCI/Docker layer tar headers record numeric UID, GID, and mode values. Linux permission checks use those numeric IDs; names such as `_apt` are only resolved through `/etc/passwd`.
- **Extractor Requirement**: A rootful image extractor must restore each layer entry's numeric ownership and mode, use `lchown` for symlinks, and apply modes after `chown` because `chown` clears set-ID bits. This preserves the access model encoded by the image rather than adding application-specific ownership workarounds.
- **APT Example**: APT starts as root but deliberately downloads through its `_apt` helper (commonly UID `42`). If an APT state directory loses its image-defined ownership or permissions, APT falls back to an unsandboxed root download and emits a warning. This is a rootfs metadata issue, not a PID, UTS, IPC, or mount namespace configuration issue.
- **User Namespace Boundary**: Docker's optional user-namespace remapping translates container IDs to a subordinate host ID range while preserving the container-visible IDs. Containy's current rootful runtime intentionally does not use `CLONE_NEWUSER`, so it restores tar-header IDs directly on the host filesystem before chrooting.

---

### 8. Private Init Bootstrap

- **Re-execution boundary**: The supervisor starts `/proc/self/exe` in new namespaces and marks it as the private init path with `_CONTAINY_INTERNAL_INIT=1`.
- **Configuration transport**: Rootfs, command, and arguments are JSON-encoded into an anonymous Linux `memfd`, passed as the first `exec.Cmd.ExtraFiles` entry, and therefore inherited by the child as file descriptor `3`.
- **Why FD 3**: Descriptors `0`, `1`, and `2` are stdin, stdout, and stderr. Go begins extra child descriptors at `3`.
- **Lifecycle**: Private init reads and closes the bootstrap descriptor, prepares runtime filesystems, enters the rootfs, and replaces itself with the workload.

See [`spec/bootstrap.md`](spec/bootstrap.md) for the protocol and a comparison with Docker/containerd/runc.
