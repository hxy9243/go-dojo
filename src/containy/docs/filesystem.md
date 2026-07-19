# Importing Docker/OCI Images to a Rootfs Tar for Containy

## Problem

Containy's MVP accepts a **single flat rootfs tar archive** — not an OCI image
layout, not a `docker save` archive, and not a registry reference (see spec
§6.2 and §2 Non-goals). The input tar must contain a single Linux root
filesystem at archive root:

```
bin/        → symlink to usr/bin
etc/passwd
usr/bin/sh
usr/bin/bash
lib/        → symlink to usr/lib
...
```

However, Docker and OCI images are stored as **layered tar archives** with
metadata manifests. The `src/containy/ubuntu/` directory is an OCI image
layout (produced by `docker save` or `skopeo copy`) with this structure:

```
ubuntu/
├── index.json          # OCI index → points to manifest blob
├── manifest.json       # Docker save manifest (also present)
├── oci-layout          # "1.0.0"
├── repositories
├── ubuntu.tar          # the raw docker-save tar (blobs + metadata)
└── blobs/sha256/
    ├── d6b1a90...      # layer 1: the base Ubuntu rootfs (~111 MB)
    ├── d85193e...      # layer 2: .rock/metadata.yaml (rockcraft metadata)
    ├── de7345b...      # image config JSON
    └── 14de686...      # OCI manifest JSON
```

Each layer is an independent tar. To get a usable rootfs, you must **flatten
the layers in order**, applying whiteouts, and re-archive the merged result.

## Solution: `oci-to-rootfs.sh`

The script `src/containy/ubuntu/oci-to-rootfs.sh` converts an OCI image
layout (or Docker save directory) into the flat rootfs tar that containy
expects.

### Usage

```bash
./oci-to-rootfs.sh <oci-image-dir> <output-rootfs.tar>
```

### What it does

1. **Resolves the layer list** by reading either:
   - `manifest.json` (Docker save format — `.[0].Layers[]`), or
   - `index.json` → manifest blob → `.layers[].digest` (OCI layout).
2. **Extracts each layer tar in order** into a staging directory. Later
   layers overwrite earlier ones, matching the OCI layering semantics.
3. **Processes OCI whiteouts** (`.wh.<name>` and `.wh..wh..opq` entries)
   to remove files/directories deleted by upper layers. (The Ubuntu image
   has none, but the script handles them for generality.)
4. **Re-archives** the merged staging directory as a flat tar with relative
   paths (`bin/`, `etc/passwd`, etc.).

### Example

```bash
cd src/containy/ubuntu
./oci-to-rootfs.sh . rootfs.tar
# → Found 2 layer(s)
# → Merging layers into /tmp/tmp.XXX ...
# → Merged rootfs contains 6595 entries.
# → Creating rootfs.tar ...
# → Done. Output: rootfs.tar (100M)
```

### Verification

```bash
# Check key files exist
tar -tf rootfs.tar | grep -E 'etc/passwd$|usr/bin/sh$|usr/bin/bash$'

# Check symlinks are preserved
tar -tvf rootfs.tar | grep -E ' \./bin$| \./sbin$| \./lib$'
# lrwxrwxrwx ... ./bin -> usr/bin
# lrwxrwxrwx ... ./sbin -> usr/sbin
# lrwxrwxrwx ... ./lib -> usr/lib
```

## How Containy Uses the Rootfs Tar

Per spec §6.2, containy's `internal/rootfs` extractor will:

1. Accept the tar (or `.tar.gz`) as `<rootfs.tar>` on the CLI.
2. Extract it into `/var/lib/containy/containers/<id>/rootfs/`.
3. Enforce path-traversal protection (rejects `..`, absolute paths, symlink
   escapes, etc.).
4. Support directories, regular files, symlinks, and hard links.
5. Preserve permission bits and numeric UID/GID.
6. Reject sockets, FIFOs, device nodes, sparse files, and OCI whiteouts
   (`.wh.*`).

The init process then `pivot_root`s into this extracted rootfs (spec §6.4),
mounts `/proc` and `/dev`, and runs the workload command inside it.

## Running Containy with the Converted Rootfs

```bash
sudo containy run \
  --memory 128m \
  --cpu 0.5 \
  --pids 64 \
  --user 1000 \
  ./src/containy/ubuntu/rootfs.tar \
  /bin/sh -c 'echo "pid=$$ host=$(hostname)"'
```

## Alternative: Manual Conversion (No Script)

If you don't want to use the script, you can flatten layers manually:

```bash
# 1. Create a staging directory
mkdir /tmp/rootfs-staging && cd /tmp/rootfs-staging

# 2. Extract layers in order (base layer first)
tar -xf /path/to/layer1.tar
tar -xf /path/to/layer2.tar
# ... repeat for each layer

# 3. Remove any .wh.* whiteout markers
find . -name '.wh.*' -delete

# 4. Re-archive as a flat rootfs tar
tar -cf /path/to/rootfs.tar .
```

## Notes

- The `.rock/` directory in the Ubuntu image is rockcraft metadata and is
  harmless inside the rootfs. It won't interfere with containy.
- The output tar is uncompressed. Containy also accepts `.tar.gz` input (spec
  §4 CLI contract). To compress: `gzip rootfs.tar → rootfs.tar.gz`.
- The script requires `jq` for JSON parsing and standard `tar`/`find`.