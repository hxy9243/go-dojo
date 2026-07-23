# Preparing a Rootfs for Containy

Containy can run from an existing rootfs directory, a Docker-save archive, or
a local Docker image reference.

## Run a local Docker image

```bash
sudo containy run ubuntu:latest /bin/sh -c 'echo "pid=$$"'
```

For a value that is not an existing path, containy runs:

```bash
docker save ubuntu:latest -o <temporary-archive>
```

It then extracts the archive into a temporary flat rootfs. Cleanup removes the
archive and attempts to remove the rootfs after the container exits. The image
must already exist in the local Docker image store; containy does not pull it.

## Run a Docker-save archive

Create an archive explicitly:

```bash
docker pull ubuntu:latest
docker save ubuntu:latest -o ubuntu.tar
sudo containy run ./ubuntu.tar /bin/sh
```

The archive must be an uncompressed `docker save` tar containing
`manifest.json` and the referenced layer tars. A raw rootfs tar, `.tar.gz`,
and OCI image-layout directory are not currently accepted.

You can also create the archive through containy:

```bash
containy import ubuntu:latest ubuntu.tar
```

## Run an existing rootfs directory

```bash
sudo containy run ./rootfs /bin/sh
```

The directory must contain the requested executable and any dynamic loader and
libraries it requires. Unlike a temporary extracted rootfs, a supplied
directory is caller-owned and remains after the run.

Containy may modify it by:

- normalizing `/etc/resolv.conf`;
- creating bind-mount destination placeholders;
- creating missing runtime directory mountpoints such as `/proc`, `/dev`,
  `/run`, and `/tmp`.

Runtime mounts themselves are detached when the container exits.

## How layer extraction works

A Docker image is a sequence of filesystem changes rather than one rootfs tar.
Containy reads the ordered layer list from `manifest.json`, then applies each
layer into one directory:

```text
base layer
    + next layer
    + whiteout deletions
    + final layer
    = flat rootfs
```

It restores numeric UID/GID and mode metadata. Preserving numeric ownership is
important because image services may change to users such as `_apt` and rely
on the directory permissions encoded in the image.

## Resource and mount example

```bash
sudo containy run \
  --memory 128m \
  --cpu 0.5 \
  --pids 64 \
  -v "$PWD/data:/data:ro" \
  -it \
  ubuntu:latest \
  /bin/sh
```

## Security warning

Containy is an educational, rootful runtime. Its image extractor is not
hardened against malicious tar paths or unbounded archive sizes, and `chroot`
is not a production sandbox. Use only trusted images, archives, rootfs
directories, mount sources, and commands.

Implementation details:

- [System overview](../spec/overview.md)
- [Filesystem specification](../spec/filesystem.md)
- [Bind mounts](../spec/bind-mount.md)
- [Runtime directories](../spec/runtime-dir.md)
